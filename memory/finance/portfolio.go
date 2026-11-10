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
	// Realized gains are after fees; Tax is what was withheld on sales and
	// dividends, and Net is realized gains and dividends less it.
	Cost, Value, Unrealized, Realized, Dividends, Tax, Net *big.Rat
	// Missing says why figures are missing: no close, no rate, no price for
	// a trade, or no buy for shares sold.
	Missing   []string
	positions []*Position
	closes    map[string]Price
	rates     *Rates
	now       time.Time
}

// Stock is one ISIN across the user's accounts.
type Stock struct {
	ISIN     string
	Name     string
	Market   string
	Quantity *big.Rat
	Accounts []AccountHolding
	// In the base currency.
	Cost, Value, Unrealized, Realized, Dividends, Tax, Net *big.Rat
	Close                                        *big.Rat
	CloseCurrency                                string
	CloseDate                                    time.Time
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
}

// BuildPortfolio values the user's positions at the latest closes and
// today's rates.
func BuildPortfolio(trades []Trade, actions []CorporateAction, rates *Rates, base string,
	instruments map[string]Instrument, closes map[string]Price, now time.Time) Portfolio {
	pf := Portfolio{Base: base, Cost: zero(), Value: zero(), Unrealized: zero(), Realized: zero(), Dividends: zero(), Tax: zero(),
		closes: closes, rates: rates, now: now}
	pf.positions = Ledger(trades, actions, rates, base, now)
	byISIN := map[string]*Stock{}
	stock := func(isin string) *Stock {
		s, ok := byISIN[isin]
		if !ok {
			in := instruments[isin]
			s = &Stock{ISIN: isin, Name: in.Name, Market: cmp.Or(in.Market, MarketOf(isin)), Quantity: zero(),
				Cost: zero(), Realized: zero(), Dividends: zero(), Tax: zero()}
			byISIN[isin] = s
		}
		return s
	}
	missing := map[string]bool{}
	for _, p := range pf.positions {
		s := stock(p.ISIN)
		q := p.Quantity()
		if q.Sign() > 0 {
			s.Quantity.Add(s.Quantity, q)
			s.Accounts = append(s.Accounts, AccountHolding{Account: p.Account, Quantity: q, AvgCost: div(p.Cost(), q), Currency: p.Currency})
		}
		// Why a figure in the base currency is missing: its own figure is,
		// or else the rate.
		why := func(own *big.Rat) string {
			if own == nil {
				return "price for a trade of " + p.ISIN + " in " + p.Account
			}
			return "rate for " + p.Currency
		}
		s.Cost = add2(s.Cost, p.CostBase(), &missing, why(p.Cost()))
		for _, sale := range p.Sales {
			s.Realized = add2(s.Realized, sale.GainBase, &missing, why(sale.Gain))
			s.Tax = add2(s.Tax, sale.TaxBase, &missing, why(sale.Tax))
		}
		for _, in := range p.Income {
			s.Dividends = add2(s.Dividends, in.AmountBase, &missing, why(in.Amount))
			s.Tax = add2(s.Tax, in.TaxBase, &missing, why(in.Amount))
		}
		if p.Oversold != nil {
			// A buy is missing: realized gains leave out these shares.
			missing["buy for "+DecText(p.Oversold, 8)+" "+p.ISIN+" sold in "+p.Account] = true
		}
	}
	for _, t := range trades {
		if t.Side == SideDividend || t.Date.After(now) {
			continue
		}
		s := stock(t.ISIN)
		s.Points = append(s.Points, Point{Date: t.Date, Side: t.Side, Account: t.Account, Quantity: t.Quantity,
			Price: t.AvgPrice, Currency: t.Currency})
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
		s.Net = net(s.Realized, s.Dividends, s.Tax)
		pf.Stocks = append(pf.Stocks, *s)
	}
	slices.SortFunc(pf.Stocks, func(a, b Stock) int {
		return cmp.Or(cmp.Compare(b.Quantity.Sign(), a.Quantity.Sign()), cmp.Compare(a.Name, b.Name), cmp.Compare(a.ISIN, b.ISIN))
	})
	for _, s := range pf.Stocks {
		pf.Cost = add2(pf.Cost, s.Cost, nil, "")
		pf.Realized = add2(pf.Realized, s.Realized, nil, "")
		pf.Dividends = add2(pf.Dividends, s.Dividends, nil, "")
		pf.Tax = add2(pf.Tax, s.Tax, nil, "")
		if s.Quantity.Sign() > 0 {
			pf.Value = add2(pf.Value, s.Value, nil, "")
			pf.Unrealized = add2(pf.Unrealized, s.Unrealized, nil, "")
		}
	}
	pf.Net = net(pf.Realized, pf.Dividends, pf.Tax)
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

// net is realized gains and dividends less the tax withheld on them: what
// the user kept. Nil when any is.
func net(realized, dividends, tax *big.Rat) *big.Rat {
	return diff(sum(realized, dividends), tax)
}

// ProfitLine is the profit of one stock, market, account, year or sale,
// in the base currency.
type ProfitLine struct {
	Key        string // the ISIN, the market, the account, the year, or the sale's date
	Name       string // the stock's, by stock and sale
	Realized   *big.Rat
	Dividends  *big.Rat
	Tax        *big.Rat // withheld on the sales and dividends
	Net        *big.Rat // realized gains and dividends less tax
	Unrealized *big.Rat // by stock, market and account; nil by year and sale
	// By sale: what was sold, in the trade's currency, fees included.
	ISIN, Account, Currency string
	Quantity, Proceeds, Cost *big.Rat
}

// ProfitBy are the ways Profit breaks profit down.
var ProfitBy = []string{"stock", "market", "account", "year", "sale"}

// Profit breaks the portfolio's profit down by "stock", "market",
// "account", "year" (realized gains, dividends and tax by the year they
// were made in) or "sale" (each sale, oldest first), with unrealized gains
// now except by year and sale. A non-empty account keeps only its trades.
func (pf Portfolio) Profit(by, account string) ([]ProfitLine, error) {
	if !slices.Contains(ProfitBy, by) {
		return nil, fmt.Errorf("profit is by stock, market, account, year or sale, not %q", by)
	}
	stocks := map[string]Stock{}
	for _, s := range pf.Stocks {
		stocks[s.ISIN] = s
	}
	lines := map[string]*ProfitLine{}
	line := func(p *Position, day time.Time) *ProfitLine {
		key := p.ISIN
		switch by {
		case "market":
			key = stocks[p.ISIN].Market
		case "account":
			key = p.Account
		case "year":
			key = strconv.Itoa(day.Year())
		}
		if l, ok := lines[key]; ok {
			return l
		}
		l := &ProfitLine{Key: key, Realized: zero(), Dividends: zero(), Tax: zero()}
		if by == "stock" {
			l.Name = stocks[p.ISIN].Name
		}
		if by != "year" {
			l.Unrealized = zero()
		}
		lines[key] = l
		return l
	}
	var out []ProfitLine
	for _, p := range pf.positions {
		if account != "" && p.Account != account {
			continue
		}
		for _, s := range p.Sales {
			if by == "sale" {
				out = append(out, ProfitLine{Key: s.Date.Format(time.DateOnly), Name: stocks[p.ISIN].Name, ISIN: p.ISIN,
					Account: p.Account, Currency: p.Currency, Quantity: s.Quantity, Proceeds: s.Proceeds, Cost: s.Cost,
					Realized: s.GainBase, Dividends: zero(), Tax: s.TaxBase, Net: net(s.GainBase, zero(), s.TaxBase)})
				continue
			}
			l := line(p, s.Date)
			l.Realized, l.Tax = add2(l.Realized, s.GainBase, nil, ""), add2(l.Tax, s.TaxBase, nil, "")
		}
		if by == "sale" {
			continue
		}
		for _, in := range p.Income {
			l := line(p, in.Date)
			l.Dividends, l.Tax = add2(l.Dividends, in.AmountBase, nil, ""), add2(l.Tax, in.TaxBase, nil, "")
		}
		if by != "year" {
			l := line(p, time.Time{})
			l.Unrealized = add2(l.Unrealized, pf.unrealized(p), nil, "")
		}
	}
	if by == "sale" {
		slices.SortStableFunc(out, func(a, b ProfitLine) int {
			return cmp.Or(cmp.Compare(a.Key, b.Key), cmp.Compare(a.Name, b.Name), cmp.Compare(a.Account, b.Account))
		})
		return out, nil
	}
	for _, l := range lines {
		l.Net = net(l.Realized, l.Dividends, l.Tax)
		out = append(out, *l)
	}
	slices.SortFunc(out, func(a, b ProfitLine) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}

// unrealized is p's gain on what it holds, at the latest close; nil when
// the close or a rate is missing.
func (pf Portfolio) unrealized(p *Position) *big.Rat {
	q := p.Quantity()
	if q.Sign() == 0 {
		return zero()
	}
	c, ok := pf.closes[p.ISIN]
	if !ok {
		return nil
	}
	return diff(pf.rates.Convert(mul(q, dec(c.Close)), c.Currency, pf.Base, pf.now), p.CostBase())
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
