package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
	"roamind.ai/memory/store"
)

// JobSearchName is the cron workflow of the job assistant: every
// quarter-hour it brings in the web app's profiles, gives each new user a
// daily search time, starts a job-hunt run for each user whose time has
// come today, and a job-links run for each user with links queued.
const JobSearchName = "job-search"

const (
	// jobDayStart and jobDayEnd bound the daily search times, in minutes
	// after local midnight: 08:00 to 22:00.
	jobDayStart = 8 * 60
	jobDayEnd   = 22 * 60
	// jobGap is how far apart users' search times are kept, so searches
	// are spread through the day; jobStep is the grid they are picked
	// from.
	jobGap  = 90
	jobStep = 15
)

// jobSearchOutput lists what a dispatch did.
type jobSearchOutput struct {
	Profiles  int      `json:"profiles"`
	Scheduled []string `json:"scheduled,omitempty"` // new users given a time, "user hh:mm"
	Hunts     []string `json:"hunts,omitempty"`     // user ids
	Links     []string `json:"links,omitempty"`     // user ids, of job-links runs
	// Failed are the users whose run could not be started, logged; the
	// others still start.
	Failed []string `json:"failed,omitempty"`
}

// JobSearch declares the cron task that starts job-hunt and job-links
// runs, as child runs. A run that can't be started is logged and listed;
// the user is due again at the next quarter-hour, and its links are queued
// again.
func JobSearch(c *hatchet.Client, d Deps, hunt, links *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(JobSearchName, func(ctx hatchet.Context, _ struct{}) (jobSearchOutput, error) {
		out, due, users, err := d.dueHunts(ctx, d.now())
		if err != nil {
			return out, err
		}
		for _, in := range due {
			if _, err := hunt.RunNoWait(ctx, in); err != nil {
				slog.Error("job-hunt not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, in.UserID)
				continue
			}
			if err := d.Store.MarkRan(ctx, in.UserID, in.Date); err != nil {
				return out, err
			}
			out.Hunts = append(out.Hunts, in.UserID)
		}
		claimed, err := d.claimLinks(ctx, users, d.now().In(d.Location).Format(time.DateOnly))
		for _, in := range claimed {
			if _, err := links.RunNoWait(ctx, in); err != nil {
				slog.Error("job-links not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, in.UserID)
				if err := d.unclaimLinks(ctx, in); err != nil {
					return out, err
				}
				continue
			}
			out.Links = append(out.Links, in.UserID)
		}
		return out, err
	},
		hatchet.WithWorkflowDescription("Every quarter-hour, start each job-assistant user's daily job hunt at their time"),
		hatchet.WithWorkflowCron("*/15 * * * *"),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// dueHunts brings in every profile of the web app as a user with a daily
// search time, and returns a job-hunt input for each user whose time has
// come today and who has not searched yet today, and the profiles by user
// id.
func (d Deps) dueHunts(ctx context.Context, now time.Time) (jobSearchOutput, []JobHuntInput, map[string]web.Profile, error) {
	var out jobSearchOutput
	profiles, err := d.Web.Profiles(ctx)
	if err != nil {
		return out, nil, nil, err
	}
	out.Profiles = len(profiles)
	byUser := map[string]web.Profile{}
	for _, p := range profiles {
		email := identity.NormalizeEmail(p.Email)
		if email == "" {
			continue
		}
		id := identity.UserID(email)
		if err := d.Store.UpsertWebUser(ctx, store.User{ID: id, Email: email, DisplayName: p.Name}); err != nil {
			return out, nil, nil, err
		}
		minute, created, err := d.assignSchedule(ctx, id)
		if err != nil {
			return out, nil, nil, err
		}
		if created {
			out.Scheduled = append(out.Scheduled, fmt.Sprintf("%s %02d:%02d", id, minute/60, minute%60))
		}
		byUser[id] = p
	}
	local := now.In(d.Location)
	date := local.Format(time.DateOnly)
	due, err := d.Store.DueSchedules(ctx, local.Hour()*60+local.Minute(), date)
	if err != nil {
		return out, nil, nil, err
	}
	var hunts []JobHuntInput
	for _, id := range due {
		if p, ok := byUser[id]; ok && !p.Paused {
			hunts = append(hunts, JobHuntInput{UserID: id, UID: p.UID, Date: date})
		}
	}
	return out, hunts, byUser, nil
}

// assignSchedule gives the user a daily search time unless they have one,
// and returns it; created is true when it is new.
func (d Deps) assignSchedule(ctx context.Context, userID string) (minute int, created bool, err error) {
	for {
		m, ok, err := d.Store.ScheduleOf(ctx, userID)
		if err != nil || ok {
			return m, false, err
		}
		taken, err := d.Store.ScheduleMinutes(ctx)
		if err != nil {
			return 0, false, err
		}
		m, ok = pickMinute(taken)
		if !ok {
			return 0, false, fmt.Errorf("no free search time for %s", userID)
		}
		saved, err := d.Store.SaveSchedule(ctx, userID, m)
		if err != nil || saved {
			return m, saved, err
		}
		// The minute was taken meanwhile, or the user got a time: look again.
	}
}

// pickMinute picks a random search time on the jobStep grid between
// jobDayStart and jobDayEnd, at least jobGap from every taken one; when
// none is that far, one as far as can be. ok is false when every time is
// taken.
func pickMinute(taken []int) (int, bool) {
	gaps := map[int]int{} // free minute → its distance to the nearest taken one
	widest := 0
	for m := jobDayStart; m < jobDayEnd; m += jobStep {
		if slices.Contains(taken, m) {
			continue
		}
		gap := jobDayEnd - jobDayStart
		for _, t := range taken {
			gap = min(gap, max(m-t, t-m))
		}
		gaps[m], widest = gap, max(widest, gap)
	}
	var picks []int
	for m, gap := range gaps {
		if gap >= min(jobGap, widest) {
			picks = append(picks, m)
		}
	}
	if len(picks) == 0 {
		return 0, false
	}
	slices.Sort(picks)
	return picks[rand.IntN(len(picks))], true
}
