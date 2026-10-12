// Package store is the SQLite persistence for Slack: user identities,
// thread → claude session mapping, goal channels and inbound event dedupe. Conversation
// content itself lives only in claude's session files.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id               TEXT PRIMARY KEY, -- sha256(lowercase(trim(email)))
	email            TEXT NOT NULL UNIQUE,
	display_name     TEXT NOT NULL DEFAULT '',
	slack_team_id    TEXT,
	slack_user_id    TEXT,
	memory_project   TEXT UNIQUE, -- Basic Memory project, bm-u-<name>-<idx>
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	UNIQUE (slack_team_id, slack_user_id)
);
CREATE TABLE IF NOT EXISTS threads (
	channel_id        TEXT NOT NULL,
	thread_ts         TEXT NOT NULL,
	user_id           TEXT NOT NULL REFERENCES users(id),
	claude_session_id TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	PRIMARY KEY (channel_id, thread_ts)
);
-- One channel per goal, created by the harness; the agent never sees it.
-- Several users can share a channel, each linked to the goal in their own
-- memory.
CREATE TABLE IF NOT EXISTS goal_channels (
	user_id        TEXT NOT NULL REFERENCES users(id),
	goal_permalink TEXT NOT NULL, -- Basic Memory permalink of the goal note
	goal_title     TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	created_at     INTEGER NOT NULL,
	PRIMARY KEY (user_id, goal_permalink),
	UNIQUE (channel_id, user_id)
);
-- Daily exercises posted for learning goals; a reply in the exercise's
-- thread answers it.
CREATE TABLE IF NOT EXISTS exercises (
	user_id        TEXT NOT NULL REFERENCES users(id),
	goal_permalink TEXT NOT NULL,
	channel_id     TEXT NOT NULL,
	thread_ts      TEXT NOT NULL, -- the exercise message
	note_permalink TEXT NOT NULL, -- the exercise note in the user's memory
	posted_at      INTEGER NOT NULL,
	answered_at    INTEGER, -- first reply in the thread; NULL until then
	PRIMARY KEY (channel_id, thread_ts)
);
CREATE INDEX IF NOT EXISTS exercises_goal ON exercises (user_id, goal_permalink, posted_at);
CREATE TABLE IF NOT EXISTS inbound_events (
	event_id    TEXT PRIMARY KEY, -- Slack event_id
	received_at INTEGER NOT NULL
);
`

// Store wraps the SQLite database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// User is a person known to the agent, keyed by email hash.
type User struct {
	ID          string
	Email       string
	DisplayName string
	SlackTeamID string
	SlackUserID string
	// MemoryProject is the user's Basic Memory project, assigned once by
	// AssignMemoryProject; empty until then.
	MemoryProject string
}

// UserBySlack finds the user linked to a Slack identity, or nil.
func (s *Store) UserBySlack(ctx context.Context, teamID, slackUserID string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, display_name, slack_team_id, slack_user_id, COALESCE(memory_project, '') FROM users
		 WHERE slack_team_id = ? AND slack_user_id = ?`, teamID, slackUserID)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.SlackTeamID, &u.SlackUserID, &u.MemoryProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// UserByID finds a user by id, or nil.
func (s *Store) UserByID(ctx context.Context, id string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, display_name, COALESCE(slack_team_id, ''), COALESCE(slack_user_id, ''), COALESCE(memory_project, '') FROM users
		 WHERE id = ?`, id)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.SlackTeamID, &u.SlackUserID, &u.MemoryProject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// UpsertSlackUser creates the user for u.ID or links the Slack identity to
// the existing user with that email.
func (s *Store) UpsertSlackUser(ctx context.Context, u User) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (id, email, display_name, slack_team_id, slack_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			display_name = excluded.display_name,
			slack_team_id = excluded.slack_team_id,
			slack_user_id = excluded.slack_user_id,
			updated_at = excluded.updated_at`,
		u.ID, u.Email, u.DisplayName, u.SlackTeamID, u.SlackUserID, now, now)
	return err
}

// AssignMemoryProject gives the user a Basic Memory project named base-<n>,
// with the smallest n no other user has, and returns it. It is called when
// the user is created; afterwards the name is read with the user row, and
// it sticks even if the user is renamed.
func (s *Store) AssignMemoryProject(ctx context.Context, userID, base string) (string, error) {
	var cur sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT memory_project FROM users WHERE id = ?`, userID).Scan(&cur)
	if err != nil {
		return "", err
	}
	if cur.Valid && cur.String != "" {
		return cur.String, nil
	}
	for n := 1; n < 1000; n++ {
		name := fmt.Sprintf("%s-%d", base, n)
		res, err := s.db.ExecContext(ctx,
			`UPDATE users SET memory_project = ? WHERE id = ? AND memory_project IS NULL`, name, userID)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				continue // taken by another user
			}
			return "", err
		}
		if k, _ := res.RowsAffected(); k == 1 {
			return name, nil
		}
		// Assigned concurrently; use that.
		return s.AssignMemoryProject(ctx, userID, base)
	}
	return "", fmt.Errorf("no free memory project name for %s", base)
}

// Thread maps a Slack thread to a claude session.
type Thread struct {
	ChannelID, ThreadTS string
	UserID              string
	ClaudeSessionID     string
}

// GetThread returns the thread or nil if it has never been seen.
func (s *Store) GetThread(ctx context.Context, channelID, threadTS string) (*Thread, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, claude_session_id FROM threads
		 WHERE channel_id = ? AND thread_ts = ?`, channelID, threadTS)
	t := Thread{ChannelID: channelID, ThreadTS: threadTS}
	err := row.Scan(&t.UserID, &t.ClaudeSessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}

// SaveThread records the claude session for a thread.
func (s *Store) SaveThread(ctx context.Context, t Thread) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO threads (channel_id, thread_ts, user_id, claude_session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (channel_id, thread_ts) DO UPDATE SET
			claude_session_id = excluded.claude_session_id,
			updated_at = excluded.updated_at`,
		t.ChannelID, t.ThreadTS, t.UserID, t.ClaudeSessionID, now, now)
	return err
}

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

// MarkEvent records an inbound event and reports whether it is new. A false
// result means the event was already processed (e.g. a Slack retry).
func (s *Store) MarkEvent(ctx context.Context, eventID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_events (event_id, received_at) VALUES (?, ?)
		 ON CONFLICT DO NOTHING`, eventID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
