package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
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
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO exercises (id, user_id, goal_permalink, channel_id, thread_ts, note_permalink, posted_at, created_on, updated_on)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)`,
		rowID(e.ChannelID, e.ThreadTS), e.UserID, e.GoalPermalink, e.ChannelID, e.ThreadTS, e.NotePermalink, e.PostedAt.Unix(), now)
	return err
}

// LastExercise returns the goal's most recent exercise, or nil.
func (s *Store) LastExercise(ctx context.Context, userID, goalPermalink string) (*Exercise, error) {
	r, err := async.Await(table[exerciseRow](s).
		Where("user_id = ? AND goal_permalink = ?", userID, goalPermalink).
		OrderBy("posted_at DESC").First(ctx))
	if errors.Is(err, odm.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e := &Exercise{UserID: userID, GoalPermalink: goalPermalink, ChannelID: r.ChannelID, ThreadTS: r.ThreadTS,
		NotePermalink: r.NotePermalink, PostedAt: time.Unix(r.PostedAt, 0)}
	if r.AnsweredAt != nil {
		e.AnsweredAt = time.Unix(*r.AnsweredAt, 0)
	}
	return e, nil
}

// MarkExerciseAnswered records a reply in a thread. When the thread is an
// exercise's, it returns the exercise's note permalink; otherwise "".
func (s *Store) MarkExerciseAnswered(ctx context.Context, channelID, threadTS string) (string, error) {
	var note string
	err := s.db.QueryRowContext(ctx, `
		UPDATE exercises SET answered_at = COALESCE(answered_at, $1)
		WHERE channel_id = $2 AND thread_ts = $3
		RETURNING note_permalink`,
		time.Now().Unix(), channelID, threadTS).Scan(&note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return note, err
}
