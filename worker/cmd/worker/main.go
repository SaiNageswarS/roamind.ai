// Command worker runs the Roamind Hatchet worker.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/store"
	"roamind.ai/memory"
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

	dataDir := envOr("ROAMIND_DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	dataDir, _ = filepath.Abs(dataDir)
	st, err := store.Open(filepath.Join(dataDir, "roamind.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	token := os.Getenv("SLACK_BOT_TOKEN")
	if token == "" {
		slog.Warn("SLACK_BOT_TOKEN is not set; Slack calls will fail")
	}

	// Long-term memory needs Basic Memory's bm CLI; without it turns run
	// without memory.
	mem, err := memory.New(filepath.Join(dataDir, "memory"), os.Getenv("ROAMIND_BM_BIN"))
	if err != nil {
		slog.Warn("memory disabled", "err", err)
		mem = nil
	} else {
		mem.Search = memory.DefaultSearch
	}

	loc, err := location(os.Getenv("ROAMIND_TZ"))
	if err != nil {
		return err
	}
	c, err := client.HatchetClient()
	if err != nil {
		return err
	}
	wfs := workflows.Register(c, workflows.Deps{
		Store: st,
		Slack: slackapi.New(token),
		Agent: agent.Config{
			DataDir:      dataDir,
			ClaudeBinary: os.Getenv("ROAMIND_CLAUDE_BIN"),
			Model:        os.Getenv("ROAMIND_MODEL"),
			Memory:       mem,
		},
		Memory:   mem,
		Location: loc,
	})
	if mem != nil {
		slog.Info("daily exercises", "tz", loc.String(),
			"crons_utc", workflows.ExerciseCrons(loc, workflows.ExerciseHour))
	}

	slots, _ := strconv.Atoi(envOr("ROAMIND_WORKER_SLOTS", "4"))
	w, err := c.NewWorker("roamind-worker", hatchet.WithWorkflows(wfs...), hatchet.WithSlots(slots))
	if err != nil {
		return err
	}
	slog.Info("worker starting", "data_dir", dataDir, "slots", slots)
	return w.StartBlocking(ctx)
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
