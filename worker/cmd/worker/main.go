// Command worker runs the Roamind Hatchet worker. `task init` prepares its
// store and memory projects first.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/web"
	"roamind.ai/connectors/feeds"
	"roamind.ai/connectors/jobs"
	"roamind.ai/memory/store"
	"roamind.ai/memory/store/notes"
	"roamind.ai/worker/client"
	"roamind.ai/worker/workflows"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dataDir, err := filepath.Abs(envOr("ROAMIND_DATA_DIR", "./data"))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "roamind.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Ready(ctx); err != nil {
		return fmt.Errorf("%w: run `task init`", err)
	}

	token := os.Getenv("SLACK_BOT_TOKEN")
	if token == "" {
		slog.Warn("SLACK_BOT_TOKEN is not set; Slack calls will fail")
	}

	// Long-term memory needs Basic Memory's bm CLI; without it turns run
	// without memory.
	// New users' projects are set up from the repo's memory folder.
	mem, err := notes.New(filepath.Join(dataDir, "memory"), envOr("ROAMIND_MEMORY_DIR", "./memory"), os.Getenv("ROAMIND_BM_BIN"))
	if err != nil {
		slog.Warn("memory disabled", "err", err)
		mem = nil
	}

	loc, err := location(os.Getenv("ROAMIND_TZ"))
	if err != nil {
		return err
	}
	mcpBin, err := mcpBinary(os.Getenv("ROAMIND_MCP_BIN"))
	if err != nil {
		return err
	}
	var toolsServer []string
	if mcpBin != "" {
		// claude runs it in the user's workspace, so the paths are absolute.
		toolsServer = []string{mcpBin, "--data", dataDir}
	} else {
		slog.Warn("bin/mcp not found (task mcp); chat turns run without Roamind's tools")
	}
	// The job assistant needs the web app's Firebase project.
	var webApp workflows.WebAPI
	if project := os.Getenv("FIREBASE_PROJECT_ID"); project != "" {
		wc, err := web.New(ctx, project, os.Getenv("FIREBASE_STORAGE_BUCKET"))
		if err != nil {
			return err
		}
		defer wc.Close()
		webApp = wc
	} else {
		slog.Warn("FIREBASE_PROJECT_ID is not set; no job assistant")
	}
	jobsPerRun, _ := strconv.Atoi(os.Getenv("ROAMIND_JOBS_PER_RUN"))
	// Domains the job search never reads, comma-separated.
	var blocked []string
	for _, d := range strings.Split(os.Getenv("ROAMIND_JOBS_BLOCKED_DOMAINS"), ",") {
		if d = strings.TrimSpace(d); d != "" {
			blocked = append(blocked, d)
		}
	}
	jobsClient := jobs.New(20 * time.Second)
	jobsClient.Blocked = blocked
	// Chrome renders posting pages that fill themselves in with JavaScript;
	// ROAMIND_CHROME_BIN=off reads them as sent.
	if path := chromeBin(); path != "" {
		chrome, err := jobs.NewChrome(path, 45*time.Second, 2)
		if err != nil {
			return err
		}
		defer chrome.Close()
		jobsClient.Render = chrome
		slog.Info("job pages rendered", "chrome", path)
	} else {
		slog.Warn("no Chrome; job pages that need JavaScript can't be read")
	}

	c, err := client.HatchetClient()
	if err != nil {
		return err
	}
	wfs, listen := workflows.Register(c, workflows.Deps{
		Store: st,
		Slack: slackapi.New(token),
		Agent: agent.Config{
			DataDir:        dataDir,
			ClaudeBinary:   os.Getenv("ROAMIND_CLAUDE_BIN"),
			Model:          os.Getenv("ROAMIND_MODEL"),
			Memory:         mem,
			ToolsServer:    toolsServer,
			BlockedDomains: blocked,
		},
		Memory:     mem,
		Feeds:      feeds.New(20 * time.Second),
		Web:        webApp,
		Jobs:       jobsClient,
		JobsPerRun: jobsPerRun,
		Location:   loc,
	})
	if mem != nil {
		slog.Info("daily exercises", "tz", loc.String(),
			"crons_utc", workflows.LocalCrons(loc, time.Now(), workflows.ExerciseHour))
		slog.Info("news shorts", "tz", loc.String(),
			"crons_utc", workflows.LocalCrons(loc, time.Now(), workflows.NewsHours...))
		slog.Info("job searches", "tz", loc.String(),
			"crons_utc", workflows.LocalCrons(loc, time.Now(), workflows.JobSearchHours...))
	}

	if listen != nil {
		// The job assistant starts its runs as users act in the web app.
		go func() {
			if err := listen(ctx); err != nil && ctx.Err() == nil {
				slog.Error("web app listener stopped", "err", err)
			}
		}()
	} else if webApp != nil {
		slog.Warn("memory is disabled; no job assistant")
	}

	slots, _ := strconv.Atoi(envOr("ROAMIND_WORKER_SLOTS", "4"))
	w, err := c.NewWorker("roamind-worker", hatchet.WithWorkflows(wfs...), hatchet.WithSlots(slots))
	if err != nil {
		return err
	}
	slog.Info("worker starting", "data_dir", dataDir, "slots", slots)
	return w.StartBlocking(ctx)
}

// mcpBinary is the absolute path of the mcp command (task build):
// bin, or by default mcp next to the worker's own binary; "" when there is
// none.
func mcpBinary(bin string) (string, error) {
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		bin = filepath.Join(filepath.Dir(exe), "mcp")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(bin); err != nil {
		return "", nil
	}
	return bin, nil
}

// location loads the zone tz, or by default the server's zone under its
// name (e.g. Europe/Berlin rather than Local), read from /etc/localtime.
func location(tz string) (*time.Location, error) {
	if tz != "" {
		return time.LoadLocation(tz)
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if _, name, ok := strings.Cut(target, "zoneinfo/"); ok {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc, nil
			}
		}
	}
	return time.Local, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// chromeBin is ROAMIND_CHROME_BIN, else the first Chrome on PATH, or "".
func chromeBin() string {
	if v := os.Getenv("ROAMIND_CHROME_BIN"); v != "" {
		if v == "off" {
			return ""
		}
		return v
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}
