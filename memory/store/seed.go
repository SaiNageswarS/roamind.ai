package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// SeedInterests makes the catalog's curated interests (owner "") those in
// dir/seed/interests.yaml, where dir is the repo's memory folder: it adds or
// updates each, reading its rubric from the file it names, and deletes
// curated interests no longer listed, with their follows. Safe to repeat;
// `task init` runs it (`worker -seed`).
func (s *Store) SeedInterests(ctx context.Context, dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "seed", "interests.yaml"))
	if err != nil {
		return err
	}
	var seed []struct {
		Interest string   `yaml:"interest"`
		Default  bool     `yaml:"default"`
		Rubric   string   `yaml:"rubric"`
		Sources  []string `yaml:"sources"`
	}
	if err := yaml.Unmarshal(b, &seed); err != nil {
		return fmt.Errorf("seed/interests.yaml: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	names := make([]string, 0, len(seed))
	for _, in := range seed {
		rubric, err := os.ReadFile(filepath.Join(dir, "seed", in.Rubric))
		if err != nil {
			return fmt.Errorf("rubric of %s: %w", in.Interest, err)
		}
		sources, err := json.Marshal(in.Sources)
		if err != nil {
			return err
		}
		names = append(names, in.Interest)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO interests (interest, owner, sources, rubric, is_default, created_on, updated_on)
			VALUES ($1, '', $2, $3, $4, $5, $5)
			ON CONFLICT (owner, interest) DO UPDATE SET
				interest = excluded.interest, sources = excluded.sources, rubric = excluded.rubric,
				is_default = excluded.is_default, updated_on = excluded.updated_on`,
			in.Interest, string(sources), string(rubric), in.Default, now); err != nil {
			return err
		}
	}
	// ON DELETE CASCADE removes the follows of a dropped interest.
	if _, err := tx.ExecContext(ctx, `DELETE FROM interests WHERE owner = '' AND NOT (interest = ANY($1::citext[]))`, names); err != nil {
		return err
	}
	return tx.Commit()
}
