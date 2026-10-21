package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Kinds of channel: what a channel is for.
const (
	// KindGoal is a goal's channel; its ref is the goal note's permalink.
	KindGoal = "goal"
	// KindNews is the user's news-shorts channel.
	KindNews = "news"
)

// Channel is a Slack channel the harness made for a user, for one purpose.
type Channel struct {
	UserID    string
	Kind      string
	Ref       string // the goal's permalink for KindGoal; "" otherwise
	Title     string // the goal's title for KindGoal; "" otherwise
	ChannelID string
	// SlackUserID is the user's Slack user, read from users.
	SlackUserID string
}

// Channel returns the user's channel for kind and ref, or "".
func (s *Store) Channel(ctx context.Context, userID, kind, ref string) (string, error) {
	var ch string
	err := s.db.QueryRowContext(ctx,
		`SELECT channel_id FROM channels WHERE user_id = ? AND kind = ? AND ref = ?`,
		userID, kind, ref).Scan(&ch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return ch, err
}

// ChannelOf returns what channelID is for, to the Slack user slackUserID,
// or nil when it is none of their channels.
func (s *Store) ChannelOf(ctx context.Context, channelID, slackUserID string) (*Channel, error) {
	c := Channel{ChannelID: channelID, SlackUserID: slackUserID}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.user_id, c.kind, c.ref, c.title
		FROM channels c JOIN users u ON u.id = c.user_id
		WHERE c.channel_id = ? AND u.slack_user_id = ?`, channelID, slackUserID).
		Scan(&c.UserID, &c.Kind, &c.Ref, &c.Title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &c, err
}

// Channels returns every channel of kind, oldest first.
func (s *Store) Channels(ctx context.Context, kind string) ([]Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.user_id, c.kind, c.ref, c.title, c.channel_id, COALESCE(u.slack_user_id, '')
		FROM channels c JOIN users u ON u.id = c.user_id
		WHERE c.kind = ?
		ORDER BY c.created_at, c.rowid`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.UserID, &c.Kind, &c.Ref, &c.Title, &c.ChannelID, &c.SlackUserID); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

// SaveChannel records a channel made for a user.
func (s *Store) SaveChannel(ctx context.Context, c Channel) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (user_id, kind, ref, title, channel_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		c.UserID, c.Kind, c.Ref, c.Title, c.ChannelID, time.Now().Unix())
	return err
}
