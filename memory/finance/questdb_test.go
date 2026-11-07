package finance

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"roamind.ai/memory/finance/financetest"
)

// testDB is a QuestDB the tests start (financetest), with the schemas
// applied. It is nil, and the tests skip, when bin/ has no QuestDB.
var testDB *DB

func TestMain(m *testing.M) {
	addr, stop, err := financetest.Start()
	if err == nil && addr != "" {
		err = openTestDB(addr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "finance tests:", err)
		os.Exit(1)
	}
	code := m.Run()
	if testDB != nil {
		testDB.Close()
	}
	stop()
	os.Exit(code)
}

func openTestDB(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var err error
	if testDB, err = Open(ctx, addr); err != nil {
		return err
	}
	schemas, err := financetest.Schemas()
	if err != nil {
		return err
	}
	return testDB.Init(ctx, schemas)
}

func db(t *testing.T) *DB {
	t.Helper()
	if testDB == nil {
		t.Skip("no QuestDB in bin/ (task questdb-install)")
	}
	return testDB
}

func TestInitRepeats(t *testing.T) {
	d := db(t)
	if err := d.Init(context.Background(), "../questdb-schemas"); err != nil {
		t.Fatalf("second init: %v", err)
	}
}

func TestTradeEvents(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	obs := []TradeEvent{
		{Date: day, Source: "upload:abc", Line: 1, Account: "a1", ISIN: "US0378331005", Side: "buy",
			Quantity: "10", Price: "201.125", Amount: "2011.25", Fees: "1.5", Currency: "USD",
			Ref: "ord-1", Doc: "tradebook", ReceivedAt: day.Add(20 * time.Hour)},
		{Date: day, Source: "upload:abc", Line: 2, Account: "a1", ISIN: "US0378331005", Side: "sell",
			Quantity: "0.5", Price: "202", Currency: "USD", Doc: "tradebook"},
	}
	// Written twice, as a replayed source or a retried tool call would.
	for range 2 {
		if err := d.AddTradeEvents(ctx, "obs-user", obs); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		t.Fatal(err)
	}
	got, err := d.TradeEvents(ctx, "obs-user", day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, obs) {
		t.Errorf("TradeEvents =\n%+v\nwant\n%+v", got, obs)
	}

	// The same source and line again replaces the row.
	fixed := obs[1]
	fixed.Quantity = "5"
	if err := d.AddTradeEvents(ctx, "obs-user", []TradeEvent{fixed}); err != nil {
		t.Fatal(err)
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		t.Fatal(err)
	}
	got, err = d.TradeEvents(ctx, "obs-user", day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Quantity != "5" {
		t.Errorf("after the correction: %+v", got)
	}

	if err := d.AddTradeEvents(ctx, "", obs); err == nil {
		t.Error("trade events without a user were written")
	}
	if err := d.AddTradeEvents(ctx, "obs-user", []TradeEvent{{Source: "chat:x"}}); err == nil {
		t.Error("an trade event without a date was written")
	}
}

// TestWriteRefused checks a write QuestDB refuses fails the call that made
// it, not later in the background.
func TestWriteRefused(t *testing.T) {
	d := db(t)
	err := d.AddTradeEvents(context.Background(), "refused-user", []TradeEvent{
		{Date: time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), Source: "chat:x", Line: 1, Quantity: "not a number"},
	})
	if err == nil {
		t.Error("a quantity that isn't a number was written")
	}
}

// TestTenancy writes two users' rows and checks each user reads only theirs.
func TestTenancy(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// The same source and line for both: the user is part of the key.
	for _, u := range []string{"tenant-a", "tenant-b"} {
		if err := d.AddTradeEvents(ctx, u, []TradeEvent{
			{Date: day, Source: "upload:shared", Line: 1, ISIN: "INE009A01021", Side: "buy", Quantity: "1", Account: u},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"tenant-a", "tenant-b"} {
		got, err := d.TradeEvents(ctx, u, day, day.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Account != u {
			t.Errorf("%s reads %+v", u, got)
		}
	}
}

func TestStatements(t *testing.T) {
	got := statements("-- a comment; with a semicolon\nCREATE x (a INT, -- the a\n b INT);\n\nALTER x;\n")
	want := []string{"CREATE x (a INT, \n b INT)", "ALTER x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statements = %q, want %q", got, want)
	}
}

func TestTrimDecimal(t *testing.T) {
	for in, want := range map[string]string{"12.34500000": "12.345", "3.00000000": "3", "100": "100", "": ""} {
		if got := trimDecimal(in); got != want {
			t.Errorf("trimDecimal(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestOverScale checks a value with more decimals than its column holds is
// rounded to the column's scale, which QuestDB would otherwise refuse.
func TestOverScale(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	e := TradeEvent{Date: day, Source: "upload:scale", Line: 1, Account: "a1", ISIN: "US0378331005", Side: SideBuy,
		Quantity: "0.123456789", Price: "201.1234567", Amount: "24.83", Currency: "USD", Doc: DocStatement}
	if err := d.AddTradeEvents(ctx, "scale-user", []TradeEvent{e}); err != nil {
		t.Fatal(err)
	}
	if err := d.WaitApplied(ctx, "trade_events"); err != nil {
		t.Fatal(err)
	}
	got, err := d.TradeEvents(ctx, "scale-user", day, day.AddDate(0, 0, 1))
	if err != nil || len(got) != 1 || got[0].Quantity != "0.12345679" || got[0].Price != "201.123457" {
		t.Errorf("events = %+v %v", got, err)
	}
}

func TestWaitAppliedUnknownTable(t *testing.T) {
	if err := db(t).WaitApplied(context.Background(), "trade_events", "no_such_table"); err == nil {
		t.Error("waited on a table QuestDB doesn't have")
	}
}

func TestParseDec(t *testing.T) {
	for _, s := range []string{"1512.35", "-3", "+2", ".5", "7.", " 10 "} {
		if r, err := ParseDec(s); err != nil || r == nil {
			t.Errorf("ParseDec(%q) = %v, %v", s, r, err)
		}
	}
	for _, s := range []string{"1e3", "1/2", "0x10", "0b11", "1_000", "1,000", "--1", ".", "NaN", "Inf"} {
		if _, err := ParseDec(s); err == nil {
			t.Errorf("ParseDec(%q) accepted", s)
		}
	}
}
