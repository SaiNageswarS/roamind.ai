package workflows

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"roamind.ai/agent"
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

// A resume upload is imported into memory once, by the turn the user would
// have sharing it in Slack, and then deleted.
func TestResumeImport(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	const path = "resumes/u1/resume.pdf"
	gen := strconv.Itoa(len(fw.resumes[path])) // the fake's generation
	p := fw.profiles["u1"]
	p.ResumeGeneration = gen
	fw.profiles["u1"] = p
	var started []ResumeImportInput
	start := func(in ResumeImportInput) error {
		started = append(started, in)
		return nil
	}
	profile := func(uid string) web.Profile {
		p := fw.profiles[uid]
		p.UID = uid
		return p
	}
	d.onProfile(ctx, web.Profile{Name: "No email"}, start)
	d.onProfile(ctx, profile("u1"), start)
	if fw.imports["u1"].Status != "queued" {
		t.Fatalf("import = %+v", fw.imports["u1"])
	}
	// The snapshot of its own queued write starts no second run.
	d.onProfile(ctx, profile("u1"), start)
	if len(started) != 1 || started[0] != (ResumeImportInput{UserID: hunt.UserID, UID: "u1", Generation: gen}) {
		t.Fatalf("started = %+v", started)
	}
	in := started[0]

	mp, err := d.importMemory(ctx, in)
	if err != nil || fw.imports["u1"].Status != "running" {
		t.Fatalf("memory = %+v %v %+v", mp, err, fw.imports["u1"])
	}
	// The PDF lands in the workspace's uploads, as Slack files do, once.
	f, err := d.fetchResume(ctx, in)
	if err != nil || f.Path != "uploads/resume-"+gen+".pdf" {
		t.Fatalf("fetch = %+v %v", f, err)
	}
	ws, _ := agent.WorkspaceDir(d.Agent, in.UserID)
	if b, _ := os.ReadFile(filepath.Join(ws, f.Path)); !strings.HasPrefix(string(b), "Sai Test") {
		t.Fatalf("saved resume = %q", b)
	}
	if again, err := d.fetchResume(ctx, in); err != nil || again != f {
		t.Fatalf("fetched again = %+v %v", again, err)
	}

	bin, argsLog := fakeClaudeWith(t, "testdata/import_resume.jsonl")
	d.Agent.ClaudeBinary = bin
	up, err := d.updateSelf(ctx, in, mp, f)
	if err != nil || !strings.HasPrefix(up.Reply, "Updated your note") || !up.Deleted {
		t.Fatalf("update = %+v %v", up, err)
	}
	args, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(args), "--mcp-config") || !strings.Contains(string(args), "bm-u-sai-1") ||
		!strings.Contains(string(args), "here through the job assistant's web app") || !strings.Contains(string(args), "({how}, web, ") {
		t.Fatalf("update args: %s", args)
	}
	stdin := readStdin(t, argsLog)
	if !strings.HasPrefix(stdin, importMessage) || !strings.Contains(stdin, "- uploads/resume-"+gen+".pdf (application/pdf") {
		t.Fatalf("update message: %s", stdin)
	}
	if fw.imports["u1"] != (web.ResumeImport{Status: "done", Generation: gen, At: d.now()}) {
		t.Fatalf("import = %+v", fw.imports["u1"])
	}
	if _, ok := fw.resumes[path]; ok {
		t.Fatal("the imported upload is still there")
	}
	// Done, the profile's next snapshots start nothing.
	if d.onProfile(ctx, profile("u1"), start); len(started) != 1 {
		t.Fatalf("started again = %+v", started)
	}

	// An upload replaced during an import isn't deleted with it.
	fw.resumes[path] = "a newer resume, longer than the first one was, so another generation"
	if d.deleteUpload(ctx, in) {
		t.Fatal("deleted a newer upload")
	}
	if _, ok := fw.resumes[path]; !ok {
		t.Fatal("the newer upload is gone")
	}

	// A web-only user gets a memory project; a turn that leaves them no
	// note of their own fails.
	fw.resumes["resumes/u2/resume.pdf"] = "New Person's resume"
	fw.profiles["u2"] = web.Profile{Email: "new@x.com", Name: "New Person", ResumePath: "resumes/u2/resume.pdf", ResumeGeneration: "19"}
	started = nil
	d.onProfile(ctx, profile("u2"), start)
	if len(started) != 1 {
		t.Fatalf("new user started = %+v", started)
	}
	mp2, err := d.importMemory(ctx, started[0])
	if err != nil || mp2.Project == "" || mp2.Project == mp.Project || !d.Memory.Ready(mp2.Project) {
		t.Fatalf("new user memory = %+v %v", mp2, err)
	}
	f2, err := d.fetchResume(ctx, started[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.updateSelf(ctx, started[0], mp2, f2); err == nil {
		t.Fatal("no note, yet done")
	}
	if _, ok := fw.resumes["resumes/u2/resume.pdf"]; !ok {
		t.Fatal("a failed import's upload was deleted")
	}
}
