package workflows

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"roamind.ai/channels/identity"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

// fakeWeb is the web app's data, in memory.
type fakeWeb struct {
	mu       sync.Mutex
	profiles map[string]web.Profile // by uid
	resumes  map[string]string      // by path
	apps     map[string]web.Application
	searches map[string]web.Search // the latest, by uid
	links    map[string]fakeLink   // by uid/id
	imports  map[string]web.ResumeImport
}

// fakeLink is a link the user added, with what the worker made of it.
type fakeLink struct {
	web.Link
	web.LinkState
}

func newFakeWeb() *fakeWeb {
	return &fakeWeb{profiles: map[string]web.Profile{}, resumes: map[string]string{}, apps: map[string]web.Application{}, searches: map[string]web.Search{}, links: map[string]fakeLink{}, imports: map[string]web.ResumeImport{}}
}

func (f *fakeWeb) Profiles(context.Context) ([]web.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []web.Profile
	for uid, p := range f.profiles {
		p.UID = uid
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b web.Profile) int { return strings.Compare(a.UID, b.UID) })
	return out, nil
}

func (f *fakeWeb) Profile(_ context.Context, uid string) (*web.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.profiles[uid]
	if !ok {
		return nil, nil
	}
	p.UID = uid
	return &p, nil
}

func (f *fakeWeb) ResumeVersion(_ context.Context, path string) (int64, error) {
	return int64(len(f.resumes[path])), nil
}

func (f *fakeWeb) DownloadResume(_ context.Context, path string, _ int64, w io.Writer) error {
	_, err := io.WriteString(w, f.resumes[path])
	return err
}

func (f *fakeWeb) SaveApplication(_ context.Context, uid, id string, a web.Application) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.apps[uid+"/"+id]; !ok {
		f.apps[uid+"/"+id] = a
	}
	return nil
}

func (f *fakeWeb) ApplicationStatuses(_ context.Context, uid string, ids []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, id := range ids {
		if a, ok := f.apps[uid+"/"+id]; ok {
			out[id] = a.Status
		}
	}
	return out, nil
}

func (f *fakeWeb) SetSearch(_ context.Context, uid string, s web.Search) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.searches[uid] = s
	return nil
}

func (f *fakeWeb) ClaimLinks(_ context.Context, uid string, links []web.Link) ([]web.Link, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []web.Link
	for _, l := range links {
		fl, ok := f.links[uid+"/"+l.ID]
		if !ok || fl.LinkState.Status != web.LinkQueued {
			continue
		}
		fl.LinkState.Status, fl.Link.ClaimedAt = web.LinkProcessing, time.Now()
		f.links[uid+"/"+l.ID] = fl
		out = append(out, l)
	}
	return out, nil
}

func (f *fakeWeb) RequeueStaleLinks(_ context.Context, uid string, before time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for key, l := range f.links {
		if strings.HasPrefix(key, uid+"/") && l.LinkState.Status == web.LinkProcessing && !l.ClaimedAt.After(before) {
			l.LinkState.Status = web.LinkQueued
			f.links[key] = l
			n++
		}
	}
	return n, nil
}

func (f *fakeWeb) AddLinks(_ context.Context, uid, source string, urls []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range urls {
		id := source + strconv.Itoa(len(f.links)+1)
		f.links[uid+"/"+id] = fakeLink{web.Link{ID: id, URL: u, CreatedAt: time.Now()}, web.LinkState{Status: web.LinkQueued}}
	}
	return nil
}

func (f *fakeWeb) SetResumeImport(_ context.Context, uid string, r web.ResumeImport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imports[uid] = r
	p := f.profiles[uid]
	p.ResumeImport = &r
	f.profiles[uid] = p
	return nil
}

// The fake's generation of a resume is its length, as ResumeVersion says.
func (f *fakeWeb) DeleteResume(_ context.Context, path string, generation int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.resumes[path]
	if !ok || int64(len(r)) != generation {
		return false, nil
	}
	delete(f.resumes, path)
	return true, nil
}

// The listeners aren't exercised here: tests call their handlers.
func (f *fakeWeb) ListenProfiles(ctx context.Context, _ func(web.Profile)) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeWeb) ListenLinks(ctx context.Context, _ func(string, []web.Link)) error {
	<-ctx.Done()
	return ctx.Err()
}

// queued is uid's queued links, oldest first, as the links listener gives
// them.
func (f *fakeWeb) queued(uid string) []web.Link {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []web.Link
	for key, l := range f.links {
		if strings.HasPrefix(key, uid+"/") && l.LinkState.Status == web.LinkQueued {
			out = append(out, l.Link)
		}
	}
	slices.SortFunc(out, func(a, b web.Link) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

func (f *fakeWeb) LinkStatus(_ context.Context, uid, id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.links[uid+"/"+id].LinkState.Status, nil
}

func (f *fakeWeb) SetLink(_ context.Context, uid, id string, s web.LinkState) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.links[uid+"/"+id]
	if !ok {
		return false, nil
	}
	l.LinkState = s
	f.links[uid+"/"+id] = l
	return true, nil
}

// addLink is the user adding a link in the web app.
func (f *fakeWeb) addLink(uid, id, u string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.links[uid+"/"+id] = fakeLink{web.Link{ID: id, URL: u, CreatedAt: time.Now()}, web.LinkState{Status: web.LinkQueued}}
}

// link is the state of uid's link id.
func (f *fakeWeb) link(uid, id string) web.LinkState {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.links[uid+"/"+id].LinkState
}

// setStatus is the user reviewing an application in the web app.
func (f *fakeWeb) setStatus(uid, id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.apps[uid+"/"+id]
	a.Status = status
	f.apps[uid+"/"+id] = a
}

// atsServer serves the recorded ATS responses of connectors/jobs; the
// returned client sends every host to it.
func atsServer(t *testing.T) *jobs.Client {
	t.Helper()
	fixtures := map[string]string{
		"boards-api.greenhouse.io/v1/boards/anthropic/jobs/4461450008":           "greenhouse.json",
		"api.lever.co/v0/postings/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd": "lever.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := fixtures[r.Host+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("../../connectors/jobs/testdata/" + name)
		if err != nil {
			t.Error(err)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	return &jobs.Client{HTTP: &http.Client{Timeout: 5 * time.Second, Transport: toServer{target}}}
}

// toServer sends requests to target, keeping their Host.
type toServer struct{ target *url.URL }

func (ts toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = r.URL.Host
	r.URL.Scheme, r.URL.Host = ts.target.Scheme, ts.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

// selfNote is the jobsSetup user's own Person note: their resume.
const selfNote = `---
title: Sai Test
type: person
permalink: people/sai-test
---

- [relationship] self (stated, slack, 2026-10-11)
- [contact] sai@x.com (stated, slack, 2026-10-11)
- [experience] ML Engineer, Acme, 2020-01 to present: built and shipped ML systems in Go and JAX (stated, slack, 2026-10-11)
`

// jobsSetup is a job-assistant user with a profile, a resume upload and a
// resume in memory, at 10:00 in Berlin.
func jobsSetup(t *testing.T) (Deps, *fakeWeb, JobHuntInput) {
	t.Helper()
	d, _, _ := setup(t)
	fw := newFakeWeb()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 10, 17, 10, 0, 0, 0, berlin)
	d.Web, d.Jobs, d.Location, d.Now = fw, atsServer(t), berlin, func() time.Time { return now }
	fw.resumes["resumes/u1/resume.pdf"] = "Sai Test, sai@x.com. " + strings.Repeat("Built and shipped ML systems in Go and JAX. ", 10)
	fw.profiles["u1"] = web.Profile{Email: "Sai@X.com", Name: "Sai", ResumePath: "resumes/u1/resume.pdf",
		Roles: []string{"ML Engineer", "Account Executive"}, Regions: []string{"Berlin", "New York"}}
	ctx := context.Background()
	id := identity.UserID("sai@x.com")
	if err := d.Store.UpsertWebUser(ctx, store.User{ID: id, Email: "sai@x.com", DisplayName: "Sai"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Store.SaveWebProfile(ctx, id, "u1"); err != nil {
		t.Fatal(err)
	}
	mem, err := notes.New(t.TempDir(), "../../memory", fakeBM(t))
	if err != nil {
		t.Fatal(err)
	}
	d.Memory, d.Agent.Memory = mem, mem
	p, err := d.assignProject(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.setUpMemory(p); err != nil {
		t.Fatal(err)
	}
	writeSelf(t, d, p.Project, selfNote)
	return d, fw, JobHuntInput{UserID: id, UID: "u1", Date: "2026-10-17"}
}

// writeSelf writes the user's own Person note to project, as the agent
// would.
func writeSelf(t *testing.T, d Deps, project, note string) {
	t.Helper()
	dir := filepath.Join(d.Memory.NotesDir(project), "people")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Sai Test.md"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPickMinute(t *testing.T) {
	// Up to five users are always 90 minutes apart: four times block at
	// most 44 of the 56 grid times.
	var taken []int
	for range 5 {
		m, ok := pickMinute(taken)
		if !ok || m < jobDayStart || m >= jobDayEnd || m%jobStep != 0 {
			t.Fatalf("pick %d after %v: %v", m, taken, ok)
		}
		for _, o := range taken {
			if max(m-o, o-m) < jobGap {
				t.Fatalf("%d is within %d minutes of %d (taken %v)", m, jobGap, o, taken)
			}
		}
		taken = append(taken, m)
	}
	// Past that, times are still distinct.
	for range 45 {
		m, ok := pickMinute(taken)
		if !ok || slices.Contains(taken, m) {
			t.Fatalf("crowded pick %d %v", m, ok)
		}
		taken = append(taken, m)
	}
	for m := jobDayStart; m < jobDayEnd; m += jobStep {
		if !slices.Contains(taken, m) {
			taken = append(taken, m)
		}
	}
	if _, ok := pickMinute(taken); ok {
		t.Fatal("picked from a full day")
	}
}

func TestJobSearchDispatch(t *testing.T) {
	ctx := context.Background()
	d, fw, in := jobsSetup(t)
	// A Slack user who also uses the web app, and a paused new user.
	slackUser := store.User{ID: identity.UserID("kam@x.com"), Email: "kam@x.com", DisplayName: "Kam", SlackTeamID: "T1", SlackUserID: "U2"}
	if err := d.Store.UpsertSlackUser(ctx, slackUser); err != nil {
		t.Fatal(err)
	}
	fw.profiles["u2"] = web.Profile{Email: "kam@x.com", Name: "Kam on the web"}
	fw.profiles["u3"] = web.Profile{Email: "new@x.com", Name: "New", Paused: true}
	fw.profiles["u4"] = web.Profile{Name: "No email"}

	out, _, _, err := d.dueHunts(ctx, d.now())
	if err != nil || out.Profiles != 4 || len(out.Scheduled) != 3 {
		t.Fatalf("dispatch = %+v, %v", out, err)
	}
	if u, _ := d.Store.UserByID(ctx, slackUser.ID); u.Channel != store.ChannelSlack || u.DisplayName != "Kam" {
		t.Fatalf("slack user = %+v", u)
	}
	if u, _ := d.Store.UserByID(ctx, identity.UserID("new@x.com")); u == nil || u.Channel != store.ChannelWeb {
		t.Fatalf("web user = %+v", u)
	}
	minutes, _ := d.Store.ScheduleMinutes(ctx)
	for i := 1; i < len(minutes); i++ {
		if minutes[i]-minutes[i-1] < jobGap {
			t.Fatalf("times too close: %v", minutes)
		}
	}
	// Again: everyone keeps their time.
	if out, _, _, _ := d.dueHunts(ctx, d.now()); len(out.Scheduled) != 0 {
		t.Fatalf("rescheduled: %v", out.Scheduled)
	}

	// At 23:00 every unpaused user is due, once a day.
	late := time.Date(2026, 10, 17, 23, 0, 0, 0, d.Location)
	_, due, _, err := d.dueHunts(ctx, late)
	if err != nil || len(due) != 2 {
		t.Fatalf("due = %+v, %v", due, err)
	}
	for _, h := range due {
		if h.Date != "2026-10-17" || (h.UID != "u1" && h.UID != "u2") || (h.UID == "u1" && h.UserID != in.UserID) {
			t.Fatalf("hunt = %+v", h)
		}
		if err := d.Store.MarkRan(ctx, h.UserID, h.Date); err != nil {
			t.Fatal(err)
		}
	}
	if _, due, _, _ := d.dueHunts(ctx, late); len(due) != 0 {
		t.Fatalf("due again = %+v", due)
	}
	if _, due, _, _ := d.dueHunts(ctx, late.Add(24*time.Hour)); len(due) != 2 {
		t.Fatalf("due tomorrow = %+v", due)
	}
}

func TestJobHunt(t *testing.T) {
	ctx := context.Background()
	d, fw, in := jobsSetup(t)

	// Without a resume in memory the hunt is skipped, and the web app says
	// why.
	u, _ := d.Store.UserByID(ctx, in.UserID)
	selfPath := filepath.Join(d.Memory.NotesDir(u.MemoryProject), "people", "Sai Test.md")
	os.Rename(selfPath, selfPath+".away")
	if chk, err := d.checkHunt(ctx, in); err != nil || chk.Skip != "no resume in memory yet" || fw.searches["u1"].Status != "skipped" {
		t.Fatalf("no resume: %+v %v %+v", chk, err, fw.searches["u1"])
	}
	os.Rename(selfPath+".away", selfPath)
	// A profile whose email is another user's doesn't run as this one.
	if chk, _ := d.checkHunt(ctx, JobHuntInput{UserID: identity.UserID("other@x.com"), UID: "u1"}); chk.Skip == "" {
		t.Fatal("mismatched profile ran")
	}
	chk, err := d.checkHunt(ctx, in)
	if err != nil || chk.Skip != "" || fw.searches["u1"].Status != "running" {
		t.Fatalf("check = %+v %v", chk, err)
	}

	r, err := d.memoryResume(ctx, in.UserID)
	if err != nil || !strings.HasPrefix(r.Text, "# Sai Test\n\n- [relationship] self") || !strings.Contains(r.Text, "ML Engineer, Acme") {
		t.Fatalf("resume = %+v %v", r, err)
	}

	// A blocked domain is neither fetched by the search nor read after.
	d.Agent.BlockedDomains, d.Jobs.Blocked = []string{"jobs.example.org"}, []string{"jobs.example.org"}
	bin, argsLog := fakeClaudeWith(t, "testdata/find_jobs.jsonl")
	d.Agent.ClaudeBinary = bin
	found, err := d.findJobs(ctx, in)
	if err != nil || len(found.Postings) != 6 {
		t.Fatalf("find = %+v %v", found, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--tools WebSearch,WebFetch") || strings.Contains(string(args), "--mcp-config") ||
		!strings.Contains(string(args), `"deny":["WebFetch(domain:jobs.example.org)","WebFetch(domain:*.jobs.example.org)"]`) {
		t.Fatalf("find args: %s", args)
	}

	bin, argsLog = fakeClaudeWith(t, "testdata/rank_jobs.jsonl")
	d.Agent.ClaudeBinary = bin
	pick, err := d.pickJobs(ctx, in, r, found)
	if err != nil {
		t.Fatal(err)
	}
	var picked []string
	for _, a := range pick.Picked {
		picked = append(picked, a.Posting.URL)
	}
	// The duplicate, the broken URL and the blocked posting are dropped,
	// the closed one is unread, and the Lever one is not a fit.
	if pick.Candidates != 2 || !reflect.DeepEqual(picked, []string{"https://job-boards.greenhouse.io/anthropic/jobs/4461450008"}) ||
		!reflect.DeepEqual(pick.Unread, []string{"https://job-boards.greenhouse.io/anthropic/jobs/1"}) {
		t.Fatalf("pick = %+v", pick)
	}
	if unseen, _ := d.Store.UnseenPostings(ctx, in.UserID, []string{"https://de.jobs.example.org/view/4012345678"}); len(unseen) != 1 {
		t.Fatal("a blocked posting was recorded")
	}
	gh := pick.Picked[0]
	if gh.Score != 8 || len(gh.Posting.Questions) != 10 || gh.Resume != r.Text || gh.URLHash != store.URLHash(gh.Posting.URL) {
		t.Fatalf("greenhouse pick = %+v", gh)
	}
	args, _ = os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--tools  ") || strings.Contains(string(args), "--mcp-config") {
		t.Fatalf("rank args: %s", args)
	}

	var started []JobApplicationInput
	sp, err := d.spawnApplications(ctx, in, pick, func(a JobApplicationInput) error {
		started = append(started, a)
		return nil
	})
	if err != nil || len(sp.Started) != 1 || fw.searches["u1"] != (web.Search{Status: "done", Found: 1, LastRunAt: d.now()}) {
		t.Fatalf("spawn = %+v %v %+v", sp, err, fw.searches["u1"])
	}

	// The applications.
	bin, argsLog = fakeClaudeWith(t, "testdata/tailor_application.jsonl")
	d.Agent.ClaudeBinary = bin
	for _, a := range started {
		if c, err := d.checkApplication(ctx, a); err != nil || c.Skip != "" {
			t.Fatalf("check application = %+v %v", c, err)
		}
		tl, err := d.tailorApplication(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.publishApplication(ctx, a, tl); err != nil {
			t.Fatal(err)
		}
		// A retry changes nothing.
		if _, err := d.publishApplication(ctx, a, tl); err != nil {
			t.Fatal(err)
		}
		if c, _ := d.checkApplication(ctx, a); c.Skip != "the posting is published" {
			t.Fatalf("published check = %+v", c)
		}
	}
	stdin := readStdin(t, argsLog)
	if !strings.Contains(stdin, "- Why Anthropic? (required), textarea") || !strings.Contains(stdin, "sai@x.com") {
		t.Fatalf("tailor prompt: %s", stdin)
	}
	app := fw.apps["u1/"+gh.URLHash]
	if app.Status != web.StatusReady || !strings.HasPrefix(app.CoverLetter, "Dear") || len(app.Answers) != 2 ||
		app.ApplyURL != "https://job-boards.greenhouse.io/anthropic/jobs/4461450008" || app.Fit.Score != 8 || app.Source != jobs.Greenhouse {
		t.Fatalf("application = %+v", app)
	}

	// The next day's hunt waits for the user's review.
	if chk, _ := d.checkHunt(ctx, in); chk.Skip != "1 postings still open" {
		t.Fatalf("open check = %+v", chk)
	}
	fw.setStatus("u1", gh.URLHash, web.StatusApplied)
	if chk, _ := d.checkHunt(ctx, in); chk.Skip != "" || len(chk.Closed) != 1 {
		t.Fatalf("reviewed = %+v", chk)
	}
	// Postings found before, picked or not a fit, are never judged again.
	bin, _ = fakeClaudeWith(t, "testdata/rank_jobs.jsonl")
	d.Agent.ClaudeBinary = bin
	if again, err := d.pickJobs(ctx, in, r, found); err != nil || again.Candidates != 0 {
		t.Fatalf("picked again = %+v %v", again, err)
	}
}

// A picked posting whose application never came stops blocking after
// stalePick, and a published one its site no longer has is closed.
func TestJobHuntStale(t *testing.T) {
	ctx := context.Background()
	d, _, in := jobsSetup(t)
	stuck, _, err := d.Store.PickPosting(ctx, store.JobPosting{UserID: in.UserID, URL: "https://jobs.lever.co/acme/00000000-0000-0000-0000-000000000001", Title: "Stuck", Source: jobs.Lever})
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := d.Store.PickPosting(ctx, store.JobPosting{UserID: in.UserID, URL: "https://job-boards.greenhouse.io/anthropic/jobs/1", Title: "Gone", Source: jobs.Greenhouse})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Store.MarkPublished(ctx, in.UserID, gone.URLHash); err != nil {
		t.Fatal(err)
	}
	if chk, _ := d.checkHunt(ctx, in); chk.Skip != "1 postings still open" || !reflect.DeepEqual(chk.Closed, []string{gone.URLHash}) {
		t.Fatalf("fresh pick = %+v", chk)
	}
	// Postings are stamped with the real clock.
	later := time.Now().Add(stalePick + time.Hour)
	d.Now = func() time.Time { return later }
	if chk, _ := d.checkHunt(ctx, in); chk.Skip != "" || !reflect.DeepEqual(chk.Expired, []string{stuck.URLHash}) {
		t.Fatalf("stale pick = %+v", chk)
	}
}

// readStdin returns the prompts the fake claude next to argsLog was given.
func readStdin(t *testing.T, argsLog string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	return string(b)
}
