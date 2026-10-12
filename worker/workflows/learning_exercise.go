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
	"roamind.ai/channels/store"
	"roamind.ai/memory"
)

// LearningExerciseName is the learning-exercise workflow, which posts
// today's exercise for one learning goal:
//
//	check-skip → build-context → draft-exercise → post
//
// The user answers in the exercise's thread, where chat-turn evaluates the
// answer and updates the exercise note; that is not part of this workflow.
const LearningExerciseName = "learning-exercise"

// recentExercises is how many earlier exercises calibrate the next one.
const recentExercises = 5

// ExerciseInput names the learning goal to set an exercise for.
type ExerciseInput struct {
	UserID        string `json:"user_id"`
	GoalPermalink string `json:"goal_permalink"`
}

// checkOutput is check-skip's result. The later tasks are skipped when Skip
// is set, so it is always present in the output, even when empty.
type checkOutput struct {
	Skip      string `json:"skip"` // why the goal gets no exercise today
	ChannelID string `json:"channel_id,omitempty"`
	GoalTitle string `json:"goal_title,omitempty"`
}

// exerciseContext is build-context's result: what the draft is calibrated
// from.
type exerciseContext struct {
	// Goal is the goal note's content, exercise preferences included.
	Goal string `json:"goal"`
	// Exercises are the latest exercise notes, newest first, with the
	// user's answers and how they went.
	Exercises []string `json:"exercises,omitempty"`
}

// draftOutput is draft-exercise's result.
type draftOutput struct {
	agent.DraftedExercise
	// SessionID is the claude session that drafted the exercise; the
	// exercise's thread resumes it, so the evaluation knows what was asked.
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd,omitempty"`
}

// postOutput is post's result.
type postOutput struct {
	ThreadTS      string `json:"thread_ts"`
	NotePermalink string `json:"note_permalink"`
}

// skipped is check-skip's condition for skipping the later tasks.
const skipped = "output.skip != ''"

// LearningExercise declares the learning-exercise workflow. Runs are
// serialized per goal. Every task after check-skip lists it as a parent, so
// each one is skipped when check-skip gives a reason: Hatchet runs the
// children of a skipped task.
func LearningExercise(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(LearningExerciseName,
		hatchet.WithWorkflowDescription("Post today's exercise for one learning goal"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id + ':' + input.goal_permalink",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in ExerciseInput) (checkOutput, error) {
		return d.checkSkip(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	build := wf.NewTask("build-context", func(ctx hatchet.Context, in ExerciseInput) (exerciseContext, error) {
		return d.buildContext(ctx, in)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	draft := wf.NewTask("draft-exercise", func(ctx hatchet.Context, in ExerciseInput) (draftOutput, error) {
		var chk checkOutput
		var ec exerciseContext
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return draftOutput{}, err
		}
		if err := ctx.ParentOutput(build, &ec); err != nil {
			return draftOutput{}, err
		}
		return d.draftExercise(ctx, in, chk, ec)
	},
		hatchet.WithParents(check, build),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// Drafting has no side effects besides the claude session.
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
	wf.NewTask("post", func(ctx hatchet.Context, in ExerciseInput) (postOutput, error) {
		var chk checkOutput
		var dr draftOutput
		if err := ctx.ParentOutput(check, &chk); err != nil {
			return postOutput{}, err
		}
		if err := ctx.ParentOutput(draft, &dr); err != nil {
			return postOutput{}, err
		}
		return d.postExercise(ctx, in, chk, dr)
	},
		hatchet.WithParents(check, draft),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// A retry could post the exercise twice.
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	return wf
}

// checkSkip decides whether the goal gets an exercise today: it must be an
// active learning goal of the user with a channel, whose latest exercise is
// answered and not from today.
func (d Deps) checkSkip(ctx context.Context, in ExerciseInput) (checkOutput, error) {
	var out checkOutput
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	gc, err := d.goalChannel(ctx, in)
	if err != nil {
		return out, err
	}
	if u == nil || gc == nil {
		out.Skip = "the goal has no channel"
		return out, nil
	}
	g, err := d.goal(u, in.GoalPermalink)
	if err != nil {
		return out, err
	}
	if g == nil || !g.Active() || g.Kind != "learn" {
		out.Skip = "not an active learning goal"
		return out, nil
	}
	last, err := d.Store.LastExercise(ctx, in.UserID, in.GoalPermalink)
	if err != nil {
		return out, err
	}
	if last != nil {
		posted := last.PostedAt.In(d.Location).Format(time.DateOnly)
		switch {
		case posted == d.now().In(d.Location).Format(time.DateOnly):
			out.Skip = "today's exercise is already posted"
			return out, nil
		case last.AnsweredAt.IsZero():
			out.Skip = "the exercise of " + posted + " is unanswered"
			return out, nil
		}
	}
	out.ChannelID, out.GoalTitle = gc.ChannelID, gc.GoalTitle
	return out, nil
}

// buildContext reads what the draft is calibrated from: the goal note, with
// its exercise preferences, and the latest exercise notes. They are read
// from disk: bm's build_context lists the notes linked to the goal but not
// their content.
func (d Deps) buildContext(ctx context.Context, in ExerciseInput) (exerciseContext, error) {
	var out exerciseContext
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	g, err := d.goal(u, in.GoalPermalink)
	if err != nil || g == nil {
		return out, fmt.Errorf("goal %s: %v", in.GoalPermalink, err)
	}
	out.Goal = g.Body
	out.Exercises, err = d.Memory.RecentExercises(u.MemoryProject, in.GoalPermalink, recentExercises)
	return out, err
}

// draftExercise has the agent draft the exercise from the context, as
// structured output.
func (d Deps) draftExercise(ctx context.Context, in ExerciseInput, chk checkOutput, ec exerciseContext) (draftOutput, error) {
	var out draftOutput
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	prompt, err := agent.DraftExercisePrompt(agent.ExerciseDraft{
		UserName:  u.DisplayName,
		Date:      d.now().In(d.Location).Format(time.DateOnly),
		GoalTitle: chk.GoalTitle,
		Goal:      ec.Goal,
		Exercises: ec.Exercises,
	})
	if err != nil {
		return out, err
	}
	// The context is all the draft needs from memory, so the turn runs
	// without memory tools; it keeps web search for material.
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID:       u.ID,
		Text:         prompt,
		UserName:     u.DisplayName,
		UserEmail:    u.Email,
		OutputSchema: agent.DraftExerciseSchema,
	}, func(protocol.Packet) {})
	out.SessionID, out.CostUSD = turn.SessionID, turn.CostUSD
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(turn.Structured, &out.DraftedExercise); err != nil {
		return out, fmt.Errorf("drafted exercise: %w", err)
	}
	if strings.TrimSpace(out.Exercise) == "" {
		return out, fmt.Errorf("drafted exercise is empty")
	}
	return out, nil
}

// postExercise posts the exercise as a new message in the goal's channel,
// records it, and saves its note. The thread resumes the drafting session,
// so the user's answer there is evaluated knowing what was asked.
func (d Deps) postExercise(ctx context.Context, in ExerciseInput, chk checkOutput, dr draftOutput) (postOutput, error) {
	var out postOutput
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	now := d.now()
	date := now.In(d.Location).Format(time.DateOnly)
	folder := memory.ExerciseFolder(in.GoalPermalink)
	out.NotePermalink = u.MemoryProject + "/" + folder + "/" + date

	chunks := slack.Split(slack.ToMrkdwn(dr.Exercise), slack.MaxChunk)
	_, ts, err := d.Slack.PostMessageContext(ctx, chk.ChannelID, slackapi.MsgOptionText(chunks[0], false))
	if err != nil {
		return out, fmt.Errorf("post exercise: %w", err)
	}
	out.ThreadTS = ts
	for _, c := range chunks[1:] {
		if _, _, err := d.Slack.PostMessageContext(ctx, chk.ChannelID,
			slackapi.MsgOptionText(c, false), slackapi.MsgOptionTS(ts)); err != nil {
			return out, fmt.Errorf("post exercise: %w", err)
		}
	}

	// Recorded straight after posting: from here on a reply in the thread
	// answers the exercise.
	if err := d.Store.SaveThread(ctx, store.Thread{
		ChannelID: chk.ChannelID, ThreadTS: ts,
		UserID: u.ID, ClaudeSessionID: dr.SessionID,
	}); err != nil {
		return out, err
	}
	if err := d.Store.SaveExercise(ctx, store.Exercise{
		UserID: u.ID, GoalPermalink: in.GoalPermalink,
		ChannelID: chk.ChannelID, ThreadTS: ts, NotePermalink: out.NotePermalink, PostedAt: now,
	}); err != nil {
		return out, err
	}
	title := date + " " + dr.Topic
	if err := d.Memory.WriteNote(ctx, u.MemoryProject, folder, title, "exercise",
		exerciseNote(out.NotePermalink, title, date, in.GoalPermalink, dr.DraftedExercise)); err != nil {
		return out, err
	}
	slog.Info("exercise posted", "user", u.ID, "goal", in.GoalPermalink, "note", out.NotePermalink)
	return out, nil
}

// exerciseNote is the exercise note as the Exercise schema describes it.
func exerciseNote(permalink, title, date, goal string, ex agent.DraftedExercise) string {
	return fmt.Sprintf(`---
title: %s
type: exercise
permalink: %s
---

## Exercise

%s

## Observations
- [date] %s
- [topic] %s
- [difficulty] %s
- [format] %s

## Relations
- exercise_of [[%s]]
`, title, permalink, strings.TrimSpace(ex.Exercise), date, ex.Topic, ex.Difficulty, ex.Format, goal)
}

// goalChannel is the channel of the user's goal, or nil.
func (d Deps) goalChannel(ctx context.Context, in ExerciseInput) (*store.GoalChannel, error) {
	gcs, err := d.Store.GoalChannels(ctx)
	for _, gc := range gcs {
		if gc.UserID == in.UserID && gc.GoalPermalink == in.GoalPermalink {
			return &gc, nil
		}
	}
	return nil, err
}

// goal reads the user's goal note, or nil when there is none.
func (d Deps) goal(u *store.User, permalink string) (*memory.Goal, error) {
	if d.Memory == nil || u.MemoryProject == "" {
		return nil, nil
	}
	goals, err := d.Memory.Goals(u.MemoryProject)
	for _, g := range goals {
		if g.Permalink == permalink {
			return &g, nil
		}
	}
	return nil, err
}
