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

func october() Coverage {
	return Coverage{"a1": {{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}}}
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
	got := one(t, must(Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "50", "1512.40", "ord-7"),
		ev("mail:me@x/m2", 1, DocContractNote, "50", "1512.35", "ord-7"),
	}, nil, nil)))
	if got.State != Provisional || got.Quantity != "50" || got.AvgPrice != "1512.35" || got.Record != "mail:me@x/m2" {
		t.Errorf("trade = %+v; want the contract note's 50 @ 1512.35, provisional", got)
	}
}

func TestReconcileTwoOrdersOfOneDay(t *testing.T) {
	got := one(t, must(Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocConfirmation, "10", "1500", "ord-1"),
		ev("mail:me@x/m2", 1, DocConfirmation, "10", "1500", "ord-2"),
		// A report's two rows are two executions, even when alike.
		ev("mail:me@x/m3", 1, DocContractNote, "10", "1500", ""),
		ev("mail:me@x/m3", 2, DocContractNote, "10", "1500", ""),
	}, nil, nil)))
	if got.Quantity != "20" {
		t.Errorf("quantity = %s, want 20", got.Quantity)
	}
}

func TestReconcileReportWins(t *testing.T) {
	trades, notices := Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocContractNote, "40", "1512.35", ""),
		ev("upload:tb", 3, DocTradebook, "50", "1512.35", ""),
		// A statement for the same month repeats the tradebook's row.
		ev("upload:st", 9, DocStatement, "50", "1512.30", ""),
	}, october(), nil)
	got := one(t, trades)
	if got.State != Confirmed || got.Quantity != "50" || got.Record != "upload:tb" {
		t.Errorf("trade = %+v; want the tradebook's 50, confirmed", got)
	}
	if len(notices) != 1 || notices[0].Kind != NoticeCorrected {
		t.Fatalf("notices = %+v", notices)
	}
	if want := "mail said 40 @ 1512.35 INR; the report says 50 @ 1512.35 INR"; notices[0].Detail != want {
		t.Errorf("detail = %q, want %q", notices[0].Detail, want)
	}
	// Mail that agrees within fees and rounding is no news.
	_, notices = Reconcile([]TradeEvent{
		ev("mail:me@x/m1", 1, DocContractNote, "50", "1513.20", ""),
		ev("upload:tb", 3, DocTradebook, "50", "1512.35", ""),
	}, october(), nil)
	if len(notices) != 0 {
		t.Errorf("notices for agreeing mail: %+v", notices)
	}
}

func TestReconcileDropsMailACoveringReportLacks(t *testing.T) {
	trades, notices := Reconcile([]TradeEvent{ev("mail:me@x/m1", 1, DocConfirmation, "5", "1500", "")}, october(), nil)
	if got := one(t, trades); got.State != Void {
		t.Errorf("trade = %+v, want void", got)
	}
	if len(notices) != 1 || notices[0].Kind != NoticeDropped {
		t.Errorf("notices = %+v", notices)
	}
}

func TestReconcileProvisionalUntilAReport(t *testing.T) {
	mail := ev("mail:me@x/m1", 1, DocContractNote, "50", "1512.35", "")
	before := one(t, must(Reconcile([]TradeEvent{mail}, nil, nil)))
	if before.State != Provisional {
		t.Fatalf("before = %+v", before)
	}
	after := one(t, must(Reconcile([]TradeEvent{mail, ev("upload:tb", 1, DocTradebook, "50", "1512.35", "")}, october(), []Trade{before})))
	if after.State != Confirmed || after.Record != "upload:tb" {
		t.Errorf("after = %+v", after)
	}
}

func TestReconcileVoidsWhatsGone(t *testing.T) {
	gone := Trade{Date: nov3, Account: "a1", ISIN: "US0378331005", Side: SideSell, Quantity: "1", State: Provisional}
	trades, _ := Reconcile([]TradeEvent{ev("upload:tb", 1, DocTradebook, "1", "10", "")}, nil, []Trade{gone})
	states := map[string]string{}
	for _, tr := range trades {
		states[tr.ISIN] = tr.State
	}
	if states["US0378331005"] != Void || states["INE009A01021"] != Confirmed {
		t.Errorf("states = %v", states)
	}
}

func TestReconcileDividends(t *testing.T) {
	div := func(source, doc, amount string) TradeEvent {
		return TradeEvent{Date: nov3, Source: source, Line: 1, Account: "a1", ISIN: "US0378331005", Side: SideDividend,
			Amount: amount, TaxWithheld: "1.5", Currency: "USD", Doc: doc}
	}
	got := one(t, must(Reconcile([]TradeEvent{div("mail:me@x/m1", DocConfirmation, "10.00"), div("upload:st", DocStatement, "10")}, nil, nil)))
	if got.Amount != "10" || got.TaxWithheld != "1.5" || got.Quantity != "" || got.State != Confirmed {
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
	want, wantNotices := Reconcile(events, october(), nil)
	for range 20 {
		shuffled := slices.Clone(events)
		rand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got, notices := Reconcile(shuffled, october(), nil)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(notices, wantNotices) {
			t.Fatalf("order changed the result:\n%+v\n%+v\nwant\n%+v\n%+v", got, notices, want, wantNotices)
		}
	}
}

func must(trades []Trade, _ []Notice) []Trade { return trades }
