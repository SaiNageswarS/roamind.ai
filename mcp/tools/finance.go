package tools

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/finance"
	"roamind.ai/memory/store"
)

// ReportRow is a trade or dividend as a report or the user gives it.
type ReportRow struct {
	Date        string `json:"date" jsonschema:"the trade date, ISO (YYYY-MM-DD)"`
	ISIN        string `json:"isin" jsonschema:"the instrument's ISIN, 12 characters"`
	Name        string `json:"name,omitempty" jsonschema:"the instrument's name as the report shows it"`
	Side        string `json:"side" jsonschema:"buy, sell, vest (an RSU vest), espp (an ESPP purchase), dividend, transfer_in or transfer_out"`
	Quantity    string `json:"quantity,omitempty" jsonschema:"shares or units, a positive decimal; required but for dividends"`
	Price       string `json:"price,omitempty" jsonschema:"price of one unit, a decimal; for a transfer in, its original cost per unit"`
	Amount      string `json:"amount,omitempty" jsonschema:"quantity × price before fees, positive; a dividend's gross amount"`
	Fees        string `json:"fees,omitempty" jsonschema:"brokerage, transaction taxes and other charges of the trade, positive"`
	TaxWithheld string `json:"tax_withheld,omitempty" jsonschema:"tax withheld on a dividend"`
	Currency    string `json:"currency" jsonschema:"ISO currency of price, amount and fees, e.g. INR"`
	Ref         string `json:"ref,omitempty" jsonschema:"the order or trade number, when shown"`
}

// ReportHolding is a holding a report shows.
type ReportHolding struct {
	ISIN     string `json:"isin"`
	Name     string `json:"name,omitempty"`
	Quantity string `json:"quantity" jsonschema:"shares or units held, a decimal"`
	Value    string `json:"value,omitempty" jsonschema:"the value the report gives it"`
	Currency string `json:"currency,omitempty"`
}

// ImportReportInput is import_report's input.
type ImportReportInput struct {
	File           string          `json:"file" jsonschema:"the report's path in the workspace, as the upload was saved"`
	Broker         string          `json:"broker" jsonschema:"the broker or platform that issued it, e.g. zerodha"`
	AccountNumber  string          `json:"account_number,omitempty" jsonschema:"the account, client or demat number as the report shows it, masked digits and all; empty when it shows none"`
	Currency       string          `json:"currency,omitempty" jsonschema:"the account's currency, when the report says"`
	Doc            string          `json:"doc" jsonschema:"tradebook, statement, contract_note or holdings (holdings only)"`
	PeriodFrom     string          `json:"period_from,omitempty" jsonschema:"the first day the report's trades cover, ISO; required but for holdings"`
	PeriodTo       string          `json:"period_to,omitempty" jsonschema:"its last day, ISO"`
	Rows           []ReportRow     `json:"rows,omitempty" jsonschema:"every trade and dividend in the report, one per row as it lists them"`
	AsOf           string          `json:"as_of,omitempty" jsonschema:"the date of the holdings it shows, ISO; required with holdings"`
	Holdings       []ReportHolding `json:"holdings,omitempty" jsonschema:"the holdings it shows, if any"`
	TotalBuyAmount string          `json:"total_buy_amount,omitempty" jsonschema:"the report's own total of buys before fees, when it gives one; checked against the rows"`
	TotalSell      string          `json:"total_sell_amount,omitempty" jsonschema:"its total of sells before fees, when it gives one"`
}

// ImportReportOutput is import_report's result.
type ImportReportOutput struct {
	Message   string   `json:"message"`
	Account   string   `json:"account,omitempty"`
	Notices   []string `json:"notices,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// RecordTradeInput is record_trade's input.
type RecordTradeInput struct {
	Account string `json:"account" jsonschema:"the account's id or name, from list_accounts"`
	ReportRow
}

// AccountInput names an account.
type AccountInput struct {
	Account string `json:"account" jsonschema:"the account's id or name, from list_accounts"`
	Name    string `json:"name" jsonschema:"what the user calls it"`
}

// AccountEntry is an account as list_accounts shows it.
type AccountEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Broker   string `json:"broker"`
	Number   string `json:"number,omitempty"`
	Currency string `json:"currency,omitempty"`
}

// ListAccountsOutput is list_accounts' result.
type ListAccountsOutput struct {
	Accounts []AccountEntry `json:"accounts"`
}

// HoldingLine is a stock across accounts, as list_holdings shows it.
// Money is in the base currency unless it says otherwise.
type HoldingLine struct {
	Name        string        `json:"name"`
	ISIN        string        `json:"isin"`
	Market      string        `json:"market"`
	Quantity    string        `json:"quantity"`
	Accounts    []AccountLine `json:"accounts,omitempty"`
	Cost        string        `json:"cost,omitempty"`
	Value       string        `json:"value,omitempty"`
	Unrealized  string        `json:"unrealized_gain,omitempty"`
	Realized    string        `json:"realized_gain,omitempty"`
	Dividends   string        `json:"dividends,omitempty"`
	Close       string        `json:"close,omitempty" jsonschema:"latest close, in close_currency"`
	CloseCcy    string        `json:"close_currency,omitempty"`
	CloseDate   string        `json:"close_date,omitempty"`
	Provisional bool          `json:"provisional,omitempty" jsonschema:"some of it rests on mail or chat trades no report has confirmed yet"`
	Points      []PointEntry  `json:"recent_points,omitempty"`
}

// AccountLine is one account's part of a stock.
type AccountLine struct {
	Account  string `json:"account"`
	Quantity string `json:"quantity"`
	AvgCost  string `json:"avg_cost" jsonschema:"per share, fees included, in currency"`
	Currency string `json:"currency"`
}

// PointEntry is an entry or exit.
type PointEntry struct {
	Date     string `json:"date"`
	Side     string `json:"side"`
	Account  string `json:"account"`
	Quantity string `json:"quantity"`
	Price    string `json:"price,omitempty"`
	Currency string `json:"currency"`
	State    string `json:"state" jsonschema:"confirmed by a report, or provisional from mail or chat"`
}

// ListHoldingsOutput is list_holdings' result.
type ListHoldingsOutput struct {
	BaseCurrency string        `json:"base_currency"`
	Holdings     []HoldingLine `json:"holdings"`
	Sold         []HoldingLine `json:"sold_out,omitempty" jsonschema:"stocks no longer held, with what they made"`
	Cost         string        `json:"total_cost,omitempty"`
	Value        string        `json:"total_value,omitempty"`
	Unrealized   string        `json:"total_unrealized_gain,omitempty"`
	Realized     string        `json:"total_realized_gain,omitempty"`
	Dividends    string        `json:"total_dividends,omitempty"`
	Missing      []string      `json:"missing,omitempty" jsonschema:"why figures are left out: say so rather than guess them"`
}

// StockHistoryInput is stock_history's input.
type StockHistoryInput struct {
	Stock string `json:"stock" jsonschema:"the ISIN, or part of the stock's name"`
}

// StockHistoryOutput is stock_history's result.
type StockHistoryOutput struct {
	Stock  HoldingLine  `json:"stock"`
	Points []PointEntry `json:"points"`
}

// ProfitInput is profit's input.
type ProfitInput struct {
	By string `json:"by,omitempty" jsonschema:"stock, market or year; stock by default"`
}

// ProfitEntry is a line of profit's result.
type ProfitEntry struct {
	Key        string `json:"key"`
	Name       string `json:"name,omitempty"`
	Realized   string `json:"realized_gain"`
	Dividends  string `json:"dividends"`
	Unrealized string `json:"unrealized_gain,omitempty"`
}

// ProfitOutput is profit's result.
type ProfitOutput struct {
	BaseCurrency string        `json:"base_currency"`
	By           string        `json:"by"`
	Lines        []ProfitEntry `json:"lines"`
	Missing      []string      `json:"missing,omitempty"`
}

// ConflictEntry is a conflict as list_conflicts shows it.
type ConflictEntry struct {
	ID      int64  `json:"id"`
	Account string `json:"account"`
	ISIN    string `json:"isin"`
	Day     string `json:"day"`
	Detail  string `json:"detail"`
	State   string `json:"state"`
}

// ListConflictsInput is list_conflicts' input.
type ListConflictsInput struct {
	State string `json:"state,omitempty" jsonschema:"open, resolved or dismissed; open by default"`
}

// ListConflictsOutput is list_conflicts' result.
type ListConflictsOutput struct {
	Conflicts []ConflictEntry `json:"conflicts"`
}

// ConflictInput names a conflict.
type ConflictInput struct {
	ID int64 `json:"id" jsonschema:"the conflict's id, from list_conflicts"`
}

// CurrencyInput is set_base_currency's input.
type CurrencyInput struct {
	Currency string `json:"currency" jsonschema:"ISO currency code, e.g. EUR or INR"`
}

var (
	isoDay   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	currency = regexp.MustCompile(`^[A-Z]{3}$`)
)

// financeTools adds the tools of the user's finances: importing reports,
// recording trades, and every figure about their holdings, profit and
// conflicts. Figures come from the ledger (memory/finance); the model
// never computes them.
func financeTools(s *mcp.Server, d Deps) {
	st, db, userID := d.Store, d.Finance, d.UserID
	reconcile := func(ctx context.Context) (finance.Result, error) {
		return finance.ReconcileUser(ctx, db, st, userID)
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "import_report",
		Description: "Import a broker report the user uploaded (a tradebook, statement, contract note or holdings report): " +
			"read the file, then pass every trade and dividend row, and the holdings it shows, exactly as it lists them. " +
			"For the days of its period, the report's trades become the account's trades; mail and chat trades there are " +
			"replaced. Rows are checked (ISIN checksum, quantity × price against amount, dates in the period) and the import " +
			"is refused with the problems listed, to fix and retry. The same file is never imported twice.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ImportReportInput) (*mcp.CallToolResult, ImportReportOutput, error) {
		var out ImportReportOutput
		path, err := workspaceFile(d, in.File)
		if err != nil {
			return nil, out, err
		}
		sha, err := hashFile(path)
		if err != nil {
			return nil, out, err
		}
		if prev, err := st.FinanceUploadOf(ctx, userID, sha); err != nil || prev != nil {
			if prev != nil {
				out.Message = fmt.Sprintf("%s was already imported on %s, into account %s", in.File, prev.ImportedAt.Format(time.DateOnly), prev.Account)
			}
			return nil, out, err
		}
		if err := oneOf("doc", in.Doc, finance.DocTradebook, finance.DocStatement, finance.DocContractNote, "holdings"); err != nil {
			return nil, out, err
		}
		var problems []string
		var from, to time.Time
		if in.Doc != "holdings" {
			from, to, problems = period(in.PeriodFrom, in.PeriodTo)
		} else if len(in.Rows) > 0 {
			problems = append(problems, "a holdings report has no rows; use statement for one with trades")
		}
		for i, r := range in.Rows {
			for _, p := range checkRow(r, from, to) {
				problems = append(problems, fmt.Sprintf("row %d: %s", i+1, p))
			}
		}
		var asOf time.Time
		if len(in.Holdings) > 0 {
			if asOf, err = time.Parse(time.DateOnly, in.AsOf); err != nil || !isoDay.MatchString(in.AsOf) {
				problems = append(problems, "as_of must be the ISO date of the holdings")
			}
		}
		for i, h := range in.Holdings {
			if !finance.ValidISIN(strings.TrimSpace(h.ISIN)) {
				problems = append(problems, fmt.Sprintf("holding %d: %q is not a valid ISIN", i+1, h.ISIN))
			}
			if q, err := finance.ParseDec(h.Quantity); err != nil || q == nil || q.Sign() < 0 {
				problems = append(problems, fmt.Sprintf("holding %d: quantity %q is not a decimal of zero or more", i+1, h.Quantity))
			}
		}
		problems = append(problems, checkTotal("total_buy_amount", in.TotalBuyAmount, in.Rows, finance.SideBuy)...)
		problems = append(problems, checkTotal("total_sell_amount", in.TotalSell, in.Rows, finance.SideSell)...)
		if len(in.Rows) == 0 && len(in.Holdings) == 0 {
			problems = append(problems, "the report has no rows and no holdings")
		}
		if len(problems) > 0 {
			if len(problems) > 20 {
				problems = append(problems[:20], fmt.Sprintf("and %d more", len(problems)-20))
			}
			return nil, out, fmt.Errorf("nothing was imported; fix these and retry:\n%s", strings.Join(problems, "\n"))
		}

		acct, _, err := st.EnsureFinAccount(ctx, userID, in.Broker, in.AccountNumber, in.Currency)
		if err != nil {
			return nil, out, err
		}
		source := "upload:" + sha
		now := time.Now()
		events := make([]finance.TradeEvent, len(in.Rows))
		for i, r := range in.Rows {
			events[i] = event(r, acct.ID, source, i+1, in.Doc, now)
		}
		holdings := make([]finance.Holding, len(in.Holdings))
		for i, h := range in.Holdings {
			holdings[i] = finance.Holding{Date: asOf, Account: acct.ID, ISIN: strings.TrimSpace(h.ISIN), Quantity: h.Quantity,
				Value: h.Value, Currency: strings.ToUpper(h.Currency), Source: source}
		}
		if err := saveInstruments(ctx, db, in.Rows, in.Holdings); err != nil {
			return nil, out, err
		}
		if _, err := st.MarkFinanceDirty(ctx, userID); err != nil {
			return nil, out, err
		}
		if err := db.AddTradeEvents(ctx, userID, events); err != nil {
			return nil, out, err
		}
		if err := db.PutHoldings(ctx, userID, holdings); err != nil {
			return nil, out, err
		}
		if err := st.SaveFinanceUpload(ctx, store.FinanceUpload{UserID: userID, SHA256: sha, File: filepath.Base(path), Account: acct.ID,
			Doc: in.Doc, PeriodFrom: in.PeriodFrom, PeriodTo: in.PeriodTo, Rows: len(in.Rows)}); err != nil {
			return nil, out, err
		}
		res, err := reconcile(ctx)
		if err != nil {
			return nil, out, err
		}
		out.Account = acct.ID + " (" + acct.Label() + ")"
		out.Message = fmt.Sprintf("imported %d rows and %d holdings into %s; the ledger has %d confirmed and %d provisional trades",
			len(in.Rows), len(in.Holdings), out.Account, res.Confirmed, res.Provisional)
		for _, n := range res.Notices {
			if n.Account == acct.ID && !n.Date.Before(from) && !n.Date.After(to) {
				out.Notices = append(out.Notices, fmt.Sprintf("%s %s %s: %s", n.Date.Format(time.DateOnly), n.ISIN, n.Side, n.Detail))
			}
		}
		for _, c := range res.Conflicts {
			out.Conflicts = append(out.Conflicts, fmt.Sprintf("#%d %s %s: %s", c.ID, c.Account, c.ISIN, c.Detail))
		}
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "record_trade",
		Description: "Record a trade or dividend the user tells you about, in one of their accounts. It counts until a report " +
			"covering its day is imported, which replaces it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in RecordTradeInput) (*mcp.CallToolResult, Done, error) {
		acct, err := st.FinAccountOf(ctx, userID, in.Account)
		if err != nil {
			return nil, Done{}, err
		}
		if acct == nil {
			return nil, Done{}, fmt.Errorf("no account %q; list_accounts shows them, and a report import makes a new one", in.Account)
		}
		if ps := checkRow(in.ReportRow, time.Time{}, time.Time{}); len(ps) > 0 {
			return nil, Done{}, errors.New(strings.Join(ps, "; "))
		}
		now := time.Now()
		if err := saveInstruments(ctx, db, []ReportRow{in.ReportRow}, nil); err != nil {
			return nil, Done{}, err
		}
		if _, err := st.MarkFinanceDirty(ctx, userID); err != nil {
			return nil, Done{}, err
		}
		e := event(in.ReportRow, acct.ID, fmt.Sprintf("chat:%d", now.UnixNano()), 1, finance.DocChat, now)
		if err := db.AddTradeEvents(ctx, userID, []finance.TradeEvent{e}); err != nil {
			return nil, Done{}, err
		}
		if _, err := reconcile(ctx); err != nil {
			return nil, Done{}, err
		}
		return nil, Done{Message: fmt.Sprintf("recorded %s %s %s in %s, provisional until a report confirms it", in.Side, in.Quantity, in.ISIN, acct.Label())}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_accounts",
		Description: "List the user's brokerage accounts, made as their reports are imported.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListAccountsOutput, error) {
		out := ListAccountsOutput{Accounts: []AccountEntry{}}
		as, err := st.FinAccounts(ctx, userID)
		for _, a := range as {
			out.Accounts = append(out.Accounts, AccountEntry{ID: a.ID, Name: a.Label(), Broker: a.Broker, Number: a.Number, Currency: a.Currency})
		}
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "name_account",
		Description: "Set what the user calls one of their accounts.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in AccountInput) (*mcp.CallToolResult, Done, error) {
		acct, err := st.FinAccountOf(ctx, userID, in.Account)
		if err != nil || acct == nil {
			return nil, Done{}, cmp.Or(err, fmt.Errorf("no account %q", in.Account))
		}
		if strings.TrimSpace(in.Name) == "" {
			return nil, Done{}, errors.New("name is required")
		}
		if _, err := st.NameFinAccount(ctx, userID, acct.ID, in.Name); err != nil {
			return nil, Done{}, err
		}
		return nil, Done{Message: acct.ID + " is now " + strings.TrimSpace(in.Name)}, nil
	})
	portfolio := func(ctx context.Context) (finance.Portfolio, map[string]string, error) {
		base, err := st.BaseCurrency(ctx, userID)
		if err != nil {
			return finance.Portfolio{}, nil, err
		}
		if err := db.WaitApplied(ctx, "trades"); err != nil {
			return finance.Portfolio{}, nil, err
		}
		pf, err := db.Portfolio(ctx, userID, base, time.Now())
		if err != nil {
			return pf, nil, err
		}
		labels := map[string]string{}
		as, err := st.FinAccounts(ctx, userID)
		for _, a := range as {
			labels[a.ID] = a.Label()
		}
		return pf, labels, err
	}
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_holdings",
		Description: "The user's holdings: one line per stock across all accounts, with the quantity in each, average cost, " +
			"value, unrealized and realized gain, dividends and the latest entry and exit points, and totals, in their base " +
			"currency. Quote its figures; never compute your own.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ListHoldingsOutput, error) {
		pf, labels, err := portfolio(ctx)
		if err != nil {
			return nil, ListHoldingsOutput{}, err
		}
		out := ListHoldingsOutput{BaseCurrency: pf.Base, Holdings: []HoldingLine{}, Cost: money(pf.Cost), Value: money(pf.Value),
			Unrealized: money(pf.Unrealized), Realized: money(pf.Realized), Dividends: money(pf.Dividends), Missing: pf.Missing}
		for _, s := range pf.Stocks {
			line := holdingLine(s, labels)
			if n := len(line.Points); n > 4 {
				line.Points = line.Points[n-4:]
			}
			if s.Quantity.Sign() > 0 {
				out.Holdings = append(out.Holdings, line)
			} else {
				out.Sold = append(out.Sold, line)
			}
		}
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "stock_history",
		Description: "Every entry and exit point of one stock across the user's accounts, with its current holding.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in StockHistoryInput) (*mcp.CallToolResult, StockHistoryOutput, error) {
		pf, labels, err := portfolio(ctx)
		if err != nil {
			return nil, StockHistoryOutput{}, err
		}
		q := strings.ToLower(strings.TrimSpace(in.Stock))
		var found []finance.Stock
		for _, s := range pf.Stocks {
			if strings.ToLower(s.ISIN) == q {
				found = []finance.Stock{s}
				break
			}
			if q != "" && strings.Contains(strings.ToLower(s.Name), q) {
				found = append(found, s)
			}
		}
		switch len(found) {
		case 0:
			return nil, StockHistoryOutput{}, fmt.Errorf("the user has no trades of %q", in.Stock)
		case 1:
		default:
			var names []string
			for _, s := range found {
				names = append(names, s.Name+" ("+s.ISIN+")")
			}
			return nil, StockHistoryOutput{}, fmt.Errorf("%q matches %s; ask with the ISIN", in.Stock, strings.Join(names, ", "))
		}
		line := holdingLine(found[0], labels)
		out := StockHistoryOutput{Points: line.Points}
		line.Points = nil
		out.Stock = line
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "profit",
		Description: "The user's profit in their base currency, by stock, market (US, EU, IN) or year: realized gains and " +
			"dividends, and unrealized gains by stock and market.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ProfitInput) (*mcp.CallToolResult, ProfitOutput, error) {
		by := cmp.Or(in.By, "stock")
		pf, _, err := portfolio(ctx)
		if err != nil {
			return nil, ProfitOutput{}, err
		}
		lines, err := pf.Profit(by)
		if err != nil {
			return nil, ProfitOutput{}, err
		}
		out := ProfitOutput{BaseCurrency: pf.Base, By: by, Lines: []ProfitEntry{}, Missing: pf.Missing}
		for _, l := range lines {
			out.Lines = append(out.Lines, ProfitEntry{Key: l.Key, Name: l.Name, Realized: money(l.Realized),
				Dividends: money(l.Dividends), Unrealized: money(l.Unrealized)})
		}
		return nil, out, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_conflicts",
		Description: "Holdings a report showed that the user's trades don't give, open until the numbers agree: usually a " +
			"report for a gap is missing, or a transfer wasn't recorded.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListConflictsInput) (*mcp.CallToolResult, ListConflictsOutput, error) {
		state := cmp.Or(in.State, store.ConflictOpen)
		if err := oneOf("state", state, store.ConflictOpen, store.ConflictResolved, store.ConflictDismissed); err != nil {
			return nil, ListConflictsOutput{}, err
		}
		cs, err := st.FinanceConflicts(ctx, userID, state)
		out := ListConflictsOutput{Conflicts: []ConflictEntry{}}
		for _, c := range cs {
			out.Conflicts = append(out.Conflicts, ConflictEntry{ID: c.ID, Account: c.Account, ISIN: c.ISIN, Day: c.Day, Detail: c.Detail, State: c.State})
		}
		return nil, out, err
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "dismiss_conflict",
		Description: "Dismiss an open conflict the user says is fine as it is.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ConflictInput) (*mcp.CallToolResult, Done, error) {
		ok, err := st.DismissFinanceConflict(ctx, userID, in.ID)
		if err != nil {
			return nil, Done{}, err
		}
		if !ok {
			return nil, Done{}, fmt.Errorf("no open conflict #%d; list_conflicts shows them", in.ID)
		}
		return nil, Done{Message: fmt.Sprintf("dismissed #%d", in.ID)}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name:        "set_base_currency",
		Description: "Set the currency the user's totals and profit are in (EUR by default).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CurrencyInput) (*mcp.CallToolResult, Done, error) {
		c := strings.ToUpper(strings.TrimSpace(in.Currency))
		if !currency.MatchString(c) {
			return nil, Done{}, fmt.Errorf("%q is not an ISO currency code", in.Currency)
		}
		if err := st.SetBaseCurrency(ctx, userID, c); err != nil {
			return nil, Done{}, err
		}
		return nil, Done{Message: "totals are now in " + c}, nil
	})
}

// workspaceFile is file's absolute path in the user's workspace
// (agent.WorkspaceDir); files outside it are refused.
func workspaceFile(d Deps, file string) (string, error) {
	ws, err := filepath.Abs(filepath.Join(d.DataDir, "users", d.UserID, "workspace"))
	if err != nil {
		return "", err
	}
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	p = filepath.Clean(p)
	if rel, err := filepath.Rel(ws, p); err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%s is not in the workspace", file)
	}
	if info, err := os.Stat(p); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("no file %s in the workspace", file)
	}
	return p, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// period parses a report's period.
func period(fromS, toS string) (from, to time.Time, problems []string) {
	var err1, err2 error
	from, err1 = time.Parse(time.DateOnly, fromS)
	to, err2 = time.Parse(time.DateOnly, toS)
	if err1 != nil || err2 != nil || !isoDay.MatchString(fromS) || !isoDay.MatchString(toS) {
		return from, to, []string{"period_from and period_to must be the ISO dates the report covers"}
	}
	if to.Before(from) {
		return from, to, []string{"period_to is before period_from"}
	}
	return from, to, nil
}

// checkRow returns what is wrong with a row; from and to, when set, are
// the period its date must be in.
func checkRow(r ReportRow, from, to time.Time) []string {
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
	if !finance.ValidISIN(strings.TrimSpace(r.ISIN)) {
		ps = append(ps, fmt.Sprintf("%q is not a valid ISIN", r.ISIN))
	}
	if !slices.Contains(finance.Sides, r.Side) {
		ps = append(ps, "side must be one of "+strings.Join(finance.Sides, ", "))
	}
	if !currency.MatchString(strings.ToUpper(strings.TrimSpace(r.Currency))) {
		ps = append(ps, fmt.Sprintf("currency %q is not an ISO code", r.Currency))
	}
	nums := map[string]*big.Rat{}
	for name, v := range map[string]string{"quantity": r.Quantity, "price": r.Price, "amount": r.Amount, "fees": r.Fees, "tax_withheld": r.TaxWithheld} {
		n, err := finance.ParseDec(v)
		if err != nil || n != nil && n.Sign() < 0 {
			ps = append(ps, fmt.Sprintf("%s %q is not a positive decimal", name, v))
		}
		nums[name] = n
	}
	q, p, a := nums["quantity"], nums["price"], nums["amount"]
	if r.Side == finance.SideDividend {
		if a == nil || a.Sign() == 0 {
			ps = append(ps, "a dividend needs its amount")
		}
		return ps
	}
	if q == nil || q.Sign() == 0 {
		ps = append(ps, "quantity is required")
	}
	if p == nil && a == nil && r.Side != finance.SideTransferOut {
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

// checkTotal compares a report's own total of side's amounts with its rows.
func checkTotal(field, total string, rows []ReportRow, side string) []string {
	want, err := finance.ParseDec(total)
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
		a, _ := finance.ParseDec(r.Amount)
		if a == nil {
			q, _ := finance.ParseDec(r.Quantity)
			p, _ := finance.ParseDec(r.Price)
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

func event(r ReportRow, account, source string, line int, doc string, received time.Time) finance.TradeEvent {
	day, _ := time.Parse(time.DateOnly, r.Date)
	return finance.TradeEvent{Date: day, Source: source, Line: line, Account: account, ISIN: strings.TrimSpace(r.ISIN), Side: r.Side,
		Quantity: strings.TrimSpace(r.Quantity), Price: strings.TrimSpace(r.Price), Amount: strings.TrimSpace(r.Amount),
		Fees: strings.TrimSpace(r.Fees), TaxWithheld: strings.TrimSpace(r.TaxWithheld), Currency: strings.ToUpper(strings.TrimSpace(r.Currency)),
		Ref: strings.TrimSpace(r.Ref), Doc: doc, ReceivedAt: received}
}

// saveInstruments records the instruments the rows and holdings name that
// aren't known yet, or are known without a name.
func saveInstruments(ctx context.Context, db *finance.DB, rows []ReportRow, holdings []ReportHolding) error {
	known, err := db.Instruments(ctx)
	if err != nil {
		return err
	}
	add := map[string]finance.Instrument{}
	note := func(isin, name, cur string) {
		isin = strings.TrimSpace(isin)
		if k, ok := known[isin]; ok && k.Name != "" || add[isin].Name != "" {
			return
		}
		in := finance.Instrument{ISIN: isin, Name: strings.TrimSpace(name), Market: finance.MarketOf(isin), Currency: strings.ToUpper(cur)}
		if strings.HasPrefix(isin, "INF") {
			in.Kind = "fund"
		}
		add[isin] = in
	}
	for _, r := range rows {
		note(r.ISIN, r.Name, r.Currency)
	}
	for _, h := range holdings {
		note(h.ISIN, h.Name, h.Currency)
	}
	var ins []finance.Instrument
	for _, in := range add {
		ins = append(ins, in)
	}
	return db.PutInstruments(ctx, ins)
}

func holdingLine(s finance.Stock, labels map[string]string) HoldingLine {
	l := HoldingLine{Name: cmp.Or(s.Name, s.ISIN), ISIN: s.ISIN, Market: s.Market, Quantity: finance.DecText(s.Quantity, 8),
		Cost: money(s.Cost), Value: money(s.Value), Unrealized: money(s.Unrealized), Realized: money(s.Realized),
		Dividends: money(s.Dividends), Close: finance.DecText(s.Close, 6), CloseCcy: s.CloseCurrency, Provisional: s.Provisional}
	if !s.CloseDate.IsZero() {
		l.CloseDate = s.CloseDate.Format(time.DateOnly)
	}
	for _, a := range s.Accounts {
		l.Accounts = append(l.Accounts, AccountLine{Account: cmp.Or(labels[a.Account], a.Account), Quantity: finance.DecText(a.Quantity, 8),
			AvgCost: finance.DecText(a.AvgCost, 4), Currency: a.Currency})
	}
	for _, p := range s.Points {
		l.Points = append(l.Points, PointEntry{Date: p.Date.Format(time.DateOnly), Side: p.Side, Account: cmp.Or(labels[p.Account], p.Account),
			Quantity: p.Quantity, Price: p.Price, Currency: p.Currency, State: p.State})
	}
	return l
}

// money is an amount to the cent, "" when unknown.
func money(r *big.Rat) string {
	if r == nil {
		return ""
	}
	return r.FloatString(2)
}
