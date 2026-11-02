package workflows

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roamind.ai/agent"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

func TestMailMessage(t *testing.T) {
	ctx := context.Background()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	d, _, _ := setup(t)
	bin, argsLog := fakeClaudeWith(t, "testdata/relate_mail.jsonl")
	d.Agent.ClaudeBinary = bin
	mem, err := notes.New(t.TempDir(), "../../memory", fakeBM(t))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, berlin)
	d.Memory, d.Agent.Memory, d.Location, d.Now = mem, mem, berlin, func() time.Time { return now }

	if err := d.Store.UpsertSlackUser(ctx, store.User{ID: "h1", Email: "sai@x.com", DisplayName: "Sai", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	p, err := d.Store.AssignMemoryProject(ctx, "h1", "bm-u-h1")
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Setup(p); err != nil {
		t.Fatal(err)
	}
	root := mem.NotesDir(p)
	existing := map[string]string{
		"events/2026/Parents' flight to Hyderabad.md": "---\ntitle: Flight to Hyderabad\ntype: event\npermalink: " + p +
			"/events/2026/parents-flight-to-hyderabad\n---\n\nParents' return flight to India.\n\n## Observations\n- [date] 2026-11-12T09:15\n",
		"people/Etihad.md": "---\ntitle: Etihad\ntype: person\npermalink: " + p + "/people/etihad\n---\n\n- [contact] no-reply@etihad.com\n",
		"goals/Plan parents' return travel.md": "---\ntitle: Plan parents' return travel\ntype: goal\npermalink: " + p +
			"/goals/plan-parents-return-travel\n---\n\n- [status] active\n",
	}
	for name, body := range existing {
		path := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	msg := MailInput{MailMessage: agent.MailMessage{Address: "Sai@Gmail.com", MessageID: "18c1", From: "Etihad <no-reply@etihad.com>",
		To: "sai@gmail.com", Subject: "Your flight has changed", Link: "https://mail.google.com/mail/u/0/#all/18c1",
		Category: "travel", Summary: "EY 134 moved to 10:15", Text: "Your flight EY 134 now departs at 10:15."}}

	// check: an account not connected is dropped.
	if chk, err := d.checkMail(ctx, msg); err != nil || chk.Skip != "the account Sai@Gmail.com is not connected" {
		t.Fatalf("unconnected = %+v %v", chk, err)
	}
	if err := d.Store.ConnectMail(ctx, "sai@gmail.com", "h1"); err != nil {
		t.Fatal(err)
	}
	// A heartbeat only records that the script runs.
	if chk, err := d.checkMail(ctx, MailInput{MailMessage: agent.MailMessage{Address: "sai@gmail.com"}, Heartbeat: true}); err != nil || chk.Skip != "heartbeat" {
		t.Fatalf("heartbeat = %+v %v", chk, err)
	}
	if a, _ := d.Store.MailAccountOf(ctx, "sai@gmail.com"); a == nil || !a.SeenAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("seen = %+v", a)
	}
	chk, err := d.checkMail(ctx, msg)
	if err != nil || chk.Skip != "" || chk.UserID != "h1" {
		t.Fatalf("check = %+v %v", chk, err)
	}
	if again, _ := d.checkMail(ctx, msg); again.Skip != "the message was sent before" {
		t.Fatalf("second check = %+v", again)
	}

	// relate: structured output from a turn that reads memory and searches
	// the web only, given the mail and the active goals.
	rel, err := d.relateMail(ctx, msg, chk)
	if err != nil || len(rel.Related.Items) != 4 || rel.CostUSD != 0.03 {
		t.Fatalf("relate = %+v %v", rel, err)
	}
	args, _ := os.ReadFile(argsLog)
	for _, want := range []string{"--json-schema", `"--project","` + p + `"`, "--tools WebSearch --",
		"--allowedTools WebSearch,mcp__memory__read_note,mcp__memory__build_context,mcp__memory__search_notes "} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("relate args lack %q: %s", want, args)
		}
	}
	for _, deny := range []string{"mcp__roamind", "Bash", "WebFetch"} {
		if strings.Contains(string(args), deny) {
			t.Fatalf("relate args have %q: %s", deny, args)
		}
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	for _, want := range []string{"Gmail account Sai@Gmail.com", "2026-11-02, in Europe/Berlin",
		"- Plan parents' return travel (" + p + "/goals/plan-parents-return-travel)", "sorted it as travel: EY 134 moved to 10:15",
		"Your flight EY 134 now departs at 10:15.\n</mail>"} {
		if !strings.Contains(string(stdin), want) {
			t.Fatalf("prompt lacks %q:\n%s", want, stdin)
		}
	}

	// save: the flight's note is rewritten in place and its item moves;
	// the bill is a new task with its reminder; the known person and the
	// item in a wrong folder are dropped.
	sv, err := d.saveMail(ctx, msg, chk, rel)
	if err != nil || len(sv.Notes) != 2 || len(sv.Dropped) != 2 {
		t.Fatalf("save = %+v %v", sv, err)
	}
	if !strings.Contains(sv.Dropped[0], "already lists no-reply@etihad.com") || !strings.Contains(sv.Dropped[1], "is not where event notes go") {
		t.Fatalf("dropped = %q", sv.Dropped)
	}
	flight, err := os.ReadFile(filepath.Join(root, "events/2026/Parents' flight to Hyderabad.md"))
	if err != nil || !strings.Contains(string(flight), "permalink: "+p+"/events/2026/parents-flight-to-hyderabad\n") ||
		!strings.Contains(string(flight), "- [date] 2026-11-12T10:15 (observed, email, 2026-11-02)") {
		t.Fatalf("flight note = %s %v", flight, err)
	}
	if _, err := os.Stat(filepath.Join(root, "events/2026/Flight to Hyderabad.md")); err == nil {
		t.Fatal("the update made a second flight note")
	}
	bill, err := os.ReadFile(filepath.Join(root, "tasks/Pay Acme: INV-7.md"))
	if err != nil || !strings.HasPrefix(string(bill), "---\ntitle: 'Pay Acme: INV-7'\ntype: task\npermalink: "+p+"/tasks/pay-acme-inv-7\n---\n\nAcme's invoice") {
		t.Fatalf("bill note = %s %v", bill, err)
	}
	items, err := d.Store.Items(ctx, "h1", store.ItemFilter{})
	if err != nil || len(items) != 2 {
		t.Fatalf("items = %+v %v", items, err)
	}
	byKey := map[string]store.Item{}
	for _, it := range items {
		byKey[it.Key] = it
	}
	if f := byKey["7ZMZT6"]; f.Note != p+"/events/2026/parents-flight-to-hyderabad" || f.Date != "2026-11-12T10:15" ||
		f.EventKind != "travel" || f.ShowFrom != "2026-11-11" || f.Source != "mail:Sai@Gmail.com/18c1" {
		t.Fatalf("flight item = %+v", f)
	}
	if b := byKey["INV-7"]; b.Kind != store.ItemTask || b.Note != p+"/tasks/pay-acme-inv-7" || b.ShowFrom != "2026-11-17" || b.Until != "" {
		t.Fatalf("bill item = %+v", b)
	}
	if st, _ := d.Store.MessageStatus(ctx, "sai@gmail.com", "18c1"); st != store.MailDone {
		t.Fatalf("status = %q", st)
	}

	// A mail with nothing to save is skipped.
	msg.MessageID = "18c2"
	chk, _ = d.checkMail(ctx, msg)
	if sv, err := d.saveMail(ctx, msg, chk, mailRelate{Related: agent.RelatedMail{Skip: "a newsletter"}}); err != nil || len(sv.Notes) != 0 {
		t.Fatalf("skip save = %+v %v", sv, err)
	}
	if st, _ := d.Store.MessageStatus(ctx, "sai@gmail.com", "18c2"); st != store.MailSkipped {
		t.Fatalf("skipped status = %q", st)
	}
}

// A quiet mail account is in the inbox digest, even on a day with nothing
// due.
func TestInboxStaleMail(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setup(t)
	withMemory(t, &d)
	d.Location = time.UTC
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	if err := d.Store.UpsertSlackUser(ctx, store.User{ID: "h1", Email: "sai@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Store.AssignMemoryProject(ctx, "h1", "bm-u-h1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SaveChannel(ctx, store.Channel{UserID: "h1", Kind: store.KindInbox, ChannelID: "I1"}); err != nil {
		t.Fatal(err)
	}
	in := InboxInput{UserID: "h1", Day: "2026-11-12"}
	if err := d.Store.ConnectMail(ctx, "sai@gmail.com", "h1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SeenMail(ctx, "sai@gmail.com", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if chk, _ := d.checkInbox(ctx, in); chk.Skip != "nothing is due" {
		t.Fatalf("fresh account = %+v", chk)
	}
	if err := d.Store.SeenMail(ctx, "sai@gmail.com", now.Add(-MailStaleAfter-time.Hour)); err != nil {
		t.Fatal(err)
	}
	chk, err := d.checkInbox(ctx, in)
	if err != nil || chk.Skip != "" || len(chk.StaleMail) != 1 || chk.StaleMail[0] != "sai@gmail.com" || len(chk.Items) != 0 {
		t.Fatalf("stale account = %+v %v", chk, err)
	}
	prompt, err := agent.InboxDigestPrompt(agent.InboxDigest{UserName: "Sai", Date: in.Day, Weekday: "Thursday", StaleMail: chk.StaleMail})
	if err != nil || !strings.Contains(prompt, "None of their tasks or events is due today.") ||
		!strings.Contains(prompt, "Script sends their mail to Roamind:\n\n- sai@gmail.com") || strings.Contains(prompt, "from the items table") {
		t.Fatalf("prompt: %v\n%s", err, prompt)
	}
}
