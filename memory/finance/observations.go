package finance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Observation is a trade, dividend or charge as one source reported it
// (02_ledger.sql). Source and Line are its key: a source written again
// replaces its rows, and identical rows are skipped.
type Observation struct {
	Date        time.Time // the trade date
	Source      string    // mail:<address>/<message id>, upload:<sha256>, chat:<thread>
	Line        int       // its row in that source
	Account     string
	ISIN        string
	Side        string // buy, sell, vest, espp, dividend, transfer_in, transfer_out
	Quantity    string
	Price       string
	Amount      string
	Fees        string
	TaxWithheld string
	Currency    string
	FXRate      string // the broker's, when given
	Ref         string // order or trade number
	Doc         string // contract_note, confirmation, tradebook, statement, chat
	ReceivedAt  time.Time
}

const observationColumns = `ts, source, line, account, isin, side,
	quantity::STRING, price::STRING, amount::STRING, fees::STRING, tax_withheld::STRING,
	currency, fx_rate::STRING, ref, doc, received_at`

// AddObservations writes the user's observations. They're readable once
// QuestDB has applied them (WaitApplied).
func (d *DB) AddObservations(ctx context.Context, userID string, obs []Observation) error {
	if userID == "" {
		return errors.New("finance: no user")
	}
	var b pgx.Batch
	for _, o := range obs {
		if o.Date.IsZero() || o.Source == "" {
			return fmt.Errorf("finance: observation %d of %q has no date or source", o.Line, o.Source)
		}
		b.Queue(`INSERT INTO observations (ts, user_id, source, line, account, isin, side,
				quantity, price, amount, fees, tax_withheld, currency, fx_rate, ref, doc, received_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
				$8::DECIMAL(24, 8), $9::DECIMAL(20, 6), $10::DECIMAL(24, 8), $11::DECIMAL(24, 8), $12::DECIMAL(24, 8),
				$13, $14::DECIMAL(20, 10), $15, $16, $17)`,
			o.Date.UTC(), userID, o.Source, o.Line, orNull(o.Account), orNull(o.ISIN), orNull(o.Side),
			orNull(o.Quantity), orNull(o.Price), orNull(o.Amount), orNull(o.Fees), orNull(o.TaxWithheld),
			orNull(o.Currency), orNull(o.FXRate), orNull(o.Ref), orNull(o.Doc), timeOrNull(o.ReceivedAt))
	}
	return d.pool.SendBatch(ctx, &b).Close()
}

// Observations is the user's observations dated from from up to, not
// including, until, by date, source and line.
func (d *DB) Observations(ctx context.Context, userID string, from, until time.Time) ([]Observation, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+observationColumns+` FROM observations
		WHERE user_id = $1 AND ts >= $2 AND ts < $3
		ORDER BY ts, source, line`, userID, from.UTC(), until.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Observation
	for rows.Next() {
		var o Observation
		var received *time.Time
		if err := rows.Scan(&o.Date, &o.Source, &o.Line, nullable(&o.Account), nullable(&o.ISIN), nullable(&o.Side),
			nullable(&o.Quantity), nullable(&o.Price), nullable(&o.Amount), nullable(&o.Fees), nullable(&o.TaxWithheld),
			nullable(&o.Currency), nullable(&o.FXRate), nullable(&o.Ref), nullable(&o.Doc), &received); err != nil {
			return nil, err
		}
		for _, p := range []*string{&o.Quantity, &o.Price, &o.Amount, &o.Fees, &o.TaxWithheld, &o.FXRate} {
			*p = trimDecimal(*p)
		}
		if received != nil {
			o.ReceivedAt = received.UTC()
		}
		o.Date = o.Date.UTC()
		out = append(out, o)
	}
	return out, rows.Err()
}
