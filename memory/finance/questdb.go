// Package finance is the finance assistant's ledger in QuestDB: market data
// shared by every user, and each user's observations, trades, holdings and
// valuations. Its tables are defined in memory/questdb-schemas and created by
// `task init` (bin/worker -questdb-init); its state (accounts, uploads,
// conflicts, alerts) is in the SQLite store.
//
// It is the only code that queries QuestDB. Every function on a user's data
// takes the user's id and binds it as a parameter, so a caller can't read or
// write another user's rows (TestTenancy).
//
// Decimals cross this package as text ("12.345", "" for unknown), so no
// precision is lost between QuestDB's DECIMAL columns and the ledger's maths.
package finance

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultAddr is where deploy/bin/run-questdb.sh has QuestDB listen for
// Postgres wire.
const DefaultAddr = "127.0.0.1:8812"

// DB is a connection pool to QuestDB, as its admin user.
type DB struct{ pool *pgxpool.Pool }

// Open connects to QuestDB's Postgres wire at addr (host:port) as admin with
// password, which must not be QuestDB's default.
func Open(ctx context.Context, addr, password string) (*DB, error) {
	if password == "" || password == "quest" {
		return nil, errors.New("finance: ROAMIND_QUESTDB_PASSWORD is not set, or is QuestDB's default")
	}
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword("admin", password), Host: addr, Path: "/qdb"}
	cfg, err := pgxpool.ParseConfig(dsn.String())
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("finance: QuestDB at %s: %w", addr, err)
	}
	return &DB{pool: pool}, nil
}

func (d *DB) Close() { d.pool.Close() }

// Init runs the statements of every .sql file in dir, in name order. They
// are CREATE TABLE IF NOT EXISTS and ALTER TABLE … DEDUP statements, so it's
// safe to repeat.
func (d *DB) Init(ctx context.Context, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("finance: no schemas in %s", dir)
	}
	slices.Sort(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		for _, stmt := range statements(string(b)) {
			if _, err := d.pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("finance: %s: %w\n%s", filepath.Base(f), err, stmt)
			}
		}
	}
	return nil
}

// statements splits a schema file into its statements, without the --
// comments. Schemas have no string literals holding -- or ;.
func statements(sql string) []string {
	var b strings.Builder
	for line := range strings.Lines(sql) {
		line, _, _ = strings.Cut(line, "--")
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for stmt := range strings.SplitSeq(b.String(), ";") {
		if stmt = strings.TrimSpace(stmt); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// WaitApplied waits until QuestDB has applied every write committed to the
// tables so far: a WAL table's rows are readable only once its writer has
// caught up with its sequencer. A suspended table is an error.
func (d *DB) WaitApplied(ctx context.Context, tables ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		pending := 0
		rows, err := d.pool.Query(ctx, `SELECT name, suspended, writerTxn, sequencerTxn, errorMessage FROM wal_tables()`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name, msg string
			var suspended bool
			var writer, sequencer int64
			if err := rows.Scan(&name, &suspended, &writer, &sequencer, nullable(&msg)); err != nil {
				rows.Close()
				return err
			}
			if !slices.Contains(tables, name) {
				continue
			}
			if suspended {
				rows.Close()
				return fmt.Errorf("finance: QuestDB suspended table %s: %s", name, msg)
			}
			if writer < sequencer {
				pending++
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if pending == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("finance: waiting for QuestDB to apply %v: %w", tables, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// nullable scans a text column that may be NULL into s, "" for NULL.
func nullable(s *string) scanText { return scanText{s} }

type scanText struct{ p *string }

func (t scanText) Scan(v any) error {
	switch v := v.(type) {
	case nil:
		*t.p = ""
	case string:
		*t.p = v
	default:
		return fmt.Errorf("finance: can't scan %T as text", v)
	}
	return nil
}

// orNull is v, or NULL when v is "".
func orNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// timeOrNull is t, or NULL when t is zero.
func timeOrNull(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// trimDecimal drops the trailing zeros QuestDB pads a DECIMAL's scale with:
// 12.34500000 is 12.345, 3.00000000 is 3.
func trimDecimal(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
