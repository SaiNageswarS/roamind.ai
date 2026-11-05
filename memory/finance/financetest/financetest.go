// Package financetest starts a QuestDB for tests, the way
// deploy/bin/run-questdb.sh runs it in production, from the
// bin/questdb-<version> that `task questdb-install` downloads.
package financetest

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

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

// Schemas is the directory of QuestDB's schemas, for finance.DB.Init.
func Schemas() (string, error) {
	r, err := root()
	return filepath.Join(r, "memory", "questdb-schemas"), err
}

// Start starts a QuestDB in a temporary directory, on free ports, and
// waits until it answers. addr is its HTTP port, for finance.Open; it is
// "" when bin/ has no QuestDB, and the tests should skip. stop stops it
// and removes its data.
func Start() (addr string, stop func(), err error) {
	r, err := root()
	if err != nil {
		return "", nil, err
	}
	homes, _ := filepath.Glob(filepath.Join(r, "bin", "questdb-*", "bin", "questdb.sh"))
	if len(homes) == 0 {
		return "", func() {}, nil
	}
	dir, err := os.MkdirTemp("", "questdb-test-")
	if err != nil {
		return "", nil, err
	}
	ports := make([]string, 2)
	for i := range ports {
		if ports[i], err = freePort(); err != nil {
			return "", nil, err
		}
	}
	// The tag lets it run beside another QuestDB.
	cmd := exec.Command(homes[0], "start", "-n", "-d", dir, "-t", fmt.Sprintf("test-%d", os.Getpid()))
	cmd.Env = append(os.Environ(),
		"QDB_HTTP_BIND_TO=127.0.0.1:"+ports[0],
		"QDB_HTTP_MIN_NET_BIND_TO=127.0.0.1:"+ports[1],
		"QDB_PG_ENABLED=false",
		"QDB_LINE_TCP_ENABLED=false",
		"QDB_LINE_UDP_ENABLED=false",
		"QDB_TELEMETRY_ENABLED=false",
	)
	// questdb.sh runs java as its child: stop kills the whole group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	stop = func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
		os.RemoveAll(dir)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get("http://127.0.0.1:" + ports[0] + "/exec?query=SELECT%201")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return "127.0.0.1:" + ports[0], stop, nil
			}
		}
		if time.Now().After(deadline) {
			stop()
			return "", nil, fmt.Errorf("QuestDB didn't start: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
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
