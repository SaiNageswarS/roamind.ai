package store

import (
	"context"
	"testing"
	"time"

	"roamind.ai/memory/finance"
)

func TestFinAccounts(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	a1, created, err := s.EnsureFinAccount(ctx, "a", " Zerodha ", "XX1234", "inr")
	if err != nil || !created || a1.ID != "a1" || a1.Broker != "zerodha" || a1.Currency != "INR" {
		t.Fatalf("first account = %+v %v %v", a1, created, err)
	}
	again, created, err := s.EnsureFinAccount(ctx, "a", "ZERODHA", "XX1234", "")
	if err != nil || created || again != a1 {
		t.Fatalf("same broker and number = %+v %v %v", again, created, err)
	}
	a2, _, _ := s.EnsureFinAccount(ctx, "a", "zerodha", "XX9999", "")
	b1, _, _ := s.EnsureFinAccount(ctx, "b", "zerodha", "XX1234", "")
	if a2.ID != "a2" || b1.ID != "a1" || b1.UserID != "b" {
		t.Fatalf("a2 = %+v, b's = %+v", a2, b1)
	}
	if ok, err := s.NameFinAccount(ctx, "a", "a2", "Kite"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := s.NameFinAccount(ctx, "b", "a2", "x"); ok {
		t.Error("b named a's account")
	}
	if got, _ := s.FinAccountOf(ctx, "a", "kite"); got == nil || got.ID != "a2" || got.Label() != "Kite" {
		t.Errorf("by name = %+v", got)
	}
	if got, _ := s.FinAccountOf(ctx, "a", "A1"); got == nil || got.Label() != "zerodha XX1234" {
		t.Errorf("by id = %+v", got)
	}
	if got, _ := s.FinAccountOf(ctx, "b", "kite"); got != nil {
		t.Errorf("b sees a's account %+v", got)
	}
	if as, _ := s.FinAccounts(ctx, "a"); len(as) != 2 {
		t.Errorf("a's accounts = %+v", as)
	}
}

func TestLedger(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	acct, _, err := s.EnsureFinAccount(ctx, "a", "broker", "X1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.EnsureFinAccount(ctx, "b", "broker", "X1", ""); err != nil {
		t.Fatal(err)
	}
	if u, err := s.FinanceUploadOf(ctx, "a", "h1"); u != nil || err != nil {
		t.Fatal(u, err)
	}
	const isin = "US0378331005"
	report := []finance.Row{
		{Date: "2026-01-05", ISIN: isin, Name: "Example  Inc", Side: "buy", Quantity: "10", Price: "100", Amount: "1000", Fees: "2", Currency: "usd"},
		{Date: "2026-02-05", ISIN: isin, Side: "buy", Quantity: "5", Price: "120", Amount: "600", Fees: "1", Currency: "USD"},
		{Date: "2026-03-05", ISIN: isin, Side: "sell", Quantity: "12", Price: "130", Amount: "1560", Fees: "3", Currency: "USD"},
		{Date: "2026-03-10", Symbol: "zz", Side: "sell", Quantity: "4", Price: "10", Currency: "USD"},
		{Date: "2026-03-20", ISIN: isin, Side: "dividend", Amount: "1.5", TaxWithheld: "0.3", Currency: "USD"},
	}
	up := FinanceUpload{UserID: "a", SHA256: "h1", File: "tb.csv", Account: acct.ID, Doc: "tradebook", PeriodFrom: "2026-01-01", PeriodTo: "2026-03-31"}
	un, err := s.ImportFinanceReport(ctx, up, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(un) != 1 || un[0].String() != "2026-03-10: 4 of ZZ" {
		t.Errorf("unmatched = %v", un)
	}
	if _, err := s.ImportFinanceReport(ctx, up, report); err == nil {
		t.Error("the same file was imported twice")
	}
	if u, _ := s.FinanceUploadOf(ctx, "a", "h1"); u == nil || u.PeriodTo != "2026-03-31" || u.Rows != 5 {
		t.Errorf("upload = %+v", u)
	}
	if u, _ := s.FinanceUploadOf(ctx, "b", "h1"); u != nil {
		t.Error("b sees a's upload")
	}

	// The first buy is sold whole, the second in part: 3 of its 5 are open.
	type lot struct{ Date, Quantity, Cost string }
	lots := func(user string) []lot {
		t.Helper()
		rows, err := s.db.QueryContext(ctx, `SELECT trade_date::text, quantity::float8::text, round(cost, 2)::text
			FROM fin_open_lots WHERE user_id = $1 ORDER BY trade_date`, user)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []lot
		for rows.Next() {
			var l lot
			if err := rows.Scan(&l.Date, &l.Quantity, &l.Cost); err != nil {
				t.Fatal(err)
			}
			out = append(out, l)
		}
		return out
	}
	if got := lots("a"); len(got) != 1 || got[0] != (lot{"2026-02-05", "3", "360.60"}) {
		t.Errorf("open lots = %v", got)
	}
	var gain string
	if err := s.db.QueryRowContext(ctx, `SELECT round(SUM(gain), 2)::text FROM fin_realized WHERE user_id = 'a' AND instrument = $1`, isin).Scan(&gain); err != nil {
		t.Fatal(err)
	}
	// Proceeds 1557, less cost 1002 for 10 and 2/5 of 601 for 2.
	if gain != "314.60" {
		t.Errorf("gain = %s", gain)
	}

	// A mail of a day the report covers, with a side it has, isn't counted;
	// one after the report's period is, and is matched.
	mail := []finance.Row{
		{Date: "2026-03-05", ISIN: isin, Side: "sell", Quantity: "12", Price: "130", Currency: "USD"},
		{Date: "2026-04-02", ISIN: isin, Side: "sell", Quantity: "1", Price: "140", Currency: "USD"},
	}
	if _, err := s.SaveFinanceMail(ctx, "a", acct.ID, "mail:m1", finance.DocConfirmation, mail); err != nil {
		t.Fatal(err)
	}
	var counted []bool
	rows, _ := s.db.QueryContext(ctx, `SELECT counted FROM fin_transactions WHERE source = 'mail:m1' ORDER BY line`)
	for rows.Next() {
		var c bool
		_ = rows.Scan(&c)
		counted = append(counted, c)
	}
	rows.Close()
	if len(counted) != 2 || counted[0] || !counted[1] {
		t.Errorf("mail rows counted = %v", counted)
	}
	if got := lots("a"); len(got) != 1 || got[0].Quantity != "2" {
		t.Errorf("open lots after the mail = %v", got)
	}
	// A mail written again replaces its rows.
	if _, err := s.SaveFinanceMail(ctx, "a", acct.ID, "mail:m1", finance.DocConfirmation, mail[:1]); err != nil {
		t.Fatal(err)
	}
	if got := lots("a"); len(got) != 1 || got[0].Quantity != "3" {
		t.Errorf("open lots after the mail is rewritten = %v", got)
	}
	if got := lots("b"); len(got) != 0 {
		t.Errorf("b sees a's lots: %v", got)
	}
}

func TestFinanceSenders(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	jobsUsers(t, s, "a", "b")
	if err := s.SeenFinanceSender(ctx, "a", "Notices.Broker.Example", "Trade confirmation", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.TrustedFinanceDomain(ctx, "a", "notices.broker.example"); d != "" {
		t.Errorf("a sender only seen is trusted: %q", d)
	}
	if err := s.TrustFinanceSender(ctx, "a", "broker.example", true); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.TrustedFinanceDomain(ctx, "a", "notices.broker.example"); d != "broker.example" {
		t.Errorf("a subdomain of a trusted domain: %q", d)
	}
	for _, other := range []string{"broker.example.evil", "evilbroker.example", "example"} {
		if d, _ := s.TrustedFinanceDomain(ctx, "a", other); d != "" {
			t.Errorf("%s is trusted as %q", other, d)
		}
	}
	if d, _ := s.TrustedFinanceDomain(ctx, "b", "broker.example"); d != "" {
		t.Error("b trusts a's sender")
	}
	// Seeing it again keeps it trusted.
	if err := s.SeenFinanceSender(ctx, "a", "broker.example", "Dividend", time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	fs, err := s.FinanceSenders(ctx, "a")
	if err != nil || len(fs) != 2 || fs[0].Domain != "broker.example" || !fs[0].Trusted || fs[0].Subject != "Dividend" ||
		fs[1].Domain != "notices.broker.example" || fs[1].Trusted {
		t.Errorf("senders = %+v %v", fs, err)
	}
	if err := s.TrustFinanceSender(ctx, "a", "broker.example", false); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.TrustedFinanceDomain(ctx, "a", "broker.example"); d != "" {
		t.Error("still trusted after untrusting")
	}
}
