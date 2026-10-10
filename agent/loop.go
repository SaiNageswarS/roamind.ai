// Package agent runs one conversational turn: it drives `claude -p` (which
// owns the tool-calling loop), translates its stream-json events into
// onyx-style packets and passes answer text through the citation processor.
package agent

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"roamind.ai/agent/citation"
	"roamind.ai/agent/claude"
	"roamind.ai/protocol"
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
}

// Emit receives packets as the turn progresses.
type Emit func(protocol.Packet)

// RunTurn runs one turn for in.UserID. When in.SessionID cannot be resumed the
// turn is retried in a fresh session and the output is marked SessionReset.
func RunTurn(ctx context.Context, cfg Config, in protocol.TurnInput, channel string, emit Emit) (protocol.TurnOutput, error) {
	if in.UserID == "" {
		return protocol.TurnOutput{}, errors.New("agent: empty user id")
	}
	dir := filepath.Join(cfg.DataDir, "users", in.UserID, "workspace")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return protocol.TurnOutput{}, err
	}
	var sp bytes.Buffer
	err := systemPrompt.Execute(&sp, map[string]string{
		"UserName": orDefault(in.UserName, "the user"),
		"Channel":  orDefault(channel, "chat"),
		"Date":     time.Now().Format("Monday, 2 January 2006"),
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

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
