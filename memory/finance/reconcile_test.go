package finance

import (
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"
)

var nov3 = time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)

func ev(source string, line int, doc, qty, price, ref string) TradeEvent {
	return TradeEvent{Date: nov3, Source: source, Line: line, Account: "a1", ISIN: "INE009A01021", Side: SideBuy,
		Quantity: qty, Price: price, Currency: "INR", Ref: ref, Doc: doc}
}

func one(t *testing.T, trades []Trade) Trade {
	t.Helper()
	if len(trades) != 1 {
		t.Fatalf("trades = %+v, want one", trades)
	}
	return trades[0]
}

func TestReconcileMailCopiesCountOnce(t *testing.T) {
	// An "order executed" mail and the contract note for the same order.
	got := one(t, Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "50", "1512.40", "ord-7"),
		ev("mail:me@x/m2", 1, DocContractNote, "50", "1512.35", "ord-7"),
	}))
	if got.Quantity != "50" || got.AvgPrice != "1512.35" || got.Record != "mail:me@x/m2" {
		t.Errorf("trade = %+v; want the contract note's 50 @ 1512.35", got)
	}
}

func TestReconcileTwoOrdersOfOneDay(t *testing.T) {
	got := one(t, Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "10", "1500", "ord-1"),
		ev("mail:me@x/m2", 1, DocConfirmation, "10", "1500", "ord-2"),
		// A report's two rows are two executions, even when alike.
		ev("mail:me@x/m3", 1, DocContractNote, "10", "1500", ""),
		ev("mail:me@x/m3", 2, DocContractNote, "10", "1500", ""),
	}))
	if got.Quantity != "20" {
		t.Errorf("quantity = %s, want 20", got.Quantity)
	}
}

func TestReconcileReportAndMailCountOnce(t *testing.T) {
	got := one(t, Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocContractNote, "50", "1512.40", ""),
		ev("upload:tb", 3, DocTradebook, "50", "1512.35", ""),
		// A statement for the same month repeats the tradebook's row.
		ev("upload:st", 9, DocStatement, "50", "1512.30", ""),
	}))
	if got.Quantity != "50" || got.AvgPrice != "1512.35" || got.Record != "upload:tb" {
		t.Errorf("trade = %+v; want the tradebook's 50 @ 1512.35", got)
	}
	// The report's copy is kept even when mail has the same kind of
	// document.
	got = one(t, Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocContractNote, "50", "1512.40", ""),
		ev("upload:cn", 1, DocContractNote, "50", "1512.35", ""),
	}))
	if got.Record != "upload:cn" {
		t.Errorf("kept %s, want the upload", got.Record)
	}
}

func TestReconcileKeepsWhatOnlyOneSourceHas(t *testing.T) {
	// Mail's fill a report doesn't have is still a fill: nothing is dropped.
	got := one(t, Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "5", "1400", ""),
		ev("upload:tb", 1, DocTradebook, "50", "1512.35", ""),
	}))
	if got.Quantity != "55" {
		t.Errorf("quantity = %s, want 55", got.Quantity)
	}
}

func TestReconcileDividends(t *testing.T) {
	div := func(source, doc, amount string) TradeEvent {
		return TradeEvent{Date: nov3, Source: source, Line: 1, Account: "a1", ISIN: "US0378331005", Side: SideDividend,
			Amount: amount, TaxWithheld: "1.5", Currency: "USD", Doc: doc}
	}
	got := one(t, Reconcile([]TradeEvent{div("mail:me@x/m1", DocConfirmation, "10.00"), div("upload:st", DocStatement, "10")}))
	if got.Amount != "10" || got.TaxWithheld != "1.5" || got.Quantity != "" || got.Record != "upload:st" {
		t.Errorf("dividend = %+v", got)
	}
}

func TestReconcileOrderIndependent(t *testing.T) {
	events := []TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "50", "1512.40", "ord-7"),
		ev("mail:me@x/m2", 1, DocContractNote, "50", "1512.35", "ord-7"),
		ev("upload:tb", 1, DocTradebook, "50", "1512.35", ""),
		ev("upload:tb", 2, DocTradebook, "10", "1490", ""),
		ev("chat:t1", 1, DocChat, "60", "1500", ""),
	}
	events[3].Side = SideSell
	want := Reconcile(events)
	for range 20 {
		shuffled := slices.Clone(events)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		if got := Reconcile(shuffled); !reflect.DeepEqual(got, want) {
			t.Fatalf("order changed the result:\n%+v\nwant\n%+v", got, want)
		}
	}
}
