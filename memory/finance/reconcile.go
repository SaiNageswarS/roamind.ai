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

// fill is one execution, maybe reported by several sources; or, when
// copies list it in parts, the parts one source listed.
type fill struct {
	TradeEvent
	sources map[string]bool
	refs    map[string]bool // every copy's order and trade numbers
}

func newFill(e TradeEvent) fill {
	f := fill{TradeEvent: e, sources: map[string]bool{e.Source: true}, refs: map[string]bool{}}
	for _, r := range refsOf(e) {
		f.refs[r] = true
	}
	return f
}

// refsOf is a row's order and trade numbers.
func refsOf(e TradeEvent) []string { return strings.Fields(e.Ref) }

// mergeFills turns the events of one day-group into its fills, counting
// each execution once however many sources report it. Sources are taken
// from the most trusted (docRank), each one's rows matched against the
// fills so far (matchSource), so the copy kept is the most trusted one,
// with what it lacks (fees, tax, the ref, the price or amount) taken from
// the other copies in that order. Rows of one source are never copies of
// each other: a report's two rows are two executions. Nor are two chat
// rows: each record_trade is its own source, and two are two trades.
func mergeFills(events []TradeEvent) []fill {
	bySource := map[string][]TradeEvent{}
	rank := map[string]int{}
	for _, e := range events {
		if r, ok := rank[e.Source]; !ok || docRank(e) < r {
			rank[e.Source] = docRank(e)
		}
		bySource[e.Source] = append(bySource[e.Source], e)
	}
	sources := make([]string, 0, len(bySource))
	for s := range bySource {
		sources = append(sources, s)
	}
	slices.SortFunc(sources, func(a, b string) int { return cmp.Or(cmp.Compare(rank[a], rank[b]), strings.Compare(a, b)) })
	var fills []fill
	for _, src := range sources {
		rows := bySource[src]
		slices.SortStableFunc(rows, func(a, b TradeEvent) int { return cmp.Compare(a.Line, b.Line) })
		fills = matchSource(fills, src, rows)
	}
	return fills
}

// matchSource adds one source's rows to fills, as copies where they are:
//  1. rows and fills that share an order or trade number are copies of
//     each other, one to one or as parts (an order filled in two
//     executions, and its one confirmation);
//  2. a row and a fill with the same quantity and a price within 0.1%
//     are copies, the nearest prices paired first;
//  3. what is left on both sides, when it adds up to the same quantity
//     and amount, is one set of executions listed in parts;
//
// and the rest are new fills. Rows with order or trade numbers none of a
// fill's are never copies of it.
func matchSource(fills []fill, src string, rows []TradeEvent) []fill {
	chat := family(src) == familyChat
	open := make([]bool, len(fills)) // fills this source may still copy
	for i := range fills {
		open[i] = !fills[i].sources[src] && !(chat && fills[i].chatOnly())
	}
	left := make([]bool, len(rows))
	for i := range rows {
		left[i] = true
	}

	// 1. By order and trade numbers.
	shared := make([][]int, len(rows)) // the fills each row shares one with
	byFill := map[int][]TradeEvent{}   // the rows that share one with each fill
	for r, e := range rows {
		for i := range fills {
			if open[i] && sharesRef(fills[i], e) {
				shared[r] = append(shared[r], i)
				byFill[i] = append(byFill[i], e)
			}
		}
	}
	for r, e := range rows {
		for _, i := range shared[r] {
			left[r] = false
			fills[i].copyOf(e, len(shared[r]) == 1 && len(byFill[i]) == 1)
		}
	}
	for i, rs := range byFill {
		open[i] = false
		if len(rs) > 1 {
			addFees(fills, []int{i}, rs)
		}
	}

	// 2. One to one, by quantity and the nearest price.
	type pair struct {
		r, i int
		dist *big.Rat
	}
	var pairs []pair
	for r, e := range rows {
		if !left[r] {
			continue
		}
		for i := range fills {
			if open[i] && !refsDiffer(fills[i], e) && sameFill(e, fills[i].TradeEvent) {
				pairs = append(pairs, pair{r, i, distance(e, fills[i].TradeEvent)})
			}
		}
	}
	slices.SortStableFunc(pairs, func(a, b pair) int { return cmp.Or(a.dist.Cmp(b.dist), cmp.Compare(a.i, b.i), cmp.Compare(a.r, b.r)) })
	for _, p := range pairs {
		if left[p.r] && open[p.i] {
			left[p.r], open[p.i] = false, false
			fills[p.i].copyOf(rows[p.r], true)
		}
	}

	// 3. What's left, as one set listed in parts.
	var restRows []TradeEvent
	var restFills []int
	for r, e := range rows {
		if left[r] {
			restRows = append(restRows, e)
		}
	}
	for i := range fills {
		if open[i] {
			restFills = append(restFills, i)
		}
	}
	if len(restRows) > 0 && len(restFills) > 0 && len(restRows)+len(restFills) > 2 && sameTotal(restRows, fills, restFills) {
		for _, e := range restRows {
			for _, i := range restFills {
				fills[i].copyOf(e, false)
			}
		}
		addFees(fills, restFills, restRows)
		return fills
	}
	for r, e := range rows {
		if left[r] {
			fills = append(fills, newFill(e))
		}
	}
	return fills
}

// copyOf records c as a copy of f; one is true when c is a copy of f
// alone, not of it with others, so f can take what it lacks from it.
func (f *fill) copyOf(c TradeEvent, one bool) {
	f.sources[c.Source] = true
	for _, r := range refsOf(c) {
		f.refs[r] = true
	}
	if one {
		f.fillIn(c)
	}
}

// sharesRef reports whether e has an order or trade number of f's.
func sharesRef(f fill, e TradeEvent) bool {
	for _, r := range refsOf(e) {
		if f.refs[r] {
			return true
		}
	}
	return false
}

// refsDiffer reports whether f and e both have order or trade numbers and
// share none: two orders.
func refsDiffer(f fill, e TradeEvent) bool {
	return len(f.refs) > 0 && len(refsOf(e)) > 0 && !sharesRef(f, e)
}

// distance is how far apart two copies' prices (else amounts) are, as a
// fraction.
func distance(a, b TradeEvent) *big.Rat {
	x, y := dec(a.Price), dec(b.Price)
	if x == nil || y == nil {
		x, y = gross(a), gross(b)
	}
	if d := div(new(big.Rat).Abs(orZero(diff(x, y))), y); d != nil {
		return d
	}
	return zero()
}

// sameTotal reports whether rows and the fills at is add up to the same
// quantity, and an amount within 0.1%.
func sameTotal(rows []TradeEvent, fills []fill, is []int) bool {
	var qr, qf, ar, af *big.Rat = zero(), zero(), zero(), zero()
	for _, e := range rows {
		qr, ar = sum(qr, dec(e.Quantity)), sum(ar, gross(e))
	}
	for _, i := range is {
		qf, af = sum(qf, dec(fills[i].Quantity)), sum(af, gross(fills[i].TradeEvent))
	}
	return eq(qr, qf) && qr.Sign() > 0 && near(ar, af, copyTolerance)
}

// addFees gives the fills at is the rows' fees and tax when none of them
// has any: a contract note's charges for executions a tradebook lists
// without them. They go on the first fill, as one charge of the set.
func addFees(fills []fill, is []int, rows []TradeEvent) {
	var fees, tax *big.Rat
	for _, e := range rows {
		fees, tax = add(fees, dec(e.Fees)), add(tax, dec(e.TaxWithheld))
	}
	for _, i := range is {
		if fills[i].Fees != "" || fills[i].TaxWithheld != "" {
			return
		}
	}
	fills[is[0]].Fees, fills[is[0]].TaxWithheld = DecText(fees, 8), DecText(tax, 8)
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
		{&f.Fees, c.Fees}, {&f.TaxWithheld, c.TaxWithheld}, {&f.FXRate, c.FXRate},
		{&f.Price, c.Price}, {&f.Amount, c.Amount},
	} {
		if *p.to == "" {
			*p.to = p.from
		}
	}
}

// sameFill reports whether two rows from different sources, which share
// no order or trade number, can be one execution: the same quantity and a
// price within 0.1%; for rows without a quantity (dividends), an amount
// within 0.1%.
func sameFill(a, b TradeEvent) bool {
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
