package finance

import (
	"context"
	"fmt"
	"time"

	qdb "github.com/questdb/go-questdb-client/v4"
)

// TradeEvent is a trade, dividend or charge as one source reported it
// (02_ledger.sql). Source and Line are its key: a source written again
// replaces its rows, and identical rows are skipped.
type TradeEvent struct {
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

// AddTradeEvents writes the user's trade events, one INSERT each. They're
// readable once QuestDB has applied them (WaitApplied).
func (d *DB) AddTradeEvents(ctx context.Context, userID string, events []TradeEvent) error {
	if userID == "" {
		return errNoUser
	}
	for _, o := range events {
		if o.Date.IsZero() || o.Source == "" {
			return fmt.Errorf("finance: trade event %d of %q has no date or source", o.Line, o.Source)
		}
	}
	for _, o := range events {
		err := d.exec(ctx, `INSERT INTO trade_events (ts, user_id, source, line, account, isin, side,
				quantity, price, amount, fees, tax_withheld, currency, fx_rate, ref, doc, received_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7,
				$8::DECIMAL(24, 8), $9::DECIMAL(20, 6), $10::DECIMAL(24, 8), $11::DECIMAL(24, 8), $12::DECIMAL(24, 8),
				$13, $14::DECIMAL(20, 10), $15, $16, $17)`,
			func(b *qdb.QwpBinds) {
				timestamp(b, 0, o.Date)
				for i, v := range []string{userID, o.Source} {
					text(b, 1+i, v)
				}
				b.IntBind(3, int32(o.Line))
				for i, v := range []string{o.Account, o.ISIN, o.Side, o.Quantity, o.Price, o.Amount, o.Fees,
					o.TaxWithheld, o.Currency, o.FXRate, o.Ref, o.Doc} {
					text(b, 4+i, v)
				}
				timestamp(b, 16, o.ReceivedAt)
			})
		if err != nil {
			return fmt.Errorf("finance: trade event %d of %q: %w", o.Line, o.Source, err)
		}
	}
	return nil
}

// TradeEvents is the user's trade events dated from from up to, not
// including, until, by date, source and line.
func (d *DB) TradeEvents(ctx context.Context, userID string, from, until time.Time) ([]TradeEvent, error) {
	var out []TradeEvent
	err := d.query(ctx, `SELECT ts, source, line, account, isin, side,
			quantity::STRING, price::STRING, amount::STRING, fees::STRING, tax_withheld::STRING,
			currency, fx_rate::STRING, ref, doc, received_at
		FROM trade_events
		WHERE user_id = $1 AND ts >= $2 AND ts < $3
		ORDER BY ts, source, line`,
		func(b *qdb.QwpBinds) {
			text(b, 0, userID)
			timestamp(b, 1, from)
			timestamp(b, 2, until)
		},
		func(b *qdb.QwpColumnBatch, r int) error {
			s := func(col int) string { return b.String(col, r) }
			out = append(out, TradeEvent{
				Date: timeAt(b, 0, r), Source: s(1), Line: int(b.Int32(2, r)),
				Account: s(3), ISIN: s(4), Side: s(5),
				Quantity: trimDecimal(s(6)), Price: trimDecimal(s(7)), Amount: trimDecimal(s(8)),
				Fees: trimDecimal(s(9)), TaxWithheld: trimDecimal(s(10)),
				Currency: s(11), FXRate: trimDecimal(s(12)), Ref: s(13), Doc: s(14),
				ReceivedAt: timeAt(b, 15, r),
			})
			return nil
		})
	return out, err
}
