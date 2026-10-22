package store

import (
	"context"
	"testing"
)

func TestInboxDigests(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	if posted, err := s.InboxPosted(ctx, "a", "2026-10-23"); posted || err != nil {
		t.Fatalf("posted = %v %v", posted, err)
	}
	id, ok, err := s.ReserveInbox(ctx, "a", "C1", "2026-10-23")
	if !ok || err != nil {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	if _, ok, _ := s.ReserveInbox(ctx, "a", "C1", "2026-10-23"); ok {
		t.Fatal("reserved twice")
	}
	if _, ok, _ := s.ReserveInbox(ctx, "b", "C2", "2026-10-23"); !ok {
		t.Fatal("b's day refused")
	}
	if posted, _ := s.InboxPosted(ctx, "a", "2026-10-23"); !posted {
		t.Fatal("a reserved day is not taken")
	}

	// A day whose post failed is given back; a posted one isn't.
	if err := s.ReleaseInbox(ctx, id); err != nil {
		t.Fatal(err)
	}
	if posted, _ := s.InboxPosted(ctx, "a", "2026-10-23"); posted {
		t.Fatal("released day still taken")
	}
	id, _, _ = s.ReserveInbox(ctx, "a", "C1", "2026-10-23")
	if err := s.CompleteInbox(ctx, id, "1.0", "digest"); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseInbox(ctx, id); err != nil {
		t.Fatal(err)
	}
	if posted, _ := s.InboxPosted(ctx, "a", "2026-10-23"); !posted {
		t.Fatal("a posted day was released")
	}
}
