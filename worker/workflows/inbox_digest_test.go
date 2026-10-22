package workflows

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

func TestInboxDigest(t *testing.T) {
	ctx := context.Background()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	d, fs, _ := setup(t)
	bin, argsLog := fakeClaudeWith(t, "testdata/inbox_digest.jsonl")
	d.Agent.ClaudeBinary = bin
	mem, err := notes.New(t.TempDir(), "../../memory", "true")
	if err != nil {
		t.Fatal(err)
	}
	d.Memory, d.Agent.Memory, d.Location = mem, mem, berlin
	// The cron fires at 08:00 in Berlin.
	d.Now = func() time.Time { return time.Date(2026, 11, 12, 8, 0, 5, 0, berlin) }

	users := map[string]store.User{
		"h1": {ID: "h1", Email: "a@x.com", DisplayName: "Sai", SlackTeamID: "T1", SlackUserID: "U1"},
		"h2": {ID: "h2", Email: "b@x.com", DisplayName: "Kam", SlackTeamID: "T1", SlackUserID: "U2"},
	}
	for _, u := range users {
		if err := d.Store.UpsertSlackUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		p, err := d.Store.AssignMemoryProject(ctx, u.ID, "bm-u-"+u.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := mem.Setup(p); err != nil {
			t.Fatal(err)
		}
	}
	// A web user has no Slack, so no inbox channel.
	if err := d.Store.UpsertWebUser(ctx, store.User{ID: "w1", Email: "w@x.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Store.AssignMemoryProject(ctx, "w1", "bm-u-w1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SaveChannel(ctx, store.Channel{UserID: "h1", Kind: store.KindInbox, ChannelID: "I1"}); err != nil {
		t.Fatal(err)
	}

	// The dispatcher: a digest for h1, and user-setup for h2, who has no
	// inbox channel yet.
	out, digests, missing, err := d.dueInbox(ctx, d.now())
	if err != nil || out.Day != "2026-11-12" || !reflect.DeepEqual(digests, []InboxInput{{UserID: "h1", Day: "2026-11-12"}}) ||
		!reflect.DeepEqual(missing, []UserSetupInput{{UserID: "h2"}}) {
		t.Fatalf("due = %+v %+v %+v %v", out, digests, missing, err)
	}
	in := digests[0]

	if chk, err := d.checkInbox(ctx, InboxInput{UserID: "h2", Day: in.Day}); err != nil || chk.Skip != "the user has no inbox channel" {
		t.Fatalf("h2 = %+v %v", chk, err)
	}
	if chk, err := d.checkInbox(ctx, in); err != nil || chk.Skip != "nothing is due" {
		t.Fatalf("no items = %+v %v", chk, err)
	}

	put := func(key, kind, eventKind, date, end string) {
		t.Helper()
		from, until, err := store.InboxLeads(kind, eventKind, date, "", end)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Store.PutItem(ctx, store.Item{UserID: "h1", Key: key, Kind: kind, EventKind: eventKind, Note: "bm-u-h1-1/notes/" + key,
			Title: "title of " + key, Date: date, ShowFrom: from, Until: until, Source: store.SourceChat}); err != nil {
			t.Fatal(err)
		}
	}
	put("Pay Acme INV-7", store.ItemTask, "", "2026-11-11", "")            // overdue
	put("7ZMZT6", store.ItemEvent, "travel", "2026-11-13T09:15", "")       // tomorrow
	put("Renew passport", store.ItemTask, "", "2026-12-01", "")            // not yet
	put("Dentist", store.ItemEvent, "appointment", "2026-11-12T10:00", "") // today, but done
	put("Old trip", store.ItemEvent, "travel", "2026-11-01", "2026-11-05") // over
	if _, err := d.Store.CloseItem(ctx, "h1", "Dentist", store.ItemDone); err != nil {
		t.Fatal(err)
	}

	chk, err := d.checkInbox(ctx, in)
	if err != nil || chk.Skip != "" || chk.ChannelID != "I1" || len(chk.Items) != 2 {
		t.Fatalf("check = %+v %v", chk, err)
	}
	if bill, flight := chk.Items[0], chk.Items[1]; bill.Key != "Pay Acme INV-7" || !bill.Overdue || flight.Key != "7ZMZT6" ||
		flight.Overdue || flight.EventKind != "travel" || flight.Note != "bm-u-h1-1/notes/7ZMZT6" {
		t.Fatalf("items = %+v", chk.Items)
	}

	// draft: structured output from a turn that reads the user's memory and
	// searches the web only, with a saved session, given the due items.
	dr, err := d.draftInbox(ctx, in, chk)
	if err != nil || !strings.Contains(dr.Digest, "_7ZMZT6_") || dr.SessionID != "7a1e2b3c-inbox-session" {
		t.Fatalf("draft = %+v %v", dr, err)
	}
	args, _ := os.ReadFile(argsLog)
	for _, want := range []string{"--json-schema", `"--project","bm-u-h1-1"`, "--tools WebSearch --", "mcp__memory__write_note --system-prompt",
		"--allowedTools WebSearch,mcp__memory__read_note,mcp__memory__build_context,mcp__memory__search_notes "} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("draft args lack %q: %s", want, args)
		}
	}
	for _, deny := range []string{"--no-session-persistence", "mcp__roamind", "Bash", "WebFetch", "mcp__memory,", "write_note on your own"} {
		if strings.Contains(string(args), deny) {
			t.Fatalf("draft args have %q: %s", deny, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	for _, want := range []string{"today, Thursday 2026-11-12", "key: Pay Acme INV-7; task; 2026-11-11; overdue; note: bm-u-h1-1/notes/Pay Acme INV-7",
		"key: 7ZMZT6; event, travel; 2026-11-13T09:15; note:"} {
		if !strings.Contains(string(stdin), want) {
			t.Fatalf("prompt lacks %q:\n%s", want, stdin)
		}
	}
	if strings.Contains(string(stdin), "Renew passport") || strings.Contains(string(stdin), "Dentist") {
		t.Fatalf("prompt has items not due:\n%s", stdin)
	}

	// post: a failed post gives the day back.
	fs.failPosts = true
	if _, err := d.postInbox(ctx, in, chk, dr); err == nil {
		t.Fatal("no error")
	}
	if posted, _ := d.Store.InboxPosted(ctx, "h1", in.Day); posted {
		t.Fatal("a failed post kept the day")
	}
	fs.failPosts = false
	po, err := d.postInbox(ctx, in, chk, dr)
	if err != nil || po.TS == "" || fs.postsTo["I1"] != 1 || fs.threads[0] != "" || !strings.Contains(fs.texts[0], "*Overdue*") {
		t.Fatalf("post = %+v %v; posts %q", po, err, fs.texts)
	}
	th, err := d.Store.GetThread(ctx, "I1", po.TS)
	if err != nil || th == nil || th.UserID != "h1" || th.ClaudeSessionID != dr.SessionID {
		t.Fatalf("thread = %+v %v", th, err)
	}
	if po, err := d.postInbox(ctx, in, chk, dr); err != nil || po.Skipped == "" || fs.postsTo["I1"] != 1 {
		t.Fatalf("second post = %+v %v", po, err)
	}
	if chk, _ := d.checkInbox(ctx, in); chk.Skip != "the digest of 2026-11-12 is already posted" {
		t.Fatalf("after posting = %+v", chk)
	}
}

// A user set up before inbox channels existed gets their turn, and
// user-setup starts again for the channel.
func TestProcessSetupWithoutInboxChannel(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setup(t)
	withMemory(t, &d)
	full := d.SetupUser
	d.SetupUser = func(ctx context.Context, userID string) error {
		p, err := d.assignProject(ctx, userID)
		if err != nil {
			return err
		}
		if _, err := d.setUpMemory(p); err != nil {
			return err
		}
		_, err = d.SyncNews(ctx, userID) // no inbox-channel task then
		return err
	}
	if out, _ := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); out.Skipped != "setting up user" {
		t.Fatalf("first message: %+v", out)
	}
	d.SetupUser = full
	runs := countRuns(&d)
	out, err := d.Process(ctx, "r1", mention("Ev1", "U1", "2.0", ""))
	if err != nil || out.Skipped != "" {
		t.Fatalf("second message: %+v %v", out, err)
	}
	if ch, _ := d.Store.Channel(ctx, out.UserID, store.KindInbox, ""); ch == "" || *runs != 1 {
		t.Fatalf("inbox channel %q after %d user-setup runs", ch, *runs)
	}
}
