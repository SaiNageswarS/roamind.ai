package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Interest is a catalog interest: a topic with its curated feeds and the
// rubric its items are ranked by.
type Interest struct {
	ID      int64
	Name    string
	Sources []string // RSS/Atom feed URLs
	// Rubric is prose for the ranking: the criteria each item is scored on,
	// 0 to 10, when an item is not to be included at all, and optionally
	// the categories to file items under.
	Rubric string
	// Default is set for curated interests every user follows.
	Default bool
	// Owner is the user who added the interest; empty when it is curated.
	Owner string
}

// Curated reports whether the interest is part of the pre-built catalog.
func (in Interest) Curated() bool { return in.Owner == "" }

// UserInterest is an interest a user follows.
type UserInterest struct {
	Interest string
	Status   string // active or muted
}

// Interest statuses.
const (
	StatusActive = "active"
	StatusMuted  = "muted"
)

// ErrNoInterest is returned for an interest that is not in the user's
// catalog.
var ErrNoInterest = errors.New("no such interest in the catalog")

// AddInterest adds an interest of the user's own to their catalog; it
// fails when their catalog has one of the same name, ignoring case.
func (s *Store) AddInterest(ctx context.Context, userID string, in Interest) error {
	have, err := s.CatalogInterest(ctx, userID, in.Name)
	if err != nil {
		return err
	}
	if have != nil {
		return fmt.Errorf("%q is already in the catalog", have.Name)
	}
	sources, err := json.Marshal(in.Sources)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO interests (interest, owner, sources, rubric, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, in.Name, userID, string(sources), in.Rubric, now, now)
	return err
}

const interestColumns = `i.id, i.interest, i.owner, i.sources, i.rubric, i.is_default`

// Catalog returns the interests the user can follow: the curated ones and
// their own, by name.
func (s *Store) Catalog(ctx context.Context, userID string) ([]Interest, error) {
	return s.queryInterests(ctx, `
		SELECT `+interestColumns+` FROM interests i WHERE i.owner IN ('', ?) ORDER BY i.interest, i.owner`, userID)
}

// CatalogInterest returns the interest named name, ignoring case, in the
// user's catalog, their own first; nil when there is none.
func (s *Store) CatalogInterest(ctx context.Context, userID, name string) (*Interest, error) {
	ins, err := s.queryInterests(ctx, `
		SELECT `+interestColumns+` FROM interests i WHERE i.interest = ? AND i.owner IN ('', ?)
		ORDER BY i.owner DESC LIMIT 1`, name, userID)
	if err != nil || len(ins) == 0 {
		return nil, err
	}
	return &ins[0], nil
}

// DefaultInterests returns the curated interests every user follows, by
// name.
func (s *Store) DefaultInterests(ctx context.Context) ([]Interest, error) {
	return s.queryInterests(ctx, `
		SELECT `+interestColumns+` FROM interests i WHERE i.owner = '' AND i.is_default = 1 ORDER BY i.id`)
}

// ActiveInterests returns the interests the user follows and has not muted,
// in the order they followed them.
func (s *Store) ActiveInterests(ctx context.Context, userID string) ([]Interest, error) {
	return s.queryInterests(ctx, `
		SELECT `+interestColumns+`
		FROM user_interests u JOIN interests i ON i.id = u.interest_id
		WHERE u.user_id = ? AND u.status = 'active' ORDER BY u.created_at, u.rowid`, userID)
}

func (s *Store) queryInterests(ctx context.Context, query string, args ...any) ([]Interest, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Interest
	for rows.Next() {
		var in Interest
		var sources string
		if err := rows.Scan(&in.ID, &in.Name, &in.Owner, &sources, &in.Rubric, &in.Default); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(sources), &in.Sources); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// UserInterests returns the interests the user follows, muted ones
// included, in the order they followed them.
func (s *Store) UserInterests(ctx context.Context, userID string) ([]UserInterest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.interest, u.status FROM user_interests u JOIN interests i ON i.id = u.interest_id
		WHERE u.user_id = ? ORDER BY u.created_at, u.rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UserInterest
	for rows.Next() {
		var ui UserInterest
		if err := rows.Scan(&ui.Interest, &ui.Status); err != nil {
			return nil, err
		}
		out = append(out, ui)
	}
	return out, rows.Err()
}

// FollowInterest makes the user follow the interest of their catalog, or
// resumes it when muted. It returns ErrNoInterest when their catalog lacks
// it.
func (s *Store) FollowInterest(ctx context.Context, userID, name string) error {
	in, err := s.CatalogInterest(ctx, userID, name)
	if err != nil {
		return err
	}
	if in == nil {
		return ErrNoInterest
	}
	now := time.Now().Unix()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO user_interests (user_id, interest_id, status, created_at, updated_at)
		VALUES (?, ?, 'active', ?, ?)
		ON CONFLICT (user_id, interest_id) DO UPDATE SET status = 'active', updated_at = excluded.updated_at`,
		userID, in.ID, now, now)
	return err
}

// FollowDefaults makes the user follow the default interests they don't
// follow yet, leaving the others as they are, and returns the ones added.
func (s *Store) FollowDefaults(ctx context.Context, userID string) ([]string, error) {
	defaults, err := s.DefaultInterests(ctx)
	if err != nil {
		return nil, err
	}
	var added []string
	for _, in := range defaults {
		now := time.Now().Unix()
		res, err := s.db.ExecContext(ctx, `
			INSERT INTO user_interests (user_id, interest_id, status, created_at, updated_at)
			VALUES (?, ?, 'active', ?, ?) ON CONFLICT DO NOTHING`, userID, in.ID, now, now)
		if err != nil {
			return added, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			added = append(added, in.Name)
		}
	}
	return added, nil
}

// SetInterestStatus mutes or resumes an interest the user follows, and
// reports whether they follow it.
func (s *Store) SetInterestStatus(ctx context.Context, userID, name, status string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE user_interests SET status = ?, updated_at = ?
		WHERE user_id = ? AND interest_id IN (SELECT id FROM interests WHERE interest = ? AND owner IN ('', ?))`,
		status, time.Now().Unix(), userID, name, userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// URLHash identifies a link for dedupe: the SHA-256 of the URL without its
// fragment, utm_ parameters and trailing slash, with a lower-case scheme and
// host.
func URLHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		u.Scheme, u.Host, u.Fragment, u.RawFragment = strings.ToLower(u.Scheme), strings.ToLower(u.Host), "", ""
		q := u.Query()
		for k := range q {
			if strings.HasPrefix(strings.ToLower(k), "utm_") {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode()
		u.Path = strings.TrimSuffix(u.Path, "/")
		raw = u.String()
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Unsent returns the urls not yet sent to the user, in order.
func (s *Store) Unsent(ctx context.Context, userID string, urls []string) ([]string, error) {
	var out []string
	for _, u := range urls {
		var one int
		err := s.db.QueryRowContext(ctx,
			`SELECT 1 FROM sent_urls WHERE user_id = ? AND url_hash = ?`, userID, URLHash(u)).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			out = append(out, u)
			continue
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SentStory is a story sent to a user, with the title of the item sent for
// it.
type SentStory struct {
	Story, Title string
}

// SentStoriesSince returns the stories sent to the user since t, newest
// first.
func (s *Store) SentStoriesSince(ctx context.Context, userID string, t time.Time) ([]SentStory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT story, title FROM sent_stories WHERE user_id = ? AND sent_at >= ? ORDER BY sent_at DESC, rowid DESC`, userID, t.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SentStory
	for rows.Next() {
		var st SentStory
		if err := rows.Scan(&st.Story, &st.Title); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func markStoriesSent(ctx context.Context, db execer, userID string, stories []SentStory) error {
	now := time.Now().Unix()
	for _, st := range stories {
		if st.Story == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sent_stories (user_id, story, title, sent_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id, story) DO UPDATE SET title = excluded.title, sent_at = excluded.sent_at`,
			userID, st.Story, st.Title, now); err != nil {
			return err
		}
	}
	return nil
}

func markSent(ctx context.Context, db execer, userID string, urls []string) error {
	now := time.Now().Unix()
	for _, u := range urls {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sent_urls (user_id, url_hash, sent_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
			userID, URLHash(u), now); err != nil {
			return err
		}
	}
	return nil
}
