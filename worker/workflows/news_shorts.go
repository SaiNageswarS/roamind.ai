package workflows

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
)

// NewsShortsName is the cron workflow that, at each of NewsHours, starts a
// news-digest run for every user with a news channel, and a news-channels
// run for every user still without one.
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
	Skipped  string   `json:"skipped,omitempty"`
	Slot     string   `json:"slot,omitempty"`
	Digests  []string `json:"digests,omitempty"`  // user ids
	Channels []string `json:"channels,omitempty"` // user ids
}

// NewsShorts declares the cron task that starts digest runs, one per user,
// as child runs.
func NewsShorts(c *hatchet.Client, d Deps, digest *hatchet.Workflow, channels *hatchet.StandaloneTask) *hatchet.StandaloneTask {
	return c.NewStandaloneTask(NewsShortsName, func(ctx hatchet.Context, _ struct{}) (newsDispatchOutput, error) {
		out, digests, missing, err := d.dueNews(ctx, d.now())
		if err != nil {
			return out, err
		}
		for _, in := range missing {
			if _, err := channels.RunNoWait(ctx, in); err != nil {
				return out, fmt.Errorf("news channel for %s: %w", in.UserID, err)
			}
			out.Channels = append(out.Channels, in.UserID)
		}
		for _, in := range digests {
			if _, err := digest.RunNoWait(ctx, in); err != nil {
				return out, fmt.Errorf("digest for %s: %w", in.UserID, err)
			}
			out.Digests = append(out.Digests, in.UserID)
		}
		return out, nil
	},
		hatchet.WithWorkflowDescription(fmt.Sprintf("Every day at %s %s, start a news digest for every user", newsTimes(), d.Location)),
		hatchet.WithWorkflowCron(LocalCrons(d.Location, NewsHours...)...),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(time.Minute),
	)
}

// newsSlot is the digest slot for t: its local date and hour.
func (d Deps) newsSlot(t time.Time) string {
	return t.In(d.Location).Format("2006-01-02 15")
}

// dueNews returns, at one of NewsHours local time, a digest input for each
// user with a news channel, and a news-channels input for each user with
// memory but no news channel.
func (d Deps) dueNews(ctx context.Context, now time.Time) (newsDispatchOutput, []DigestInput, []NewsChannelsInput, error) {
	var out newsDispatchOutput
	local := now.In(d.Location)
	if !slices.Contains(NewsHours, local.Hour()) {
		out.Skipped = fmt.Sprintf("it is %s in %s, not %s", local.Format("15:04"), d.Location, newsTimes())
		return out, nil, nil, nil
	}
	out.Slot = d.newsSlot(now)
	users, err := d.Store.Users(ctx)
	if err != nil {
		return out, nil, nil, err
	}
	var digests []DigestInput
	var missing []NewsChannelsInput
	for _, u := range users {
		if u.MemoryProject == "" {
			continue
		}
		ch, err := d.Store.NewsChannelOf(ctx, u.ID)
		if err != nil {
			return out, nil, nil, err
		}
		if ch == "" {
			missing = append(missing, NewsChannelsInput{UserID: u.ID})
			continue
		}
		digests = append(digests, DigestInput{UserID: u.ID, Slot: out.Slot})
	}
	return out, digests, missing, nil
}
