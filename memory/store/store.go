// Package store reads and writes Roamind's PostgreSQL database. Its tables are
// defined in memory/sql-schemas and created by `task init`; each schema file
// has a Go file here with the models and queries of its tables:
//
//	01_users.sql      users.go
//	02_threads.sql    threads.go
//	03_goals.sql      goals.go
//	04_interests.sql  interests.go
//	05_news.sql       news.go
//	06_events.sql     events.go
//	07_jobs.sql       jobs.go
//	08_channels.sql   channels.go
//	09_items.sql      items.go
//	10_inbox.sql      inbox.go
//	11_mail.sql       mail.go
//	12_finance.sql    finance.go
//
// Conversation content itself lives only in claude's session files.
package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/SaiNageswarS/go-api-boot/odm/postgres"
	"github.com/jackc/pgx/v5/stdlib"
)

// DefaultURI is the database roamind over the local PostgreSQL socket,
// for POSTGRES_URI when it is not set. The role is the OS user's (peer
// authentication), so there is no password.
const DefaultURI = "postgres:///roamind?host=/var/run/postgresql"

// Store wraps the PostgreSQL database. Simple tables are read and written
// with go-api-boot's ORM (rows.go) on pg; the rest with SQL, on db, a
// database/sql handle over the same connection pool.
type Store struct {
	db     *sql.DB
	pg     *postgres.DB
	schema string // the schema of the tables: the tenant of the ORM
}

// Open connects to the database at uri, a PostgreSQL connection URI such as
// postgres:///roamind?host=/var/run/postgresql (the local socket). Its
// tables come from memory/sql-schemas, applied by `task init`.
func Open(ctx context.Context, uri string) (*Store, error) {
	pg, err := postgres.Open(ctx, uri)
	if err != nil {
		return nil, err
	}
	s := &Store{db: stdlib.OpenDBFromPool(pg.Pool()), pg: pg}
	if err := s.db.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&s.schema); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// DB is the go-api-boot database, for its ORM and raw pgx.
func (s *Store) DB() *postgres.DB { return s.pg }

// Ready returns an error when the tables have not been created yet.
func (s *Store) Ready(ctx context.Context) error {
	var ok bool
	if err := s.db.QueryRowContext(ctx, `SELECT to_regclass('users') IS NOT NULL`).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("the store has no tables")
	}
	return nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	s.pg.Close()
	return err
}

// execer is a database or a transaction.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
