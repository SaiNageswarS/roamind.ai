package agent

import (
	_ "embed"
	"text/template"
)

//go:embed prompts/extract_finance.md
var extractFinanceTmpl string

var extractFinancePrompt = template.Must(template.New("extract_finance").Parse(extractFinanceTmpl))

// ExtractModel is the model that reads trades from broker mail: a small
// one, as it needs no memory or tools.
const ExtractModel = "claude-haiku-5-5"

// FinanceExtracting is what the agent is given to read trades from a
// broker's mail.
type FinanceExtracting struct {
	UserName string
	// Accounts are the user's brokerage accounts: "<id> <broker> <number>".
	Accounts []string
	Message  MailMessage
}

// ExtractFinancePrompt is the turn text that reads trades from a broker's
// mail; run it with ToolsNone, Model ExtractModel and OutputSchema
// ExtractedTradesSchema.
func ExtractFinancePrompt(e FinanceExtracting) (string, error) {
	return execute(extractFinancePrompt, e)
}

// ExtractedTradesSchema is the structured output of reading a broker's
// mail: its account and rows, or why it has none.
const ExtractedTradesSchema = `{
  "type": "object",
  "properties": {
    "skip": {"type": "string", "description": "Why the mail reports no trade or dividend done; empty when it has rows"},
    "broker": {"type": "string", "description": "The broker or platform that sent it"},
    "account": {"type": "string", "description": "The id of the user's account it is about, from their list; empty when none matches"},
    "account_number": {"type": "string", "description": "The account, client or demat number as the mail shows it, masked digits and all; empty when it shows none"},
    "doc": {"type": "string", "enum": ["contract_note", "confirmation", "statement"]},
    "rows": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "date": {"type": "string", "description": "The trade date, ISO"},
          "isin": {"type": "string", "description": "As the mail shows it; empty when it shows none"},
          "symbol": {"type": "string"},
          "exchange": {"type": "string"},
          "name": {"type": "string"},
          "side": {"type": "string", "enum": ["buy", "sell", "vest", "espp", "dividend", "transfer_in", "transfer_out"]},
          "quantity": {"type": "string"},
          "price": {"type": "string"},
          "amount": {"type": "string"},
          "fees": {"type": "string"},
          "tax_withheld": {"type": "string"},
          "currency": {"type": "string"},
          "ref": {"type": "string", "description": "Every order and trade number of the row, space-separated"}
        },
        "required": ["date", "isin", "symbol", "exchange", "name", "side", "quantity", "price", "amount", "fees", "tax_withheld", "currency", "ref"],
        "additionalProperties": false
      }
    }
  },
  "required": ["skip", "broker", "account", "account_number", "doc", "rows"],
  "additionalProperties": false
}`

// ExtractedTrades is the structured output of ExtractedTradesSchema.
type ExtractedTrades struct {
	Skip          string         `json:"skip"`
	Broker        string         `json:"broker"`
	Account       string         `json:"account"`
	AccountNumber string         `json:"account_number"`
	Doc           string         `json:"doc"`
	Rows          []ExtractedRow `json:"rows"`
}

// ExtractedRow is a trade or dividend read from a mail.
type ExtractedRow struct {
	Date        string `json:"date"`
	ISIN        string `json:"isin"`
	Symbol      string `json:"symbol"`
	Exchange    string `json:"exchange"`
	Name        string `json:"name"`
	Side        string `json:"side"`
	Quantity    string `json:"quantity"`
	Price       string `json:"price"`
	Amount      string `json:"amount"`
	Fees        string `json:"fees"`
	TaxWithheld string `json:"tax_withheld"`
	Currency    string `json:"currency"`
	Ref         string `json:"ref"`
}
