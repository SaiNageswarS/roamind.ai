package notes

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRecentExercises(t *testing.T) {
	n := Notes{Dir: t.TempDir()}
	goal := "bm-u-sai-1/goals/learn-german"
	if got, err := n.RecentExercises("bm-u-sai-1", goal, 3); err != nil || got != nil {
		t.Fatalf("none yet: %v %v", got, err)
	}
	dir := filepath.Join(n.NotesDir("bm-u-sai-1"), "exercises", "learn-german")
	os.MkdirAll(dir, 0o700)
	for _, day := range []string{"2026-10-12", "2026-10-14", "2026-10-13", "2026-10-11"} {
		body := "---\ntitle: " + day + " x\ntype: exercise\n---\n\nexercise of " + day + "\n"
		os.WriteFile(filepath.Join(dir, day+" Topic.md"), []byte(body), 0o600)
	}
	got, err := n.RecentExercises("bm-u-sai-1", goal, 3)
	want := []string{"exercise of 2026-10-14", "exercise of 2026-10-13", "exercise of 2026-10-12"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q %v, want %q", got, err, want)
	}
}

func TestWriteNote(t *testing.T) {
	// A fake bm that records its arguments, config dir and stdin.
	dir := t.TempDir()
	bin := filepath.Join(dir, "bm")
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\necho \"$@\" > " + log + "\necho \"$BASIC_MEMORY_CONFIG_DIR\" >> " + log + "\ncat >> " + log + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	n := Notes{Dir: t.TempDir(), Binary: bin}
	if err := n.WriteNote(context.Background(), "bm-u-sai-1", "exercises/learn-german", "2026-10-12 Dative", "exercise", "body"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "tool write-note --project bm-u-sai-1 --folder exercises/learn-german --title 2026-10-12 Dative --type exercise --overwrite\n" +
		n.ConfigDir("bm-u-sai-1") + "\nbody"
	if string(b) != want {
		t.Fatalf("bm got:\n%s\nwant:\n%s", b, want)
	}
}
