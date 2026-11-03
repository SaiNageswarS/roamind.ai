package agent

import (
	"bytes"
	_ "embed"
	"text/template"
)

//go:embed prompts/inbox_digest.md
var inboxDigestTmpl string

var inboxDigestPrompt = template.Must(template.New("inbox_digest").Parse(inboxDigestTmpl))

// InboxDigest is what the agent is given to write a user's inbox digest.
type InboxDigest struct {
	UserName string
	Date     string // today, an ISO date
	Weekday  string
	Items    []InboxItem
	// StaleMail are the user's Gmail accounts whose mail script sent
	// nothing for two days.
	StaleMail []string
	// UnsavedMail are the user's mails of the past week that were sent to
	// Roamind but not saved.
	UnsavedMail []InboxMail
}

// InboxMail is a mail that wasn't saved.
type InboxMail struct {
	Address string `json:"address"` // the Gmail account
	Subject string `json:"subject"`
	Link    string `json:"link"`
}

// InboxItem is one of the user's items that is due, from the items table.
type InboxItem struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`                 // task or event
	EventKind string `json:"event_kind,omitempty"` // an event's kind
	Title     string `json:"title"`
	Date      string `json:"date,omitempty"` // ISO date or date-time
	Note      string `json:"note"`           // the note's permalink
	Overdue   bool   `json:"overdue,omitempty"`
}

// InboxDigestPrompt is the turn text that has the agent write an inbox
// digest; run it with OutputSchema InboxDigestSchema.
func InboxDigestPrompt(d InboxDigest) (string, error) {
	var b bytes.Buffer
	err := inboxDigestPrompt.Execute(&b, d)
	return b.String(), err
}

// InboxDigestSchema is the structured output of an inbox digest.
const InboxDigestSchema = `{
  "type": "object",
  "properties": {
    "digest": {"type": "string", "description": "The digest in light markdown, as posted"}
  },
  "required": ["digest"],
  "additionalProperties": false
}`

// WrittenInbox is the structured output of InboxDigestSchema.
type WrittenInbox struct {
	Digest string `json:"digest"`
}
