package finance

import (
	"context"
	"time"

	qdb "github.com/questdb/go-questdb-client/v4"
)

// Trade is a reconciled day-group: one account's trades of one ISIN on one
// day and side (02_ledger.sql). Its figures are decimal text, in Currency.
type Trade struct {
	Date        time.Time
	Account     string
	ISIN        string
	Side        string
	Quantity    string
	AvgPrice    string
	Amount      string // quantity × price before fees
	Fees        string
	TaxWithheld string
	Currency    string
	Record      string // the source of the copy kept: the report, or the mail
}

var tradeCols = cols("ts", "user_id", "account", "isin", "side",
	"quantity:"+castMoney, "avg_price:"+castPrice, "amount:"+castMoney, "fees:"+castMoney, "tax_withheld:"+castMoney,
	"currency", "record")

// PutTrades writes the user's trades; each replaces the trade of its day,
// account, ISIN and side.
func (d *DB) PutTrades(ctx context.Context, userID string, trades []Trade) error {
	if userID == "" {
		return errNoUser
	}
	rows := make([][]any, len(trades))
	for i, t := range trades {
		rows[i] = []any{day(t.Date), userID, t.Account, t.ISIN, t.Side,
			t.Quantity, t.AvgPrice, t.Amount, t.Fees, t.TaxWithheld, t.Currency, t.Record}
	}
	return d.insert(ctx, "trades", tradeCols, rows)
}

// Trades returns the user's trades, by date, account, ISIN and side.
func (d *DB) Trades(ctx context.Context, userID string) ([]Trade, error) {
	var out []Trade
	err := d.query(ctx, `SELECT ts, account, isin, side, quantity::STRING, avg_price::STRING, amount::STRING,
			fees::STRING, tax_withheld::STRING, currency, record
		FROM trades WHERE user_id = $1 ORDER BY ts, account, isin, side`,
		func(b *qdb.QwpBinds) { text(b, 0, userID) },
		func(b *qdb.QwpColumnBatch, r int) error {
			s := func(col int) string { return b.String(col, r) }
			out = append(out, Trade{Date: timeAt(b, 0, r), Account: s(1), ISIN: s(2), Side: s(3),
				Quantity: trimDecimal(s(4)), AvgPrice: trimDecimal(s(5)), Amount: trimDecimal(s(6)),
				Fees: trimDecimal(s(7)), TaxWithheld: trimDecimal(s(8)), Currency: s(9), Record: s(10)})
			return nil
		})
	return out, err
}
