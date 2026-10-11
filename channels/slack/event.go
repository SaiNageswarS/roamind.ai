// Package slack adapts Slack Events API payloads (delivered through Hatchet's
// Slack webhook) and renders agent snapshots back into a Slack thread.
package slack

import (
	"context"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
)

// Envelope is a Slack Events API event_callback payload.
type Envelope struct {
	Type     string `json:"type"` // event_callback
	TeamID   string `json:"team_id"`
	APIAppID string `json:"api_app_id"`
	EventID  string `json:"event_id"`
	Event    Event  `json:"event"`
	// Authorizations name the installation the event was delivered for; its
	// user is the bot itself.
	Authorizations []Authorization `json:"authorizations,omitempty"`
}

// Authorization is one installation an event was delivered for.
type Authorization struct {
	UserID string `json:"user_id"`
	IsBot  bool   `json:"is_bot"`
}

// BotUserID returns the bot's own user id, or "" if the payload has none.
func (e Envelope) BotUserID() string {
	for _, a := range e.Authorizations {
		if a.IsBot {
			return a.UserID
		}
	}
	return ""
}

// Event is the inner event of an Envelope.
type Event struct {
	Type        string `json:"type"` // app_mention, message
	Subtype     string `json:"subtype,omitempty"`
	User        string `json:"user"`
	BotID       string `json:"bot_id,omitempty"`
	Text        string `json:"text"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type,omitempty"` // im, channel, group, mpim
	TS          string `json:"ts"`
	ThreadTS    string `json:"thread_ts,omitempty"`
	Team        string `json:"team,omitempty"`
	// Files are shared with the message (subtype file_share in DMs).
	Files []File `json:"files,omitempty"`
}

// ThreadTS is the timestamp of the thread root; a top-level message starts
// its own thread.
func (e Event) RootTS() string {
	if e.ThreadTS != "" {
		return e.ThreadTS
	}
	return e.TS
}

// EventKeyExpression is the CEL expression configured on the Hatchet Slack
// webhook. Chat messages (mentions, DMs to the bot and messages in private
// channels the bot is in, with or without shared files) get the key
// "slack:chat"; everything else, including the bot's own messages and
// edits, is routed to "slack:ignored:<type>" so it never starts a run.
// Private-channel messages are for goal channels; chat-turn skips them
// elsewhere, where only mentions address the agent.
const EventKeyExpression = `has(input.event) && !has(input.event.bot_id) && ` +
	`(!has(input.event.subtype) || input.event.subtype == 'file_share') && ` +
	`(input.event.type == 'app_mention' || (input.event.type == 'message' && has(input.event.channel_type) && (input.event.channel_type == 'im' || input.event.channel_type == 'group'))) ` +
	`? 'slack:chat' : 'slack:ignored:' + (has(input.event) ? input.event.type : input.type)`

// ChatEventKey is the event key chat-turn listens on.
const ChatEventKey = "slack:chat"

// IsChat mirrors EventKeyExpression in Go, as a second line of defence.
func (e Envelope) IsChat() bool {
	ev := e.Event
	if ev.BotID != "" || (ev.Subtype != "" && ev.Subtype != "file_share") || ev.User == "" {
		return false
	}
	return ev.Type == "app_mention" || (ev.Type == "message" && (ev.ChannelType == "im" || ev.ChannelType == "group"))
}

// TeamOf returns the team the event's user belongs to.
func (e Envelope) TeamOf() string {
	if e.Event.Team != "" {
		return e.Event.Team
	}
	return e.TeamID
}

var mentionRe = regexp.MustCompile(`<@([A-Z0-9]+)(?:\|[^>]*)?>`)

// UserAPI looks up the people mentioned in a message.
type UserAPI interface {
	GetUserInfoContext(ctx context.Context, user string, options ...slack.GetUserInfoOption) (*slack.User, error)
}

// CleanText removes the bot's own @-mention from message text and replaces
// mentions of other people with "@Name", so the agent knows who is meant.
// A mention whose user can't be looked up is kept as is.
func CleanText(ctx context.Context, api UserAPI, text, botUserID string) string {
	names := map[string]string{}
	text = mentionRe.ReplaceAllStringFunc(text, func(m string) string {
		id := mentionRe.FindStringSubmatch(m)[1]
		if id == botUserID {
			return ""
		}
		name, ok := names[id]
		if !ok {
			if info, err := api.GetUserInfoContext(ctx, id); err == nil {
				name = info.Profile.DisplayName
				if name == "" {
					name = info.RealName
				}
			}
			names[id] = name
		}
		if name == "" {
			return m
		}
		return "@" + name
	})
	return strings.TrimSpace(text)
}
