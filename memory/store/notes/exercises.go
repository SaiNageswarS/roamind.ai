package notes

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

// RecentExercises returns the contents of the goal's latest exercise notes,
// at most limit, newest first. Exercise notes are titled with their ISO date, so
// file names sort by date.
func (n Notes) RecentExercises(project, goalPermalink string, limit int) ([]string, error) {
	if err := ValidProject(project); err != nil {
		return nil, err
	}
	dir := filepath.Join(n.NotesDir(project), filepath.FromSlash(ExerciseFolder(goalPermalink)))
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
	for _, name := range names[:min(limit, len(names))] {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		_, body := frontmatter(string(b))
		notes = append(notes, strings.TrimSpace(body))
	}
	return notes, nil
}

// WriteNote creates or replaces a note in project, which bm must have been
// set up for, through bm's CLI, which indexes it at once. content may start
// with frontmatter, e.g. to fix the permalink.
func (n Notes) WriteNote(ctx context.Context, project, folder, title, noteType, content string) error {
	if err := ValidProject(project); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, n.Binary, "tool", "write-note",
		"--project", project, "--folder", folder, "--title", title, "--type", noteType, "--overwrite")
	cmd.Env = append(os.Environ(), "BASIC_MEMORY_CONFIG_DIR="+n.ConfigDir(project))
	cmd.Stdin = strings.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bm write-note: %w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}
