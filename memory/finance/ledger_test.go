package finance

import (
	"math/big"
	"testing"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func tr(date, account, side, qty, price, fees, currency string) Trade {
	q, p := dec(qty), dec(price)
	return Trade{Date: d(date), Account: account, ISIN: "US0378331005", Side: side, Quantity: qty, AvgPrice: price,
		Amount: DecText(mul(q, p), 8), Fees: fees, Currency: currency}
}

func is(t *testing.T, label string, got *big.Rat, want string) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = nil, want %s", label, want)
		return
	}
	if s := trimDecimal(got.FloatString(6)); s != want {
		t.Errorf("%s = %s, want %s", label, s, want)
	}
}

var rates = NewRates([]FX{
	{Date: d("2026-01-02"), Pair: "EURUSD", Rate: "1.25"},
	{Date: d("2026-02-04"), Pair: "EURUSD", Rate: "1.25"},
	{Date: d("2026-06-01"), Pair: "EURUSD", Rate: "1.10"},
	{Date: d("2026-01-02"), Pair: "EURINR", Rate: "90"},
})

func TestLedgerOldestFirst(t *testing.T) {
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "5", "USD"),
		tr("2026-02-05", "a1", SideBuy, "10", "120", "5", "USD"),
		tr("2026-06-10", "a1", SideSell, "15", "150", "10", "USD"),
	}, nil, rates, "EUR", d("2026-12-31"))
	if len(ps) != 1 {
		t.Fatalf("positions = %+v", ps)
	}
	p := ps[0]
	is(t, "quantity", p.Quantity(), "5")
	// The second lot cost 1205; half of it is left.
	is(t, "cost", p.Cost(), "602.5")
	s := p.Sales[0]
	// 15 × 150 − 10 = 2240, less 1005 and half of 1205.
	is(t, "proceeds", s.Proceeds, "2240")
	is(t, "cost sold", s.Cost, "1607.5")
	is(t, "gain", s.Gain, "632.5")
	// 2240 / 1.10 − (1005 / 1.25 + 602.5 / 1.25) = 2036.363636 − 1286
	is(t, "gain in EUR", s.GainBase, "750.363636")
	if !s.Acquired.Equal(d("2026-01-05")) {
		t.Errorf("acquired = %v", s.Acquired)
	}
}

func TestLedgerPerAccount(t *testing.T) {
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "", "USD"),
		tr("2026-01-06", "a2", SideBuy, "10", "200", "", "USD"),
		tr("2026-06-10", "a2", SideSell, "5", "250", "", "USD"),
	}, nil, rates, "EUR", d("2026-12-31"))
	gains := map[string]string{}
	for _, p := range ps {
		for _, s := range p.Sales {
			gains[p.Account] = DecText(s.Gain, 2)
		}
	}
	// The sale in a2 takes a2's lot, not a1's older one.
	if gains["a2"] != "250" || gains["a1"] != "" {
		t.Errorf("gains = %v", gains)
	}
}

func TestLedgerSplitsAndTransfers(t *testing.T) {
	split := CorporateAction{Date: d("2026-03-01"), ISIN: "US0378331005", Kind: ActionSplit, Factor: "4"}
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "400", "", "USD"),
		// After the split, prices and quantities are in new shares.
		tr("2026-03-02", "a1", SideSell, "20", "110", "", "USD"),
		tr("2026-04-01", "a1", SideTransferOut, "10", "", "", "USD"),
		{Date: d("2026-04-01"), Account: "a2", ISIN: "US0378331005", Side: SideTransferIn, Quantity: "10", AvgPrice: "100", Amount: "1000", Currency: "USD"},
	}, []CorporateAction{split}, rates, "EUR", d("2026-12-31"))
	by := map[string]*Position{}
	for _, p := range ps {
		by[p.Account] = p
	}
	is(t, "a1 left", by["a1"].Quantity(), "10")
	is(t, "a1 cost left", by["a1"].Cost(), "1000")
	is(t, "a1 gain", by["a1"].Sales[0].Gain, "200")
	if len(by["a1"].Sales) != 1 {
		t.Errorf("the transfer out was a sale: %+v", by["a1"].Sales)
	}
	is(t, "a2 held", by["a2"].Quantity(), "10")
	is(t, "a2 cost", by["a2"].Cost(), "1000")

	// Before the ex-date the split isn't applied yet.
	early := Ledger([]Trade{tr("2026-01-05", "a1", SideBuy, "10", "400", "", "USD")}, []CorporateAction{split}, rates, "EUR", d("2026-02-01"))
	is(t, "before the split", early[0].Quantity(), "10")
}

func TestLedgerDividendsAndOverselling(t *testing.T) {
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "", "USD"),
		{Date: d("2026-02-10"), Account: "a1", ISIN: "US0378331005", Side: SideDividend, Amount: "25", TaxWithheld: "3.75", Currency: "USD"},
		tr("2026-06-10", "a1", SideSell, "12", "150", "", "USD"),
	}, nil, rates, "EUR", d("2026-12-31"))
	p := ps[0]
	is(t, "dividend", p.Income[0].Amount, "25")
	is(t, "dividend in EUR", p.Income[0].AmountBase, "20")
	is(t, "withheld", p.Income[0].TaxWithheld, "3.75")
	is(t, "oversold", p.Oversold, "2")
	// Only the 10 shares held are a sale: 10 × 150 − 1000.
	is(t, "gain", p.Sales[0].Gain, "500")
	is(t, "held", p.Quantity(), "0")
}

func TestRatesConvert(t *testing.T) {
	is(t, "USD to INR", rates.Convert(dec("125"), "USD", "INR", d("2026-01-10")), "9000")
	// The last rate within ten days stands in for a weekend.
	if rates.Convert(dec("1"), "USD", "EUR", d("2026-01-20")) != nil {
		t.Error("a rate eighteen days old was used")
	}
	if rates.Convert(dec("1"), "JPY", "EUR", d("2026-01-02")) != nil {
		t.Error("a missing currency converted")
	}
}

func TestProfit(t *testing.T) {
	trades := []Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "", "USD"),
		tr("2026-06-10", "a1", SideSell, "5", "120", "", "USD"),
		{Date: d("2026-06-15"), Account: "a1", ISIN: "US0378331005", Side: SideDividend, Amount: "11", Currency: "USD"},
		{Date: d("2026-02-04"), Account: "a1", ISIN: "INE009A01021", Side: SideBuy, Quantity: "2", AvgPrice: "9000", Amount: "18000", Currency: "INR"},
	}
	closes := map[string]Price{"US0378331005": {Close: "125", Currency: "USD"}}
	rates := NewRates([]FX{
		{Date: d("2026-01-02"), Pair: "EURUSD", Rate: "1.25"},
		{Date: d("2026-06-10"), Pair: "EURUSD", Rate: "1.10"},
		{Date: d("2026-02-04"), Pair: "EURINR", Rate: "90"},
		{Date: d("2026-06-19"), Pair: "EURINR", Rate: "90"},
	})
	pf := BuildPortfolio(trades, nil, rates, "EUR", nil, closes, d("2026-06-20"))
	byYear, err := pf.Profit("year")
	if err != nil || len(byYear) != 1 || byYear[0].Key != "2026" {
		t.Fatalf("by year = %+v %v", byYear, err)
	}
	// 5 × 120 / 1.10 − 5 × 100 / 1.25 = 545.454545 − 400
	is(t, "realized 2026", byYear[0].Realized, "145.454545")
	is(t, "dividends 2026", byYear[0].Dividends, "10")
	byMarket, _ := pf.Profit("market")
	if len(byMarket) != 2 || byMarket[0].Key != "IN" || byMarket[1].Key != "US" {
		t.Fatalf("by market = %+v", byMarket)
	}
	// 5 × 125 / 1.10 − 400; INFY has no price, so IN has no unrealized gain.
	is(t, "unrealized US", byMarket[1].Unrealized, "168.181818")
	if byMarket[0].Unrealized != nil || len(pf.Missing) != 1 || pf.Missing[0] != "no price for INE009A01021" {
		t.Errorf("IN = %+v, missing = %v", byMarket[0], pf.Missing)
	}
	if _, err := pf.Profit("week"); err == nil {
		t.Error("profit by week")
	}

}

func TestLedgerUnknownCost(t *testing.T) {
	ps := Ledger([]Trade{
		{Date: d("2026-01-05"), Account: "a1", ISIN: "US0378331005", Side: SideBuy, Quantity: "10", Currency: "USD"},
		tr("2026-01-06", "a1", SideBuy, "10", "100", "", "USD"),
		tr("2026-06-10", "a1", SideSell, "5", "150", "", "USD"),
	}, nil, rates, "EUR", d("2026-12-31"))
	p := ps[0]
	is(t, "held", p.Quantity(), "15")
	if p.Cost() != nil || p.Sales[0].Cost != nil || p.Sales[0].Gain != nil {
		t.Errorf("a buy without a price gave cost %v, sale %+v", p.Cost(), p.Sales[0])
	}
	is(t, "proceeds", p.Sales[0].Proceeds, "750")
}

func TestLedgerBadSplitFactor(t *testing.T) {
	zero := CorporateAction{Date: d("2026-03-01"), ISIN: "US0378331005", Kind: ActionSplit, Factor: "0"}
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "", "USD"),
		tr("2026-06-10", "a1", SideSell, "5", "150", "", "USD"),
	}, []CorporateAction{zero}, rates, "EUR", d("2026-12-31"))
	is(t, "held", ps[0].Quantity(), "5")
}

func TestOversoldIsMissing(t *testing.T) {
	trades := []Trade{tr("2026-06-10", "a1", SideSell, "5", "150", "", "USD")}
	pf := BuildPortfolio(trades, nil, rates, "EUR", nil, nil, d("2026-12-31"))
	if len(pf.positions[0].Sales) != 0 {
		t.Errorf("a sale of nothing: %+v", pf.positions[0].Sales)
	}
	if len(pf.Missing) != 1 || pf.Missing[0] != "no buy for 5 US0378331005 sold in a1" {
		t.Errorf("missing = %v", pf.Missing)
	}
}

func TestNewRatesUnsorted(t *testing.T) {
	rs := NewRates([]FX{{Date: d("2026-06-01"), Pair: "EURUSD", Rate: "1.10"}, {Date: d("2026-01-02"), Pair: "EURUSD", Rate: "1.25"}})
	is(t, "June", rs.Convert(dec("110"), "USD", "EUR", d("2026-06-03")), "100")
	is(t, "January", rs.Convert(dec("125"), "USD", "EUR", d("2026-01-02")), "100")
	if rs.Convert(dec("1"), "USD", "EUR", d("2026-01-01")) != nil {
		t.Error("a rate from after the day was used")
	}
}

func TestLedgerTransferKeepsLots(t *testing.T) {
	ps := Ledger([]Trade{
		tr("2026-01-05", "a1", SideBuy, "10", "100", "", "USD"),
		tr("2026-02-05", "a1", SideBuy, "10", "120", "", "USD"),
		tr("2026-05-10", "a1", SideTransferOut, "15", "", "", "USD"),
		// The other broker shows the transfer two days later, at its own
		// price that day.
		{Date: d("2026-05-12"), Account: "a2", ISIN: "US0378331005", Side: SideTransferIn, Quantity: "15", AvgPrice: "180", Amount: "2700", Currency: "USD"},
		tr("2026-06-10", "a2", SideSell, "12", "200", "", "USD"),
	}, nil, rates, "EUR", d("2026-12-31"))
	by := map[string]*Position{}
	for _, p := range ps {
		by[p.Account] = p
	}
	is(t, "a1 left", by["a1"].Quantity(), "5")
	is(t, "a1 cost left", by["a1"].Cost(), "600")
	// a2 has the January lot and half the February one, at their costs.
	s := by["a2"].Sales[0]
	is(t, "cost of the 12 sold", s.Cost, "1240")
	is(t, "gain", s.Gain, "1160")
	if !s.Acquired.Equal(d("2026-01-05")) {
		t.Errorf("acquired %v, want the January buy", s.Acquired)
	}
	is(t, "a2 left", by["a2"].Quantity(), "3")
	// A transfer in that matches no transfer out arrives at its price.
	lone := Ledger([]Trade{{Date: d("2026-05-12"), Account: "a2", ISIN: "US0378331005", Side: SideTransferIn, Quantity: "3", AvgPrice: "180", Amount: "540", Currency: "USD"}},
		nil, rates, "EUR", d("2026-12-31"))
	is(t, "lone transfer in", lone[0].Cost(), "540")
}
