// Package web is Roamind's web channel: the job assistant's web app on
// Firebase, which the worker reaches through Firestore and Cloud Storage
// with a service account. The web app writes users' profiles and resumes;
// the worker writes their applications and search status, and drafts the
// postings users link to. The data contract is in docs/web.md.
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MaxResume caps a resume's size.
const MaxResume = 10 << 20

// Profile is a user's job-search profile, from profiles/{uid}.
type Profile struct {
	UID        string    `firestore:"-"`
	Email      string    `firestore:"email"`
	Name       string    `firestore:"name"`
	ResumePath string    `firestore:"resume_path"` // in the bucket
	Notes      string    `firestore:"notes"`
	Roles      []string  `firestore:"roles"`
	Regions    []string  `firestore:"regions"`
	Paused     bool      `firestore:"paused"`
	UpdatedAt  time.Time `firestore:"updated_at"`
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
	CV          string    `firestore:"cv_md"`
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

// QueuedLinks returns up to n of uid's links waiting for the worker,
// oldest first.
func (c *Client) QueuedLinks(ctx context.Context, uid string, n int) ([]Link, error) {
	// Sorted here: ordering the query by created_at would need a
	// composite index.
	docs, err := c.links(uid).Where("status", "==", LinkQueued).Documents(ctx).GetAll()
	if err != nil {
		return nil, err
	}
	out := make([]Link, 0, len(docs))
	for _, doc := range docs {
		var l Link
		if err := doc.DataTo(&l); err != nil {
			return nil, fmt.Errorf("link %s/%s: %w", uid, doc.Ref.ID, err)
		}
		l.ID = doc.Ref.ID
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Link) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out[:min(n, len(out))], nil
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
