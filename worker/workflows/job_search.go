package workflows

import (
	"context"
	"log/slog"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
	"roamind.ai/memory/store"
)

// JobSearchName is the cron workflow of the job assistant: at each of
// JobSearchHours it brings in the web app's profiles, starts a job-hunt run
// for every user, and queues again the links a stopped worker left
// processing. A hunt skips itself while the user's last picks wait for
// their review, or the search is paused (check-skip).
const JobSearchName = "job-search"

// JobSearchHours are the hours, in the server's zone (ROAMIND_TZ overrides
// it), of the job searches.
var JobSearchHours = []int{8, 12, 16, 20}

// staleClaim is how long a link may stay processing before it counts as
// left behind: a job-links run marks it done or failed well within.
const staleClaim = time.Hour

// jobSearchOutput lists what a dispatch did.
type jobSearchOutput struct {
	Profiles int      `json:"profiles"`
	Hunts    []string `json:"hunts,omitempty"`    // user ids
	Requeued int      `json:"requeued,omitempty"` // links left processing, queued again
	// Failed are the users whose run could not be started, logged; the
	// others still start.
	Failed []string `json:"failed,omitempty"`
}

// JobSearch declares the cron task that starts job-hunt runs, as child
// runs. A run that can't be started is logged and listed; the user is
// hunted again at the next search.
func JobSearch(c *hatchet.Client, d Deps, hunt *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(JobSearchName, func(ctx hatchet.Context, _ struct{}) (jobSearchOutput, error) {
		out, hunts, users, err := d.hunts(ctx, d.now())
		if err != nil {
			return out, err
		}
		for _, in := range hunts {
			if _, err := hunt.RunNoWait(ctx, in); err != nil {
				slog.Error("job-hunt not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, in.UserID)
				continue
			}
			out.Hunts = append(out.Hunts, in.UserID)
		}
		// Queued again, the web app's listener takes them up.
		for _, p := range users {
			n, err := d.Web.RequeueStaleLinks(ctx, p.UID, d.now().Add(-staleClaim))
			if err != nil {
				return out, err
			}
			out.Requeued += n
		}
		return out, nil
	},
		hatchet.WithWorkflowDescription("A few times a day, start a job hunt for every job-assistant user"),
		hatchet.WithWorkflowCron(LocalCrons(d.Location, time.Now(), JobSearchHours...)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// hunts brings in every profile of the web app as a user, and returns a
// job-hunt input for each user whose search isn't paused, and the profiles
// by user id.
func (d Deps) hunts(ctx context.Context, now time.Time) (jobSearchOutput, []JobHuntInput, map[string]web.Profile, error) {
	var out jobSearchOutput
	profiles, err := d.Web.Profiles(ctx)
	if err != nil {
		return out, nil, nil, err
	}
	out.Profiles = len(profiles)
	byUser := map[string]web.Profile{}
	var hunts []JobHuntInput
	date := now.In(d.Location).Format(time.DateOnly)
	for _, p := range profiles {
		email := identity.NormalizeEmail(p.Email)
		if email == "" {
			continue
		}
		id := identity.UserID(email)
		if err := d.bringIn(ctx, id, email, p); err != nil {
			return out, nil, nil, err
		}
		byUser[id] = p
		if !p.Paused {
			hunts = append(hunts, JobHuntInput{UserID: id, UID: p.UID, Date: date})
		}
	}
	return out, hunts, byUser, nil
}

// bringIn makes the profile's owner a user, as a web user unless they are
// one already.
func (d Deps) bringIn(ctx context.Context, userID, email string, p web.Profile) error {
	return d.Store.UpsertWebUser(ctx, store.User{ID: userID, Email: email, DisplayName: p.Name})
}
