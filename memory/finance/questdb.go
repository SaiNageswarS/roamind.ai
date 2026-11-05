// Package finance is the finance assistant's ledger in QuestDB: market data
// shared by every user, and each user's trade events, trades, holdings and
// valuations. Its tables are defined in memory/questdb-schemas and created by
// `task init` (bin/worker -questdb-init); its state (accounts, uploads,
// conflicts, alerts) is in the SQLite store.
//
// It talks to QuestDB with the official client over QWP, QuestDB's
// WebSocket protocol on its HTTP port. Writes are INSERTs run with Exec,
// which returns once the server has taken the statement or refused it; the
// client's ingestion sender reports refusals only later, in the background.
//
// It is the only Roamind code that queries QuestDB. Every function on a
// user's data takes the user's id and binds it as a parameter, so a caller
// can't read or write another user's rows (TestTenancy).
//
// Decimals cross this package as text ("12.345", "" for unknown), so no
// precision is lost between QuestDB's DECIMAL columns and the ledger's maths.
package finance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	qdb "github.com/questdb/go-questdb-client/v4"
)

// DefaultAddr is where deploy/bin/run-questdb.sh has QuestDB's HTTP port,
// which serves QWP, the web console and the REST API.
const DefaultAddr = "127.0.0.1:9000"

// DB is a pool of QWP query sessions to QuestDB.
type DB struct{ qdb *qdb.QuestDB }

// Open connects to QuestDB's HTTP port at addr (host:port) and checks it
// answers a query. Connections are made as they're needed (lazy_connect),
// so no ingestion connection is opened: writes are INSERTs.
func Open(ctx context.Context, addr string) (*DB, error) {
	q, err := qdb.NewQuestDB(ctx, "ws::addr="+addr+";lazy_connect=true;", qdb.WithQueryPoolMax(4))
	if err != nil {
		return nil, fmt.Errorf("finance: QuestDB at %s: %w", addr, err)
	}
	d := &DB{qdb: q}
	if err := d.query(ctx, `SELECT 1`, nil, func(*qdb.QwpColumnBatch, int) error { return nil }); err != nil {
		d.Close()
		return nil, fmt.Errorf("finance: QuestDB at %s: %w", addr, err)
	}
	return d, nil
}

func (d *DB) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = d.qdb.Close(ctx)
}

// exec runs one statement with its parameters.
func (d *DB) exec(ctx context.Context, sql string, binds qdb.QwpBindFunc) error {
	s, err := d.qdb.BorrowQuery(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	var opts []qdb.QwpQueryOption
	if binds != nil {
		opts = append(opts, qdb.WithQwpQueryBinds(binds))
	}
	_, err = s.Exec(ctx, sql, opts...)
	return err
}

// query runs a SELECT and calls row for each row of its result.
func (d *DB) query(ctx context.Context, sql string, binds qdb.QwpBindFunc, row func(b *qdb.QwpColumnBatch, r int) error) error {
	s, err := d.qdb.BorrowQuery(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	var opts []qdb.QwpQueryOption
	if binds != nil {
		opts = append(opts, qdb.WithQwpQueryBinds(binds))
	}
	cur := s.Query(ctx, sql, opts...)
	defer cur.Close()
	for b, err := range cur.Batches() {
		if err != nil {
			return err
		}
		for r := range b.RowCount() {
			if err := row(b, r); err != nil {
				return err
			}
		}
	}
	return nil
}

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
			if err := d.exec(ctx, stmt, nil); err != nil {
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
		err := d.query(ctx, `SELECT name, suspended, writerTxn, sequencerTxn, errorMessage FROM wal_tables()`, nil,
			func(b *qdb.QwpColumnBatch, r int) error {
				name := b.String(0, r)
				if !slices.Contains(tables, name) {
					return nil
				}
				if b.Bool(1, r) {
					return fmt.Errorf("finance: QuestDB suspended table %s: %s", name, b.String(4, r))
				}
				if b.Int64(2, r) < b.Int64(3, r) {
					pending++
				}
				return nil
			})
		if err != nil {
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

// insert writes rows into table, a few dozen per statement. Each row has a
// value per column: a string (bound as text, "" as NULL, and cast when the
// column has a cast, such as a DECIMAL), a time.Time (zero is NULL) or an
// int.
func (d *DB) insert(ctx context.Context, table string, cols []column, rows [][]any) error {
	const perStatement = 50
	for len(rows) > 0 {
		n := min(len(rows), perStatement)
		var sql strings.Builder
		names := make([]string, len(cols))
		for i, c := range cols {
			names[i] = c.name
		}
		fmt.Fprintf(&sql, "INSERT INTO %s (%s) VALUES ", table, strings.Join(names, ", "))
		for r := range n {
			if r > 0 {
				sql.WriteString(", ")
			}
			sql.WriteByte('(')
			for i, c := range cols {
				if i > 0 {
					sql.WriteString(", ")
				}
				fmt.Fprintf(&sql, "$%d", r*len(cols)+i+1)
				if c.cast != "" {
					sql.WriteString("::" + c.cast)
				}
			}
			sql.WriteByte(')')
		}
		batch := rows[:n]
		var bindErr error
		err := d.exec(ctx, sql.String(), func(b *qdb.QwpBinds) {
			for r, row := range batch {
				for i, v := range row {
					at := r*len(cols) + i
					switch v := v.(type) {
					case string:
						text(b, at, v)
					case time.Time:
						timestamp(b, at, v)
					case int:
						b.IntBind(at, int32(v))
					default:
						bindErr = fmt.Errorf("finance: can't bind %T", v)
					}
				}
			}
		})
		if err := errors.Join(bindErr, err); err != nil {
			return fmt.Errorf("finance: insert into %s: %w", table, err)
		}
		rows = rows[n:]
	}
	return nil
}

// column is a column insert writes, with the cast its text value needs.
type column struct{ name, cast string }

// Casts of text values.
const (
	castMoney = "DECIMAL(24, 8)" // amounts and quantities
	castPrice = "DECIMAL(20, 6)" // the price of one unit
	castRate  = "DECIMAL(20, 10)"
)

func cols(spec ...string) []column {
	out := make([]column, len(spec))
	for i, s := range spec {
		name, cast, _ := strings.Cut(s, ":")
		out[i] = column{name, cast}
	}
	return out
}

// text binds v as VARCHAR at i, or NULL when v is "". Symbols and decimals
// are bound as text too; a decimal's statement casts it.
func text(b *qdb.QwpBinds, i int, v string) {
	if v == "" {
		b.NullVarcharBind(i)
		return
	}
	b.VarcharBind(i, v)
}

// timestamp binds t at i, or NULL when t is zero.
func timestamp(b *qdb.QwpBinds, i int, t time.Time) {
	if t.IsZero() {
		b.NullTimestampMicrosBind(i)
		return
	}
	b.TimestampMicrosBind(i, t.UnixMicro())
}

// timeAt is the TIMESTAMP at (col, r), zero for NULL.
func timeAt(b *qdb.QwpColumnBatch, col, r int) time.Time {
	if b.IsNull(col, r) {
		return time.Time{}
	}
	return time.UnixMicro(b.Int64(col, r)).UTC()
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

var errNoUser = errors.New("finance: no user")

// day is t as a calendar day, at midnight UTC.
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
