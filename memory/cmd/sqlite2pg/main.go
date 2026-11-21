// Command sqlite2pg copies Roamind's old SQLite database into PostgreSQL,
// once:
//
//	sqlite2pg -sqlite data/roamind.db [-postgres URI]
//
// The Postgres tables must exist (`task init`) and be empty. It copies every
// table both databases have, in one transaction, in SQLite's rowid order
// (which the tables' seq columns keep), converts SQLite's 0/1 to booleans,
// then sets the identity columns' sequences past the copied ids and checks
// that each table has as many rows as it had. Nothing is written to SQLite.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"

	"roamind.ai/memory/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("sqlite2pg", "err", err)
		os.Exit(1)
	}
}

func run() error {
	path := flag.String("sqlite", "./data/roamind.db", "the SQLite database to read")
	uri := flag.String("postgres", "", "the PostgreSQL URI (default: POSTGRES_URI, else "+store.DefaultURI+")")
	flag.Parse()
	if *uri == "" {
		*uri = os.Getenv("POSTGRES_URI")
	}
	if *uri == "" {
		*uri = store.DefaultURI
	}
	if _, err := os.Stat(*path); err != nil {
		return err
	}
	ctx := context.Background()
	src, err := sql.Open("sqlite", "file:"+*path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer src.Close()
	pool, err := pgxpool.New(ctx, *uri)
	if err != nil {
		return err
	}
	defer pool.Close()

	tables, err := order(ctx, pool)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	counts := map[string]int{}
	for _, t := range tables {
		scols, err := sqliteColumns(src, t)
		if err != nil || len(scols) == 0 {
			slog.Info("not in SQLite, skipped", "table", t)
			continue
		}
		pcols, err := pgColumns(ctx, tx, t)
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return fmt.Errorf("table %s already has %d rows; migrate into empty tables", t, n)
		}
		var cols []string
		var bools []bool
		for _, c := range scols {
			if kind, ok := pcols[c]; ok {
				cols = append(cols, c)
				bools = append(bools, kind == "boolean")
			}
		}
		if counts[t], err = copyTable(ctx, src, tx, t, cols, bools); err != nil {
			return fmt.Errorf("table %s: %w", t, err)
		}
		slog.Info("copied", "table", t, "rows", counts[t])
	}
	// Identity columns continue after the copied ids.
	rows, err := tx.Query(ctx, `SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND is_identity = 'YES'`)
	if err != nil {
		return err
	}
	type idc struct{ table, col string }
	var ids []idc
	for rows.Next() {
		var c idc
		if err := rows.Scan(&c.table, &c.col); err != nil {
			return err
		}
		ids = append(ids, c)
	}
	rows.Close()
	for _, c := range ids {
		q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence(%s, %s), greatest(coalesce(max(%s), 0), 1), max(%s) IS NOT NULL) FROM %s`,
			quote(c.table), quote(c.col), pgx.Identifier{c.col}.Sanitize(), pgx.Identifier{c.col}.Sanitize(), pgx.Identifier{c.table}.Sanitize())
		if _, err := tx.Exec(ctx, q); err != nil {
			return fmt.Errorf("sequence of %s.%s: %w", c.table, c.col, err)
		}
	}
	// Row counts agree.
	for t, want := range counts {
		var got int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{t}.Sanitize()).Scan(&got); err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("table %s: %d rows in PostgreSQL, %d copied", t, got, want)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	slog.Info("done", "tables", len(counts), "rows", total)
	return nil
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// order returns the schema's tables, each after the tables it references.
func order(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT c.relname, coalesce(p.relname, '') FROM pg_class c
		LEFT JOIN pg_constraint k ON k.conrelid = c.oid AND k.contype = 'f'
		LEFT JOIN pg_class p ON p.oid = k.confrelid
		WHERE c.relkind = 'r' AND c.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deps := map[string][]string{}
	for rows.Next() {
		var t, ref string
		if err := rows.Scan(&t, &ref); err != nil {
			return nil, err
		}
		deps[t] = append(deps[t], ref)
	}
	var names []string
	for t := range deps {
		names = append(names, t)
	}
	sort.Strings(names)
	var out []string
	done := map[string]bool{}
	var visit func(string)
	visit = func(t string) {
		if done[t] {
			return
		}
		done[t] = true
		for _, d := range deps[t] {
			if d != "" && d != t {
				visit(d)
			}
		}
		out = append(out, t)
	}
	for _, t := range names {
		visit(t)
	}
	return out, rows.Err()
}

func sqliteColumns(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// pgColumns maps the table's column names to their data types.
func pgColumns(ctx context.Context, tx pgx.Tx, table string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var c, k string
		if err := rows.Scan(&c, &k); err != nil {
			return nil, err
		}
		m[c] = k
	}
	return m, rows.Err()
}

func copyTable(ctx context.Context, src *sql.DB, tx pgx.Tx, table string, cols []string, bools []bool) (int, error) {
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pgx.Identifier{c}.Sanitize()
	}
	rows, err := src.Query(fmt.Sprintf(`SELECT %s FROM %s ORDER BY rowid`, strings.Join(quoted, ", "), pgx.Identifier{table}.Sanitize()))
	if err != nil {
		// A table without rowid (none today) has no order to keep.
		if rows, err = src.Query(fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(quoted, ", "), pgx.Identifier{table}.Sanitize())); err != nil {
			return 0, err
		}
	}
	defer rows.Close()
	ph := make([]string, len(cols))
	for i := range ph {
		ph[i] = fmt.Sprintf("$%d", i+1)
	}
	// OVERRIDING SYSTEM VALUE keeps the copied ids of GENERATED ALWAYS columns.
	ins := fmt.Sprintf(`INSERT INTO %s (%s) OVERRIDING SYSTEM VALUE VALUES (%s)`, pgx.Identifier{table}.Sanitize(), strings.Join(quoted, ", "), strings.Join(ph, ", "))
	n := 0
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		args := make([]any, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case []byte:
				args[i] = string(x)
			case int64:
				if bools[i] {
					args[i] = x != 0
				} else {
					args[i] = x
				}
			default:
				args[i] = v
			}
		}
		if _, err := tx.Exec(ctx, ins, args...); err != nil {
			return n, fmt.Errorf("row %d: %w", n+1, err)
		}
		n++
	}
	return n, rows.Err()
}
