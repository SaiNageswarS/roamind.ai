package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FinAccount is one of a user's brokerage accounts.
type FinAccount struct {
	UserID   string
	ID       string // a1, a2, …
	Broker   string
	Number   string
	Currency string
	Name     string
}

// Label is how the account is shown: its name, else its broker and number.
func (a FinAccount) Label() string {
	if a.Name != "" {
		return a.Name
	}
	if a.Number == "" {
		return a.Broker
	}
	return a.Broker + " " + a.Number
}

const finAccountCols = `user_id, id, broker, number, currency, name`

func scanFinAccount(sc interface{ Scan(...any) error }) (FinAccount, error) {
	var a FinAccount
	err := sc.Scan(&a.UserID, &a.ID, &a.Broker, &a.Number, &a.Currency, &a.Name)
	return a, err
}

// EnsureFinAccount returns the user's account at broker with number,
// making it when there is none. Broker is matched without case.
func (s *Store) EnsureFinAccount(ctx context.Context, userID, broker, number, currency string) (FinAccount, bool, error) {
	broker, number = strings.ToLower(strings.TrimSpace(broker)), strings.TrimSpace(number)
	if broker == "" {
		return FinAccount{}, false, errors.New("an account needs its broker")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FinAccount{}, false, err
	}
	defer tx.Rollback()
	a, err := scanFinAccount(tx.QueryRowContext(ctx, `SELECT `+finAccountCols+` FROM fin_accounts
		WHERE user_id = ? AND broker = ? AND number = ?`, userID, broker, number))
	if err == nil {
		return a, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return a, false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fin_accounts WHERE user_id = ?`, userID).Scan(&n); err != nil {
		return a, false, err
	}
	a = FinAccount{UserID: userID, ID: fmt.Sprintf("a%d", n+1), Broker: broker, Number: number, Currency: strings.ToUpper(strings.TrimSpace(currency))}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fin_accounts (`+finAccountCols+`, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.UserID, a.ID, a.Broker, a.Number, a.Currency, a.Name, time.Now().Unix()); err != nil {
		return a, false, err
	}
	return a, true, tx.Commit()
}

// FinAccounts returns the user's accounts, oldest first.
func (s *Store) FinAccounts(ctx context.Context, userID string) ([]FinAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+finAccountCols+` FROM fin_accounts WHERE user_id = ? ORDER BY created_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var as []FinAccount
	for rows.Next() {
		a, err := scanFinAccount(rows)
		if err != nil {
			return nil, err
		}
		as = append(as, a)
	}
	return as, rows.Err()
}

// FinAccountOf returns the user's account whose id or name is ref, in any
// case, or nil.
func (s *Store) FinAccountOf(ctx context.Context, userID, ref string) (*FinAccount, error) {
	ref = strings.TrimSpace(ref)
	a, err := scanFinAccount(s.db.QueryRowContext(ctx, `SELECT `+finAccountCols+` FROM fin_accounts
		WHERE user_id = ? AND (id = ? OR (name != '' AND name = ? COLLATE NOCASE))`, userID, strings.ToLower(ref), ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// NameFinAccount sets what the user calls an account; false when they have
// no account id.
func (s *Store) NameFinAccount(ctx context.Context, userID, id, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE fin_accounts SET name = ? WHERE user_id = ? AND id = ?`,
		strings.TrimSpace(name), userID, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FinanceUpload is a report imported from an upload.
type FinanceUpload struct {
	UserID     string
	SHA256     string
	File       string
	Account    string
	Doc        string
	PeriodFrom string // ISO dates; "" for holdings only
	PeriodTo   string
	Rows       int
	ImportedAt time.Time
}

const financeUploadCols = `user_id, sha256, file, account, doc, period_from, period_to, rows, imported_at`

func scanFinanceUpload(sc interface{ Scan(...any) error }) (FinanceUpload, error) {
	var u FinanceUpload
	var at int64
	err := sc.Scan(&u.UserID, &u.SHA256, &u.File, &u.Account, &u.Doc, &u.PeriodFrom, &u.PeriodTo, &u.Rows, &at)
	u.ImportedAt = time.Unix(at, 0)
	return u, err
}

// FinanceUploadOf returns the user's import of the file with this hash, or
// nil.
func (s *Store) FinanceUploadOf(ctx context.Context, userID, sha string) (*FinanceUpload, error) {
	u, err := scanFinanceUpload(s.db.QueryRowContext(ctx, `SELECT `+financeUploadCols+` FROM finance_uploads
		WHERE user_id = ? AND sha256 = ?`, userID, sha))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, err
}

// SaveFinanceUpload records an imported report.
func (s *Store) SaveFinanceUpload(ctx context.Context, u FinanceUpload) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO finance_uploads (`+financeUploadCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		u.UserID, u.SHA256, u.File, u.Account, u.Doc, u.PeriodFrom, u.PeriodTo, u.Rows, time.Now().Unix())
	return err
}

// FinanceUploads returns the user's imported reports, oldest first.
func (s *Store) FinanceUploads(ctx context.Context, userID string) ([]FinanceUpload, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+financeUploadCols+` FROM finance_uploads WHERE user_id = ? ORDER BY imported_at, rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var us []FinanceUpload
	for rows.Next() {
		u, err := scanFinanceUpload(rows)
		if err != nil {
			return nil, err
		}
		us = append(us, u)
	}
	return us, rows.Err()
}

// MarkFinanceDirty records that the user's trade events changed; it
// returns the mark, for ClearFinanceDirty.
func (s *Store) MarkFinanceDirty(ctx context.Context, userID string) (int64, error) {
	since := time.Now().UnixNano()
	_, err := s.db.ExecContext(ctx, `INSERT INTO finance_dirty (user_id, since) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET since = excluded.since`, userID, since)
	return since, err
}

// FinanceDirty returns the user's mark, or 0 when their trades are up to
// date.
func (s *Store) FinanceDirty(ctx context.Context, userID string) (int64, error) {
	var since int64
	err := s.db.QueryRowContext(ctx, `SELECT since FROM finance_dirty WHERE user_id = ?`, userID).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return since, err
}

// DirtyFinanceUsers returns the users whose trades are not up to date.
func (s *Store) DirtyFinanceUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT user_id FROM finance_dirty ORDER BY since`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClearFinanceDirty clears the user's mark if it is still since: a change
// made during the reconcile keeps them dirty.
func (s *Store) ClearFinanceDirty(ctx context.Context, userID string, since int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM finance_dirty WHERE user_id = ? AND since = ?`, userID, since)
	return err
}

// DefaultBaseCurrency is the base currency of a user who set none.
const DefaultBaseCurrency = "EUR"

// BaseCurrency returns the currency the user's totals are in.
func (s *Store) BaseCurrency(ctx context.Context, userID string) (string, error) {
	var c string
	err := s.db.QueryRowContext(ctx, `SELECT base_currency FROM finance_settings WHERE user_id = ?`, userID).Scan(&c)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultBaseCurrency, nil
	}
	return c, err
}

// SetBaseCurrency sets the currency the user's totals are in.
func (s *Store) SetBaseCurrency(ctx context.Context, userID, currency string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO finance_settings (user_id, base_currency) VALUES (?, ?)
		ON CONFLICT (user_id) DO UPDATE SET base_currency = excluded.base_currency`, userID, strings.ToUpper(currency))
	return err
}

// FinanceSender is a domain broker mail comes from (finance_senders).
type FinanceSender struct {
	Domain  string
	Trusted bool
	SeenAt  time.Time // zero before a finance mail from it
	Subject string    // that mail's
}

// TrustFinanceSender sets whether the user trusts mail from domain and its
// subdomains.
func (s *Store) TrustFinanceSender(ctx context.Context, userID, domain string, trusted bool) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finance_senders (user_id, domain, trusted, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, domain) DO UPDATE SET trusted = excluded.trusted, updated_at = excluded.updated_at`,
		userID, strings.ToLower(domain), trusted, time.Now().Unix())
	return err
}

// SeenFinanceSender records a finance mail from domain, keeping whether it
// is trusted (untrusted when new).
func (s *Store) SeenFinanceSender(ctx context.Context, userID, domain, subject string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finance_senders (user_id, domain, seen_at, subject, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user_id, domain) DO UPDATE SET seen_at = excluded.seen_at, subject = excluded.subject`,
		userID, strings.ToLower(domain), at.Unix(), subject, time.Now().Unix())
	return err
}

// FinanceSenders returns the user's senders, trusted ones first, then the
// most recently seen.
func (s *Store) FinanceSenders(ctx context.Context, userID string) ([]FinanceSender, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, trusted, seen_at, subject FROM finance_senders
		WHERE user_id = ? ORDER BY trusted DESC, seen_at DESC, domain`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FinanceSender
	for rows.Next() {
		var f FinanceSender
		var seen sql.NullInt64
		if err := rows.Scan(&f.Domain, &f.Trusted, &seen, &f.Subject); err != nil {
			return nil, err
		}
		if seen.Valid {
			f.SeenAt = time.Unix(seen.Int64, 0)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// TrustedFinanceDomain returns the trusted domain of the user's that
// domain is, or is a subdomain of; "" when none is.
func (s *Store) TrustedFinanceDomain(ctx context.Context, userID, domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for d := domain; d != ""; {
		var ok bool
		err := s.db.QueryRowContext(ctx, `SELECT trusted FROM finance_senders WHERE user_id = ? AND domain = ?`, userID, d).Scan(&ok)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if ok {
			return d, nil
		}
		_, rest, found := strings.Cut(d, ".")
		if !found || !strings.Contains(rest, ".") {
			break // not a bare top-level domain
		}
		d = rest
	}
	return "", nil
}

// StockName is the ISIN the user's reports and mails mean by key (a
// finance.Resolver key); "" when unknown.
func (s *Store) StockName(ctx context.Context, userID, key string) (string, error) {
	var isin string
	err := s.db.QueryRowContext(ctx, `SELECT isin FROM finance_stock_names WHERE user_id = ? AND key = ?`, userID, key).Scan(&isin)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return isin, err
}

// SaveStockName records that key means isin, for the user.
func (s *Store) SaveStockName(ctx context.Context, userID, key, isin string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finance_stock_names (user_id, key, isin, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET isin = excluded.isin, updated_at = excluded.updated_at`,
		userID, key, isin, time.Now().Unix())
	return err
}
