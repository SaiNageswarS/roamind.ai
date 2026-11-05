package finance

import (
	"context"
	"testing"
	"time"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/storetest"
)

func TestReconcileUser(t *testing.T) {
	db := db(t)
	ctx := context.Background()
	st := storetest.Open(t)
	const user = "reconcile-user"
	if err := st.UpsertSlackUser(ctx, store.User{ID: user, Email: "r@x.com", SlackTeamID: "T", SlackUserID: "U"}); err != nil {
		t.Fatal(err)
	}
	const infy = "INE009A01021"
	buy := func(source, doc, qty string, on time.Time) TradeEvent {
		return TradeEvent{Date: on, Source: source, Line: 1, Account: "a1", ISIN: infy, Side: SideBuy,
			Quantity: qty, Price: "1500", Amount: DecText(mul(dec(qty), dec("1500")), 8), Currency: "INR", Doc: doc}
	}
	oct10, nov3 := d("2026-10-10"), d("2026-11-03")

	// Mail first: provisional.
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{buy("mail:me@x/m1", DocContractNote, "40", oct10), buy("mail:me@x/m2", DocContractNote, "5", nov3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkFinanceDirty(ctx, user); err != nil {
		t.Fatal(err)
	}
	res, err := ReconcileUser(ctx, db, st, user)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provisional != 2 || res.Confirmed != 0 {
		t.Fatalf("after mail: %+v", res)
	}
	if mark, _ := st.FinanceDirty(ctx, user); mark != 0 {
		t.Error("still dirty after the reconcile")
	}

	// A tradebook for October says 50, and holds 50 as of the 31st.
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{buy("upload:tb1", DocTradebook, "50", oct10)}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutHoldings(ctx, user, []Holding{{Date: d("2026-10-31"), Account: "a1", ISIN: infy, Quantity: "60", Source: "upload:tb1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveFinanceUpload(ctx, store.FinanceUpload{UserID: user, SHA256: "tb1", File: "tb.csv", Account: "a1", Doc: DocTradebook,
		PeriodFrom: "2026-10-01", PeriodTo: "2026-10-31", Rows: 1}); err != nil {
		t.Fatal(err)
	}
	if res, err = ReconcileUser(ctx, db, st, user); err != nil {
		t.Fatal(err)
	}
	if res.Confirmed != 1 || res.Provisional != 1 || len(res.Notices) != 1 || res.Notices[0].Kind != NoticeCorrected {
		t.Fatalf("after the tradebook: %+v", res)
	}
	// The report shows 60 on the 31st; its trades give 50.
	if len(res.Conflicts) != 1 || res.Conflicts[0].Detail != "the report of 2026-10-31 shows 60; the trades give 50" {
		t.Fatalf("conflicts = %+v", res.Conflicts)
	}

	pf, err := db.Portfolio(ctx, user, "EUR", d("2026-11-05"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Stocks) != 1 {
		t.Fatalf("stocks = %+v", pf.Stocks)
	}
	s := pf.Stocks[0]
	is(t, "quantity", s.Quantity, "55")
	if !s.Provisional || len(s.Points) != 2 || s.Points[0].State != Confirmed || s.Points[1].State != Provisional {
		t.Errorf("stock = %+v", s)
	}
	// No price or INR rate yet: the value is missing, and says why.
	if s.Value != nil || len(pf.Missing) == 0 {
		t.Errorf("value = %v, missing = %v", s.Value, pf.Missing)
	}

	if err := db.PutFX(ctx, []FX{{Date: d("2026-11-04"), Pair: "EURINR", Source: SourceECB, Rate: "100"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutPrices(ctx, []Price{{Date: d("2026-11-04"), ISIN: infy, MIC: "NSE", Close: "1600"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutListings(ctx, []Listing{{ISIN: infy, MIC: "NSE", Symbol: "INFY", Currency: "INR"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.WaitApplied(ctx, "fx", "prices", "listings"); err != nil {
		t.Fatal(err)
	}
	if pf, err = db.Portfolio(ctx, user, "EUR", d("2026-11-05")); err != nil {
		t.Fatal(err)
	}
	// 55 × 1600 / 100 = 880; the cost of 55 × 1500 at a rate of 100 only
	// exists for the November lot (no rate on 10 October).
	is(t, "value", pf.Stocks[0].Value, "880")
	if pf.Stocks[0].Cost != nil {
		t.Errorf("cost without an October rate = %v", pf.Stocks[0].Cost)
	}
}
