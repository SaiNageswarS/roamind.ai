package finance

import (
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Row is a trade or dividend as a report or a mail gives it: decimals as
// text, the date ISO.
type Row struct {
	Date        string
	ISIN        string // only when printed
	Name        string
	Symbol      string // the ticker, when the row gives one
	Exchange    string // the symbol's exchange, when given
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
	switch isin := strings.TrimSpace(r.ISIN); {
	case isin != "" && !ValidISIN(isin):
		ps = append(ps, fmt.Sprintf("%q is not a valid ISIN", r.ISIN))
	case r.Instrument() == "":
		ps = append(ps, "the row needs its ISIN, symbol or name")
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

// Instrument is what the row's buys and sells are matched by: its ISIN,
// else its symbol, else its name, as printed.
func (r Row) Instrument() string {
	if isin := strings.ToUpper(strings.TrimSpace(r.ISIN)); isin != "" {
		return isin
	}
	if sym := strings.ToUpper(strings.TrimSpace(r.Symbol)); sym != "" {
		return sym
	}
	return strings.Join(strings.Fields(r.Name), " ")
}
