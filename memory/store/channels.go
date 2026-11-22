package store

import (
	"context"
	"database/sql"
	"errors"
)

// Kinds of channel: what a channel is for.
const (
	// KindGoal is a goal's channel; its ref is the goal note's permalink.
	KindGoal = "goal"
	// KindNews is the user's news-shorts channel.
	KindNews = "news"
	// KindInbox is the user's inbox channel, where their daily inbox
	// digest is posted.
	KindInbox = "inbox"
	// KindFinance is the user's finance channel, where they upload reports
	// and ask about their holdings.
	KindFinance = "finance"
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
	r, err := find[channelRow](ctx, s, rowID(userID, kind, ref))
	if r == nil {
		return "", err
	}
	return r.ChannelID, nil
}

// ChannelOf returns what channelID is for, to the Slack user slackUserID,
// or nil when it is none of their channels.
func (s *Store) ChannelOf(ctx context.Context, channelID, slackUserID string) (*Channel, error) {
	c := Channel{ChannelID: channelID, SlackUserID: slackUserID}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.user_id, c.kind, c.ref, c.title
		FROM channels c JOIN users u ON u.id = c.user_id
		WHERE c.channel_id = $1 AND u.slack_user_id = $2`, channelID, slackUserID).
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
		WHERE c.kind = $1
		ORDER BY c.created_on, c.seq`, kind)
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
	// insert, not save: a channel saved twice is a bug to see.
	return insert(ctx, s, channelRow{UserID: c.UserID, Kind: c.Kind, Ref: c.Ref, Title: c.Title, ChannelID: c.ChannelID})
}
