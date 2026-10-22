package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roamind.ai/agent/claude"
	"roamind.ai/agent/protocol"
)

func TestTranslatorFixture(t *testing.T) {
	f, err := os.Open("claude/testdata/tool_use.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var snap protocol.Snapshot
	tr := newTranslator(func(p protocol.Packet) { snap.Apply(p) })
	if err := claude.Decode(f, tr.handle); err != nil {
		t.Fatal(err)
	}
	if !tr.done || tr.resultErr != "" {
		t.Fatalf("done=%v err=%q", tr.done, tr.resultErr)
	}
	if len(snap.Tools) != 1 || snap.Tools[0].Name != "Read" || !snap.Tools[0].Done {
		t.Fatalf("tools = %+v", snap.Tools)
	}
	if snap.Text == "" || snap.Text != tr.out.Text || !snap.Done {
		t.Fatalf("snapshot text %q vs out %q, done=%v", snap.Text, tr.out.Text, snap.Done)
	}
	if tr.out.SessionID == "" {
		t.Fatal("no session id")
	}
}

func TestDraftExercisePrompt(t *testing.T) {
	first, err := DraftExercisePrompt(ExerciseDraft{UserName: "Sai", Date: "2026-10-12", GoalTitle: "Learn German", Goal: "goal body"})
	if err != nil || !strings.Contains(first, "first exercise") || !strings.Contains(first, "goal body") {
		t.Fatalf("first: %v\n%s", err, first)
	}
	next, err := DraftExercisePrompt(ExerciseDraft{UserName: "Sai", GoalTitle: "Learn German", Goal: "g", Exercises: []string{"ex one", "ex two"}})
	if err != nil || strings.Contains(next, "first exercise") || !strings.Contains(next, "<exercise>\nex one\n</exercise>") || !strings.Contains(next, "Calibrate") {
		t.Fatalf("next: %v\n%s", err, next)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(DraftExerciseSchema), &schema); err != nil {
		t.Fatal(err)
	}
}

// Each tool set gives claude only its tools: a turn on third-party input
// gets none, in a throwaway directory, with no saved session.
func TestToolSets(t *testing.T) {
	dir := t.TempDir()
	fixture, err := filepath.Abs("claude/testdata/tool_use.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n{ echo \"cwd=$PWD\"; printf '%s|' \"$@\"; echo; } >> " + logf + "\ncat > /dev/null\ncat " + fixture + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DataDir: filepath.Join(dir, "data"), ClaudeBinary: bin, ToolsServer: []string{"/opt/mcp"}}
	workspace, _ := WorkspaceDir(cfg, "u1")
	cases := []struct {
		tools      string
		want, deny []string
		inWS       bool
	}{
		{protocol.ToolsWorkspace, []string{"--tools|Bash,Read,Write,Edit", "Edit(//", "mcp__roamind", "--mcp-config", "and work in"}, []string{"--no-session-persistence"}, true},
		{protocol.ToolsWeb, []string{"--tools|WebSearch,WebFetch|", "--allowedTools|WebSearch,WebFetch|", "You can search the web and read pages (WebSearch, WebFetch)."}, []string{"Bash", "--mcp-config", "and work in", "--no-session-persistence"}, true},
		{protocol.ToolsRecall, []string{"--tools|WebSearch|", "--allowedTools|WebSearch|", "You can search the web (WebSearch)."}, []string{"Bash", "WebFetch", "Write", "--mcp-config", "and work in", "--no-session-persistence", "--settings"}, true},
		{protocol.ToolsNone, []string{"--tools||", "--no-session-persistence", "Work only from what this message gives you."}, []string{"--allowedTools", "--mcp-config", "WebFetch", "--settings"}, false},
	}
	for _, c := range cases {
		os.Remove(logf)
		if _, err := RunTurn(context.Background(), cfg, protocol.TurnInput{UserID: "u1", Text: "hi", Tools: c.tools, OutputSchema: "{}"}, func(protocol.Packet) {}); err != nil {
			t.Fatalf("%q: %v", c.tools, err)
		}
		if c.tools == protocol.ToolsWorkspace {
			// A turn given Roamind's tool sets also gets its server.
			os.Remove(logf)
			if _, err := RunTurn(context.Background(), cfg, protocol.TurnInput{UserID: "u1", Text: "hi", ServerTools: []string{protocol.ServerInterests}}, func(protocol.Packet) {}); err != nil {
				t.Fatal(err)
			}
		}
		b, _ := os.ReadFile(logf)
		call := string(b)
		for _, w := range c.want {
			if !strings.Contains(call, w) {
				t.Errorf("%q: lacks %q:\n%s", c.tools, w, call)
			}
		}
		for _, d := range c.deny {
			if strings.Contains(call, d) {
				t.Errorf("%q: has %q:\n%s", c.tools, d, call)
			}
		}
		if got := strings.Contains(call, "cwd="+workspace+"\n"); got != c.inWS {
			t.Errorf("%q: in the workspace = %v:\n%s", c.tools, got, call)
		}
	}
	if _, err := RunTurn(context.Background(), cfg, protocol.TurnInput{UserID: "u1", Text: "hi", Tools: "all"}, func(protocol.Packet) {}); err == nil {
		t.Fatal("an unknown tool set ran")
	}
}
