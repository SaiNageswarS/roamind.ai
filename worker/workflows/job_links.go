package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
)

// JobLinksName is the job-links workflow, which drafts applications for
// the postings a user linked to in the web app:
//
//	check-skip → resume → pick → spawn
//
// check-skip needs a profile with a resume; resume reads it as text; pick
// reads the postings from their sites, has the agent judge their fit and
// records them; spawn starts a job-application run for each. Unlike a
// job hunt, it doesn't wait for the user's reviews and keeps every posting
// whatever its fit: the user asked for these. job-search claims the links,
// marking them processing, before it starts the run.
const JobLinksName = "job-links"

// maxLinks is how many links a run takes at most; the rest wait for the
// next quarter-hour.
const maxLinks = 20

// Reasons a link failed, for the user.
const (
	linkNoResume  = "Upload your resume in your profile first."
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

// hunt is the job hunt input for the same user, for the steps the two
// workflows share.
func (in JobLinksInput) hunt() JobHuntInput {
	return JobHuntInput{UserID: in.UserID, UID: in.UID, Date: in.Date}
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

// JobLinks declares the job-links workflow. Runs are serialized per user,
// and every task after check-skip is skipped when it gives a reason. When
// a task fails for good, the links still processing are marked failed.
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
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in JobLinksInput) (applicationCheck, error) {
		return d.checkLinks(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	resume := wf.NewTask("resume", func(ctx hatchet.Context, in JobLinksInput) (huntResume, error) {
		return d.readResume(ctx, in.hunt())
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
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
		hatchet.WithParents(check, resume),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
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
		hatchet.WithParents(check, pick),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in JobLinksInput) (struct{}, error) {
		return struct{}{}, d.failProcessingLinks(ctx, in.UID, linkIDs(in.Links), linkFailed)
	})
	return wf
}

// claimLinks takes up to maxLinks of each user's queued links, marking
// them processing, and returns a job-links input for each user with any.
// users maps user ids to their profiles.
func (d Deps) claimLinks(ctx context.Context, users map[string]web.Profile, date string) ([]JobLinksInput, error) {
	var out []JobLinksInput
	for id, p := range users {
		queued, err := d.Web.QueuedLinks(ctx, p.UID, maxLinks)
		if err != nil {
			return out, fmt.Errorf("links of %s: %w", p.UID, err)
		}
		in := JobLinksInput{UserID: id, UID: p.UID, Date: date}
		for _, l := range queued {
			ok, err := d.Web.SetLink(ctx, p.UID, l.ID, web.LinkState{Status: web.LinkProcessing})
			if err != nil {
				return out, err
			}
			if ok {
				in.Links = append(in.Links, l)
			}
		}
		if len(in.Links) > 0 {
			out = append(out, in)
		}
	}
	return out, nil
}

// unclaimLinks gives back links whose run could not be started, for the
// next quarter-hour.
func (d Deps) unclaimLinks(ctx context.Context, in JobLinksInput) error {
	for _, l := range in.Links {
		if _, err := d.Web.SetLink(ctx, in.UID, l.ID, web.LinkState{Status: web.LinkQueued}); err != nil {
			return err
		}
	}
	return nil
}

// checkLinks needs a profile whose email is the user's, with a resume;
// otherwise the links fail with a reason the user can act on.
func (d Deps) checkLinks(ctx context.Context, in JobLinksInput) (applicationCheck, error) {
	var out applicationCheck
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil {
		return out, err
	}
	reason := linkFailed
	switch {
	case u == nil || p == nil:
		out.Skip = "no such user or profile"
	case identity.UserID(p.Email) != u.ID:
		out.Skip = "the profile's email is not the user's"
	case p.ResumePath == "":
		out.Skip, reason = "no resume uploaded yet", linkNoResume
	}
	if out.Skip != "" {
		return out, d.failProcessingLinks(ctx, in.UID, linkIDs(in.Links), reason)
	}
	return out, nil
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
