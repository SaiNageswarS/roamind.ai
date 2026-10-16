package notes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Setup creates project for a new user, or completes it: its folders, its
// bm config.json and the schema notes it lacks. The config lists only this
// project and carries the settings in memory/basic-memory-config.json
// (search models), merged over any keys bm has added. Schema notes come
// from memory/basic-memory-schemas; ones the project has, possibly edited,
// are kept. Safe to repeat. `task init` brings existing projects up to
// date the same way (BM_CONFIG in Taskfile.yml; TestSetupMatchesTaskInit).
func (n Notes) Setup(project string) error {
	if err := ValidProject(project); err != nil {
		return err
	}
	for _, d := range []string{n.ConfigDir(project), filepath.Join(n.NotesDir(project), "schemas")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := n.writeConfig(project); err != nil {
		return err
	}
	return copySchemas(filepath.Join(n.Memory, "basic-memory-schemas"), filepath.Join(n.NotesDir(project), "schemas"))
}

// Ready reports whether project has been set up: its config.json is
// there and readable, so bm can start. A project that isn't goes through
// Setup again.
func (n Notes) Ready(project string) bool {
	if ValidProject(project) != nil {
		return false
	}
	cfg := map[string]any{}
	return readJSON(filepath.Join(n.ConfigDir(project), "config.json"), &cfg) == nil
}

// writeConfig writes project's config.json. Writing it directly, rather than
// `bm project add`, saves a slow CLI start and keeps bm's implicit "main"
// project out.
func (n Notes) writeConfig(project string) error {
	path := filepath.Join(n.ConfigDir(project), "config.json")
	cfg := map[string]any{}
	if err := readJSON(path, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	settings := map[string]any{}
	if err := readJSON(filepath.Join(n.Memory, "basic-memory-config.json"), &settings); err != nil {
		return err
	}
	merge(cfg, settings)
	cfg["projects"] = map[string]any{
		project: map[string]any{"path": n.NotesDir(project), "mode": "local"},
	}
	cfg["default_project"] = project
	cfg["project_root"] = n.NotesDir(project)
	cfg["semantic_embedding_cache_dir"] = filepath.Join(n.Dir, "fastembed_cache")
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(n.ConfigDir(project), "config-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// merge sets src's keys in dst, merging objects key by key, as jq's *
// does.
func merge(dst, src map[string]any) {
	for k, v := range src {
		if sv, ok := v.(map[string]any); ok {
			if dv, ok := dst[k].(map[string]any); ok {
				merge(dv, sv)
				continue
			}
		}
		dst[k] = v
	}
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// copySchemas copies the Picoschema notes in src that dir lacks. bm picks
// them up as plain markdown files; fields map to observation categories,
// and validation is "warn", so they guide rather than gate.
func copySchemas(src, dir string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(src, e.Name()))
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
