package finance

import (
	"math/big"
	"testing"
	"time"
)

func TestMatchFIFO(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	q := func(s string) *big.Rat { r, _ := ParseDec(s); return r }
	txns := []Txn{
		// Out of order on purpose: matching sorts by date.
		{ID: 5, Account: "a1", Instrument: "X", Date: d("2026-03-01"), Side: SideSell, Quantity: q("12")},
		{ID: 1, Account: "a1", Instrument: "X", Date: d("2026-01-01"), Side: SideBuy, Quantity: q("10")},
		{ID: 2, Account: "a1", Instrument: "X", Date: d("2026-02-01"), Side: SideVest, Quantity: q("5")},
		{ID: 3, Account: "a1", Instrument: "X", Date: d("2026-02-15"), Side: SideDividend, Quantity: nil},
		// Another account's shares of X are its own.
		{ID: 4, Account: "a2", Instrument: "X", Date: d("2025-01-01"), Side: SideBuy, Quantity: q("100")},
		// A day trade: the sell sorts after that day's buy.
		{ID: 6, Account: "a1", Instrument: "Y", Date: d("2026-04-01"), Side: SideSell, Quantity: q("1.5")},
		{ID: 7, Account: "a1", Instrument: "Y", Date: d("2026-04-01"), Side: SideBuy, Quantity: q("1")},
		{ID: 8, Account: "a1", Instrument: "X", Date: d("2026-05-01"), Side: SideTransferOut, Quantity: q("3")},
	}
	got := MatchFIFO(txns)
	want := []Match{
		{SellID: 5, BuyID: 1, Quantity: q("10")},
		{SellID: 5, BuyID: 2, Quantity: q("2")},
		{SellID: 8, BuyID: 2, Quantity: q("3")},
		{SellID: 6, BuyID: 7, Quantity: q("1")},
		{SellID: 6, BuyID: 0, Quantity: q("0.5")}, // sold beyond what was bought
	}
	if len(got) != len(want) {
		t.Fatalf("got %d matches, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		g := got[i]
		if g.SellID != w.SellID || g.BuyID != w.BuyID || g.Quantity.Cmp(w.Quantity) != 0 {
			t.Errorf("match %d = %d←%d %s, want %d←%d %s", i, g.SellID, g.BuyID, g.Quantity.FloatString(2), w.SellID, w.BuyID, w.Quantity.FloatString(2))
		}
	}
	if txns[0].ID != 5 {
		t.Error("MatchFIFO reordered its input")
	}
}
