package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestInterests(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, u := range []User{{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"}, {ID: "h2", Email: "b@x.com", SlackTeamID: "T1", SlackUserID: "U2"}} {
		if err := s.UpsertSlackUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	rubric := "1. positivity 0-10\n2. credibility 0-10"
	catalog := []Interest{
		{Name: "Stoic principles", Sources: []string{"https://a.org/feed"}, Rubric: rubric, Default: true},
		{Name: "Healthy eating", Sources: []string{"https://b.org/feed"}, Rubric: rubric, Default: true},
		{Name: "Statistics", Sources: []string{"https://s.org/feed"}, Rubric: rubric},
	}
	for _, in := range catalog {
		sources, _ := json.Marshal(in.Sources)
		if _, err := s.db.Exec(`INSERT INTO interests (interest, sources, rubric, is_default, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, 0)`, in.Name, string(sources), in.Rubric, in.Default); err != nil {
			t.Fatal(err)
		}
	}

	// Defaults, once.
	if added, err := s.FollowDefaults(ctx, "h1"); err != nil || len(added) != 2 {
		t.Fatalf("defaults: %v %v", added, err)
	}
	if added, _ := s.FollowDefaults(ctx, "h1"); len(added) != 0 {
		t.Fatalf("defaults twice: %v", added)
	}

	// h1 adds an interest of their own: h2 doesn't see it, and can add one
	// of the same name; a duplicate in h1's catalog fails.
	rnn := Interest{Name: "Recurrent neural networks", Sources: []string{"https://c.org/rss"}, Rubric: "1. novelty 0-10"}
	if err := s.AddInterest(ctx, "h1", rnn); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInterest(ctx, "h1", Interest{Name: "recurrent NEURAL networks", Sources: []string{"x"}}); err == nil {
		t.Fatal("added a duplicate interest")
	}
	if err := s.AddInterest(ctx, "h1", Interest{Name: "healthy EATING", Sources: []string{"x"}}); err == nil {
		t.Fatal("added a curated interest's name")
	}
	if cat, _ := s.Catalog(ctx, "h2"); len(cat) != 3 {
		t.Fatalf("h2 sees %+v", cat)
	}
	if err := s.FollowInterest(ctx, "h2", "Recurrent neural networks"); !errors.Is(err, ErrNoInterest) {
		t.Fatalf("h2 followed h1's interest: %v", err)
	}
	if err := s.AddInterest(ctx, "h2", Interest{Name: "Recurrent neural networks", Sources: []string{"https://d.org/rss"}, Rubric: "x"}); err != nil {
		t.Fatalf("h2's own: %v", err)
	}
	cat, err := s.Catalog(ctx, "h1")
	if err != nil || len(cat) != 4 || cat[1].Name != "Recurrent neural networks" || cat[1].Curated() || cat[1].Sources[0] != "https://c.org/rss" {
		t.Fatalf("h1 catalog = %+v %v", cat, err)
	}
	if in, err := s.CatalogInterest(ctx, "h1", "stoic PRINCIPLES"); err != nil || in == nil || in.Rubric != rubric || !in.Default || !in.Curated() {
		t.Fatalf("lookup = %+v %v", in, err)
	}

	// Following, muting and resuming.
	if err := s.FollowInterest(ctx, "h1", "recurrent neural networks"); err != nil {
		t.Fatal(err)
	}
	if err := s.FollowInterest(ctx, "h1", "Knitting"); !errors.Is(err, ErrNoInterest) {
		t.Fatalf("follow missing: %v", err)
	}
	if ok, err := s.SetInterestStatus(ctx, "h1", "HEALTHY EATING", StatusMuted); err != nil || !ok {
		t.Fatalf("mute: %v %v", ok, err)
	}
	if ok, _ := s.SetInterestStatus(ctx, "h1", "Statistics", StatusMuted); ok {
		t.Fatal("muted an interest not followed")
	}
	uis, err := s.UserInterests(ctx, "h1")
	if err != nil || len(uis) != 3 || uis[1].Status != StatusMuted || uis[2].Interest != "Recurrent neural networks" {
		t.Fatalf("user interests = %+v %v", uis, err)
	}
	active, err := s.ActiveInterests(ctx, "h1")
	if err != nil || len(active) != 2 || active[1].Sources[0] != "https://c.org/rss" {
		t.Fatalf("active = %+v %v", active, err)
	}
	if err := s.FollowInterest(ctx, "h1", "Healthy eating"); err != nil {
		t.Fatal(err)
	}
	if active, _ := s.ActiveInterests(ctx, "h1"); len(active) != 3 {
		t.Fatalf("after resume: %+v", active)
	}
}

func TestSentURLs(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	if URLHash("HTTPS://Example.org/a/?utm_source=rss&id=2#top") != URLHash("https://example.org/a?id=2") {
		t.Fatal("variants of a link hash differently")
	}
	if URLHash("https://example.org/a?id=2") == URLHash("https://example.org/a?id=3") {
		t.Fatal("different links hash the same")
	}
	if err := markSent(ctx, s.db, "h1", []string{"https://example.org/a?utm_medium=x"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Unsent(ctx, "h1", []string{"https://example.org/a", "https://example.org/b"})
	if err != nil || len(got) != 1 || got[0] != "https://example.org/b" {
		t.Fatalf("unsent = %v %v", got, err)
	}

	start := time.Now().Add(-time.Second)
	if err := markStoriesSent(ctx, s.db, "h1", []SentStory{{"openai-gpt5-launch", "GPT-5 is here"}, {}, {"openai-gpt5-launch", "OpenAI launches GPT-5"}}); err != nil {
		t.Fatal(err)
	}
	if st, err := s.SentStoriesSince(ctx, "h1", start); err != nil || len(st) != 1 || st[0].Title != "OpenAI launches GPT-5" {
		t.Fatalf("stories = %v %v", st, err)
	}
	if st, _ := s.SentStoriesSince(ctx, "h1", time.Now().Add(time.Hour)); len(st) != 0 {
		t.Fatalf("stories later = %v", st)
	}
}
