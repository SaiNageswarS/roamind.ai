package slack

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/slack-go/slack"

	"roamind.ai/agent/protocol"
)

func TestIsChat(t *testing.T) {
	cases := []struct {
		ev   Event
		want bool
	}{
		{Event{Type: "app_mention", User: "U1"}, true},
		{Event{Type: "message", ChannelType: "im", User: "U1"}, true},
		{Event{Type: "message", ChannelType: "channel", User: "U1"}, false},
		{Event{Type: "message", ChannelType: "im", User: "U1", BotID: "B1"}, false},
		{Event{Type: "message", ChannelType: "im", User: "U1", Subtype: "message_changed"}, false},
	}
	for i, c := range cases {
		if got := (Envelope{Event: c.ev}).IsChat(); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}

func TestEnvelopeDecode(t *testing.T) {
	raw := `{"type":"event_callback","team_id":"T1","event_id":"Ev1","authorizations":[{"user_id":"UBOT","is_bot":true}],"event":{"type":"app_mention","user":"U1","text":"<@UBOT> hi there","channel":"C1","ts":"1.1","thread_ts":"1.0"}}`
	var e Envelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	if e.Event.RootTS() != "1.0" || CleanText(context.Background(), users{}, e.Event.Text, e.BotUserID()) != "hi there" || e.TeamOf() != "T1" {
		t.Fatalf("%+v", e)
	}
}

// users names U<n> "Person <n>"; UGONE can't be looked up.
type users struct{}

func (users) GetUserInfoContext(_ context.Context, id string, _ ...slack.GetUserInfoOption) (*slack.User, error) {
	if id == "UGONE" {
		return nil, errors.New("user_not_found")
	}
	u := &slack.User{ID: id, RealName: "Real " + id}
	if id != "U2" {
		u.Profile.DisplayName = "Person " + strings.TrimPrefix(id, "U")
	}
	return u, nil
}

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		"<@UBOT> remember <@U1> is my father": "remember @Person 1 is my father",
		"<@U1> and <@U1|kam> met <@U2>":       "@Person 1 and @Person 1 met @Real U2",
		"ask <@UGONE>":                        "ask <@UGONE>",
		"no mentions":                         "no mentions",
	}
	for in, want := range cases {
		if got := CleanText(context.Background(), users{}, in, "UBOT"); got != want {
			t.Errorf("CleanText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToMrkdwn(t *testing.T) {
	cases := map[string]string{
		"**bold** and *it*":      "*bold* and _it_",
		"see [[1]](https://a)":   "see <https://a|[1]>",
		"[docs](https://d) x":    "<https://d|docs> x",
		"# Title":                "*Title*",
		"a < b & c":              "a &lt; b &amp; c",
		"`**raw**` **b**":        "`**raw**` *b*",
		"```\n**raw**\n```":      "```\n**raw**\n```",
		"~~old~~":                "~old~",
		"- item one\n- item two": "- item one\n- item two",
	}
	for in, want := range cases {
		if got := ToMrkdwn(in); got != want {
			t.Errorf("ToMrkdwn(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderBodyTools(t *testing.T) {
	s := protocol.Snapshot{Text: "done", Tools: []protocol.ToolLine{
		{ID: "1", Name: "mcp__roamind__memory_search", Done: true},
		{ID: "2", Name: "Read"},
	}}
	got := RenderBody(s)
	want := "_✓ memory_search_\n_🔧 Read…_\n\ndone"
	if got != want {
		t.Fatalf("got %q", got)
	}
}

func TestSplit(t *testing.T) {
	text := strings.Repeat("word ", 1000)
	chunks := Split(text, 300)
	for _, c := range chunks {
		if len(c) > 300 {
			t.Fatalf("chunk too long: %d", len(c))
		}
	}
	if strings.Join(chunks, " ") != strings.TrimSpace(text) {
		t.Fatal("content lost")
	}
	if got := Split("short", 300); len(got) != 1 || got[0] != "short" {
		t.Fatalf("got %v", got)
	}
}

type fakeAPI struct {
	mu      sync.Mutex
	posts   []string
	updates map[string]string
	n       int
}

func (f *fakeAPI) PostMessageContext(_ context.Context, _ string, opts ...slack.MsgOption) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	ts := string(rune('a' + f.n))
	f.posts = append(f.posts, ts)
	return "", ts, nil
}

func (f *fakeAPI) UpdateMessageContext(_ context.Context, _, ts string, opts ...slack.MsgOption) (string, string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updates == nil {
		f.updates = map[string]string{}
	}
	f.updates[ts] = "updated"
	return "", ts, "", nil
}

func TestUpdaterSpillsIntoReplies(t *testing.T) {
	ctx := context.Background()
	api := &fakeAPI{}
	u, err := NewUpdater(ctx, api, "C1", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	u.Apply(protocol.Packet{Type: protocol.PacketMessageDelta, Content: strings.Repeat("word ", 1000)})
	u.Apply(protocol.Packet{Type: protocol.PacketStop})
	u.Finish(ctx, "")
	if len(api.posts) < 2 {
		t.Fatalf("want placeholder + spill posts, got %v", api.posts)
	}
	if api.updates[api.posts[0]] == "" {
		t.Fatal("placeholder not updated")
	}
}

func TestIsChatFileShare(t *testing.T) {
	dm := Envelope{Event: Event{Type: "message", ChannelType: "im", User: "U1", Subtype: "file_share"}}
	if !dm.IsChat() {
		t.Fatal("file_share DM not treated as chat")
	}
	dm.Event.Subtype = "message_changed"
	if dm.IsChat() {
		t.Fatal("edit treated as chat")
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"../../etc/passwd": "passwd", "report v2.pdf": "report v2.pdf", "..hidden": "hidden",
		"a/b\\c?.txt": "b_c_.txt", "": "file",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
