package store

import (
	"context"
	"time"
)

// MarkEvent records an inbound event and reports whether it is new. A false
// result means the event was already processed (e.g. a Slack retry).
func (s *Store) MarkEvent(ctx context.Context, eventID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO inbound_events (event_id, received_at) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, eventID, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// EventSeen reports whether MarkEvent recorded eventID.
func (s *Store) EventSeen(ctx context.Context, eventID string) (bool, error) {
	var seen bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM inbound_events WHERE event_id = $1)`, eventID).Scan(&seen)
	return seen, err
}
