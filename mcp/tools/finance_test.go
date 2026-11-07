package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/finance"
	"roamind.ai/memory/finance/financetest"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/storetest"
)

// testFinance is a QuestDB the tests start, or nil when bin/ has none.
var testFinance *finance.DB

func TestMain(m *testing.M) {
	addr, stop, err := financetest.Start()
	if err == nil && addr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if testFinance, err = finance.Open(ctx, addr); err == nil {
			var schemas string
			if schemas, err = financetest.Schemas(); err == nil {
				err = testFinance.Init(ctx, schemas)
			}
		}
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "tools tests:", err)
		os.Exit(1)
	}
	code := m.Run()
	if testFinance != nil {
		testFinance.Close()
	}
	stop()
	os.Exit(code)
}

func TestFinance(t *testing.T) {
	if testFinance == nil {
		t.Skip("no QuestDB in bin/ (task questdb-install)")
	}
	ctx := context.Background()
	st := storetest.Open(t)
	data := t.TempDir()
	for _, id := range []string{"f1", "f2"} {
		if err := st.UpsertSlackUser(ctx, store.User{ID: id, Email: id + "@x.com", SlackTeamID: "T1", SlackUserID: "U" + id}); err != nil {
			t.Fatal(err)
		}
	}
	ws := filepath.Join(data, "users", "f1", "workspace")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(ws, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tradebook.csv", "date,isin,qty,price\n2026-10-10,INE009A01021,50,1500\n")
	write("other.csv", "another file")
	if err := os.MkdirAll(filepath.Join(ws, "parsers"), 0o700); err != nil {
		t.Fatal(err)
	}
	write("parsers/zerodha_tradebook.py", "# Zerodha tradebook CSV\n")
	write("notaparser.py", "")
	if err := os.WriteFile(filepath.Join(data, "secret.csv"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	connect := func(userID string) func(string, map[string]any) (string, bool) {
		st1, st2 := mcp.NewInMemoryTransports()
		server := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
		if err := Register(server, Deps{Store: st, UserID: userID, Finance: testFinance, DataDir: data}, []string{Finance}); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Connect(ctx, st1, nil); err != nil {
			t.Fatal(err)
		}
		cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil).Connect(ctx, st2, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return func(name string, args map[string]any) (string, bool) {
			t.Helper()
			res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(res.StructuredContent)
			if res.IsError {
				b, _ = json.Marshal(res.Content)
			}
			return string(b), res.IsError
		}
	}
	call := connect("f1")
	row := func(date, side, qty, price string) map[string]any {
		return map[string]any{"date": date, "isin": "INE009A01021", "name": "Infosys", "side": side,
			"quantity": qty, "price": price, "amount": fmt.Sprint(mustMul(qty, price)), "currency": "INR"}
	}
	parsed := func(rows ...map[string]any) map[string]any {
		return map[string]any{"broker": "Zerodha", "account_number": "XX1234", "doc": "tradebook",
			"period_from": "2026-10-01", "period_to": "2026-10-31", "rows": rows}
	}
	// report writes the parser's output, and returns import_report's args.
	report := func(file string, p map[string]any) map[string]any {
		b, _ := json.Marshal(p)
		write("tradebook.rows.json", string(b))
		return map[string]any{"file": file, "parser": "parsers/zerodha_tradebook.py", "parsed": "tradebook.rows.json"}
	}

	// Bad rows are refused, with every problem named.
	msg, isErr := call("import_report", report("tradebook.csv", parsed(
		map[string]any{"date": "2026-11-20", "isin": "INE009A01022", "side": "bought", "quantity": "-1", "currency": "rupees"})))
	for _, want := range []string{"outside the period", "not a valid ISIN", "side must be", "not a positive decimal", "not an ISO code"} {
		if !isErr || !strings.Contains(msg, want) {
			t.Errorf("bad row: %s; want %q", msg, want)
		}
	}
	if msg, isErr := call("import_report", report("../../secret.csv", parsed(row("2026-10-10", "buy", "50", "1500")))); !isErr || !strings.Contains(msg, "not in the workspace") {
		t.Errorf("a file outside the workspace: %s", msg)
	}
	// A parser outside parsers/ is refused, and so is output that isn't a
	// ParsedReport.
	a := report("tradebook.csv", parsed(row("2026-10-10", "buy", "50", "1500")))
	a["parser"] = "notaparser.py"
	if msg, isErr := call("import_report", a); !isErr || !strings.Contains(msg, "in parsers/") {
		t.Errorf("a parser outside parsers/: %s", msg)
	}
	if msg, isErr := call("import_report", report("tradebook.csv", map[string]any{"trades": []any{}})); !isErr || !strings.Contains(msg, "not the parser's JSON") {
		t.Errorf("output of another shape: %s", msg)
	}
	// A total that doesn't match its rows means one is missing.
	r := parsed(row("2026-10-10", "buy", "50", "1500"))
	r["total_buy_amount"] = "80000"
	if msg, isErr := call("import_report", report("tradebook.csv", r)); !isErr || !strings.Contains(msg, "add up to 75000.00") {
		t.Errorf("a wrong total: %s", msg)
	}

	msg, isErr = call("import_report", report("tradebook.csv", parsed(row("2026-10-10", "buy", "50", "1500"))))
	if isErr || !strings.Contains(msg, "imported 1 rows into a1 (zerodha XX1234) with zerodha_tradebook.py; the ledger has 1 trades") {
		t.Fatalf("import: %s", msg)
	}
	if msg, _ := call("import_report", report("tradebook.csv", parsed(row("2026-10-10", "buy", "50", "1500")))); !strings.Contains(msg, "already imported") {
		t.Errorf("second import: %s", msg)
	}

	if msg, isErr := call("record_trade", map[string]any{"account": "a1", "date": "2026-11-03", "isin": "INE009A01021",
		"side": "sell", "quantity": "10", "price": "1600", "currency": "INR"}); isErr || !strings.Contains(msg, "recorded sell 10") {
		t.Fatalf("record_trade: %s", msg)
	}
	if msg, _ := call("record_trade", map[string]any{"account": "nope", "date": "2026-11-03", "isin": "INE009A01021",
		"side": "sell", "quantity": "1", "price": "1", "currency": "INR"}); !strings.Contains(msg, "no account") {
		t.Errorf("unknown account: %s", msg)
	}
	if msg, _ := call("name_account", map[string]any{"account": "a1", "name": "Kite"}); !strings.Contains(msg, "a1 is now Kite") {
		t.Errorf("name_account: %s", msg)
	}

	msg, _ = call("list_holdings", nil)
	var h ListHoldingsOutput
	if err := json.Unmarshal([]byte(msg), &h); err != nil || len(h.Holdings) != 1 {
		t.Fatalf("list_holdings = %s %v", msg, err)
	}
	hl := h.Holdings[0]
	if hl.Name != "Infosys" || hl.Quantity != "40" || len(hl.Accounts) != 1 || hl.Accounts[0].Account != "Kite" ||
		hl.Accounts[0].AvgCost != "1500" || len(hl.Points) != 2 {
		t.Errorf("holding = %+v", hl)
	}
	if len(h.Missing) == 0 {
		t.Errorf("no price or rate, yet nothing missing: %s", msg)
	}

	msg, _ = call("stock_history", map[string]any{"stock": "infos"})
	var sh StockHistoryOutput
	if err := json.Unmarshal([]byte(msg), &sh); err != nil || len(sh.Points) != 2 || sh.Points[0].Side != "buy" {
		t.Errorf("stock_history = %s %v", msg, err)
	}
	if msg, isErr := call("profit", map[string]any{"by": "week"}); !isErr || !strings.Contains(msg, "stock, market or year") {
		t.Errorf("profit by week: %s", msg)
	}
	if msg, isErr := call("profit", map[string]any{"by": "year"}); isErr || !strings.Contains(msg, `"by":"year"`) {
		t.Errorf("profit by year: %s", msg)
	}
	if msg, _ := call("set_base_currency", map[string]any{"currency": "inr"}); !strings.Contains(msg, "INR") {
		t.Errorf("set_base_currency: %s", msg)
	}

	if msg, isErr := call("trust_mail_sender", map[string]any{"domain": "com", "trusted": true}); !isErr || !strings.Contains(msg, "not a mail domain") {
		t.Errorf("trusting a top-level domain: %s", msg)
	}
	if msg, isErr := call("trust_mail_sender", map[string]any{"domain": "Broker.Example", "trusted": true}); isErr || !strings.Contains(msg, "broker.example") {
		t.Errorf("trust: %s", msg)
	}
	if msg, _ := call("list_mail_senders", nil); !strings.Contains(msg, `{"domain":"broker.example","trusted":true}`) {
		t.Errorf("senders: %s", msg)
	}

	// A row without an ISIN gets it from the ledger's names; one it can't
	// find is refused until the user says which it is (name_stock).
	trade := func(by map[string]any) (string, bool) {
		args := map[string]any{"account": "a1", "date": "2026-11-04", "side": "buy", "quantity": "1", "price": "10", "currency": "INR"}
		maps.Copy(args, by)
		return call("record_trade", args)
	}
	if msg, isErr := trade(map[string]any{"name": "INFOSYS LTD"}); isErr || !strings.Contains(msg, "recorded buy 1 INFOSYS LTD") {
		t.Errorf("a trade by name: %s", msg)
	}
	if msg, isErr := trade(map[string]any{"symbol": "ZZTEST", "exchange": "NSE"}); !isErr || !strings.Contains(msg, `no instrument found for \"ZZTEST\"`) {
		t.Errorf("an unknown symbol: %s", msg)
	}
	if msg, isErr := call("name_stock", map[string]any{"symbol": "ZZTEST", "isin": "INE009A01022"}); !isErr || !strings.Contains(msg, "not a valid ISIN") {
		t.Errorf("naming a bad ISIN: %s", msg)
	}
	if msg, isErr := call("name_stock", map[string]any{"symbol": "zztest", "isin": "INE009A01021"}); isErr || !strings.Contains(msg, "is now Infosys") {
		t.Errorf("name_stock: %s", msg)
	}
	if msg, isErr := trade(map[string]any{"symbol": "ZZTEST", "exchange": "NSE"}); isErr {
		t.Errorf("a named symbol: %s", msg)
	}

	// Another user sees none of it.
	other := connect("f2")
	if msg, _ := other("list_mail_senders", nil); msg != `{"senders":[]}` {
		t.Errorf("f2 sees f1's senders: %s", msg)
	}
	if msg, _ := other("list_holdings", nil); !strings.Contains(msg, `"holdings":[]`) {
		t.Errorf("f2 sees f1's holdings: %s", msg)
	}
	if msg, _ := other("list_accounts", nil); msg != `{"accounts":[]}` {
		t.Errorf("f2 sees f1's accounts: %s", msg)
	}
	if msg, isErr := other("record_trade", map[string]any{"account": "a1", "date": "2026-11-03", "isin": "INE009A01021",
		"side": "sell", "quantity": "1", "price": "1", "currency": "INR"}); !isErr {
		t.Errorf("f2 recorded into f1's account: %s", msg)
	}
	if msg, isErr := other("import_report", report("tradebook.csv", parsed(row("2026-10-10", "buy", "50", "1500")))); !isErr || !strings.Contains(msg, "no file") {
		t.Errorf("f2 imported f1's upload: %s", msg)
	}
}

func mustMul(a, b string) string {
	x, _ := finance.ParseDec(a)
	y, _ := finance.ParseDec(b)
	return finance.DecText(x.Mul(x, y), 8)
}
