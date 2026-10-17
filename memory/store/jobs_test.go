package store

import (
	"context"
	"reflect"
	"testing"
)

func jobsUsers(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.UpsertWebUser(context.Background(), User{ID: id, Email: id + "@x"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchedules(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	if _, ok, err := s.ScheduleOf(ctx, "a"); ok || err != nil {
		t.Fatalf("unscheduled: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SaveSchedule(ctx, "a", 600); !ok || err != nil {
		t.Fatalf("a: ok=%v err=%v", ok, err)
	}
	// The minute is a's, and a keeps theirs.
	if ok, err := s.SaveSchedule(ctx, "b", 600); ok || err != nil {
		t.Fatalf("b taken: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SaveSchedule(ctx, "a", 700); ok || err != nil {
		t.Fatalf("a again: ok=%v err=%v", ok, err)
	}
	if ok, err := s.SaveSchedule(ctx, "b", 720); !ok || err != nil {
		t.Fatalf("b: ok=%v err=%v", ok, err)
	}
	if ms, _ := s.ScheduleMinutes(ctx); !reflect.DeepEqual(ms, []int{600, 720}) {
		t.Fatalf("minutes = %v", ms)
	}
	if m, ok, _ := s.ScheduleOf(ctx, "b"); !ok || m != 720 {
		t.Fatalf("b = %d %v", m, ok)
	}

	due := func(minute int, date string) []string {
		t.Helper()
		ids, err := s.DueSchedules(ctx, minute, date)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if ids := due(599, "2026-10-17"); len(ids) != 0 {
		t.Fatalf("early: %v", ids)
	}
	if ids := due(650, "2026-10-17"); !reflect.DeepEqual(ids, []string{"a"}) {
		t.Fatalf("10:50: %v", ids)
	}
	if err := s.MarkRan(ctx, "a", "2026-10-17"); err != nil {
		t.Fatal(err)
	}
	// Once a day: a is done today, b is due later, and both run tomorrow.
	if ids := due(800, "2026-10-17"); !reflect.DeepEqual(ids, []string{"b"}) {
		t.Fatalf("13:20: %v", ids)
	}
	if ids := due(800, "2026-10-18"); !reflect.DeepEqual(ids, []string{"a", "b"}) {
		t.Fatalf("tomorrow: %v", ids)
	}
}

func TestJobPostings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	p := JobPosting{UserID: "a", URL: "https://jobs.lever.co/acme/1?utm_source=x", Title: "Engineer", Company: "Acme", Source: "lever"}
	got, ok, err := s.PickPosting(ctx, p)
	if err != nil || !ok || got.URLHash != URLHash("https://jobs.lever.co/acme/1") || got.Status != PostingPicked {
		t.Fatalf("pick = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := s.PickPosting(ctx, p); ok {
		t.Fatal("picked twice")
	}
	// Per user: b may get the same posting.
	p.UserID = "b"
	if _, ok, _ := s.PickPosting(ctx, p); !ok {
		t.Fatal("b's pick refused")
	}

	unseen, err := s.UnseenPostings(ctx, "a", []string{"https://jobs.lever.co/acme/1", "https://jobs.lever.co/acme/2"})
	if err != nil || !reflect.DeepEqual(unseen, []string{"https://jobs.lever.co/acme/2"}) {
		t.Fatalf("unseen = %v %v", unseen, err)
	}

	open := func(user string) []string {
		t.Helper()
		ps, err := s.OpenPostings(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		var hs []string
		for _, p := range ps {
			hs = append(hs, p.Status)
		}
		return hs
	}
	if err := s.MarkPublished(ctx, "a", got.URLHash); err != nil {
		t.Fatal(err)
	}
	if st := open("a"); !reflect.DeepEqual(st, []string{PostingPublished}) {
		t.Fatalf("open = %v", st)
	}
	if err := s.ClosePostings(ctx, "a", []string{got.URLHash}); err != nil {
		t.Fatal(err)
	}
	if st := open("a"); len(st) != 0 {
		t.Fatalf("open after close = %v", st)
	}
	// Closing is final, and failed postings are not open.
	if err := s.MarkPublished(ctx, "a", got.URLHash); err != nil {
		t.Fatal(err)
	}
	if pp, _ := s.JobPostingOf(ctx, "a", got.URLHash); pp == nil || pp.Status != PostingClosed {
		t.Fatalf("closed posting = %+v", pp)
	}
	if err := s.MarkFailed(ctx, "b", got.URLHash); err != nil {
		t.Fatal(err)
	}
	if st := open("b"); len(st) != 0 {
		t.Fatalf("b open = %v", st)
	}
	// A posting judged not a fit is seen, and not open.
	passed := JobPosting{UserID: "a", URL: "https://jobs.lever.co/acme/3", Title: "Clerk", Source: "lever"}
	if err := s.PassPosting(ctx, passed); err != nil {
		t.Fatal(err)
	}
	if unseen, _ := s.UnseenPostings(ctx, "a", []string{passed.URL}); len(unseen) != 0 {
		t.Fatalf("passed unseen = %v", unseen)
	}
	if st := open("a"); len(st) != 0 {
		t.Fatalf("passed open = %v", st)
	}
	if pp, _ := s.JobPostingOf(ctx, "a", "nope"); pp != nil {
		t.Fatalf("missing = %+v", pp)
	}

	// A user asking for a passed or failed posting gets it picked again;
	// open and closed ones stay as they are.
	if rp, ok, err := s.RepickPosting(ctx, passed); err != nil || !ok || rp.Status != PostingPicked {
		t.Fatalf("repick passed = %+v %v %v", rp, ok, err)
	}
	if st := open("a"); !reflect.DeepEqual(st, []string{PostingPicked}) {
		t.Fatalf("repicked open = %v", st)
	}
	if _, ok, _ := s.RepickPosting(ctx, passed); ok {
		t.Fatal("repicked a picked posting")
	}
	p.UserID = "b"
	if _, ok, _ := s.RepickPosting(ctx, p); !ok {
		t.Fatal("b's failed posting not repicked")
	}
	p.UserID = "a"
	if _, ok, _ := s.RepickPosting(ctx, p); ok {
		t.Fatal("repicked a closed posting")
	}
}

func TestWebProfilesAndRequests(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	jobsUsers(t, s, "a", "b")
	if uid, err := s.WebProfileOf(ctx, "a"); uid != "" || err != nil {
		t.Fatalf("no profile = %q %v", uid, err)
	}
	if err := s.SaveWebProfile(ctx, "a", "uid-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWebProfile(ctx, "a", "uid-a2"); err != nil {
		t.Fatal(err)
	}
	if uid, _ := s.WebProfileOf(ctx, "a"); uid != "uid-a2" {
		t.Fatalf("profile = %q", uid)
	}

	if err := s.AddLinkRequests(ctx, "a", []string{"https://x/1", "https://x/2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLinkRequests(ctx, "a", []string{"https://x/1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.TakeLinkRequests(ctx, "a"); err != nil || len(got) != 2 {
		t.Fatalf("take = %v %v", got, err)
	}
	if got, _ := s.TakeLinkRequests(ctx, "a"); len(got) != 0 {
		t.Fatalf("taken twice = %v", got)
	}
	if got, _ := s.TakeLinkRequests(ctx, "b"); len(got) != 0 {
		t.Fatalf("b's = %v", got)
	}

}
