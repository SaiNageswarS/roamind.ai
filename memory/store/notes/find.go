package notes

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Found is a note on disk: where WriteNote rewrites it in place. bm names
// a note's file after the title it was first written with, which a later
// title can differ from, and writing it with another title would make a
// second note.
type Found struct {
	Folder string // relative to the notes folder, with slashes; "" at the top
	Name   string // the file name without .md
	Type   string
	Body   string // the note after its frontmatter
}

// FindNote returns the note of project with permalink, read straight from
// disk, or nil when there is none.
func (n Notes) FindNote(project, permalink string) (*Found, error) {
	if err := ValidProject(project); err != nil {
		return nil, err
	}
	var found *Found
	errFound := errors.New("found")
	err := n.walk(project, "", func(rel string, fm map[string]string, body string) error {
		if fm["permalink"] != permalink {
			return nil
		}
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		found = &Found{Folder: dir, Name: strings.TrimSuffix(path.Base(rel), ".md"), Type: fm["type"], Body: body}
		return errFound
	})
	if errors.Is(err, errFound) {
		err = nil
	}
	return found, err
}

// HasContact reports whether a person note of project in people/ lists
// contact, such as an email address, as a [contact], in any case.
func (n Notes) HasContact(project, contact string) (bool, error) {
	if err := ValidProject(project); err != nil {
		return false, err
	}
	contact = strings.ToLower(strings.TrimSpace(contact))
	if contact == "" {
		return false, nil
	}
	has := false
	errFound := errors.New("found")
	err := n.walk(project, "people", func(_ string, fm map[string]string, body string) error {
		if fm["type"] != "person" {
			return nil
		}
		for _, m := range contactRe.FindAllStringSubmatch(body, -1) {
			if strings.Contains(strings.ToLower(m[1]), contact) {
				has = true
				return errFound
			}
		}
		return nil
	})
	if errors.Is(err, errFound) {
		err = nil
	}
	return has, err
}

var contactRe = regexp.MustCompile(`(?m)^- \[contact\] (.+)$`)

// walk calls fn with the slash path relative to the notes folder, the
// frontmatter and the body of each note under folder, skipping schemas.
func (n Notes) walk(project, folder string, fn func(rel string, fm map[string]string, body string) error) error {
	root := n.NotesDir(project)
	return filepath.WalkDir(filepath.Join(root, filepath.FromSlash(folder)), func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "schemas" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".md") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fm, body := frontmatter(string(b))
		return fn(rel, fm, body)
	})
}

var apostrophes = strings.NewReplacer("'", "", "’", "")

// Permalink is the permalink a new note of project gets for its folder and
// title, as bm makes it: lowercase words joined by hyphens.
func Permalink(project, folder, title string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(apostrophes.Replace(strings.ToLower(title)), "-"), "-")
	return strings.Trim(project+"/"+strings.Trim(folder, "/"), "/") + "/" + slug
}
