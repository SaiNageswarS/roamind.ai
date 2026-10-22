package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/slack"
	"roamind.ai/memory/store"
)

// InboxDigestsName is the cron workflow that each morning, at InboxHour,
// starts an inbox-digest run for every Slack user with memory and an inbox
// channel, and a user-setup run for every one still without a channel.
const InboxDigestsName = "inbox-digests"

// InboxDigestName is the inbox-digest workflow, which posts one user's
// inbox digest in their inbox channel:
//
//	check-skip → draft → post
//
// check-skip picks the user's items that are due today from the items
// table, and skips the day when there are none; draft has the agent write
// the digest, reading each item's note and related notes in memory; post
// posts it. The digest's thread resumes the drafting session, so a reply
// such as "done" closes the item it is about with the items tools.
const InboxDigestName = "inbox-digest"

// InboxHour is the local hour at which inbox digests are posted.
const InboxHour = 8

// inboxDispatchOutput lists the runs started.
type inboxDispatchOutput struct {
	Day     string   `json:"day,omitempty"`
	Digests []string `json:"digests,omitempty"` // user ids
	Setups  []string `json:"setups,omitempty"`  // user ids
	// Failed are the runs that could not be started, logged; the others
	// still start.
	Failed []string `json:"failed,omitempty"`
}

// InboxInput names the user and day of an inbox digest.
type InboxInput struct {
	UserID string `json:"user_id"`
	Day    string `json:"day"` // local ISO date
}

// inboxCheck is check-skip's result; Skip is always present, so the later
// tasks' skip condition can read it.
type inboxCheck struct {
	Skip      string            `json:"skip"`
	ChannelID string            `json:"channel_id,omitempty"`
	Items     []agent.InboxItem `json:"items,omitempty"`
}

// inboxDraft is draft's result.
type inboxDraft struct {
	Digest string `json:"digest"`
	// SessionID is the claude session that wrote the digest; its thread
	// resumes it.
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
}

// inboxPost is post's result.
type inboxPost struct {
	Skipped string `json:"skipped,omitempty"`
	TS      string `json:"ts,omitempty"`
}

// InboxDigests declares the cron task that starts inbox-digest runs, one
// per user, as child runs, and user-setup runs, on their own (see
// topLevel), for users without an inbox channel. A run that can't be
// started is logged and listed; the others still start.
func InboxDigests(c *hatchet.Client, d Deps, digest, setup *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(InboxDigestsName, func(ctx hatchet.Context, _ struct{}) (inboxDispatchOutput, error) {
		out, digests, missing, err := d.dueInbox(ctx, d.now())
		if err != nil {
			return out, err
		}
		for _, in := range missing {
			if _, err := setup.RunNoWait(topLevel(ctx), in); err != nil {
				slog.Error("user-setup not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, "user-setup "+in.UserID)
				continue
			}
			out.Setups = append(out.Setups, in.UserID)
		}
		for _, in := range digests {
			if _, err := digest.RunNoWait(ctx, in); err != nil {
				slog.Error("inbox-digest not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, "inbox-digest "+in.UserID)
				continue
			}
			out.Digests = append(out.Digests, in.UserID)
		}
		return out, nil
	},
		hatchet.WithWorkflowDescription(fmt.Sprintf("Each day at %d:00 %s, start an inbox digest for every user", InboxHour, d.Location)),
		hatchet.WithWorkflowCron(LocalCrons(d.Location, time.Now(), InboxHour)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// dueInbox returns, for the local day of now, an inbox-digest input for
// each Slack user with memory and an inbox channel, and a user-setup input
// for each one without a channel.
func (d Deps) dueInbox(ctx context.Context, now time.Time) (inboxDispatchOutput, []InboxInput, []UserSetupInput, error) {
	var out inboxDispatchOutput
	out.Day = now.In(d.Location).Format(time.DateOnly)
	users, err := d.Store.Users(ctx)
	if err != nil {
		return out, nil, nil, err
	}
	var digests []InboxInput
	var missing []UserSetupInput
	for _, u := range users {
		if u.MemoryProject == "" || u.SlackUserID == "" {
			continue
		}
		ch, err := d.Store.Channel(ctx, u.ID, store.KindInbox, "")
		if err != nil {
			return out, nil, nil, err
		}
		if ch == "" {
			missing = append(missing, UserSetupInput{UserID: u.ID})
			continue
		}
		digests = append(digests, InboxInput{UserID: u.ID, Day: out.Day})
	}
	return out, digests, missing, nil
}

// InboxDigest declares the inbox-digest workflow. Runs are serialized per
// user. As in learning-exercise, every task after check-skip lists it as a
// parent, so each one is skipped when check-skip gives a reason.
func InboxDigest(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(InboxDigestName,
		hatchet.WithWorkflowDescription("Post an inbox digest of what is due for one user"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in InboxInput) (inboxCheck, error) {
		return d.checkInbox(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	draft := wf.NewTask("draft", func(ctx hatchet.Context, in InboxInput) (inboxDraft, error) {
		var chk inboxCheck
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return inboxDraft{}, err
		}
		return d.draftInbox(ctx, in, chk)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// The draft only reads memory.
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
	wf.NewTask("post", func(ctx hatchet.Context, in InboxInput) (inboxPost, error) {
		var chk inboxCheck
		var dr inboxDraft
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return inboxPost{}, err
		}
		if err := ctx.ParentOutput(draft, &dr); err != nil {
			return inboxPost{}, err
		}
		return d.postInbox(ctx, in, chk, dr)
	},
		hatchet.WithParents(check, draft),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// A retry could post the digest twice.
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	return wf
}

// checkInbox decides whether the user gets a digest on the day: they need
// memory and an inbox channel, the day must not be posted yet, and some of
// their items must be due.
func (d Deps) checkInbox(ctx context.Context, in InboxInput) (inboxCheck, error) {
	var out inboxCheck
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	if u == nil || u.MemoryProject == "" {
		out.Skip = "no such user with memory"
		return out, nil
	}
	ch, err := d.Store.Channel(ctx, u.ID, store.KindInbox, "")
	if err != nil {
		return out, err
	}
	if ch == "" {
		out.Skip = "the user has no inbox channel"
		return out, nil
	}
	posted, err := d.Store.InboxPosted(ctx, u.ID, in.Day)
	if err != nil {
		return out, err
	}
	if posted {
		out.Skip = "the digest of " + in.Day + " is already posted"
		return out, nil
	}
	day, err := time.ParseInLocation(time.DateOnly, in.Day, d.Location)
	if err != nil {
		return out, fmt.Errorf("day %q: %w", in.Day, err)
	}
	due, err := d.Store.DueItems(ctx, u.ID, day)
	if err != nil {
		return out, err
	}
	if len(due) == 0 {
		out.Skip = "nothing is due"
		return out, nil
	}
	out.ChannelID = ch
	for _, it := range due {
		out.Items = append(out.Items, agent.InboxItem{
			Key: it.Key, Kind: it.Kind, EventKind: it.EventKind, Title: it.Title, Date: it.Date, Note: it.Note,
			// Events stop showing after their day; tasks stay until closed.
			Overdue: it.Kind == store.ItemTask && it.Date != "" && it.Date[:min(len(it.Date), 10)] < in.Day,
		})
	}
	return out, nil
}

// draftInbox has the agent write the digest from the due items, reading
// their notes, as structured output. Like an exercise draft, the turn gets
// the workspace tools and the user's memory, and runs in the workspace
// directory, so the digest's thread can resume it.
func (d Deps) draftInbox(ctx context.Context, in InboxInput, chk inboxCheck) (inboxDraft, error) {
	var out inboxDraft
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	day, err := time.ParseInLocation(time.DateOnly, in.Day, d.Location)
	if err != nil {
		return out, fmt.Errorf("day %q: %w", in.Day, err)
	}
	prompt, err := agent.InboxDigestPrompt(agent.InboxDigest{
		UserName: u.DisplayName, Date: in.Day, Weekday: day.Weekday().String(), Items: chk.Items,
	})
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:        u.ID,
		Text:          prompt,
		UserName:      u.DisplayName,
		UserEmail:     u.Email,
		MemoryProject: u.MemoryProject,
		Tools:         protocol.ToolsWorkspace,
		ServerTools:   d.serverTools(InboxDigestName),
		OutputSchema:  agent.InboxDigestSchema,
	}, func(protocol.Packet) {})
	out.SessionID, out.CostUSD = turn.SessionID, turn.CostUSD
	if err != nil {
		return out, err
	}
	var w agent.WrittenInbox
	if err := json.Unmarshal(turn.Structured, &w); err != nil {
		return out, fmt.Errorf("inbox digest: %w", err)
	}
	if out.Digest = strings.TrimSpace(w.Digest); out.Digest == "" {
		return out, fmt.Errorf("inbox digest is empty")
	}
	return out, nil
}

// postInbox reserves the day, posts the digest as a new message in the
// inbox channel, and records it and its thread, which resumes the drafting
// session.
func (d Deps) postInbox(ctx context.Context, in InboxInput, chk inboxCheck, dr inboxDraft) (inboxPost, error) {
	var out inboxPost
	// The day is reserved before posting, so a run that dies after posting
	// is never replayed into a second post.
	id, ok, err := d.Store.ReserveInbox(ctx, in.UserID, chk.ChannelID, in.Day)
	if err != nil {
		return out, err
	}
	if !ok {
		out.Skipped = "the digest of " + in.Day + " is already posted"
		return out, nil
	}
	chunks := slack.Split(slack.ToMrkdwn(dr.Digest), slack.MaxChunk)
	_, ts, err := d.Slack.PostMessageContext(ctx, chk.ChannelID, slackapi.MsgOptionText(chunks[0], false),
		slackapi.MsgOptionDisableLinkUnfurl())
	if err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if rerr := d.Store.ReleaseInbox(releaseCtx, id); rerr != nil {
			slog.Error("release inbox day", "user", in.UserID, "day", in.Day, "err", rerr)
		}
		return out, fmt.Errorf("post inbox digest: %w", err)
	}
	out.TS = ts
	// Recorded straight after posting: from here on a reply in the thread
	// is about the digest.
	if err := d.Store.CompleteInbox(ctx, id, ts, dr.Digest); err != nil {
		return out, err
	}
	if err := d.Store.SaveThread(ctx, store.Thread{
		ChannelID: chk.ChannelID, ThreadTS: ts, UserID: in.UserID, ClaudeSessionID: dr.SessionID,
	}); err != nil {
		return out, err
	}
	for _, c := range chunks[1:] {
		if _, _, err := d.Slack.PostMessageContext(ctx, chk.ChannelID,
			slackapi.MsgOptionText(c, false), slackapi.MsgOptionTS(ts), slackapi.MsgOptionDisableLinkUnfurl()); err != nil {
			return out, fmt.Errorf("post inbox digest: %w", err)
		}
	}
	slog.Info("inbox digest posted", "user", in.UserID, "day", in.Day, "items", len(chk.Items))
	return out, nil
}
