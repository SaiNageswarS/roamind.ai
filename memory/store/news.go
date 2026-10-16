package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NewsChannelOf returns the user's news channel, or "".
func (s *Store) NewsChannelOf(ctx context.Context, userID string) (string, error) {
	var ch string
	err := s.db.QueryRowContext(ctx, `SELECT channel_id FROM news_channels WHERE user_id = ?`, userID).Scan(&ch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ch, err
}

// IsNewsChannelOf reports whether channelID is the news channel of the
// Slack user slackUserID.
func (s *Store) IsNewsChannelOf(ctx context.Context, channelID, slackUserID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM news_channels n JOIN users u ON u.id = n.user_id
		WHERE n.channel_id = ? AND u.slack_user_id = ?`, channelID, slackUserID).Scan(&n)
	return n > 0, err
}

// SaveNewsChannel records the user's news channel.
func (s *Store) SaveNewsChannel(ctx context.Context, userID, channelID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO news_channels (user_id, channel_id, created_at) VALUES (?, ?, ?)`,
		userID, channelID, time.Now().Unix())
	return err
}

// Digest is one news digest posted in a user's news channel.
type Digest struct {
	ID            int64
	UserID        string
	ChannelID, TS string
	Slot          string // local "YYYY-MM-DD HH"
	Text          string // the items as posted
	PostedAt      time.Time
}

// DigestItem is one item of a digest.
type DigestItem struct {
	ID, DigestID         int64
	Interest, Title, URL string
	// Story is the ranking's slug for the item's story; it is recorded as
	// sent with the digest, not kept on the item.
	Story string
}

// DigestPosted reports whether the user's digest for slot is posted.
func (s *Store) DigestPosted(ctx context.Context, userID, slot string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM digests WHERE user_id = ? AND slot = ?`, userID, slot).Scan(&n)
	return n > 0, err
}

// ReserveDigest reserves the user's digest slot before the digest is
// posted, and returns the digest's id; ok is false when the slot is already
// reserved or posted. Reserving first means a run that dies after posting
// can't be replayed into a second post.
func (s *Store) ReserveDigest(ctx context.Context, userID, channelID, slot string) (id int64, ok bool, err error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO digests (user_id, channel_id, slot, posted_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, slot) DO NOTHING`, userID, channelID, slot, time.Now().Unix())
	if err != nil {
		return 0, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return 0, false, err
	}
	id, err = res.LastInsertId()
	return id, err == nil, err
}

// ReleaseDigest gives back a reserved slot whose digest was not posted.
func (s *Store) ReleaseDigest(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM digests WHERE id = ? AND ts IS NULL`, id)
	return err
}

// CompleteDigest records a reserved digest as posted, in one transaction:
// its message, its items, in order, and their links and stories as sent to
// the user.
func (s *Store) CompleteDigest(ctx context.Context, d Digest, items []DigestItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE digests SET ts = ?, text = ?, posted_at = ? WHERE id = ?`,
		d.TS, d.Text, d.PostedAt.Unix(), d.ID); err != nil {
		return err
	}
	urls := make([]string, len(items))
	stories := make([]SentStory, len(items))
	for i, it := range items {
		urls[i], stories[i] = it.URL, SentStory{Story: it.Story, Title: it.Title}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO digest_items (digest_id, interest, title, url) VALUES (?, ?, ?, ?)`,
			d.ID, it.Interest, it.Title, it.URL); err != nil {
			return err
		}
	}
	if err := markSent(ctx, tx, d.UserID, urls); err != nil {
		return err
	}
	if err := markStoriesSent(ctx, tx, d.UserID, stories); err != nil {
		return err
	}
	return tx.Commit()
}

// DigestByTS returns the digest posted as the message channelID/ts, or nil.
func (s *Store) DigestByTS(ctx context.Context, channelID, ts string) (*Digest, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, slot, text, posted_at FROM digests
		WHERE channel_id = ? AND ts = ?`, channelID, ts)
	d := Digest{ChannelID: channelID, TS: ts}
	var posted int64
	err := row.Scan(&d.ID, &d.UserID, &d.Slot, &d.Text, &posted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	d.PostedAt = time.Unix(posted, 0)
	return &d, err
}

// RecentDigestItems returns the items posted to the user since, newest
// first.
func (s *Store) RecentDigestItems(ctx context.Context, userID string, since time.Time) ([]DigestItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.digest_id, i.interest, i.title, i.url
		FROM digest_items i JOIN digests d ON d.id = i.digest_id
		WHERE d.user_id = ? AND d.posted_at >= ? ORDER BY d.posted_at DESC, i.id`, userID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []DigestItem
	for rows.Next() {
		var it DigestItem
		if err := rows.Scan(&it.ID, &it.DigestID, &it.Interest, &it.Title, &it.URL); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Signal is one piece of feedback on a digest.
type Signal struct {
	UserID   string
	DigestID int64
	Kind     string // reaction or reply
	Value    int    // +1, 0 or -1
}

// SaveSignal records feedback.
func (s *Store) SaveSignal(ctx context.Context, sg Signal) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO digest_signals (user_id, digest_id, kind, value, at) VALUES (?, ?, ?, ?, ?)`,
		sg.UserID, sg.DigestID, sg.Kind, sg.Value, time.Now().Unix())
	return err
}

// InterestSignal sums up how the user took to one interest's items.
type InterestSignal struct {
	Interest string
	Shown    int // items posted
	Liked    int // positive reactions and replies on digests with its items
	Disliked int // negative reactions on digests with its items
	// LikedTitles are the titles of its items in liked digests, newest
	// first.
	LikedTitles []string
}

// InterestSignals sums up the feedback on the items posted to the user
// since, per interest. A reaction or reply on a digest counts for the
// interest of each of its items.
func (s *Store) InterestSignals(ctx context.Context, userID string, since time.Time) ([]InterestSignal, error) {
	items, err := s.RecentDigestItems(ctx, userID, since)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT digest_id, value FROM digest_signals WHERE user_id = ? AND at >= ?`, userID, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	liked, disliked := map[int64]int{}, map[int64]int{}
	for rows.Next() {
		var digest int64
		var v int
		if err := rows.Scan(&digest, &v); err != nil {
			return nil, err
		}
		switch {
		case v > 0:
			liked[digest]++
		case v < 0:
			disliked[digest]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	by := map[string]*InterestSignal{}
	var order []string
	for _, it := range items {
		is := by[it.Interest]
		if is == nil {
			is = &InterestSignal{Interest: it.Interest}
			by[it.Interest] = is
			order = append(order, it.Interest)
		}
		is.Shown++
		is.Liked += liked[it.DigestID]
		is.Disliked += disliked[it.DigestID]
		if liked[it.DigestID] > 0 {
			is.LikedTitles = append(is.LikedTitles, it.Title)
		}
	}
	out := make([]InterestSignal, 0, len(order))
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out, nil
}
