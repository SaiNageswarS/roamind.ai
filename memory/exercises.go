package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// ExerciseFolder is the folder of a goal's exercise notes, named after the
// goal note: exercises/<goal slug>.
func ExerciseFolder(goalPermalink string) string {
	return "exercises/" + path.Base(goalPermalink)
}

// RecentExercises returns the contents of the goal's n latest exercise
// notes, newest first. Exercise notes are titled with their ISO date, so
// file names sort by date.
func (c *Config) RecentExercises(project, goalPermalink string, n int) ([]string, error) {
	if !projectRe.MatchString(project) {
		return nil, errors.New("invalid memory project " + project)
	}
	dir := filepath.Join(c.notesDir(project), filepath.FromSlash(ExerciseFolder(goalPermalink)))
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	slices.Reverse(names)
	var notes []string
	for _, name := range names[:min(n, len(names))] {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		_, body := frontmatter(string(b))
		notes = append(notes, strings.TrimSpace(body))
	}
	return notes, nil
}

// WriteNote creates or replaces a note in project through bm's CLI, which
// indexes it at once. content may start with frontmatter, e.g. to fix the
// permalink.
func (c *Config) WriteNote(ctx context.Context, project, folder, title, noteType, content string) error {
	if err := c.Ensure(project); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.Binary, "tool", "write-note",
		"--project", project, "--folder", folder, "--title", title, "--type", noteType, "--overwrite")
	cmd.Env = append(os.Environ(), "BASIC_MEMORY_CONFIG_DIR="+c.configDir(project))
	cmd.Stdin = strings.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bm write-note: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}
