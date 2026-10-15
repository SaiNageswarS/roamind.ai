package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/channels/store"
)

// NewsChannelsName is the news-channels workflow, which gives a user their
// private news-shorts channel, and makes them follow the catalog's default
// interests. Like goal
// channels, it is never archived here.
const NewsChannelsName = "news-channels"

// NewsChannelsInput names the user who needs a news channel.
type NewsChannelsInput struct {
	UserID string `json:"user_id"`
}

// NewsChannelsOutput is the run's result.
type NewsChannelsOutput struct {
	Created string   `json:"created,omitempty"` // channel name
	Seeded  []string `json:"seeded,omitempty"`  // default interests followed
}

// NewsChannels declares the news-channels task. Runs are serialized per
// user, so a user never gets two channels.
func NewsChannels(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return c.NewStandaloneTask(NewsChannelsName, func(ctx hatchet.Context, in NewsChannelsInput) (NewsChannelsOutput, error) {
		return d.SyncNews(ctx, in.UserID)
	},
		hatchet.WithWorkflowDescription("Create a user's news-shorts channel and seed their interests"),
		hatchet.WithConcurrency(&hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
}

// newsChannelsTrigger returns a hook for chat-turn: after a turn it starts a
// news-channels run for a user without a news channel, which is how new
// users get theirs.
func newsChannelsTrigger(task *hatchet.StandaloneTask, d Deps) func(ctx context.Context, u *store.User) error {
	return func(ctx context.Context, u *store.User) error {
		if u.MemoryProject == "" || u.SlackUserID == "" {
			return nil
		}
		ch, err := d.Store.NewsChannelOf(ctx, u.ID)
		if err != nil || ch != "" {
			return err
		}
		_, err = task.RunNoWait(ctx, NewsChannelsInput{UserID: u.ID})
		return err
	}
}

// SyncNews creates the user's news channel when they have none, and seeds
// the default interests they lack.
func (d Deps) SyncNews(ctx context.Context, userID string) (NewsChannelsOutput, error) {
	var out NewsChannelsOutput
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil || u.MemoryProject == "" || u.SlackUserID == "" {
		return out, err
	}
	have, err := d.Store.NewsChannelOf(ctx, u.ID)
	if err != nil {
		return out, err
	}
	var ch *slackapi.Channel
	if have == "" {
		if ch, err = d.createChannel(ctx, NewsChannelName(u.DisplayName)); err != nil {
			return out, err
		}
		// Record the channel first: if a later step fails, the retry finds
		// it instead of creating a second one.
		if err := d.Store.SaveNewsChannel(ctx, u.ID, ch.ID); err != nil {
			return out, err
		}
		if _, err := d.Slack.InviteUsersToConversationContext(ctx, ch.ID, u.SlackUserID); err != nil && !isSlackErr(err, "already_in_channel") {
			return out, fmt.Errorf("invite: %w", err)
		}
		out.Created = ch.Name
	}
	if out.Seeded, err = d.Store.FollowDefaults(ctx, u.ID); err != nil {
		return out, err
	}
	if ch != nil {
		defaults, err := d.Store.DefaultInterests(ctx)
		if err != nil {
			return out, err
		}
		names := make([]string, len(defaults))
		for i, in := range defaults {
			names[i] = in.Name
		}
		if _, _, err := d.Slack.PostMessageContext(ctx, ch.ID, slackapi.MsgOptionText(d.newsIntro(names), false)); err != nil {
			return out, fmt.Errorf("intro: %w", err)
		}
		slog.Info("news channel created", "user", u.ID, "channel", ch.Name)
	}
	return out, nil
}

// newsIntro introduces the channel, with the interests the user starts
// with: the catalog's defaults.
func (d Deps) newsIntro(defaults []string) string {
	return fmt.Sprintf("This is your news-shorts channel. Every day at %s (%s) I'll post a few short, uplifting "+
		"pieces from trusted sources on your interests. You start with: %s.\n\n"+
		"Tell me here what else you're curious about, or what you'd like more or less of, no @mention needed. "+
		"React to a digest with 👍 or 👎, or reply in its thread; I use both to choose what comes next.",
		newsTimes(), d.Location, strings.Join(defaults, ", "))
}

// NewsChannelName is the Slack channel name for a user's news channel:
// news-shorts-<slug of their name>. Channel names are unique across the
// workspace, private ones included.
func NewsChannelName(userName string) string {
	s := strings.Trim(nonName.ReplaceAllString(strings.ToLower(userName), "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		return "news-shorts"
	}
	return "news-shorts-" + s
}
