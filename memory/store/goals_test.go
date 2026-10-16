package store

import (
	"context"
	"testing"
	"time"
)

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
