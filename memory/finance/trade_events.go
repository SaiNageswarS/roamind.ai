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
	Price       string // of one unit
	Amount      string // quantity × price before fees, or a dividend's gross amount; always positive
	Fees        string // brokerage, taxes on the trade and other charges, positive
	TaxWithheld string // on a dividend
	Currency    string // of price, amount and fees
	FXRate      string // the broker's, when given
	Ref         string // order or trade number
	Doc         string // contract_note, confirmation, tradebook, statement, chat
	ReceivedAt  time.Time
}

// Sides of a trade event, and of a trade.
const (
	SideBuy         = "buy"
	SideSell        = "sell"
	SideVest        = "vest"
	SideESPP        = "espp"
	SideDividend    = "dividend"
	SideTransferIn  = "transfer_in"
	SideTransferOut = "transfer_out"
)

// Sides are every side, as the tools name them.
var Sides = []string{SideBuy, SideSell, SideVest, SideESPP, SideDividend, SideTransferIn, SideTransferOut}

// Kinds of document a trade event comes from.
const (
	DocTradebook    = "tradebook"
	DocContractNote = "contract_note"
	DocStatement    = "statement"
	DocConfirmation = "confirmation"
	DocChat         = "chat"
)

var tradeEventCols = cols("ts", "user_id", "source", "line", "account", "isin", "side",
	"quantity:"+castMoney, "price:"+castPrice, "amount:"+castMoney, "fees:"+castMoney, "tax_withheld:"+castMoney,
	"currency", "fx_rate:"+castRate, "ref", "doc", "received_at")

// AddTradeEvents writes the user's trade events. They're readable once
// QuestDB has applied them (WaitApplied).
func (d *DB) AddTradeEvents(ctx context.Context, userID string, events []TradeEvent) error {
	if userID == "" {
		return errNoUser
	}
	rows := make([][]any, len(events))
	for i, o := range events {
		if o.Date.IsZero() || o.Source == "" {
			return fmt.Errorf("finance: trade event %d of %q has no date or source", o.Line, o.Source)
		}
		rows[i] = []any{day(o.Date), userID, o.Source, o.Line, o.Account, o.ISIN, o.Side,
			o.Quantity, o.Price, o.Amount, o.Fees, o.TaxWithheld, o.Currency, o.FXRate, o.Ref, o.Doc, o.ReceivedAt}
	}
	return d.insert(ctx, "trade_events", tradeEventCols, rows)
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
