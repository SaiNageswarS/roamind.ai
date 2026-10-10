package agent

import (
	"os"
	"testing"

	"roamind.ai/agent/claude"
	"roamind.ai/protocol"
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
