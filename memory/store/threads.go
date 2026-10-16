package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

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
