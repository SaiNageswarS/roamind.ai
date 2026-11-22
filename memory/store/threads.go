package store

import (
	"context"
	"errors"

	"github.com/SaiNageswarS/go-api-boot/odm"
	"github.com/SaiNageswarS/go-collection-boot/async"
)

// Thread maps a Slack thread to a claude session.
type Thread struct {
	ChannelID, ThreadTS string
	UserID              string
	ClaudeSessionID     string
}

// GetThread returns the thread or nil if it has never been seen.
func (s *Store) GetThread(ctx context.Context, channelID, threadTS string) (*Thread, error) {
	r, err := async.Await(s.threads.FindByID(ctx, rowID(channelID, threadTS)))
	if errors.Is(err, odm.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &Thread{ChannelID: r.ChannelID, ThreadTS: r.ThreadTS, UserID: r.UserID, ClaudeSessionID: r.ClaudeSessionID}, nil
}

// SaveThread records the claude session for a thread.
func (s *Store) SaveThread(ctx context.Context, t Thread) error {
	_, err := async.Await(s.threads.Save(ctx, threadRow{ChannelID: t.ChannelID, ThreadTS: t.ThreadTS, UserID: t.UserID, ClaudeSessionID: t.ClaudeSessionID}))
	return err
}
