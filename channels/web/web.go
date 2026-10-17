// Package web is Roamind's web channel: the job assistant's web app on
// Firebase, which the worker reaches through Firestore and Cloud Storage
// with a service account. The web app writes users' profiles, resumes and
// links to postings; the worker writes the resumes into users' memory, and
// writes their applications and the state of its work. The data contract
// is in docs/web.md.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaxResume caps a resume's size.
const MaxResume = 10 << 20

// Profile is a user's job-search profile, from profiles/{uid}.
type Profile struct {
	UID        string `firestore:"-"`
	Email      string `firestore:"email"`
	Name       string `firestore:"name"`
	ResumePath string `firestore:"resume_path"` // in the bucket
	// ResumeGeneration is the Storage generation of the upload, set by the
	// web app with it; it changes with every upload.
	ResumeGeneration string    `firestore:"resume_generation"`
	Notes            string    `firestore:"notes"`
	Roles            []string  `firestore:"roles"`
	Regions          []string  `firestore:"regions"`
	Paused           bool      `firestore:"paused"`
	UpdatedAt        time.Time `firestore:"updated_at"`
	// ResumeImport is the worker's: how writing the latest upload into the
	// user's memory went; nil before the first.
	ResumeImport *ResumeImport `firestore:"resume_import"`
}

// Application is the job assistant's work for one posting, in
// profiles/{uid}/applications/{id}, for the user to review.
type Application struct {
	Title       string    `firestore:"title"`
	Company     string    `firestore:"company"`
	Location    string    `firestore:"location"`
	URL         string    `firestore:"url"`
	ApplyURL    string    `firestore:"apply_url"`
	Source      string    `firestore:"source"`
	PostedAt    string    `firestore:"posted_at"` // ISO date, or ""
	Fit         Fit       `firestore:"fit"`
	CoverLetter string    `firestore:"cover_letter_md"`
	Answers     []Answer  `firestore:"answers"`
	Status      string    `firestore:"status"`
	CreatedAt   time.Time `firestore:"created_at"`
	// LinkID is the link the user added the posting with, if they did.
	LinkID string `firestore:"link_id,omitempty"`
}

// Fit is how well a posting suits the user.
type Fit struct {
	Score   int      `firestore:"score"` // 0 to 10
	Reasons []string `firestore:"reasons"`
}

// Answer is a drafted answer to an application form's question.
type Answer struct {
	Question string `firestore:"question"`
	Required bool   `firestore:"required"`
	Answer   string `firestore:"answer"`
}

// Statuses of an Application. The worker writes StatusReady; the user
// changes it in the web app.
const (
	StatusReady     = "ready"
	StatusApplied   = "applied"
	StatusDismissed = "dismissed"
)

// Link is a posting the user added in the web app, in
// profiles/{uid}/links/{id}, for the worker to draft an application for.
type Link struct {
	ID        string    `firestore:"-" json:"id"`
	URL       string    `firestore:"url" json:"url"`
	Status    string    `firestore:"status" json:"-"`
	CreatedAt time.Time `firestore:"created_at" json:"-"`
	// ClaimedAt is when the worker took it up.
	ClaimedAt time.Time `firestore:"claimed_at,omitempty" json:"-"`
}

// Sources of a Link: added in the web app, or asked for in Slack, which
// the worker adds for the user.
const (
	SourceWeb   = "web"
	SourceSlack = "slack"
)

// ResumeImport is the state of writing the user's latest resume upload
// into their memory, shown in the web app. Once it is done, the upload is
// deleted: memory holds the resume.
type ResumeImport struct {
	Status     string    `firestore:"status"` // queued, running, done or failed
	Reason     string    `firestore:"reason"`
	Generation string    `firestore:"generation"`
	At         time.Time `firestore:"at"`
}

// Statuses of a Link. The web app writes LinkQueued; the worker the
// others. The user may remove a link unless it is LinkProcessing.
const (
	LinkQueued     = "queued"
	LinkProcessing = "processing"
	LinkDone       = "done"
	LinkFailed     = "failed"
)

// LinkState is how the worker is getting on with a link, for the web app
// to show.
type LinkState struct {
	Status string
	// Reason says why it failed, for the user.
	Reason string
	// ApplicationID is the application drafted from it, once done.
	ApplicationID string
}

// Search is the state of the user's latest job search, shown in the web
// app.
type Search struct {
	Status    string    `firestore:"status"` // running, done, skipped or failed
	Reason    string    `firestore:"reason"`
	Found     int       `firestore:"found"`
	LastRunAt time.Time `firestore:"last_run_at"`
}

// Client reads and writes the web app's data.
type Client struct {
	fs     *firestore.Client
	bucket *storage.BucketHandle
	gcs    *storage.Client
}

// New connects to the Firebase project's Firestore and Storage bucket with
// the application default credentials (GOOGLE_APPLICATION_CREDENTIALS).
func New(ctx context.Context, projectID, bucket string) (*Client, error) {
	fs, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("firestore: %w", err)
	}
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		fs.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}
	return &Client{fs: fs, bucket: gcs.Bucket(bucket), gcs: gcs}, nil
}

// Close closes the connections.
func (c *Client) Close() error {
	return errors.Join(c.fs.Close(), c.gcs.Close())
}

// Profiles returns every profile.
func (c *Client) Profiles(ctx context.Context) ([]Profile, error) {
	it := c.fs.Collection("profiles").Documents(ctx)
	defer it.Stop()
	var out []Profile
	for {
		doc, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var p Profile
		if err := doc.DataTo(&p); err != nil {
			return nil, fmt.Errorf("profile %s: %w", doc.Ref.ID, err)
		}
		p.UID = doc.Ref.ID
		out = append(out, p)
	}
}

// Profile returns the profile of uid, or nil.
func (c *Client) Profile(ctx context.Context, uid string) (*Profile, error) {
	doc, err := c.fs.Collection("profiles").Doc(uid).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := doc.DataTo(&p); err != nil {
		return nil, fmt.Errorf("profile %s: %w", uid, err)
	}
	p.UID = uid
	return &p, nil
}

// ResumeVersion returns the generation of the resume at path, which
// changes whenever the user uploads a new one.
func (c *Client) ResumeVersion(ctx context.Context, path string) (int64, error) {
	attrs, err := c.bucket.Object(path).Attrs(ctx)
	if err != nil {
		return 0, fmt.Errorf("resume %s: %w", path, err)
	}
	if attrs.Size > MaxResume {
		return 0, fmt.Errorf("resume %s: larger than %d MB", path, MaxResume>>20)
	}
	return attrs.Generation, nil
}

// DownloadResume writes the given generation of the resume at path to w.
func (c *Client) DownloadResume(ctx context.Context, path string, generation int64, w io.Writer) error {
	r, err := c.bucket.Object(path).Generation(generation).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("resume %s: %w", path, err)
	}
	defer r.Close()
	_, err = io.Copy(w, io.LimitReader(r, MaxResume))
	return err
}

// DeleteResume deletes the resume at path if it is still the given
// generation; ok is false when a newer upload has replaced it, or it is
// gone.
func (c *Client) DeleteResume(ctx context.Context, path string, generation int64) (bool, error) {
	err := c.bucket.Object(path).If(storage.Conditions{GenerationMatch: generation}).Delete(ctx)
	var gerr *googleapi.Error
	switch {
	case errors.Is(err, storage.ErrObjectNotExist):
		return false, nil
	case errors.As(err, &gerr) && gerr.Code == http.StatusPreconditionFailed:
		return false, nil
	}
	return err == nil, err
}

func (c *Client) applications(uid string) *firestore.CollectionRef {
	return c.fs.Collection("profiles").Doc(uid).Collection("applications")
}

// SaveApplication writes the application with id for uid, unless it is
// there already, so a retry never overwrites the user's review.
func (c *Client) SaveApplication(ctx context.Context, uid, id string, a Application) error {
	_, err := c.applications(uid).Doc(id).Create(ctx, a)
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}

// ApplicationStatuses returns the status of each of uid's applications
// with the given ids; an application not there has none.
func (c *Client) ApplicationStatuses(ctx context.Context, uid string, ids []string) (map[string]string, error) {
	refs := make([]*firestore.DocumentRef, len(ids))
	for i, id := range ids {
		refs[i] = c.applications(uid).Doc(id)
	}
	docs, err := c.fs.GetAll(ctx, refs)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, doc := range docs {
		if !doc.Exists() {
			continue
		}
		if s, err := doc.DataAt("status"); err == nil {
			out[doc.Ref.ID], _ = s.(string)
		}
	}
	return out, nil
}

// SetSearch records the state of uid's job search.
func (c *Client) SetSearch(ctx context.Context, uid string, s Search) error {
	_, err := c.fs.Collection("profiles").Doc(uid).Set(ctx, map[string]any{"search": s}, firestore.Merge([]string{"search"}))
	return err
}

func (c *Client) links(uid string) *firestore.CollectionRef {
	return c.fs.Collection("profiles").Doc(uid).Collection("links")
}

// queuedLinks are the links of every user waiting for the worker.
func (c *Client) queuedLinks() firestore.Query {
	return c.fs.CollectionGroup("links").Where("status", "==", LinkQueued)
}

// ListenLinks calls fn, one call at a time, with each user's newly queued
// links, oldest first: those queued already when it starts, then each one
// as it is added. It returns when ctx is done or the listener fails.
func (c *Client) ListenLinks(ctx context.Context, fn func(uid string, links []Link)) error {
	it := c.queuedLinks().Snapshots(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if err != nil {
			return err
		}
		byUser := map[string][]Link{}
		var uids []string
		for _, ch := range snap.Changes {
			if ch.Kind != firestore.DocumentAdded {
				continue
			}
			uid := ch.Doc.Ref.Parent.Parent.ID
			if byUser[uid] == nil {
				uids = append(uids, uid)
			}
			var l Link
			if err := ch.Doc.DataTo(&l); err != nil {
				return fmt.Errorf("link %s/%s: %w", uid, ch.Doc.Ref.ID, err)
			}
			l.ID = ch.Doc.Ref.ID
			byUser[uid] = append(byUser[uid], l)
		}
		for _, uid := range uids {
			links := byUser[uid]
			slices.SortFunc(links, func(a, b Link) int { return a.CreatedAt.Compare(b.CreatedAt) })
			fn(uid, links)
		}
	}
}

// ListenProfiles calls fn, one call at a time, with each profile: every
// one when it starts, then each one as it changes. It returns when ctx is
// done or the listener fails.
func (c *Client) ListenProfiles(ctx context.Context, fn func(Profile)) error {
	it := c.fs.Collection("profiles").Snapshots(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if err != nil {
			return err
		}
		for _, ch := range snap.Changes {
			if ch.Kind == firestore.DocumentRemoved {
				continue
			}
			var p Profile
			if err := ch.Doc.DataTo(&p); err != nil {
				return fmt.Errorf("profile %s: %w", ch.Doc.Ref.ID, err)
			}
			p.UID = ch.Doc.Ref.ID
			fn(p)
		}
	}
}

// ClaimLinks marks those of uid's links still queued as processing, in one
// transaction, so no link is taken up twice, and returns them.
func (c *Client) ClaimLinks(ctx context.Context, uid string, links []Link) ([]Link, error) {
	var claimed []Link
	err := c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		claimed = nil
		refs := make([]*firestore.DocumentRef, len(links))
		for i, l := range links {
			refs[i] = c.links(uid).Doc(l.ID)
		}
		docs, err := tx.GetAll(refs)
		if err != nil {
			return err
		}
		now := time.Now()
		for i, doc := range docs {
			if !doc.Exists() {
				continue
			}
			if s, _ := doc.Data()["status"].(string); s != LinkQueued {
				continue
			}
			if err := tx.Update(doc.Ref, []firestore.Update{
				{Path: "status", Value: LinkProcessing}, {Path: "claimed_at", Value: now},
			}); err != nil {
				return err
			}
			claimed = append(claimed, links[i])
		}
		return nil
	})
	return claimed, err
}

// RequeueStaleLinks queues again uid's links that have been processing
// since before: a worker that stopped between claiming them and starting
// their run left them behind. It returns how many.
func (c *Client) RequeueStaleLinks(ctx context.Context, uid string, before time.Time) (int, error) {
	docs, err := c.links(uid).Where("status", "==", LinkProcessing).Documents(ctx).GetAll()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, doc := range docs {
		var l Link
		if err := doc.DataTo(&l); err != nil {
			return n, fmt.Errorf("link %s/%s: %w", uid, doc.Ref.ID, err)
		}
		if l.ClaimedAt.After(before) {
			continue
		}
		if _, err := doc.Ref.Update(ctx, []firestore.Update{{Path: "status", Value: LinkQueued}}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// AddLinks queues links to postings for uid, as the web app does, from
// source.
func (c *Client) AddLinks(ctx context.Context, uid, source string, urls []string) error {
	now := time.Now()
	return c.fs.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		for _, u := range urls {
			if err := tx.Create(c.links(uid).NewDoc(), map[string]any{
				"url": u, "status": LinkQueued, "source": source, "created_at": now,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetResumeImport records how writing uid's resume into their memory went.
func (c *Client) SetResumeImport(ctx context.Context, uid string, r ResumeImport) error {
	_, err := c.fs.Collection("profiles").Doc(uid).Set(ctx, map[string]any{"resume_import": r}, firestore.Merge([]string{"resume_import"}))
	return err
}

// LinkStatus returns the status of uid's link id, or "" when the user has
// removed it.
func (c *Client) LinkStatus(ctx context.Context, uid, id string) (string, error) {
	doc, err := c.links(uid).Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	s, _ := doc.Data()["status"].(string)
	return s, nil
}

// SetLink records how the worker is getting on with uid's link id; ok is
// false when the user has removed it.
func (c *Client) SetLink(ctx context.Context, uid, id string, s LinkState) (bool, error) {
	_, err := c.links(uid).Doc(id).Update(ctx, []firestore.Update{
		{Path: "status", Value: s.Status},
		{Path: "reason", Value: s.Reason},
		{Path: "application_id", Value: s.ApplicationID},
	})
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return err == nil, err
}
