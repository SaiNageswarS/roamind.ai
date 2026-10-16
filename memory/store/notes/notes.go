// Package notes reads and writes users' Basic Memory notes, the part of
// their data kept as markdown rather than in SQLite. Each user has a
// project, laid out under Dir:
//
//	Dir/fastembed_cache/           embedding model, downloaded once
//	Dir/users/<project>/config/    bm's config.json (only this project) and
//	                               memory.db (index, full-text search and
//	                               embeddings)
//	Dir/users/<project>/notes/     the markdown notes
//
// Every project is self-contained, with its own bm config and database, so
// users share nothing but the read-only embedding model.
//
// Notes are read straight from disk and written through bm's CLI, which
// indexes them at once. A project is created by Setup, from the user-setup
// workflow, before anything reads or writes it.
package notes

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Notes locates the projects.
type Notes struct {
	Dir string
	// Binary is the bm executable: WriteNote runs it, and claude starts it
	// as the memory MCP server.
	Binary string
	// Memory is the repo's memory folder, where Setup reads
	// basic-memory-config.json and basic-memory-schemas.
	Memory string
}

// New returns the projects in dir, set up from the repo's memory folder,
// finding bin ("" means "bm") on PATH. Paths are made absolute: claude
// starts bm, from the user's workspace.
func New(dir, memory, bin string) (*Notes, error) {
	if bin == "" {
		bin = "bm"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("basic memory: %w", err)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	if memory, err = filepath.Abs(memory); err != nil {
		return nil, err
	}
	return &Notes{Dir: dir, Binary: path, Memory: memory}, nil
}

// ConfigDir is project's bm config folder.
func (n Notes) ConfigDir(project string) string {
	return filepath.Join(n.Dir, "users", project, "config")
}

// NotesDir is project's notes folder.
func (n Notes) NotesDir(project string) string {
	return filepath.Join(n.Dir, "users", project, "notes")
}

var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidProject returns an error for a name that can't be a project, so it
// never reaches a path.
func ValidProject(project string) error {
	if !projectRe.MatchString(project) {
		return fmt.Errorf("invalid memory project %q", project)
	}
	return nil
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ProjectBase is the project name for a user, before the index that keeps
// it unique: bm-u-<slugified name>.
func ProjectBase(userName string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(userName), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		s = "user"
	}
	return "bm-u-" + s
}
