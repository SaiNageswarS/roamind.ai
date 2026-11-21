package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SaiNageswarS/go-collection-boot/async"
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
		WHERE user_id = $1 AND broker = $2 AND number = $3`, userID, broker, number))
	if err == nil {
		return a, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return a, false, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fin_accounts WHERE user_id = $1`, userID).Scan(&n); err != nil {
		return a, false, err
	}
	a = FinAccount{UserID: userID, ID: fmt.Sprintf("a%d", n+1), Broker: broker, Number: number, Currency: strings.ToUpper(strings.TrimSpace(currency))}
	if _, err := tx.ExecContext(ctx, `INSERT INTO fin_accounts (`+finAccountCols+`, created_on) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		a.UserID, a.ID, a.Broker, a.Number, a.Currency, a.Name, time.Now().Unix()); err != nil {
		return a, false, err
	}
	return a, true, tx.Commit()
}

// FinAccounts returns the user's accounts, oldest first.
func (s *Store) FinAccounts(ctx context.Context, userID string) ([]FinAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+finAccountCols+` FROM fin_accounts WHERE user_id = $1 ORDER BY created_on, seq`, userID)
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
		WHERE user_id = $1 AND (id = $2 OR (name != '' AND lower(name) = lower($3)))`, userID, strings.ToLower(ref), ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// NameFinAccount sets what the user calls an account; false when they have
// no account id.
func (s *Store) NameFinAccount(ctx context.Context, userID, id, name string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE fin_accounts SET name = $1 WHERE user_id = $2 AND id = $3`,
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

func (r financeUploadRow) upload() FinanceUpload {
	return FinanceUpload{UserID: r.UserID, SHA256: r.SHA256, File: r.File, Account: r.Account, Doc: r.Doc,
		PeriodFrom: r.PeriodFrom, PeriodTo: r.PeriodTo, Rows: int(r.Rows), ImportedAt: time.Unix(r.ImportedAt, 0)}
}

// FinanceUploadOf returns the user's import of the file with this hash, or
// nil.
func (s *Store) FinanceUploadOf(ctx context.Context, userID, sha string) (*FinanceUpload, error) {
	r, err := find[financeUploadRow](ctx, s, rowID(userID, sha))
	if r == nil {
		return nil, err
	}
	u := r.upload()
	return &u, nil
}

// SaveFinanceUpload records an imported report.
func (s *Store) SaveFinanceUpload(ctx context.Context, u FinanceUpload) error {
	// An insert, not the ORM's Save: a file imported twice is refused.
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finance_uploads (id, user_id, sha256, file, account, doc, period_from, period_to, rows, imported_at, created_on, updated_on)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $10)`,
		rowID(u.UserID, u.SHA256), u.UserID, u.SHA256, u.File, u.Account, u.Doc, u.PeriodFrom, u.PeriodTo, u.Rows, now)
	return err
}

// FinanceUploads returns the user's imported reports, oldest first.
func (s *Store) FinanceUploads(ctx context.Context, userID string) ([]FinanceUpload, error) {
	rows, err := async.Await(table[financeUploadRow](s).Where("user_id = ?", userID).OrderBy("imported_at", "seq").Find(ctx))
	if err != nil {
		return nil, err
	}
	var us []FinanceUpload
	for _, r := range rows {
		us = append(us, r.upload())
	}
	return us, nil
}

// MarkFinanceDirty records that the user's trade events changed; it
// returns the mark, for ClearFinanceDirty.
func (s *Store) MarkFinanceDirty(ctx context.Context, userID string) (int64, error) {
	since := time.Now().UnixNano()
	return since, save(ctx, s, financeDirtyRow{UserID: userID, Since: since})
}

// FinanceDirty returns the user's mark, or 0 when their trades are up to
// date.
func (s *Store) FinanceDirty(ctx context.Context, userID string) (int64, error) {
	r, err := find[financeDirtyRow](ctx, s, userID)
	if r == nil {
		return 0, err
	}
	return r.Since, nil
}

// DirtyFinanceUsers returns the users whose trades are not up to date.
func (s *Store) DirtyFinanceUsers(ctx context.Context) ([]string, error) {
	rows, err := async.Await(table[financeDirtyRow](s).OrderBy("since").Find(ctx))
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	return ids, err
}

// ClearFinanceDirty clears the user's mark if it is still since: a change
// made during the reconcile keeps them dirty.
func (s *Store) ClearFinanceDirty(ctx context.Context, userID string, since int64) error {
	_, err := async.Await(table[financeDirtyRow](s).Where("user_id = ? AND since = ?", userID, since).Delete(ctx))
	return err
}

// DefaultBaseCurrency is the base currency of a user who set none.
const DefaultBaseCurrency = "EUR"

// BaseCurrency returns the currency the user's totals are in.
func (s *Store) BaseCurrency(ctx context.Context, userID string) (string, error) {
	r, err := find[financeSettingsRow](ctx, s, userID)
	if r == nil {
		return DefaultBaseCurrency, err
	}
	return r.BaseCurrency, nil
}

// SetBaseCurrency sets the currency the user's totals are in.
func (s *Store) SetBaseCurrency(ctx context.Context, userID, currency string) error {
	return save(ctx, s, financeSettingsRow{UserID: userID, BaseCurrency: strings.ToUpper(currency)})
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
		INSERT INTO finance_senders (user_id, domain, trusted, updated_on) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, domain) DO UPDATE SET trusted = excluded.trusted, updated_on = excluded.updated_on`,
		userID, strings.ToLower(domain), trusted, time.Now().Unix())
	return err
}

// SeenFinanceSender records a finance mail from domain, keeping whether it
// is trusted (untrusted when new).
func (s *Store) SeenFinanceSender(ctx context.Context, userID, domain, subject string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO finance_senders (user_id, domain, seen_at, subject, updated_on) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, domain) DO UPDATE SET seen_at = excluded.seen_at, subject = excluded.subject`,
		userID, strings.ToLower(domain), at.Unix(), subject, time.Now().Unix())
	return err
}

// FinanceSenders returns the user's senders, trusted ones first, then the
// most recently seen.
func (s *Store) FinanceSenders(ctx context.Context, userID string) ([]FinanceSender, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT domain, trusted, seen_at, subject FROM finance_senders
		WHERE user_id = $1 ORDER BY trusted DESC, seen_at DESC NULLS LAST, domain`, userID)
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
		err := s.db.QueryRowContext(ctx, `SELECT trusted FROM finance_senders WHERE user_id = $1 AND domain = $2`, userID, d).Scan(&ok)
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
	r, err := find[stockNameRow](ctx, s, rowID(userID, strings.ToLower(key)))
	if r == nil {
		return "", err
	}
	return r.ISIN, nil
}

// SaveStockName records that key means isin, for the user.
func (s *Store) SaveStockName(ctx context.Context, userID, key, isin string) error {
	return save(ctx, s, stockNameRow{UserID: userID, Key: key, ISIN: isin})
}
