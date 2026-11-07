package finance

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Row is a trade or dividend as a report, a mail or the user gives it,
// before it is a TradeEvent: decimals as text, the date ISO.
type Row struct {
	Date        string
	ISIN        string
	Name        string
	Side        string
	Quantity    string
	Price       string
	Amount      string // quantity × price before fees
	Fees        string
	TaxWithheld string
	Currency    string
	Ref         string // the order and trade numbers, space-separated
}

var (
	isoDay   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	currency = regexp.MustCompile(`^[A-Z]{3}$`)
)

// CheckRow returns what is wrong with a row; from and to, when set, are
// the period its date must be in.
func CheckRow(r Row, from, to time.Time) []string {
	var ps []string
	day, err := time.Parse(time.DateOnly, r.Date)
	switch {
	case err != nil || !isoDay.MatchString(r.Date):
		ps = append(ps, fmt.Sprintf("date %q is not an ISO date", r.Date))
	case !from.IsZero() && (day.Before(from) || day.After(to)):
		ps = append(ps, fmt.Sprintf("%s is outside the period", r.Date))
	case day.After(time.Now().AddDate(0, 0, 1)):
		ps = append(ps, fmt.Sprintf("%s is in the future", r.Date))
	}
	if !ValidISIN(strings.TrimSpace(r.ISIN)) {
		ps = append(ps, fmt.Sprintf("%q is not a valid ISIN", r.ISIN))
	}
	if !slices.Contains(Sides, r.Side) {
		ps = append(ps, "side must be one of "+strings.Join(Sides, ", "))
	}
	if !currency.MatchString(strings.ToUpper(strings.TrimSpace(r.Currency))) {
		ps = append(ps, fmt.Sprintf("currency %q is not an ISO code", r.Currency))
	}
	nums := map[string]*big.Rat{}
	for name, v := range map[string]string{"quantity": r.Quantity, "price": r.Price, "amount": r.Amount, "fees": r.Fees, "tax_withheld": r.TaxWithheld} {
		n, err := ParseDec(v)
		if err != nil || n != nil && n.Sign() < 0 {
			ps = append(ps, fmt.Sprintf("%s %q is not a positive decimal", name, v))
		}
		nums[name] = n
	}
	q, p, a := nums["quantity"], nums["price"], nums["amount"]
	if r.Side == SideDividend {
		if a == nil || a.Sign() == 0 {
			ps = append(ps, "a dividend needs its amount")
		}
		return ps
	}
	if q == nil || q.Sign() == 0 {
		ps = append(ps, "quantity is required")
	}
	if p == nil && a == nil && r.Side != SideTransferOut {
		ps = append(ps, "price or amount is required")
	}
	if q != nil && p != nil && a != nil {
		qp := new(big.Rat).Mul(q, p)
		diff := new(big.Rat).Abs(new(big.Rat).Sub(qp, a))
		if diff.Cmp(new(big.Rat).Mul(a, big.NewRat(1, 100))) > 0 {
			ps = append(ps, fmt.Sprintf("quantity × price is %s, not the amount %s (amount is before fees)", qp.FloatString(2), r.Amount))
		}
	}
	return ps
}

// CheckTotal compares a report's own total of side's amounts with its rows.
func CheckTotal(field, total string, rows []Row, side string) []string {
	want, err := ParseDec(total)
	if err != nil {
		return []string{fmt.Sprintf("%s %q is not a decimal", field, total)}
	}
	if want == nil {
		return nil
	}
	sum := new(big.Rat)
	for _, r := range rows {
		if r.Side != side {
			continue
		}
		a, _ := ParseDec(r.Amount)
		if a == nil {
			q, _ := ParseDec(r.Quantity)
			p, _ := ParseDec(r.Price)
			if q != nil && p != nil {
				a = new(big.Rat).Mul(q, p)
			}
		}
		if a != nil {
			sum.Add(sum, a)
		}
	}
	diff := new(big.Rat).Abs(new(big.Rat).Sub(sum, want))
	if diff.Cmp(new(big.Rat).Mul(new(big.Rat).Abs(want), big.NewRat(5, 1000))) > 0 {
		return []string{fmt.Sprintf("the rows' %s amounts add up to %s, not the report's %s: a row is missing or misread", side, sum.FloatString(2), total)}
	}
	return nil
}

// Event is the row as a trade event of account, at line of source.
func (r Row) Event(account, source string, line int, doc string, received time.Time) TradeEvent {
	day, _ := time.Parse(time.DateOnly, r.Date)
	return TradeEvent{Date: day, Source: source, Line: line, Account: account, ISIN: strings.TrimSpace(r.ISIN), Side: r.Side,
		Quantity: strings.TrimSpace(r.Quantity), Price: strings.TrimSpace(r.Price), Amount: strings.TrimSpace(r.Amount),
		Fees: strings.TrimSpace(r.Fees), TaxWithheld: strings.TrimSpace(r.TaxWithheld), Currency: strings.ToUpper(strings.TrimSpace(r.Currency)),
		Ref: strings.TrimSpace(r.Ref), Doc: doc, ReceivedAt: received}
}

// SaveInstruments records the instruments the rows name that aren't known
// yet, or are known without a name.
func (d *DB) SaveInstruments(ctx context.Context, rows []Row) error {
	known, err := d.Instruments(ctx)
	if err != nil {
		return err
	}
	add := map[string]Instrument{}
	note := func(isin, name, cur string) {
		isin = strings.TrimSpace(isin)
		if k, ok := known[isin]; ok && k.Name != "" || add[isin].Name != "" {
			return
		}
		in := Instrument{ISIN: isin, Name: strings.TrimSpace(name), Market: MarketOf(isin), Currency: strings.ToUpper(cur)}
		if strings.HasPrefix(isin, "INF") {
			in.Kind = "fund"
		}
		add[isin] = in
	}
	for _, r := range rows {
		note(r.ISIN, r.Name, r.Currency)
	}
	var ins []Instrument
	for _, in := range add {
		ins = append(ins, in)
	}
	return d.PutInstruments(ctx, ins)
}
