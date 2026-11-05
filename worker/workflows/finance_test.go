package workflows

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/connectors/market"
	"roamind.ai/memory/finance"
	"roamind.ai/memory/finance/financetest"
	"roamind.ai/memory/store"
)

func TestProcessFinanceChannel(t *testing.T) {
	ctx := context.Background()
	d, fs, argsLog := setup(t)
	withMemory(t, &d)
	for _, ts := range []string{"0.5", "1.0"} {
		if _, err := d.Process(ctx, "r0", mention("Ev"+ts, "U1", ts, "")); err != nil {
			t.Fatal(err)
		}
	}
	owner := identity.UserID("owner@x.com")
	if _, _, err := d.Store.EnsureFinAccount(ctx, owner, "zerodha", "XX1234", "INR"); err != nil {
		t.Fatal(err)
	}
	out, err := d.financeChannel(ctx, owner)
	if err != nil || !strings.HasPrefix(out.Created, "finance-") {
		t.Fatalf("finance channel = %+v %v", out, err)
	}
	ch, _ := d.Store.Channel(ctx, owner, store.KindFinance, "")
	if again, err := d.financeChannel(ctx, owner); err != nil || again.Created != "" {
		t.Errorf("second run = %+v %v", again, err)
	}
	n := 0
	for _, c := range fs.created {
		if strings.HasPrefix(c, "finance-") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("finance channels created = %d of %v", n, fs.created)
	}

	os.Remove(argsLog)
	turn, err := d.Process(ctx, "r", slack.Envelope{TeamID: "T1", EventID: "Ev9", Event: slack.Event{
		Type: "message", ChannelType: "group", User: "U1", Text: "how are my stocks?", Channel: ch, TS: "3.0"}})
	if err != nil || turn.Skipped != "" {
		t.Fatalf("turn = %+v %v", turn, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "finance channel") || !strings.Contains(string(args), "a1 zerodha XX1234") {
		t.Errorf("the finance channel isn't in the prompt: %s", args)
	}
}

func TestFetchMarket(t *testing.T) {
	addr, stop, err := financetest.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if addr == "" {
		t.Skip("no QuestDB in bin/ (task questdb-install)")
	}
	ctx := context.Background()
	db, err := finance.Open(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	schemas, _ := financetest.Schemas()
	if err := db.Init(ctx, schemas); err != nil {
		t.Fatal(err)
	}
	const apple, fund = "US0378331005", "INF846K01WO1"
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	if err := db.PutTrades(ctx, "u", []finance.Trade{
		{Date: day, Account: "a1", ISIN: apple, Side: finance.SideBuy, Quantity: "1", Currency: "USD", State: finance.Confirmed},
		{Date: day, Account: "a1", ISIN: fund, Side: finance.SideBuy, Quantity: "10", Currency: "INR", State: finance.Provisional},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WaitApplied(ctx, "trades"); err != nil {
		t.Fatal(err)
	}
	var eodFrom []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/service/data/EXR/D..EUR.SP00.A":
			w.Write([]byte("KEY,CURRENCY,TIME_PERIOD,OBS_VALUE\nX,USD,2026-11-03,1.16\nX,INR,2026-11-03,106\n"))
		case "/spages/NAVAll.txt":
			w.Write([]byte("135762;INF846K01WO1;-;Axis Children's Fund;Direct Plan;Growth Option;30.5281;05-Nov-2026\n"))
		case "/api/search/" + apple:
			w.Write([]byte(`[{"Code":"APC","Exchange":"XETRA","Name":"Apple","Type":"Common Stock","Currency":"EUR","ISIN":"US0378331005"},
				{"Code":"AAPL","Exchange":"US","Name":"Apple Inc","Type":"Common Stock","Currency":"USD","ISIN":"US0378331005"}]`))
		case "/api/eod/AAPL.US":
			eodFrom = append(eodFrom, r.URL.Query().Get("from"))
			w.Write([]byte(`[{"date":"2026-11-03","open":1,"high":2,"low":1,"close":331.9,"volume":5}]`))
		case "/api/splits/AAPL.US":
			w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	d := Deps{Finance: db, Market: &market.Client{HTTP: srv.Client(), EODHDKey: "k", ECB: srv.URL, AMFI: srv.URL, EODHD: srv.URL}}
	out, err := d.fetchMarket(ctx, day.Add(48*time.Hour))
	if err != nil || len(out.Problems) > 0 {
		t.Fatalf("fetch = %+v %v", out, err)
	}
	if out.Rates != 2 || out.Prices != 2 || out.Listings != 2 {
		t.Errorf("fetched %+v", out)
	}
	if err := db.WaitApplied(ctx, "prices", "listings", "instruments", "fx"); err != nil {
		t.Fatal(err)
	}
	closes, err := db.LatestCloses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Apple is priced on its home exchange in dollars, not on XETRA.
	if c := closes[apple]; c.MIC != "US" || c.Close != "331.9" || c.Currency != "USD" {
		t.Errorf("Apple = %+v", c)
	}
	if c := closes[fund]; c.MIC != "AMFI" || c.Close != "30.5281" || c.Currency != "INR" {
		t.Errorf("fund = %+v", c)
	}
	ins, _ := db.Instruments(ctx)
	if in := ins[apple]; in.MainMIC != "US" || in.Name != "Apple Inc" || in.Kind != "stock" {
		t.Errorf("instrument = %+v", in)
	}
	// The next run asks only for what's new.
	if _, err := d.fetchMarket(ctx, day.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(eodFrom) != 2 || eodFrom[0] != "2026-10-27" || eodFrom[1] != "2026-11-04" {
		t.Errorf("EOD from = %v", eodFrom)
	}
}
