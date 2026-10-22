package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Item is the state of one of the user's tasks or events; its note in
// Basic Memory holds the details.
type Item struct {
	UserID    string
	Key       string // a stable title, or the confirmation, order or invoice number
	Kind      string // ItemTask or ItemEvent
	EventKind string // the Event's kind; "" for tasks
	Note      string // the note's permalink
	Title     string
	Date      string // ISO date or date-time; "" if undated
	ShowFrom  string // ISO date; "" never shows
	Until     string // ISO date; "" until it is closed
	// State is open, done or cancelled. PutItem keeps a row's state when
	// it is "", and opens new rows.
	State     string
	Source    string // SourceChat, or "mail:<address>/<message id>"
	UpdatedAt time.Time
	ClosedAt  time.Time // zero while open
}

// Kinds of Item: the note types they come from.
const (
	ItemTask  = "task"
	ItemEvent = "event"
)

// States of an Item.
const (
	ItemOpen      = "open"
	ItemDone      = "done"
	ItemCancelled = "cancelled"
)

// SourceChat is the Source of items saved in chat.
const SourceChat = "chat"

// DeliveryKind is the event kind of an order arriving.
const DeliveryKind = "delivery"

// isoDate is the layout of ISO dates.
const isoDate = "2006-01-02"

// dateLayouts are the ISO dates and date-times ParseItemDate accepts.
var dateLayouts = []string{isoDate, "2006-01-02T15:04", "2006-01-02T15:04:05", time.RFC3339}

// ParseItemDate returns the day of an ISO date or date-time.
func ParseItemDate(s string) (time.Time, error) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
		}
	}
	return time.Time{}, errors.New("not an ISO date or date-time: " + s)
}

// InboxLeads returns the first and last days an item shows as due, from
// its kind and dates, each an ISO date or date-time, or "":
//
//   - a task shows from remindOn, else the day before date, until it is
//     closed: overdue tasks stay;
//   - a delivery shows from remindOn, else on date, until date;
//   - any other event shows from remindOn, else the day before date, until
//     end, else date.
//
// An item with neither date nor remindOn never shows ("" showFrom); an
// event with no date shows until it is closed ("" until).
func InboxLeads(kind, eventKind, date, remindOn, end string) (showFrom, until string, err error) {
	var days [3]time.Time
	for i, s := range []string{date, remindOn, end} {
		if s == "" {
			continue
		}
		if days[i], err = ParseItemDate(s); err != nil {
			return "", "", err
		}
	}
	d, from, last := days[0], days[1], days[2]
	if from.IsZero() && !d.IsZero() {
		from = d
		if eventKind != DeliveryKind {
			from = d.AddDate(0, 0, -1)
		}
	}
	switch {
	case kind == ItemTask:
		last = time.Time{}
	case eventKind == DeliveryKind || last.IsZero():
		last = d
	}
	return dayOf(from), dayOf(last), nil
}

// dayOf is t's ISO date, or "" for the zero time.
func dayOf(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(isoDate)
}

// PutItem adds the user's item, or updates the one with its key: its kind,
// note, title, dates and source, and its state unless it.State is "".
func (s *Store) PutItem(ctx context.Context, it Item) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO items (user_id, key, kind, event_kind, note, title, date, show_from, until, state, source, updated_at, closed_at)
		VALUES (:user, :key, :kind, :event_kind, :note, :title, NULLIF(:date, ''), NULLIF(:show_from, ''), NULLIF(:until, ''),
			COALESCE(NULLIF(:state, ''), 'open'), :source, :now,
			CASE WHEN :state IN ('', 'open') THEN NULL ELSE :now END)
		ON CONFLICT (user_id, key) DO UPDATE SET
			kind = excluded.kind, event_kind = excluded.event_kind, note = excluded.note, title = excluded.title,
			date = excluded.date, show_from = excluded.show_from, until = excluded.until,
			source = excluded.source, updated_at = excluded.updated_at,
			state = COALESCE(NULLIF(:state, ''), items.state),
			closed_at = CASE
				WHEN :state IN ('', items.state) THEN items.closed_at
				WHEN :state = 'open' THEN NULL
				ELSE :now
			END`,
		sql.Named("user", it.UserID), sql.Named("key", strings.TrimSpace(it.Key)), sql.Named("kind", it.Kind),
		sql.Named("event_kind", it.EventKind), sql.Named("note", it.Note), sql.Named("title", it.Title),
		sql.Named("date", it.Date), sql.Named("show_from", it.ShowFrom), sql.Named("until", it.Until),
		sql.Named("state", it.State), sql.Named("source", it.Source), sql.Named("now", time.Now().Unix()))
	return err
}

// CloseItem sets the state of the user's item with key to done or
// cancelled, or reopens it with open; false when there is no such item.
func (s *Store) CloseItem(ctx context.Context, userID, key, state string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE items SET
			state = :state,
			updated_at = :now,
			closed_at = CASE
				WHEN :state = 'open' THEN NULL
				WHEN :state = state THEN closed_at
				ELSE :now
			END
		WHERE user_id = :user AND key = :key`,
		sql.Named("user", userID), sql.Named("key", strings.TrimSpace(key)), sql.Named("state", state),
		sql.Named("now", time.Now().Unix()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// itemColumns are the columns scanItems reads, in its order.
const itemColumns = `user_id, key, kind, event_kind, note, title, COALESCE(date, ''), COALESCE(show_from, ''),
	COALESCE(until, ''), state, source, updated_at, closed_at`

// DueItems returns the user's open items that show on day, by date.
func (s *Store) DueItems(ctx context.Context, userID string, day time.Time) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+itemColumns+` FROM items
		WHERE user_id = :user AND state = 'open'
			AND show_from <= :day AND (until IS NULL OR until >= :day)
		ORDER BY date IS NULL, date, title`,
		sql.Named("user", userID), sql.Named("day", day.Format(isoDate)))
	return scanItems(rows, err)
}

// ItemFilter selects items for Items; its zero value selects open items.
type ItemFilter struct {
	// State is open, done or cancelled; "" is open, or any state with Key.
	State string
	// From and To bound the items' dates, as ISO dates; undated items are
	// left out when either is set.
	From, To string
	// Folder is the start of the items' note permalinks, such as
	// tasks/<goal>/, with or without the user's memory project before it,
	// which Basic Memory puts at the start of permalinks.
	Folder string
	Key    string
}

// maxItems caps how many items Items returns.
const maxItems = 200

// Items returns the user's items that f selects, by date, undated last.
// Each filter left empty selects every item.
func (s *Store) Items(ctx context.Context, userID string, f ItemFilter) ([]Item, error) {
	if f.State == "" && f.Key == "" {
		f.State = ItemOpen
	}
	projectFolder := f.Folder
	if f.Folder != "" {
		u, err := s.UserByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		if u != nil && u.MemoryProject != "" {
			projectFolder = u.MemoryProject + "/" + f.Folder
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+itemColumns+` FROM items
		WHERE user_id = :user
			AND (:state = '' OR state = :state)
			AND (:key = '' OR key = :key)
			AND (:from = '' OR substr(date, 1, 10) >= :from)
			AND (:to = '' OR substr(date, 1, 10) <= :to)
			AND (:folder = ''
				OR substr(note, 1, length(:folder)) = :folder
				OR substr(note, 1, length(:project_folder)) = :project_folder)
		ORDER BY date IS NULL, date, title
		LIMIT :limit`,
		sql.Named("user", userID), sql.Named("state", f.State), sql.Named("key", strings.TrimSpace(f.Key)),
		sql.Named("from", f.From), sql.Named("to", f.To),
		sql.Named("folder", f.Folder), sql.Named("project_folder", projectFolder), sql.Named("limit", maxItems))
	return scanItems(rows, err)
}

// scanItems reads rows of itemColumns.
func scanItems(rows *sql.Rows, err error) ([]Item, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var updated int64
		var closed sql.NullInt64
		if err := rows.Scan(&it.UserID, &it.Key, &it.Kind, &it.EventKind, &it.Note, &it.Title, &it.Date, &it.ShowFrom,
			&it.Until, &it.State, &it.Source, &updated, &closed); err != nil {
			return nil, err
		}
		it.UpdatedAt = time.Unix(updated, 0)
		if closed.Valid {
			it.ClosedAt = time.Unix(closed.Int64, 0)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
