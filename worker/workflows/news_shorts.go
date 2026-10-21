package workflows

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// NewsShortsName is the cron workflow that, at each of NewsHours, starts a
// news-digest run for every user with a news channel, and a user-setup run
// for every user still without one.
const NewsShortsName = "news-shorts"

// NewsHours are the hours, in the server's zone (ROAMIND_TZ overrides it),
// at which digests are posted: once a day, at 16:00, for now.
var NewsHours = []int{16}

// newsTimes is NewsHours as clock times, such as "8:00, 12:00".
func newsTimes() string {
	times := make([]string, len(NewsHours))
	for i, h := range NewsHours {
		times[i] = fmt.Sprintf("%d:00", h)
	}
	return strings.Join(times, ", ")
}

// newsDispatchOutput lists the runs started.
type newsDispatchOutput struct {
	Slot    string   `json:"slot,omitempty"`
	Digests []string `json:"digests,omitempty"` // user ids
	Setups  []string `json:"setups,omitempty"`  // user ids
	// Failed are the runs that could not be started, logged; the others
	// still start.
	Failed []string `json:"failed,omitempty"`
}

// NewsShorts declares the cron task that starts digest runs, one per user,
// as child runs, and user-setup runs, on their own (see topLevel), for
// users without a news channel. A run that can't be started is logged and
// listed; the others still start.
func NewsShorts(c *hatchet.Client, d Deps, digest, setup *hatchet.Workflow) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(NewsShortsName, func(ctx hatchet.Context, _ struct{}) (newsDispatchOutput, error) {
		out, digests, missing, err := d.dueNews(ctx, d.now())
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
				slog.Error("news-digest not started", "user", in.UserID, "err", err)
				out.Failed = append(out.Failed, "news-digest "+in.UserID)
				continue
			}
			out.Digests = append(out.Digests, in.UserID)
		}
		return out, nil
	},
		hatchet.WithWorkflowDescription(fmt.Sprintf("Every day at %s %s, start a news digest for every user", newsTimes(), d.Location)),
		hatchet.WithWorkflowCron(LocalCrons(d.Location, time.Now(), NewsHours...)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// newsSlot is the digest slot for t: its local date and hour.
func (d Deps) newsSlot(t time.Time) string {
	return t.In(d.Location).Format("2006-01-02 15")
}

// dueNews returns a digest input for each user with a news channel, for
// the slot of now, and a user-setup input for each user without one.
func (d Deps) dueNews(ctx context.Context, now time.Time) (newsDispatchOutput, []DigestInput, []UserSetupInput, error) {
	var out newsDispatchOutput
	out.Slot = d.newsSlot(now)
	users, err := d.Store.Users(ctx)
	if err != nil {
		return out, nil, nil, err
	}
	var digests []DigestInput
	var missing []UserSetupInput
	for _, u := range users {
		ch, err := d.Store.NewsChannelOf(ctx, u.ID)
		if err != nil {
			return out, nil, nil, err
		}
		if ch == "" {
			missing = append(missing, UserSetupInput{UserID: u.ID})
			continue
		}
		digests = append(digests, DigestInput{UserID: u.ID, Slot: out.Slot})
	}
	return out, digests, missing, nil
}
