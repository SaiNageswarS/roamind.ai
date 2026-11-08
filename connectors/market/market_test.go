package market

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ecbCSV = `KEY,FREQ,CURRENCY,CURRENCY_DENOM,EXR_TYPE,EXR_SUFFIX,TIME_PERIOD,OBS_VALUE,OBS_STATUS
EXR.D.INR.EUR.SP00.A,D,INR,EUR,SP00,A,2026-10-28,106.0095,A
EXR.D.USD.EUR.SP00.A,D,USD,EUR,SP00,A,2026-10-28,1.1642,A
EXR.D.ISK.EUR.SP00.A,D,ISK,EUR,SP00,A,2026-10-28,NaN,A
`

const amfi = `Scheme Code;ISIN Div Payout/ ISIN Growth;ISIN Div Reinvestment;Scheme Name;Plan;Option;Net Asset Value;Date
 
Axis Mutual Fund
 
135762;INF846K01WO1;-;Axis Children's Fund;Direct Plan;Growth Option;30.5281;05-Nov-2026
135763;INF846K01WS2;INF846K01WQ6;Axis Children's Fund;Direct Plan;IDCW Option;28.1750;05-Nov-2026
135764;INF846K01WR4;-;Closed Fund;Direct Plan;Growth Option;N.A.;05-Nov-2026
`

func server(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Query().Get("api_token") != "k" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		switch {
		case r.URL.Path == "/service/data/EXR/D..EUR.SP00.A":
			if r.URL.Query().Get("startPeriod") != "2026-10-28" {
				t.Errorf("ECB from %s", r.URL.Query().Get("startPeriod"))
			}
			w.Write([]byte(ecbCSV))
		case r.URL.Path == "/spages/NAVAll.txt":
			w.Write([]byte(amfi))
		case r.URL.Path == "/api/search/US0378331005":
			w.Write([]byte(`[{"Code":"AAPL","Exchange":"US","Name":"Apple Inc","Type":"Common Stock","Currency":"USD","ISIN":"US0378331005"},
				{"Code":"APC","Exchange":"XETRA","Name":"Apple Inc","Type":"Common Stock","Currency":"EUR","ISIN":"US0378331005"},
				{"Code":"OTHER","Exchange":"US","ISIN":"US0000000000"}]`))
		case r.URL.Path == "/api/eod/AAPL.US":
			w.Write([]byte(`[{"date":"2026-11-03","open":325.58,"high":334.5,"low":325.33,"close":331.9,"adjusted_close":331.9,"volume":63160026}]`))
		case r.URL.Path == "/api/splits/AAPL.US":
			w.Write([]byte(`[{"date":"2020-08-31","split":"4.000000\/1.000000"},{"date":"2026-01-02","split":"1.000000\/10.000000"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	// The test server is on loopback, which New refuses.
	return &Client{HTTP: srv.Client(), EODHDKey: "k", ECB: srv.URL, AMFI: srv.URL, EODHD: srv.URL}
}

func TestECBRates(t *testing.T) {
	rates, err := server(t).ECBRates(context.Background(), time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC))
	if err != nil || len(rates) != 2 || rates[0].Currency != "INR" || rates[0].Rate != "106.0095" || rates[1].Currency != "USD" {
		t.Errorf("rates = %+v %v", rates, err)
	}
}

func TestAMFINAVs(t *testing.T) {
	navs, err := server(t).AMFINAVs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := navs["INF846K01WO1"]; n.NAV != "30.5281" || n.Scheme != "135762" || n.Day.Format(time.DateOnly) != "2026-11-05" ||
		n.Name != "Axis Children's Fund - Direct Plan - Growth Option" {
		t.Errorf("growth = %+v", n)
	}
	if navs["INF846K01WQ6"].NAV != "28.1750" || len(navs) != 3 {
		t.Errorf("navs = %+v", navs)
	}
}

func TestEODHD(t *testing.T) {
	c := server(t)
	ctx := context.Background()
	ls, err := c.Search(ctx, "US0378331005")
	if err != nil || len(ls) != 2 || ls[1].Exchange != "XETRA" || ls[1].Currency != "EUR" {
		t.Errorf("search = %+v %v", ls, err)
	}
	bars, err := c.EOD(ctx, "AAPL", "US", time.Time{})
	if err != nil || len(bars) != 1 || bars[0].Close != "331.9" || bars[0].Volume != 63160026 {
		t.Errorf("eod = %+v %v", bars, err)
	}
	splits, err := c.Splits(ctx, "AAPL", "US", time.Time{})
	if err != nil || len(splits) != 2 || splits[0].Factor != "4" || splits[1].Factor != "0.1" {
		t.Errorf("splits = %+v %v", splits, err)
	}
	c.EODHDKey = ""
	if _, err := c.EOD(ctx, "AAPL", "US", time.Time{}); !errors.Is(err, ErrNoKey) {
		t.Errorf("without a key: %v", err)
	}
	c.EODHDKey = "wrong"
	if _, err := c.Search(ctx, "US0378331005"); err == nil || strings.Contains(err.Error(), "wrong") {
		t.Errorf("a refused request: %v", err)
	}
}
