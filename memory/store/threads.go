package store

import "context"

// Thread maps a Slack thread to a claude session.
type Thread struct {
	ChannelID, ThreadTS string
	UserID              string
	ClaudeSessionID     string
}

// GetThread returns the thread or nil if it has never been seen.
func (s *Store) GetThread(ctx context.Context, channelID, threadTS string) (*Thread, error) {
	r, err := find[threadRow](ctx, s, rowID(channelID, threadTS))
	if r == nil {
		return nil, err
	}
	return &Thread{ChannelID: r.ChannelID, ThreadTS: r.ThreadTS, UserID: r.UserID, ClaudeSessionID: r.ClaudeSessionID}, nil
}

// SaveThread records the claude session for a thread.
func (s *Store) SaveThread(ctx context.Context, t Thread) error {
	return save(ctx, s, threadRow{ChannelID: t.ChannelID, ThreadTS: t.ThreadTS, UserID: t.UserID, ClaudeSessionID: t.ClaudeSessionID})
}
