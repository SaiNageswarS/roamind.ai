package finance

import (
	"cmp"
	"context"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"time"
)

// Portfolio is a user's holdings across their accounts, one Stock per
// ISIN, with totals in the base currency.
type Portfolio struct {
	Base   string
	Stocks []Stock
	// Totals in the base currency; a nil one couldn't be computed (Missing).
	Cost, Value, Unrealized, Realized, Dividends *big.Rat
	// Missing says why figures are missing: no price, or no rate.
	Missing   []string
	positions []*Position
	stocks    map[string]Instrument
}

// Stock is one ISIN across the user's accounts.
type Stock struct {
	ISIN     string
	Name     string
	Market   string
	Quantity *big.Rat
	Accounts []AccountHolding
	// In the base currency.
	Cost, Value, Unrealized, Realized, Dividends *big.Rat
	Close                                        *big.Rat
	CloseCurrency                                string
	CloseDate                                    time.Time
	// Provisional is true when a figure rests on a trade no report has
	// confirmed yet.
	Provisional bool
	// Points are the stock's entries and exits, by date.
	Points []Point
}

// AccountHolding is what one account holds of a stock.
type AccountHolding struct {
	Account  string
	Quantity *big.Rat
	AvgCost  *big.Rat // per share, fees included, in Currency
	Currency string
}

// Point is an entry or exit: a day's trades of one account on one side.
type Point struct {
	Date     time.Time
	Side     string
	Account  string
	Quantity string
	Price    string
	Currency string
	State    string
}

// BuildPortfolio values the user's positions at the latest closes and
// today's rates.
func BuildPortfolio(trades []Trade, actions []CorporateAction, rates *Rates, base string,
	instruments map[string]Instrument, closes map[string]Price, now time.Time) Portfolio {
	pf := Portfolio{Base: base, Cost: zero(), Value: zero(), Unrealized: zero(), Realized: zero(), Dividends: zero(),
		stocks: instruments}
	pf.positions = Ledger(trades, actions, rates, base, now)
	byISIN := map[string]*Stock{}
	stock := func(isin string) *Stock {
		s, ok := byISIN[isin]
		if !ok {
			in := instruments[isin]
			s = &Stock{ISIN: isin, Name: in.Name, Market: cmp.Or(in.Market, MarketOf(isin)), Quantity: zero(),
				Cost: zero(), Realized: zero(), Dividends: zero()}
			byISIN[isin] = s
		}
		return s
	}
	missing := map[string]bool{}
	for _, p := range pf.positions {
		s := stock(p.ISIN)
		s.Provisional = s.Provisional || p.Provisional
		q := p.Quantity()
		if q.Sign() > 0 {
			s.Quantity.Add(s.Quantity, q)
			s.Accounts = append(s.Accounts, AccountHolding{Account: p.Account, Quantity: q, AvgCost: div(p.Cost(), q), Currency: p.Currency})
		}
		s.Cost = add2(s.Cost, p.CostBase(), &missing, "rate for "+p.Currency)
		for _, sale := range p.Sales {
			s.Realized = add2(s.Realized, sale.GainBase, &missing, "rate for "+p.Currency)
		}
		for _, in := range p.Income {
			s.Dividends = add2(s.Dividends, in.AmountBase, &missing, "rate for "+p.Currency)
		}
	}
	for _, t := range trades {
		if t.State == Void || t.Side == SideDividend {
			continue
		}
		s := stock(t.ISIN)
		s.Points = append(s.Points, Point{Date: t.Date, Side: t.Side, Account: t.Account, Quantity: t.Quantity,
			Price: t.AvgPrice, Currency: t.Currency, State: t.State})
	}
	for _, s := range byISIN {
		slices.SortFunc(s.Points, func(a, b Point) int { return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(a.Account, b.Account)) })
		if c, ok := closes[s.ISIN]; ok {
			s.Close, s.CloseCurrency, s.CloseDate = dec(c.Close), c.Currency, c.Date
		}
		if s.Quantity.Sign() > 0 {
			if s.Close == nil {
				missing["price for "+s.ISIN] = true
			} else if v := rates.Convert(mul(s.Quantity, s.Close), s.CloseCurrency, base, now); v == nil {
				missing["rate for "+s.CloseCurrency] = true
			} else {
				s.Value = v
				if s.Cost != nil {
					s.Unrealized = sub(v, s.Cost)
				}
			}
		}
		pf.Stocks = append(pf.Stocks, *s)
	}
	slices.SortFunc(pf.Stocks, func(a, b Stock) int {
		return cmp.Or(cmp.Compare(b.Quantity.Sign(), a.Quantity.Sign()), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ISIN, b.ISIN))
	})
	for _, s := range pf.Stocks {
		pf.Cost = add2(pf.Cost, s.Cost, nil, "")
		pf.Realized = add2(pf.Realized, s.Realized, nil, "")
		pf.Dividends = add2(pf.Dividends, s.Dividends, nil, "")
		if s.Quantity.Sign() > 0 {
			pf.Value = add2(pf.Value, s.Value, nil, "")
			pf.Unrealized = add2(pf.Unrealized, s.Unrealized, nil, "")
		}
	}
	for m := range missing {
		pf.Missing = append(pf.Missing, "no "+m)
	}
	slices.Sort(pf.Missing)
	return pf
}

// add2 is total + v, or nil once either is nil, noting why in missing.
func add2(total, v *big.Rat, missing *map[string]bool, why string) *big.Rat {
	if total == nil || v == nil {
		if v == nil && missing != nil && why != "" {
			(*missing)[why] = true
		}
		return nil
	}
	return new(big.Rat).Add(total, v)
}

// ProfitLine is the profit of one stock, market or year, in the base
// currency.
type ProfitLine struct {
	Key        string // the ISIN, the market or the year
	Name       string // the stock's, by stock
	Realized   *big.Rat
	Dividends  *big.Rat
	Unrealized *big.Rat // by stock and market; nil by year
}

// Profit breaks the portfolio's profit down by "stock", "market" or
// "year": realized gains and dividends by the year they were made in, and
// unrealized gains now.
func (pf Portfolio) Profit(by string) ([]ProfitLine, error) {
	lines := map[string]*ProfitLine{}
	line := func(key string) *ProfitLine {
		if l, ok := lines[key]; ok {
			return l
		}
		l := &ProfitLine{Key: key, Realized: zero(), Dividends: zero()}
		if by != "year" {
			l.Unrealized = zero()
		}
		lines[key] = l
		return l
	}
	switch by {
	case "stock", "market":
		for _, s := range pf.Stocks {
			key := s.ISIN
			if by == "market" {
				key = s.Market
			}
			l := line(key)
			if by == "stock" {
				l.Name = s.Name
			}
			l.Realized, l.Dividends = add2(l.Realized, s.Realized, nil, ""), add2(l.Dividends, s.Dividends, nil, "")
			if s.Quantity.Sign() > 0 {
				l.Unrealized = add2(l.Unrealized, s.Unrealized, nil, "")
			}
		}
	case "year":
		for _, p := range pf.positions {
			for _, s := range p.Sales {
				l := line(strconv.Itoa(s.Date.Year()))
				l.Realized = add2(l.Realized, s.GainBase, nil, "")
			}
			for _, in := range p.Income {
				l := line(strconv.Itoa(in.Date.Year()))
				l.Dividends = add2(l.Dividends, in.AmountBase, nil, "")
			}
		}
	default:
		return nil, fmt.Errorf("profit is by stock, market or year, not %q", by)
	}
	out := make([]ProfitLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b ProfitLine) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

// Mismatch is a reported holding the trades don't give.
type Mismatch struct {
	Account  string
	ISIN     string
	Day      time.Time
	Reported *big.Rat
	Ledger   *big.Rat
}

// CheckHoldings compares each account's latest reported holdings with
// what its trades give on the report's day. A stock the trades give but
// the report doesn't list counts as reported 0.
func CheckHoldings(trades []Trade, actions []CorporateAction, reported []Holding) []Mismatch {
	byAccount := map[string][]Holding{}
	for _, h := range reported {
		byAccount[h.Account] = append(byAccount[h.Account], h)
	}
	accounts := make([]string, 0, len(byAccount))
	for a := range byAccount {
		accounts = append(accounts, a)
	}
	slices.Sort(accounts)
	var out []Mismatch
	for _, a := range accounts {
		hs := byAccount[a]
		on := hs[0].Date
		var own []Trade
		for _, t := range trades {
			if t.Account == a {
				own = append(own, t)
			}
		}
		ledger := map[string]*big.Rat{}
		for _, p := range Ledger(own, actions, nil, "", on) {
			ledger[p.ISIN] = p.Quantity()
		}
		listed := map[string]*big.Rat{}
		for _, h := range hs {
			listed[h.ISIN] = add(listed[h.ISIN], dec(h.Quantity))
		}
		isins := make([]string, 0, len(listed)+len(ledger))
		for i := range listed {
			isins = append(isins, i)
		}
		for i := range ledger {
			if _, ok := listed[i]; !ok {
				isins = append(isins, i)
			}
		}
		slices.Sort(isins)
		for _, i := range isins {
			r, l := cmp.Or(listed[i], zero()), cmp.Or(ledger[i], zero())
			if r.Cmp(l) != 0 {
				out = append(out, Mismatch{Account: a, ISIN: i, Day: on, Reported: r, Ledger: l})
			}
		}
	}
	return out
}

// Portfolio reads the user's trades and the market data, and values them.
func (d *DB) Portfolio(ctx context.Context, userID, base string, now time.Time) (Portfolio, error) {
	trades, err := d.Trades(ctx, userID)
	if err != nil {
		return Portfolio{}, err
	}
	actions, err := d.Splits(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	rates, err := d.Rates(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	ins, err := d.Instruments(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	closes, err := d.LatestCloses(ctx)
	if err != nil {
		return Portfolio{}, err
	}
	return BuildPortfolio(trades, actions, rates, base, ins, closes, now), nil
}
