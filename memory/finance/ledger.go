package finance

import (
	"cmp"
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
	// Provisional is true when a trade it rests on is provisional.
	Provisional bool
}

// Lot is shares bought on one day and still held.
type Lot struct {
	Date     time.Time
	Quantity *big.Rat
	Cost     *big.Rat // price × quantity + fees
	CostBase *big.Rat
}

// Sale is a sell matched against the oldest lots.
type Sale struct {
	Date     time.Time
	Quantity *big.Rat
	Proceeds *big.Rat // price × quantity − fees
	Cost     *big.Rat
	Gain     *big.Rat
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

// Cost is what the shares held cost, fees included.
func (p *Position) Cost() *big.Rat {
	c := zero()
	for _, l := range p.Lots {
		c.Add(c, l.Cost)
	}
	return c
}

// CostBase is Cost in the base currency, nil when a rate was missing.
func (p *Position) CostBase() *big.Rat {
	c := zero()
	for _, l := range p.Lots {
		if l.CostBase == nil {
			return nil
		}
		c.Add(c, l.CostBase)
	}
	return c
}

// acquires and disposes are the sides that add shares and take them.
func acquires(side string) bool {
	return side == SideBuy || side == SideVest || side == SideESPP || side == SideTransferIn
}

func disposes(side string) bool { return side == SideSell || side == SideTransferOut }

// Ledger computes each account's positions from the trades up to and
// including until (void ones aside), applying splits and bonuses before
// the trades of their ex-date. Sells take the oldest lots first, per
// account; a transfer out takes them with their cost and realizes
// nothing, and a transfer in arrives at its price.
func Ledger(trades []Trade, actions []CorporateAction, rates *Rates, base string, until time.Time) []*Position {
	byKey := map[[2]string][]Trade{}
	for _, t := range trades {
		if t.State == Void || t.Date.After(until) {
			continue
		}
		k := [2]string{t.Account, t.ISIN}
		byKey[k] = append(byKey[k], t)
	}
	splits := map[string][]CorporateAction{}
	for _, a := range actions {
		if (a.Kind == ActionSplit || a.Kind == ActionBonus) && dec(a.Factor) != nil && !a.Date.After(until) {
			splits[a.ISIN] = append(splits[a.ISIN], a)
		}
	}
	keys := make([][2]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [2]string) int { return slices.Compare(a[:], b[:]) })

	var out []*Position
	for _, k := range keys {
		ts := byKey[k]
		// Within a day, what was bought is there to sell.
		slices.SortStableFunc(ts, func(a, b Trade) int {
			return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(sideOrder(a.Side), sideOrder(b.Side)))
		})
		p := &Position{Account: k[0], ISIN: k[1], Currency: ts[0].Currency}
		pending := slices.Clone(splits[k[1]])
		slices.SortFunc(pending, func(a, b CorporateAction) int { return a.Date.Compare(b.Date) })
		applyUpTo := func(day time.Time, inclusive bool) {
			for len(pending) > 0 && (pending[0].Date.Before(day) || inclusive && pending[0].Date.Equal(day)) {
				f := dec(pending[0].Factor)
				for i := range p.Lots {
					p.Lots[i].Quantity = mul(p.Lots[i].Quantity, f)
				}
				pending = pending[1:]
			}
		}
		for _, t := range ts {
			applyUpTo(t.Date, true)
			if t.State == Provisional {
				p.Provisional = true
			}
			qty, amount, fees := dec(t.Quantity), gross(TradeEvent{Amount: t.Amount, Quantity: t.Quantity, Price: t.AvgPrice}), dec(t.Fees)
			switch {
			case acquires(t.Side) && qty != nil && qty.Sign() > 0:
				cost := add(amount, fees)
				if cost == nil {
					cost = zero()
				}
				p.Lots = append(p.Lots, Lot{Date: t.Date, Quantity: qty, Cost: cost, CostBase: rates.Convert(cost, t.Currency, base, t.Date)})
			case disposes(t.Side) && qty != nil && qty.Sign() > 0:
				proceeds := sub(amount, fees)
				if proceeds == nil {
					proceeds = zero()
				}
				p.dispose(t, qty, proceeds, rates, base)
			case t.Side == SideDividend:
				p.Income = append(p.Income, Income{Date: t.Date, Amount: amount, AmountBase: rates.Convert(amount, t.Currency, base, t.Date),
					TaxWithheld: dec(t.TaxWithheld)})
			}
		}
		applyUpTo(until, true)
		out = append(out, p)
	}
	return out
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
// proceeds; for a transfer out, with nothing realized.
func (p *Position) dispose(t Trade, qty, proceeds *big.Rat, rates *Rates, base string) {
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
		cost.Add(cost, part)
		if partBase == nil || costBase == nil {
			costBase = nil
		} else {
			costBase.Add(costBase, partBase)
		}
		if take == l.Quantity {
			p.Lots = p.Lots[1:]
		} else {
			l.Quantity, l.Cost, l.CostBase = sub(l.Quantity, take), sub(l.Cost, part), sub(l.CostBase, partBase)
			if partBase == nil {
				l.CostBase = nil
			}
		}
		left = sub(left, take)
	}
	if left.Sign() > 0 {
		p.Oversold = add(p.Oversold, left)
	}
	if t.Side != SideSell {
		return
	}
	sold := sub(qty, left)
	if left.Sign() > 0 {
		// Only the shares the lots held count; the rest is oversold.
		proceeds = mul(proceeds, div(sold, qty))
	}
	s := Sale{Date: t.Date, Quantity: sold, Proceeds: proceeds, Cost: cost, Gain: sub(proceeds, cost), Acquired: acquired}
	if pb := rates.Convert(proceeds, t.Currency, base, t.Date); pb != nil && costBase != nil {
		s.GainBase = sub(pb, costBase)
	}
	p.Sales = append(p.Sales, s)
}
