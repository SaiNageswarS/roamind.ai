package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// MailAccount is a Gmail account whose Apps Script sends its mail to
// Roamind.
type MailAccount struct {
	Address     string
	UserID      string
	ConnectedAt time.Time
	SeenAt      time.Time // zero before the script's first request
}

// MailMessage is a message an account's script sent.
type MailMessage struct {
	Address   string
	MessageID string
	UserID    string
	Category  string
	Status    string
}

// Statuses of a MailMessage. A skipped message held nothing to save.
const (
	MailPicked  = "picked"
	MailDone    = "done"
	MailSkipped = "skipped"
	MailFailed  = "failed"
)

// ConnectMail adds the user's Gmail account, or moves it to them; task
// mail-connect does the same with the sqlite3 CLI.
func (s *Store) ConnectMail(ctx context.Context, address, userID string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_accounts (address, user_id, connected_at) VALUES (?, ?, ?)
		ON CONFLICT (address) DO UPDATE SET user_id = excluded.user_id, connected_at = excluded.connected_at`,
		strings.TrimSpace(address), userID, time.Now().Unix())
	return err
}

// MailAccountOf returns the account with address, in any case, or nil.
func (s *Store) MailAccountOf(ctx context.Context, address string) (*MailAccount, error) {
	var a MailAccount
	var connected int64
	var seen sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT address, user_id, connected_at, seen_at FROM mail_accounts WHERE address = ?`,
		strings.TrimSpace(address)).Scan(&a.Address, &a.UserID, &connected, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.ConnectedAt = time.Unix(connected, 0)
	if seen.Valid {
		a.SeenAt = time.Unix(seen.Int64, 0)
	}
	return &a, nil
}

// SeenMail records a request from the account's script at at.
func (s *Store) SeenMail(ctx context.Context, address string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mail_accounts SET seen_at = ? WHERE address = ?`,
		at.Unix(), strings.TrimSpace(address))
	return err
}

// StaleMailAccounts returns the user's accounts whose script hasn't sent a
// request since before, by address; an account never heard from counts
// from when it was connected.
func (s *Store) StaleMailAccounts(ctx context.Context, userID string, before time.Time) ([]MailAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT address, user_id, connected_at, seen_at FROM mail_accounts
		WHERE user_id = ? AND COALESCE(seen_at, connected_at) < ? ORDER BY address`, userID, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailAccount
	for rows.Next() {
		var a MailAccount
		var connected int64
		var seen sql.NullInt64
		if err := rows.Scan(&a.Address, &a.UserID, &connected, &seen); err != nil {
			return nil, err
		}
		a.ConnectedAt = time.Unix(connected, 0)
		if seen.Valid {
			a.SeenAt = time.Unix(seen.Int64, 0)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PickMessage records m as picked, before its turn; ok is false when the
// message was sent before, unless its turn failed then.
func (s *Store) PickMessage(ctx context.Context, m MailMessage) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_messages (address, message_id, user_id, category, status, received_at)
		VALUES (?, ?, ?, ?, 'picked', ?)
		ON CONFLICT (address, message_id) DO UPDATE SET
			status = 'picked', category = excluded.category, received_at = excluded.received_at, done_at = NULL
		WHERE mail_messages.status = 'failed'`,
		strings.TrimSpace(m.Address), m.MessageID, m.UserID, m.Category, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishMessage records the picked message as done, skipped or failed.
func (s *Store) FinishMessage(ctx context.Context, address, messageID, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE mail_messages SET status = ?, done_at = ?
		WHERE address = ? AND message_id = ? AND status = 'picked'`,
		status, time.Now().Unix(), strings.TrimSpace(address), messageID)
	return err
}

// MessageStatus returns the message's status, or "" when it was never sent.
func (s *Store) MessageStatus(ctx context.Context, address, messageID string) (string, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM mail_messages WHERE address = ? AND message_id = ?`,
		strings.TrimSpace(address), messageID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return status, err
}
