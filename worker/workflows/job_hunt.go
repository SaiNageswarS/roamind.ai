package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
)

// JobHuntName is the job-hunt workflow, one user's daily search:
//
//	check-skip → resume, find → pick → spawn
//
// check-skip closes the postings the user is done with and skips the hunt
// while any are still open; resume reads their resume, their own Person
// note in memory; find
// searches the web for postings on applicant tracking sites; pick reads the
// new postings from their sites, has the agent judge their fit and keeps
// the best; spawn starts a job-application run for each. Sites on the
// blocked domains (ROAMIND_JOBS_BLOCKED_DOMAINS) are never read.
const JobHuntName = "job-hunt"

const (
	// JobsPerRun is how many postings a hunt picks at most, by default.
	JobsPerRun = 5
	// minFit is the fit score, 0 to 10, a posting needs to be picked.
	minFit = 6
	// maxFound is how many postings a search returns at most, and
	// maxCandidates how many new ones are read and judged.
	maxFound      = 15
	maxCandidates = 20
	// stalePick is how long a picked posting may wait for its application
	// before it counts as failed.
	stalePick = 12 * time.Hour
)

// JobHuntInput names the user, by user id and web app uid, and the local
// date of a hunt.
type JobHuntInput struct {
	UserID string `json:"user_id"`
	UID    string `json:"uid"`
	Date   string `json:"date"`
}

// huntCheck is check-skip's result; Skip is always present, so the later
// tasks' skip condition can read it.
type huntCheck struct {
	Skip    string   `json:"skip"`
	Closed  []string `json:"closed,omitempty"`  // url hashes closed now
	Expired []string `json:"expired,omitempty"` // stale picks marked failed
}

// huntResume is the user's resume: their own Person note.
type huntResume struct {
	Text string `json:"text"`
}

// huntFound is find's result.
type huntFound struct {
	Postings []agent.FoundJob `json:"postings,omitempty"`
	CostUSD  float64          `json:"cost_usd,omitempty"`
}

// huntPick is pick's result: the applications to write.
type huntPick struct {
	Picked     []JobApplicationInput `json:"picked,omitempty"`
	Candidates int                   `json:"candidates"`
	// Unread are the postings that could not be read from their sites.
	Unread  []string `json:"unread,omitempty"`
	CostUSD float64  `json:"cost_usd,omitempty"`
}

// huntSpawn lists the job-application runs started.
type huntSpawn struct {
	Started []string `json:"started,omitempty"` // url hashes
	Failed  []string `json:"failed,omitempty"`
}

// JobHunt declares the job-hunt workflow. Runs are serialized per user. As in
// news-digest, every task after check-skip lists it as a parent, so each
// one is skipped when check-skip gives a reason. When a task fails, the
// web app is told the search failed.
func JobHunt(c *hatchet.Client, d Deps, application *hatchet.Workflow) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(JobHuntName,
		hatchet.WithWorkflowDescription("Find job postings for one user and start their applications"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in JobHuntInput) (huntCheck, error) {
		return d.checkHunt(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	resume := wf.NewTask("resume", func(ctx hatchet.Context, in JobHuntInput) (huntResume, error) {
		return d.memoryResume(ctx, in.UserID)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	find := wf.NewTask("find", func(ctx hatchet.Context, in JobHuntInput) (huntFound, error) {
		return d.findJobs(ctx, in)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(15*time.Minute),
	)
	pick := wf.NewTask("pick", func(ctx hatchet.Context, in JobHuntInput) (huntPick, error) {
		var r huntResume
		var f huntFound
		if err := ctx.ParentOutput(resume, &r); err != nil {
			return huntPick{}, err
		}
		if err := ctx.ParentOutput(find, &f); err != nil {
			return huntPick{}, err
		}
		return d.pickJobs(ctx, in, r, f)
	},
		hatchet.WithParents(check, resume, find),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// Picking records the postings before it returns them.
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(15*time.Minute),
	)
	wf.NewTask("spawn", func(ctx hatchet.Context, in JobHuntInput) (huntSpawn, error) {
		var p huntPick
		if err := ctx.ParentOutput(pick, &p); err != nil {
			return huntSpawn{}, err
		}
		return d.spawnApplications(ctx, in, p, func(a JobApplicationInput) error {
			_, err := application.RunNoWait(ctx, a)
			return err
		})
	},
		hatchet.WithParents(check, pick),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(0),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in JobHuntInput) (struct{}, error) {
		return struct{}{}, d.Web.SetSearch(ctx, in.UID, web.Search{Status: "failed", Reason: "The search failed; it runs again tomorrow.", LastRunAt: d.now()})
	})
	return wf
}

// checkHunt decides whether the user's hunt runs: they need a resume in
// memory, a profile with roles and regions, and no open postings. It first closes the
// postings they marked applied or dismissed, and those their sites no
// longer have.
func (d Deps) checkHunt(ctx context.Context, in JobHuntInput) (huntCheck, error) {
	var out huntCheck
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil {
		return out, err
	}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil {
		return out, err
	}
	switch {
	case u == nil || p == nil:
		out.Skip = "no such user or profile"
	case identity.UserID(p.Email) != u.ID:
		out.Skip = "the profile's email is not the user's"
	case p.Paused:
		out.Skip = "the search is paused"
	case !d.hasResume(u):
		out.Skip = "no resume in memory yet"
	case len(p.Roles) == 0 || len(p.Regions) == 0:
		out.Skip = "no roles or regions yet"
	}
	if out.Skip != "" {
		return out, d.setSearch(ctx, in.UID, "skipped", out.Skip, 0)
	}

	open, err := d.Store.OpenPostings(ctx, u.ID)
	if err != nil {
		return out, err
	}
	var published []string
	for _, op := range open {
		switch {
		case op.Status == store.PostingPublished:
			published = append(published, op.URLHash)
		case d.now().Sub(op.FoundAt) > stalePick:
			if err := d.Store.MarkFailed(ctx, u.ID, op.URLHash); err != nil {
				return out, err
			}
			out.Expired = append(out.Expired, op.URLHash)
		}
	}
	statuses, err := d.Web.ApplicationStatuses(ctx, in.UID, published)
	if err != nil {
		return out, err
	}
	stillOpen := 0
	for _, op := range open {
		if op.Status != store.PostingPublished {
			if !slices.Contains(out.Expired, op.URLHash) {
				stillOpen++
			}
			continue
		}
		switch statuses[op.URLHash] {
		case web.StatusApplied, web.StatusDismissed:
			out.Closed = append(out.Closed, op.URLHash)
			continue
		}
		if _, err := d.Jobs.Fetch(ctx, op.URL); errors.Is(err, jobs.ErrGone) {
			out.Closed = append(out.Closed, op.URLHash)
			continue
		}
		stillOpen++
	}
	if err := d.Store.ClosePostings(ctx, u.ID, out.Closed); err != nil {
		return out, err
	}
	if stillOpen > 0 {
		out.Skip = fmt.Sprintf("%d postings still open", stillOpen)
		return out, d.setSearch(ctx, in.UID, "skipped", fmt.Sprintf("Review your %d open applications to get new ones.", stillOpen), 0)
	}
	return out, d.setSearch(ctx, in.UID, "running", "", 0)
}

// setSearch tells the web app how the user's search went.
func (d Deps) setSearch(ctx context.Context, uid, status, reason string, found int) error {
	return d.Web.SetSearch(ctx, uid, web.Search{Status: status, Reason: reason, Found: found, LastRunAt: d.now()})
}

// errNoResume is memoryResume's error for a user whose memory holds no
// resume yet.
var errNoResume = errors.New("no resume in memory: no Person note with relationship self")

// memoryResume returns the user's resume: their own Person note, which a
// resume upload in the web app writes and chat turns keep up to date.
func (d Deps) memoryResume(ctx context.Context, userID string) (huntResume, error) {
	u, err := d.Store.UserByID(ctx, userID)
	if err != nil || u == nil {
		return huntResume{}, fmt.Errorf("user %s: %v", userID, err)
	}
	if u.MemoryProject == "" {
		return huntResume{}, errNoResume
	}
	self, err := d.Memory.Self(u.MemoryProject)
	if err != nil {
		return huntResume{}, err
	}
	if self == nil {
		return huntResume{}, errNoResume
	}
	return huntResume{Text: cutText("# "+self.Title+"\n\n"+self.Body, maxResume)}, nil
}

// hasResume reports whether the user's memory holds their resume.
func (d Deps) hasResume(u *store.User) bool {
	if u.MemoryProject == "" {
		return false
	}
	self, err := d.Memory.Self(u.MemoryProject)
	return err == nil && self != nil
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// cutText shortens s to at most n bytes.
func cutText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// findJobs has the agent search the web for postings on applicant tracking
// sites, with WebSearch and WebFetch only.
func (d Deps) findJobs(ctx context.Context, in JobHuntInput) (huntFound, error) {
	var out huntFound
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return out, fmt.Errorf("profile %s: %v", in.UID, err)
	}
	s := agent.JobSearch{
		UserName: orDefault(p.Name, "the user"), Date: in.Date, Roles: p.Roles, Regions: p.Regions,
		Notes: p.Notes, Max: maxFound,
	}
	prompt, err := agent.FindJobsPrompt(s)
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID: in.UserID, Text: prompt, UserName: s.UserName, Tools: protocol.ToolsWeb, OutputSchema: agent.FoundJobsSchema,
	}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	var found agent.FoundJobs
	if err := json.Unmarshal(turn.Structured, &found); err != nil {
		return out, fmt.Errorf("search: %w", err)
	}
	if len(found.Postings) > maxFound {
		found.Postings = found.Postings[:maxFound]
	}
	out.Postings = found.Postings
	return out, nil
}

// pickJobs reads the postings the search found that are new to the user
// from their sites, has the agent judge their fit, and records and returns
// the best JobsPerRun with a fit of at least minFit. Those with a lower fit
// are recorded as passed; good ones beyond JobsPerRun are left for later
// hunts.
func (d Deps) pickJobs(ctx context.Context, in JobHuntInput, r huntResume, f huntFound) (huntPick, error) {
	var out huntPick
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return out, fmt.Errorf("profile %s: %v", in.UID, err)
	}
	found := d.newPostings(ctx, in.UserID, f.Postings)
	postings, unread := d.readPostings(ctx, found)
	out.Unread, out.Candidates = unread, len(postings)
	if len(postings) == 0 {
		return out, d.setSearch(ctx, in.UID, "done", "No new postings today.", 0)
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
	type scored struct {
		posting jobs.Posting
		fit     agent.JobFit
	}
	var best []scored
	for i, jp := range postings {
		f, ok := fits["c"+strconv.Itoa(i+1)]
		switch {
		case ok && f.Score >= minFit:
			best = append(best, scored{jp, f})
		case ok:
			// Not a fit: recorded, so it is not judged again.
			if err := d.Store.PassPosting(ctx, store.JobPosting{
				UserID: in.UserID, URL: jp.URL, Title: jp.Title, Company: jp.Company, Source: jp.Source,
			}); err != nil {
				return out, err
			}
		}
	}
	sort.SliceStable(best, func(i, j int) bool { return best[i].fit.Score > best[j].fit.Score })
	for _, b := range best {
		if len(out.Picked) == d.jobsPerRun() {
			break
		}
		rec, ok, err := d.Store.PickPosting(ctx, store.JobPosting{
			UserID: in.UserID, URL: b.posting.URL, Title: b.posting.Title, Company: b.posting.Company, Source: b.posting.Source,
		})
		if err != nil {
			return out, err
		}
		if !ok {
			continue
		}
		out.Picked = append(out.Picked, JobApplicationInput{
			UserID: in.UserID, UID: in.UID, URLHash: rec.URLHash, Date: in.Date,
			Posting: b.posting, Score: b.fit.Score, Reasons: b.fit.Reasons, Resume: r.Text,
		})
	}
	if len(out.Picked) == 0 {
		return out, d.setSearch(ctx, in.UID, "done", fmt.Sprintf("None of %d new postings was a good enough fit today.", len(postings)), 0)
	}
	return out, nil
}

// newPostings returns the postings found, each once, by its canonical URL,
// and only those not found for the user before and not on a blocked
// domain.
func (d Deps) newPostings(ctx context.Context, userID string, found []agent.FoundJob) []agent.FoundJob {
	var all []agent.FoundJob
	seen := map[string]bool{}
	for _, fj := range found {
		ref, err := jobs.Parse(fj.URL)
		if err != nil || seen[ref.URL] || d.Jobs.Blocks(ref.URL) {
			continue
		}
		seen[ref.URL] = true
		fj.URL = ref.URL
		all = append(all, fj)
	}
	urls := make([]string, len(all))
	for i, fj := range all {
		urls[i] = fj.URL
	}
	unseen, err := d.Store.UnseenPostings(ctx, userID, urls)
	if err != nil {
		slog.Warn("unseen postings", "user", userID, "err", err)
		return nil
	}
	keep := map[string]bool{}
	for _, u := range unseen {
		keep[u] = true
	}
	var out []agent.FoundJob
	for _, fj := range all {
		if keep[fj.URL] && len(out) < maxCandidates {
			out = append(out, fj)
		}
	}
	return out
}

// readPostings reads the postings from their sites, a few at a time. It
// returns those read, in order, and the URLs of those that could not be
// read or are gone.
func (d Deps) readPostings(ctx context.Context, found []agent.FoundJob) ([]jobs.Posting, []string) {
	read := make([]*jobs.Posting, len(found))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, fj := range found {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			jp, err := d.Jobs.Fetch(ctx, fj.URL)
			if err != nil {
				slog.Info("posting not read", "url", fj.URL, "err", err)
				return
			}
			jp.URL = fj.URL // the canonical URL the posting is recorded under
			if jp.Title == "" {
				jp.Title = fj.Title
			}
			if jp.Company == "" || jp.Source == jobs.Other {
				jp.Company = orDefault(fj.Company, jp.Company)
			}
			if jp.Location == "" {
				jp.Location = fj.Location
			}
			read[i] = jp
		}()
	}
	wg.Wait()
	var out []jobs.Posting
	var unread []string
	for i, jp := range read {
		if jp == nil || jp.Description == "" {
			unread = append(unread, found[i].URL)
			continue
		}
		out = append(out, *jp)
	}
	return out, unread
}

func (d Deps) jobsPerRun() int {
	if d.JobsPerRun > 0 {
		return d.JobsPerRun
	}
	return JobsPerRun
}

// spawnApplications starts a job-application run for each picked posting.
// A posting whose run could not be started is marked failed, so it doesn't
// stay open.
func (d Deps) spawnApplications(ctx context.Context, in JobHuntInput, p huntPick, start func(JobApplicationInput) error) (huntSpawn, error) {
	var out huntSpawn
	for _, a := range p.Picked {
		if err := start(a); err != nil {
			slog.Error("job-application not started", "user", in.UserID, "posting", a.Posting.URL, "err", err)
			out.Failed = append(out.Failed, a.URLHash)
			if err := d.Store.MarkFailed(ctx, in.UserID, a.URLHash); err != nil {
				return out, err
			}
			continue
		}
		out.Started = append(out.Started, a.URLHash)
	}
	if len(p.Picked) == 0 {
		return out, nil
	}
	return out, d.setSearch(ctx, in.UID, "done", "", len(out.Started))
}
