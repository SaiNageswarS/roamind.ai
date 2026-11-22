package store

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// The seed is loaded as `task init` loads it, from the repo's memory folder.
func TestSeedInterests(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seed := func() {
		t.Helper()
		if err := s.SeedInterests(ctx, "../"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	// A curated interest dropped from the file, followed by h1, and h1's own.
	if _, err := s.db.Exec(`INSERT INTO interests (interest, sources, rubric, created_on, updated_on) VALUES ('Knitting', '[]', 'x', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.FollowInterest(ctx, "h1", "Knitting"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddInterest(ctx, "h1", Interest{Name: "Chess", Sources: []string{"https://c.org/rss"}, Rubric: "x"}); err != nil {
		t.Fatal(err)
	}

	seed()
	first, err := s.Catalog(ctx, "h1")
	if err != nil || len(first) < 2 {
		t.Fatalf("catalog = %+v %v", first, err)
	}
	seed() // safe to repeat
	cat, err := s.Catalog(ctx, "h1")
	if err != nil || len(cat) != len(first) {
		t.Fatalf("catalog after a second seed = %d interests, %v; want %d", len(cat), err, len(first))
	}
	defaults := 0
	for _, in := range cat {
		if !in.Curated() {
			continue
		}
		if in.Default {
			defaults++
		}
		if len(in.Sources) == 0 || !strings.Contains(in.Rubric, "0-10") || !strings.Contains(in.Rubric, "include (true/false)") {
			t.Errorf("%s: %d sources, rubric %q", in.Name, len(in.Sources), in.Rubric)
		}
		for _, src := range in.Sources {
			if u, err := url.Parse(src); err != nil || u.Scheme != "https" {
				t.Errorf("%s: source %q", in.Name, src)
			}
		}
	}
	if defaults == 0 {
		t.Error("no default interests")
	}
	if uis, _ := s.UserInterests(ctx, "h1"); len(uis) != 0 {
		t.Fatalf("follows of a dropped interest remain: %+v", uis)
	}
}
