// Package agent runs one conversational turn: it drives `claude -p` (which
// owns the tool-calling loop), translates its stream-json events into
// onyx-style packets and passes answer text through the citation processor.
package agent

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"text/template"
	"time"

	"roamind.ai/agent/citation"
	"roamind.ai/agent/claude"
	"roamind.ai/agent/protocol"
	"roamind.ai/memory/store/notes"
)

//go:embed prompts/system.md
var systemPromptTmpl string

var systemPrompt = template.Must(template.New("system").Parse(systemPromptTmpl))

// Config is the process-wide agent configuration.
type Config struct {
	// DataDir holds per-user workspaces (DataDir/users/<user_id>/workspace).
	DataDir string
	// ClaudeBinary defaults to "claude".
	ClaudeBinary string
	Model        string
	// Memory enables long-term memory for turns that name a project.
	Memory *notes.Notes
	// ToolsServer, when set, is the command of Roamind's MCP server (bin/mcp),
	// whose tools keep the user's data, such as their news interests; each
	// turn appends --user <id> and --tools with its ServerTools. Turns with
	// no ServerTools don't get it.
	ToolsServer []string
	// BlockedDomains are domains, with their subdomains, that ToolsWeb
	// turns never fetch.
	BlockedDomains []string
}

// Emit receives packets as the turn progresses.
type Emit func(protocol.Packet)

// RunTurn runs one turn for in.UserID. When in.SessionID cannot be resumed the
// turn is retried in a fresh session and the output is marked SessionReset.
func RunTurn(ctx context.Context, cfg Config, in protocol.TurnInput, emit Emit) (protocol.TurnOutput, error) {
	if in.UserID == "" {
		return protocol.TurnOutput{}, errors.New("agent: empty user id")
	}
	if in.Tools != protocol.ToolsWorkspace && in.Tools != protocol.ToolsWeb && in.Tools != protocol.ToolsNone {
		return protocol.TurnOutput{}, fmt.Errorf("agent: unknown tool set %q", in.Tools)
	}
	var dir string
	var err error
	if in.Tools == protocol.ToolsNone {
		// A throwaway directory: the turn can't touch the workspace, and its
		// session, which is not saved anyway, is not kept with the user's.
		if dir, err = os.MkdirTemp("", "roamind-turn-"); err != nil {
			return protocol.TurnOutput{}, err
		}
		defer os.RemoveAll(dir)
	} else {
		if dir, err = WorkspaceDir(cfg, in.UserID); err != nil {
			return protocol.TurnOutput{}, err
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return protocol.TurnOutput{}, err
		}
	}
	workspace := in.Tools == protocol.ToolsWorkspace
	withMemory := workspace && cfg.Memory != nil && in.MemoryProject != ""
	withTools := workspace && len(cfg.ToolsServer) > 0 && len(in.ServerTools) > 0
	var sp bytes.Buffer
	err = systemPrompt.Execute(&sp, map[string]any{
		"UserName":  orDefault(in.UserName, "the user"),
		"UserEmail": in.UserEmail,
		"Date":      time.Now().Format("Monday, 2 January 2006"),
		"ISODate":   time.Now().Format("2006-01-02"),
		"Memory":    withMemory,
		"Goal":      in.Goal,
		"Exercise":  exercise(in),
		"News":      in.News,
		"Interests": withTools && slices.Contains(in.ServerTools, protocol.ServerInterests),
		"Tools":     orDefault(in.Tools, "workspace"),
		"Channel":   orDefault(in.Channel, "slack"),
	})
	if err != nil {
		return protocol.TurnOutput{}, err
	}
	opts := claude.Options{
		Binary:       cfg.ClaudeBinary,
		Dir:          dir,
		SessionID:    in.SessionID,
		Model:        cfg.Model,
		SystemPrompt: sp.String(),
		JSONSchema:   in.OutputSchema,
	}
	switch in.Tools {
	case protocol.ToolsWorkspace:
		opts.Tools, opts.Settings, opts.AllowedTools = workspaceTools, workspaceSettings, allowedWorkspaceTools(dir)
	case protocol.ToolsWeb:
		opts.Tools, opts.Settings, opts.AllowedTools = webTools, webSettings(cfg.BlockedDomains), webTools
	case protocol.ToolsNone:
		opts.NoSessionPersistence = true
	}
	servers, allowed, disallowed, err := mcpServers(cfg, in, withMemory, withTools)
	if err != nil {
		return protocol.TurnOutput{}, err
	}
	opts.AllowedTools = append(opts.AllowedTools, allowed...)
	opts.DisallowedTools = disallowed
	if len(servers) > 0 {
		b, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return protocol.TurnOutput{}, err
		}
		opts.MCPConfig = string(b)
	}

	out, err := runOnce(ctx, opts, in.Text, emit)
	if errors.Is(err, claude.ErrSessionNotFound) {
		opts.SessionID = ""
		out, err = runOnce(ctx, opts, in.Text, emit)
		out.SessionReset = true
	}
	if err != nil {
		emit(protocol.Packet{Type: protocol.PacketError, Error: err.Error()})
		return out, err
	}
	return out, nil
}

// WorkspaceDir is the user's workspace: claude's working directory, where
// its file tools and sandboxed commands may write. It is absolute, because
// the workspace permission rules name it.
func WorkspaceDir(cfg Config, userID string) (string, error) {
	return filepath.Abs(filepath.Join(cfg.DataDir, "users", userID, "workspace"))
}

func runOnce(ctx context.Context, opts claude.Options, prompt string, emit Emit) (protocol.TurnOutput, error) {
	t := newTranslator(emit)
	err := claude.Run(ctx, opts, prompt, t.handle)
	if err != nil {
		return t.out, err
	}
	if !t.done {
		return t.out, errors.New("claude ended without a result")
	}
	if t.resultErr != "" {
		return t.out, fmt.Errorf("claude: %s", t.resultErr)
	}
	return t.out, nil
}

// translator maps claude stream-json events to packets.
type translator struct {
	emit      Emit
	cites     *citation.Processor
	out       protocol.TurnOutput
	wroteText bool // any answer text emitted in this turn
	// needBreak is set when a new text block starts after earlier text, so
	// separate assistant messages don't run together.
	needBreak bool
	done      bool
	resultErr string
}

func newTranslator(emit Emit) *translator {
	return &translator{emit: emit, cites: citation.New(citation.Hyperlink)}
}

func (t *translator) send(ps []protocol.Packet) {
	for _, p := range ps {
		if p.Type == protocol.PacketMessageDelta {
			t.out.Text += p.Content
			t.wroteText = true
		}
		t.emit(p)
	}
}

func (t *translator) handle(ev claude.Event) error {
	if ev.SessionID != "" {
		t.out.SessionID = ev.SessionID
	}
	switch ev.Type {
	case "stream_event":
		se := ev.Event
		if se == nil {
			return nil
		}
		switch se.Type {
		case "content_block_start":
			if se.ContentBlock == nil {
				return nil
			}
			switch se.ContentBlock.Type {
			case "text":
				t.needBreak = t.wroteText
			case "tool_use":
				t.send(t.cites.Flush())
				t.emit(protocol.Packet{Type: protocol.PacketToolStart, ToolCallID: se.ContentBlock.ID, ToolName: se.ContentBlock.Name})
			}
		case "content_block_delta":
			if se.Delta != nil && se.Delta.Type == "text_delta" && se.Delta.Text != "" {
				text := se.Delta.Text
				if t.needBreak {
					t.needBreak = false
					text = "\n\n" + text
				}
				t.send(t.cites.Process(text))
			}
		}
	case "assistant":
		// Complete tool calls carry their final arguments.
		if ev.Message == nil {
			return nil
		}
		for _, b := range ev.Message.Content {
			if b.Type == "tool_use" {
				t.emit(protocol.Packet{Type: protocol.PacketToolStart, ToolCallID: b.ID, ToolName: b.Name, ToolArgs: string(b.Input)})
			}
		}
	case "user":
		if ev.Message == nil {
			return nil
		}
		for _, b := range ev.Message.Content {
			if b.Type == "tool_result" {
				t.emit(protocol.Packet{Type: protocol.PacketToolResult, ToolCallID: b.ToolUseID, IsError: b.IsError})
			}
		}
	case "result":
		t.send(t.cites.Flush())
		t.done = true
		t.out.CostUSD = ev.TotalCostUSD
		t.out.NumTurns = ev.NumTurns
		t.out.Citations = t.cites.Seen()
		t.out.Structured = ev.StructuredOutput
		if ev.IsError {
			t.resultErr = ev.Subtype
			if len(ev.Errors) > 0 {
				t.resultErr = fmt.Sprintf("%s: %v", ev.Subtype, ev.Errors)
			}
			return nil
		}
		// Prefer claude's final text if streaming produced none (e.g. partial
		// messages unavailable).
		if !t.wroteText && ev.Result != "" {
			t.send(t.cites.Process(ev.Result))
			t.send(t.cites.Flush())
		}
		t.emit(protocol.Packet{Type: protocol.PacketStop, StopReason: "finished"})
	}
	return nil
}

// exercise is the exercise a turn answers, or nil; it is only shown with
// its goal.
func exercise(in protocol.TurnInput) *protocol.ExerciseRef {
	if in.Goal == nil {
		return nil
	}
	return in.Exercise
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
