package notes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestHasContact(t *testing.T) {
	n := Notes{Dir: t.TempDir()}
	root := n.NotesDir("bm-u-sai-1")
	files := map[string]string{
		"people/Asha.md": "---\ntitle: Asha\ntype: person\npermalink: bm-u-sai-1/people/asha\n---\n\n" +
			"- [relationship] dentist\n- [contact] Asha Rao <Asha@Clinic.example>\n",
		"tasks/Contact.md":  "---\ntitle: Contact\ntype: task\npermalink: bm-u-sai-1/tasks/contact\n---\n- [contact] bob@x.com\n",
		"people/readme.txt": "- [contact] bob@x.com\n",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for contact, want := range map[string]bool{"asha@clinic.example": true, " ASHA@clinic.example ": true, "bob@x.com": false, "": false} {
		if has, err := n.HasContact("bm-u-sai-1", contact); err != nil || has != want {
			t.Errorf("HasContact(%q) = %v %v", contact, has, err)
		}
	}
}

func TestFindNote(t *testing.T) {
	// A fake bm that logs its arguments and config dir and prints what bm
	// prints for each permalink.
	dir := t.TempDir()
	bin, log := filepath.Join(dir, "bm"), filepath.Join(dir, "log")
	script := `#!/bin/sh
echo "$@" >> ` + log + `
echo "$BASIC_MEMORY_CONFIG_DIR" >> ` + log + `
for last; do :; done
case "$last" in
  */flight) echo '{"title": "Flight to Hyderabad", "permalink": "bm-u-sai-1/events/2026/flight",
    "file_path": "events/2026/Parents'"'"' flight to Hyderabad.md", "content": "---\\n", "frontmatter": {"type": "event"}}' ;;
  top) echo '{"permalink": "bm-u-sai-1/top", "file_path": "Top.md", "frontmatter": {"type": "note"}}' ;;
  broken) echo oops >&2; exit 1 ;;
  *) echo '{"title": null, "permalink": null, "file_path": null, "content": "", "frontmatter": null,
    "related_results": [{"permalink": "bm-u-sai-1/top", "file_path": "Top.md"}]}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	n := Notes{Dir: t.TempDir(), Binary: bin}
	// bm named the file after the note's first title.
	f, err := n.FindNote(ctx, "bm-u-sai-1", "bm-u-sai-1/events/2026/flight")
	if err != nil || f == nil || *f != (Found{Permalink: "bm-u-sai-1/events/2026/flight", Folder: "events/2026",
		Name: "Parents' flight to Hyderabad", Type: "event"}) {
		t.Fatalf("found = %+v %v", f, err)
	}
	b, _ := os.ReadFile(log)
	if want := "tool read-note --project bm-u-sai-1 --json --frontmatter -- bm-u-sai-1/events/2026/flight\n" +
		n.ConfigDir("bm-u-sai-1") + "\n"; string(b) != want {
		t.Fatalf("bm got:\n%s\nwant:\n%s", b, want)
	}
	// bm resolves a permalink without the project; FindNote returns bm's.
	if f, _ := n.FindNote(ctx, "bm-u-sai-1", "top"); f == nil || f.Permalink != "bm-u-sai-1/top" || f.Folder != "" || f.Name != "Top" {
		t.Fatalf("top = %+v", f)
	}
	// No note has the permalink: bm's suggestions are not a match.
	if f, err := n.FindNote(ctx, "bm-u-sai-1", "bm-u-sai-1/missing"); err != nil || f != nil {
		t.Fatalf("missing = %+v %v", f, err)
	}
	if f, err := n.FindNote(ctx, "bm-u-sai-1", "broken"); err == nil || f != nil {
		t.Fatalf("bm failing = %+v %v", f, err)
	}
	// No permalink, or a bad project, never reaches bm.
	os.Remove(log)
	if f, err := n.FindNote(ctx, "bm-u-sai-1", " "); err != nil || f != nil {
		t.Fatalf("empty = %+v %v", f, err)
	}
	if _, err := n.FindNote(ctx, "../x", "top"); err == nil {
		t.Fatal("bad project accepted")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("bm ran")
	}
}

func TestPermalink(t *testing.T) {
	for _, c := range []struct{ folder, title, want string }{
		{"events/2026", "Parents' flight Dusseldorf to Hyderabad 2026-11-12", "p/events/2026/parents-flight-dusseldorf-to-hyderabad-2026-11-12"},
		{"tasks/", "Pay Acme INV-7", "p/tasks/pay-acme-inv-7"},
		{"people", "Dr. Asha Rao", "p/people/dr-asha-rao"},
	} {
		if got := Permalink("p", c.folder, c.title); got != c.want {
			t.Errorf("Permalink(%q, %q) = %q", c.folder, c.title, got)
		}
	}
}
