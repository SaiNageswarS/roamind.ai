// Package pgtest gives tests a PostgreSQL schema of their own with the
// repo's tables (memory/sql-schemas), as `task init` creates them. It
// imports nothing of Roamind, so the store's own tests can use it too.
//
// Tests connect to ROAMIND_TEST_POSTGRES_URI, by default the database
// roamind_test over the local socket (create it with `createdb
// roamind_test`); they are skipped when it can't be reached.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultURI is the test database, over the local socket.
const DefaultURI = "postgres:///roamind_test?host=/var/run/postgresql"

func baseURI() string {
	if u := os.Getenv("ROAMIND_TEST_POSTGRES_URI"); u != "" {
		return u
	}
	return DefaultURI
}

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

// Root is the repo root.
func Root(t testing.TB) string {
	t.Helper()
	dir, err := root()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Schema creates an empty schema for the test and returns the URI of a
// connection that uses it. The schema is dropped when the test ends.
func Schema(t testing.TB) (uri string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, baseURI())
	if err == nil {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = conn.Ping(pingCtx)
		cancel()
	}
	if err != nil {
		t.Skipf("no test database at %s (createdb roamind_test): %v", baseURI(), err)
	}
	var b [6]byte
	rand.Read(b[:])
	name := "t_" + hex.EncodeToString(b[:])
	if _, err := conn.Exec(ctx, `CREATE SCHEMA `+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Exec(context.Background(), `DROP SCHEMA `+name+` CASCADE`)
		conn.Close(context.Background())
	})
	u, err := url.Parse(baseURI())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// The schema first, then public, which holds the citext extension.
	q.Set("search_path", name+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// Apply runs the repo's schema files, in name order, in the schema uri
// connects to.
func Apply(t testing.TB, uri string) {
	t.Helper()
	ctx := context.Background()
	names, err := filepath.Glob(filepath.Join(Root(t), "memory", "sql-schemas", "*.sql"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no schema files: %v", err)
	}
	conn, err := pgx.Connect(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, string(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
