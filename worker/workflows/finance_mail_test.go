package workflows

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"roamind.ai/agent"
	"roamind.ai/memory/finance"
	"roamind.ai/memory/finance/financetest"
	"roamind.ai/memory/store"
)

func TestSignedBy(t *testing.T) {
	for _, c := range []struct {
		auth string
		want bool
	}{
		{"mx.google.com; dkim=pass header.i=@broker.example header.s=s1 header.b=x; spf=pass smtp.mailfrom=b.example", true},
		{"mx.google.com; dkim=pass header.i=@mail.broker.example; spf=pass", true},
		{"mx.google.com; dkim=pass header.d=broker.example", true},
		{"mx.google.com; dkim=none; dmarc=pass (p=NONE) header.from=broker.example", true},
		// Signed by someone else, failed, or a header the sender added.
		{"mx.google.com; dkim=pass header.i=@evil.example; dmarc=fail header.from=broker.example", false},
		{"mx.google.com; dkim=fail header.i=@broker.example", false},
		{"mx.google.com; dkim=pass header.i=@notbroker.example", false},
		{"evil.example; dkim=pass header.i=@broker.example", false},
		{"", false},
	} {
		if got := signedBy(c.auth, "broker.example"); got != c.want {
			t.Errorf("signedBy(%q) = %v, want %v", c.auth, got, c.want)
		}
	}
	if d := senderDomain("Broker <Notices@Mail.Broker.Example>"); d != "mail.broker.example" {
		t.Errorf("sender domain = %q", d)
	}
}

func TestBrokerMail(t *testing.T) {
	addr, stop, err := financetest.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if addr == "" {
		t.Skip("no QuestDB in bin/ (task questdb-install)")
	}
	ctx := context.Background()
	db, err := finance.Open(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	schemas, _ := financetest.Schemas()
	if err := db.Init(ctx, schemas); err != nil {
		t.Fatal(err)
	}

	d, fs, _ := setup(t)
	bin, argsLog := fakeClaudeWith(t, "testdata/extract_trades.jsonl")
	d.Agent.ClaudeBinary, d.Finance = bin, db
	now := time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	var mailed []string
	d.FinanceMailed = func(_ context.Context, u *store.User) error { mailed = append(mailed, u.ID); return nil }
	// The user has no memory: broker mail doesn't need it.
	if err := d.Store.UpsertSlackUser(ctx, store.User{ID: "f1", Email: "sam@x.com", DisplayName: "Sam", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	secret, err := d.Store.ConnectMail(ctx, "sam@gmail.com", "f1")
	if err != nil {
		t.Fatal(err)
	}
	// TCS is known by its listing.
	if err := db.PutListings(ctx, []finance.Listing{{ISIN: "INE467B01029", MIC: "NSE", Symbol: "TCS", Currency: "INR"}}); err != nil {
		t.Fatal(err)
	}
	signed := "mx.google.com; dkim=pass header.i=@broker.example header.s=s1; spf=pass"
	msg := MailInput{MailMessage: agent.MailMessage{Address: "sam@gmail.com", MessageID: "cn1", From: "Example Broker <notices@mail.broker.example>",
		Date: "2026-11-03T17:30:00.000Z", Subject: "Contract note for 3 Nov", Link: "https://mail.google.com/mail/u/0/#all/cn1",
		Category: "finance", Text: "Contract note attached", AuthResults: signed}}
	msg.Signature = mailSignature(secret, msg)

	// Not trusted yet: listed, and not read.
	if chk, err := d.checkMail(ctx, msg); err != nil || !strings.Contains(chk.Skip, "doesn't trust") {
		t.Fatalf("untrusted = %+v %v", chk, err)
	}
	if fs, _ := d.Store.FinanceSenders(ctx, "f1"); len(fs) != 1 || fs[0].Domain != "mail.broker.example" || fs[0].Trusted {
		t.Fatalf("senders = %+v", fs)
	}
	if err := d.Store.TrustFinanceSender(ctx, "f1", "broker.example", true); err != nil {
		t.Fatal(err)
	}
	// Trusted, but not signed by it.
	forged := msg
	forged.AuthResults = "mx.google.com; dkim=pass header.i=@evil.example; spf=pass"
	if chk, err := d.checkMail(ctx, forged); err != nil || !strings.Contains(chk.Skip, "didn't sign") {
		t.Fatalf("forged = %+v %v", chk, err)
	}

	// The finance channel exists, for what can't be read.
	if _, _, err := d.Store.EnsureFinAccount(ctx, "f1", "example broker", "XX1234", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.financeChannel(ctx, "f1"); err != nil {
		t.Fatal(err)
	}
	chk, err := d.checkMail(ctx, msg)
	if err != nil || chk.Skip != "" || chk.Trusted != "broker.example" {
		t.Fatalf("trusted = %+v %v", chk, err)
	}
	os.Remove(argsLog)
	rel, err := d.extractTrades(ctx, msg, chk)
	if err != nil || rel.Trades == nil || len(rel.Trades.Rows) != 3 {
		t.Fatalf("extract = %+v %v", rel, err)
	}
	if args, _ := os.ReadFile(argsLog); !strings.Contains(string(args), "--model "+agent.ExtractModel) {
		t.Errorf("extract didn't use the small model: %s", args)
	}
	posts := len(fs.texts)
	saved, err := d.saveTrades(ctx, msg, chk, rel.Trades)
	if err != nil || saved.Trades != 2 || len(saved.Dropped) != 1 {
		t.Fatalf("save = %+v %v", saved, err)
	}
	if len(fs.texts) != posts+1 || !strings.Contains(fs.texts[posts], "couldn't read 1 of the trades") || !strings.Contains(fs.texts[posts], "NOSUCH") {
		t.Errorf("posts = %q", fs.texts[posts:])
	}
	if len(mailed) != 1 {
		t.Errorf("after mail = %v", mailed)
	}
	if err := db.WaitApplied(ctx, "trade_events"); err != nil {
		t.Fatal(err)
	}
	evs, err := db.TradeEvents(ctx, "f1", now.AddDate(0, 0, -1), now.AddDate(0, 0, 1))
	if err != nil || len(evs) != 2 {
		t.Fatalf("events = %+v %v", evs, err)
	}
	if e := evs[1]; e.ISIN != "INE467B01029" || e.Account != "a1" || e.Source != "mail:sam@gmail.com/cn1" || e.Doc != finance.DocContractNote {
		t.Errorf("TCS = %+v", e)
	}
	if st, _ := d.Store.MessageStatus(ctx, "sam@gmail.com", "cn1"); st != store.MailDone {
		t.Errorf("status = %q", st)
	}
}

func TestBrokerMailAfterMemory(t *testing.T) {
	// A broker's mail read into memory before its domain was trusted goes
	// to the ledger when sent again; sent again after that, it is skipped.
	d, _, _ := setup(t)
	ctx := context.Background()
	if err := d.Store.UpsertSlackUser(ctx, store.User{ID: "f1", Email: "sam@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Store.ConnectMail(ctx, "sam@gmail.com", "f1"); err != nil {
		t.Fatal(err)
	}
	m := store.MailMessage{Address: "sam@gmail.com", MessageID: "m1", UserID: "f1", Category: "other", Subject: "s", Link: "l"}
	if ok, _ := d.Store.PickMessage(ctx, m, time.Time{}); !ok {
		t.Fatal("first pick")
	}
	d.Store.FinishMessage(ctx, "sam@gmail.com", "m1", store.MailDone)
	m.Category = "finance"
	if ok, _ := d.Store.PickMessage(ctx, m, time.Time{}); !ok {
		t.Error("not picked again for the ledger")
	}
	d.Store.FinishMessage(ctx, "sam@gmail.com", "m1", store.MailDone)
	if ok, _ := d.Store.PickMessage(ctx, m, time.Time{}); ok {
		t.Error("picked for the ledger twice")
	}
}
