package memory

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Goal is a goal note as written by the agent (see schemas/Goal.md).
type Goal struct {
	Title     string
	Permalink string
	// Status is the goal's [status] observation; empty when the note has
	// none yet.
	Status string
}

// Active reports whether the user is working on the goal. A goal saved
// without a status counts as active: the schema requires one, so it is
// missing only on a fresh note.
func (g Goal) Active() bool { return g.Status == "" || g.Status == "active" }

var statusRe = regexp.MustCompile(`(?m)^- \[status\] ([a-z]+)`)

// Goals reads the goal notes in project's goals/ folder straight from disk.
// Notes bm has not given a permalink yet are skipped; they appear once bm
// writes it.
func (c *Config) Goals(project string) ([]Goal, error) {
	if !projectRe.MatchString(project) {
		return nil, errors.New("invalid memory project " + project)
	}
	var goals []Goal
	err := filepath.WalkDir(filepath.Join(c.notesDir(project), "goals"), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fm, body := frontmatter(string(b))
		if fm["type"] != "goal" || fm["permalink"] == "" {
			return nil
		}
		g := Goal{Title: fm["title"], Permalink: fm["permalink"]}
		if m := statusRe.FindStringSubmatch(body); m != nil {
			g.Status = m[1]
		}
		goals = append(goals, g)
		return nil
	})
	return goals, err
}

// frontmatter returns the top-level scalar keys of a note's YAML
// frontmatter, and the body after it.
func frontmatter(note string) (map[string]string, string) {
	fm := map[string]string{}
	rest, ok := strings.CutPrefix(note, "---\n")
	if !ok {
		return fm, note
	}
	head, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return fm, note
	}
	sc := bufio.NewScanner(strings.NewReader(head))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || k == "" || strings.HasPrefix(k, " ") || strings.HasPrefix(k, "-") {
			continue
		}
		fm[k] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return fm, body
}
