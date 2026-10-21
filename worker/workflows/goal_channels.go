package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

// GoalChannelsName is the goal-channels workflow, which gives each of a
// user's active goals its own private Slack channel. The harness owns the
// mapping; the agent only learns, in a goal channel, which goal the
// conversation is about. Channels are never archived here: the user
// archives them.
const GoalChannelsName = "goal-channels"

// GoalChannelsInput names the user whose goals to sync.
type GoalChannelsInput struct {
	UserID string `json:"user_id"`
}

// GoalChannelsOutput lists the channels created by the run.
type GoalChannelsOutput struct {
	Created []string `json:"created,omitempty"` // channel names
}

// GoalChannels declares the goal-channels task. Runs are serialized per
// user, so a goal never gets two channels.
func GoalChannels(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return c.NewStandaloneTask(GoalChannelsName, func(ctx hatchet.Context, in GoalChannelsInput) (GoalChannelsOutput, error) {
		return d.Sync(ctx, in.UserID)
	},
		hatchet.WithWorkflowDescription("Create a Slack channel for each new active goal"),
		hatchet.WithConcurrency(&hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
}

// goalChannelsTrigger returns a hook for chat-turn: after a turn it starts a sync run
// for the user when an active goal has no channel yet. The check reads only
// local files and SQLite, so most turns start no run.
func goalChannelsTrigger(task *hatchet.StandaloneTask, d Deps) func(ctx context.Context, u *store.User, _ FinishedTurn) error {
	return func(ctx context.Context, u *store.User, _ FinishedTurn) error {
		missing, err := d.Missing(ctx, u)
		if err != nil || len(missing) == 0 {
			return err
		}
		_, err = task.RunNoWait(ctx, GoalChannelsInput{UserID: u.ID})
		return err
	}
}

// Missing returns the user's active goals that have no channel yet.
func (d Deps) Missing(ctx context.Context, u *store.User) ([]notes.Goal, error) {
	if d.Memory == nil || u.MemoryProject == "" || u.SlackUserID == "" {
		return nil, nil
	}
	goals, err := d.Memory.Goals(u.MemoryProject)
	if err != nil {
		return nil, err
	}
	var missing []notes.Goal
	for _, g := range goals {
		if !g.Active() {
			continue
		}
		ch, err := d.Store.Channel(ctx, u.ID, store.KindGoal, g.Permalink)
		if err != nil {
			return nil, err
		}
		if ch == "" {
			missing = append(missing, g)
		}
	}
	return missing, nil
}

// Sync creates a channel for each of the user's active goals that lacks one.
func (d Deps) Sync(ctx context.Context, userID string) (GoalChannelsOutput, error) {
	var out GoalChannelsOutput
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil {
		return out, err
	}
	missing, err := d.Missing(ctx, u)
	if err != nil {
		return out, err
	}
	for _, g := range missing {
		intro := fmt.Sprintf("This channel is for your goal *%s*. Talk to me here, no @mention needed, "+
			"about progress, tasks and plans for it; I'll keep them with the goal.", g.Title)
		c := store.Channel{Kind: store.KindGoal, Ref: g.Permalink, Title: g.Title}
		_, name, err := d.openChannel(ctx, u, c, ChannelName(g.Title), intro)
		if err != nil {
			return out, fmt.Errorf("goal %s: %w", g.Permalink, err)
		}
		slog.Info("goal channel created", "user", u.ID, "goal", g.Permalink, "channel", name)
		out.Created = append(out.Created, name)
	}
	return out, nil
}

// ChannelName is the Slack channel name for a goal: goal-<slug of title>,
// leaving room for a -n suffix.
func ChannelName(title string) string {
	if name := channelName("goal", title, 60); name != "goal" {
		return name
	}
	return "goal-untitled"
}
