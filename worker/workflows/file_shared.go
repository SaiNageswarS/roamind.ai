package workflows

import (
	"context"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/channels/slack"
)

// FileSharedName is the slack-file-shared workflow: the fallback for a
// message with files whose message event never arrives. Slack sends a
// file_shared event for each file and, usually, the message event that
// starts a chat turn; sometimes it sends only file_shared (four PDFs posted
// within a minute got two message events). The fallback finds the file's
// message and starts its chat-turn run, which chat-turn skips when the
// message event already started one.
const FileSharedName = "slack-file-shared"

// FileSharedDelay gives the message event time to arrive and start its turn
// first, so the fallback finds the message handled and makes no Slack calls.
const FileSharedDelay = 20 * time.Second

// FileSharedOutput is recorded as the run's result in Hatchet.
type FileSharedOutput struct {
	File    string   `json:"file,omitempty"`
	Skipped string   `json:"skipped,omitempty"`
	Handled []string `json:"handled,omitempty"` // messages that had a turn
	Started []string `json:"started,omitempty"` // messages given a turn
}

// FileShared declares the slack-file-shared task; it starts chat runs.
func FileShared(c *hatchet.Client, d Deps, chat *hatchet.StandaloneTask) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(FileSharedName, func(ctx hatchet.Context, env slack.Envelope) (FileSharedOutput, error) {
		return d.fileShared(ctx, env, FileSharedDelay, func(ctx context.Context, env slack.Envelope) error {
			_, err := chat.RunNoWait(topLevel(ctx), env)
			return err
		})
	},
		hatchet.WithWorkflowEvents(slack.FileSharedEventKey),
		hatchet.WithWorkflowDescription("A chat turn for a shared file's message that Slack sent no message event for"),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
}

// fileShared waits delay, then starts a chat turn, with run, for each
// message the file was shared in, in the event's channel, that has had none.
func (d Deps) fileShared(ctx context.Context, env slack.Envelope, delay time.Duration, run func(context.Context, slack.Envelope) error) (FileSharedOutput, error) {
	ev := env.Event
	out := FileSharedOutput{File: ev.FileID}
	if ev.FileID == "" || ev.ChannelID == "" {
		out.Skipped = "no file or channel"
		return out, nil
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
		return out, ctx.Err()
	}
	f, _, _, err := d.Slack.GetFileInfoContext(ctx, ev.FileID, 0, 0)
	if err != nil {
		return out, err
	}
	channelType, shares := "group", f.Shares.Private[ev.ChannelID]
	if strings.HasPrefix(ev.ChannelID, "D") {
		channelType = "im"
	}
	if len(shares) == 0 {
		channelType, shares = "channel", f.Shares.Public[ev.ChannelID]
	}
	for _, sh := range shares {
		seen, err := d.Store.EventSeen(ctx, messageKey(ev.ChannelID, sh.Ts))
		if err != nil {
			return out, err
		}
		if seen {
			out.Handled = append(out.Handled, sh.Ts)
			continue
		}
		msg, err := d.sharedMessage(ctx, ev.ChannelID, sh)
		if err != nil {
			return out, err
		}
		if msg == nil {
			continue
		}
		chat, err := d.chatEnvelope(ctx, env, channelType, *msg)
		if err != nil {
			return out, err
		}
		if !chat.IsChat() {
			continue
		}
		if err := run(ctx, chat); err != nil {
			return out, err
		}
		out.Started = append(out.Started, sh.Ts)
	}
	return out, nil
}

// sharedMessage reads the message of a file share, or nil if it is gone.
func (d Deps) sharedMessage(ctx context.Context, channel string, sh slackapi.ShareFileInfo) (*slackapi.Message, error) {
	root := sh.ThreadTs
	if root == "" {
		root = sh.Ts
	}
	msgs, _, _, err := d.Slack.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
		ChannelID: channel, Timestamp: root, Oldest: sh.Ts, Latest: sh.Ts, Inclusive: true, Limit: 10,
	})
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if msgs[i].Timestamp == sh.Ts {
			return &msgs[i], nil
		}
	}
	return nil, nil
}

// chatEnvelope is the event chat-turn would have got for msg: a message in
// an IM or private channel, or the @mention of the bot where a message
// does not address the agent (route).
func (d Deps) chatEnvelope(ctx context.Context, env slack.Envelope, channelType string, msg slackapi.Message) (slack.Envelope, error) {
	ev := slack.Event{
		Type: "message", Subtype: msg.SubType, User: msg.User, BotID: msg.BotID, Text: msg.Text,
		Channel: env.Event.ChannelID, ChannelType: channelType, TS: msg.Timestamp, Team: msg.Team,
	}
	if msg.ThreadTimestamp != msg.Timestamp {
		ev.ThreadTS = msg.ThreadTimestamp
	}
	for _, f := range msg.Files {
		ev.Files = append(ev.Files, slack.File{ID: f.ID, Name: f.Name, Mimetype: f.Mimetype, Size: int64(f.Size), URLPrivateDownload: f.URLPrivateDownload})
	}
	mentioned := env.BotUserID() != "" && strings.Contains(msg.Text, "<@"+env.BotUserID())
	switch {
	case channelType == "channel" && mentioned:
		ev.Type = "app_mention"
	case channelType == "group" && mentioned:
		_, _, skip, err := d.route(ctx, ev)
		if err != nil {
			return slack.Envelope{}, err
		}
		if skip != "" {
			ev.Type = "app_mention"
		}
	}
	return slack.Envelope{
		Type: env.Type, TeamID: env.TeamID, APIAppID: env.APIAppID,
		Authorizations: env.Authorizations, Event: ev,
	}, nil
}
