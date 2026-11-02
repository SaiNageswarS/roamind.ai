package agent

import (
	_ "embed"
	"text/template"
)

//go:embed prompts/relate_mail.md
var relateMailTmpl string

var relateMailPrompt = template.Must(template.New("relate_mail").Parse(relateMailTmpl))

// MailMessage is a message as a Gmail account's Apps Script sends it
// (deploy/apps-script/Code.js), with the category it gave it.
type MailMessage struct {
	Address     string   `json:"address"` // the account the script runs in
	MessageID   string   `json:"message_id"`
	ThreadID    string   `json:"thread_id,omitempty"`
	Link        string   `json:"link,omitempty"` // the message in Gmail
	From        string   `json:"from,omitempty"`
	To          string   `json:"to,omitempty"`
	Cc          string   `json:"cc,omitempty"`
	Date        string   `json:"date,omitempty"` // RFC 3339
	Subject     string   `json:"subject,omitempty"`
	Category    string   `json:"category,omitempty"` // bill, travel, order, appointment, commitment, person, other
	Summary     string   `json:"summary,omitempty"`  // the script's one line
	Text        string   `json:"text,omitempty"`
	JSONLD      []string `json:"jsonld,omitempty"` // the schema.org scripts of its HTML
	Attachments []string `json:"attachments,omitempty"`
}

// MailRelating is what the agent is given to relate a mail to the user's
// memory.
type MailRelating struct {
	UserName  string
	UserEmail string
	Date      string // today, an ISO date
	Timezone  string
	Goals     []MailGoal // the user's active goals
	Message   MailMessage
}

// MailGoal is one of the user's active goals.
type MailGoal struct {
	Title     string
	Permalink string
}

// RelateMailPrompt is the turn text that has the agent find what a mail
// is about in memory and say what to save; run it with ToolsRecall and
// OutputSchema RelatedMailSchema.
func RelateMailPrompt(r MailRelating) (string, error) {
	return execute(relateMailPrompt, r)
}

// RelatedMailSchema is the structured output of relating a mail: the notes
// to write, or why there are none.
const RelatedMailSchema = `{
  "type": "object",
  "properties": {
    "skip": {"type": "string", "description": "Why nothing in the mail is worth saving; empty when there are items"},
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "kind": {"type": "string", "enum": ["task", "event", "person"]},
          "note": {"type": "string", "description": "The permalink of the existing note this updates, as memory gives it; empty for a new note"},
          "title": {"type": "string", "description": "A new note's title, as its schema says; for an update, the note's title"},
          "folder": {"type": "string", "description": "A new note's folder: tasks/ or tasks/<goal folder>/, events/<YYYY>/ (the year of its date), or people/"},
          "content": {"type": "string", "description": "The whole note after its frontmatter, in Basic Memory markdown: an opening line, ## Observations and ## Relations; for an update, the note as it was with the mail's changes"},
          "key": {"type": "string", "description": "Tasks and events: a stable key, the confirmation, order or invoice number when there is one, else the title; for an update, the item's key; empty for people"},
          "event_kind": {"type": "string", "enum": ["", "appointment", "travel", "stay", "ticket", "meeting", "delivery", "milestone", "other"], "description": "Events: the note's kind; empty otherwise"},
          "date": {"type": "string", "description": "A task's due or an event's date, ISO date or date-time (YYYY-MM-DD or YYYY-MM-DDTHH:MM) in the user's timezone; empty when unknown"},
          "remind_on": {"type": "string", "description": "ISO date to start reminding the user; always set for bills, three days before due; else empty"},
          "end": {"type": "string", "description": "ISO date or date-time an event ends, for stays and trips over several days; else empty"},
          "state": {"type": "string", "enum": ["open", "done", "cancelled"], "description": "From what the mail shows: done for a paid bill or a delivered order, cancelled for a cancelled booking"},
          "contact": {"type": "string", "description": "People: their email address; empty otherwise"}
        },
        "required": ["kind", "note", "title", "folder", "content", "key", "event_kind", "date", "remind_on", "end", "state", "contact"],
        "additionalProperties": false
      }
    }
  },
  "required": ["skip", "items"],
  "additionalProperties": false
}`

// RelatedMail is the structured output of RelatedMailSchema.
type RelatedMail struct {
	Skip  string        `json:"skip"`
	Items []RelatedItem `json:"items"`
}

// RelatedItem is a note to write from a mail, with its item row for tasks
// and events.
type RelatedItem struct {
	Kind      string `json:"kind"`
	Note      string `json:"note"`
	Title     string `json:"title"`
	Folder    string `json:"folder"`
	Content   string `json:"content"`
	Key       string `json:"key"`
	EventKind string `json:"event_kind"`
	Date      string `json:"date"`
	RemindOn  string `json:"remind_on"`
	End       string `json:"end"`
	State     string `json:"state"`
	Contact   string `json:"contact"`
}
