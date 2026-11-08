// Package market reads market data for the finance assistant: the ECB's
// euro reference rates, AMFI's daily NAVs of Indian mutual funds, and,
// with an API key, EODHD's end-of-day prices and splits and its ISIN
// lookup. Only ISINs, tickers and dates are sent; nothing about a user.
// Requests go through safehttp, so they reach public addresses only.
package market

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"roamind.ai/connectors/safehttp"
)

// Client reads the providers.
type Client struct {
	HTTP *http.Client
	// EODHDKey is the EODHD API key; without it there are no prices of
	// stocks and ETFs, nor splits.
	EODHDKey string
	// Base URLs, overridden in tests.
	ECB, AMFI, EODHD string
}

// New is a client with the providers' URLs.
func New(timeout time.Duration, eodhdKey string) *Client {
	return &Client{
		HTTP:     safehttp.New(timeout),
		EODHDKey: eodhdKey,
		ECB:      "https://data-api.ecb.europa.eu",
		AMFI:     "https://portal.amfiindia.com",
		EODHD:    "https://eodhd.com",
	}
}

// ErrNoKey is returned by EODHD calls without a key.
var ErrNoKey = errors.New("market: EODHD_API_KEY is not set")

// maxBody caps a response; AMFI's full NAV list is about 3 MB.
const maxBody = 32 << 20

func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "roamind-finance/1")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL may hold the EODHD key.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("market: %s: %w", host(u), err)
	}
	defer resp.Body.Close()
	body, err := safehttp.ReadAll(resp.Body, maxBody)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("market: %s: %s", host(u), resp.Status)
	}
	return body, nil
}

func host(u string) string {
	if p, err := url.Parse(u); err == nil {
		return p.Host + p.Path
	}
	return "request"
}

// Rate is one euro in Currency on Day.
type Rate struct {
	Day      time.Time
	Currency string
	Rate     string
}

// ECBRates returns the ECB's daily euro reference rates of every currency
// it publishes, from from on.
func (c *Client) ECBRates(ctx context.Context, from time.Time) ([]Rate, error) {
	body, err := c.get(ctx, c.ECB+"/service/data/EXR/D..EUR.SP00.A?format=csvdata&startPeriod="+from.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		// Nothing published since from.
		return nil, nil
	}
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	head, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("market: ECB: %w", err)
	}
	col := map[string]int{}
	for i, h := range head {
		col[h] = i
	}
	cur, day, val := col["CURRENCY"], col["TIME_PERIOD"], col["OBS_VALUE"]
	if cur == 0 && day == 0 {
		return nil, errors.New("market: ECB: unexpected columns")
	}
	var out []Rate
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("market: ECB: %w", err)
		}
		if max(cur, day, val) >= len(rec) || rec[val] == "" || rec[val] == "NaN" {
			continue
		}
		d, err := time.Parse(time.DateOnly, rec[day])
		if err != nil {
			continue
		}
		out = append(out, Rate{Day: d, Currency: rec[cur], Rate: rec[val]})
	}
	return out, nil
}

// schemeName is a scheme's name from its columns: the fund, then its plan
// and option when the list gives them apart.
func schemeName(cols []string) string {
	var parts []string
	for _, c := range cols {
		if c = strings.TrimSpace(c); c != "" && c != "-" {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, " - ")
}

// NAV is an Indian mutual fund's net asset value on Day.
type NAV struct {
	Day    time.Time
	Scheme string // AMFI's scheme code
	ISIN   string
	Name   string
	NAV    string
}

// AMFINAVs returns the latest NAV of every Indian mutual fund scheme, by
// ISIN (a scheme's payout and reinvestment ISINs both).
func (c *Client) AMFINAVs(ctx context.Context) (map[string]NAV, error) {
	body, err := c.get(ctx, c.AMFI+"/spages/NAVAll.txt")
	if err != nil {
		return nil, err
	}
	out := map[string]NAV{}
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		f := strings.Split(strings.TrimSpace(sc.Text()), ";")
		// Scheme Code;ISIN Growth;ISIN Reinvestment;Scheme Name;[Plan;Option;]Net Asset Value;Date
		if len(f) < 6 {
			continue
		}
		d, err := time.Parse("02-Jan-2006", f[len(f)-1])
		if err != nil {
			continue
		}
		nav := f[len(f)-2]
		if _, ok := new(big.Rat).SetString(nav); !ok {
			continue
		}
		for _, isin := range f[1:3] {
			if isin = strings.TrimSpace(isin); len(isin) == 12 {
				out[isin] = NAV{Day: d, Scheme: f[0], ISIN: isin, Name: schemeName(f[3 : len(f)-2]), NAV: nav}
			}
		}
	}
	return out, sc.Err()
}

// Listing is a place an ISIN trades, as EODHD knows it.
type Listing struct {
	Code     string // the ticker
	Exchange string // EODHD's exchange code: US, NSE, XETRA, …
	Name     string
	Type     string // Common Stock, ETF, FUND, …
	Currency string
	ISIN     string
}

// Search returns the listings of an ISIN.
func (c *Client) Search(ctx context.Context, isin string) ([]Listing, error) {
	out, err := c.search(ctx, isin)
	var mine []Listing
	for _, l := range out {
		if l.ISIN == "" || strings.EqualFold(l.ISIN, isin) {
			mine = append(mine, l)
		}
	}
	return mine, err
}

// SearchSymbol returns the listings with ticker symbol that name their
// ISIN: what a report or mail that gives a symbol and no ISIN is about.
func (c *Client) SearchSymbol(ctx context.Context, symbol string) ([]Listing, error) {
	out, err := c.search(ctx, symbol)
	var mine []Listing
	for _, l := range out {
		if l.ISIN != "" && strings.EqualFold(l.Code, symbol) {
			mine = append(mine, l)
		}
	}
	return mine, err
}

// SearchName returns the listings EODHD finds for a company's or fund's
// name that name their ISIN, best matches first.
func (c *Client) SearchName(ctx context.Context, name string) ([]Listing, error) {
	out, err := c.search(ctx, name)
	var mine []Listing
	for _, l := range out {
		if l.ISIN != "" {
			mine = append(mine, l)
		}
	}
	return mine, err
}

// search is EODHD's search for q: a ticker, ISIN or name.
func (c *Client) search(ctx context.Context, q string) ([]Listing, error) {
	if c.EODHDKey == "" {
		return nil, ErrNoKey
	}
	body, err := c.get(ctx, c.EODHD+"/api/search/"+url.PathEscape(q)+"?fmt=json&limit=50&api_token="+url.QueryEscape(c.EODHDKey))
	if err != nil {
		return nil, err
	}
	var out []Listing
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("market: EODHD search: %w", err)
	}
	return out, nil
}

// Bar is a day's prices, unadjusted.
type Bar struct {
	Day                    time.Time
	Open, High, Low, Close string
	Volume                 int
}

// EOD returns a listing's daily bars from from on.
func (c *Client) EOD(ctx context.Context, code, exchange string, from time.Time) ([]Bar, error) {
	if c.EODHDKey == "" {
		return nil, ErrNoKey
	}
	body, err := c.get(ctx, c.EODHD+"/api/eod/"+url.PathEscape(code+"."+exchange)+"?fmt=json&from="+from.Format(time.DateOnly)+"&api_token="+url.QueryEscape(c.EODHDKey))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var rows []struct {
		Date                   string
		Open, High, Low, Close json.Number
		Volume                 json.Number
	}
	if err := dec.Decode(&rows); err != nil {
		return nil, fmt.Errorf("market: EODHD eod: %w", err)
	}
	out := make([]Bar, 0, len(rows))
	for _, r := range rows {
		d, err := time.Parse(time.DateOnly, r.Date)
		if err != nil || r.Close == "" {
			continue
		}
		v, _ := r.Volume.Int64()
		out = append(out, Bar{Day: d, Open: r.Open.String(), High: r.High.String(), Low: r.Low.String(), Close: r.Close.String(), Volume: int(v)})
	}
	return out, nil
}

// Split is a split on its ex-date: Factor new shares for each old one.
type Split struct {
	Day    time.Time
	Factor string
}

// Splits returns a listing's splits from from on.
func (c *Client) Splits(ctx context.Context, code, exchange string, from time.Time) ([]Split, error) {
	if c.EODHDKey == "" {
		return nil, ErrNoKey
	}
	body, err := c.get(ctx, c.EODHD+"/api/splits/"+url.PathEscape(code+"."+exchange)+"?fmt=json&from="+from.Format(time.DateOnly)+"&api_token="+url.QueryEscape(c.EODHDKey))
	if err != nil {
		return nil, err
	}
	var rows []struct{ Date, Split string }
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("market: EODHD splits: %w", err)
	}
	var out []Split
	for _, r := range rows {
		d, err := time.Parse(time.DateOnly, r.Date)
		newShares, oldShares, ok := strings.Cut(r.Split, "/")
		if err != nil || !ok {
			continue
		}
		n, ok1 := new(big.Rat).SetString(newShares)
		o, ok2 := new(big.Rat).SetString(oldShares)
		if !ok1 || !ok2 || o.Sign() == 0 || n.Sign() == 0 {
			continue
		}
		f := new(big.Rat).Quo(n, o)
		out = append(out, Split{Day: d, Factor: strings.TrimRight(strings.TrimRight(f.FloatString(10), "0"), ".")})
	}
	return out, nil
}
