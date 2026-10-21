package workflows

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roamind.ai/agent"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
)

func mention(eventID, user, ts, threadTS string) slack.Envelope {
	return slack.Envelope{TeamID: "T1", EventID: eventID,
		Authorizations: []slack.Authorization{{UserID: "UBOT", IsBot: true}}, Event: slack.Event{
			Type: "app_mention", User: user, Text: "<@UBOT> hello", Channel: "C1", TS: ts, ThreadTS: threadTS,
		}}
}

func TestProcessConversation(t *testing.T) {
	ctx := context.Background()
	d, fs, argsLog := setup(t)

	out, err := d.Process(ctx, "run1", mention("Ev1", "U1", "1.0", ""))
	if err != nil || out.Skipped != "" {
		t.Fatalf("first turn: out=%+v err=%v", out, err)
	}
	if out.UserID != identity.UserID("owner@x.com") || out.SessionID == "" {
		t.Fatalf("out = %+v", out)
	}
	if fs.updates == 0 {
		t.Fatal("placeholder never edited")
	}

	// A reply in the same thread resumes the stored session.
	if _, err := d.Process(ctx, "run2", mention("Ev2", "U1", "1.1", "1.0")); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(argsLog)
	lines := strings.Split(strings.TrimSpace(string(args)), "\n")
	if len(lines) != 2 || strings.Contains(lines[0], "--resume") || !strings.Contains(lines[1], "--resume "+out.SessionID) {
		t.Fatalf("claude args:\n%s", args)
	}

	// Slack redelivery is ignored.
	if out, _ := d.Process(ctx, "run3", mention("Ev2", "U1", "1.1", "1.0")); !strings.HasPrefix(out.Skipped, "duplicate") {
		t.Fatalf("redelivery not skipped: %+v", out)
	}
}

func TestProcessRefusals(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setup(t)
	if _, err := d.Process(ctx, "r1", mention("Ev1", "U1", "1.0", "")); err != nil {
		t.Fatal(err)
	}
	external := mention("Ev2", "U3", "2.0", "")
	external.Event.Team = "TOTHER"
	cases := []struct {
		env  slack.Envelope
		want string
	}{
		{external, "members of this workspace"},
		{mention("Ev3", "U2", "1.2", "1.0"), "another user"},
		{slack.Envelope{EventID: "Ev4", Event: slack.Event{Type: "message", ChannelType: "im", User: "U1", BotID: "B1"}}, "not a chat"},
	}
	for _, c := range cases {
		out, err := d.Process(ctx, "r", c.env)
		if err != nil || !strings.Contains(out.Skipped, c.want) {
			t.Errorf("want skip %q, got out=%+v err=%v", c.want, out, err)
		}
	}
}

func TestProcessSavesSharedFiles(t *testing.T) {
	ctx := context.Background()
	d, _, argsLog := setup(t)
	env := slack.Envelope{TeamID: "T1", EventID: "EvF", Event: slack.Event{
		Type: "message", Subtype: "file_share", ChannelType: "im", User: "U1",
		Text: "summarize this", Channel: "D1", TS: "2.0",
		Files: []slack.File{
			{ID: "F1", Name: "paper.pdf", Mimetype: "application/pdf", Size: 13, URLPrivateDownload: "https://files.slack.com/paper.pdf"},
			{ID: "F2", FileAccess: "check_file_info"}, // details via files.info; hostile name
			{ID: "F3", Name: "huge.mov", Size: slack.MaxFileSize + 1, URLPrivateDownload: "https://files.slack.com/huge"},
		},
	}}
	if out, err := d.Process(ctx, "runF", env); err != nil || out.Skipped != "" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	ws, _ := agent.WorkspaceDir(d.Agent, identity.UserID("owner@x.com"))
	if b, err := os.ReadFile(filepath.Join(ws, "uploads", "paper.pdf")); err != nil || string(b) != "%PDF-1.4 fake" {
		t.Fatalf("paper.pdf = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "uploads", "notes.txt")); err != nil {
		t.Fatalf("files.info file not saved inside uploads: %v", err)
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	for _, want := range []string{"summarize this", "uploads/paper.pdf (application/pdf, 13 B)", "uploads/notes.txt", "huge.mov: not saved, larger than 50 MB"} {
		if !strings.Contains(string(stdin), want) {
			t.Errorf("prompt lacks %q:\n%s", want, stdin)
		}
	}
}

func TestProcessGoalChannel(t *testing.T) {
	ctx := context.Background()
	d, _, argsLog := setup(t)
	withMemory(t, &d)
	var after []string
	d.AfterTurn = func(_ context.Context, u *store.User, _ FinishedTurn) error {
		after = append(after, u.MemoryProject)
		return nil
	}

	// First contact creates and sets up the owner; their next message is a
	// turn. Then their goal gets channel GOAL.
	for _, ts := range []string{"0.5", "1.0"} {
		if _, err := d.Process(ctx, "r0", mention("Ev"+ts, "U1", ts, "")); err != nil {
			t.Fatal(err)
		}
	}
	if len(after) != 1 || after[0] == "" {
		t.Fatalf("AfterTurn calls = %v", after)
	}
	owner := identity.UserID("owner@x.com")
	err := d.Store.SaveChannel(ctx, store.Channel{Kind: store.KindGoal,
		UserID: owner, Ref: "bm-u-test-u1-1/goals/learn-jax", Title: "Learn JAX",
		ChannelID: "GOAL",
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := func(id, user, channel, ts string) slack.Envelope {
		return slack.Envelope{TeamID: "T1", EventID: id, Event: slack.Event{
			Type: "message", ChannelType: "group", User: user, Text: "finished the vmap chapter", Channel: channel, TS: ts,
		}}
	}
	inGoal := func(m slack.Envelope) slack.Envelope { m.Event.Channel = "GOAL"; return m }
	cases := []struct {
		name string
		env  slack.Envelope
		skip string // "" means a turn runs
	}{
		{"owner message in goal channel", msg("Ev1", "U1", "GOAL", "2.0"), ""},
		{"owner mention in goal channel", inGoal(mention("Ev2", "U1", "2.1", "")), "handled as its message"},
		{"other user's message in goal channel", msg("Ev3", "U2", "GOAL", "2.2"), "not addressed"},
		{"other user's mention in goal channel", inGoal(mention("Ev4", "U2", "2.3", "")), ""},
		{"message in another private channel", msg("Ev5", "U1", "G9", "2.4"), "not addressed"},
		{"member's message with their own goal in a shared channel", msg("Ev6", "U2", "GOAL", "2.5"), ""},
	}
	// U2 joins GOAL with the same goal in their own memory, for the last case.
	if _, err := d.Process(ctx, "r0", mention("Ev00", "U2", "1.1", "")); err != nil {
		t.Fatal(err)
	}
	other := identity.UserID("other@x.com")
	for _, c := range cases {
		if c.env.EventID == "Ev6" {
			if err := d.Store.SaveChannel(ctx, store.Channel{Kind: store.KindGoal,
				UserID: other, Ref: "bm-u-test-u2-1/goals/learn-jax", Title: "Learn JAX",
				ChannelID: "GOAL",
			}); err != nil {
				t.Fatal(err)
			}
		}
		os.Remove(argsLog)
		out, err := d.Process(ctx, "r", c.env)
		if err != nil || !strings.Contains(out.Skipped, c.skip) || (c.skip == "") != (out.Skipped == "") {
			t.Errorf("%s: want skip %q, got out=%+v err=%v", c.name, c.skip, out, err)
			continue
		}
		args, _ := os.ReadFile(argsLog)
		goalPrompt := strings.Contains(string(args), "memory://bm-u-test-u1-1/goals/learn-jax")
		if wantGoal := c.name == "owner message in goal channel"; goalPrompt != wantGoal {
			t.Errorf("%s: goal in prompt = %v, want %v", c.name, goalPrompt, wantGoal)
		}
		if c.env.EventID == "Ev6" && !strings.Contains(string(args), "memory://bm-u-test-u2-1/goals/learn-jax") {
			t.Errorf("%s: member's own goal not in prompt", c.name)
		}
	}
}

func TestProcessExerciseAnswer(t *testing.T) {
	ctx := context.Background()
	d, _, argsLog := setup(t)
	withMemory(t, &d)
	if _, err := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); err != nil {
		t.Fatal(err)
	}
	owner := identity.UserID("owner@x.com")
	goal := "bm-u-test-u1-1/goals/learn-german"
	if err := d.Store.SaveChannel(ctx, store.Channel{Kind: store.KindGoal,
		UserID: owner, Ref: goal, Title: "Learn German", ChannelID: "GOAL",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SaveExercise(ctx, store.Exercise{
		UserID: owner, GoalPermalink: goal, ChannelID: "GOAL", ThreadTS: "5.0", NotePermalink: "bm-u-test-u1-1/exercises/learn-german/2026-10-12", PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	reply := func(id, user, ts, threadTS string) slack.Envelope {
		return slack.Envelope{TeamID: "T1", EventID: id, Event: slack.Event{
			Type: "message", ChannelType: "group", User: user, Text: "Ich gehe in den Park.", Channel: "GOAL", TS: ts, ThreadTS: threadTS,
		}}
	}
	exercisePrompt := func() bool {
		args, _ := os.ReadFile(argsLog)
		return strings.Contains(string(args), "saved in memory as bm-u-test-u1-1/exercises/learn-german/2026-10-12")
	}

	// A top-level message in the goal channel is a plain goal turn.
	os.Remove(argsLog)
	if _, err := d.Process(ctx, "r1", reply("Ev1", "U1", "6.0", "")); err != nil {
		t.Fatal(err)
	}
	if e, _ := d.Store.LastExercise(ctx, owner, goal); !e.AnsweredAt.IsZero() || exercisePrompt() {
		t.Fatal("top-level message answered the exercise")
	}
	// Someone else's reply doesn't answer it.
	if _, err := d.Process(ctx, "r2", reply("Ev2", "U2", "6.1", "5.0")); err != nil {
		t.Fatal(err)
	}
	if e, _ := d.Store.LastExercise(ctx, owner, goal); !e.AnsweredAt.IsZero() {
		t.Fatal("other user's reply answered the exercise")
	}
	// The owner's reply in the thread does, and the turn evaluates it.
	os.Remove(argsLog)
	if _, err := d.Process(ctx, "r3", reply("Ev3", "U1", "6.2", "5.0")); err != nil {
		t.Fatal(err)
	}
	if e, _ := d.Store.LastExercise(ctx, owner, goal); e.AnsweredAt.IsZero() || !exercisePrompt() {
		t.Fatal("owner's reply did not answer the exercise")
	}
}
