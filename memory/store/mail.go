package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// MailAccount is a Gmail account whose Apps Script sends its mail to
// Roamind.
type MailAccount struct {
	Address     string
	UserID      string
	Secret      string // the account's signing key, hex
	ConnectedAt time.Time
	SeenAt      time.Time // zero before the script's first request
}

// MailMessage is a message an account's script sent.
type MailMessage struct {
	Address   string
	MessageID string
	UserID    string
	Category  string
	Subject   string
	Link      string
	Status    string
	PickedAt  time.Time
}

// Statuses of a MailMessage. A skipped message held nothing to save.
const (
	MailPicked  = "picked"
	MailDone    = "done"
	MailSkipped = "skipped"
	MailFailed  = "failed"
)

// ConnectMail adds the user's Gmail account, or moves it to them, with a
// new signing key, which it returns for the account's script; task
// mail-connect does the same with the sqlite3 CLI.
func (s *Store) ConnectMail(ctx context.Context, address, userID string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(b)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_accounts (address, user_id, secret, connected_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (address) DO UPDATE SET user_id = excluded.user_id, secret = excluded.secret,
			connected_at = excluded.connected_at`,
		strings.TrimSpace(address), userID, secret, time.Now().Unix())
	return secret, err
}

const mailAccountCols = `address, user_id, secret, connected_at, seen_at`

func scanMailAccount(sc interface{ Scan(...any) error }) (MailAccount, error) {
	var a MailAccount
	var connected int64
	var seen sql.NullInt64
	if err := sc.Scan(&a.Address, &a.UserID, &a.Secret, &connected, &seen); err != nil {
		return a, err
	}
	a.ConnectedAt = time.Unix(connected, 0)
	if seen.Valid {
		a.SeenAt = time.Unix(seen.Int64, 0)
	}
	return a, nil
}

// MailAccountOf returns the account with address, in any case, or nil.
func (s *Store) MailAccountOf(ctx context.Context, address string) (*MailAccount, error) {
	a, err := scanMailAccount(s.db.QueryRowContext(ctx, `
		SELECT `+mailAccountCols+` FROM mail_accounts WHERE address = ?`, strings.TrimSpace(address)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
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
		SELECT `+mailAccountCols+` FROM mail_accounts
		WHERE user_id = ? AND COALESCE(seen_at, connected_at) < ? ORDER BY address`, userID, before.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailAccount
	for rows.Next() {
		a, err := scanMailAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PickMessage records m as picked, before its turn; ok is false when the
// message was sent before, unless its turn failed then, or its run was
// picked before stuckBefore and never finished, as when it was cancelled,
// or it now goes to the ledger (category finance) and didn't then: a
// broker's mail sent again after the user trusted its domain.
func (s *Store) PickMessage(ctx context.Context, m MailMessage, stuckBefore time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO mail_messages (address, message_id, user_id, category, subject, link, status, picked_at)
		VALUES (?, ?, ?, ?, ?, ?, 'picked', ?)
		ON CONFLICT (address, message_id) DO UPDATE SET
			status = 'picked', category = excluded.category, subject = excluded.subject, link = excluded.link,
			picked_at = excluded.picked_at, done_at = NULL
		WHERE mail_messages.status = 'failed' OR (mail_messages.status = 'picked' AND mail_messages.picked_at < ?)
			OR (excluded.category = 'finance' AND mail_messages.category != 'finance')`,
		strings.TrimSpace(m.Address), m.MessageID, m.UserID, m.Category, m.Subject, m.Link, time.Now().Unix(),
		stuckBefore.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishMessage records the message as done, skipped or failed. Only a
// picked or failed message is finished: a failed run replayed from its
// relate or save task, which doesn't pick it again, can still finish it.
func (s *Store) FinishMessage(ctx context.Context, address, messageID, status string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE mail_messages SET status = ?, done_at = ?
		WHERE address = ? AND message_id = ? AND status IN ('picked', 'failed')`,
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

// UnsavedMail returns the user's messages picked since since whose turn
// failed, or whose run was picked before stuckBefore and never finished,
// oldest first.
func (s *Store) UnsavedMail(ctx context.Context, userID string, since, stuckBefore time.Time) ([]MailMessage, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT address, message_id, user_id, category, subject, link, status, picked_at FROM mail_messages
		WHERE user_id = ? AND picked_at >= ? AND (status = 'failed' OR (status = 'picked' AND picked_at < ?))
		ORDER BY picked_at, address, message_id`, userID, since.Unix(), stuckBefore.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailMessage
	for rows.Next() {
		var m MailMessage
		var picked int64
		if err := rows.Scan(&m.Address, &m.MessageID, &m.UserID, &m.Category, &m.Subject, &m.Link, &m.Status, &picked); err != nil {
			return nil, err
		}
		m.PickedAt = time.Unix(picked, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}
