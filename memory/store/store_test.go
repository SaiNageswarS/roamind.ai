package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// open returns a store with the repo's tables (memory/sql-schemas), as
// `task init` creates them. (storetest does this for other packages.)
func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	applySchema(t, s)
	return s
}

func applySchema(t *testing.T, s *Store) {
	t.Helper()
	names, _ := filepath.Glob("../sql-schemas/*.sql")
	if len(names) == 0 {
		t.Fatal("no schema files")
	}
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(string(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// The schema files apply in order, and again on an existing database.
func TestSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Ready(context.Background()); (err == nil) != (i == 1) {
			t.Fatalf("ready %d: %v", i, err)
		}
		applySchema(t, s)
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&n); err != nil || n != 13 {
			t.Fatalf("open %d: %d tables, %v", i, n, err)
		}
		s.Close()
	}
}
