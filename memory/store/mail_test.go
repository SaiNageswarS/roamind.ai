package store

import (
	"context"
	"testing"
	"time"
)

func TestMail(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	if a, err := s.MailAccountOf(ctx, "me@gmail.com"); err != nil || a != nil {
		t.Fatalf("unknown account = %+v %v", a, err)
	}
	for _, c := range []struct{ address, user string }{{"me@gmail.com", "b"}, {"me@gmail.com", "a"}, {"work@x.com", "a"}} {
		if err := s.ConnectMail(ctx, c.address, c.user); err != nil {
			t.Fatal(err)
		}
	}
	// Connecting again moves the account; addresses match in any case.
	a, err := s.MailAccountOf(ctx, " Me@Gmail.com")
	if err != nil || a == nil || a.UserID != "a" || a.Address != "me@gmail.com" || !a.SeenAt.IsZero() {
		t.Fatalf("account = %+v %v", a, err)
	}

	// Never heard from counts from connecting.
	later := time.Now().Add(time.Hour)
	if stale, err := s.StaleMailAccounts(ctx, "a", later); err != nil || len(stale) != 2 {
		t.Fatalf("stale = %+v %v", stale, err)
	}
	if err := s.SeenMail(ctx, "ME@gmail.com", later.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stale, err := s.StaleMailAccounts(ctx, "a", later)
	if err != nil || len(stale) != 1 || stale[0].Address != "work@x.com" {
		t.Fatalf("stale after a request = %+v %v", stale, err)
	}
	if stale, _ := s.StaleMailAccounts(ctx, "b", later); len(stale) != 0 {
		t.Fatalf("b's stale = %+v", stale)
	}

	m := MailMessage{Address: "me@gmail.com", MessageID: "18c1", UserID: "a", Category: "bill"}
	if ok, err := s.PickMessage(ctx, m); err != nil || !ok {
		t.Fatalf("pick = %v %v", ok, err)
	}
	if ok, _ := s.PickMessage(ctx, m); ok {
		t.Fatal("picked twice")
	}
	// A failed message is picked again, as when its run is replayed.
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailFailed); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.PickMessage(ctx, m); !ok {
		t.Fatal("failed message not picked again")
	}
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailDone); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.PickMessage(ctx, m); ok {
		t.Fatal("done message picked again")
	}
	// Only a picked message is finished.
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailFailed); err != nil {
		t.Fatal(err)
	}
	if st, err := s.MessageStatus(ctx, m.Address, m.MessageID); err != nil || st != MailDone {
		t.Fatalf("status = %q %v", st, err)
	}
	if st, _ := s.MessageStatus(ctx, m.Address, "other"); st != "" {
		t.Fatalf("unknown message status = %q", st)
	}
}
