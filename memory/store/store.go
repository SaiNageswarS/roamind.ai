// Package store reads and writes Roamind's SQLite database. Its tables are
// defined in memory/sql-schemas and created by `task init`; each schema file
// has a Go file here with the models and queries of its tables:
//
//	01_users.sql      users.go
//	02_threads.sql    threads.go
//	03_goals.sql      goals.go
//	04_interests.sql  interests.go
//	05_news.sql       news.go
//	06_events.sql     events.go
//
// Conversation content itself lives only in claude's session files.
package store

import (
	"context"
	"database/sql"
	"errors"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct{ db *sql.DB }

// Open opens (creating if needed) the database at path. Its tables come
// from memory/sql-schemas, applied by `task init`.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// Ready returns an error when the tables have not been created yet.
func (s *Store) Ready(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return errors.New("the store has no tables")
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// execer is a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
