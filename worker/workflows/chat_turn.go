package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/store"
	"roamind.ai/memory"
	"roamind.ai/protocol"
)

// ChatTurnName is the chat-turn workflow: one agent turn for one Slack
// message, one run per message, serialized per thread.
const ChatTurnName = "chat-turn"

// ChatTurnConcurrency serializes runs within one Slack thread so turns never
// resume the same claude session concurrently.
const ChatTurnConcurrency = `input.event.channel + ':' + (has(input.event.thread_ts) ? input.event.thread_ts : input.event.ts)`

// ChatTurnOutput is recorded as the run's result in Hatchet.
type ChatTurnOutput struct {
	Skipped   string  `json:"skipped,omitempty"`
	UserID    string  `json:"user_id,omitempty"`
	Thread    string  `json:"thread,omitempty"`
	SessionID string  `json:"session_id,omitempty"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
	Reset     bool    `json:"session_reset,omitempty"`
}

// ChatTurn declares the chat-turn task.
func ChatTurn(c *hatchet.Client, d Deps) *hatchet.StandaloneTask {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	return c.NewStandaloneTask(ChatTurnName, d.handleChat,
		hatchet.WithWorkflowEvents(slack.ChatEventKey),
		hatchet.WithWorkflowDescription("One agent turn for one Slack message"),
		hatchet.WithConcurrency(&hatchet.Concurrency{
			Expression:    ChatTurnConcurrency,
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
}

func (d Deps) handleChat(ctx hatchet.Context, env slack.Envelope) (ChatTurnOutput, error) {
	return d.Process(ctx, ctx.WorkflowRunId(), env)
}

// Process handles one Slack message end to end.
func (d Deps) Process(ctx context.Context, runID string, env slack.Envelope) (ChatTurnOutput, error) {
	ev := env.Event
	out := ChatTurnOutput{Thread: ev.Channel + ":" + ev.RootTS()}
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

	goal, skip, err := d.route(ctx, ev)
	if err != nil {
		return out, err
	}
	if skip != "" {
		out.Skipped = skip
		return out, nil
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
	var exercise *protocol.ExerciseRef
	if goal != nil && ev.ThreadTS != "" {
		// A reply in a daily exercise's thread answers it.
		note, err := d.Store.MarkExerciseAnswered(ctx, provider, ev.Channel, ev.ThreadTS)
		if err != nil {
			return out, err
		}
		if note != "" {
			exercise = &protocol.ExerciseRef{Permalink: note}
		}
	}

	up, err := slack.NewUpdater(ctx, d.Slack, ev.Channel, ev.RootTS())
	if err != nil {
		return out, fmt.Errorf("post placeholder: %w", err)
	}
	text := slack.CleanText(ctx, d.Slack, ev.Text, env.BotUserID())
	if len(ev.Files) > 0 {
		dir, err := agent.WorkspaceDir(d.Agent, user.ID)
		if err != nil {
			return out, err
		}
		saved := slack.SaveFiles(ctx, d.Slack, ev.Files, dir)
		for _, f := range saved {
			if f.Problem != "" {
				log.Warn("file not saved", "name", f.Name, "problem", f.Problem)
			}
		}
		text = strings.TrimSpace(text + "\n\n" + slack.AttachmentNote(saved))
	}
	turn, runErr := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:        user.ID,
		SessionID:     sessionID,
		Text:          text,
		UserName:      user.DisplayName,
		UserEmail:     user.Email,
		MemoryProject: user.MemoryProject,
		Goal:          goal,
		Exercise:      exercise,
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
	if d.AfterTurn != nil && user.MemoryProject != "" {
		if err := d.AfterTurn(finishCtx, user); err != nil {
			log.Error("after turn", "err", err)
		}
	}
	out.SessionID, out.CostUSD, out.Reset = turn.SessionID, turn.CostUSD, turn.SessionReset
	if runErr != nil {
		return out, runErr
	}
	return out, nil
}

// route decides whether a chat event starts a turn, and which goal it is
// about. In a goal channel, every message from a user with a goal there is a
// turn about their goal (their @mentions there arrive twice, so the mention
// copy is skipped); a channel can hold several users' goals, each in their
// own memory. Elsewhere, and for anyone else, private-channel messages start
// a turn only through their @mention copy.
func (d Deps) route(ctx context.Context, ev slack.Event) (*protocol.GoalRef, string, error) {
	var gc *store.GoalChannel
	if ev.ChannelType == "group" || ev.Type == "app_mention" {
		var err error
		if gc, err = d.Store.GoalChannelOf(ctx, "slack", ev.Channel, ev.User); err != nil {
			return nil, "", err
		}
	}
	switch {
	case ev.Type == "message" && ev.ChannelType == "group" && gc == nil:
		return nil, "not addressed to the agent", nil
	case ev.Type == "app_mention" && gc != nil:
		return nil, "mention in own goal channel, handled as its message", nil
	case gc != nil:
		return &protocol.GoalRef{Title: gc.GoalTitle, Permalink: gc.GoalPermalink}, "", nil
	}
	return nil, "", nil
}

// resolveUser maps the Slack sender to a user, creating it from the Slack
// profile email on first contact. A non-empty refusal means the sender may
// not use the agent.
func (d Deps) resolveUser(ctx context.Context, env slack.Envelope) (*store.User, string, error) {
	team, slackUser := env.TeamOf(), env.Event.User
	if team != env.TeamID {
		// Someone from another organisation, e.g. in a Slack Connect channel.
		return nil, "Sorry, I only work with members of this workspace.", nil
	}
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
		if u.MemoryProject, err = d.Store.AssignMemoryProject(ctx, u.ID, memory.ProjectBase(u.DisplayName)); err != nil {
			return nil, "", err
		}
	}
	return u, "", nil
}

func (d Deps) reply(ctx context.Context, ev slack.Event, text string) error {
	_, _, err := d.Slack.PostMessageContext(ctx, ev.Channel,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(ev.RootTS()))
	return err
}
