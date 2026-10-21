package workflows

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	slackapi "github.com/slack-go/slack"

	"roamind.ai/memory/store"
)

// openChannel returns the user's channel for c's kind and ref, creating it
// when there is none: a private channel named name (or name-2, name-3, …
// when that is taken), recorded before anything else, so a retry never
// creates a second one, then introduced with intro. The user is invited
// either way, so a retry makes up a failed invite. created is the new
// channel's name, or "" when it existed.
func (d Deps) openChannel(ctx context.Context, u *store.User, c store.Channel, name, intro string) (id, created string, err error) {
	if id, err = d.Store.Channel(ctx, u.ID, c.Kind, c.Ref); err != nil {
		return "", "", err
	}
	var ch *slackapi.Channel
	if id == "" {
		if ch, err = d.createChannel(ctx, name); err != nil {
			return "", "", err
		}
		c.UserID, c.ChannelID = u.ID, ch.ID
		if err := d.Store.SaveChannel(ctx, c); err != nil {
			return "", "", err
		}
		id = ch.ID
	}
	if _, err := d.Slack.InviteUsersToConversationContext(ctx, id, u.SlackUserID); err != nil && !isSlackErr(err, "already_in_channel") {
		return "", "", fmt.Errorf("invite: %w", err)
	}
	if ch == nil {
		return id, "", nil
	}
	if _, _, err := d.Slack.PostMessageContext(ctx, id, slackapi.MsgOptionText(intro, false)); err != nil {
		return "", "", fmt.Errorf("intro: %w", err)
	}
	return id, ch.Name, nil
}

// createChannel creates a private channel named base, or base-2, base-3, …
// when the name is taken.
func (d Deps) createChannel(ctx context.Context, base string) (*slackapi.Channel, error) {
	for n := 1; n <= 20; n++ {
		name := base
		if n > 1 {
			name = fmt.Sprintf("%s-%d", base, n)
		}
		ch, err := d.Slack.CreateConversationContext(ctx, slackapi.CreateConversationParams{ChannelName: name, IsPrivate: true})
		if isSlackErr(err, "name_taken") {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create channel %s: %w", name, err)
		}
		return ch, nil
	}
	return nil, fmt.Errorf("no free channel name for %s", base)
}

var nonName = regexp.MustCompile(`[^a-z0-9]+`)

// channelName is prefix-<slug of s>, the slug cut to max characters, or
// prefix alone when s has nothing to slug. Channel names are unique across
// the workspace, private ones included, and at most 80 characters.
func channelName(prefix, s string, max int) string {
	s = strings.Trim(nonName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > max {
		s = strings.TrimRight(s[:max], "-")
	}
	if s == "" {
		return prefix
	}
	return prefix + "-" + s
}

func isSlackErr(err error, code string) bool {
	var se slackapi.SlackErrorResponse
	return errors.As(err, &se) && se.Err == code
}
