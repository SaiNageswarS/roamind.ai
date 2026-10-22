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
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO items (user_id, key, kind, event_kind, note, title, date, show_from, until, state, source, updated_at, closed_at)
		VALUES (?1, ?2, ?3, ?4, ?5, ?6, NULLIF(?7, ''), NULLIF(?8, ''), NULLIF(?9, ''), COALESCE(NULLIF(?10, ''), 'open'), ?11, ?12,
			CASE WHEN COALESCE(NULLIF(?10, ''), 'open') = 'open' THEN NULL ELSE ?12 END)
		ON CONFLICT (user_id, key) DO UPDATE SET
			kind = excluded.kind, event_kind = excluded.event_kind, note = excluded.note, title = excluded.title,
			date = excluded.date, show_from = excluded.show_from, until = excluded.until,
			source = excluded.source, updated_at = excluded.updated_at,
			closed_at = CASE
				WHEN ?10 = '' OR ?10 = items.state THEN items.closed_at
				WHEN ?10 = 'open' THEN NULL
				ELSE ?12 END,
			state = CASE WHEN ?10 = '' THEN items.state ELSE ?10 END`,
		it.UserID, strings.TrimSpace(it.Key), it.Kind, it.EventKind, it.Note, it.Title,
		it.Date, it.ShowFrom, it.Until, it.State, it.Source, now)
	return err
}

// CloseItem sets the state of the user's item with key to done or
// cancelled, or reopens it with open; false when there is no such item.
func (s *Store) CloseItem(ctx context.Context, userID, key, state string) (bool, error) {
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		UPDATE items SET
			closed_at = CASE WHEN ?3 = 'open' THEN NULL WHEN state = ?3 THEN closed_at ELSE ?4 END,
			state = ?3, updated_at = ?4
		WHERE user_id = ?1 AND key = ?2`, userID, strings.TrimSpace(key), state, now)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DueItems returns the user's open items that show on day, by date.
func (s *Store) DueItems(ctx context.Context, userID string, day time.Time) ([]Item, error) {
	d := day.Format(isoDate)
	return s.items(ctx, `user_id = ? AND state = 'open' AND show_from <= ? AND (until IS NULL OR until >= ?)`, userID, d, d)
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
func (s *Store) Items(ctx context.Context, userID string, f ItemFilter) ([]Item, error) {
	where, args := []string{"user_id = ?"}, []any{userID}
	if f.State == "" && f.Key == "" {
		f.State = ItemOpen
	}
	if f.State != "" {
		where, args = append(where, "state = ?"), append(args, f.State)
	}
	if f.Key != "" {
		where, args = append(where, "key = ?"), append(args, strings.TrimSpace(f.Key))
	}
	if f.From != "" {
		where, args = append(where, "substr(date, 1, 10) >= ?"), append(args, f.From)
	}
	if f.To != "" {
		where, args = append(where, "substr(date, 1, 10) <= ?"), append(args, f.To)
	}
	if f.Folder != "" {
		u, err := s.UserByID(ctx, userID)
		if err != nil {
			return nil, err
		}
		folders := []string{f.Folder}
		if u != nil && u.MemoryProject != "" && !strings.HasPrefix(f.Folder, u.MemoryProject+"/") {
			folders = append(folders, u.MemoryProject+"/"+f.Folder)
		}
		var or []string
		for _, folder := range folders {
			or, args = append(or, "substr(note, 1, length(?)) = ?"), append(args, folder, folder)
		}
		where = append(where, "("+strings.Join(or, " OR ")+")")
	}
	return s.items(ctx, strings.Join(where, " AND "), args...)
}

func (s *Store) items(ctx context.Context, where string, args ...any) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, key, kind, event_kind, note, title, COALESCE(date, ''), COALESCE(show_from, ''),
			COALESCE(until, ''), state, source, updated_at, closed_at
		FROM items WHERE `+where+`
		ORDER BY date IS NULL, date, title LIMIT ?`, append(args, maxItems)...)
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
