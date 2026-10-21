package store

import (
	"context"
	"testing"
	"time"
)

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
