package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

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
	have, err := d.Store.GoalsWithChannel(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var missing []notes.Goal
	for _, g := range goals {
		if g.Active() && !have[g.Permalink] {
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
		ch, err := d.create(ctx, u, g)
		if err != nil {
			return out, fmt.Errorf("goal %s: %w", g.Permalink, err)
		}
		slog.Info("goal channel created", "user", u.ID, "goal", g.Permalink, "channel", ch.Name)
		out.Created = append(out.Created, ch.Name)
	}
	return out, nil
}

func (d Deps) create(ctx context.Context, u *store.User, g notes.Goal) (*slackapi.Channel, error) {
	ch, err := d.createChannel(ctx, ChannelName(g.Title))
	if err != nil {
		return nil, err
	}
	// Record the channel first: if a later step fails, the retry finds the
	// goal done instead of creating a second channel.
	err = d.Store.SaveGoalChannel(ctx, store.GoalChannel{
		UserID: u.ID, GoalPermalink: g.Permalink, GoalTitle: g.Title,
		ChannelID: ch.ID,
	})
	if err != nil {
		return nil, err
	}
	if _, err := d.Slack.InviteUsersToConversationContext(ctx, ch.ID, u.SlackUserID); err != nil && !isSlackErr(err, "already_in_channel") {
		return nil, fmt.Errorf("invite: %w", err)
	}
	intro := fmt.Sprintf("This channel is for your goal *%s*. Talk to me here, no @mention needed, "+
		"about progress, tasks and plans for it; I'll keep them with the goal.", g.Title)
	if _, _, err := d.Slack.PostMessageContext(ctx, ch.ID, slackapi.MsgOptionText(intro, false)); err != nil {
		return nil, fmt.Errorf("intro: %w", err)
	}
	return ch, nil
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

// ChannelName is the Slack channel name for a goal: goal-<slug of title>,
// within Slack's 80-character limit (leaving room for a -n suffix).
func ChannelName(title string) string {
	s := strings.Trim(nonName.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 60 {
		s = strings.TrimRight(s[:60], "-")
	}
	if s == "" {
		s = "untitled"
	}
	return "goal-" + s
}

func isSlackErr(err error, code string) bool {
	var se slackapi.SlackErrorResponse
	return errors.As(err, &se) && se.Err == code
}
