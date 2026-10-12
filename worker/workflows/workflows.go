// Package workflows holds Roamind's Hatchet workflows, one file each: their
// tasks and how they are wired, triggered and limited.
//
//   - chat-turn: one agent turn per Slack message.
//   - goal-channels: a private Slack channel for each new active goal.
//   - learning-exercises: each morning, a learning-exercise run per
//     learning goal.
//   - learning-exercise: check-skip → build-context → draft-exercise → post.
package workflows

import (
	"context"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
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
	// without memory and goals get neither channels nor exercises.
	Memory *memory.Config
	// Location is the timezone of the daily exercises.
	Location *time.Location
	// Now defaults to time.Now.
	Now func() time.Time
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
	d.AfterTurn = goalChannelsTrigger(goals, d)
	exercise := LearningExercise(c, d)
	return []hatchet.WorkflowBase{ChatTurn(c, d), goals, exercise, LearningExercises(c, d, exercise)}
}
