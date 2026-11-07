package finance

import (
	"cmp"
	"maps"
	"math/big"
	"slices"
	"time"
)

// Position is one account's holding of one ISIN, from its trades: the open
// lots, oldest first, and what was sold and received. Amounts are in the
// trades' currency, and in the base currency (…Base) at each day's ECB
// rate; a Base figure is nil when a rate was missing.
type Position struct {
	Account  string
	ISIN     string
	Currency string
	Lots     []Lot
	Sales    []Sale
	Income   []Income
	// Oversold is quantity sold beyond what the trades bought.
	Oversold *big.Rat
}

// Lot is shares bought on one day and still held.
type Lot struct {
	Date     time.Time
	Quantity *big.Rat
	Cost     *big.Rat // price × quantity + fees; nil when its price is unknown
	CostBase *big.Rat
}

// Sale is a sell matched against the oldest lots.
type Sale struct {
	Date     time.Time
	Quantity *big.Rat
	Proceeds *big.Rat  // price × quantity − fees; nil when its price is unknown
	Cost     *big.Rat  // nil when a lot's is
	Gain     *big.Rat  // nil when either is
	GainBase *big.Rat  // proceeds at the sale's rate, less cost at each lot's
	Acquired time.Time // the oldest lot sold
}

// Income is a dividend received.
type Income struct {
	Date        time.Time
	Amount      *big.Rat // gross
	AmountBase  *big.Rat
	TaxWithheld *big.Rat
}

// Quantity is the shares held.
func (p *Position) Quantity() *big.Rat {
	q := zero()
	for _, l := range p.Lots {
		q.Add(q, l.Quantity)
	}
	return q
}

// Cost is what the shares held cost, fees included; nil when a lot's cost
// is unknown.
func (p *Position) Cost() *big.Rat {
	c := zero()
	for _, l := range p.Lots {
		c = sum(c, l.Cost)
	}
	return c
}

// CostBase is Cost in the base currency, nil when it or a rate is missing.
func (p *Position) CostBase() *big.Rat {
	c := zero()
	for _, l := range p.Lots {
		c = sum(c, l.CostBase)
	}
	return c
}

// acquires and disposes are the sides that add shares and take them.
func acquires(side string) bool {
	return side == SideBuy || side == SideVest || side == SideESPP || side == SideTransferIn
}

func disposes(side string) bool { return side == SideSell || side == SideTransferOut }

// transferWindow is how far apart a transfer out of one account and its
// transfer in to another may be dated.
const transferWindow = 7 * 24 * time.Hour

// Ledger computes each account's positions from the trades up to and
// including until, applying splits and bonuses before the trades of their
// ex-date. Sells take the oldest lots first, per account. A transfer out
// takes them with their cost and realizes nothing; the transfer in to
// another of the user's accounts that matches it (the same quantity,
// within transferWindow) gets those lots, with their dates and costs, and
// a transfer in that matches none arrives at its price.
func Ledger(trades []Trade, actions []CorporateAction, rates *Rates, base string, until time.Time) []*Position {
	byISIN := map[string][]Trade{}
	for _, t := range trades {
		if !t.Date.After(until) {
			byISIN[t.ISIN] = append(byISIN[t.ISIN], t)
		}
	}
	splits := map[string][]CorporateAction{}
	for _, a := range actions {
		if f := dec(a.Factor); (a.Kind == ActionSplit || a.Kind == ActionBonus) && f != nil && f.Sign() > 0 && !a.Date.After(until) {
			splits[a.ISIN] = append(splits[a.ISIN], a)
		}
	}
	var out []*Position
	for _, isin := range slices.Sorted(maps.Keys(byISIN)) {
		out = append(out, ledgerOf(isin, byISIN[isin], splits[isin], rates, base, until)...)
	}
	slices.SortFunc(out, func(a, b *Position) int {
		return cmp.Or(cmp.Compare(a.Account, b.Account), cmp.Compare(a.ISIN, b.ISIN))
	})
	return out
}

// ledgerOf is Ledger for one ISIN's trades, across accounts.
func ledgerOf(isin string, ts []Trade, pending []CorporateAction, rates *Rates, base string, until time.Time) []*Position {
	// Within a day, what was bought is there to sell.
	slices.SortStableFunc(ts, func(a, b Trade) int {
		return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(sideOrder(a.Side), sideOrder(b.Side)), cmp.Compare(a.Account, b.Account))
	})
	pending = slices.Clone(pending)
	slices.SortFunc(pending, func(a, b CorporateAction) int { return a.Date.Compare(b.Date) })
	into := pairTransfers(ts) // a transfer out's index → its transfer in's
	paired := map[int]bool{}
	for _, in := range into {
		paired[in] = true
	}

	positions := map[string]*Position{}
	position := func(t Trade) *Position {
		p, ok := positions[t.Account]
		if !ok {
			p = &Position{Account: t.Account, ISIN: isin, Currency: t.Currency}
			positions[t.Account] = p
		}
		return p
	}
	applyUpTo := func(day time.Time) {
		for len(pending) > 0 && !pending[0].Date.After(day) {
			f := dec(pending[0].Factor)
			for _, p := range positions {
				for i := range p.Lots {
					p.Lots[i].Quantity = mul(p.Lots[i].Quantity, f)
				}
			}
			pending = pending[1:]
		}
	}
	for i, t := range ts {
		if paired[i] {
			continue // taken with its transfer out
		}
		applyUpTo(t.Date)
		p := position(t)
		qty, amount, fees := dec(t.Quantity), t.amount(), orZero(dec(t.Fees))
		switch {
		case acquires(t.Side) && qty != nil && qty.Sign() > 0:
			p.acquire(t, qty, sum(amount, fees), rates, base)
		case disposes(t.Side) && qty != nil && qty.Sign() > 0:
			moved := p.dispose(t, qty, diff(amount, fees), rates, base)
			if j, ok := into[i]; ok {
				in := ts[j]
				dst := position(in)
				dst.Lots = append(dst.Lots, moved...)
				slices.SortStableFunc(dst.Lots, func(a, b Lot) int { return a.Date.Compare(b.Date) })
				// Shares the out didn't have arrive at the in's price.
				if rest := sub(qty, lotsQuantity(moved)); rest.Sign() > 0 {
					dst.acquire(in, rest, mul(div(sum(in.amount(), orZero(dec(in.Fees))), qty), rest), rates, base)
				}
			}
		case t.Side == SideDividend:
			p.Income = append(p.Income, Income{Date: t.Date, Amount: amount, AmountBase: rates.Convert(amount, t.Currency, base, t.Date),
				TaxWithheld: dec(t.TaxWithheld)})
		}
	}
	applyUpTo(until)
	out := make([]*Position, 0, len(positions))
	for _, p := range positions {
		out = append(out, p)
	}
	return out
}

// pairTransfers matches each transfer out to a transfer in of another
// account with the same quantity, the nearest in date within
// transferWindow; ts are one ISIN's trades.
func pairTransfers(ts []Trade) map[int]int {
	into := map[int]int{}
	taken := map[int]bool{}
	for i, o := range ts {
		if o.Side != SideTransferOut {
			continue
		}
		best := -1
		var gap time.Duration
		for j, in := range ts {
			if in.Side != SideTransferIn || taken[j] || in.Account == o.Account || !eq(dec(in.Quantity), dec(o.Quantity)) {
				continue
			}
			g := in.Date.Sub(o.Date).Abs()
			if g <= transferWindow && (best < 0 || g < gap) {
				best, gap = j, g
			}
		}
		if best >= 0 {
			into[i], taken[best] = best, true
		}
	}
	return into
}

// acquire adds a lot of qty bought on t's day for cost.
func (p *Position) acquire(t Trade, qty, cost *big.Rat, rates *Rates, base string) {
	p.Lots = append(p.Lots, Lot{Date: t.Date, Quantity: qty, Cost: cost, CostBase: rates.Convert(cost, t.Currency, base, t.Date)})
}

func lotsQuantity(lots []Lot) *big.Rat {
	q := zero()
	for _, l := range lots {
		q = sum(q, l.Quantity)
	}
	return q
}

func sideOrder(side string) int {
	switch {
	case acquires(side):
		return 0
	case disposes(side):
		return 2
	}
	return 1
}

// dispose takes qty from the oldest lots: for a sell, as a Sale for
// proceeds; for a transfer out, with nothing realized. It returns the lots
// taken, in parts where it took part of one.
func (p *Position) dispose(t Trade, qty, proceeds *big.Rat, rates *Rates, base string) []Lot {
	var moved []Lot
	left := new(big.Rat).Set(qty)
	cost, costBase := zero(), zero()
	var acquired time.Time
	for len(p.Lots) > 0 && left.Sign() > 0 {
		l := &p.Lots[0]
		if acquired.IsZero() {
			acquired = l.Date
		}
		take := l.Quantity
		if take.Cmp(left) > 0 {
			take = left
		}
		share := div(take, l.Quantity)
		part, partBase := mul(l.Cost, share), mul(l.CostBase, share)
		cost, costBase = sum(cost, part), sum(costBase, partBase)
		moved = append(moved, Lot{Date: l.Date, Quantity: new(big.Rat).Set(take), Cost: part, CostBase: partBase})
		if take == l.Quantity {
			p.Lots = p.Lots[1:]
		} else {
			l.Quantity, l.Cost, l.CostBase = sub(l.Quantity, take), diff(l.Cost, part), diff(l.CostBase, partBase)
		}
		left = sub(left, take)
	}
	if left.Sign() > 0 {
		p.Oversold = add(p.Oversold, left)
	}
	if t.Side != SideSell {
		return moved
	}
	sold := sub(qty, left)
	if sold.Sign() == 0 {
		return moved
	}
	if left.Sign() > 0 {
		// Only the shares the lots held count; the rest is oversold.
		proceeds = mul(proceeds, div(sold, qty))
	}
	s := Sale{Date: t.Date, Quantity: sold, Proceeds: proceeds, Cost: cost, Gain: diff(proceeds, cost), Acquired: acquired}
	if pb := rates.Convert(proceeds, t.Currency, base, t.Date); pb != nil && costBase != nil {
		s.GainBase = sub(pb, costBase)
	}
	p.Sales = append(p.Sales, s)
	return moved
}
