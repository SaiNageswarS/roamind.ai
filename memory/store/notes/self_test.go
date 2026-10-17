package notes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelf(t *testing.T) {
	n, err := New(t.TempDir(), "../..", "true")
	if err != nil {
		t.Fatal(err)
	}
	const project = "bm-u-sai-1"
	if s, err := n.Self(project); s != nil || err != nil {
		t.Fatalf("no notes = %+v %v", s, err)
	}
	dir := filepath.Join(n.NotesDir(project), "people")
	os.MkdirAll(dir, 0o755)
	write := func(name, note string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(note), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Kam.md", "---\ntitle: Kam\ntype: person\npermalink: p/kam\n---\n\n- [relationship] colleague\n")
	write("Sai.md", "---\ntitle: Sai\ntype: person\npermalink: p/sai\n---\n\n- [relationship] self (stated, slack, 2026-10-11)\n- [interest] agents\n")
	s, err := n.Self(project)
	if err != nil || s == nil || s.Title != "Sai" || s.Permalink != "p/sai" || !strings.HasSuffix(s.Body, "- [interest] agents") {
		t.Fatalf("self = %+v %v", s, err)
	}
}
