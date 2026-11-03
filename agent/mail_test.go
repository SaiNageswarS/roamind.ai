package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRelateMailPrompt(t *testing.T) {
	if !json.Valid([]byte(RelatedMailSchema)) {
		t.Fatal("schema is not JSON")
	}
	r := MailRelating{UserName: "Sai", UserEmail: "sai@x.com", Date: "2026-11-02", Timezone: "Europe/Berlin",
		Goals: []MailGoal{{Title: "Plan parents' return travel", Permalink: "bm-u-sai-1/goals/plan-parents-return-travel"}},
		Message: MailMessage{Address: "sai@gmail.com", From: "Etihad <no-reply@etihad.com>", To: "sai@gmail.com",
			Date: "2026-11-02T08:00:00Z", Subject: "Your flight has changed", Link: "https://mail.google.com/mail/u/0/#all/18c1",
			Category: "travel", Summary: "EY 134 moved", Text: "New departure 10:15.", JSONLD: []string{`{"@type":"FlightReservation"}`},
			Attachments: []string{"eticket.pdf"}}}
	p, err := RelateMailPrompt(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Gmail account sai@gmail.com", "in Europe/Berlin", "- Plan parents' return travel (bm-u-sai-1/goals/plan-parents-return-travel)",
		"sorted it as travel: EY 134 moved", "<mail>\nFrom: Etihad <no-reply@etihad.com>\nTo: sai@gmail.com\nDate:",
		"Attachments: eticket.pdf;", "New departure 10:15.\n</mail>", "<schema-org>\n{\"@type\":\"FlightReservation\"}\n</schema-org>",
		"(observed, email, 2026-11-02)"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Cc:") {
		t.Errorf("prompt has an empty Cc:\n%s", p)
	}
	r.Goals, r.Message.Summary = nil, ""
	if p, _ := RelateMailPrompt(r); strings.Contains(p, "active goals") || !strings.Contains(p, "sorted it as travel\n") {
		t.Errorf("prompt without goals or summary:\n%s", p)
	}
}
