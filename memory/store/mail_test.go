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
	secrets := map[string]bool{}
	for _, c := range []struct{ address, user string }{{"me@gmail.com", "b"}, {"me@gmail.com", "a"}, {"work@x.com", "a"}} {
		secret, err := s.ConnectMail(ctx, c.address, c.user)
		if err != nil || len(secret) != 64 || secrets[secret] {
			t.Fatalf("connect %s = %q %v", c.address, secret, err)
		}
		secrets[secret] = true
	}
	// Connecting again moves the account, with a new key; addresses match
	// in any case.
	a, err := s.MailAccountOf(ctx, " Me@Gmail.com")
	if err != nil || a == nil || a.UserID != "a" || a.Address != "me@gmail.com" || !secrets[a.Secret] || !a.SeenAt.IsZero() {
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
	if err != nil || len(stale) != 1 || stale[0].Address != "work@x.com" || stale[0].Secret == "" {
		t.Fatalf("stale after a request = %+v %v", stale, err)
	}
	if stale, _ := s.StaleMailAccounts(ctx, "b", later); len(stale) != 0 {
		t.Fatalf("b's stale = %+v", stale)
	}

	now := time.Now()
	past := now.Add(-time.Hour)
	m := MailMessage{Address: "me@gmail.com", MessageID: "18c1", UserID: "a", Category: "bill", Subject: "Your bill", Link: "https://mail/18c1"}
	if ok, err := s.PickMessage(ctx, m, past); err != nil || !ok {
		t.Fatalf("pick = %v %v", ok, err)
	}
	if ok, _ := s.PickMessage(ctx, m, past); ok {
		t.Fatal("picked twice")
	}
	// A run picked before stuckBefore that never finished is picked
	// again, as when it was cancelled.
	if ok, _ := s.PickMessage(ctx, m, now.Add(time.Minute)); !ok {
		t.Fatal("stuck message not picked again")
	}
	// A failed message is picked again, as when its mail is sent again.
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailFailed); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.PickMessage(ctx, m, past); !ok {
		t.Fatal("failed message not picked again")
	}
	// A failed run replayed from relate finishes without picking again.
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailFailed); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailDone); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.PickMessage(ctx, m, now.Add(time.Minute)); ok {
		t.Fatal("done message picked again")
	}
	// A done message is not finished again.
	if err := s.FinishMessage(ctx, m.Address, m.MessageID, MailFailed); err != nil {
		t.Fatal(err)
	}
	if st, err := s.MessageStatus(ctx, m.Address, m.MessageID); err != nil || st != MailDone {
		t.Fatalf("status = %q %v", st, err)
	}
	if st, _ := s.MessageStatus(ctx, m.Address, "other"); st != "" {
		t.Fatalf("unknown message status = %q", st)
	}

	// Unsaved mail: failed, or picked and stuck; not done, not running.
	for _, id := range []string{"f1", "p1", "r1"} {
		if ok, err := s.PickMessage(ctx, MailMessage{Address: "me@gmail.com", MessageID: id, UserID: "a", Subject: "S " + id}, past); err != nil || !ok {
			t.Fatalf("pick %s = %v %v", id, ok, err)
		}
	}
	if err := s.FinishMessage(ctx, "me@gmail.com", "f1", MailFailed); err != nil {
		t.Fatal(err)
	}
	unsaved, err := s.UnsavedMail(ctx, "a", now.Add(-24*time.Hour), now.Add(time.Minute))
	if err != nil || len(unsaved) != 3 {
		t.Fatalf("unsaved = %+v %v", unsaved, err)
	}
	if unsaved, _ := s.UnsavedMail(ctx, "a", now.Add(-24*time.Hour), past); len(unsaved) != 1 || unsaved[0].MessageID != "f1" ||
		unsaved[0].Subject != "S f1" || unsaved[0].Status != MailFailed {
		t.Fatalf("unsaved, none stuck = %+v", unsaved)
	}
	if unsaved, _ := s.UnsavedMail(ctx, "a", now.Add(time.Minute), now.Add(time.Minute)); len(unsaved) != 0 {
		t.Fatalf("unsaved since now = %+v", unsaved)
	}
	if unsaved, _ := s.UnsavedMail(ctx, "b", now.Add(-24*time.Hour), now.Add(time.Minute)); len(unsaved) != 0 {
		t.Fatalf("b's unsaved = %+v", unsaved)
	}
}
