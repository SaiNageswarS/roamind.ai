package agent

import (
	"encoding/json"
	"os"
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
