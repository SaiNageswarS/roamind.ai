package finance

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"

	"roamind.ai/connectors/market"
)

// The ledger keys every instrument by its ISIN, the one name of it that
// every report, mail and price feed agree on. Reports and mails mostly
// name a stock by its ticker or its name instead, so a Resolver looks the
// ISIN up: from what the user told before (Names), the ledger's own
// instruments and listings, then EODHD's search for stocks and ETFs and
// AMFI's scheme list for Indian funds. A name that fits more than one
// instrument is never guessed: the user picks, and Names remembers it.

// Names are the user's own names of instruments, each the ISIN it is
// (store.Store's finance_stock_names).
type Names interface {
	StockName(ctx context.Context, userID, key string) (string, error)
	SaveStockName(ctx context.Context, userID, key, isin string) error
}

// Lookup is the market data a Resolver searches (market.Client).
type Lookup interface {
	Search(ctx context.Context, isin string) ([]market.Listing, error)
	SearchSymbol(ctx context.Context, symbol string) ([]market.Listing, error)
	SearchName(ctx context.Context, name string) ([]market.Listing, error)
	AMFINAVs(ctx context.Context) (map[string]market.NAV, error)
}

// Candidate is an instrument a name may be.
type Candidate struct {
	ISIN     string `json:"isin"`
	Name     string `json:"name"`
	Symbol   string `json:"symbol,omitempty"`
	Exchange string `json:"exchange,omitempty"`
}

func (c Candidate) String() string {
	s := cmp.Or(c.Name, c.Symbol)
	if c.Symbol != "" && c.Name != "" {
		s += " (" + c.Symbol + cmp.Or(" on "+c.Exchange, "") + ")"
	}
	return s + " " + c.ISIN
}

// Resolver finds the ISINs of a user's rows. One serves a tool call or a
// mail: it keeps what it looked up, and AMFI's list once it is read.
type Resolver struct {
	DB     *DB
	Names  Names
	Lookup Lookup // nil: the ledger and the user's names only
	UserID string

	mu    sync.Mutex
	known map[string]resolved // by key
	funds []Candidate
	ins   map[string]Instrument
	lists map[string][]Listing
}

type resolved struct {
	isin  string
	cands []Candidate
}

// Unresolved is a row whose ISIN couldn't be found: none, or more than
// one, Candidates.
type Unresolved struct {
	What       string // the symbol or name, as the row gives it
	Candidates []Candidate
}

func (u Unresolved) Error() string {
	if len(u.Candidates) == 0 {
		return fmt.Sprintf("no instrument found for %q", u.What)
	}
	cs := make([]string, len(u.Candidates))
	for i, c := range u.Candidates {
		cs[i] = c.String()
	}
	return fmt.Sprintf("%q may be any of: %s", u.What, strings.Join(cs, "; "))
}

// Resolve sets the row's ISIN when it has none, from its symbol, exchange
// and name; an *Unresolved error when none fits, or more than one does.
// A row with an ISIN is left as it is: CheckRow checks it.
func (r *Resolver) Resolve(ctx context.Context, row *Row) error {
	row.ISIN = strings.ToUpper(strings.TrimSpace(row.ISIN))
	if row.ISIN != "" {
		return nil
	}
	symbol, exchange, name := strings.ToUpper(strings.TrimSpace(row.Symbol)), exchangeOf(row.Exchange), strings.TrimSpace(row.Name)
	what := cmp.Or(symbol, name)
	if what == "" {
		return &Unresolved{What: "(a row with no ISIN, symbol or name)"}
	}
	currency := strings.ToUpper(strings.TrimSpace(row.Currency))
	keys := stockKeys(symbol, exchange, currency, name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if k, ok := r.known[keys[0]]; ok {
		return r.answer(row, what, k)
	}
	k, err := r.resolve(ctx, keys, symbol, exchange, name, currency)
	if err != nil {
		return err
	}
	if r.known == nil {
		r.known = map[string]resolved{}
	}
	r.known[keys[0]] = k
	return r.answer(row, what, k)
}

func (r *Resolver) answer(row *Row, what string, k resolved) error {
	if k.isin == "" {
		return &Unresolved{What: what, Candidates: k.cands}
	}
	row.ISIN = k.isin
	return nil
}

func (r *Resolver) resolve(ctx context.Context, keys []string, symbol, exchange, name, currency string) (resolved, error) {
	// What the user said it is.
	for _, key := range keys {
		isin, err := r.Names.StockName(ctx, r.UserID, key)
		if err != nil || isin != "" {
			return resolved{isin: isin}, err
		}
	}
	// What the ledger knows.
	if err := r.loadLedger(ctx); err != nil {
		return resolved{}, err
	}
	var cands []Candidate
	if symbol != "" {
		for isin, ls := range r.lists {
			for _, l := range ls {
				if strings.EqualFold(l.Symbol, symbol) {
					cands = append(cands, Candidate{ISIN: isin, Name: r.ins[isin].Name, Symbol: l.Symbol, Exchange: l.MIC})
				}
			}
		}
	} else {
		for isin, in := range r.ins {
			if sameName(in.Name, name) {
				cands = append(cands, Candidate{ISIN: isin, Name: in.Name})
			}
		}
	}
	if k := pick(cands, exchange); k.isin != "" {
		return k, nil
	}
	// The market's.
	found, err := r.search(ctx, symbol, "", currency)
	if err == nil && len(found) == 0 && name != "" {
		found, err = r.search(ctx, "", name, currency)
	}
	if err != nil {
		return resolved{}, err
	}
	k := pick(found, exchange)
	if k.isin != "" {
		// Kept, so the next report of it resolves alike without a search;
		// under its most precise key only, which what decided it is part of.
		if err := r.Names.SaveStockName(ctx, r.UserID, keys[0], k.isin); err != nil {
			return resolved{}, err
		}
	}
	return k, nil
}

// search is what the market has for a symbol, else a name: an Indian
// fund's from AMFI's list, else EODHD's search.
func (r *Resolver) search(ctx context.Context, symbol, name, currency string) ([]Candidate, error) {
	if r.Lookup == nil {
		return nil, nil
	}
	var cands []Candidate
	var err error
	var ls []market.Listing
	switch {
	case symbol == "" && name == "":
		return nil, nil
	case symbol != "":
		ls, err = r.Lookup.SearchSymbol(ctx, symbol)
	case looksLikeFund(name, currency):
		if err := r.loadFunds(ctx); err != nil {
			return nil, err
		}
		for _, f := range r.funds {
			if nameWithin(name, f.Name) {
				cands = append(cands, f)
			}
		}
		return cands, nil
	default:
		ls, err = r.Lookup.SearchName(ctx, name)
		ls = slices.DeleteFunc(ls, func(l market.Listing) bool { return !sameName(l.Name, name) })
	}
	if errors.Is(err, market.ErrNoKey) {
		return nil, nil
	}
	if err != nil {
		slog.Warn("instrument search", "symbol", symbol, "name", name, "err", err)
		return nil, nil
	}
	for _, l := range ls {
		if currency == "" || strings.EqualFold(l.Currency, currency) {
			cands = append(cands, Candidate{ISIN: l.ISIN, Name: l.Name, Symbol: l.Code, Exchange: l.Exchange})
		}
	}
	return cands, nil
}

// pick is the one ISIN among cands, those on exchange first; else the
// candidates, one per ISIN, at most maxCandidates.
func pick(cands []Candidate, exchange string) resolved {
	byISIN := func(cs []Candidate) []Candidate {
		var out []Candidate
		seen := map[string]bool{}
		for _, c := range cs {
			if !seen[c.ISIN] && ValidISIN(c.ISIN) {
				seen[c.ISIN] = true
				out = append(out, c)
			}
		}
		return out
	}
	if exchange != "" {
		on := byISIN(slices.DeleteFunc(slices.Clone(cands), func(c Candidate) bool { return exchangeOf(c.Exchange) != exchange }))
		if len(on) == 1 {
			return resolved{isin: on[0].ISIN}
		}
	}
	all := byISIN(cands)
	if len(all) == 1 {
		return resolved{isin: all[0].ISIN}
	}
	if len(all) > maxCandidates {
		all = all[:maxCandidates]
	}
	return resolved{cands: all}
}

const maxCandidates = 6

func (r *Resolver) loadLedger(ctx context.Context) error {
	if r.ins != nil {
		return nil
	}
	ins, err := r.DB.Instruments(ctx)
	if err != nil {
		return err
	}
	lists, err := r.DB.Listings(ctx)
	if err != nil {
		return err
	}
	r.ins, r.lists = ins, lists
	return nil
}

func (r *Resolver) loadFunds(ctx context.Context) error {
	if r.funds != nil {
		return nil
	}
	navs, err := r.Lookup.AMFINAVs(ctx)
	if err != nil {
		return fmt.Errorf("AMFI's scheme list: %w", err)
	}
	r.funds = make([]Candidate, 0, len(navs))
	for _, n := range navs {
		r.funds = append(r.funds, Candidate{ISIN: n.ISIN, Name: n.Name, Symbol: n.Scheme, Exchange: "AMFI"})
	}
	slices.SortFunc(r.funds, func(a, b Candidate) int { return cmp.Compare(a.Name+a.ISIN, b.Name+b.ISIN) })
	return nil
}

// Known reports whether isin is an instrument: one the ledger has, or the
// market lists. Without a Lookup, a valid ISIN is taken as it is.
func (r *Resolver) Known(ctx context.Context, isin string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadLedger(ctx); err != nil {
		return "", err
	}
	if in, ok := r.ins[isin]; ok {
		return cmp.Or(in.Name, isin), nil
	}
	if r.Lookup == nil {
		return isin, nil
	}
	if strings.HasPrefix(isin, "INF") {
		if err := r.loadFunds(ctx); err != nil {
			return "", err
		}
		for _, f := range r.funds {
			if f.ISIN == isin {
				return f.Name, nil
			}
		}
		return "", nil
	}
	ls, err := r.Lookup.Search(ctx, isin)
	if errors.Is(err, market.ErrNoKey) {
		return isin, nil
	}
	if err != nil {
		return "", err
	}
	for _, l := range ls {
		if strings.EqualFold(l.ISIN, isin) {
			return cmp.Or(l.Name, isin), nil
		}
	}
	return "", nil
}

// Remember saves that the symbol (on exchange) or name is isin, for the
// user's later reports and mails.
func (r *Resolver) Remember(ctx context.Context, symbol, exchange, name, isin string) error {
	keys := stockKeys(strings.ToUpper(strings.TrimSpace(symbol)), exchangeOf(exchange), "", strings.TrimSpace(name))
	if len(keys) == 0 {
		return errors.New("give the symbol or the name it is")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, key := range keys {
		if err := r.Names.SaveStockName(ctx, r.UserID, key, isin); err != nil {
			return err
		}
	}
	clear(r.known)
	return nil
}

// stockKeys are the Names keys of a symbol on exchange or in currency, and
// a name, most precise first.
func stockKeys(symbol, exchange, currency, name string) []string {
	var keys []string
	if symbol != "" {
		switch {
		case exchange != "":
			keys = append(keys, "symbol:"+symbol+"@"+exchange)
		case currency != "":
			keys = append(keys, "symbol:"+symbol+"$"+currency)
		}
		keys = append(keys, "symbol:"+symbol)
	}
	if n := normName(name); n != "" {
		keys = append(keys, "name:"+n)
	}
	return keys
}

// exchangeOf is EODHD's code of an exchange as reports name it: every US
// exchange is US.
func exchangeOf(e string) string {
	e = strings.ToUpper(strings.TrimSpace(e))
	switch e {
	case "NASDAQ", "NYSE", "NYSEARCA", "NYSE ARCA", "ARCA", "AMEX", "NYSE MKT", "NYSE AMERICAN", "BATS", "CBOE", "XNAS", "XNYS", "OTC":
		return "US"
	case "XNSE", "NSE":
		return "NSE"
	case "XBOM", "BSE":
		return "BSE"
	case "XETR", "XETRA":
		return "XETRA"
	}
	return e
}

var (
	nonWord = regexp.MustCompile(`[^a-z0-9]+`)
	// Words that only say what kind of company or document it is.
	nameNoise = map[string]bool{
		"inc": true, "incorporated": true, "corp": true, "corporation": true, "co": true, "company": true,
		"ltd": true, "limited": true, "plc": true, "sa": true, "ag": true, "nv": true, "se": true, "the": true,
		"common": true, "stock": true, "shares": true, "share": true, "ordinary": true, "holdings": true,
		"plan": true, "option": true, "scheme": true,
	}
	// Words reports abbreviate.
	nameWords = map[string]string{
		"dividend": "idcw", "div": "idcw", "payout": "idcw", "gr": "growth", "dir": "direct", "reg": "regular",
		"cl": "class",
	}
)

// nameTokens are a name's words, lowercase, abbreviations spelled out and
// the noise dropped.
func nameTokens(s string) []string {
	var out []string
	for _, w := range strings.Fields(nonWord.ReplaceAllString(strings.ToLower(s), " ")) {
		w = cmp.Or(nameWords[w], w)
		if !nameNoise[w] {
			out = append(out, w)
		}
	}
	return out
}

func normName(s string) string { return strings.Join(nameTokens(s), " ") }

// sameName reports whether two names are one company's: "MongoDB, Inc."
// and "MONGODB INC".
func sameName(a, b string) bool {
	na := normName(a)
	return na != "" && na == normName(b)
}

// nameWithin reports whether every word of name is in full: a fund's
// name as a statement shortens it, within AMFI's.
func nameWithin(name, full string) bool {
	ws := nameTokens(name)
	if len(ws) == 0 {
		return false
	}
	have := nameTokens(full)
	for _, w := range ws {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// looksLikeFund reports whether a name with no symbol is an Indian mutual
// fund's, which AMFI lists, rather than a company's.
func looksLikeFund(name, currency string) bool {
	if currency != "" && currency != "INR" {
		return false
	}
	n := " " + normName(name) + " "
	for _, w := range []string{" fund ", " growth ", " idcw ", " direct ", " regular ", " etf ", " fof "} {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}
