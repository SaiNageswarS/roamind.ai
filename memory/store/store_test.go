package store

import (
	"context"
	"testing"

	"roamind.ai/memory/store/pgtest"
)

// open returns a store with the repo's tables (memory/sql-schemas), as
// `task init` creates them, in a schema of its own. (storetest does this
// for other packages.)
func open(t *testing.T) *Store {
	t.Helper()
	uri := pgtest.Schema(t)
	pgtest.Apply(t, uri)
	s, err := Open(context.Background(), uri)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// The schema files apply in order, and again on a database that has them.
func TestSchema(t *testing.T) {
	uri := pgtest.Schema(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		s, err := Open(ctx, uri)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Ready(ctx); (err == nil) != (i == 1) {
			t.Fatalf("ready %d: %v", i, err)
		}
		pgtest.Apply(t, uri)
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema()`).Scan(&n); err != nil || n != 24 {
			t.Fatalf("open %d: %d tables, %v", i, n, err)
		}
		s.Close()
	}
}
