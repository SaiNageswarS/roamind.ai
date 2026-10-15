package workflows

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/store"
)

// fakeSlack records what the workflows send to Slack.
type fakeSlack struct {
	mu      sync.Mutex
	posts   []string       // ts of each post
	threads []string       // thread_ts of each post
	texts   []string       // text of each post
	postsTo map[string]int // posts per channel
	updates int
	emails  map[string]string
	taken   map[string]bool // channel names in use
	created []string
	invites map[string]string
	// failPosts makes PostMessageContext fail.
	failPosts bool
}

func newFakeSlack() *fakeSlack {
	return &fakeSlack{
		postsTo: map[string]int{}, invites: map[string]string{}, taken: map[string]bool{},
		emails: map[string]string{"U1": "Owner@x.com", "U2": "other@x.com", "U3": "stranger@x.com"},
	}
}

func (f *fakeSlack) PostMessageContext(_ context.Context, ch string, opts ...slackapi.MsgOption) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPosts {
		return "", "", fmt.Errorf("slack is down")
	}
	ts := fmt.Sprintf("m%d", len(f.posts))
	_, v, _ := slackapi.UnsafeApplyMsgOptions("", "", "", opts...)
	f.posts = append(f.posts, ts)
	f.threads = append(f.threads, v.Get("thread_ts"))
	f.texts = append(f.texts, v.Get("text"))
	f.postsTo[ch]++
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

func (f *fakeSlack) CreateConversationContext(_ context.Context, p slackapi.CreateConversationParams) (*slackapi.Channel, error) {
	if !p.IsPrivate {
		return nil, fmt.Errorf("public channel requested")
	}
	if f.taken[p.ChannelName] {
		return nil, slackapi.SlackErrorResponse{Err: "name_taken"}
	}
	f.taken[p.ChannelName] = true
	f.created = append(f.created, p.ChannelName)
	ch := &slackapi.Channel{}
	ch.ID, ch.Name = fmt.Sprintf("G%d", len(f.created)), p.ChannelName
	return ch, nil
}

func (f *fakeSlack) InviteUsersToConversationContext(_ context.Context, ch string, users ...string) (*slackapi.Channel, error) {
	f.invites[ch] = users[0]
	return nil, nil
}

// fakeClaude writes a script that replays the agent's recorded stream-json
// fixture and logs its arguments.
func fakeClaude(t *testing.T) (bin, argsLog string) {
	t.Helper()
	return fakeClaudeWith(t, "../../agent/claude/testdata/tool_use.jsonl")
}

func fakeClaudeWith(t *testing.T, fixturePath string) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	fixture, err := filepath.Abs(fixturePath)
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
	fs := newFakeSlack()
	return Deps{
		Store: st,
		Slack: fs,
		Agent: agent.Config{DataDir: t.TempDir(), ClaudeBinary: bin},
	}, fs, argsLog
}
