package workflows

import (
	"context"
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/memory"
)

// LearningExercisesName is the cron workflow that each morning starts a
// learning-exercise run for every active learning goal with a channel;
// each run decides for itself whether its goal gets an exercise today.
const LearningExercisesName = "learning-exercises"

// ExerciseHour is the local hour at which exercises are posted.
const ExerciseHour = 9

// dispatchOutput lists the goals given a learning-exercise run.
type dispatchOutput struct {
	Skipped string   `json:"skipped,omitempty"`
	Started []string `json:"started,omitempty"` // goal permalinks
}

// LearningExercises declares the cron task that starts exercise, one run
// per goal, as child runs.
func LearningExercises(c *hatchet.Client, d Deps, exercise *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(LearningExercisesName, func(ctx hatchet.Context, _ struct{}) (dispatchOutput, error) {
		out, due, err := d.dueGoals(ctx, d.now())
		if err != nil {
			return out, err
		}
		for _, in := range due {
			if _, err := exercise.RunNoWait(ctx, in); err != nil {
				return out, fmt.Errorf("start %s: %w", in.GoalPermalink, err)
			}
			out.Started = append(out.Started, in.GoalPermalink)
		}
		return out, nil
	},
		hatchet.WithWorkflowDescription(fmt.Sprintf("Each day at %d:00 %s, start an exercise for every learning goal", ExerciseHour, d.Location)),
		hatchet.WithWorkflowCron(ExerciseCrons(d.Location, ExerciseHour)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// ExerciseCrons are UTC cron expressions for hour:00 in loc. Hatchet crons
// run in UTC, so a zone with daylight saving time gets one for each offset,
// and the run that is not at hour local time does nothing.
func ExerciseCrons(loc *time.Location, hour int) []string {
	var crons []string
	seen := map[string]bool{}
	year := time.Now().Year()
	for _, m := range []time.Month{time.January, time.July} {
		t := time.Date(year, m, 1, hour, 0, 0, 0, loc).UTC()
		c := fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour())
		if !seen[c] {
			seen[c] = true
			crons = append(crons, c)
		}
	}
	return crons
}

// dueGoals returns the active learning goals with a channel, when it is
// ExerciseHour local time.
func (d Deps) dueGoals(ctx context.Context, now time.Time) (dispatchOutput, []ExerciseInput, error) {
	var out dispatchOutput
	local := now.In(d.Location)
	if local.Hour() != ExerciseHour {
		out.Skipped = fmt.Sprintf("it is %s in %s, not %d:00", local.Format("15:04"), d.Location, ExerciseHour)
		return out, nil, nil
	}
	gcs, err := d.Store.GoalChannels(ctx, provider)
	if err != nil {
		return out, nil, err
	}
	var due []ExerciseInput
	goals := map[string][]memory.Goal{} // by user
	for _, gc := range gcs {
		if _, ok := goals[gc.UserID]; !ok {
			u, err := d.Store.UserByID(ctx, gc.UserID)
			if err != nil {
				return out, nil, err
			}
			if u != nil && u.MemoryProject != "" {
				if goals[gc.UserID], err = d.Memory.Goals(u.MemoryProject); err != nil {
					return out, nil, err
				}
			}
		}
		for _, g := range goals[gc.UserID] {
			if g.Permalink == gc.GoalPermalink && g.Active() && g.Kind == "learn" {
				due = append(due, ExerciseInput{UserID: gc.UserID, GoalPermalink: gc.GoalPermalink})
			}
		}
	}
	return out, due, nil
}
