package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Exercise is one exercise posted for a learning goal.
type Exercise struct {
	UserID, GoalPermalink string
	ChannelID, ThreadTS   string
	NotePermalink         string
	PostedAt              time.Time
	// AnsweredAt is when the user first replied; zero while unanswered.
	AnsweredAt time.Time
}

// SaveExercise records a posted exercise.
func (s *Store) SaveExercise(ctx context.Context, e Exercise) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO exercises (user_id, goal_permalink, channel_id, thread_ts, note_permalink, posted_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.UserID, e.GoalPermalink, e.ChannelID, e.ThreadTS, e.NotePermalink, e.PostedAt.Unix())
	return err
}

// LastExercise returns the goal's most recent exercise, or nil.
func (s *Store) LastExercise(ctx context.Context, userID, goalPermalink string) (*Exercise, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT channel_id, thread_ts, note_permalink, posted_at, answered_at FROM exercises
		WHERE user_id = ? AND goal_permalink = ? ORDER BY posted_at DESC LIMIT 1`, userID, goalPermalink)
	e := Exercise{UserID: userID, GoalPermalink: goalPermalink}
	var posted int64
	var answered sql.NullInt64
	err := row.Scan(&e.ChannelID, &e.ThreadTS, &e.NotePermalink, &posted, &answered)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	e.PostedAt = time.Unix(posted, 0)
	if answered.Valid {
		e.AnsweredAt = time.Unix(answered.Int64, 0)
	}
	return &e, err
}

// MarkExerciseAnswered records a reply in a thread. When the thread is an
// exercise's, it returns the exercise's note permalink; otherwise "".
func (s *Store) MarkExerciseAnswered(ctx context.Context, channelID, threadTS string) (string, error) {
	var note string
	err := s.db.QueryRowContext(ctx, `
		UPDATE exercises SET answered_at = COALESCE(answered_at, ?)
		WHERE channel_id = ? AND thread_ts = ?
		RETURNING note_permalink`,
		time.Now().Unix(), channelID, threadTS).Scan(&note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return note, err
}
