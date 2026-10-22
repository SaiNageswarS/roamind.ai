package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/memory/store"
)

// eventKinds are the Event schema's kinds.
var eventKinds = []string{"appointment", "travel", "stay", "ticket", "meeting", store.DeliveryKind, "milestone", "other"}

// SaveItemInput is save_item's input.
type SaveItemInput struct {
	Key       string `json:"key" jsonschema:"a stable key: the confirmation, order or invoice number when there is one, else the note's title"`
	Kind      string `json:"kind" jsonschema:"the note type: task or event"`
	EventKind string `json:"event_kind,omitempty" jsonschema:"an event's kind, as in its note: appointment, travel, stay, ticket, meeting, delivery, milestone or other"`
	Note      string `json:"note" jsonschema:"the permalink of the task or event note"`
	Title     string `json:"title" jsonschema:"one line saying what it is"`
	Date      string `json:"date,omitempty" jsonschema:"the task's due or the event's date, an ISO date or date-time (YYYY-MM-DD or YYYY-MM-DDTHH:MM); omit when there is none or only the month is known"`
	RemindOn  string `json:"remind_on,omitempty" jsonschema:"ISO date from which to remind the user, when they ask for a reminder or a bill is due; otherwise the reminder starts the day before (on the day for deliveries)"`
	End       string `json:"end,omitempty" jsonschema:"ISO date or date-time an event ends, for stays and trips over several days"`
}

// CloseItemInput is close_item's input.
type CloseItemInput struct {
	Key   string `json:"key" jsonschema:"the item's key, as list_items shows it"`
	State string `json:"state" jsonschema:"done (also paid or delivered), cancelled, or open to reopen it"`
}

// ListItemsInput is list_items' input.
type ListItemsInput struct {
	State  string `json:"state,omitempty" jsonschema:"open, done or cancelled; open by default, any state with key"`
	From   string `json:"from,omitempty" jsonschema:"ISO date: only items dated on or after it"`
	To     string `json:"to,omitempty" jsonschema:"ISO date: only items dated on or before it"`
	Folder string `json:"folder,omitempty" jsonschema:"only items whose note is in this folder, such as tasks/<goal folder>/ for a goal's tasks"`
	Key    string `json:"key,omitempty" jsonschema:"only the item with this key"`
}

// ItemEntry is an item as list_items shows it.
type ItemEntry struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	EventKind string `json:"event_kind,omitempty"`
	Note      string `json:"note"`
	Title     string `json:"title"`
	Date      string `json:"date,omitempty"`
	ShowFrom  string `json:"remind_from,omitempty"`
	Until     string `json:"remind_until,omitempty"`
	State     string `json:"state"`
	Closed    string `json:"closed,omitempty"` // ISO date it was closed
}

// ListItemsOutput is list_items' result.
type ListItemsOutput struct {
	Items []ItemEntry `json:"items"`
}

// items adds the tools that track the state of the user's tasks and
// events: whether each is open, done or cancelled, and when to remind the
// user of it. Their notes in memory hold the details and no status.
func items(s *mcp.Server, d Deps) {
	st, userID := d.Store, d.UserID
	mcp.AddTool(s, &mcp.Tool{
		Name: "save_item",
		Description: "Track an active task or event note, a task still to do (dated or not) or an event still ahead: add it, " +
			"or change its dates when it moves. Tasks already done and past events need no item. Write or edit the note first, " +
			"then save its item with the note's permalink. The item holds the state " +
			"the note doesn't: it is open until close_item, and the user is reminded of it from the day before its date " +
			"(on the day for deliveries, from remind_on when given); open tasks stay until done.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in SaveItemInput) (*mcp.CallToolResult, Done, error) {
		it := store.Item{UserID: userID, Key: strings.TrimSpace(in.Key), Kind: in.Kind, EventKind: in.EventKind,
			Note: strings.TrimSpace(in.Note), Title: strings.TrimSpace(in.Title), Date: strings.TrimSpace(in.Date), Source: store.SourceChat}
		if err := oneOf("kind", it.Kind, store.ItemTask, store.ItemEvent); err != nil {
			return nil, Done{}, err
		}
		if it.Kind == store.ItemEvent && it.EventKind != "" {
			if err := oneOf("event_kind", it.EventKind, eventKinds...); err != nil {
				return nil, Done{}, err
			}
		}
		if err := check(it.Kind == store.ItemEvent || it.EventKind == "", "event_kind is for events only"); err != nil {
			return nil, Done{}, err
		}
		if err := check(it.Key != "" && it.Note != "" && it.Title != "", "key, note and title are required"); err != nil {
			return nil, Done{}, err
		}
		var err error
		it.ShowFrom, it.Until, err = store.InboxLeads(it.Kind, it.EventKind, it.Date, strings.TrimSpace(in.RemindOn), strings.TrimSpace(in.End))
		if err != nil {
			return nil, Done{}, err
		}
		if err := st.PutItem(ctx, it); err != nil {
			return nil, Done{}, err
		}
		msg := "saved " + it.Key + "; "
		switch {
		case it.ShowFrom == "":
			msg += "it is undated, so there is no reminder"
		case it.Until == "":
			msg += "reminded from " + it.ShowFrom + " until it is closed"
		default:
			msg += "reminded from " + it.ShowFrom + " to " + it.Until
		}
		return nil, Done{Message: msg}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "close_item",
		Description: "Close a task or event when it is done, paid or delivered (done) or called off (cancelled), " +
			"or reopen it (open). Its note doesn't change.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CloseItemInput) (*mcp.CallToolResult, Done, error) {
		if err := oneOf("state", in.State, store.ItemDone, store.ItemCancelled, store.ItemOpen); err != nil {
			return nil, Done{}, err
		}
		ok, err := st.CloseItem(ctx, userID, in.Key, in.State)
		if err != nil {
			return nil, Done{}, err
		}
		if !ok {
			return nil, Done{}, fmt.Errorf("no item %q; list_items shows the keys", in.Key)
		}
		return nil, Done{Message: in.Key + " is " + in.State}, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "list_items",
		Description: "List the user's tasks and events with their state and reminder dates, by date, undated last. " +
			"With no arguments, the open ones. Filter by state, a date range, a note folder or a key, to answer what's on " +
			"this week, which tasks of a goal are left, or whether an order arrived. Read an item's note for its details.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in ListItemsInput) (*mcp.CallToolResult, ListItemsOutput, error) {
		out := ListItemsOutput{Items: []ItemEntry{}}
		if in.State != "" {
			if err := oneOf("state", in.State, store.ItemOpen, store.ItemDone, store.ItemCancelled); err != nil {
				return nil, out, err
			}
		}
		for _, day := range []string{in.From, in.To} {
			if day == "" {
				continue
			}
			if _, err := store.ParseItemDate(day); err != nil || len(day) != len("2006-01-02") {
				return nil, out, fmt.Errorf("from and to are ISO dates (YYYY-MM-DD), not %q", day)
			}
		}
		items, err := st.Items(ctx, userID, store.ItemFilter{State: in.State, From: in.From, To: in.To,
			Folder: strings.TrimSpace(in.Folder), Key: in.Key})
		if err != nil {
			return nil, out, err
		}
		for _, it := range items {
			e := ItemEntry{Key: it.Key, Kind: it.Kind, EventKind: it.EventKind, Note: it.Note, Title: it.Title,
				Date: it.Date, ShowFrom: it.ShowFrom, Until: it.Until, State: it.State}
			if !it.ClosedAt.IsZero() {
				e.Closed = it.ClosedAt.Format("2006-01-02")
			}
			out.Items = append(out.Items, e)
		}
		return nil, out, nil
	})
}
