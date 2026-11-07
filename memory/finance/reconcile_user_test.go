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

	// Mail first, then an October tradebook repeating the 10 October fill.
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{buy("mail:me@x/m1", DocContractNote, "50", oct10), buy("mail:me@x/m2", DocContractNote, "5", nov3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.MarkFinanceDirty(ctx, user); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileUser(ctx, db, st, user); err != nil || n != 2 {
		t.Fatalf("after mail: %d %v", n, err)
	}
	if mark, _ := st.FinanceDirty(ctx, user); mark != 0 {
		t.Error("still dirty after the reconcile")
	}
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{buy("upload:tb1", DocTradebook, "50", oct10)}); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileUser(ctx, db, st, user); err != nil || n != 2 {
		t.Fatalf("after the tradebook: %d %v", n, err)
	}
	trades, err := db.Trades(ctx, user)
	if err != nil || len(trades) != 2 || trades[0].Quantity != "50" || trades[0].Record != "upload:tb1" {
		t.Fatalf("trades = %+v %v", trades, err)
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
	if len(s.Points) != 2 {
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

func TestReconcileUserGoneGroups(t *testing.T) {
	db := db(t)
	ctx := context.Background()
	st := storetest.Open(t)
	const user = "gone-user"
	if err := st.UpsertSlackUser(ctx, store.User{ID: user, Email: "g@x.com", SlackTeamID: "T", SlackUserID: "UG"}); err != nil {
		t.Fatal(err)
	}
	ev := func(on time.Time, line int) TradeEvent {
		return TradeEvent{Date: on, Source: "mail:me@x/m9", Line: line, Account: "a1", ISIN: "INE009A01021", Side: SideBuy,
			Quantity: "5", Price: "1500", Amount: "7500", Currency: "INR", Doc: DocContractNote}
	}
	// A mail read as two days' trades, then read again as one.
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{ev(d("2026-10-01"), 1), ev(d("2026-10-02"), 2)}); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileUser(ctx, db, st, user); err != nil || n != 2 {
		t.Fatalf("first read: %d %v", n, err)
	}
	if err := db.AddTradeEvents(ctx, user, []TradeEvent{ev(d("2026-10-02"), 1)}); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileUser(ctx, db, st, user); err != nil || n != 1 {
		t.Fatalf("second read: %d %v", n, err)
	}
	trades, err := db.Trades(ctx, user)
	if err != nil || len(trades) != 1 || !trades[0].Date.Equal(d("2026-10-02")) {
		t.Errorf("trades = %+v %v; want only 2 October's", trades, err)
	}
}
