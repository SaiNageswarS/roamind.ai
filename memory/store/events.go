package store

import (
	"context"
	"time"
)

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
