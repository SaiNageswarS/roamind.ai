package store

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"roamind.ai/memory/finance"
)

// The finance ledger (12_finance.sql): fin_transactions holds each
// source's rows as it gave them, and fin_lot_matches how sells are matched
// to buys. Every write of a source settles the user's ledger in the same
// transaction (settle), so the tables always agree.

// ImportFinanceReport records an imported report and writes its rows, as
// the source upload:<sha256>, into its account. A file imported before is
// refused. It returns the sells of the account sold beyond what its
// transactions bought (Unmatched).
func (s *Store) ImportFinanceReport(ctx context.Context, u FinanceUpload, rows []finance.Row) ([]Unmatched, error) {
	return s.ledgerTx(ctx, u.UserID, u.Account, func(tx *sql.Tx) error {
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO finance_uploads (id, user_id, sha256, file, account, doc, period_from, period_to, rows, imported_at, created_on, updated_on)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10, $10) ON CONFLICT DO NOTHING`,
			rowID(u.UserID, u.SHA256), u.UserID, u.SHA256, u.File, u.Account, u.Doc, u.PeriodFrom, u.PeriodTo, len(rows), now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%s is already imported", u.File)
		}
		return writeSource(ctx, tx, u.UserID, u.Account, "upload:"+u.SHA256, u.Doc, rows)
	})
}

// SaveFinanceMail writes a broker mail's rows as source, replacing the
// rows it gave before, into account; doc is the kind of mail
// (finance.DocConfirmation, …). It returns what ImportFinanceReport does.
func (s *Store) SaveFinanceMail(ctx context.Context, userID, account, source, doc string, rows []finance.Row) ([]Unmatched, error) {
	return s.ledgerTx(ctx, userID, account, func(tx *sql.Tx) error {
		return writeSource(ctx, tx, userID, account, source, doc, rows)
	})
}

// Unmatched is shares a sell, or a transfer out, disposed of beyond what
// the account's transactions acquired: its buys are in no report imported.
type Unmatched struct {
	Date       string
	Instrument string
	Name       string
	Quantity   string
}

func (u Unmatched) String() string {
	what := u.Instrument
	if u.Name != "" && u.Name != u.Instrument {
		what += " (" + u.Name + ")"
	}
	return fmt.Sprintf("%s: %s of %s", u.Date, u.Quantity, what)
}

// ledgerTx runs write and settles the user's ledger in one transaction,
// one at a time per user, and returns the account's unmatched sells.
func (s *Store) ledgerTx(ctx context.Context, userID, account string, write func(*sql.Tx) error) ([]Unmatched, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('fin_ledger:' || $1))`, userID); err != nil {
		return nil, err
	}
	if err := write(tx); err != nil {
		return nil, err
	}
	if err := settle(ctx, tx, userID); err != nil {
		return nil, err
	}
	un, err := unmatched(ctx, tx, userID, account)
	if err != nil {
		return nil, err
	}
	return un, tx.Commit()
}

// writeSource replaces source's rows with rows, numbered from 1.
func writeSource(ctx context.Context, tx *sql.Tx, userID, account, source, doc string, rows []finance.Row) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM fin_transactions WHERE user_id = $1 AND source = $2`, userID, source); err != nil {
		return err
	}
	now := time.Now().Unix()
	for i, r := range rows {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO fin_transactions (user_id, account, source, line, doc, trade_date, instrument, isin, symbol, exchange, name,
				side, quantity, price, amount, fees, tax_withheld, currency, ref, created_on)
			VALUES ($1, $2, $3, $4, $5, $6::date, $7, $8, $9, $10, $11, $12,
				NULLIF($13, '')::numeric, NULLIF($14, '')::numeric, NULLIF($15, '')::numeric,
				COALESCE(NULLIF($16, '')::numeric, 0), COALESCE(NULLIF($17, '')::numeric, 0), $18, $19, $20)`,
			userID, account, source, i+1, doc, r.Date, r.Instrument(), strings.ToUpper(strings.TrimSpace(r.ISIN)),
			strings.ToUpper(strings.TrimSpace(r.Symbol)), strings.TrimSpace(r.Exchange), strings.Join(strings.Fields(r.Name), " "),
			r.Side, strings.TrimSpace(r.Quantity), strings.TrimSpace(r.Price), strings.TrimSpace(r.Amount),
			strings.TrimSpace(r.Fees), strings.TrimSpace(r.TaxWithheld), strings.ToUpper(strings.TrimSpace(r.Currency)),
			strings.TrimSpace(r.Ref), now)
		if err != nil {
			return fmt.Errorf("row %d: %w", i+1, err)
		}
	}
	return nil
}

// settle brings the user's ledger up to date with their transactions. A
// mail's row is not counted when a report has it too: a report of the
// same account covers its date and has rows of its side. Then the counted
// sells are matched to buys afresh (finance.MatchFIFO).
func settle(ctx context.Context, tx *sql.Tx, userID string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE fin_transactions t SET counted = NOT (t.source LIKE 'mail:%' AND EXISTS (
			SELECT 1 FROM finance_uploads u
			JOIN fin_transactions r ON r.user_id = u.user_id AND r.source = 'upload:' || u.sha256 AND r.side = t.side
			WHERE u.user_id = t.user_id AND u.account = t.account AND u.period_from <> '' AND u.period_to <> ''
				AND t.trade_date BETWEEN u.period_from::date AND u.period_to::date))
		WHERE t.user_id = $1`, userID)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, account, instrument, trade_date, side, quantity::text FROM fin_transactions
		WHERE user_id = $1 AND counted AND quantity IS NOT NULL
			AND side IN ('buy', 'vest', 'espp', 'transfer_in', 'sell', 'transfer_out')`, userID)
	if err != nil {
		return err
	}
	var txns []finance.Txn
	for rows.Next() {
		var t finance.Txn
		var q string
		if err := rows.Scan(&t.ID, &t.Account, &t.Instrument, &t.Date, &t.Side, &q); err != nil {
			rows.Close()
			return err
		}
		if t.Quantity, err = finance.ParseDec(q); err != nil {
			rows.Close()
			return err
		}
		txns = append(txns, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fin_lot_matches WHERE user_id = $1`, userID); err != nil {
		return err
	}
	for _, m := range finance.MatchFIFO(txns) {
		buy := sql.NullInt64{Int64: m.BuyID, Valid: m.BuyID != 0}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fin_lot_matches (user_id, sell_id, buy_id, quantity) VALUES ($1, $2, $3, $4::numeric)`,
			userID, m.SellID, buy, decText(m.Quantity)); err != nil {
			return err
		}
	}
	return nil
}

// unmatched is the account's sells beyond what it bought.
func unmatched(ctx context.Context, tx *sql.Tx, userID, account string) ([]Unmatched, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT s.trade_date, s.instrument, s.name, m.quantity::text
		FROM fin_lot_matches m JOIN fin_transactions s ON s.id = m.sell_id
		WHERE m.user_id = $1 AND s.account = $2 AND m.buy_id IS NULL
		ORDER BY s.trade_date, s.id`, userID, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Unmatched
	for rows.Next() {
		var u Unmatched
		var d time.Time
		if err := rows.Scan(&d, &u.Instrument, &u.Name, &u.Quantity); err != nil {
			return nil, err
		}
		u.Date = d.Format(time.DateOnly)
		u.Quantity = decText(mustDec(u.Quantity))
		out = append(out, u)
	}
	return out, rows.Err()
}

// decText is a quantity as exact decimal text. Quantities are sums and
// differences of decimals, so 20 places hold them exactly.
func decText(r *big.Rat) string { return finance.DecText(r, 20) }

func mustDec(s string) *big.Rat {
	r, _ := finance.ParseDec(s)
	return r
}
