package notes

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// SelfFolder is the folder of Person notes, the user's own among them.
const SelfFolder = "people"

// Self is the user's own Person note (relationship self), which holds
// their resume: contact details, roles, education and the rest.
type Self struct {
	Title     string
	Permalink string
	// Body is the note's content after its frontmatter.
	Body string
}

var selfRe = regexp.MustCompile(`(?m)^- \[relationship\] self\b`)

// Self reads the user's own Person note in project straight from disk; it
// is nil when there is none yet.
func (n Notes) Self(project string) (*Self, error) {
	if err := ValidProject(project); err != nil {
		return nil, err
	}
	var self *Self
	err := filepath.WalkDir(filepath.Join(n.NotesDir(project), SelfFolder), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") || self != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fm, body := frontmatter(string(b))
		if fm["type"] == "person" && selfRe.MatchString(body) {
			self = &Self{Title: fm["title"], Permalink: fm["permalink"], Body: strings.TrimSpace(body)}
		}
		return nil
	})
	return self, err
}

// Resume is what a resume says about its owner, one Person observation per
// entry, in the formats of the Person schema.
type Resume struct {
	Contact     []string
	Location    []string
	Experience  []string
	Education   []string
	Skill       []string
	Project     []string
	Publication []string
}

// field is one Person field of a resume.
type field struct {
	name   string
	values []string
}

// fields are the resume's Person fields, in the order a note lists them.
func (r Resume) fields() []field {
	return []field{
		{"contact", r.Contact}, {"location", r.Location}, {"experience", r.Experience},
		{"education", r.Education}, {"skill", r.Skill}, {"project", r.Project}, {"publication", r.Publication},
	}
}

// kept are the fields a resume adds to rather than replaces: a resume lists
// some of the ways to reach someone and where they live, not all.
var kept = []string{"contact", "location"}

var observationRe = regexp.MustCompile(`^- \[([a-z_]+)\] (.*?)(?: \(([^()]*)\))?$`)

// SelfNote is the user's own Person note with the resume merged into
// self's body, for WriteNote; self is nil for a user who has none yet. The
// resume replaces each career field it has (experience, education, skill,
// project, publication) and adds the contact details and locations the
// note lacks; every other line is kept. evidence is the context of the new
// facts, e.g. "stated, resume, 2026-10-17".
func SelfNote(self *Self, r Resume, evidence string) string {
	var body string
	head := "---\ntype: person\n---\n\n"
	if self != nil {
		body = self.Body
		if self.Permalink != "" {
			head = fmt.Sprintf("---\ntitle: %s\ntype: person\npermalink: %s\n---\n\n", self.Title, self.Permalink)
		}
	}

	replaced := map[string]bool{}
	for _, f := range r.fields() {
		if len(f.values) > 0 && !slices.Contains(kept, f.name) {
			replaced[f.name] = true
		}
	}
	byField := map[string][]string{}
	var order, other []string
	have := map[string]bool{} // field + value, of the kept fields
	for _, line := range strings.Split(body, "\n") {
		m := observationRe.FindStringSubmatch(strings.TrimRight(line, " "))
		if m == nil {
			other = append(other, line)
			continue
		}
		name := m[1]
		if replaced[name] {
			continue
		}
		if byField[name] == nil {
			order = append(order, name)
		}
		byField[name] = append(byField[name], line)
		have[name+"\x00"+strings.ToLower(m[2])] = true
	}

	if byField["relationship"] == nil {
		order = append([]string{"relationship"}, order...)
		byField["relationship"] = []string{"- [relationship] self (" + evidence + ")"}
	}
	for _, f := range r.fields() {
		for _, v := range f.values {
			v = factValue(v)
			if v == "" || have[f.name+"\x00"+strings.ToLower(v)] {
				continue
			}
			have[f.name+"\x00"+strings.ToLower(v)] = true
			if byField[f.name] == nil {
				order = append(order, f.name)
			}
			byField[f.name] = append(byField[f.name], "- ["+f.name+"] "+v+" ("+evidence+")")
		}
	}

	// The resume's fields in their order, then the note's others as they
	// were; links and text after the facts.
	var lines []string
	for _, f := range append([]field{{name: "relationship"}}, r.fields()...) {
		lines = append(lines, byField[f.name]...)
		delete(byField, f.name)
	}
	for _, name := range order {
		lines = append(lines, byField[name]...)
	}
	rest := strings.TrimSpace(strings.Join(other, "\n"))
	if rest != "" {
		lines = append(lines, "", rest)
	}
	return head + strings.Join(lines, "\n") + "\n"
}

// factValue makes v one fact line's value: one line, with no links, which
// would read as another fact or a relation. Its own parentheses stay: the
// evidence context always comes after them.
func factValue(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	v = strings.NewReplacer("[[", "", "]]", "").Replace(v)
	return strings.TrimLeft(v, "- ")
}
