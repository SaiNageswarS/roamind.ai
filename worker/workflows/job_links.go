package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
)

// JobLinksName is the job-links workflow, which drafts applications for
// the postings a user linked to, in the web app or in Slack:
//
//	resume → pick → spawn
//
// resume reads the user's resume from their memory; pick reads the
// postings from their sites and has the agent judge their fit; spawn
// starts a job-application run for each. Whenever a user has links, it
// runs: unlike a job hunt, it has no check-skip, doesn't wait for the
// user's reviews and keeps every posting whatever its fit, since the user
// asked for these.
//
// Results go back where the links came from (jobReply). The web app's
// listener claims the links added there, marking them processing, and
// starts a run with the profile's uid: the postings are recorded, so a
// posting is drafted once, and the applications are reviewed in the web
// app. After a chat turn, the links the user sent in Slack start a run with
// the thread: the applications are posted there, and nothing is recorded,
// since there is no review in Slack. A Slack user needn't use the web app.
const JobLinksName = "job-links"

// maxLinks is how many links a run takes at most; more make more runs.
const maxLinks = 20

// Reasons a link failed, for the user.
const (
	linkNoResume      = "Upload your resume in your profile, or tell Roamind about your work in Slack, first."
	linkNoResumeSlack = "Share your resume with me, or tell me about your work, first."
	linkNotURL        = "This doesn't look like a link to a job posting."
	linkBlocked       = "Postings on this site aren't read."
	linkUnread        = "Couldn't read this posting: it may have closed, or its page needs a login."
	linkFailed        = "Drafting this application failed. Send the link again to retry."
	linkInFlight      = "Being drafted; it will be ready shortly."
	linkDraftedBy     = "Already drafted."
)

// JobLinksInput names the user, where the links came from, the local date
// and the links. Links from Slack have their URL as id.
type JobLinksInput struct {
	UserID string `json:"user_id"`
	// UID is the web app profile the links were added in.
	UID string `json:"uid,omitempty"`
	// Slack is the thread the links were sent in, instead.
	Slack *SlackThread `json:"slack,omitempty"`
	Date  string       `json:"date"`
	Links []web.Link   `json:"links"`
}

func (in JobLinksInput) reply(d Deps) jobReply { return d.jobReply(in.UID, in.Slack) }

// linksPick is pick's result: the applications to write.
type linksPick struct {
	Picked []JobApplicationInput `json:"picked,omitempty"`
	// Done are the links whose posting already has an application.
	Done []string `json:"done,omitempty"`
	// Failed are the links that could not be drafted, with the reason.
	Failed  map[string]string `json:"failed,omitempty"`
	CostUSD float64           `json:"cost_usd,omitempty"`
}

// JobLinks declares the job-links workflow. Runs are serialized per user.
// When a task fails for good, the links not drafted yet are failed.
func JobLinks(c *hatchet.Client, d Deps, application *hatchet.Workflow) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(JobLinksName,
		hatchet.WithWorkflowDescription("Draft applications for the job postings a user linked to"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	resume := wf.NewTask("resume", func(ctx hatchet.Context, in JobLinksInput) (huntResume, error) {
		return d.memoryResume(ctx, in.UserID)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	pick := wf.NewTask("pick", func(ctx hatchet.Context, in JobLinksInput) (linksPick, error) {
		var r huntResume
		if err := ctx.ParentOutput(resume, &r); err != nil {
			return linksPick{}, err
		}
		return d.pickLinks(ctx, in, r)
	},
		hatchet.WithParents(resume),
		// Picking records the postings before it returns them.
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(15*time.Minute),
	)
	wf.NewTask("spawn", func(ctx hatchet.Context, in JobLinksInput) (huntSpawn, error) {
		var p linksPick
		if err := ctx.ParentOutput(pick, &p); err != nil {
			return huntSpawn{}, err
		}
		return d.spawnLinkApplications(ctx, in, p, func(a JobApplicationInput) error {
			_, err := application.RunNoWait(ctx, a)
			return err
		})
	},
		hatchet.WithParents(pick),
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in JobLinksInput) (struct{}, error) {
		return struct{}{}, d.linksFailed(ctx, in)
	})
	return wf
}

// linksFailed fails the run's links not drafted yet: they can't be drafted
// without a resume in memory, else drafting failed.
func (d Deps) linksFailed(ctx context.Context, in JobLinksInput) error {
	reason := linkFailed
	if u, err := d.Store.UserByID(ctx, in.UserID); err == nil && (u == nil || !d.hasResume(u)) {
		reason = linkNoResume
		if in.Slack != nil {
			reason = linkNoResumeSlack
		}
	}
	return in.reply(d).failPending(ctx, linkIDs(in.Links), reason)
}

// pickLinks reads the linked postings from their sites, has the agent
// judge their fit, and returns them all, whatever their fit. For the web
// app, it records them, and a posting that already has an application
// points its links there.
func (d Deps) pickLinks(ctx context.Context, in JobLinksInput, r huntResume) (linksPick, error) {
	out := linksPick{Failed: map[string]string{}}
	reply := in.reply(d)
	ranking, err := d.linksRanking(ctx, in, r)
	if err != nil {
		return out, err
	}

	// Each posting once, by its canonical URL, with the links to it.
	byURL := map[string][]string{}
	var found []agent.FoundJob
	for _, l := range in.Links {
		ref, err := jobs.Parse(l.URL)
		switch {
		case err != nil:
			out.Failed[l.ID] = linkNotURL
		case d.Jobs.Blocks(ref.URL):
			out.Failed[l.ID] = linkBlocked
		default:
			if byURL[ref.URL] == nil {
				found = append(found, agent.FoundJob{URL: ref.URL})
			}
			byURL[ref.URL] = append(byURL[ref.URL], l.ID)
		}
	}

	// Slack keeps no record of postings, so its links are always drafted.
	fresh := found
	if in.Slack == nil {
		if fresh, err = d.undrafted(ctx, in.UserID, found, byURL, reply, &out); err != nil {
			return out, err
		}
	}

	postings, unread := d.readPostings(ctx, fresh)
	for _, u := range unread {
		for _, id := range byURL[u] {
			out.Failed[id] = linkUnread
		}
	}
	if err := reply.fail(ctx, out.Failed); err != nil {
		return out, err
	}
	if len(postings) == 0 {
		return out, nil
	}

	for i, jp := range postings {
		ranking.Candidates = append(ranking.Candidates, agent.JobCandidate{
			ID: "c" + strconv.Itoa(i+1), Title: jp.Title, Company: jp.Company, Location: jp.Location, Posted: jp.Posted, Description: jp.Description,
		})
	}
	prompt, err := agent.RankJobsPrompt(ranking)
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID: in.UserID, Text: prompt, UserName: ranking.UserName,
		// The postings are third-party text: the turn gets no tools.
		Tools: protocol.ToolsNone, OutputSchema: agent.RankJobsSchema,
	}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	var ranked agent.RankedJobs
	if err := json.Unmarshal(turn.Structured, &ranked); err != nil {
		return out, fmt.Errorf("ranking: %w", err)
	}
	fits := map[string]agent.JobFit{}
	for _, f := range ranked.Judgements {
		fits[f.ID] = f
	}

	for i, jp := range postings {
		h := store.URLHash(jp.URL)
		if in.Slack == nil {
			rec := store.JobPosting{UserID: in.UserID, URL: jp.URL, Title: jp.Title, Company: jp.Company, Source: jp.Source}
			got, ok, err := d.Store.PickPosting(ctx, rec)
			if err == nil && !ok {
				got, ok, err = d.Store.RepickPosting(ctx, rec)
			}
			if err != nil {
				return out, err
			}
			if !ok {
				// A job hunt picked it meanwhile.
				if err := reply.done(ctx, byURL[jp.URL], got.URLHash, linkInFlight); err != nil {
					return out, err
				}
				out.Done = append(out.Done, byURL[jp.URL]...)
				continue
			}
			h = got.URLHash
		}
		f := fits["c"+strconv.Itoa(i+1)]
		out.Picked = append(out.Picked, JobApplicationInput{
			UserID: in.UserID, UID: in.UID, Slack: in.Slack, URLHash: h, Date: in.Date,
			UserName: ranking.UserName, Notes: ranking.Notes,
			Posting: jp, Score: f.Score, Reasons: f.Reasons, Resume: r.Text, LinkIDs: byURL[jp.URL],
		})
	}
	return out, nil
}

// undrafted returns the postings found that have no application for the
// web app yet: one found before keeps its application, and its links point
// there; those passed over or failed are drafted again.
func (d Deps) undrafted(ctx context.Context, userID string, found []agent.FoundJob, byURL map[string][]string, reply jobReply, out *linksPick) ([]agent.FoundJob, error) {
	var fresh []agent.FoundJob
	for _, fj := range found {
		h := store.URLHash(fj.URL)
		prev, err := d.Store.JobPostingOf(ctx, userID, h)
		if err != nil {
			return nil, err
		}
		if prev == nil || prev.Status == store.PostingPassed || prev.Status == store.PostingFailed {
			fresh = append(fresh, fj)
			continue
		}
		reason := linkDraftedBy
		if prev.Status == store.PostingPicked {
			reason = linkInFlight
		}
		if err := reply.done(ctx, byURL[fj.URL], h, reason); err != nil {
			return nil, err
		}
		out.Done = append(out.Done, byURL[fj.URL]...)
	}
	return fresh, nil
}

// linksRanking is what the links' fit is judged against: the web app
// profile's roles, regions and notes, or only the resume for links from
// Slack.
func (d Deps) linksRanking(ctx context.Context, in JobLinksInput, r huntResume) (agent.JobRanking, error) {
	if in.Slack != nil {
		u, err := d.Store.UserByID(ctx, in.UserID)
		if err != nil || u == nil {
			return agent.JobRanking{}, fmt.Errorf("user %s: %v", in.UserID, err)
		}
		return agent.JobRanking{UserName: orDefault(u.DisplayName, "the user"), Resume: r.Text}, nil
	}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return agent.JobRanking{}, fmt.Errorf("profile %s: %v", in.UID, err)
	}
	return agent.JobRanking{UserName: orDefault(p.Name, "the user"), Roles: p.Roles, Regions: p.Regions, Notes: p.Notes, Resume: r.Text}, nil
}

// spawnLinkApplications starts a job-application run for each picked
// posting. A posting whose run could not be started fails, and so do its
// links.
func (d Deps) spawnLinkApplications(ctx context.Context, in JobLinksInput, p linksPick, start func(JobApplicationInput) error) (huntSpawn, error) {
	var out huntSpawn
	for _, a := range p.Picked {
		if err := start(a); err != nil {
			slog.Error("job-application not started", "user", in.UserID, "posting", a.Posting.URL, "err", err)
			out.Failed = append(out.Failed, a.URLHash)
			if in.Slack == nil {
				if err := d.Store.MarkFailed(ctx, in.UserID, a.URLHash); err != nil {
					return out, err
				}
			}
			if err := in.reply(d).failPending(ctx, a.LinkIDs, linkFailed); err != nil {
				return out, err
			}
			continue
		}
		out.Started = append(out.Started, a.URLHash)
	}
	return out, nil
}

func linkIDs(links []web.Link) []string {
	ids := make([]string, len(links))
	for i, l := range links {
		ids[i] = l.ID
	}
	return ids
}

// typedURL finds the web addresses in a Slack message, plain or as Slack
// writes links: <https://…> or <https://…|label>.
var typedURL = regexp.MustCompile(`https?://[^\s<>|]+`)

// startSlackLinks starts a job-links run, replying in the turn's thread,
// for the job links the user asked for in the turn (the submit_job_links
// tool). Only links in the user's own message are taken: a page the turn
// read can't make it apply anywhere.
func (d Deps) startSlackLinks(start func(context.Context, JobLinksInput) error) func(context.Context, *store.User, FinishedTurn) error {
	return func(ctx context.Context, u *store.User, t FinishedTurn) error {
		asked, err := d.Store.TakeLinkRequests(ctx, u.ID)
		if err != nil || len(asked) == 0 {
			return err
		}
		typed := map[string]bool{}
		for _, raw := range typedURL.FindAllString(t.Text, -1) {
			if ref, err := jobs.Parse(raw); err == nil {
				typed[ref.URL] = true
			}
		}
		var links []web.Link
		seen := map[string]bool{}
		for _, raw := range asked {
			ref, err := jobs.Parse(raw)
			if err != nil || !typed[ref.URL] {
				slog.Warn("job link not in the user's message; dropped", "user", u.ID, "url", raw)
				continue
			}
			if !seen[ref.URL] {
				seen[ref.URL] = true
				links = append(links, web.Link{ID: ref.URL, URL: ref.URL})
			}
		}
		th := &SlackThread{Channel: t.Channel, ThreadTS: t.ThreadTS}
		var errs []error
		for len(links) > 0 {
			batch := links[:min(maxLinks, len(links))]
			links = links[len(batch):]
			if err := start(ctx, JobLinksInput{UserID: u.ID, Slack: th, Date: d.today(), Links: batch}); err != nil {
				errs = append(errs, err, d.jobReply("", th).failPending(ctx, linkIDs(batch), linkFailed))
			}
		}
		return errors.Join(errs...)
	}
}
