package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Options configures a `claude -p` invocation.
type Options struct {
	// Binary defaults to "claude".
	Binary string
	// Dir is the working directory. Claude keys stored sessions by directory,
	// so resuming only works from the same Dir.
	Dir string
	// SessionID resumes an existing session when set.
	SessionID string
	Model     string
	// SystemPrompt replaces claude's default (coding-agent) system prompt, which
	// would otherwise describe tools the agent does not have.
	SystemPrompt string
	// Tools are the built-in tools to enable (--tools); empty disables them all.
	Tools []string
	// Settings is a JSON string or file path for --settings, e.g. the sandbox
	// and permission rules. It is honored even though user, project and local
	// settings files are ignored.
	Settings string
	// MCPConfig is a JSON string or file path for --mcp-config.
	MCPConfig string
	// AllowedTools lists tools (e.g. Bash, mcp__memory) the model may call;
	// in dontAsk mode everything else is denied.
	AllowedTools []string
	// DisallowedTools are removed from the model's view entirely.
	DisallowedTools []string
	Env             []string
}

// Args builds the command-line arguments. User settings and ambient MCP
// servers are always ignored, and anything not explicitly allowed is denied
// (dontAsk): the model only gets the tools we hand it.
func (o Options) Args() []string {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--tools", strings.Join(o.Tools, ","),
		"--strict-mcp-config",
		"--setting-sources", "",
		"--permission-mode", "dontAsk",
	}
	if o.Settings != "" {
		args = append(args, "--settings", o.Settings)
	}
	if o.MCPConfig != "" {
		args = append(args, "--mcp-config", o.MCPConfig)
	}
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	if o.SystemPrompt != "" {
		args = append(args, "--system-prompt", o.SystemPrompt)
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	if o.SessionID != "" {
		args = append(args, "--resume", o.SessionID)
	}
	return args
}

// ErrSessionNotFound is returned when --resume names a session claude does
// not have.
var ErrSessionNotFound = errors.New("claude session not found")

// Run executes claude with prompt on stdin and calls fn for each event.
// Cancelling ctx sends SIGINT so claude can end the turn cleanly.
func Run(ctx context.Context, o Options, prompt string, fn func(Event) error) error {
	bin := o.Binary
	if bin == "" {
		bin = "claude"
	}
	cmd := exec.CommandContext(ctx, bin, o.Args()...)
	cmd.Dir = o.Dir
	cmd.Env = append(os.Environ(), o.Env...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 10 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start claude: %w", err)
	}

	sawResult, sessionMissing := false, false
	decodeErr := Decode(stdout, func(ev Event) error {
		if ev.Type == "result" {
			sawResult = true
			if ev.IsError && o.SessionID != "" && slices.ContainsFunc(ev.Errors, isSessionMissing) {
				sessionMissing = true
				return nil
			}
		}
		return fn(ev)
	})
	waitErr := cmd.Wait()

	if decodeErr != nil {
		return decodeErr
	}
	if sessionMissing || (o.SessionID != "" && isSessionMissing(stderr.String())) {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, o.SessionID)
	}
	if waitErr != nil && !sawResult {
		return fmt.Errorf("claude exited: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func isSessionMissing(msg string) bool {
	return strings.Contains(msg, "No conversation found with session ID")
}
