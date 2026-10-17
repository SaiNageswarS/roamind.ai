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

func TestSelfNote(t *testing.T) {
	self := &Self{Title: "Sai", Permalink: "bm-u-sai-1/people/sai", Body: strings.Join([]string{
		"- [relationship] self (stated, slack, 2026-10-11)",
		"- [contact] sai@x.com (stated, slack, 2026-10-11)",
		"- [experience] Engineer, Old Co, 2010-01 to 2012-01 (stated, slack, 2026-10-11)",
		"- [interest] agents (stated, slack, 2026-10-11)",
		"- [publication] A paper, 2015 (stated, slack, 2026-10-11)",
		"",
		"- works_at [[places/new-co]]",
	}, "\n")}
	r := Resume{
		Contact:    []string{"SAI@x.com", "+49 123"},
		Experience: []string{"Scientist, New Co (Search), 2022-05 to present: agents", "Engineer, Mid Co, 2012-01 to 2022-04"},
		Skill:      []string{"Go, Python", "  "},
		Project:    []string{"- roamind [[x]]"},
	}
	got := SelfNote(self, r, "stated, resume, 2026-10-17")
	want := `---
title: Sai
type: person
permalink: bm-u-sai-1/people/sai
---

- [relationship] self (stated, slack, 2026-10-11)
- [contact] sai@x.com (stated, slack, 2026-10-11)
- [contact] +49 123 (stated, resume, 2026-10-17)
- [experience] Scientist, New Co (Search), 2022-05 to present: agents (stated, resume, 2026-10-17)
- [experience] Engineer, Mid Co, 2012-01 to 2022-04 (stated, resume, 2026-10-17)
- [skill] Go, Python (stated, resume, 2026-10-17)
- [project] roamind x (stated, resume, 2026-10-17)
- [publication] A paper, 2015 (stated, slack, 2026-10-11)
- [interest] agents (stated, slack, 2026-10-11)

- works_at [[places/new-co]]
`
	if got != want {
		t.Fatalf("note =\n%s\nwant\n%s", got, want)
	}

	// A user with no note yet gets one, marked as theirs.
	fresh := SelfNote(nil, Resume{Education: []string{"M.Tech CS, IIT Madras, 2012 to 2014"}}, "stated, resume, 2026-10-17")
	if fresh != "---\ntype: person\n---\n\n- [relationship] self (stated, resume, 2026-10-17)\n- [education] M.Tech CS, IIT Madras, 2012 to 2014 (stated, resume, 2026-10-17)\n" {
		t.Fatalf("fresh =\n%s", fresh)
	}
}
