package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoals(t *testing.T) {
	c := &Config{Dir: t.TempDir(), ready: map[string]bool{}}
	dir := filepath.Join(c.notesDir("bm-u-sai-1"), "goals")
	notes := map[string]string{
		"Learn JAX.md": "---\ntitle: Learn JAX\ntype: goal\npermalink: bm-u-sai-1/goals/learn-jax\n---\n\n" +
			"- [status] active (stated, slack, 2026-10-11)\n- [kind] learn (inferred, slack, 2026-10-11)\n",
		"sub/Ship chip.md": "---\ntitle: 'Ship chip: v2'\ntype: goal\npermalink: bm-u-sai-1/goals/ship-chip\ntags:\n- x: y\n---\n- [status] achieved\n",
		"Fresh.md":         "---\ntitle: Fresh\ntype: goal\npermalink: bm-u-sai-1/goals/fresh\n---\n- [outcome] something\n",
		"Unsynced.md":      "---\ntitle: Unsynced\ntype: goal\n---\n- [status] active\n",
		"Note.md":          "---\ntitle: Note\ntype: note\npermalink: bm-u-sai-1/goals/note\n---\n",
	}
	for name, body := range notes {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goals, err := c.Goals("bm-u-sai-1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Goal{}
	for _, g := range goals {
		got[g.Permalink] = g
	}
	want := map[string]Goal{
		"bm-u-sai-1/goals/learn-jax": {"Learn JAX", "bm-u-sai-1/goals/learn-jax", "active"},
		"bm-u-sai-1/goals/ship-chip": {"Ship chip: v2", "bm-u-sai-1/goals/ship-chip", "achieved"},
		"bm-u-sai-1/goals/fresh":     {"Fresh", "bm-u-sai-1/goals/fresh", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("goals = %+v", goals)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
	if !got["bm-u-sai-1/goals/fresh"].Active() || got["bm-u-sai-1/goals/ship-chip"].Active() {
		t.Error("Active wrong")
	}

	// A project without goals yet.
	if g, err := c.Goals("bm-u-new-1"); err != nil || len(g) != 0 {
		t.Fatalf("empty project: %v %v", g, err)
	}
}
