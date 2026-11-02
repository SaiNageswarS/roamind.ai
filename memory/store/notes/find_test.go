package notes

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindNote(t *testing.T) {
	n := Notes{Dir: t.TempDir()}
	root := n.NotesDir("bm-u-sai-1")
	files := map[string]string{
		// bm named the file after the note's first title.
		"events/2026/Parents' flight to Hyderabad.md": "---\ntitle: Flight to Hyderabad\ntype: event\n" +
			"permalink: bm-u-sai-1/events/2026/parents-flight-to-hyderabad\n---\n\nThe flight.\n",
		"people/Asha.md": "---\ntitle: Asha\ntype: person\npermalink: bm-u-sai-1/people/asha\n---\n\n" +
			"- [relationship] dentist\n- [contact] Asha Rao <Asha@Clinic.example>\n",
		"Top.md":            "---\ntitle: Top\ntype: note\npermalink: bm-u-sai-1/top\n---\n",
		"schemas/Event.md":  "---\ntitle: Event\ntype: schema\npermalink: bm-u-sai-1/schemas/event\n---\n",
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
	f, err := n.FindNote("bm-u-sai-1", "bm-u-sai-1/events/2026/parents-flight-to-hyderabad")
	if err != nil || f == nil || f.Folder != "events/2026" || f.Name != "Parents' flight to Hyderabad" || f.Type != "event" ||
		f.Body != "\n\nThe flight.\n" {
		t.Fatalf("found = %+v %v", f, err)
	}
	if f, _ := n.FindNote("bm-u-sai-1", "bm-u-sai-1/top"); f == nil || f.Folder != "" || f.Name != "Top" {
		t.Fatalf("top = %+v", f)
	}
	for _, p := range []string{"bm-u-sai-1/schemas/event", "bm-u-sai-1/missing"} {
		if f, err := n.FindNote("bm-u-sai-1", p); err != nil || f != nil {
			t.Fatalf("%s = %+v %v", p, f, err)
		}
	}
	if f, err := n.FindNote("bm-u-none-1", "x"); err != nil || f != nil {
		t.Fatalf("no project = %+v %v", f, err)
	}

	for contact, want := range map[string]bool{"asha@clinic.example": true, " ASHA@clinic.example ": true, "bob@x.com": false, "": false} {
		if has, err := n.HasContact("bm-u-sai-1", contact); err != nil || has != want {
			t.Errorf("HasContact(%q) = %v %v", contact, has, err)
		}
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
