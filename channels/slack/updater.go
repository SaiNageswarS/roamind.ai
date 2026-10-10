package slack

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/slack-go/slack"

	"roamind.ai/protocol"
)

// API is the subset of the Slack client the updater needs.
type API interface {
	PostMessageContext(ctx context.Context, channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessageContext(ctx context.Context, channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
}

// UpdateInterval throttles chat.update calls (Slack tier 3 is ~50/min).
const UpdateInterval = 1200 * time.Millisecond

// Updater streams a turn into a thread by editing a placeholder message,
// spilling into additional thread replies when the answer outgrows one
// message.
type Updater struct {
	api      API
	channel  string
	threadTS string

	mu       sync.Mutex
	ts       []string // message timestamps, first is the placeholder
	sent     []string // last text sent per message
	snap     protocol.Snapshot
	dirty    bool
	lastSend time.Time

	stop chan struct{}
	done chan struct{}
}

// Placeholder is shown until the first tokens arrive.
const Placeholder = "_…thinking_"

// NewUpdater posts the placeholder in the thread and starts the flush loop.
func NewUpdater(ctx context.Context, api API, channel, threadTS string) (*Updater, error) {
	_, ts, err := api.PostMessageContext(ctx, channel,
		slack.MsgOptionText(Placeholder, false), slack.MsgOptionTS(threadTS))
	if err != nil {
		return nil, err
	}
	u := &Updater{
		api: api, channel: channel, threadTS: threadTS,
		ts: []string{ts}, sent: []string{Placeholder},
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	go u.loop(ctx)
	return u, nil
}

// Apply folds a packet into the snapshot; the flush loop sends it.
func (u *Updater) Apply(p protocol.Packet) {
	u.mu.Lock()
	u.snap.Apply(p)
	u.dirty = true
	u.mu.Unlock()
}

// Snapshot returns a copy of the current state.
func (u *Updater) Snapshot() protocol.Snapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.snap
}

func (u *Updater) loop(ctx context.Context) {
	defer close(u.done)
	t := time.NewTicker(UpdateInterval)
	defer t.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			u.flush(ctx, false)
		}
	}
}

// Finish stops streaming and writes the final message, with a Sources block
// and an optional trailing note.
func (u *Updater) Finish(ctx context.Context, note string) {
	close(u.stop)
	<-u.done
	u.mu.Lock()
	if note != "" {
		u.snap.Text += "\n\n_" + note + "_"
	}
	u.dirty = true
	u.mu.Unlock()
	u.flush(ctx, true)
}

func (u *Updater) flush(ctx context.Context, final bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.dirty {
		return
	}
	u.dirty = false
	body := RenderBody(u.snap)
	if body == "" {
		if !final {
			return
		}
		body = "_(no response)_"
	}
	chunks := Split(body, MaxChunk)
	var sources []slack.Block
	if final {
		sources = SourcesBlocks(u.snap)
	}
	for i, chunk := range chunks {
		last := i == len(chunks)-1
		opts := []slack.MsgOption{slack.MsgOptionText(chunk, false), slack.MsgOptionDisableLinkUnfurl()}
		if last && len(sources) > 0 {
			section := slack.NewSectionBlock(slack.NewTextBlockObject(slack.MarkdownType, chunk, false, false), nil, nil)
			opts = append(opts, slack.MsgOptionBlocks(append([]slack.Block{section}, sources...)...))
		}
		if i < len(u.ts) {
			if u.sent[i] == chunk && !(last && len(sources) > 0) {
				continue
			}
			if _, _, _, err := u.api.UpdateMessageContext(ctx, u.channel, u.ts[i], opts...); err != nil {
				slog.Warn("slack update failed", "err", err, "channel", u.channel)
				u.dirty = true
				return
			}
			u.sent[i] = chunk
			continue
		}
		opts = append(opts, slack.MsgOptionTS(u.threadTS))
		_, ts, err := u.api.PostMessageContext(ctx, u.channel, opts...)
		if err != nil {
			slog.Warn("slack post failed", "err", err, "channel", u.channel)
			u.dirty = true
			return
		}
		u.ts = append(u.ts, ts)
		u.sent = append(u.sent, chunk)
	}
	u.lastSend = time.Now()
}
