package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
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
		fresh, err := d.Store.MarkEvent(ctx, env.EventID)
		if err != nil {
			return out, err
		}
		if !fresh {
			out.Skipped = "duplicate event " + env.EventID
			return out, nil
		}
	}

	goal, news, skip, err := d.route(ctx, ev)
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
	if out.Skipped, err = d.setUp(ctx, ev, user); err != nil || out.Skipped != "" {
		return out, err
	}

	thread, err := d.Store.GetThread(ctx, ev.Channel, ev.RootTS())
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
		note, err := d.Store.MarkExerciseAnswered(ctx, ev.Channel, ev.ThreadTS)
		if err != nil {
			return out, err
		}
		if note != "" {
			exercise = &protocol.ExerciseRef{Permalink: note}
		}
	}

	var newsRef *protocol.NewsRef
	if news {
		newsRef = &protocol.NewsRef{}
		ins, err := d.Store.UserInterests(ctx, user.ID)
		if err != nil {
			return out, err
		}
		for _, in := range ins {
			newsRef.Interests = append(newsRef.Interests, protocol.InterestRef{Interest: in.Interest, Status: in.Status})
		}
		if ev.ThreadTS != "" {
			// A reply in a digest's thread is about the digest, and counts as
			// liking it.
			dg, err := d.Store.DigestByTS(ctx, ev.Channel, ev.ThreadTS)
			if err != nil {
				return out, err
			}
			if dg != nil && dg.UserID == user.ID {
				newsRef.Digest = dg.Text
				if err := d.Store.SaveSignal(ctx, store.Signal{UserID: user.ID, DigestID: dg.ID, Kind: "reply", Value: 1}); err != nil {
					return out, err
				}
			}
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
		News:          newsRef,
		Tools:         protocol.ToolsWorkspace,
		ServerTools:   d.serverTools(ChatTurnName),
	}, up.Apply)

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
			ChannelID: ev.Channel, ThreadTS: ev.RootTS(),
			UserID: user.ID, ClaudeSessionID: turn.SessionID,
		})
		if err != nil {
			log.Error("save thread", "err", err)
		}
	}
	if d.AfterTurn != nil && user.MemoryProject != "" {
		if err := d.AfterTurn(finishCtx, user, FinishedTurn{Text: text, Channel: ev.Channel, ThreadTS: ev.RootTS()}); err != nil {
			log.Error("after turn", "err", err)
		}
	}
	out.SessionID, out.CostUSD, out.Reset = turn.SessionID, turn.CostUSD, turn.SessionReset
	if runErr != nil {
		return out, runErr
	}
	return out, nil
}

// route decides whether a chat event starts a turn, and what it is about.
// In a goal channel, every message from a user with a goal there is a turn
// about their goal (their @mentions there arrive twice, so the mention copy
// is skipped); a channel can hold several users' goals, each in their own
// memory. The user's news channel works the same way, with news turns.
// Elsewhere, and for anyone else, private-channel messages start a turn only
// through their @mention copy.
func (d Deps) route(ctx context.Context, ev slack.Event) (*protocol.GoalRef, bool, string, error) {
	var gc *store.GoalChannel
	news := false
	if ev.ChannelType == "group" || ev.Type == "app_mention" {
		var err error
		if gc, err = d.Store.GoalChannelOf(ctx, ev.Channel, ev.User); err != nil {
			return nil, false, "", err
		}
		if gc == nil {
			if news, err = d.Store.IsNewsChannelOf(ctx, ev.Channel, ev.User); err != nil {
				return nil, false, "", err
			}
		}
	}
	own := gc != nil || news
	switch {
	case ev.Type == "message" && ev.ChannelType == "group" && !own:
		return nil, false, "not addressed to the agent", nil
	case ev.Type == "app_mention" && own:
		return nil, false, "mention in own goal or news channel, handled as its message", nil
	case gc != nil:
		return &protocol.GoalRef{Title: gc.GoalTitle, Permalink: gc.GoalPermalink}, false, "", nil
	}
	return nil, news, "", nil
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
	}
	return u, "", nil
}

// setupReply answers a user's messages while user-setup runs for them.
const setupReply = "Hi! I'm setting things up for you: your memory and your news-shorts channel. " +
	"Give me a minute, then send your message again."

// setupFailedReply answers a message whose user-setup could not be started.
const setupFailedReply = "Sorry, I couldn't start setting things up for you. Please try again in a minute."

// setUp starts user-setup, without waiting, for a user who is not fully
// set up. While their memory project is not ready (a new user above all),
// it tells them so and returns why the turn is skipped; a user missing
// only their news channel gets their turn.
func (d Deps) setUp(ctx context.Context, ev slack.Event, u *store.User) (string, error) {
	if d.SetupUser == nil || d.Memory == nil {
		return "", nil
	}
	memory := u.MemoryProject != "" && d.Memory.Ready(u.MemoryProject)
	if memory {
		ch, err := d.Store.NewsChannelOf(ctx, u.ID)
		if err != nil || ch != "" {
			return "", err
		}
	}
	if err := d.SetupUser(ctx, u.ID); err != nil {
		if !memory {
			err = errors.Join(err, d.reply(ctx, ev, setupFailedReply))
		}
		return "", fmt.Errorf("start user setup: %w", err)
	}
	if memory {
		return "", nil
	}
	return "setting up user", d.reply(ctx, ev, setupReply)
}

func (d Deps) reply(ctx context.Context, ev slack.Event, text string) error {
	_, _, err := d.Slack.PostMessageContext(ctx, ev.Channel,
		slackapi.MsgOptionText(text, false), slackapi.MsgOptionTS(ev.RootTS()))
	return err
}
