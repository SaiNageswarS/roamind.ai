package workflows

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"

	"roamind.ai/agent"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/connectors/feeds"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
	"roamind.ai/memory/store/storetest"
)

func TestNewsChannelName(t *testing.T) {
	for in, want := range map[string]string{
		"Sai":                   "news-shorts-sai",
		"Kameswar R.":           "news-shorts-kameswar-r",
		"":                      "news-shorts",
		strings.Repeat("a", 50): "news-shorts-" + strings.Repeat("a", 40),
	} {
		if got := NewsChannelName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The webhook's CEL expression routes reactions to news-reaction, and
// leaves chat routing as it was.
func TestEventKeyExpression(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("input", cel.DynType), cel.Variable("headers", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	ast, iss := env.Compile(slack.EventKeyExpression)
	if iss.Err() != nil {
		t.Fatal(iss.Err())
	}
	prg, err := env.Program(ast)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		`{"type":"event_callback","event":{"type":"reaction_added","user":"U1","reaction":"+1","item":{"type":"message","channel":"G1","ts":"1.0"}}}`: slack.ReactionEventKey,
		`{"type":"event_callback","event":{"type":"message","channel_type":"group","user":"U1","text":"hi"}}`:                                         slack.ChatEventKey,
		`{"type":"event_callback","event":{"type":"app_mention","user":"U1","text":"hi"}}`:                                                            slack.ChatEventKey,
		`{"type":"event_callback","event":{"type":"message","channel_type":"group","user":"U1","bot_id":"B1"}}`:                                       "slack:ignored:message",
		`{"type":"event_callback","event":{"type":"reaction_removed","user":"U1"}}`:                                                                   "slack:ignored:reaction_removed",
		`{"type":"block_actions"}`: "slack:ignored:block_actions",
	}
	for raw, want := range cases {
		var input map[string]any
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		got, _, err := prg.Eval(map[string]any{"input": input, "headers": map[string]any{}})
		if err != nil || got.Value() != want {
			t.Errorf("%s: %v %v, want %s", raw, got, err, want)
		}
	}
}

func TestReactionValue(t *testing.T) {
	for name, want := range map[string]int{"+1": 1, "+1::skin-tone-3": 1, "heart": 1, "-1": -1, "zzz": -1, "eyes": 0} {
		if got := ReactionValue(name); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
}

// newsSetup is a user with memory, as after their first chat turn.
func newsSetup(t *testing.T) (Deps, *fakeSlack, store.User) {
	t.Helper()
	ctx := context.Background()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	d, fs, _ := setup(t)
	mem, err := notes.New(t.TempDir(), "../../memory", "true")
	if err != nil {
		t.Fatal(err)
	}
	d.Memory, d.Agent.Memory, d.Location = mem, mem, berlin
	// The test feeds are on loopback, which feeds.New refuses.
	d.Feeds = &feeds.Client{HTTP: &http.Client{Timeout: 5 * time.Second}}
	// Ranking turns run without it.
	d.Agent.ToolsServer = []string{"/opt/mcp"}
	slotTime := time.Date(2026, 10, 13, 16, 0, 3, 0, berlin)
	d.Now = func() time.Time { return slotTime }
	storetest.Seed(t, d.Store)
	u := store.User{ID: "h1", Email: "a@x.com", DisplayName: "Sai", SlackTeamID: "T1", SlackUserID: "U1"}
	if err := d.Store.UpsertSlackUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.MemoryProject, err = d.Store.AssignMemoryProject(ctx, u.ID, "bm-u-sai"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Setup(u.MemoryProject); err != nil {
		t.Fatal(err)
	}
	return d, fs, u
}

func TestSyncNews(t *testing.T) {
	ctx := context.Background()
	d, fs, u := newsSetup(t)
	fs.taken["news-shorts-sai"] = true

	// The user follows the catalog's six default interests.
	out, err := d.SyncNews(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Created != "news-shorts-sai-2" || len(out.Seeded) != 6 || out.Seeded[0] != "Positive living" {
		t.Fatalf("out = %+v", out)
	}
	if fs.invites["G1"] != "U1" || fs.postsTo["G1"] != 1 || !strings.Contains(fs.texts[0], "Stoic principles") ||
		!strings.Contains(fs.texts[0], "Every day at 16:00 (Europe/Berlin)") || strings.Contains(fs.texts[0], "Read") {
		t.Fatalf("invites=%v posts=%v texts=%q", fs.invites, fs.postsTo, fs.texts)
	}
	if ch, _ := d.Store.NewsChannelOf(ctx, u.ID); ch != "G1" {
		t.Fatalf("channel = %q", ch)
	}
	ins, err := d.Store.ActiveInterests(ctx, u.ID)
	if err != nil || len(ins) != 6 || ins[0].Name != "Positive living" || len(ins[0].Sources) == 0 {
		t.Fatalf("interests = %+v %v", ins, err)
	}

	// Idempotent: no second channel, nothing more to follow.
	if out, err := d.SyncNews(ctx, u.ID); err != nil || out.Created != "" || len(out.Seeded) != 0 || len(fs.created) != 1 {
		t.Fatalf("second sync: %+v %v", out, err)
	}
}

// feedServer serves the RSS feeds of TestNewsDigest; /broken fails.
func feedServer(t *testing.T) *httptest.Server {
	t.Helper()
	item := func(title, link, date string) string {
		return "<item><title>" + title + "</title><link>" + link + "</link><pubDate>" + date +
			"</pubDate><description>About " + title + ".</description></item>"
	}
	fresh, stale := "Mon, 12 Oct 2026 06:00:00 GMT", "Tue, 01 Sep 2026 06:00:00 GMT"
	feedsByPath := map[string]string{
		"/positive": item("Kindness at work", "https://ex.org/kindness", fresh) + item("Already sent", "https://ex.org/sent", fresh),
		"/stoic":    item("Seneca on time", "https://ex.org/seneca", fresh) + item("An old letter", "https://ex.org/old", stale),
		"/science": item("Sleep consolidates skills", "https://ex.org/sleep?utm_source=rss", "Mon, 12 Oct 2026 07:00:00 GMT") +
			item("A telescope sees the first galaxies", "https://ex.org/telescope", fresh),
		"/empty": "",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := feedsByPath[r.URL.Path]
		if !ok {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed ` + r.URL.Path + `</title>` + body + `</channel></rss>`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNewsDigest(t *testing.T) {
	ctx := context.Background()
	d, fs, u := newsSetup(t)
	bin, argsLog := fakeClaudeWith(t, "testdata/rank_digest.jsonl")
	d.Agent.ClaudeBinary = bin
	other := store.User{ID: "h2", Email: "b@x.com", DisplayName: "Kam", SlackTeamID: "T1", SlackUserID: "U2"}
	if err := d.Store.UpsertSlackUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Store.AssignMemoryProject(ctx, other.ID, "bm-u-kam"); err != nil {
		t.Fatal(err)
	}
	// The default interests read local feeds; Neuroscience shares Science's
	// feed, so its items go to Science, which the user follows first.
	srv := feedServer(t)
	paths := map[string]string{
		"Positive living": "/positive", "Stoic principles": "/stoic", "Healthy eating": "/broken",
		"Science": "/science", "Positive scientific developments across the world": "/empty",
		"Neuroscience for better living": "/science",
	}
	for name, p := range paths {
		storetest.SetSources(t, d.Store, name, srv.URL+p)
	}
	if _, err := d.SyncNews(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	storetest.MarkSent(t, d.Store, u.ID, "https://ex.org/sent")
	in := DigestInput{UserID: u.ID, Slot: "2026-10-13 16"}

	// The dispatcher: at the news hours only; a user without a channel gets one.
	if out, _, _, err := d.dueNews(ctx, d.now().Add(time.Hour)); err != nil || out.Skipped == "" {
		t.Fatalf("9:00: %+v %v", out, err)
	}
	out, digests, missing, err := d.dueNews(ctx, d.now())
	if err != nil || out.Slot != in.Slot || !reflect.DeepEqual(digests, []DigestInput{in}) ||
		!reflect.DeepEqual(missing, []UserSetupInput{{UserID: other.ID}}) {
		t.Fatalf("8:00: %+v %v %v %v", out, digests, missing, err)
	}

	chk, err := d.checkDigest(ctx, in)
	if err != nil || chk.Skip != "" || chk.ChannelID != "G1" {
		t.Fatalf("check: %+v %v", chk, err)
	}
	if chk, _ := d.checkDigest(ctx, DigestInput{UserID: other.ID, Slot: in.Slot}); !strings.Contains(chk.Skip, "no news channel") {
		t.Fatalf("other: %+v", chk)
	}

	// gather: fresh, unsent items, each once, newest first per interest.
	g, err := d.gatherDigest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range g.Candidates {
		got = append(got, c.ID+" "+c.Interest+": "+c.Title)
	}
	want := []string{
		"c1 Positive living: Kindness at work",
		"c2 Stoic principles: Seneca on time",
		"c3 Science: Sleep consolidates skills",
		"c4 Science: A telescope sees the first galaxies",
	}
	if !reflect.DeepEqual(got, want) || len(g.Interests) != 3 || !reflect.DeepEqual(g.FailedFeeds, []string{srv.URL + "/broken"}) {
		t.Fatalf("gather: %q interests=%d failed=%v", got, len(g.Interests), g.FailedFeeds)
	}
	if c := g.Candidates[0]; c.Published != "2026-10-12" || c.Content != "About Kindness at work." || c.Source != "Feed /positive" {
		t.Fatalf("candidate = %+v", c)
	}

	// rank: one structured-output turn per interest, with its rubric; each
	// keeps the judgements of its own candidates.
	r, err := d.rankDigest(ctx, in, g)
	if err != nil || len(r.Judgements) != 4 || len(r.Failed) != 0 {
		t.Fatalf("rank: %+v %v", r, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--json-schema") || strings.Contains(string(args), "--mcp-config") ||
		!strings.Contains(string(args), "--no-session-persistence") || strings.Contains(string(args), "--allowedTools") {
		t.Fatalf("rank args: %s", args)
	}
	if calls := strings.Count(string(args), "--json-schema"); calls != 3 {
		t.Fatalf("rank turns = %d, want one per interest with candidates", calls)
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	for _, want := range []string{"1. positivity -- does it leave the reader uplifted", "1. practical wisdom -- can the reader live by it",
		`from the feeds of their interest "Science"`, `<candidate id="c3">`, "The coverage\nday is 2026-10-13."} {
		if !strings.Contains(string(stdin), want) {
			t.Fatalf("rank prompt lacks %q:\n%s", want, stdin)
		}
	}

	// When every interest's ranking fails, rank fails, to be retried.
	ok := d.Agent.ClaudeBinary
	d.Agent.ClaudeBinary = "false"
	if bad, err := d.rankDigest(ctx, in, g); err == nil || len(bad.Failed) != 3 {
		t.Fatalf("all rankings failing: %+v %v", bad, err)
	}
	d.Agent.ClaudeBinary = ok

	// A post whose slot is already reserved, as when a run that posted is
	// replayed, posts nothing.
	taken := DigestInput{UserID: u.ID, Slot: "2026-10-13 04"}
	if _, ok, err := d.Store.ReserveDigest(ctx, u.ID, "G1", taken.Slot); err != nil || !ok {
		t.Fatal(ok, err)
	}
	posts := len(fs.texts)
	if again, err := d.postDigest(ctx, taken, chk, g, r); err != nil || !strings.Contains(again.Skipped, "already posted") || len(fs.texts) != posts {
		t.Fatalf("post on a reserved slot: %+v %v", again, err)
	}
	// When Slack fails, the slot is given back, so a retry can post.
	fs.failPosts = true
	if _, err := d.postDigest(ctx, in, chk, g, r); err == nil {
		t.Fatal("a failed post succeeded")
	}
	fs.failPosts = false
	if taken, _ := d.Store.DigestPosted(ctx, u.ID, in.Slot); taken {
		t.Fatal("a failed post kept its slot")
	}

	// post: the included items with a mean of 5 or more, best first, marked
	// as sent with their stories.
	post, err := d.postDigest(ctx, in, chk, g, r)
	if err != nil || post.Items != 3 {
		t.Fatalf("post: %+v %v", post, err)
	}
	msg := fs.texts[len(fs.texts)-1]
	wantMsg := "*News shorts · 16:00*\n\n" +
		"*<https://ex.org/sleep?utm_source=rss|Sleep consolidates skills>*\nA night's sleep locks in newly practised skills.\n_Science_\n\n" +
		"*<https://ex.org/telescope|A telescope sees the first galaxies>*\nA telescope sees &lt;the first&gt; galaxies.\n_Science_\n\n" +
		"*<https://ex.org/kindness|Kindness at work>*\nSmall kindnesses at work lift everyone &amp; last.\n_Positive living_"
	if msg != wantMsg {
		t.Fatalf("posted %q,\nwant %q", msg, wantMsg)
	}
	if th, _ := d.Store.GetThread(ctx, "G1", post.TS); th == nil || th.UserID != u.ID {
		t.Fatalf("thread = %+v", th)
	}
	if st, _ := d.Store.SentStoriesSince(ctx, u.ID, d.now().Add(-time.Hour)); len(st) != 3 {
		t.Fatalf("sent stories = %v", st)
	}
	if chk, _ := d.checkDigest(ctx, in); !strings.Contains(chk.Skip, "already posted") {
		t.Fatalf("same slot: %+v", chk)
	}

	// Feedback: only the owner's reactions count, each event once.
	react := func(id, user, emoji string) slack.Envelope {
		return slack.Envelope{TeamID: "T1", EventID: id, Event: slack.Event{Type: "reaction_added", User: user, Reaction: emoji,
			Item: &slack.ItemRef{Type: "message", Channel: "G1", TS: post.TS}}}
	}
	for _, c := range []struct {
		env  slack.Envelope
		want int
		skip string
	}{
		{react("Ev1", "U1", "fire"), 1, ""},
		{react("Ev1", "U1", "fire"), 0, "duplicate"},
		{react("Ev2", "U2", "fire"), 0, "owner"},
	} {
		out, err := d.RecordReaction(ctx, c.env)
		if err != nil || out.Value != c.want || !strings.Contains(out.Skipped, c.skip) {
			t.Errorf("reaction %s: %+v %v", c.env.EventID, out, err)
		}
	}

	// The next slot: the sent items are gone; the one scored too low stays.
	d.Now = func() time.Time { return time.Date(2026, 10, 14, 16, 0, 2, 0, d.Location) }
	g, err = d.gatherDigest(ctx, DigestInput{UserID: u.ID, Slot: "2026-10-14 16"})
	if err != nil || len(g.Candidates) != 1 || g.Candidates[0].Title != "Seneca on time" || g.Candidates[0].ID != "c1" {
		t.Fatalf("next gather: %+v %v", g.Candidates, err)
	}
	// The next ranking is given the stories sent, to reuse their slugs.
	os.Remove(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	if _, err := d.rankDigest(ctx, DigestInput{UserID: u.ID, Slot: "2026-10-14 16"}, g); err != nil {
		t.Fatal(err)
	}
	stdin, _ = os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	if !strings.Contains(string(stdin), "- sleep-consolidates-skills: Sleep consolidates skills") {
		t.Fatalf("next rank prompt lacks the sent stories:\n%s", stdin)
	}

	// With nothing good enough, nothing is posted.
	posts = len(fs.texts)
	if out, err := d.postDigest(ctx, DigestInput{UserID: u.ID, Slot: "2026-10-14 16"}, chk, g, digestRank{}); err != nil || out.Skipped == "" || len(fs.texts) != posts {
		t.Fatalf("empty post: %+v %v", out, err)
	}

	// When every feed is down, gather fails, to be retried.
	for name := range paths {
		storetest.SetSources(t, d.Store, name, srv.URL+"/broken")
	}
	if _, err := d.gatherDigest(ctx, in); err == nil || !strings.Contains(err.Error(), "no feed could be read") {
		t.Fatalf("all feeds down: %v", err)
	}
}

func TestPickItems(t *testing.T) {
	var cands []agent.DigestCandidate
	interests := []string{"Science", "Science", "Science", "Science", "Stoic principles", "Healthy eating", "Stoic principles", "Stoic principles"}
	for i, in := range interests {
		id := "c" + strconv.Itoa(i+1)
		cands = append(cands, agent.DigestCandidate{ID: id, Interest: in, Title: id, URL: "https://x/" + id})
	}
	judge := func(id, story, category string, include bool, scores ...int) agent.Judgement {
		j := agent.Judgement{ID: id, Story: story, Category: category, Include: include, Summary: "s"}
		for _, s := range scores {
			j.Scores = append(j.Scores, agent.CriterionScore{Name: "x", Score: s})
		}
		return j
	}
	r := digestRank{Judgements: []agent.Judgement{
		judge("c1", "a", "research", true, 9, 9),
		judge("c2", "a", "", true, 8, 9),         // the same story as c1
		judge("c3", "b", "research", true, 8, 8), // a second Science research item
		judge("c4", "c", "infra", true, 7, 7),
		judge("c5", "d", "", false, 7, 8),   // not included
		judge("c6", "e", "", true, 4, 5),    // a mean under 5
		judge("c7", "sent", "", true, 6, 6), // a story sent lately
		judge("c8", "f", "", true, 6, 7),
	}}
	sent := map[string]bool{"sent": true}
	pick := func(signals []store.InterestSignal) (ids []string) {
		for _, it := range pickItems(digestGather{Candidates: cands}, r, signals, sent) {
			ids = append(ids, it.ID)
		}
		return ids
	}
	if got := pick(nil); !reflect.DeepEqual(got, []string{"c1", "c4", "c8"}) {
		t.Errorf("no feedback: %v", got)
	}
	// Liked interests move up, disliked ones down.
	sig := []store.InterestSignal{{Interest: "Stoic principles", Liked: 3}, {Interest: "Science", Disliked: 2}}
	if got := pick(sig); !reflect.DeepEqual(got, []string{"c1", "c8", "c4"}) {
		t.Errorf("with feedback: %v", got)
	}
}

func TestProcessNewsChannel(t *testing.T) {
	ctx := context.Background()
	d, _, argsLog := setup(t)
	withMemory(t, &d)
	d.Agent.ToolsServer = []string{"/opt/mcp", "--data", "/srv/data"}
	if _, err := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); err != nil {
		t.Fatal(err)
	}
	owner := identity.UserID("owner@x.com")
	// user-setup gave them their news channel at first contact.
	ch, err := d.Store.NewsChannelOf(ctx, owner)
	if err != nil || ch == "" {
		t.Fatalf("news channel %q, %v", ch, err)
	}
	storetest.Seed(t, d.Store)
	if err := d.Store.FollowInterest(ctx, owner, "Positive living"); err != nil {
		t.Fatal(err)
	}
	dgID, _, err := d.Store.ReserveDigest(ctx, owner, ch, "2026-10-13 08")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Store.CompleteDigest(ctx, store.Digest{ID: dgID, UserID: owner, TS: "5.0", Text: "1. Sleep consolidates skills", PostedAt: time.Now()},
		[]store.DigestItem{{Interest: "Science", Title: "Sleep", URL: "https://x.org"}}); err != nil {
		t.Fatal(err)
	}
	msg := func(id, user, ts, threadTS string) slack.Envelope {
		return slack.Envelope{TeamID: "T1", EventID: id, Event: slack.Event{
			Type: "message", ChannelType: "group", User: user, Text: "I'm into oscillatory neural networks", Channel: ch, TS: ts, ThreadTS: threadTS,
		}}
	}
	prompt := func() string { b, _ := os.ReadFile(argsLog); return string(b) }

	os.Remove(argsLog)
	if out, err := d.Process(ctx, "r1", msg("Ev1", "U1", "6.0", "")); err != nil || out.Skipped != "" {
		t.Fatalf("owner message: %+v %v", out, err)
	}
	if !strings.Contains(prompt(), "news-shorts channel") || strings.Contains(prompt(), "<digest>") {
		t.Fatal("top-level message: no news prompt, or a digest")
	}
	// The turn gets the interests in its prompt and the tools pinned to the user.
	for _, want := range []string{"- Positive living", "follow_interest",
		`"roamind":{"args":["--data","/srv/data","--user","` + owner + `","--tools","interests,jobs"],"command":"/opt/mcp"`, "mcp__roamind"} {
		if !strings.Contains(prompt(), want) {
			t.Errorf("turn lacks %s:\n%s", want, prompt())
		}
	}
	if out, _ := d.Process(ctx, "r2", msg("Ev2", "U2", "6.1", "")); !strings.Contains(out.Skipped, "not addressed") {
		t.Fatalf("other user: %+v", out)
	}
	news := mention("Ev3", "U1", "6.2", "")
	news.Event.Channel = ch
	if out, _ := d.Process(ctx, "r3", news); !strings.Contains(out.Skipped, "handled as its message") {
		t.Fatalf("owner mention: %+v", out)
	}

	// A reply in a digest's thread brings the digest and counts as liking it.
	if err := d.Store.SaveThread(ctx, store.Thread{ChannelID: ch, ThreadTS: "5.0", UserID: owner}); err != nil {
		t.Fatal(err)
	}
	os.Remove(argsLog)
	if out, err := d.Process(ctx, "r4", msg("Ev4", "U1", "6.3", "5.0")); err != nil || out.Skipped != "" {
		t.Fatalf("reply: %+v %v", out, err)
	}
	if !strings.Contains(prompt(), "1. Sleep consolidates skills") {
		t.Fatal("reply: digest not in prompt")
	}
	sigs, err := d.Store.InterestSignals(ctx, owner, time.Now().Add(-time.Hour))
	if err != nil || len(sigs) != 1 || sigs[0].Liked != 1 {
		t.Fatalf("signals = %+v %v (digest %d)", sigs, err, dgID)
	}
}

// A failed invite is retried with the channel already made: the user is
// added, and no second channel is created.
func TestSyncNewsRetriesInvite(t *testing.T) {
	ctx := context.Background()
	d, fs, u := newsSetup(t)
	fs.failInvites = 1
	if _, err := d.SyncNews(ctx, u.ID); err == nil {
		t.Fatal("failed invite not reported")
	}
	if out, err := d.SyncNews(ctx, u.ID); err != nil || out.Created != "" {
		t.Fatalf("retry: %+v %v", out, err)
	}
	if len(fs.created) != 1 || fs.invites["G1"] != "U1" {
		t.Fatalf("created %v, invites %v", fs.created, fs.invites)
	}
}
