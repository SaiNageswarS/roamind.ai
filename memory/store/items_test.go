package store

import (
	"context"
	"testing"
	"time"
)

func TestInboxLeads(t *testing.T) {
	for _, c := range []struct {
		kind, eventKind, date, remindOn, end string
		from, until                          string
	}{
		{ItemTask, "", "2026-11-10", "", "", "2026-11-09", ""},
		{ItemTask, "", "2026-11-10", "2026-11-07", "", "2026-11-07", ""},
		{ItemTask, "", "", "", "", "", ""},
		{ItemEvent, DeliveryKind, "2026-11-10", "", "", "2026-11-10", "2026-11-10"},
		{ItemEvent, "travel", "2026-11-10T06:40", "", "", "2026-11-09", "2026-11-10"},
		{ItemEvent, "stay", "2026-11-10", "", "2026-11-14T11:00", "2026-11-09", "2026-11-14"},
		{ItemEvent, "ticket", "2026-12-01", "2026-11-20", "", "2026-11-20", "2026-12-01"},
		{ItemEvent, "appointment", "", "2026-11-20", "", "2026-11-20", ""},
		{ItemEvent, "milestone", "", "", "", "", ""},
	} {
		from, until, err := InboxLeads(c.kind, c.eventKind, c.date, c.remindOn, c.end)
		if err != nil || from != c.from || until != c.until {
			t.Errorf("InboxLeads(%q, %q, %q, %q, %q) = %q, %q, %v; want %q, %q",
				c.kind, c.eventKind, c.date, c.remindOn, c.end, from, until, err, c.from, c.until)
		}
	}
	if _, _, err := InboxLeads(ItemEvent, "", "2027-06", "", ""); err == nil {
		t.Error("a month was taken for a date")
	}
}

func TestItems(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	put := func(it Item) {
		t.Helper()
		if it.UserID == "" {
			it.UserID = "a"
		}
		if it.Source == "" {
			it.Source = SourceChat
		}
		var err error
		if it.ShowFrom, it.Until, err = InboxLeads(it.Kind, it.EventKind, it.Date, "", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.PutItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(items []Item, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var ks string
		for _, it := range items {
			ks += it.Key + ";"
		}
		return ks
	}
	day := func(s string) time.Time {
		d, _ := ParseItemDate(s)
		return d
	}

	put(Item{Key: "Pay Acme INV-7", Kind: ItemTask, Note: "tasks/pay-acme-inv-7", Title: "Pay Acme INV-7", Date: "2026-11-10"})
	put(Item{Key: "Call the bank", Kind: ItemTask, Note: "tasks/call-the-bank", Title: "Call the bank"})
	put(Item{Key: "Write tests", Kind: ItemTask, Note: "tasks/roamind/write-tests", Title: "Write tests", Date: "2026-11-12"})
	put(Item{Key: "112-3456", Kind: ItemEvent, EventKind: DeliveryKind, Note: "events/2026/headphones", Title: "Headphones arrive", Date: "2026-11-10"})
	put(Item{Key: "XK7Q2P", Kind: ItemEvent, EventKind: "travel", Note: "events/2026/flight-to-lisbon", Title: "Flight to Lisbon", Date: "2026-11-11T06:40"})
	// Basic Memory starts permalinks with the project.
	project, err := s.AssignMemoryProject(ctx, "a", "bm-u-a")
	if err != nil {
		t.Fatal(err)
	}
	put(Item{Key: "Book flights", Kind: ItemTask, Note: project + "/tasks/roamind/book-flights", Title: "Book flights"})
	put(Item{UserID: "b", Key: "B's task", Kind: ItemTask, Note: "tasks/b", Title: "B's task", Date: "2026-11-10"})

	if got := keys(s.DueItems(ctx, "a", day("2026-11-08"))); got != "" {
		t.Errorf("due on the 8th = %s", got)
	}
	if got := keys(s.DueItems(ctx, "a", day("2026-11-10"))); got != "112-3456;Pay Acme INV-7;XK7Q2P;" {
		t.Errorf("due on the 10th = %s", got)
	}
	// The delivery expired, the flight is today, the bill is overdue.
	if got := keys(s.DueItems(ctx, "a", day("2026-11-11"))); got != "Pay Acme INV-7;XK7Q2P;Write tests;" {
		t.Errorf("due on the 11th = %s", got)
	}

	// Closing, by key in any case.
	if ok, err := s.CloseItem(ctx, "a", "pay acme inv-7", ItemDone); !ok || err != nil {
		t.Fatalf("close = %v %v", ok, err)
	}
	if ok, _ := s.CloseItem(ctx, "a", "B's task", ItemDone); ok {
		t.Fatal("closed another user's item")
	}
	if got := keys(s.DueItems(ctx, "a", day("2026-11-11"))); got != "XK7Q2P;Write tests;" {
		t.Errorf("due after paying = %s", got)
	}
	paid, _ := s.Items(ctx, "a", ItemFilter{Key: "Pay Acme INV-7"})
	if len(paid) != 1 || paid[0].State != ItemDone || paid[0].ClosedAt.IsZero() {
		t.Fatalf("paid = %+v", paid)
	}

	// Rescheduling keeps the state; a cancellation from mail closes it.
	put(Item{Key: "Pay Acme INV-7", Kind: ItemTask, Note: "tasks/pay-acme-inv-7", Title: "Pay Acme INV-7", Date: "2026-11-20"})
	if got, _ := s.Items(ctx, "a", ItemFilter{Key: "Pay Acme INV-7"}); got[0].State != ItemDone || got[0].Date != "2026-11-20" || !got[0].ClosedAt.Equal(paid[0].ClosedAt) {
		t.Fatalf("rescheduled = %+v", got[0])
	}
	put(Item{Key: "XK7Q2P", Kind: ItemEvent, EventKind: "travel", Note: "events/2026/flight-to-lisbon", Title: "Flight to Lisbon", Date: "2026-11-11T06:40", State: ItemCancelled})
	if got := keys(s.DueItems(ctx, "a", day("2026-11-11"))); got != "Write tests;" {
		t.Errorf("due after the cancellation = %s", got)
	}
	// Reopening.
	if ok, _ := s.CloseItem(ctx, "a", "XK7Q2P", ItemOpen); !ok {
		t.Fatal("not reopened")
	}
	if got, _ := s.Items(ctx, "a", ItemFilter{Key: "XK7Q2P"}); got[0].State != ItemOpen || !got[0].ClosedAt.IsZero() {
		t.Fatalf("reopened = %+v", got[0])
	}

	for _, c := range []struct {
		f    ItemFilter
		want string
	}{
		{ItemFilter{}, "112-3456;XK7Q2P;Write tests;Book flights;Call the bank;"},
		{ItemFilter{State: ItemDone}, "Pay Acme INV-7;"},
		{ItemFilter{From: "2026-11-11", To: "2026-11-12"}, "XK7Q2P;Write tests;"},
		{ItemFilter{Folder: "tasks/roamind/"}, "Write tests;Book flights;"},
		{ItemFilter{Folder: project + "/tasks/roamind/"}, "Book flights;"},
		{ItemFilter{Folder: "tasks/"}, "Write tests;Book flights;Call the bank;"},
		{ItemFilter{Folder: "roamind/"}, ""},
		{ItemFilter{Key: "nothing"}, ""},
	} {
		if got := keys(s.Items(ctx, "a", c.f)); got != c.want {
			t.Errorf("Items(%+v) = %s; want %s", c.f, got, c.want)
		}
	}
}
