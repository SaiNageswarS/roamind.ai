package goalchannels

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/channels/store"
	"roamind.ai/memory"
)

type fakeSlack struct {
	taken   map[string]bool
	created []string
	invites map[string]string
	posts   map[string]int
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

func (f *fakeSlack) PostMessageContext(_ context.Context, ch string, _ ...slackapi.MsgOption) (string, string, error) {
	f.posts[ch]++
	return ch, "1.0", nil
}

func writeGoal(t *testing.T, mem *memory.Config, project, title, slug, status string) {
	t.Helper()
	dir := filepath.Join(mem.Dir, "users", project, "notes", "goals")
	os.MkdirAll(dir, 0o700)
	body := fmt.Sprintf("---\ntitle: %s\ntype: goal\npermalink: %s/goals/%s\n---\n\n- [status] %s (stated, slack, 2026-10-11)\n", title, project, slug, status)
	if err := os.WriteFile(filepath.Join(dir, title+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSync(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	mem, err := memory.New(t.TempDir(), "true")
	if err != nil {
		t.Fatal(err)
	}
	u := store.User{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"}
	if err := st.UpsertSlackUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.MemoryProject, err = st.AssignMemoryProject(ctx, u.ID, "bm-u-a"); err != nil {
		t.Fatal(err)
	}
	fs := &fakeSlack{taken: map[string]bool{"goal-learn-jax": true}, invites: map[string]string{}, posts: map[string]int{}}
	d := Deps{Store: st, Slack: fs, Memory: mem}

	if m, err := d.Missing(ctx, &u); err != nil || len(m) != 0 {
		t.Fatalf("no goals: %v %v", m, err)
	}

	writeGoal(t, mem, u.MemoryProject, "Learn JAX", "learn-jax", "active")
	writeGoal(t, mem, u.MemoryProject, "Old thesis", "old-thesis", "achieved")
	if m, err := d.Missing(ctx, &u); err != nil || len(m) != 1 || m[0].Title != "Learn JAX" {
		t.Fatalf("new goal: %v %v", m, err)
	}

	out, err := d.Sync(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The taken name gets a suffix; the achieved goal gets no channel.
	if len(out.Created) != 1 || out.Created[0] != "goal-learn-jax-2" {
		t.Fatalf("created = %v", out.Created)
	}
	if fs.invites["G1"] != "U1" || fs.posts["G1"] != 1 {
		t.Fatalf("invites=%v posts=%v", fs.invites, fs.posts)
	}
	gc, err := st.GoalChannelByChannel(ctx, "slack", "G1")
	if err != nil || gc == nil || gc.GoalPermalink != u.MemoryProject+"/goals/learn-jax" || gc.GoalTitle != "Learn JAX" {
		t.Fatalf("gc=%+v err=%v", gc, err)
	}

	// Idempotent: nothing left to create.
	if out, err := d.Sync(ctx, u.ID); err != nil || len(out.Created) != 0 {
		t.Fatalf("second sync: %+v %v", out, err)
	}
	if m, err := d.Missing(ctx, &u); err != nil || len(m) != 0 {
		t.Fatalf("after sync: %v %v", m, err)
	}
}

func TestChannelName(t *testing.T) {
	for in, want := range map[string]string{
		"Learn JAX":                 "goal-learn-jax",
		"Ship the analog chip: v2!": "goal-ship-the-analog-chip-v2",
		"日本語":                       "goal-untitled",
		"  a  b ":                   "goal-a-b",
	} {
		if got := ChannelName(in); got != want {
			t.Errorf("ChannelName(%q) = %q, want %q", in, got, want)
		}
	}
	long := ChannelName("abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij")
	if len(long) > 70 {
		t.Errorf("long name %q", long)
	}
}
