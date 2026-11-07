package finance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"roamind.ai/connectors/market"
)

type fakeNames map[string]string

func (n fakeNames) StockName(_ context.Context, userID, key string) (string, error) {
	return n[userID+"|"+key], nil
}

func (n fakeNames) SaveStockName(_ context.Context, userID, key, isin string) error {
	n[userID+"|"+key] = isin
	return nil
}

type fakeLookup struct {
	listings []market.Listing
	navs     map[string]market.NAV
	searches int
}

func (f *fakeLookup) Search(_ context.Context, isin string) ([]market.Listing, error) {
	f.searches++
	var out []market.Listing
	for _, l := range f.listings {
		if l.ISIN == isin {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLookup) SearchSymbol(_ context.Context, symbol string) ([]market.Listing, error) {
	f.searches++
	var out []market.Listing
	for _, l := range f.listings {
		if strings.EqualFold(l.Code, symbol) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLookup) SearchName(_ context.Context, name string) ([]market.Listing, error) {
	f.searches++
	var out []market.Listing
	for _, l := range f.listings {
		if strings.Contains(strings.ToLower(l.Name), strings.Trim(strings.ToLower(strings.Fields(name)[0]), ",.")) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLookup) AMFINAVs(context.Context) (map[string]market.NAV, error) { return f.navs, nil }

const (
	mongoDB   = "US60937P1066"
	alphabetA = "US02079K3059"
	alphabetC = "US02079K1079"
	ppfasDir  = "INF879O01027"
	ppfasReg  = "INF879O01019"
)

func TestResolve(t *testing.T) {
	ctx := context.Background()
	for _, isin := range []string{mongoDB, alphabetA, alphabetC, ppfasDir, ppfasReg} {
		if !ValidISIN(isin) {
			t.Fatalf("test ISIN %s is not valid", isin)
		}
	}
	look := &fakeLookup{
		listings: []market.Listing{
			{Code: "MDB", Exchange: "US", Name: "MongoDB Inc", Currency: "USD", ISIN: mongoDB},
			{Code: "MDB", Exchange: "F", Name: "MongoDB Inc", Currency: "EUR", ISIN: mongoDB},
			{Code: "GOOGL", Exchange: "US", Name: "Alphabet Inc Class A", Currency: "USD", ISIN: alphabetA},
			{Code: "GOOG", Exchange: "US", Name: "Alphabet Inc Class C", Currency: "USD", ISIN: alphabetC},
			// Another stock with the same ticker elsewhere.
			{Code: "MDB", Exchange: "NSE", Name: "Some Indian Co", Currency: "INR", ISIN: "INE009A01021"},
		},
		navs: map[string]market.NAV{
			ppfasDir: {ISIN: ppfasDir, Scheme: "122639", Name: "Parag Parikh Flexi Cap Fund - Direct Plan - Growth"},
			ppfasReg: {ISIN: ppfasReg, Scheme: "122640", Name: "Parag Parikh Flexi Cap Fund - Regular Plan - Growth"},
		},
	}
	names := fakeNames{}
	r := &Resolver{DB: db(t), Names: names, Lookup: look, UserID: "res-user"}
	resolve := func(row Row) (string, error) {
		t.Helper()
		err := r.Resolve(ctx, &row)
		return row.ISIN, err
	}

	// A ticker on its exchange, or in its currency, is the one listing.
	for _, row := range []Row{
		{Symbol: "MDB", Exchange: "NASDAQ"},
		{Symbol: "mdb", Currency: "USD"},
		{Name: "MongoDB, Inc.", Currency: "USD"},
	} {
		if isin, err := resolve(row); err != nil || isin != mongoDB {
			t.Errorf("%+v = %s %v", row, isin, err)
		}
	}
	// A ticker on two exchanges, with two ISINs, is the user's to pick.
	var u *Unresolved
	if _, err := resolve(Row{Symbol: "MDB"}); !errors.As(err, &u) || len(u.Candidates) != 2 {
		t.Errorf("MDB anywhere: %v", err)
	}
	// Share classes are different instruments.
	if _, err := resolve(Row{Name: "Alphabet Inc", Currency: "USD"}); !errors.As(err, &u) {
		t.Errorf("Alphabet without its class: %v", err)
	}
	if isin, err := resolve(Row{Name: "Alphabet Inc Cl C", Currency: "USD"}); err != nil || isin != alphabetC {
		t.Errorf("Alphabet class C = %s %v", isin, err)
	}
	// A fund's name as a statement shortens it, against AMFI's list; the
	// plan decides.
	if isin, err := resolve(Row{Name: "Parag Parikh Flexi Cap Dir Gr", Currency: "INR"}); err != nil || isin != ppfasDir {
		t.Errorf("the direct plan = %s %v", isin, err)
	}
	if _, err := resolve(Row{Name: "Parag Parikh Flexi Cap Fund"}); !errors.As(err, &u) || len(u.Candidates) != 2 ||
		!strings.Contains(u.Error(), ppfasReg) {
		t.Errorf("a fund without its plan: %v", err)
	}
	if _, err := resolve(Row{Name: "Nothing Like It Fund"}); !errors.As(err, &u) || len(u.Candidates) != 0 {
		t.Errorf("an unknown fund: %v", err)
	}
	// A row with an ISIN keeps it.
	if isin, err := resolve(Row{ISIN: " us0378331005 ", Symbol: "MDB"}); err != nil || isin != "US0378331005" {
		t.Errorf("a row with an ISIN = %s %v", isin, err)
	}

	// What a search found is kept: the next resolver needs no search.
	look.searches = 0
	r2 := &Resolver{DB: db(t), Names: names, Lookup: look, UserID: "res-user"}
	row := Row{Symbol: "MDB", Exchange: "NASDAQ"}
	if err := r2.Resolve(ctx, &row); err != nil || row.ISIN != mongoDB || look.searches != 0 {
		t.Errorf("kept: %s %v, %d searches", row.ISIN, err, look.searches)
	}
	// The user's pick is remembered, for them only.
	if name, err := r2.Known(ctx, ppfasReg); err != nil || !strings.Contains(name, "Regular") {
		t.Errorf("Known = %q %v", name, err)
	}
	if name, err := r2.Known(ctx, "US0378331005"); err != nil || name != "" {
		t.Errorf("an ISIN no one lists = %q %v", name, err)
	}
	if err := r2.Remember(ctx, "", "", "Parag Parikh Flexi Cap Fund", ppfasReg); err != nil {
		t.Fatal(err)
	}
	row = Row{Name: "PARAG PARIKH FLEXI CAP FUND"}
	if err := r2.Resolve(ctx, &row); err != nil || row.ISIN != ppfasReg {
		t.Errorf("after the pick = %s %v", row.ISIN, err)
	}
	other := &Resolver{DB: db(t), Names: names, UserID: "res-other"}
	row = Row{Name: "Parag Parikh Flexi Cap Fund"}
	if err := other.Resolve(ctx, &row); !errors.As(err, &u) {
		t.Errorf("another user got the pick: %s %v", row.ISIN, err)
	}
}

func TestNames(t *testing.T) {
	for _, c := range [][2]string{
		{"MongoDB, Inc.", "MONGODB INC"},
		{"Tesla Inc Common Stock", "TESLA, INC."},
		{"Alphabet Inc Cl A", "Alphabet Inc. Class A"},
	} {
		if !sameName(c[0], c[1]) {
			t.Errorf("%q and %q differ", c[0], c[1])
		}
	}
	if sameName("Alphabet Inc Class A", "Alphabet Inc Class C") || sameName("Inc.", "Ltd") {
		t.Error("different names are the same")
	}
	if !looksLikeFund("Axis Bluechip Direct Growth", "") || looksLikeFund("Infosys Ltd", "INR") || looksLikeFund("Some Fund", "USD") {
		t.Error("looksLikeFund")
	}
}
