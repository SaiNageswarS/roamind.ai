// Package workflows holds Roamind's Hatchet workflows, one file each: their
// tasks and how they are wired, triggered and limited.
//
//   - chat-turn: one agent turn per Slack message.
//   - goal-channels: a private Slack channel for each new active goal.
//   - learning-exercises: each morning, a learning-exercise run per
//     learning goal.
//   - learning-exercise: check-skip → build-context → draft-exercise → post.
//   - news-channels: each user's private news-shorts channel, with the
//     default interests.
//   - news-shorts: once a day, a news-digest run per user.
//   - news-digest: check-skip → build-context → draft-digest → post.
//   - news-reaction: reactions to digests, as feedback.
package workflows

import (
	"context"
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/feeds"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/store"
	"roamind.ai/memory"
)

// SlackAPI is the Slack client surface the workflows need.
type SlackAPI interface {
	slack.API
	slack.FileAPI
	GetUserInfoContext(ctx context.Context, user string, options ...slackapi.GetUserInfoOption) (*slackapi.User, error)
	CreateConversationContext(ctx context.Context, params slackapi.CreateConversationParams) (*slackapi.Channel, error)
	InviteUsersToConversationContext(ctx context.Context, channelID string, users ...string) (*slackapi.Channel, error)
}

// Deps are the long-lived dependencies of the workflows.
type Deps struct {
	Store *store.Store
	Slack SlackAPI
	Agent agent.Config
	// Memory is the users' long-term memory; without it, chat turns run
	// without memory, goals get neither channels nor exercises, and there
	// are no news shorts.
	Memory *memory.Config
	// Feeds reads the feeds of news interests.
	Feeds *feeds.Client
	// Location is the timezone of the daily exercises and news shorts.
	Location *time.Location
	// Now defaults to time.Now.
	Now func() time.Time
	// AfterTurn, when set, runs after each chat turn with memory. Register
	// sets it to give new goals their channels and new users their news
	// channel. Its errors are logged.
	AfterTurn func(ctx context.Context, u *store.User) error
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Register declares every workflow on the client, for the worker.
func Register(c *hatchet.Client, d Deps) []hatchet.WorkflowBase {
	goals := GoalChannels(c, d)
	if d.Memory == nil {
		return []hatchet.WorkflowBase{ChatTurn(c, d), goals}
	}
	newsChannels := NewsChannels(c, d)
	d.AfterTurn = afterTurn(goalChannelsTrigger(goals, d), newsChannelsTrigger(newsChannels, d))
	exercise := LearningExercise(c, d)
	digest := NewsDigest(c, d)
	return []hatchet.WorkflowBase{
		ChatTurn(c, d), goals, exercise, LearningExercises(c, d, exercise),
		newsChannels, digest, NewsShorts(c, d, digest, newsChannels), NewsReaction(c, d),
	}
}

// afterTurn runs every hook, and joins their errors.
func afterTurn(hooks ...func(ctx context.Context, u *store.User) error) func(ctx context.Context, u *store.User) error {
	return func(ctx context.Context, u *store.User) error {
		var errs []error
		for _, h := range hooks {
			errs = append(errs, h(ctx, u))
		}
		return errors.Join(errs...)
	}
}
