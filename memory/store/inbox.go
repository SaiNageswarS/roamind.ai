package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// InboxPosted reports whether the user's inbox digest for day, a local ISO
// date, is posted or being posted.
func (s *Store) InboxPosted(ctx context.Context, userID, day string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM inbox_digests WHERE user_id = $1 AND day = $2`, userID, day).Scan(&n)
	return n > 0, err
}

// ReserveInbox reserves the user's inbox digest for day before it is
// posted, and returns its id; ok is false when the day is already reserved
// or posted. Reserving first means a run that dies after posting can't be
// replayed into a second post.
func (s *Store) ReserveInbox(ctx context.Context, userID, channelID, day string) (id int64, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO inbox_digests (user_id, day, channel_id, posted_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, day) DO NOTHING
		RETURNING id`, userID, day, channelID, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// ReleaseInbox gives back a reserved day whose digest was not posted.
func (s *Store) ReleaseInbox(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM inbox_digests WHERE id = $1 AND ts IS NULL`, id)
	return err
}

// CompleteInbox records a reserved inbox digest as posted, as the message
// ts with text.
func (s *Store) CompleteInbox(ctx context.Context, id int64, ts, text string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE inbox_digests SET ts = $1, text = $2, posted_at = $3 WHERE id = $4`,
		ts, text, time.Now().Unix(), id)
	return err
}
