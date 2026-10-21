package store

import (
	"context"
	"testing"
	"time"
)

func TestNews(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, u := range []User{
		{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"},
		{ID: "h2", Email: "b@x.com", SlackTeamID: "T1", SlackUserID: "U2"},
	} {
		if err := s.UpsertSlackUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if us, err := s.Users(ctx); err != nil || len(us) != 2 {
		t.Fatalf("users = %v %v", us, err)
	}

	// Digests: a slot is reserved once, then completed with its items, links
	// and stories; a released reservation frees the slot.
	now := time.Now()
	slot := "2026-10-13 08"
	if id, ok, err := s.ReserveDigest(ctx, "h1", "G1", slot); err != nil || !ok {
		t.Fatalf("reserve: %v %v", ok, err)
	} else if err := s.ReleaseDigest(ctx, id); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.ReserveDigest(ctx, "h1", "G1", slot)
	if err != nil || !ok {
		t.Fatalf("reserve again: %v %v", ok, err)
	}
	if _, ok, err := s.ReserveDigest(ctx, "h1", "G1", slot); err != nil || ok {
		t.Fatalf("a slot was reserved twice: %v %v", ok, err)
	}
	if ok, err := s.DigestPosted(ctx, "h1", slot); err != nil || !ok {
		t.Fatalf("a reserved slot is not taken: %v %v", ok, err)
	}
	// Another reservation in the same channel, while this one has no ts.
	if _, ok, err := s.ReserveDigest(ctx, "h1", "G1", "2026-10-13 12"); err != nil || !ok {
		t.Fatalf("reserve the next slot: %v %v", ok, err)
	}
	items := []DigestItem{
		{Interest: "Stoic principles", Title: "Seneca on time", URL: "https://a.org/1", Story: "seneca-on-time"},
		{Interest: "Neuroscience for better living", Title: "Sleep and memory", URL: "https://b.org/2"},
	}
	d := Digest{ID: id, UserID: "h1", TS: "1.0", Text: "the digest", PostedAt: now}
	if err := s.CompleteDigest(ctx, d, items); err != nil {
		t.Fatal(err)
	}
	if unsent, _ := s.Unsent(ctx, "h1", []string{"https://a.org/1", "https://b.org/2", "https://c.org/3"}); len(unsent) != 1 {
		t.Fatalf("links not marked sent: %v", unsent)
	}
	if st, _ := s.SentStoriesSince(ctx, "h1", now.Add(-time.Minute)); len(st) != 1 || st[0] != (SentStory{"seneca-on-time", "Seneca on time"}) {
		t.Fatalf("stories = %v", st)
	}
	if ok, _ := s.DigestPosted(ctx, "h1", "2026-10-13 16"); ok {
		t.Fatal("a later slot is taken")
	}
	got, err := s.DigestByTS(ctx, "G1", "1.0")
	if err != nil || got == nil || got.ID != id || got.Text != "the digest" {
		t.Fatalf("digest = %+v %v", got, err)
	}
	// Signals: reactions and replies count for every item's interest.
	for _, sg := range []Signal{
		{UserID: "h1", DigestID: id, Kind: "reaction", Value: 1},
		{UserID: "h1", DigestID: id, Kind: "reaction", Value: -1},
		{UserID: "h1", DigestID: id, Kind: "reply", Value: 1},
	} {
		if err := s.SaveSignal(ctx, sg); err != nil {
			t.Fatal(err)
		}
	}
	sigs, err := s.InterestSignals(ctx, "h1", now.Add(-time.Hour))
	if err != nil || len(sigs) != 2 {
		t.Fatalf("signals = %+v %v", sigs, err)
	}
	for i, want := range []string{"Stoic principles", "Neuroscience for better living"} {
		if s := sigs[i]; s.Interest != want || s.Shown != 1 || s.Liked != 2 || s.Disliked != 1 || len(s.LikedTitles) != 1 {
			t.Errorf("signal %d = %+v", i, s)
		}
	}
	if recent, err := s.RecentDigestItems(ctx, "h1", now.Add(-time.Hour)); err != nil || len(recent) != 2 || recent[0].Title != "Seneca on time" {
		t.Fatalf("recent = %+v %v", recent, err)
	}
}
