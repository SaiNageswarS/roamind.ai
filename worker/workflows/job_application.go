package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
)

// JobApplicationName is the job-application workflow, which writes one
// posting's application for the user to review in the web app:
//
//	check-skip → tailor → publish
//
// tailor has the agent write a CV, a cover letter and answers to the
// application form's questions; publish puts them in the web app, and
// points the links the user added the posting with at it. When a task
// fails for good, the posting is marked failed, so it doesn't keep the
// user's next hunt waiting, and so are its links.
const JobApplicationName = "job-application"

// JobApplicationInput is a picked posting, with what its application is
// written from.
type JobApplicationInput struct {
	UserID  string       `json:"user_id"`
	UID     string       `json:"uid"`
	URLHash string       `json:"url_hash"`
	Date    string       `json:"date"`
	Posting jobs.Posting `json:"posting"`
	Score   int          `json:"score"`
	Reasons []string     `json:"reasons,omitempty"`
	Resume  string       `json:"resume"`
	// LinkIDs are the links the user added the posting with, if any.
	LinkIDs []string `json:"link_ids,omitempty"`
}

// applicationCheck is check-skip's result.
type applicationCheck struct {
	Skip string `json:"skip"`
}

// applicationTailor is tailor's result.
type applicationTailor struct {
	Application agent.TailoredApplication `json:"application"`
	CostUSD     float64                   `json:"cost_usd,omitempty"`
}

// applicationPublish is publish's result.
type applicationPublish struct {
	ID string `json:"id"`
}

// JobApplication declares the job-application workflow.
func JobApplication(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(JobApplicationName,
		hatchet.WithWorkflowDescription("Write a CV, cover letter and form answers for one job posting"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	check := wf.NewTask("check-skip", func(ctx hatchet.Context, in JobApplicationInput) (applicationCheck, error) {
		return d.checkApplication(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	tailor := wf.NewTask("tailor", func(ctx hatchet.Context, in JobApplicationInput) (applicationTailor, error) {
		return d.tailorApplication(ctx, in)
	},
		hatchet.WithParents(check),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
	wf.NewTask("publish", func(ctx hatchet.Context, in JobApplicationInput) (applicationPublish, error) {
		var t applicationTailor
		if err := ctx.ParentOutput(tailor, &t); err != nil {
			return applicationPublish{}, err
		}
		return d.publishApplication(ctx, in, t)
	},
		hatchet.WithParents(check, tailor),
		hatchet.WithSkipIf(hatchet.ParentCondition(check, skipped)),
		// Publishing never overwrites an application, so it can retry.
		hatchet.WithRetries(3),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in JobApplicationInput) (struct{}, error) {
		if err := d.Store.MarkFailed(ctx, in.UserID, in.URLHash); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, d.failProcessingLinks(ctx, in.UID, in.LinkIDs, linkFailed)
	})
	return wf
}

// checkApplication skips a posting that is no longer waiting for its
// application.
func (d Deps) checkApplication(ctx context.Context, in JobApplicationInput) (applicationCheck, error) {
	p, err := d.Store.JobPostingOf(ctx, in.UserID, in.URLHash)
	if err != nil {
		return applicationCheck{}, err
	}
	switch {
	case p == nil:
		return applicationCheck{Skip: "no such posting"}, nil
	case p.Status != store.PostingPicked:
		return applicationCheck{Skip: "the posting is " + p.Status}, nil
	}
	return applicationCheck{}, nil
}

// tailorApplication has the agent write the application from the resume,
// the user's notes and the posting.
func (d Deps) tailorApplication(ctx context.Context, in JobApplicationInput) (applicationTailor, error) {
	var out applicationTailor
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return out, fmt.Errorf("profile %s: %v", in.UID, err)
	}
	t := agent.Tailoring{
		UserName: orDefault(p.Name, orDefault(u.DisplayName, "the user")), UserEmail: u.Email, Date: in.Date,
		Notes: p.Notes, Resume: in.Resume,
		Posting: agent.JobCandidate{Title: in.Posting.Title, Company: in.Posting.Company, Location: in.Posting.Location,
			Posted: in.Posting.Posted, Description: in.Posting.Description},
	}
	for _, q := range in.Posting.Questions {
		t.Questions = append(t.Questions, agent.FormQuestion{Label: q.Label, Required: q.Required, Kind: q.Kind, Options: q.Options})
	}
	prompt, err := agent.TailorApplicationPrompt(t)
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID: in.UserID, Text: prompt, UserName: t.UserName, UserEmail: u.Email,
		// The posting is third-party text: the turn gets no tools.
		Tools: protocol.ToolsNone, OutputSchema: agent.TailorApplicationSchema,
	}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(turn.Structured, &out.Application); err != nil {
		return out, fmt.Errorf("application: %w", err)
	}
	if strings.TrimSpace(out.Application.CV) == "" || strings.TrimSpace(out.Application.CoverLetter) == "" {
		return out, fmt.Errorf("the application has no CV or cover letter")
	}
	return out, nil
}

// publishApplication puts the application in the web app, under the
// posting's url hash, points the posting's links at it, then records the
// posting as published.
func (d Deps) publishApplication(ctx context.Context, in JobApplicationInput, t applicationTailor) (applicationPublish, error) {
	a := web.Application{
		Title: in.Posting.Title, Company: in.Posting.Company, Location: in.Posting.Location,
		URL: in.Posting.URL, ApplyURL: orDefault(in.Posting.ApplyURL, in.Posting.URL), Source: in.Posting.Source,
		PostedAt: in.Posting.Posted, Fit: web.Fit{Score: in.Score, Reasons: in.Reasons},
		CV: t.Application.CV, CoverLetter: t.Application.CoverLetter,
		Status: web.StatusReady, CreatedAt: d.now(),
	}
	if len(in.LinkIDs) > 0 {
		a.LinkID = in.LinkIDs[0]
	}
	for _, ans := range t.Application.Answers {
		a.Answers = append(a.Answers, web.Answer{Question: ans.Question, Required: ans.Required, Answer: ans.Answer})
	}
	if err := d.Web.SaveApplication(ctx, in.UID, in.URLHash, a); err != nil {
		return applicationPublish{}, err
	}
	if err := d.doneLinks(ctx, in.UID, in.LinkIDs, in.URLHash, ""); err != nil {
		return applicationPublish{}, err
	}
	if err := d.Store.MarkPublished(ctx, in.UserID, in.URLHash); err != nil {
		return applicationPublish{}, err
	}
	slog.Info("job application published", "user", in.UserID, "posting", in.Posting.URL, "score", in.Score)
	return applicationPublish{ID: in.URLHash}, nil
}
