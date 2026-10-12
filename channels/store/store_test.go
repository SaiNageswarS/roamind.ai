package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
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
	th := Thread{ChannelID: "C1", ThreadTS: "1.0", UserID: "h1", ClaudeSessionID: "s1"}
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	th.ClaudeSessionID = "s2"
	if err := s.SaveThread(ctx, th); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetThread(ctx, "C1", "1.0")
	if err != nil || got == nil || got.ClaudeSessionID != "s2" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got, _ := s.GetThread(ctx, "C1", "2.0"); got != nil {
		t.Fatal("want nil")
	}
}

func TestMarkEvent(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	for i, want := range []bool{true, false} {
		fresh, err := s.MarkEvent(ctx, "Ev1")
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

func TestGoalChannels(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if u, err := s.UserByID(ctx, "h1"); err != nil || u == nil || u.SlackUserID != "U1" {
		t.Fatalf("UserByID = %+v %v", u, err)
	}
	if gc, err := s.GoalChannelOf(ctx, "G1", "U1"); err != nil || gc != nil {
		t.Fatalf("gc=%v err=%v", gc, err)
	}
	gc := GoalChannel{UserID: "h1", GoalPermalink: "p/goals/jax", GoalTitle: "Learn JAX", ChannelID: "G1"}
	if err := s.SaveGoalChannel(ctx, gc); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGoalChannel(ctx, gc); err == nil {
		t.Fatal("duplicate goal channel saved")
	}
	got, err := s.GoalChannelOf(ctx, "G1", "U1")
	gc.OwnerSlackUserID = "U1"
	if err != nil || got == nil || *got != gc {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	// Another user shares the channel with a goal in their own memory.
	if gc, err := s.GoalChannelOf(ctx, "G1", "U2"); err != nil || gc != nil {
		t.Fatalf("non-member: gc=%v err=%v", gc, err)
	}
	if err := s.UpsertSlackUser(ctx, User{ID: "h2", Email: "b@x.com", SlackTeamID: "T1", SlackUserID: "U2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveGoalChannel(ctx, GoalChannel{UserID: "h2", GoalPermalink: "q/goals/jax", GoalTitle: "JAX too", ChannelID: "G1"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GoalChannelOf(ctx, "G1", "U2"); err != nil || got == nil || got.UserID != "h2" || got.GoalPermalink != "q/goals/jax" {
		t.Fatalf("shared: got=%+v err=%v", got, err)
	}
	have, err := s.GoalsWithChannel(ctx, "h1")
	if err != nil || len(have) != 1 || !have["p/goals/jax"] {
		t.Fatalf("have=%v err=%v", have, err)
	}
	all, err := s.GoalChannels(ctx)
	if err != nil || len(all) != 2 || all[0] != gc {
		t.Fatalf("all=%+v err=%v", all, err)
	}
}

func TestExercises(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.UpsertSlackUser(ctx, User{ID: "h1", Email: "a@x.com", SlackTeamID: "T1", SlackUserID: "U1"}); err != nil {
		t.Fatal(err)
	}
	if e, err := s.LastExercise(ctx, "h1", "p/goals/de"); err != nil || e != nil {
		t.Fatalf("e=%v err=%v", e, err)
	}
	day := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	for i, ts := range []string{"1.0", "2.0"} {
		e := Exercise{UserID: "h1", GoalPermalink: "p/goals/de", ChannelID: "G1", ThreadTS: ts, NotePermalink: "p/exercises/de/" + ts, PostedAt: day.AddDate(0, 0, i)}
		if err := s.SaveExercise(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	e, err := s.LastExercise(ctx, "h1", "p/goals/de")
	if err != nil || e == nil || e.ThreadTS != "2.0" || e.NotePermalink != "p/exercises/de/2.0" || !e.AnsweredAt.IsZero() || !e.PostedAt.Equal(day.AddDate(0, 0, 1)) {
		t.Fatalf("last=%+v err=%v", e, err)
	}
	if note, err := s.MarkExerciseAnswered(ctx, "G1", "9.0"); err != nil || note != "" {
		t.Fatalf("other thread marked: %q %v", note, err)
	}
	if note, err := s.MarkExerciseAnswered(ctx, "G1", "2.0"); err != nil || note != "p/exercises/de/2.0" {
		t.Fatalf("mark: %q %v", note, err)
	}
	first, _ := s.LastExercise(ctx, "h1", "p/goals/de")
	if first.AnsweredAt.IsZero() {
		t.Fatal("not answered")
	}
	// Later replies keep the first answer time.
	s.db.Exec(`UPDATE exercises SET answered_at = 1`)
	s.MarkExerciseAnswered(ctx, "G1", "2.0")
	if again, _ := s.LastExercise(ctx, "h1", "p/goals/de"); again.AnsweredAt.Unix() != 1 {
		t.Fatalf("answered_at moved: %v", again.AnsweredAt)
	}
}
