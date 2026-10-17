package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"roamind.ai/channels/web"
	"roamind.ai/memory/store"
)

func TestJobLinks(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	const gh = "https://job-boards.greenhouse.io/anthropic/jobs/4461450008"
	const lever = "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd"
	fw.addLink("u1", "l1", gh)
	fw.addLink("u1", "l2", gh+"/application?gh_src=x") // the same posting
	fw.addLink("u1", "l3", "ftp://example.com/job")
	fw.addLink("u1", "l4", "https://de.jobs.example.org/view/1")
	fw.addLink("u1", "l5", "https://job-boards.greenhouse.io/anthropic/jobs/1") // gone
	fw.addLink("u1", "l6", lever)
	d.Agent.BlockedDomains, d.Jobs.Blocked = []string{"jobs.example.org"}, []string{"jobs.example.org"}

	// The listener claims the queued links and starts a run for them.
	claimed := listenLinks(t, d, fw, "u1")
	if len(claimed) != 1 || len(claimed[0].Links) != 6 || claimed[0].UserID != hunt.UserID || claimed[0].Date != hunt.Date {
		t.Fatalf("claimed = %+v", claimed)
	}
	in := claimed[0]
	if fw.link("u1", "l6").Status != web.LinkProcessing {
		t.Fatalf("l6 = %+v", fw.link("u1", "l6"))
	}
	// Claimed links aren't claimed again.
	if again := listenLinks(t, d, fw, "u1"); len(again) != 0 {
		t.Fatalf("claimed again = %+v", again)
	}

	r, err := d.memoryResume(ctx, in.UserID)
	if err != nil {
		t.Fatal(err)
	}
	bin, argsLog := fakeClaudeWith(t, "testdata/rank_jobs.jsonl")
	d.Agent.ClaudeBinary = bin
	pick, err := d.pickLinks(ctx, in, r)
	if err != nil {
		t.Fatal(err)
	}
	// Both postings are kept, the Lever one despite its fit of 4; the
	// greenhouse one once, for both its links.
	if len(pick.Picked) != 2 || pick.Picked[0].Posting.URL != gh || pick.Picked[1].Posting.URL != lever ||
		!reflect.DeepEqual(pick.Picked[0].LinkIDs, []string{"l1", "l2"}) || pick.Picked[1].Score != 4 {
		t.Fatalf("pick = %+v", pick)
	}
	if want := map[string]string{"l3": linkNotURL, "l4": linkBlocked, "l5": linkUnread}; !reflect.DeepEqual(pick.Failed, want) {
		t.Fatalf("failed = %v", pick.Failed)
	}
	if s := fw.link("u1", "l4"); s.Status != web.LinkFailed || s.Reason != linkBlocked {
		t.Fatalf("l4 = %+v", s)
	}
	if args := readStdin(t, argsLog); !strings.Contains(args, "Palantir") {
		t.Fatalf("rank prompt: %s", args)
	}
	if p, _ := d.Store.JobPostingOf(ctx, hunt.UserID, store.URLHash(lever)); p == nil || p.Status != store.PostingPicked {
		t.Fatalf("lever posting = %+v", p)
	}

	var started []JobApplicationInput
	sp, err := d.spawnLinkApplications(ctx, in, pick, func(a JobApplicationInput) error {
		started = append(started, a)
		return nil
	})
	if err != nil || len(sp.Started) != 2 {
		t.Fatalf("spawn = %+v %v", sp, err)
	}

	// Publishing points the links at the application.
	bin, _ = fakeClaudeWith(t, "testdata/tailor_application.jsonl")
	d.Agent.ClaudeBinary = bin
	a := started[0]
	tl, err := d.tailorApplication(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.publishApplication(ctx, a, tl); err != nil {
		t.Fatal(err)
	}
	if app := fw.apps["u1/"+a.URLHash]; app.LinkID != "l1" || app.Status != web.StatusReady {
		t.Fatalf("application = %+v", app)
	}
	for _, id := range []string{"l1", "l2"} {
		if s := fw.link("u1", id); s != (web.LinkState{Status: web.LinkDone, ApplicationID: a.URLHash}) {
			t.Fatalf("%s = %+v", id, s)
		}
	}
	// A failed run fails only the links still processing.
	if err := d.failProcessingLinks(ctx, "u1", linkIDs(in.Links), linkFailed); err != nil {
		t.Fatal(err)
	}
	if fw.link("u1", "l1").Status != web.LinkDone || fw.link("u1", "l6") != (web.LinkState{Status: web.LinkFailed, Reason: linkFailed}) {
		t.Fatalf("after failure: l1 %+v, l6 %+v", fw.link("u1", "l1"), fw.link("u1", "l6"))
	}

	// Links to postings drafted before point at their application, with no
	// new turn; a failed one is drafted again.
	if err := d.Store.MarkFailed(ctx, hunt.UserID, store.URLHash(lever)); err != nil {
		t.Fatal(err)
	}
	fw.addLink("u1", "l7", gh)
	fw.addLink("u1", "l8", lever)
	claimed = listenLinks(t, d, fw, "u1")
	bin, _ = fakeClaudeWith(t, "testdata/rank_jobs.jsonl")
	d.Agent.ClaudeBinary = bin
	again, err := d.pickLinks(ctx, claimed[0], r)
	if err != nil || !slices.Equal(again.Done, []string{"l7"}) || len(again.Picked) != 1 || again.Picked[0].Posting.URL != lever {
		t.Fatalf("again = %+v %v", again, err)
	}
	if s := fw.link("u1", "l7"); s != (web.LinkState{Status: web.LinkDone, Reason: linkDraftedBy, ApplicationID: a.URLHash}) {
		t.Fatalf("l7 = %+v", s)
	}
}

// Without a resume in memory, reading it fails, and the links fail with a
// reason the user can act on.
func TestJobLinksNoResume(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	u, _ := d.Store.UserByID(ctx, hunt.UserID)
	os.Remove(filepath.Join(d.Memory.NotesDir(u.MemoryProject), "people", "Sai Test.md"))
	fw.addLink("u1", "l1", "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd")
	claimed := listenLinks(t, d, fw, "u1")
	if len(claimed) != 1 {
		t.Fatalf("claimed = %+v", claimed)
	}
	if _, err := d.memoryResume(ctx, hunt.UserID); !errors.Is(err, errNoResume) {
		t.Fatalf("resume = %v", err)
	}
	if err := d.linksFailed(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}
	if s := fw.link("u1", "l1"); s != (web.LinkState{Status: web.LinkFailed, Reason: linkNoResume}) {
		t.Fatalf("l1 = %+v", s)
	}
}

// Links whose run can't be started stay processing until job-search queues
// them again, for the listener to take up.
func TestJobLinksNotStarted(t *testing.T) {
	ctx := context.Background()
	d, fw, _ := jobsSetup(t)
	fw.addLink("u1", "l1", "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd")
	d.onLinks(ctx, "u1", fw.queued("u1"), func(JobLinksInput) error { return errors.New("hatchet is down") })
	if s := fw.link("u1", "l1"); s.Status != web.LinkProcessing {
		t.Fatalf("l1 = %+v", s)
	}
	if n, _ := fw.RequeueStaleLinks(ctx, "u1", time.Now().Add(-staleClaim)); n != 0 {
		t.Fatal("a fresh claim was requeued")
	}
	if n, _ := fw.RequeueStaleLinks(ctx, "u1", time.Now().Add(time.Minute)); n != 1 || len(listenLinks(t, d, fw, "u1")) != 1 {
		t.Fatalf("stale claim requeued %d", n)
	}
}

// listenLinks hands the user's queued links to the links listener's
// handler, and returns the runs it started.
func listenLinks(t *testing.T, d Deps, fw *fakeWeb, uid string) []JobLinksInput {
	t.Helper()
	var started []JobLinksInput
	queued := fw.queued(uid)
	if len(queued) == 0 {
		return nil
	}
	d.onLinks(context.Background(), uid, queued, func(in JobLinksInput) error {
		started = append(started, in)
		return nil
	})
	return started
}

// Slack users' links are queued in the web app when they are in the user's
// own message.
func TestQueueSlackLinks(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	u, _ := d.Store.UserByID(ctx, hunt.UserID)
	const lever = "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd"
	if err := d.Store.AddLinkRequests(ctx, u.ID, []string{lever + "?utm_source=slack", "https://evil.example/apply"}); err != nil {
		t.Fatal(err)
	}
	text := "Can you apply to <" + lever + "|this one> for me?"
	if err := d.queueSlackLinks(ctx, u, text); err != nil {
		t.Fatal(err)
	}
	q := fw.queued("u1")
	if len(q) != 1 || q[0].URL != lever {
		t.Fatalf("queued = %+v", q)
	}
	// Requests are taken once.
	if err := d.queueSlackLinks(ctx, u, text); err != nil || len(fw.queued("u1")) != 1 {
		t.Fatalf("again: %v %+v", err, fw.queued("u1"))
	}
}

// A resume upload is imported into memory once, merged into the user's own
// note.
func TestResumeImport(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	p := fw.profiles["u1"]
	p.ResumeGeneration = "7"
	var started []ResumeImportInput
	start := func(in ResumeImportInput) error {
		started = append(started, in)
		return nil
	}
	d.onProfile(ctx, web.Profile{Name: "No email"}, start)
	p.UID = "u1"
	d.onProfile(ctx, p, start)
	// The run's own status writes change the profile again: no second run.
	d.onProfile(ctx, p, start)
	if len(started) != 1 || started[0] != (ResumeImportInput{UserID: hunt.UserID, UID: "u1", Generation: "7"}) {
		t.Fatalf("started = %+v", started)
	}
	in := started[0]

	mp, err := d.importMemory(ctx, in)
	if err != nil || fw.imports["u1"].Status != "running" {
		t.Fatalf("memory = %+v %v %+v", mp, err, fw.imports["u1"])
	}
	text, err := d.readResume(ctx, in.UserID, p.ResumePath, 7)
	if err != nil || !strings.HasPrefix(text, "Sai Test") {
		t.Fatalf("read = %q %v", text, err)
	}
	// Read once per upload.
	os.Remove(d.PDFToText)
	if again, err := d.readResume(ctx, in.UserID, p.ResumePath, 7); err != nil || again != text {
		t.Fatalf("cached read = %v", err)
	}

	bin, argsLog := fakeClaudeWith(t, "testdata/extract_resume.jsonl")
	d.Agent.ClaudeBinary = bin
	ex, err := d.extractResume(ctx, in, importRead{Text: text})
	if err != nil || len(ex.Resume.Experience) != 2 {
		t.Fatalf("extract = %+v %v", ex, err)
	}
	if args, _ := os.ReadFile(argsLog); !strings.Contains(string(args), "--tools  ") {
		t.Fatalf("extract args: %s", args)
	}
	if _, err := d.writeResume(ctx, in, mp, ex); err != nil {
		t.Fatal(err)
	}
	self, err := d.Memory.Self(mp.Project)
	if err != nil || self == nil {
		t.Fatalf("self = %+v %v", self, err)
	}
	for _, want := range []string{
		"- [contact] sai@x.com (stated, slack, 2026-10-11)",
		"- [contact] +49 151 0000 (stated, resume, 2026-10-17)",
		"- [experience] ML Engineer, Acme, 2020-01 to present: ML systems in Go and JAX (stated, resume, 2026-10-17)",
	} {
		if !strings.Contains(self.Body, want) {
			t.Fatalf("self note lacks %q:\n%s", want, self.Body)
		}
	}
	// The resume's roles replace the note's; its permalink stays.
	if strings.Contains(self.Body, "built and shipped") || self.Permalink != "people/sai-test" {
		t.Fatalf("self note =\n%s", self.Body)
	}
	if fw.imports["u1"] != (web.ResumeImport{Status: "done", Generation: "7", At: d.now()}) {
		t.Fatalf("import = %+v", fw.imports["u1"])
	}

	// A web-only user gets a memory project and a note of their own.
	fw.profiles["u2"] = web.Profile{Email: "new@x.com", Name: "New Person", ResumePath: "resumes/u2/resume.pdf", ResumeGeneration: "1"}
	newUser := fw.profiles["u2"]
	newUser.UID = "u2"
	started = nil
	d.onProfile(ctx, newUser, start)
	if len(started) != 1 {
		t.Fatalf("new user started = %+v", started)
	}
	mp2, err := d.importMemory(ctx, started[0])
	if err != nil || mp2.Project == "" || mp2.Project == mp.Project {
		t.Fatalf("new user memory = %+v %v", mp2, err)
	}
	if _, err := d.writeResume(ctx, started[0], mp2, ex); err != nil {
		t.Fatal(err)
	}
	if self, _ := d.Memory.Self(mp2.Project); self == nil || !strings.Contains(self.Body, "ML Engineer, Acme") {
		t.Fatalf("new user's note = %+v", self)
	}
}
