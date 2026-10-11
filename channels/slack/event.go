// Package slack adapts Slack Events API payloads (delivered through Hatchet's
// Slack webhook) and renders agent snapshots back into a Slack thread.
package slack

import (
	"regexp"
	"strings"
)

// Envelope is a Slack Events API event_callback payload.
type Envelope struct {
	Type     string `json:"type"` // event_callback
	TeamID   string `json:"team_id"`
	APIAppID string `json:"api_app_id"`
	EventID  string `json:"event_id"`
	Event    Event  `json:"event"`
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

var mentionRe = regexp.MustCompile(`<@[A-Z0-9]+>`)

// CleanText removes user mentions (the bot's own @-mention) from message text.
func CleanText(text string) string {
	return strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
}
