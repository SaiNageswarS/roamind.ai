// Package workflows holds Roamind's Hatchet workflows, one file each: their
// tasks and how they are wired, triggered and limited.
//
//   - chat-turn: one agent turn per Slack message.
//   - goal-channels: a private Slack channel for each new active goal.
//   - learning-exercises: each morning, a learning-exercise run per
//     learning goal.
//   - learning-exercise: check-skip → build-context → draft-exercise → post.
//   - user-setup: memory-project → bm-setup → news-channel, for a new user.
//   - news-shorts: once a day, a news-digest run per user.
//   - news-digest: check-skip → build-context → draft-digest → post.
//   - news-reaction: reactions to digests, as feedback.
package workflows

import (
	"context"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/feeds"
	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
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
	// without memory, users are not set up (so they get no news channel),
	// goals get neither channels nor exercises, and there are no news
	// shorts.
	Memory *notes.Notes
	// Feeds reads the feeds of news interests.
	Feeds *feeds.Client
	// Location is the timezone of the daily exercises and news shorts.
	Location *time.Location
	// Now defaults to time.Now.
	Now func() time.Time
	// SetupUser, when set, starts setting up a user, without waiting;
	// chat-turn calls it for a user who is not set up. Register sets it to
	// start a user-setup run.
	SetupUser func(ctx context.Context, userID string) error
	// AfterTurn, when set, runs after each chat turn with memory. Register
	// sets it to give new goals their channels. Its errors are logged.
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
	setup := UserSetup(c, d)
	d.SetupUser = func(ctx context.Context, userID string) error {
		_, err := setup.RunNoWait(topLevel(ctx), UserSetupInput{UserID: userID})
		return err
	}
	d.AfterTurn = goalChannelsTrigger(goals, d)
	exercise := LearningExercise(c, d)
	digest := NewsDigest(c, d)
	return []hatchet.WorkflowBase{
		ChatTurn(c, d), setup, goals, exercise, LearningExercises(c, d, exercise),
		digest, NewsShorts(c, d, digest, setup), NewsReaction(c, d),
	}
}

// topLevel makes the runs started with ctx top-level runs rather than
// children of the task ctx belongs to: given a task's hatchet.Context, the
// SDK spawns a child run. So user-setup runs show on their own in the
// dashboard, and outlive the run that started them.
func topLevel(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
