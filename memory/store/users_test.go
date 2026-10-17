package store

import (
	"context"
	"testing"
)

func TestUsers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if u, err := s.UserBySlack(ctx, "T1", "U1"); err != nil || u != nil {
		t.Fatalf("u=%v err=%v", u, err)
	}
	u := User{ID: "h1", Email: "a@x.com", DisplayName: "A", SlackTeamID: "T1", SlackUserID: "U1"}
	if err := s.UpsertSlackUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	u.DisplayName = "A2"
	if err := s.UpsertSlackUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, err := s.UserBySlack(ctx, "T1", "U1")
	if err != nil || got == nil || got.ID != "h1" || got.DisplayName != "A2" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestAssignMemoryProject(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for _, u := range []User{
		{ID: "a", Email: "a@x", DisplayName: "Sai", SlackTeamID: "T", SlackUserID: "U1"},
		{ID: "b", Email: "b@x", DisplayName: "Sai", SlackTeamID: "T", SlackUserID: "U2"},
	} {
		if err := s.UpsertSlackUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if u, _ := s.UserBySlack(ctx, "T", "U1"); u.MemoryProject != "" {
		t.Fatalf("unassigned project = %q", u.MemoryProject)
	}
	pa, err := s.AssignMemoryProject(ctx, "a", "bm-u-sai")
	if err != nil || pa != "bm-u-sai-1" {
		t.Fatalf("a = %q, %v", pa, err)
	}
	pb, err := s.AssignMemoryProject(ctx, "b", "bm-u-sai")
	if err != nil || pb != "bm-u-sai-2" {
		t.Fatalf("b = %q, %v", pb, err)
	}
	// Sticky, whatever the base, and read back with the user row.
	if p, _ := s.AssignMemoryProject(ctx, "a", "bm-u-renamed"); p != "bm-u-sai-1" {
		t.Fatalf("a again = %q", p)
	}
	if err := s.UpsertSlackUser(ctx, User{ID: "a", Email: "a@x", DisplayName: "Renamed", SlackTeamID: "T", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserBySlack(ctx, "T", "U1"); u.MemoryProject != "bm-u-sai-1" {
		t.Fatalf("user row project = %q", u.MemoryProject)
	}
}

func TestWebUsers(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertWebUser(ctx, User{ID: "w", Email: "w@x", DisplayName: "W"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "w"); u == nil || u.Channel != ChannelWeb {
		t.Fatalf("web user = %+v", u)
	}
	// A Slack user who signs in on the web stays a Slack user.
	if err := s.UpsertSlackUser(ctx, User{ID: "s", Email: "s@x", DisplayName: "S", SlackTeamID: "T", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertWebUser(ctx, User{ID: "s", Email: "s@x", DisplayName: "Web name"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "s"); u.Channel != ChannelSlack || u.DisplayName != "S" || u.SlackUserID != "U1" {
		t.Fatalf("slack user = %+v", u)
	}
	// Only Slack users get Slack features.
	us, err := s.Users(ctx)
	if err != nil || len(us) != 1 || us[0].ID != "s" {
		t.Fatalf("users = %+v, %v", us, err)
	}
	// A web user who joins Slack becomes a Slack user.
	if err := s.UpsertSlackUser(ctx, User{ID: "w", Email: "w@x", DisplayName: "W", SlackTeamID: "T", SlackUserID: "U2"}); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, "w"); u.Channel != ChannelSlack {
		t.Fatalf("joined = %+v", u)
	}
}
