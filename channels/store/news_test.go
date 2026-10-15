package store

import (
	"context"
	"errors"
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

	// News channels: one per user, routed by its owner only.
	if ch, err := s.NewsChannelOf(ctx, "h1"); err != nil || ch != "" {
		t.Fatalf("before: %q %v", ch, err)
	}
	if err := s.SaveNewsChannel(ctx, "h1", "G1"); err != nil {
		t.Fatal(err)
	}
	if ch, _ := s.NewsChannelOf(ctx, "h1"); ch != "G1" {
		t.Fatalf("channel = %q", ch)
	}
	for slackUser, want := range map[string]bool{"U1": true, "U2": false} {
		if got, err := s.IsNewsChannelOf(ctx, "G1", slackUser); err != nil || got != want {
			t.Errorf("%s: %v %v", slackUser, got, err)
		}
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
	if err := s.SyncCatalog(ctx, catalog); err != nil {
		t.Fatal(err)
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

	// Re-syncing updates curated interests, deletes those gone from the
	// catalog with their follows, and leaves users' own alone.
	catalog[0].Sources = append(catalog[0].Sources, "https://a.org/more")
	if err := s.SyncCatalog(ctx, catalog[:1]); err != nil {
		t.Fatal(err)
	}
	if cat, _ := s.Catalog(ctx, "h1"); len(cat) != 2 || cat[1].Name != "Stoic principles" || len(cat[1].Sources) != 2 {
		t.Fatalf("after re-sync: %+v", cat)
	}
	if uis, _ := s.UserInterests(ctx, "h1"); len(uis) != 2 || uis[0].Interest != "Stoic principles" {
		t.Fatalf("follows after re-sync: %+v", uis)
	}
	if defs, _ := s.DefaultInterests(ctx); len(defs) != 1 {
		t.Fatalf("defaults after re-sync: %+v", defs)
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
	if err := s.MarkSent(ctx, "h1", []string{"https://example.org/a?utm_medium=x"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Unsent(ctx, "h1", []string{"https://example.org/a", "https://example.org/b"})
	if err != nil || len(got) != 1 || got[0] != "https://example.org/b" {
		t.Fatalf("unsent = %v %v", got, err)
	}

	start := time.Now().Add(-time.Second)
	if err := s.MarkStoriesSent(ctx, "h1", []SentStory{{"openai-gpt5-launch", "GPT-5 is here"}, {}, {"openai-gpt5-launch", "OpenAI launches GPT-5"}}); err != nil {
		t.Fatal(err)
	}
	if st, err := s.SentStoriesSince(ctx, "h1", start); err != nil || len(st) != 1 || st[0].Title != "OpenAI launches GPT-5" {
		t.Fatalf("stories = %v %v", st, err)
	}
	if st, _ := s.SentStoriesSince(ctx, "h1", time.Now().Add(time.Hour)); len(st) != 0 {
		t.Fatalf("stories later = %v", st)
	}
}
