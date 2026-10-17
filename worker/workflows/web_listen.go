package workflows

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
)

// listenRetry is how long a failed listener waits before it listens again.
const listenRetry = 30 * time.Second

// listenWeb listens to the web app until ctx is done, starting runs as the
// user acts there: a resume-import run for each new resume upload, and a
// job-links run for each user's newly queued links. A listener that fails
// listens again; it first sees everything there already, so nothing queued
// meanwhile is missed.
func (d Deps) listenWeb(ctx context.Context, links, resume *hatchet.Workflow) error {
	startLinks := func(in JobLinksInput) error {
		_, err := links.RunNoWait(ctx, in)
		return err
	}
	startImport := func(in ResumeImportInput) error {
		_, err := resume.RunNoWait(ctx, in)
		return err
	}
	go d.keepListening(ctx, "profiles", func() error {
		return d.Web.ListenProfiles(ctx, func(p web.Profile) { d.onProfile(ctx, p, startImport) })
	})
	d.keepListening(ctx, "links", func() error {
		return d.Web.ListenLinks(ctx, func(uid string, ls []web.Link) { d.onLinks(ctx, uid, ls, startLinks) })
	})
	return ctx.Err()
}

// keepListening runs listen until ctx is done, again after each failure.
func (d Deps) keepListening(ctx context.Context, what string, listen func() error) {
	for ctx.Err() == nil {
		err := listen()
		if ctx.Err() != nil {
			return
		}
		slog.Error("web app listener failed; listening again", "listener", what, "err", err, "in", listenRetry)
		select {
		case <-ctx.Done():
		case <-time.After(listenRetry):
		}
	}
}

// onProfile brings the profile's owner in as a user and, for a resume
// upload not imported yet, starts a resume-import run. The upload is
// recorded first, so the snapshots that the run's own status writes cause
// don't start it again.
func (d Deps) onProfile(ctx context.Context, p web.Profile, start func(ResumeImportInput) error) {
	email := identity.NormalizeEmail(p.Email)
	if email == "" {
		return
	}
	id := identity.UserID(email)
	log := slog.With("user", id, "uid", p.UID)
	if err := d.bringIn(ctx, id, email, p); err != nil {
		log.Error("profile not brought in", "err", err)
		return
	}
	if p.ResumePath == "" {
		return
	}
	gen := p.ResumeGeneration
	if gen == "" {
		v, err := d.Web.ResumeVersion(ctx, p.ResumePath)
		if err != nil {
			log.Error("resume version", "err", err)
			return
		}
		gen = strconv.FormatInt(v, 10)
	}
	ok, err := d.Store.StartResumeImport(ctx, id, gen)
	if err != nil || !ok {
		if err != nil {
			log.Error("resume import not recorded", "err", err)
		}
		return
	}
	if err := start(ResumeImportInput{UserID: id, UID: p.UID, Generation: gen}); err != nil {
		log.Error("resume-import not started", "err", err)
		if err := d.Store.ForgetResumeImport(ctx, id, gen); err != nil {
			log.Error("resume import not forgotten", "err", err)
		}
	}
}

// onLinks claims the user's newly queued links and starts a job-links run
// for each maxLinks of them. Links whose run can't be started stay
// processing: queued again at once, the listener would retry them in a
// loop; the job-search cron queues them again after staleClaim.
func (d Deps) onLinks(ctx context.Context, uid string, links []web.Link, start func(JobLinksInput) error) {
	log := slog.With("uid", uid)
	p, err := d.Web.Profile(ctx, uid)
	if err != nil || p == nil {
		log.Error("links of no profile", "err", err)
		return
	}
	email := identity.NormalizeEmail(p.Email)
	if email == "" {
		return
	}
	id := identity.UserID(email)
	if err := d.bringIn(ctx, id, email, *p); err != nil {
		log.Error("profile not brought in", "err", err)
		return
	}
	for len(links) > 0 {
		batch := links[:min(maxLinks, len(links))]
		links = links[len(batch):]
		claimed, err := d.Web.ClaimLinks(ctx, uid, batch)
		if err != nil {
			log.Error("links not claimed", "err", err)
			continue
		}
		if len(claimed) == 0 {
			continue
		}
		in := JobLinksInput{UserID: id, UID: uid, Date: d.today(), Links: claimed}
		if err := start(in); err != nil {
			log.Error("job-links not started; its links are retried later", "links", len(claimed), "err", err)
		}
	}
}
