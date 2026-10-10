// Package chatturn is the Hatchet task that handles one chat message: one
// run per message, serialized per thread.
package chatturn

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/store"
	"roamind.ai/protocol"
)

// Name is the Hatchet workflow/task name.
const Name = "chat-turn"

// ConcurrencyExpression serializes runs within one Slack thread so turns never
// resume the same claude session concurrently.
const ConcurrencyExpression = `input.event.channel + ':' + (has(input.event.thread_ts) ? input.event.thread_ts : input.event.ts)`

// SlackAPI is the Slack client surface the task needs.
type SlackAPI interface {
	slack.API
	GetUserInfoContext(ctx context.Context, user string, options ...slackapi.GetUserInfoOption) (*slackapi.User, error)
}

// Deps are the long-lived dependencies of the task.
type Deps struct {
	Store     *store.Store
	Slack     SlackAPI
	Allowlist identity.Allowlist
	Agent     agent.Config
}

// Output is recorded as the run's result in Hatchet.
type Output struct {
	Skipped   string  `json:"skipped,omitempty"`
	UserID    string  `json:"user_id,omitempty"`
	Thread    string  `json:"thread,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Reset     bool    `json:"session_reset,omitempty"`
}

// New registers the chat-turn task on the client.
func New(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return c.NewStandaloneTask(Name, d.handle,
		hatchet.WithWorkflowEvents(slack.ChatEventKey),
		hatchet.WithWorkflowDescription("One agent turn for one Slack message"),
		hatchet.WithConcurrency(&hatchet.Concurrency{
			Expression:    ConcurrencyExpression,
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
}

func (d Deps) handle(ctx hatchet.Context, env slack.Envelope) (Output, error) {
	return d.Process(ctx, ctx.WorkflowRunId(), env)
}

// Process handles one Slack message end to end.
func (d Deps) Process(ctx context.Context, runID string, env slack.Envelope) (Output, error) {
	ev := env.Event
	out := Output{Thread: ev.Channel + ":" + ev.RootTS()}
	log := slog.With("run", runID, "thread", out.Thread)

	if !env.IsChat() {
		out.Skipped = "not a chat message"
		return out, nil
	}
	if env.EventID != "" {
		fresh, err := d.Store.MarkEvent(ctx, "slack", env.EventID)
		if err != nil {
			return out, err
		}
		if !fresh {
			out.Skipped = "duplicate event " + env.EventID
			return out, nil
		}
	}

	user, refusal, err := d.resolveUser(ctx, env)
	if err != nil {
		return out, err
	}
	if refusal != "" {
		out.Skipped = refusal
		return out, d.reply(ctx, ev, refusal)
	}
	out.UserID = user.ID

	thread, err := d.Store.GetThread(ctx, "slack", ev.Channel, ev.RootTS())
	if err != nil {
		return out, err
	}
	if thread != nil && thread.UserID != user.ID {
		// A conversation belongs to whoever started it; the session (and,
		// later, memory) is theirs.
		out.Skipped = "thread owned by another user"
		return out, d.reply(ctx, ev, "This conversation belongs to someone else — mention me in a new thread to start your own.")
	}
	sessionID := ""
	if thread != nil {
		sessionID = thread.ClaudeSessionID
	}

	up, err := slack.NewUpdater(ctx, d.Slack, ev.Channel, ev.RootTS())
	if err != nil {
		return out, fmt.Errorf("post placeholder: %w", err)
	}
	turn, runErr := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:    user.ID,
		SessionID: sessionID,
		Text:      slack.CleanText(ev.Text),
		UserName:  user.DisplayName,
	}, "Slack", up.Apply)

	note := ""
	if turn.SessionReset && sessionID != "" {
		note = "(previous context was lost — starting fresh)"
	}
	// Finish with a fresh context so the final edit lands even if the run was
	// cancelled or timed out.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	up.Finish(finishCtx, note)

	if turn.SessionID != "" {
		err := d.Store.SaveThread(finishCtx, store.Thread{
			Provider: "slack", ChannelID: ev.Channel, ThreadTS: ev.RootTS(),
			UserID: user.ID, ClaudeSessionID: turn.SessionID,
		})
		if err != nil {
			log.Error("save thread", "err", err)
		}
	}
	out.SessionID, out.CostUSD, out.Reset = turn.SessionID, turn.CostUSD, turn.SessionReset
	if runErr != nil {
		return out, runErr
	}
	return out, nil
}

// resolveUser maps the Slack sender to a user, creating it from the Slack
// profile email on first contact. A non-empty refusal means the sender may
// not use the agent.
func (d Deps) resolveUser(ctx context.Context, env slack.Envelope) (*store.User, string, error) {
	team, slackUser := env.TeamOf(), env.Event.User
	u, err := d.Store.UserBySlack(ctx, team, slackUser)
	if err != nil {
		return nil, "", err
	}
	if u == nil {
		info, err := d.Slack.GetUserInfoContext(ctx, slackUser)
		if err != nil {
			return nil, "", fmt.Errorf("slack users.info: %w", err)
		}
		email := identity.NormalizeEmail(info.Profile.Email)
		if email == "" {
			return nil, "I can't see an email address on your Slack profile, so I can't identify you.", nil
		}
		name := info.Profile.DisplayName
		if name == "" {
			name = info.RealName
		}
		u = &store.User{
			ID: identity.UserID(email), Email: email, DisplayName: name,
			SlackTeamID: team, SlackUserID: slackUser,
		}
		if err := d.Store.UpsertSlackUser(ctx, *u); err != nil {
			return nil, "", err
		}
	}
	if !d.Allowlist.Allowed(u.Email) {
		return nil, "Sorry, you don't have access to this assistant.", nil
	}
	return u, "", nil
}

func (d Deps) reply(ctx context.Context, ev slack.Event, text string) error {
	_, _, err := d.Slack.PostMessageContext(ctx, ev.Channel,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(ev.RootTS()))
	return err
}
