// Package storetest sets up stores for tests the way `task init` does in
// production: tables from memory/sql-schemas and the curated interests from
// memory/seed/interests.yaml, in a PostgreSQL schema of the test's own (see
// package pgtest for the test database).
package storetest

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"roamind.ai/memory/store"
	"roamind.ai/memory/store/pgtest"
)

// uris maps each store Open returned to the URI of its schema.
var uris sync.Map

// Root is the repo root.
func Root(t testing.TB) string { return pgtest.Root(t) }

// Open returns a store with the repo's tables in a schema of its own,
// closed and dropped when the test ends. The test is skipped when there is
// no test database.
func Open(t testing.TB) *store.Store {
	t.Helper()
	uri := pgtest.Schema(t)
	pgtest.Apply(t, uri)
	st, err := store.Open(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	uris.Store(st, uri)
	t.Cleanup(func() { st.Close() })
	return st
}

// Seed loads memory/seed/interests.yaml into st, a store from Open, as
// `task init` does.
func Seed(t testing.TB, st *store.Store) {
	t.Helper()
	if err := st.SeedInterests(context.Background(), filepath.Join(Root(t), "memory")); err != nil {
		t.Fatal(err)
	}
}

// Curate adds curated interests to st, a store from Open.
func Curate(t testing.TB, st *store.Store, ins ...store.Interest) {
	t.Helper()
	for _, in := range ins {
		sources, err := json.Marshal(in.Sources)
		if err != nil {
			t.Fatal(err)
		}
		exec(t, st, `
			INSERT INTO interests (interest, owner, sources, rubric, is_default, created_at, updated_at)
			VALUES ($1, '', $2, $3, $4, extract(epoch FROM now())::bigint, extract(epoch FROM now())::bigint)`,
			in.Name, string(sources), in.Rubric, in.Default)
	}
}

// SetSources replaces the feeds of the curated interest named name.
func SetSources(t testing.TB, st *store.Store, name string, sources ...string) {
	t.Helper()
	b, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if n := exec(t, st, `UPDATE interests SET sources = $1 WHERE owner = '' AND interest = $2`, string(b), name); n != 1 {
		t.Fatal("no curated interest " + name)
	}
}

// MarkSent records urls as sent to the user, as a posted digest does.
func MarkSent(t testing.TB, st *store.Store, userID string, urls ...string) {
	t.Helper()
	for _, u := range urls {
		exec(t, st, `INSERT INTO sent_urls (user_id, url_hash, sent_at) VALUES ($1, $2, extract(epoch FROM now())::bigint)`,
			userID, store.URLHash(u))
	}
}

// exec runs a statement on st's database and returns the rows it changed.
func exec(t testing.TB, st *store.Store, query string, args ...any) int64 {
	t.Helper()
	if _, ok := uris.Load(st); !ok {
		t.Fatal("storetest: store not from storetest.Open")
	}
	tag, err := st.DB().Pool().Exec(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	return tag.RowsAffected()
}
