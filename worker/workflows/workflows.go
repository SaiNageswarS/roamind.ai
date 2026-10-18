// Package workflows holds Roamind's Hatchet workflows, one file each: their
// tasks and how they are wired, triggered and limited.
//
//   - chat-turn: one agent turn per Slack message.
//   - goal-channels: a private Slack channel for each new active goal.
//   - learning-exercises: each morning, a learning-exercise run per
//     learning goal.
//   - learning-exercise: check-skip → build-context → draft-exercise → post.
//   - user-setup: memory-project → bm-setup → news-channel, for a new user.
//   - news-shorts: once a day, a news-digest run per user.
//   - news-digest: check-skip → build-context → draft-digest → post.
//   - news-reaction: reactions to digests, as feedback.
//   - job-search: every quarter-hour, a job-hunt run per web user whose
//     daily time has come.
//   - job-hunt: check-skip → resume, find → pick → spawn.
//   - job-links: resume → pick → spawn, for the postings a user linked to,
//     started by the web app's listener (web_listen.go) or after a chat
//     turn, replying where the links came from (job_reply.go).
//   - resume-import: memory → fetch → update, writing a resume
//     uploaded in the web app into the user's memory, also started by the
//     listener.
//   - job-application: check-skip → tailor → publish, per posting.
package workflows

import (
	"context"
	"errors"
	"io"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/feeds"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

// SlackAPI is the Slack client surface the workflows need.
type SlackAPI interface {
	slack.API
	slack.FileAPI
	GetUserInfoContext(ctx context.Context, user string, options ...slackapi.GetUserInfoOption) (*slackapi.User, error)
	CreateConversationContext(ctx context.Context, params slackapi.CreateConversationParams) (*slackapi.Channel, error)
	InviteUsersToConversationContext(ctx context.Context, channelID string, users ...string) (*slackapi.Channel, error)
}

// WebAPI is the web app's data the job assistant needs (web.Client).
type WebAPI interface {
	Profiles(ctx context.Context) ([]web.Profile, error)
	Profile(ctx context.Context, uid string) (*web.Profile, error)
	ResumeVersion(ctx context.Context, path string) (int64, error)
	DownloadResume(ctx context.Context, path string, generation int64, w io.Writer) error
	DeleteResume(ctx context.Context, path string, generation int64) (bool, error)
	SaveApplication(ctx context.Context, uid, id string, a web.Application) error
	ApplicationStatuses(ctx context.Context, uid string, ids []string) (map[string]string, error)
	SetSearch(ctx context.Context, uid string, s web.Search) error
	SetResumeImport(ctx context.Context, uid string, r web.ResumeImport) error
	ClaimLinks(ctx context.Context, uid string, links []web.Link) ([]web.Link, error)
	LinkStatus(ctx context.Context, uid, id string) (string, error)
	SetLink(ctx context.Context, uid, id string, s web.LinkState) (bool, error)
	RequeueStaleLinks(ctx context.Context, uid string, before time.Time) (int, error)
	ListenProfiles(ctx context.Context, fn func(web.Profile)) error
	ListenLinks(ctx context.Context, fn func(uid string, links []web.Link)) error
}

// Deps are the long-lived dependencies of the workflows.
type Deps struct {
	Store *store.Store
	Slack SlackAPI
	Agent agent.Config
	// Memory is the users' long-term memory; without it, chat turns run
	// without memory, users are not set up (so they get no news channel),
	// goals get neither channels nor exercises, and there are no news
	// shorts.
	Memory *notes.Notes
	// Feeds reads the feeds of news interests.
	Feeds *feeds.Client
	// Web is the web app's data; without it, the job assistant serves
	// Slack only: no job hunts, resume uploads or links from the web app.
	// Without Memory, which holds users' resumes, there is no job
	// assistant.
	Web WebAPI
	// Jobs reads job postings from their sites.
	Jobs *jobs.Client
	// JobsPerRun caps the postings a job hunt picks; 0 means JobsPerRun.
	JobsPerRun int
	// Location is the timezone of the daily exercises, news shorts and job
	// searches.
	Location *time.Location
	// Now defaults to time.Now.
	Now func() time.Time
	// SetupUser, when set, starts setting up a user, without waiting;
	// chat-turn calls it for a user who is not set up. Register sets it to
	// start a user-setup run.
	SetupUser func(ctx context.Context, userID string) error
	// AfterTurn, when set, runs after each chat turn with memory. Register
	// sets it to give new goals their channels and to start drafting the
	// job links the user asked for. Its errors are logged.
	AfterTurn func(ctx context.Context, u *store.User, t FinishedTurn) error
}

// FinishedTurn is a chat turn, for AfterTurn: the user's message and its
// thread.
type FinishedTurn struct {
	Text     string
	Channel  string
	ThreadTS string
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Register declares every workflow on the client, for the worker. listen,
// when there is a web app, listens to it and starts the job assistant's
// runs until ctx is done; the worker runs it alongside.
func Register(c *hatchet.Client, d Deps) (wfs []hatchet.WorkflowBase, listen func(ctx context.Context) error) {
	goals := GoalChannels(c, d)
	if d.Memory == nil {
		return append(wfs, ChatTurn(c, d), goals), nil
	}
	application := JobApplication(c, d)
	links := JobLinks(c, d, application)
	wfs = append(wfs, application, links)
	if d.Web != nil {
		hunt := JobHunt(c, d, application)
		resume := ResumeImport(c, d)
		wfs = append(wfs, hunt, resume, JobSearch(c, d, hunt))
		listen = func(ctx context.Context) error { return d.listenWeb(ctx, links, resume) }
	}
	setup := UserSetup(c, d)
	d.SetupUser = func(ctx context.Context, userID string) error {
		_, err := setup.RunNoWait(topLevel(ctx), UserSetupInput{UserID: userID})
		return err
	}
	hooks := []func(context.Context, *store.User, FinishedTurn) error{
		goalChannelsTrigger(goals, d),
		d.startSlackLinks(func(ctx context.Context, in JobLinksInput) error {
			_, err := links.RunNoWait(topLevel(ctx), in)
			return err
		}),
	}
	d.AfterTurn = func(ctx context.Context, u *store.User, t FinishedTurn) error {
		var errs []error
		for _, h := range hooks {
			errs = append(errs, h(ctx, u, t))
		}
		return errors.Join(errs...)
	}
	exercise := LearningExercise(c, d)
	digest := NewsDigest(c, d)
	return append(wfs,
		ChatTurn(c, d), setup, goals, exercise, LearningExercises(c, d, exercise),
		digest, NewsShorts(c, d, digest, setup), NewsReaction(c, d),
	), listen
}

// topLevel makes the runs started with ctx top-level runs rather than
// children of the task ctx belongs to: given a task's hatchet.Context, the
// SDK spawns a child run. So user-setup runs show on their own in the
// dashboard, and outlive the run that started them.
func topLevel(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}
