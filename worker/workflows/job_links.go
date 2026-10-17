package workflows

import (
	"context"
	"encoding/json"
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
// postings from their sites, has the agent judge their fit and records
// them; spawn starts a job-application run for each. Whenever a user has
// links, it runs: unlike a job hunt, it has no check-skip, doesn't wait for
// the user's reviews and keeps every posting whatever its fit, since the
// user asked for these. The web app's listener claims the links, marking
// them processing, as they are queued, and starts the run.
const JobLinksName = "job-links"

// maxLinks is how many links a run takes at most; more make more runs.
const maxLinks = 20

// Reasons a link failed, for the user.
const (
	linkNoResume  = "Upload your resume in your profile, or tell Roamind about your work in Slack, first."
	linkNotURL    = "This doesn't look like a link to a job posting."
	linkBlocked   = "Postings on this site aren't read."
	linkUnread    = "Couldn't read this posting: it may have closed, or its page needs a login."
	linkFailed    = "Drafting this application failed. Add the link again to retry."
	linkInFlight  = "Being drafted; it will be ready shortly."
	linkDraftedBy = "Already drafted."
)

// JobLinksInput names the user, the local date and the links claimed for
// the run.
type JobLinksInput struct {
	UserID string     `json:"user_id"`
	UID    string     `json:"uid"`
	Date   string     `json:"date"`
	Links  []web.Link `json:"links"`
}

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
// When a task fails for good, the links still processing are marked
// failed.
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

// linksFailed marks failed the run's links still processing: they can't
// be drafted without a resume in memory, else drafting failed.
func (d Deps) linksFailed(ctx context.Context, in JobLinksInput) error {
	reason := linkFailed
	if u, err := d.Store.UserByID(ctx, in.UserID); err == nil && (u == nil || !d.hasResume(u)) {
		reason = linkNoResume
	}
	return d.failProcessingLinks(ctx, in.UID, linkIDs(in.Links), reason)
}

// pickLinks reads the linked postings from their sites, has the agent
// judge their fit, and records and returns them all, whatever their fit.
// A posting that already has an application points its links there.
func (d Deps) pickLinks(ctx context.Context, in JobLinksInput, r huntResume) (linksPick, error) {
	out := linksPick{Failed: map[string]string{}}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return out, fmt.Errorf("profile %s: %v", in.UID, err)
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

	// Postings found for the user before keep their application; those
	// passed over or failed are drafted again.
	var fresh []agent.FoundJob
	for _, fj := range found {
		h := store.URLHash(fj.URL)
		prev, err := d.Store.JobPostingOf(ctx, in.UserID, h)
		if err != nil {
			return out, err
		}
		if prev == nil || prev.Status == store.PostingPassed || prev.Status == store.PostingFailed {
			fresh = append(fresh, fj)
			continue
		}
		reason := linkDraftedBy
		if prev.Status == store.PostingPicked {
			reason = linkInFlight
		}
		if err := d.doneLinks(ctx, in.UID, byURL[fj.URL], h, reason); err != nil {
			return out, err
		}
		out.Done = append(out.Done, byURL[fj.URL]...)
	}

	postings, unread := d.readPostings(ctx, fresh)
	for _, u := range unread {
		for _, id := range byURL[u] {
			out.Failed[id] = linkUnread
		}
	}
	if err := d.failLinks(ctx, in.UID, out.Failed); err != nil {
		return out, err
	}
	if len(postings) == 0 {
		return out, nil
	}

	ranking := agent.JobRanking{UserName: orDefault(p.Name, "the user"), Roles: p.Roles, Regions: p.Regions, Notes: p.Notes, Resume: r.Text}
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
			if err := d.doneLinks(ctx, in.UID, byURL[jp.URL], got.URLHash, linkInFlight); err != nil {
				return out, err
			}
			out.Done = append(out.Done, byURL[jp.URL]...)
			continue
		}
		f := fits["c"+strconv.Itoa(i+1)]
		out.Picked = append(out.Picked, JobApplicationInput{
			UserID: in.UserID, UID: in.UID, URLHash: got.URLHash, Date: in.Date,
			Posting: jp, Score: f.Score, Reasons: f.Reasons, Resume: r.Text, LinkIDs: byURL[jp.URL],
		})
	}
	return out, nil
}

// spawnLinkApplications starts a job-application run for each picked
// posting. A posting whose run could not be started is marked failed, and
// so are its links.
func (d Deps) spawnLinkApplications(ctx context.Context, in JobLinksInput, p linksPick, start func(JobApplicationInput) error) (huntSpawn, error) {
	var out huntSpawn
	for _, a := range p.Picked {
		if err := start(a); err != nil {
			slog.Error("job-application not started", "user", in.UserID, "posting", a.Posting.URL, "err", err)
			out.Failed = append(out.Failed, a.URLHash)
			if err := d.Store.MarkFailed(ctx, in.UserID, a.URLHash); err != nil {
				return out, err
			}
			if err := d.failProcessingLinks(ctx, in.UID, a.LinkIDs, linkFailed); err != nil {
				return out, err
			}
			continue
		}
		out.Started = append(out.Started, a.URLHash)
	}
	return out, nil
}

// doneLinks points the links at the application with id.
func (d Deps) doneLinks(ctx context.Context, uid string, ids []string, appID, reason string) error {
	for _, id := range ids {
		if _, err := d.Web.SetLink(ctx, uid, id, web.LinkState{Status: web.LinkDone, Reason: reason, ApplicationID: appID}); err != nil {
			return err
		}
	}
	return nil
}

// failLinks marks links failed, each with its reason.
func (d Deps) failLinks(ctx context.Context, uid string, reasons map[string]string) error {
	for id, reason := range reasons {
		if _, err := d.Web.SetLink(ctx, uid, id, web.LinkState{Status: web.LinkFailed, Reason: reason}); err != nil {
			return err
		}
	}
	return nil
}

// failProcessingLinks marks failed, with reason, those of the links that
// are still processing; links done meanwhile, or removed, are left alone.
func (d Deps) failProcessingLinks(ctx context.Context, uid string, ids []string, reason string) error {
	failed := map[string]string{}
	for _, id := range ids {
		s, err := d.Web.LinkStatus(ctx, uid, id)
		if err != nil {
			return err
		}
		if s == web.LinkProcessing {
			failed[id] = reason
		}
	}
	return d.failLinks(ctx, uid, failed)
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

// queueSlackLinks queues in the web app the job links the user asked for in
// this chat turn (the submit_job_links tool), for the listener to take up.
// Only links in the user's own message are queued: a page the turn read
// can't make it apply anywhere.
func (d Deps) queueSlackLinks(ctx context.Context, u *store.User, text string) error {
	asked, err := d.Store.TakeLinkRequests(ctx, u.ID)
	if err != nil || len(asked) == 0 {
		return err
	}
	typed := map[string]bool{}
	for _, raw := range typedURL.FindAllString(text, -1) {
		if ref, err := jobs.Parse(raw); err == nil {
			typed[ref.URL] = true
		}
	}
	var urls []string
	for _, raw := range asked {
		ref, err := jobs.Parse(raw)
		if err != nil || !typed[ref.URL] {
			slog.Warn("job link not in the user's message; dropped", "user", u.ID, "url", raw)
			continue
		}
		urls = append(urls, ref.URL)
	}
	if len(urls) == 0 {
		return nil
	}
	uid, err := d.Store.WebProfileOf(ctx, u.ID)
	if err != nil {
		return err
	}
	if uid == "" {
		// The tool told the user to sign in to the web app first.
		return fmt.Errorf("user %s has no web app profile for %d job links", u.ID, len(urls))
	}
	return d.Web.AddLinks(ctx, uid, web.SourceSlack, urls)
}
