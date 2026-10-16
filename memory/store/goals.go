package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GoalChannel is the Slack channel of one of a user's goals.
type GoalChannel struct {
	UserID, GoalPermalink, GoalTitle string
	ChannelID                        string
	// OwnerSlackUserID is the goal owner's Slack user, read from users.
	OwnerSlackUserID string
}

// GoalChannelOf returns the goal that the Slack user slackUserID has in
// this channel, or nil when the channel is not a goal channel of theirs.
func (s *Store) GoalChannelOf(ctx context.Context, channelID, slackUserID string) (*GoalChannel, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT g.user_id, g.goal_permalink, g.goal_title
		FROM goal_channels g JOIN users u ON u.id = g.user_id
		WHERE g.channel_id = ? AND u.slack_user_id = ?`, channelID, slackUserID)
	gc := GoalChannel{ChannelID: channelID, OwnerSlackUserID: slackUserID}
	err := row.Scan(&gc.UserID, &gc.GoalPermalink, &gc.GoalTitle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &gc, err
}

// GoalsWithChannel returns the permalinks of the user's goals that have a
// channel.
func (s *Store) GoalsWithChannel(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT goal_permalink FROM goal_channels WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		have[p] = true
	}
	return have, rows.Err()
}

// GoalChannels returns every goal channel.
func (s *Store) GoalChannels(ctx context.Context) ([]GoalChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT g.user_id, g.goal_permalink, g.goal_title, g.channel_id, COALESCE(u.slack_user_id, '')
		FROM goal_channels g JOIN users u ON u.id = g.user_id
		ORDER BY g.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gcs []GoalChannel
	for rows.Next() {
		var gc GoalChannel
		if err := rows.Scan(&gc.UserID, &gc.GoalPermalink, &gc.GoalTitle, &gc.ChannelID, &gc.OwnerSlackUserID); err != nil {
			return nil, err
		}
		gcs = append(gcs, gc)
	}
	return gcs, rows.Err()
}

// SaveGoalChannel records the channel created for a goal.
func (s *Store) SaveGoalChannel(ctx context.Context, gc GoalChannel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO goal_channels (user_id, goal_permalink, goal_title, channel_id, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		gc.UserID, gc.GoalPermalink, gc.GoalTitle, gc.ChannelID, time.Now().Unix())
	return err
}

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
