// Command worker runs the Roamind Hatchet worker.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	slackapi "github.com/slack-go/slack"

	"roamind.ai/agent"
	"roamind.ai/channels/store"
	"roamind.ai/memory"
	"roamind.ai/workflows/chatturn"
	"roamind.ai/workflows/goalchannels"
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

	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	sc := slackapi.New(token)
	gcDeps := goalchannels.Deps{Store: st, Slack: sc, Memory: mem}
	goals := goalchannels.New(client, gcDeps)
	var afterTurn func(context.Context, *store.User) error
	if mem != nil {
		afterTurn = goalchannels.Trigger(goals, gcDeps)
	}
	task := chatturn.New(client, chatturn.Deps{
		Store: st,
		Slack: sc,
		Agent: agent.Config{
			DataDir:      dataDir,
			ClaudeBinary: os.Getenv("ROAMIND_CLAUDE_BIN"),
			Model:        os.Getenv("ROAMIND_MODEL"),
			Memory:       mem,
		},
		AfterTurn: afterTurn,
	})

	slots, _ := strconv.Atoi(envOr("ROAMIND_WORKER_SLOTS", "4"))
	w, err := client.NewWorker("roamind-worker", hatchet.WithWorkflows(task, goals), hatchet.WithSlots(slots))
	if err != nil {
		return err
	}
	slog.Info("worker starting", "data_dir", dataDir, "slots", slots)
	return w.StartBlocking(ctx)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
