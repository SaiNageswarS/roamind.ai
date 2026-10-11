package chatturn

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/store"
	"roamind.ai/memory"
)

type fakeSlack struct {
	mu      sync.Mutex
	posts   []string
	updates int
	emails  map[string]string
}

func (f *fakeSlack) PostMessageContext(_ context.Context, _ string, _ ...slackapi.MsgOption) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ts := "m" + string(rune('0'+len(f.posts)))
	f.posts = append(f.posts, ts)
	return "", ts, nil
}

func (f *fakeSlack) UpdateMessageContext(_ context.Context, _, ts string, _ ...slackapi.MsgOption) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates++
	return "", ts, "", nil
}

// files maps download URLs to their content.
var files = map[string]string{"https://files.slack.com/paper.pdf": "%PDF-1.4 fake"}

func (f *fakeSlack) GetFileInfoContext(_ context.Context, id string, _, _ int) (*slackapi.File, []slackapi.Comment, *slackapi.Paging, error) {
	return &slackapi.File{ID: id, Name: "../../notes.txt", Mimetype: "text/plain", Size: 5,
		URLPrivateDownload: "https://files.slack.com/notes"}, nil, nil, nil
}

func (f *fakeSlack) GetFileContext(_ context.Context, url string, w io.Writer) error {
	body, ok := files[url]
	if !ok {
		body = "hello"
	}
	_, err := io.WriteString(w, body)
	return err
}

func (f *fakeSlack) GetUserInfoContext(_ context.Context, user string, _ ...slackapi.GetUserInfoOption) (*slackapi.User, error) {
	u := &slackapi.User{ID: user, RealName: "Test " + user}
	u.Profile.Email = f.emails[user]
	return u, nil
}

// fakeClaude writes a script that replays the agent's recorded stream-json
// fixture and logs its arguments.
func fakeClaude(t *testing.T) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	fixture, err := filepath.Abs("../../agent/claude/testdata/tool_use.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	argsLog = filepath.Join(dir, "args.log")
	bin = filepath.Join(dir, "claude")
	// One log line per invocation; the system prompt's newlines are flattened.
	script := "#!/bin/sh\nprintf '%s ' \"$@\" | tr '\\n' ' ' >> " + argsLog + "\necho >> " + argsLog +
		"\ncat >> " + filepath.Join(dir, "stdin.log") + "\ncat " + fixture + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsLog
}

func setup(t *testing.T) (Deps, *fakeSlack, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bin, argsLog := fakeClaude(t)
	fs := &fakeSlack{emails: map[string]string{"U1": "Owner@x.com", "U2": "other@x.com", "U3": "stranger@x.com"}}
	return Deps{
		Store:     st,
		Slack:     fs,
		Allowlist: identity.ParseAllowlist("owner@x.com,other@x.com"),
		Agent:     agent.Config{DataDir: t.TempDir(), ClaudeBinary: bin},
	}, fs, argsLog
}

func mention(eventID, user, ts, threadTS string) slack.Envelope {
	return slack.Envelope{TeamID: "T1", EventID: eventID, Event: slack.Event{
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
	cases := []struct {
		env  slack.Envelope
		want string
	}{
		{mention("Ev2", "U3", "2.0", ""), "don't have access"},
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
	mem, err := memory.New(t.TempDir(), "true")
	if err != nil {
		t.Fatal(err)
	}
	d.Agent.Memory = mem
	var after []string
	d.AfterTurn = func(_ context.Context, u *store.User) error {
		after = append(after, u.MemoryProject)
		return nil
	}

	// First contact creates the owner; then their goal gets channel G1.
	if _, err := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0] == "" {
		t.Fatalf("AfterTurn calls = %v", after)
	}
	owner := identity.UserID("owner@x.com")
	err = d.Store.SaveGoalChannel(ctx, store.GoalChannel{
		UserID: owner, GoalPermalink: "bm-u-test-u1-1/goals/learn-jax", GoalTitle: "Learn JAX",
		Provider: "slack", ChannelID: "G1",
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := func(id, user, channel, ts string) slack.Envelope {
		return slack.Envelope{TeamID: "T1", EventID: id, Event: slack.Event{
			Type: "message", ChannelType: "group", User: user, Text: "finished the vmap chapter", Channel: channel, TS: ts,
		}}
	}
	inGoal := func(m slack.Envelope) slack.Envelope { m.Event.Channel = "G1"; return m }
	cases := []struct {
		name string
		env  slack.Envelope
		skip string // "" means a turn runs
	}{
		{"owner message in goal channel", msg("Ev1", "U1", "G1", "2.0"), ""},
		{"owner mention in goal channel", inGoal(mention("Ev2", "U1", "2.1", "")), "handled as its message"},
		{"other user's message in goal channel", msg("Ev3", "U2", "G1", "2.2"), "not addressed"},
		{"other user's mention in goal channel", inGoal(mention("Ev4", "U2", "2.3", "")), ""},
		{"message in another private channel", msg("Ev5", "U1", "G9", "2.4"), "not addressed"},
	}
	for _, c := range cases {
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
	}
}
