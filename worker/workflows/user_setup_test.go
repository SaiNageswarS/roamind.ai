package workflows

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"roamind.ai/channels/identity"
	"roamind.ai/memory/store/notes"
)

// withMemory gives d long-term memory, and sets users up in process, at
// once, where Register starts a user-setup run.
func withMemory(t *testing.T, d *Deps) *notes.Notes {
	t.Helper()
	mem, err := notes.New(t.TempDir(), "../../memory", "true")
	if err != nil {
		t.Fatal(err)
	}
	d.Memory, d.Agent.Memory = mem, mem
	d.SetupUser = func(ctx context.Context, userID string) error { return runUserSetup(ctx, *d, userID) }
	return mem
}

// runUserSetup runs user-setup's task bodies in the workflow's order, each
// given its parent's output.
func runUserSetup(ctx context.Context, d Deps, userID string) error {
	p, err := d.assignProject(ctx, userID)
	if err != nil {
		return err
	}
	if _, err := d.setUpMemory(p); err != nil {
		return err
	}
	_, err = d.SyncNews(ctx, userID)
	return err
}

// countRuns counts the user-setup runs chat-turn starts.
func countRuns(d *Deps) *int {
	runs := 0
	start := d.SetupUser
	d.SetupUser = func(ctx context.Context, userID string) error { runs++; return start(ctx, userID) }
	return &runs
}

// A new user's first message starts user-setup and is answered with a note
// that they are being set up; their next message gets a turn.
func TestProcessSetsUpNewUser(t *testing.T) {
	ctx := context.Background()
	d, fs, argsLog := setup(t)
	withMemory(t, &d)
	runs := countRuns(&d)

	out, err := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", ""))
	if err != nil || out.Skipped != "setting up user" {
		t.Fatalf("first message: %+v %v", out, err)
	}
	if len(fs.texts) == 0 || fs.texts[len(fs.texts)-1] != setupReply {
		t.Fatalf("posts = %q", fs.texts)
	}
	u, err := d.Store.UserByID(ctx, identity.UserID("owner@x.com"))
	if err != nil || u == nil || u.MemoryProject == "" || !d.Memory.Ready(u.MemoryProject) {
		t.Fatalf("user = %+v %v", u, err)
	}
	if ch, _ := d.Store.NewsChannelOf(ctx, u.ID); ch == "" || len(fs.created) != 1 {
		t.Fatalf("news channel %q, created %v", ch, fs.created)
	}

	if out, err := d.Process(ctx, "r1", mention("Ev1", "U1", "2.0", "")); err != nil || out.Skipped != "" || out.SessionID == "" {
		t.Fatalf("second message: %+v %v", out, err)
	}
	if *runs != 1 {
		t.Fatalf("user-setup started %d times", *runs)
	}
	if args, _ := os.ReadFile(argsLog); strings.Count(string(args), "\n") != 1 {
		t.Fatalf("claude ran for:\n%s", args)
	}
}

// While a user's memory project is not ready, every message gets the note
// and starts user-setup again; runs are serialized and safe to repeat.
func TestProcessWhileSetupRuns(t *testing.T) {
	ctx := context.Background()
	d, fs, _ := setup(t)
	withMemory(t, &d)
	d.SetupUser = func(context.Context, string) error { return nil } // still running
	runs := countRuns(&d)
	for i, ts := range []string{"1.0", "2.0"} {
		if out, err := d.Process(ctx, "r", mention("Ev"+ts, "U1", ts, "")); err != nil || out.Skipped != "setting up user" {
			t.Fatalf("message %d: %+v %v", i, out, err)
		}
	}
	if *runs != 2 || len(fs.texts) != 2 {
		t.Fatalf("runs %d, posts %q", *runs, fs.texts)
	}
}

// A user whose news channel failed gets their turn, and user-setup starts
// again for the channel.
func TestProcessSetupWithoutNewsChannel(t *testing.T) {
	ctx := context.Background()
	d, _, _ := setup(t)
	withMemory(t, &d)
	d.SetupUser = func(ctx context.Context, userID string) error {
		p, err := d.assignProject(ctx, userID)
		if err != nil {
			return err
		}
		_, err = d.setUpMemory(p) // news-channel fails
		return err
	}
	runs := countRuns(&d)
	if out, _ := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); out.Skipped != "setting up user" {
		t.Fatalf("first message: %+v", out)
	}
	if out, err := d.Process(ctx, "r1", mention("Ev1", "U1", "2.0", "")); err != nil || out.Skipped != "" {
		t.Fatalf("second message: %+v %v", out, err)
	}
	if *runs != 2 {
		t.Fatalf("user-setup started %d times", *runs)
	}
}

// A message whose user-setup can't be started is still answered.
func TestProcessSetupStartFails(t *testing.T) {
	ctx := context.Background()
	d, fs, _ := setup(t)
	withMemory(t, &d)
	d.SetupUser = func(context.Context, string) error { return errors.New("hatchet is down") }
	if _, err := d.Process(ctx, "r0", mention("Ev0", "U1", "1.0", "")); err == nil {
		t.Fatal("no error")
	}
	if len(fs.texts) != 1 || fs.texts[0] != setupFailedReply {
		t.Fatalf("posts = %q", fs.texts)
	}
}
