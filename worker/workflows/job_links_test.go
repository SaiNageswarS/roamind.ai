package workflows

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

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

	// job-search claims every queued link, whatever the user's search time.
	_, _, users, err := d.dueHunts(ctx, d.now())
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := d.claimLinks(ctx, users, hunt.Date)
	if err != nil || len(claimed) != 1 || len(claimed[0].Links) != 6 || claimed[0].UserID != hunt.UserID {
		t.Fatalf("claimed = %+v %v", claimed, err)
	}
	in := claimed[0]
	if fw.link("u1", "l6").Status != web.LinkProcessing {
		t.Fatalf("l6 = %+v", fw.link("u1", "l6"))
	}
	// Claimed links aren't claimed again.
	if again, _ := d.claimLinks(ctx, users, hunt.Date); len(again) != 0 {
		t.Fatalf("claimed again = %+v", again)
	}

	if chk, err := d.checkLinks(ctx, in); err != nil || chk.Skip != "" {
		t.Fatalf("check = %+v %v", chk, err)
	}
	r, err := d.readResume(ctx, in.hunt())
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
	claimed, _ = d.claimLinks(ctx, users, hunt.Date)
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

// Without a resume, links fail with a reason the user can act on.
func TestJobLinksNoResume(t *testing.T) {
	ctx := context.Background()
	d, fw, hunt := jobsSetup(t)
	p := fw.profiles["u1"]
	p.ResumePath = ""
	fw.profiles["u1"] = p
	fw.addLink("u1", "l1", "https://jobs.lever.co/palantir/6ed76ce8-4156-4b60-b120-403538bd66cd")
	claimed, err := d.claimLinks(ctx, map[string]web.Profile{hunt.UserID: {UID: "u1"}}, hunt.Date)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed = %+v %v", claimed, err)
	}
	if chk, err := d.checkLinks(ctx, claimed[0]); err != nil || chk.Skip == "" {
		t.Fatalf("check = %+v %v", chk, err)
	}
	if s := fw.link("u1", "l1"); s != (web.LinkState{Status: web.LinkFailed, Reason: linkNoResume}) {
		t.Fatalf("l1 = %+v", s)
	}
}
