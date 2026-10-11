package claude

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDecodeToolUseFixture(t *testing.T) {
	f, err := os.Open("testdata/tool_use.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var types []string
	var toolName, toolResultID, result, sid string
	err = Decode(f, func(ev Event) error {
		types = append(types, ev.Type)
		switch ev.Type {
		case "stream_event":
			if ev.Event.Type == "content_block_start" && ev.Event.ContentBlock.Type == "tool_use" {
				toolName = ev.Event.ContentBlock.Name
			}
		case "user":
			toolResultID = ev.Message.Content[0].ToolUseID
		case "result":
			result, sid = ev.Result, ev.SessionID
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if types[0] != "system" || !slices.Contains(types, "result") {
		t.Fatalf("types = %v", types)
	}
	if toolName != "Read" || toolResultID == "" || result == "" || sid == "" {
		t.Fatalf("tool=%q resultID=%q result=%q sid=%q", toolName, toolResultID, result, sid)
	}
}

func TestArgs(t *testing.T) {
	args := Options{SessionID: "s1", Model: "haiku", AllowedTools: []string{"a", "b"}}.Args()
	for _, want := range []string{"--resume", "s1", "--model", "haiku", "a,b", "--strict-mcp-config"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	i := slices.Index(args, "--tools")
	if args[i+1] != "" {
		t.Fatalf("--tools should be empty, got %q", args[i+1])
	}
}

func TestDecodeSystemStringMessage(t *testing.T) {
	in := `{"type":"system","subtype":"permission_denied","tool_name":"mcp__memory__x","message":"Permission denied"}` + "\n" +
		`{"type":"result","subtype":"success"}` + "\n"
	var types []string
	err := Decode(strings.NewReader(in), func(ev Event) error {
		types = append(types, ev.Type)
		return nil
	})
	if err != nil || len(types) != 2 {
		t.Fatalf("types = %v, err = %v", types, err)
	}
}
