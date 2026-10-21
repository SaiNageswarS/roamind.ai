package workflows

import (
	"context"
	"fmt"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/memory/store/notes"
)

// LearningExercisesName is the cron workflow that each morning starts a
// learning-exercise run for every active learning goal with a channel;
// each run decides for itself whether its goal gets an exercise today.
const LearningExercisesName = "learning-exercises"

// ExerciseHour is the local hour at which exercises are posted.
const ExerciseHour = 9

// dispatchOutput lists the goals given a learning-exercise run.
type dispatchOutput struct {
	Started []string `json:"started,omitempty"` // goal permalinks
}

// LearningExercises declares the cron task that starts exercise, one run
// per goal, as child runs.
func LearningExercises(c *hatchet.Client, d Deps, exercise *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(LearningExercisesName, func(ctx hatchet.Context, _ struct{}) (dispatchOutput, error) {
		due, err := d.dueGoals(ctx)
		var out dispatchOutput
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
		hatchet.WithWorkflowCron(LocalCrons(d.Location, time.Now(), ExerciseHour)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// LocalCrons are UTC cron expressions for each hour:00 in loc, at loc's
// offset on the day of at. Hatchet crons run in UTC, so in a zone with
// daylight saving time the runs are an hour off after a change of offset
// until the worker is started again.
func LocalCrons(loc *time.Location, at time.Time, hours ...int) []string {
	var crons []string
	at = at.In(loc)
	for _, hour := range hours {
		t := time.Date(at.Year(), at.Month(), at.Day(), hour, 0, 0, 0, loc).UTC()
		crons = append(crons, fmt.Sprintf("%d %d * * *", t.Minute(), t.Hour()))
	}
	return crons
}

// dueGoals returns the active learning goals with a channel.
func (d Deps) dueGoals(ctx context.Context) ([]ExerciseInput, error) {
	gcs, err := d.Store.GoalChannels(ctx)
	if err != nil {
		return nil, err
	}
	var due []ExerciseInput
	goals := map[string][]notes.Goal{} // by user
	for _, gc := range gcs {
		if _, ok := goals[gc.UserID]; !ok {
			u, err := d.Store.UserByID(ctx, gc.UserID)
			if err != nil {
				return nil, err
			}
			if u != nil && u.MemoryProject != "" {
				if goals[gc.UserID], err = d.Memory.Goals(u.MemoryProject); err != nil {
					return nil, err
				}
			}
		}
		for _, g := range goals[gc.UserID] {
			if g.Permalink == gc.GoalPermalink && g.Active() && g.Kind == "learn" {
				due = append(due, ExerciseInput{UserID: gc.UserID, GoalPermalink: gc.GoalPermalink})
			}
		}
	}
	return due, nil
}
