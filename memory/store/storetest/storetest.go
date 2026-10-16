// Package storetest sets up stores for tests the way `task init` does in
// production: tables from memory/sql-schemas and the curated interests from
// memory/seed/interests.sql.
package storetest

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"modernc.org/sqlite"

	"roamind.ai/memory/store"
)

// paths maps each store Open returned to its database file.
var paths sync.Map

// root is the repo root: the nearest directory up with a go.work.
var root = sync.OnceValues(func() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.work")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no go.work above the test")
		}
		dir = parent
	}
})

// readfile is the sqlite3 CLI's function the seed reads rubrics with, for
// paths from the repo root; NULL when the file can't be read.
func init() {
	sqlite.MustRegisterScalarFunction("readfile", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		name, _ := args[0].(string)
		dir, err := root()
		if err != nil {
			return nil, err
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil
		}
		return b, nil
	})
}

// Root is the repo root.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := root()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Open returns a store in a temporary directory with the repo's tables,
// closed when the test ends.
func Open(t testing.TB) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.db")
	exec(t, path, func(db *sql.DB) error {
		names, err := filepath.Glob(filepath.Join(Root(t), "memory", "sql-schemas", "*.sql"))
		if err != nil {
			return err
		}
		for _, name := range names {
			b, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if _, err := db.Exec(string(b)); err != nil {
				return err
			}
		}
		return nil
	})
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	paths.Store(st, path)
	t.Cleanup(func() { st.Close() })
	return st
}

// Seed runs memory/seed/interests.sql on st, a store from Open, as
// `task init` does.
func Seed(t testing.TB, st *store.Store) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Root(t), "memory", "seed", "interests.sql"))
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pathOf(t, st), func(db *sql.DB) error {
		_, err := db.Exec(string(b))
		return err
	})
}

// Curate adds curated interests to st, a store from Open.
func Curate(t testing.TB, st *store.Store, ins ...store.Interest) {
	t.Helper()
	exec(t, pathOf(t, st), func(db *sql.DB) error {
		for _, in := range ins {
			sources, err := json.Marshal(in.Sources)
			if err != nil {
				return err
			}
			if _, err := db.Exec(`
				INSERT INTO interests (interest, owner, sources, rubric, is_default, created_at, updated_at)
				VALUES (?, '', ?, ?, ?, unixepoch(), unixepoch())`,
				in.Name, string(sources), in.Rubric, in.Default); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetSources replaces the feeds of the curated interest named name.
func SetSources(t testing.TB, st *store.Store, name string, sources ...string) {
	t.Helper()
	b, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pathOf(t, st), func(db *sql.DB) error {
		res, err := db.Exec(`UPDATE interests SET sources = ? WHERE owner = '' AND interest = ?`, string(b), name)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("no curated interest " + name)
		}
		return nil
	})
}

// MarkSent records urls as sent to the user, as a posted digest does.
func MarkSent(t testing.TB, st *store.Store, userID string, urls ...string) {
	t.Helper()
	exec(t, pathOf(t, st), func(db *sql.DB) error {
		for _, u := range urls {
			if _, err := db.Exec(`INSERT INTO sent_urls (user_id, url_hash, sent_at) VALUES (?, ?, unixepoch())`,
				userID, store.URLHash(u)); err != nil {
				return err
			}
		}
		return nil
	})
}

func pathOf(t testing.TB, st *store.Store) string {
	t.Helper()
	path, ok := paths.Load(st)
	if !ok {
		t.Fatal("storetest: store not from storetest.Open")
	}
	return path.(string)
}

// exec runs f on its own connection to the database at path.
func exec(t testing.TB, path string, f func(*sql.DB) error) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := f(db); err != nil {
		t.Fatal(err)
	}
}
