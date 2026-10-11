package store

import (
	"context"
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

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

func TestThreads(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com"}); err != nil {
		t.Fatal(err)
	}
	th := Thread{Provider: "slack", ChannelID: "C1", ThreadTS: "1.0", UserID: "h1", ClaudeSessionID: "s1"}
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	th.ClaudeSessionID = "s2"
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread(ctx, "slack", "C1", "1.0")
	if err != nil || got == nil || got.ClaudeSessionID != "s2" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got, _ := s.GetThread(ctx, "slack", "C1", "2.0"); got != nil {
		t.Fatal("want nil")
	}
}

func TestMarkEvent(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, want := range []bool{true, false} {
		fresh, err := s.MarkEvent(ctx, "slack", "Ev1")
		if err != nil || fresh != want {
			t.Fatalf("call %d: fresh=%v err=%v", i, fresh, err)
		}
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
