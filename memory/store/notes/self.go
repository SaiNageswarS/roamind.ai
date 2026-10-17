package notes

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
