package finance

import (
	"cmp"
	"math/big"
	"slices"
	"strings"
	"time"
)

// Rates converts between currencies through the euro, at a day's ECB rate.
type Rates struct {
	byCurrency map[string][]dayRate // by day, ascending
}

type dayRate struct {
	day  time.Time
	rate *big.Rat // one euro in the currency
}

// rateWindow is how old the last rate before a day may be to stand in for
// it: weekends and holidays have none.
const rateWindow = 10 * 24 * time.Hour

// NewRates is rates from EURxxx pairs, in any order; other pairs are
// ignored.
func NewRates(rates []FX) *Rates {
	rs := &Rates{byCurrency: map[string][]dayRate{}}
	for _, r := range rates {
		if v := dec(r.Rate); v != nil && v.Sign() > 0 && strings.HasPrefix(r.Pair, "EUR") && len(r.Pair) == 6 {
			rs.byCurrency[r.Pair[3:]] = append(rs.byCurrency[r.Pair[3:]], dayRate{day(r.Date), v})
		}
	}
	for _, list := range rs.byCurrency {
		slices.SortStableFunc(list, func(a, b dayRate) int { return a.day.Compare(b.day) })
	}
	return rs
}

// euro is one euro in currency on day: the rate of that day, or the last
// one within rateWindow before it.
func (rs *Rates) euro(currency string, on time.Time) *big.Rat {
	if currency == "EUR" {
		return big.NewRat(1, 1)
	}
	if rs == nil {
		return nil
	}
	list := rs.byCurrency[currency]
	on = day(on)
	// The first rate after on; the one before it is the last on or before.
	i, _ := slices.BinarySearchFunc(list, on, func(r dayRate, t time.Time) int {
		return cmp.Or(r.day.Compare(t), -1)
	})
	if i == 0 || on.Sub(list[i-1].day) > rateWindow {
		return nil
	}
	return list[i-1].rate
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

// convertSome is Convert, but 0 is 0 without a rate: for figures that are
// usually none, such as tax withheld.
func (rs *Rates) convertSome(amount *big.Rat, from, to string, on time.Time) *big.Rat {
	if amount != nil && amount.Sign() == 0 {
		return zero()
	}
	return rs.Convert(amount, from, to, on)
}
