package chatturn

import (
	"context"
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
		"\ncat > /dev/null\ncat " + fixture + "\n"
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
