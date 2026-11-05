package finance

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// testDB is a QuestDB the tests start in a temporary directory, on free
// ports, from the bin/questdb-<version> `task questdb-install` downloads,
// with the schemas applied. It is nil, and the tests skip, when there is
// none.
var testDB *DB

// testHTTP is its web console and REST API, read-only as in production.
var testHTTP string

// testPG is its Postgres wire.
var testPG string

func TestMain(m *testing.M) {
	stop, err := startQuestDB()
	if err != nil {
		fmt.Fprintln(os.Stderr, "finance tests:", err)
		os.Exit(1)
	}
	code := m.Run()
	stop()
	os.Exit(code)
}

func startQuestDB() (stop func(), err error) {
	homes, _ := filepath.Glob("../../bin/questdb-*/bin/questdb.sh")
	if len(homes) == 0 {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "questdb-test-")
	if err != nil {
		return nil, err
	}
	ports := make([]string, 3)
	for i := range ports {
		if ports[i], err = freePort(); err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(homes[0], "start", "-n", "-d", dir, "-t", fmt.Sprintf("test-%d", os.Getpid()))
	cmd.Env = append(os.Environ(),
		"QDB_HTTP_BIND_TO=127.0.0.1:"+ports[0],
		"QDB_HTTP_MIN_NET_BIND_TO=127.0.0.1:"+ports[1],
		"QDB_PG_NET_BIND_TO=127.0.0.1:"+ports[2],
		"QDB_HTTP_SECURITY_READONLY=true",
		"QDB_LINE_TCP_ENABLED=false",
		"QDB_LINE_HTTP_ENABLED=false",
		"QDB_LINE_UDP_ENABLED=false",
		"QDB_TELEMETRY_ENABLED=false",
		"QDB_PG_PASSWORD=test-password",
		"QDB_PG_READONLY_USER_ENABLED=true",
		"QDB_PG_READONLY_PASSWORD=test-readonly",
	)
	// The tag lets it run beside another QuestDB. questdb.sh runs java as
	// its child: stop kills the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	stop = func() {
		if testDB != nil {
			testDB.Close()
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		os.RemoveAll(dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for {
		if testDB, err = Open(ctx, "127.0.0.1:"+ports[2], "test-password"); err == nil {
			break
		}
		if ctx.Err() != nil {
			stop()
			return nil, fmt.Errorf("QuestDB didn't start: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	testHTTP = "http://127.0.0.1:" + ports[0]
	testPG = "127.0.0.1:" + ports[2]
	if err := testDB.Init(ctx, "../questdb-schemas"); err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

func db(t *testing.T) *DB {
	t.Helper()
	if testDB == nil {
		t.Skip("no QuestDB in bin/ (task questdb-install)")
	}
	return testDB
}

func TestInitRepeats(t *testing.T) {
	d := db(t)
	if err := d.Init(context.Background(), "../questdb-schemas"); err != nil {
		t.Fatalf("second init: %v", err)
	}
}

func TestOpenRefusesDefaultPassword(t *testing.T) {
	for _, pw := range []string{"", "quest"} {
		if _, err := Open(context.Background(), DefaultAddr, pw); err == nil {
			t.Errorf("Open with password %q succeeded", pw)
		}
	}
}

func TestHTTPReadOnly(t *testing.T) {
	db(t)
	for _, q := range []string{"SELECT count() FROM observations", "DROP TABLE observations"} {
		resp, err := http.Get(testHTTP + "/exec?query=" + url.QueryEscape(q))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if ok := resp.StatusCode == http.StatusOK; ok != (q[0] == 'S') {
			t.Errorf("%s over HTTP: %s", q, resp.Status)
		}
	}
}

// TestReadonlyUser checks the read-only user, for the notebook, can query
// but not write.
func TestReadonlyUser(t *testing.T) {
	db(t)
	ctx := context.Background()
	c, err := pgx.Connect(ctx, "postgres://user:test-readonly@"+testPG+"/qdb")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var n int64
	if err := c.QueryRow(ctx, `SELECT count() FROM observations`).Scan(&n); err != nil {
		t.Errorf("read-only query: %v", err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO observations (ts, user_id, source, line) VALUES (now(), 'x', 'chat:x', 1)`); err == nil {
		t.Error("the read-only user wrote a row")
	}
	if _, err := pgx.Connect(ctx, "postgres://admin:quest@"+testPG+"/qdb"); err == nil {
		t.Error("admin logged in with the default password")
	}
}

func TestObservations(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	obs := []Observation{
		{Date: day, Source: "upload:abc", Line: 1, Account: "a1", ISIN: "US0378331005", Side: "buy",
			Quantity: "10", Price: "201.125", Amount: "2011.25", Fees: "1.5", Currency: "USD",
			Ref: "ord-1", Doc: "tradebook", ReceivedAt: day.Add(20 * time.Hour)},
		{Date: day, Source: "upload:abc", Line: 2, Account: "a1", ISIN: "US0378331005", Side: "sell",
			Quantity: "0.5", Price: "202", Currency: "USD", Doc: "tradebook"},
	}
	// Written twice, as a replayed source or a retried tool call would.
	for range 2 {
		if err := d.AddObservations(ctx, "obs-user", obs); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.WaitApplied(ctx, "observations"); err != nil {
		t.Fatal(err)
	}
	got, err := d.Observations(ctx, "obs-user", day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, obs) {
		t.Errorf("Observations =\n%+v\nwant\n%+v", got, obs)
	}

	// The same source and line again replaces the row.
	fixed := obs[1]
	fixed.Quantity = "5"
	if err := d.AddObservations(ctx, "obs-user", []Observation{fixed}); err != nil {
		t.Fatal(err)
	}
	if err := d.WaitApplied(ctx, "observations"); err != nil {
		t.Fatal(err)
	}
	got, err = d.Observations(ctx, "obs-user", day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[1].Quantity != "5" {
		t.Errorf("after the correction: %+v", got)
	}

	if err := d.AddObservations(ctx, "", obs); err == nil {
		t.Error("observations without a user were written")
	}
	if err := d.AddObservations(ctx, "obs-user", []Observation{{Source: "chat:x"}}); err == nil {
		t.Error("an observation without a date was written")
	}
}

// TestTenancy writes two users' rows and checks each user reads only theirs.
func TestTenancy(t *testing.T) {
	d := db(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	// The same source and line for both: the user is part of the key.
	for _, u := range []string{"tenant-a", "tenant-b"} {
		if err := d.AddObservations(ctx, u, []Observation{
			{Date: day, Source: "upload:shared", Line: 1, ISIN: "INE009A01021", Side: "buy", Quantity: "1", Account: u},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.WaitApplied(ctx, "observations"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"tenant-a", "tenant-b"} {
		got, err := d.Observations(ctx, u, day, day.AddDate(0, 0, 1))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Account != u {
			t.Errorf("%s reads %+v", u, got)
		}
	}
}

func TestStatements(t *testing.T) {
	got := statements("-- a comment; with a semicolon\nCREATE x (a INT, -- the a\n b INT);\n\nALTER x;\n")
	want := []string{"CREATE x (a INT, \n b INT)", "ALTER x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("statements = %q, want %q", got, want)
	}
}

func TestTrimDecimal(t *testing.T) {
	for in, want := range map[string]string{"12.34500000": "12.345", "3.00000000": "3", "100": "100", "": ""} {
		if got := trimDecimal(in); got != want {
			t.Errorf("trimDecimal(%q) = %q, want %q", in, got, want)
		}
	}
}
