// Package store is the SQLite persistence for channels: user identities,
// thread → claude session mapping and inbound event dedupe. Conversation
// content itself lives only in claude's session files.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	telegram_user_id TEXT UNIQUE,
	created_at       INTEGER NOT NULL,
	updated_at       INTEGER NOT NULL,
	UNIQUE (slack_team_id, slack_user_id)
);
CREATE TABLE IF NOT EXISTS threads (
	provider          TEXT NOT NULL,
	channel_id        TEXT NOT NULL,
	thread_ts         TEXT NOT NULL,
	user_id           TEXT NOT NULL REFERENCES users(id),
	claude_session_id TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	updated_at        INTEGER NOT NULL,
	PRIMARY KEY (provider, channel_id, thread_ts)
);
CREATE TABLE IF NOT EXISTS inbound_events (
	provider    TEXT NOT NULL,
	event_id    TEXT NOT NULL,
	received_at INTEGER NOT NULL,
	PRIMARY KEY (provider, event_id)
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
}

// UserBySlack finds the user linked to a Slack identity, or nil.
func (s *Store) UserBySlack(ctx context.Context, teamID, slackUserID string) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, email, display_name, slack_team_id, slack_user_id FROM users
		 WHERE slack_team_id = ? AND slack_user_id = ?`, teamID, slackUserID)
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.SlackTeamID, &u.SlackUserID)
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

// Thread maps a chat thread to a claude session.
type Thread struct {
	Provider, ChannelID, ThreadTS string
	UserID                        string
	ClaudeSessionID               string
}

// GetThread returns the thread or nil if it has never been seen.
func (s *Store) GetThread(ctx context.Context, provider, channelID, threadTS string) (*Thread, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT user_id, claude_session_id FROM threads
		 WHERE provider = ? AND channel_id = ? AND thread_ts = ?`, provider, channelID, threadTS)
	t := Thread{Provider: provider, ChannelID: channelID, ThreadTS: threadTS}
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
		INSERT INTO threads (provider, channel_id, thread_ts, user_id, claude_session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, channel_id, thread_ts) DO UPDATE SET
			claude_session_id = excluded.claude_session_id,
			updated_at = excluded.updated_at`,
		t.Provider, t.ChannelID, t.ThreadTS, t.UserID, t.ClaudeSessionID, now, now)
	return err
}

// MarkEvent records an inbound event and reports whether it is new. A false
// result means the event was already processed (e.g. a Slack retry).
func (s *Store) MarkEvent(ctx context.Context, provider, eventID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_events (provider, event_id, received_at) VALUES (?, ?, ?)
		 ON CONFLICT DO NOTHING`, provider, eventID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
