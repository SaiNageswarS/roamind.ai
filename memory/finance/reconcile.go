package finance

import (
	"cmp"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// Coverage is the days each account's uploaded reports cover, by account.
type Coverage map[string][]Period

// Period is a run of days, both ends included.
type Period struct{ From, To time.Time }

// Covers reports whether a report of account covers day.
func (c Coverage) Covers(account string, day time.Time) bool {
	for _, p := range c[account] {
		if !day.Before(p.From) && !day.After(p.To) {
			return true
		}
	}
	return false
}

// Notice is something a report changed in the ledger, for the user to hear
// about once: it needs nothing from them.
type Notice struct {
	Date    time.Time
	Account string
	ISIN    string
	Side    string
	Kind    string // corrected or dropped
	Detail  string
}

// Kinds of notice.
const (
	// NoticeCorrected is a report holding a trade that mail or chat
	// reported otherwise.
	NoticeCorrected = "corrected"
	// NoticeDropped is a mail or chat trade a report covering its day
	// doesn't have: a cancelled order, or a misread mail.
	NoticeDropped = "dropped"
)

// Reconcile settles the trade events into trades, one per day-group
// (account, ISIN, day, side). Uploaded reports are the golden set:
//   - a day-group with report rows is those rows, confirmed;
//   - one a report covers but has no rows for is void, and mail or chat
//     rows for it are dropped;
//   - otherwise its mail and chat rows are the trade, provisional.
//
// Copies of one fill, as two sources reported it, count once (sameFill).
// Every trade in previous that no day-group has any more comes back void.
// The result depends only on its input, not on the events' order.
func Reconcile(events []TradeEvent, cover Coverage, previous []Trade) ([]Trade, []Notice) {
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

	var trades []Trade
	var notices []Notice
	seen := map[[4]string]bool{}
	for _, k := range keys {
		g := groups[k]
		var reported, other []TradeEvent
		for _, e := range g {
			if family(e.Source) == familyReport {
				reported = append(reported, e)
			} else {
				other = append(other, e)
			}
		}
		first := g[0]
		t := Trade{Date: day(first.Date), Account: first.Account, ISIN: first.ISIN, Side: first.Side}
		notice := Notice{Date: t.Date, Account: t.Account, ISIN: t.ISIN, Side: t.Side}
		switch {
		case len(reported) > 0:
			t = total(t, mergeFills(reported), Confirmed)
			if len(other) > 0 {
				said := total(t, mergeFills(other), Provisional)
				if !agree(said, t) {
					notice.Kind = NoticeCorrected
					notice.Detail = fmt.Sprintf("%s said %s; the report says %s", sourceName(said.Record), describe(said), describe(t))
					notices = append(notices, notice)
				}
			}
		case cover.Covers(t.Account, t.Date):
			said := total(t, mergeFills(other), Provisional)
			t.State = Void
			notice.Kind = NoticeDropped
			notice.Detail = fmt.Sprintf("%s said %s; the report covering %s doesn't have it", sourceName(said.Record), describe(said), t.Date.Format(time.DateOnly))
			notices = append(notices, notice)
		default:
			t = total(t, mergeFills(other), Provisional)
		}
		seen[k] = true
		trades = append(trades, t)
	}
	for _, p := range previous {
		if p.State != Void && !seen[p.key()] {
			p.State = Void
			trades = append(trades, p)
		}
	}
	return trades, notices
}

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

func sourceName(source string) string {
	switch family(source) {
	case familyMail:
		return "mail"
	case familyChat:
		return "chat"
	}
	return "a report"
}

// docRank orders documents from the most to the least trusted, for the
// copy of a fill that is kept.
func docRank(e TradeEvent) int {
	switch family(e.Source) {
	case familyChat:
		return 10
	}
	switch e.Doc {
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
// copy from the better document is kept (docRank). Rows of one source are
// never copies of each other: a report's two rows are two executions.
func mergeFills(events []TradeEvent) []fill {
	events = slices.Clone(events)
	slices.SortFunc(events, func(a, b TradeEvent) int {
		return cmp.Or(cmp.Compare(docRank(a), docRank(b)), strings.Compare(a.Source, b.Source), cmp.Compare(a.Line, b.Line))
	})
	var fills []fill
	for _, e := range events {
		copied := false
		for i := range fills {
			if !fills[i].sources[e.Source] && sameFill(e, fills[i].TradeEvent) {
				fills[i].sources[e.Source] = true
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
// quantity, in state.
func total(t Trade, fills []fill, state string) Trade {
	var qty, amount, fees, tax *big.Rat
	for _, f := range fills {
		qty = add(qty, dec(f.Quantity))
		amount = add(amount, gross(f.TradeEvent))
		fees = add(fees, dec(f.Fees))
		tax = add(tax, dec(f.TaxWithheld))
	}
	t.Quantity, t.Amount, t.Fees, t.TaxWithheld = DecText(qty, 8), DecText(amount, 8), DecText(fees, 8), DecText(tax, 8)
	t.AvgPrice = DecText(div(amount, qty), 6)
	if len(fills) > 0 {
		t.Currency, t.Record = fills[0].Currency, fills[0].Source
	}
	t.State = state
	return t
}

// agree reports whether mail's trade a matches the report's r: the same
// quantity and an average price within 0.5%, or for rows without a
// quantity, an amount within 0.5%.
func agree(a, r Trade) bool {
	qa, qr := dec(a.Quantity), dec(r.Quantity)
	if qa != nil || qr != nil {
		return eq(qa, qr) && (a.AvgPrice == "" || r.AvgPrice == "" || near(dec(a.AvgPrice), dec(r.AvgPrice), agreeTolerance))
	}
	return near(dec(a.Amount), dec(r.Amount), agreeTolerance)
}

func describe(t Trade) string {
	if t.Quantity == "" {
		return t.Amount + " " + t.Currency
	}
	if t.AvgPrice == "" {
		return t.Quantity
	}
	return t.Quantity + " @ " + t.AvgPrice + " " + t.Currency
}
