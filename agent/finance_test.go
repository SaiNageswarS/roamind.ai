package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractFinancePrompt(t *testing.T) {
	p, err := ExtractFinancePrompt(FinanceExtracting{UserName: "Sam", Accounts: []string{"a1 broker XX1234"},
		Message: MailMessage{Address: "sam@x.com", From: "Broker <notices@broker.example>", Subject: "Contract note",
			Text: "Bought 50 INFY", PDFs: []MailPDF{{Name: "cn.pdf", Text: "Trade 1"}, {Name: "locked.pdf", Error: "password-protected"}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- a1 broker XX1234", "Bought 50 INFY", `<attachment name="cn.pdf">` + "\nTrade 1", "(no text: password-protected)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(ExtractedTradesSchema), &schema); err != nil {
		t.Fatal(err)
	}
}
