package finance

import (
	"context"
	"math/big"
	"strings"
	"time"

	qdb "github.com/questdb/go-questdb-client/v4"
)

// epoch is the fixed timestamp of reference rows (instruments, listings),
// which makes their other keys their key.
var epoch = time.Unix(0, 0).UTC()

// Instrument is a stock, ETF or fund, by ISIN (01_market.sql).
type Instrument struct {
	ISIN     string
	Name     string
	Kind     string // stock, etf, fund
	Market   string // US, EU, IN
	Currency string
	MainMIC  string // the listing whose close values it
}

// MarketOf is the market of an ISIN, from its country prefix: India, the
// US, or Europe for the rest.
func MarketOf(isin string) string {
	switch {
	case strings.HasPrefix(isin, "IN"):
		return "IN"
	case strings.HasPrefix(isin, "US"):
		return "US"
	}
	return "EU"
}

var instrumentCols = cols("ts", "isin", "name", "kind", "market", "currency", "main_mic")

// PutInstruments writes instruments; each replaces the one with its ISIN.
func (d *DB) PutInstruments(ctx context.Context, ins []Instrument) error {
	rows := make([][]any, len(ins))
	for i, in := range ins {
		rows[i] = []any{epoch, in.ISIN, in.Name, in.Kind, in.Market, in.Currency, in.MainMIC}
	}
	return d.insert(ctx, "instruments", instrumentCols, rows)
}

// Instruments returns every instrument, by ISIN.
func (d *DB) Instruments(ctx context.Context) (map[string]Instrument, error) {
	out := map[string]Instrument{}
	err := d.query(ctx, `SELECT isin, name, kind, market, currency, main_mic FROM instruments`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			in := Instrument{ISIN: b.String(0, r), Name: b.String(1, r), Kind: b.String(2, r),
				Market: b.String(3, r), Currency: b.String(4, r), MainMIC: b.String(5, r)}
			out[in.ISIN] = in
			return nil
		})
	return out, err
}

// Listing is where an instrument trades, and its code there.
type Listing struct {
	ISIN     string
	MIC      string // the exchange: EODHD's code (US, NSE, XETRA), or AMFI for Indian funds
	Symbol   string // EODHD's ticker on it, or the AMFI scheme code
	Currency string
}

var listingCols = cols("ts", "isin", "mic", "symbol", "currency")

// PutListings writes listings.
func (d *DB) PutListings(ctx context.Context, ls []Listing) error {
	rows := make([][]any, len(ls))
	for i, l := range ls {
		rows[i] = []any{epoch, l.ISIN, l.MIC, l.Symbol, l.Currency}
	}
	return d.insert(ctx, "listings", listingCols, rows)
}

// Listings returns every listing, by ISIN.
func (d *DB) Listings(ctx context.Context) (map[string][]Listing, error) {
	out := map[string][]Listing{}
	err := d.query(ctx, `SELECT isin, mic, symbol, currency FROM listings ORDER BY isin, mic`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			l := Listing{ISIN: b.String(0, r), MIC: b.String(1, r), Symbol: b.String(2, r), Currency: b.String(3, r)}
			out[l.ISIN] = append(out[l.ISIN], l)
			return nil
		})
	return out, err
}

// Price is one day's bar of a listing, unadjusted.
type Price struct {
	Date                   time.Time
	ISIN, MIC              string
	Open, High, Low, Close string
	Volume                 int
	Currency               string // filled in by LatestCloses, from the listing
}

var priceCols = cols("ts", "isin", "mic", "open:"+castPrice, "high:"+castPrice, "low:"+castPrice, "close:"+castPrice, "volume", "fetched_at")

// PutPrices writes bars; each replaces its day's bar of its listing.
func (d *DB) PutPrices(ctx context.Context, ps []Price) error {
	now := time.Now()
	rows := make([][]any, len(ps))
	for i, p := range ps {
		rows[i] = []any{day(p.Date), p.ISIN, p.MIC, p.Open, p.High, p.Low, p.Close, p.Volume, now}
	}
	return d.insert(ctx, "prices", priceCols, rows)
}

// LastPriceDays returns the day of each listing's latest bar, by ISIN and
// MIC.
func (d *DB) LastPriceDays(ctx context.Context) (map[[2]string]time.Time, error) {
	out := map[[2]string]time.Time{}
	err := d.query(ctx, `SELECT isin, mic, max(ts) FROM prices GROUP BY isin, mic`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			out[[2]string{b.String(0, r), b.String(1, r)}] = timeAt(b, 2, r)
			return nil
		})
	return out, err
}

// LatestCloses returns each instrument's latest close, from its main
// listing when it has one, else its latest close anywhere, by ISIN.
func (d *DB) LatestCloses(ctx context.Context) (map[string]Price, error) {
	ins, err := d.Instruments(ctx)
	if err != nil {
		return nil, err
	}
	ls, err := d.Listings(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]Price{}
	err = d.query(ctx, `SELECT ts, isin, mic, close::STRING FROM prices LATEST ON ts PARTITION BY isin, mic`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			p := Price{Date: timeAt(b, 0, r), ISIN: b.String(1, r), MIC: b.String(2, r), Close: trimDecimal(b.String(3, r))}
			for _, l := range ls[p.ISIN] {
				if l.MIC == p.MIC {
					p.Currency = l.Currency
				}
			}
			cur, ok := out[p.ISIN]
			main := ins[p.ISIN].MainMIC
			if !ok || p.MIC == main || cur.MIC != main && p.Date.After(cur.Date) {
				out[p.ISIN] = p
			}
			return nil
		})
	return out, err
}

// PriceOn returns the close of isin on day, or its last close before it,
// from any listing; ok is false when there is none within a week.
func (d *DB) PriceOn(ctx context.Context, isin string, on time.Time) (Price, bool, error) {
	var p Price
	found := false
	err := d.query(ctx, `SELECT ts, mic, close::STRING FROM prices WHERE isin = $1 AND ts <= $2 AND ts > $3 ORDER BY ts DESC LIMIT 1`,
		func(b *qdb.QwpBinds) {
			text(b, 0, isin)
			timestamp(b, 1, day(on))
			timestamp(b, 2, day(on).AddDate(0, 0, -7))
		},
		func(b *qdb.QwpColumnBatch, r int) error {
			p = Price{Date: timeAt(b, 0, r), ISIN: isin, MIC: b.String(1, r), Close: trimDecimal(b.String(2, r))}
			found = true
			return nil
		})
	return p, found, err
}

// FX is a day's exchange rate: one unit of Pair's first currency in its
// second, e.g. EURUSD 1.08.
type FX struct {
	Date   time.Time
	Pair   string
	Source string // ecb
	Rate   string
}

// SourceECB is the ECB's euro reference rates.
const SourceECB = "ecb"

var fxCols = cols("ts", "pair", "source", "rate:"+castRate)

// PutFX writes rates.
func (d *DB) PutFX(ctx context.Context, rates []FX) error {
	rows := make([][]any, len(rates))
	for i, r := range rates {
		rows[i] = []any{day(r.Date), r.Pair, r.Source, r.Rate}
	}
	return d.insert(ctx, "fx", fxCols, rows)
}

// LastFXDay returns the day of the latest rate from source, zero for none.
func (d *DB) LastFXDay(ctx context.Context, source string) (time.Time, error) {
	var last time.Time
	err := d.query(ctx, `SELECT max(ts) FROM fx WHERE source = $1`, func(b *qdb.QwpBinds) { text(b, 0, source) },
		func(b *qdb.QwpColumnBatch, r int) error {
			last = timeAt(b, 0, r)
			return nil
		})
	return last, err
}

// Rates returns the ECB's euro rates, for converting between currencies.
func (d *DB) Rates(ctx context.Context) (*Rates, error) {
	rs := &Rates{byCurrency: map[string][]dayRate{}}
	err := d.query(ctx, `SELECT ts, pair, rate::STRING FROM fx WHERE source = 'ecb' ORDER BY ts`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			pair := b.String(1, r)
			if !strings.HasPrefix(pair, "EUR") {
				return nil
			}
			if v := dec(b.String(2, r)); v != nil {
				rs.byCurrency[pair[3:]] = append(rs.byCurrency[pair[3:]], dayRate{timeAt(b, 0, r), v})
			}
			return nil
		})
	return rs, err
}

// Rates converts between currencies through the euro, at a day's ECB rate.
type Rates struct {
	byCurrency map[string][]dayRate // by date
}

type dayRate struct {
	day  time.Time
	rate *big.Rat // one euro in the currency
}

// NewRates is rates from EURxxx pairs, for tests and callers with their
// own rates.
func NewRates(rates []FX) *Rates {
	rs := &Rates{byCurrency: map[string][]dayRate{}}
	for _, r := range rates {
		if v := dec(r.Rate); v != nil && strings.HasPrefix(r.Pair, "EUR") {
			rs.byCurrency[r.Pair[3:]] = append(rs.byCurrency[r.Pair[3:]], dayRate{day(r.Date), v})
		}
	}
	return rs
}

// euro is one euro in currency on day: the rate of that day, or the last
// one within ten days before it (weekends, holidays).
func (rs *Rates) euro(currency string, on time.Time) *big.Rat {
	if currency == "EUR" {
		return big.NewRat(1, 1)
	}
	if rs == nil {
		return nil
	}
	list := rs.byCurrency[currency]
	on = day(on)
	for i := len(list) - 1; i >= 0; i-- {
		if !list[i].day.After(on) {
			if on.Sub(list[i].day) > 10*24*time.Hour {
				return nil
			}
			return list[i].rate
		}
	}
	return nil
}

// Convert is amount in from as to, at day's rates; nil when a rate is
// missing.
func (rs *Rates) Convert(amount *big.Rat, from, to string, on time.Time) *big.Rat {
	if amount == nil {
		return nil
	}
	if from == to {
		return new(big.Rat).Set(amount)
	}
	return mul(div(amount, rs.euro(from, on)), rs.euro(to, on))
}

// CorporateAction is a split, bonus or dividend of an instrument.
type CorporateAction struct {
	Date     time.Time // the ex-date
	ISIN     string
	Kind     string // split, bonus
	Factor   string // new shares per old
	Amount   string
	Currency string
	Source   string
}

// Kinds of corporate action the ledger applies.
const (
	ActionSplit = "split"
	ActionBonus = "bonus"
)

var actionCols = cols("ts", "isin", "kind", "factor:"+castRate, "amount:"+castPrice, "currency", "source")

// PutCorporateActions writes corporate actions.
func (d *DB) PutCorporateActions(ctx context.Context, as []CorporateAction) error {
	rows := make([][]any, len(as))
	for i, a := range as {
		rows[i] = []any{day(a.Date), a.ISIN, a.Kind, a.Factor, a.Amount, a.Currency, a.Source}
	}
	return d.insert(ctx, "corporate_actions", actionCols, rows)
}

// Splits returns every split and bonus, by date.
func (d *DB) Splits(ctx context.Context) ([]CorporateAction, error) {
	var out []CorporateAction
	err := d.query(ctx, `SELECT ts, isin, kind, factor::STRING, source FROM corporate_actions WHERE kind IN ('split', 'bonus') ORDER BY ts`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			out = append(out, CorporateAction{Date: timeAt(b, 0, r), ISIN: b.String(1, r), Kind: b.String(2, r),
				Factor: trimDecimal(b.String(3, r)), Source: b.String(4, r)})
			return nil
		})
	return out, err
}

// HeldISINs returns the ISINs any user has a trade of, with the day of
// their first trade: the instruments whose prices are fetched.
func (d *DB) HeldISINs(ctx context.Context) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	err := d.query(ctx, `SELECT isin, min(ts) FROM trades GROUP BY isin`, nil,
		func(b *qdb.QwpColumnBatch, r int) error {
			out[b.String(0, r)] = timeAt(b, 1, r)
			return nil
		})
	return out, err
}
