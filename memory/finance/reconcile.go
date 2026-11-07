package finance

import (
	"cmp"
	"math/big"
	"slices"
	"strings"
	"time"
)

// Reconcile settles the trade events into trades, one per day-group
// (account, ISIN, day, side), counting each fill once: the same fill
// reported by a report and by mail (or by two mails) is one fill, and the
// copy from the better document is kept (docRank). Nothing is ever
// dropped: an event no other source has is a fill of its own. The result
// depends only on its input, not on the events' order.
func Reconcile(events []TradeEvent) []Trade {
	groups := map[[4]string][]TradeEvent{}
	for _, e := range events {
		k := [4]string{day(e.Date).Format(time.DateOnly), e.Account, e.ISIN, e.Side}
		groups[k] = append(groups[k], e)
	}
	keys := make([][4]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b [4]string) int { return slices.Compare(a[:], b[:]) })

	trades := make([]Trade, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		t := Trade{Date: day(g[0].Date), Account: g[0].Account, ISIN: g[0].ISIN, Side: g[0].Side}
		trades = append(trades, total(t, mergeFills(g)))
	}
	return trades
}

// copyTolerance is how close two copies of one fill's price (or amount)
// are, as a fraction.
var copyTolerance = big.NewRat(1, 1000)

// Families of source, by the prefix of TradeEvent.Source.
const (
	familyReport = "upload"
	familyMail   = "mail"
	familyChat   = "chat"
)

func family(source string) string {
	f, _, _ := strings.Cut(source, ":")
	return f
}

// docRank orders the copies of a fill from the most to the least trusted,
// for the one that is kept: an uploaded report's, then mail's, each by
// document, then chat's, then any other source's.
func docRank(e TradeEvent) int {
	switch family(e.Source) {
	case familyReport:
		return docOrder(e.Doc)
	case familyMail:
		return 10 + docOrder(e.Doc)
	case familyChat:
		return 20
	}
	return 30
}

func docOrder(doc string) int {
	switch doc {
	case DocTradebook:
		return 0
	case DocContractNote:
		return 1
	case DocStatement:
		return 2
	case DocConfirmation:
		return 3
	}
	return 5
}

// fill is one execution, maybe reported by several sources.
type fill struct {
	TradeEvent
	sources map[string]bool
}

// mergeFills turns the events of one day-group into its fills: a row that
// is the same fill as one from another source is a copy of it, and the
// copy from the better document is kept (docRank), with what it lacks
// (fees, tax, the ref, the price or amount) taken from the other copies in
// that order. Rows of one source are never copies of each other: a
// report's two rows are two executions. Nor are two chat rows: each
// record_trade is its own source, and two are two trades.
func mergeFills(events []TradeEvent) []fill {
	events = slices.Clone(events)
	slices.SortFunc(events, func(a, b TradeEvent) int {
		return cmp.Or(cmp.Compare(docRank(a), docRank(b)), strings.Compare(a.Source, b.Source), cmp.Compare(a.Line, b.Line))
	})
	var fills []fill
	for _, e := range events {
		copied := false
		for i := range fills {
			f := &fills[i]
			if !f.sources[e.Source] && !(family(e.Source) == familyChat && f.chatOnly()) && sameFill(e, f.TradeEvent) {
				f.sources[e.Source] = true
				f.fillIn(e)
				copied = true
				break
			}
		}
		if !copied {
			fills = append(fills, fill{e, map[string]bool{e.Source: true}})
		}
	}
	return fills
}

// chatOnly reports whether every copy of f came from chat.
func (f *fill) chatOnly() bool {
	for s := range f.sources {
		if family(s) != familyChat {
			return false
		}
	}
	return true
}

// fillIn takes from a copy of f what f's own row lacks.
func (f *fill) fillIn(c TradeEvent) {
	for _, p := range []struct {
		to   *string
		from string
	}{
		{&f.Fees, c.Fees}, {&f.TaxWithheld, c.TaxWithheld}, {&f.Ref, c.Ref}, {&f.FXRate, c.FXRate},
		{&f.Price, c.Price}, {&f.Amount, c.Amount},
	} {
		if *p.to == "" {
			*p.to = p.from
		}
	}
}

// sameFill reports whether two rows from different sources are one
// execution: the same order or trade number when both have one; else the
// same quantity and a price within 0.1%; for rows without a quantity
// (dividends), an amount within 0.1%.
func sameFill(a, b TradeEvent) bool {
	if a.Ref != "" && b.Ref != "" {
		return a.Ref == b.Ref
	}
	qa, qb := dec(a.Quantity), dec(b.Quantity)
	if qa != nil && qb != nil {
		if !eq(qa, qb) {
			return false
		}
		pa, pb := dec(a.Price), dec(b.Price)
		if pa != nil && pb != nil {
			return near(pa, pb, copyTolerance)
		}
	}
	return near(gross(a), gross(b), copyTolerance)
}

// gross is a row's amount, else quantity × price.
func gross(e TradeEvent) *big.Rat {
	if a := dec(e.Amount); a != nil {
		return a
	}
	return mul(dec(e.Quantity), dec(e.Price))
}

// total is t with the fills' figures summed, the price averaged by
// quantity. The amount is unknown ("") when a fill's is: a trade told in
// chat without its price.
func total(t Trade, fills []fill) Trade {
	var qty, amount, fees, tax *big.Rat
	amount = zero()
	for _, f := range fills {
		qty = add(qty, dec(f.Quantity))
		amount = sum(amount, gross(f.TradeEvent))
		fees = add(fees, dec(f.Fees))
		tax = add(tax, dec(f.TaxWithheld))
	}
	t.Quantity, t.Amount, t.Fees, t.TaxWithheld = DecText(qty, 8), DecText(amount, 8), DecText(fees, 8), DecText(tax, 8)
	t.AvgPrice = DecText(div(amount, qty), 6)
	if len(fills) > 0 {
		t.Currency, t.Record = fills[0].Currency, fills[0].Source
	}
	return t
}
