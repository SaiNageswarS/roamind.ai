Write {{.UserName}}'s inbox digest for today, {{.Weekday}} {{.Date}}: the
morning message in their inbox channel on what needs their attention.

These are their open tasks and events that are due, from the items table,
each with its key, its kind, its date and the permalink of its note:
{{range .Items}}
- {{.Title}} (key: {{.Key}}; {{.Kind}}{{if .EventKind}}, {{.EventKind}}{{end}}{{if .Date}}; {{.Date}}{{end}}{{if .Overdue}}; overdue{{end}}; note: {{.Note}})
{{- end}}

Read each item's note with read_note, and its related notes with
build_context where they help, for what {{.UserName}} needs to act on it:
the time and place, who it is with, the booking reference, the amount of a
bill, the goal a task serves, and for an event its checklist, together with
their preferences for that kind of event. This turn only reads memory; the
items change when {{.UserName}} replies.

Group the items under Overdue, Today and Coming up, in that order, leaving
out empty groups: a task past its date is overdue, an item dated today or
undated is today's, and later ones are coming up. Give each item one bullet:
what it is and when, then what {{.UserName}} needs for it in a line or two,
with the checklist as a short list where there is one. End each bullet with
the item's key in italics, so a reply can name it. Close with one line
saying they can reply in this thread to mark something done or move it.

Write the digest in light markdown, exactly as {{.UserName}} will see it in
Slack, starting with a bold heading with today's date, and return it.
