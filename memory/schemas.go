package memory

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// schemas are the Picoschema notes in schemas/, seeded into every project.
// bm picks them up as plain markdown files; fields map to observation
// categories, and validation is "warn", so they guide rather than gate.
//
//go:embed schemas/*.md
var schemas embed.FS

// seedSchemas writes any missing schema notes into projectDir/schemas.
// Existing files, possibly edited, are left alone.
func seedSchemas(projectDir string) error {
	dir := filepath.Join(projectDir, "schemas")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := schemas.ReadDir("schemas")
	if err != nil {
		return err
	}
	for _, e := range entries {
		body, err := schemas.ReadFile("schemas/" + e.Name())
		if err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, e.Name()), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		_, err = f.Write(body)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
