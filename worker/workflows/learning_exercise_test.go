package workflows

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
)

func TestLocalCrons(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	kolkata, _ := time.LoadLocation("Asia/Kolkata")
	winter := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	summer := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		loc  *time.Location
		at   time.Time
		want []string
	}{
		{berlin, winter, []string{"0 8 * * *"}},
		{berlin, summer, []string{"0 7 * * *"}},
		{time.UTC, summer, []string{"0 9 * * *"}},
		{kolkata, summer, []string{"30 3 * * *"}},
	}
	for _, c := range cases {
		if got := LocalCrons(c.loc, c.at, 9); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s on %s: %v, want %v", c.loc, c.at.Format(time.DateOnly), got, c.want)
		}
	}
	want := []string{"0 6 * * *", "0 18 * * *"}
	if got := LocalCrons(berlin, summer, 8, 20); !reflect.DeepEqual(got, want) {
		t.Errorf("8 and 20 in Berlin: %v, want %v", got, want)
	}
}

// fakeBM saves the note `bm tool write-note` is given in the project's
// notes folder, as bm does.
func fakeBM(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bm")
	script := `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in --folder) folder=$2; shift ;; --title) title=$2; shift ;; esac
  shift
done
notes="$BASIC_MEMORY_CONFIG_DIR/../notes"
mkdir -p "$notes/$folder" && cat > "$notes/$folder/$title.md"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestLearningExercise(t *testing.T) {
	ctx := context.Background()
	berlin, _ := time.LoadLocation("Europe/Berlin")
	d, fs, _ := setup(t)
	bin, argsLog := fakeClaudeWith(t, "testdata/draft_exercise.jsonl")
	d.Agent.ClaudeBinary = bin
	mem, err := notes.New(t.TempDir(), "../../memory", fakeBM(t))
	if err != nil {
		t.Fatal(err)
	}
	d.Memory, d.Agent.Memory, d.Location = mem, mem, berlin
	nine := time.Date(2026, 10, 12, 9, 0, 5, 0, berlin)
	d.Now = func() time.Time { return nine }

	u := store.User{ID: "h1", Email: "a@x.com", DisplayName: "Sai", SlackTeamID: "T1", SlackUserID: "U1"}
	if err := d.Store.UpsertSlackUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.MemoryProject, err = d.Store.AssignMemoryProject(ctx, u.ID, "bm-u-sai"); err != nil {
		t.Fatal(err)
	}
	p := u.MemoryProject
	if err := mem.Setup(p); err != nil {
		t.Fatal(err)
	}
	german := writeGoal(t, mem, p, "Learn German", "learn-german", "active",
		"- [kind] learn (inferred, slack, 2026-10-11)",
		"- [exercise_preference] translations over fill-in-the-blanks (stated, slack, 2026-10-11)")
	paused := writeGoal(t, mem, p, "Learn JAX", "learn-jax", "paused", "- [kind] learn")
	build := writeGoal(t, mem, p, "Ship chip", "ship-chip", "active", "- [kind] build")
	piano := writeGoal(t, mem, p, "Learn piano", "learn-piano", "active", "- [kind] learn") // no channel
	for i, g := range []string{german, paused, build} {
		if err := d.Store.SaveChannel(ctx, store.Channel{Kind: store.KindGoal, UserID: u.ID, Ref: g, Title: "title of " + g,
			ChannelID: []string{"G0", "G1", "G2"}[i]}); err != nil {
			t.Fatal(err)
		}
	}
	in := ExerciseInput{UserID: u.ID, GoalPermalink: german}

	// The dispatcher: every active learning goal with a channel.
	if due, err := d.dueGoals(ctx); err != nil || !reflect.DeepEqual(due, []ExerciseInput{in}) {
		t.Fatalf("due=%+v err=%v", due, err)
	}

	// check-skip
	for goal, want := range map[string]string{build: "not an active learning goal", paused: "not an active learning goal", piano: "no channel"} {
		if chk, err := d.checkSkip(ctx, ExerciseInput{UserID: u.ID, GoalPermalink: goal}); err != nil || !strings.Contains(chk.Skip, want) {
			t.Errorf("%s: skip %q %v, want %q", goal, chk.Skip, err, want)
		}
	}
	chk, err := d.checkSkip(ctx, in)
	if err != nil || chk.Skip != "" || chk.ChannelID != "G0" || chk.GoalTitle != "title of "+german {
		t.Fatalf("check: %+v %v", chk, err)
	}

	// build-context: the goal with its preferences, no exercises yet.
	ec, err := d.buildContext(ctx, in)
	if err != nil || !strings.Contains(ec.Goal, "translations over fill-in-the-blanks") || len(ec.Exercises) != 0 {
		t.Fatalf("context: %+v %v", ec, err)
	}

	// draft-exercise: structured output from a turn without memory tools.
	dr, err := d.draftExercise(ctx, in, chk, ec)
	if err != nil || dr.Topic != "Dative prepositions" || dr.Difficulty != "easy" || dr.SessionID != "5d0c1f0e-draft-session" {
		t.Fatalf("draft: %+v %v", dr, err)
	}
	args, _ := os.ReadFile(argsLog)
	// The draft has the workspace tools and the user's memory.
	if !strings.Contains(string(args), "--json-schema") || !strings.Contains(string(args), "--tools Bash,Read,Write,Edit") ||
		!strings.Contains(string(args), `"--project","`+p+`"`) || strings.Contains(string(args), "--no-session-persistence") {
		t.Fatalf("draft args: %s", args)
	}
	stdin, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "stdin.log"))
	if !strings.Contains(string(stdin), "first exercise") || !strings.Contains(string(stdin), "translations over fill-in-the-blanks") {
		t.Fatalf("draft prompt: %s", stdin)
	}

	// post: a channel message, recorded, with its note saved.
	out, err := d.postExercise(ctx, in, chk, dr)
	if err != nil {
		t.Fatal(err)
	}
	if fs.threads[0] != "" || !strings.Contains(fs.texts[0], "*dem*") || out.ThreadTS != fs.posts[0] {
		t.Fatalf("posted in thread %q: %q", fs.threads[0], fs.texts[0])
	}
	th, err := d.Store.GetThread(ctx, "G0", out.ThreadTS)
	if err != nil || th == nil || th.ClaudeSessionID != dr.SessionID || th.UserID != u.ID {
		t.Fatalf("thread = %+v %v", th, err)
	}
	wantNote := p + "/exercises/learn-german/2026-10-12"
	if e, err := d.Store.LastExercise(ctx, u.ID, german); err != nil || e == nil || e.NotePermalink != wantNote || out.NotePermalink != wantNote {
		t.Fatalf("exercise = %+v %v", e, err)
	}
	note, err := os.ReadFile(filepath.Join(mem.Dir, "users", p, "notes", "exercises", "learn-german", "2026-10-12 Dative prepositions.md"))
	for _, want := range []string{"permalink: " + wantNote, "## Exercise\n\nFill in the blanks:", "- [difficulty] easy", "- exercise_of [[" + german + "]]"} {
		if err != nil || !strings.Contains(string(note), want) {
			t.Fatalf("note lacks %q (%v):\n%s", want, err, note)
		}
	}

	// The same day, and every day until it is answered, check-skip stops the run.
	if chk, _ := d.checkSkip(ctx, in); !strings.Contains(chk.Skip, "already posted") {
		t.Fatalf("same day: %+v", chk)
	}
	for day := 1; day <= 2; day++ {
		d.Now = func() time.Time { return nine.AddDate(0, 0, day) }
		if chk, err := d.checkSkip(ctx, in); err != nil || !strings.Contains(chk.Skip, "unanswered") {
			t.Fatalf("day +%d: %+v %v", day, chk, err)
		}
	}
	if note, err := d.Store.MarkExerciseAnswered(ctx, "G0", out.ThreadTS); err != nil || note != wantNote {
		t.Fatalf("answer: %q %v", note, err)
	}
	if chk, err := d.checkSkip(ctx, in); err != nil || chk.Skip != "" {
		t.Fatalf("after the answer: %+v %v", chk, err)
	}
	// The next draft is calibrated from the posted exercise.
	if ec, err := d.buildContext(ctx, in); err != nil || len(ec.Exercises) != 1 || !strings.Contains(ec.Exercises[0], "Dative") {
		t.Fatalf("next context: %+v %v", ec, err)
	}
}
