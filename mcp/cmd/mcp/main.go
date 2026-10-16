// Command mcp is Roamind's MCP server, serving the tools in package tools
// over stdio. claude starts it for each chat turn, pinned to the turn's
// user (see agent.Config.ToolsServer):
//
//	mcp --data DIR --user ID
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/channels/feeds"
	"roamind.ai/mcp/tools"
	"roamind.ai/memory/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("mcp server exited", "err", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	dataDir := fs.String("data", "./data", "data directory holding roamind.db")
	user := fs.String("user", "", "the user the tools serve")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return errors.New("--user is required")
	}
	st, err := store.Open(filepath.Join(*dataDir, "roamind.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
	tools.Register(s, tools.Deps{Store: st, Feeds: feeds.New(15 * time.Second), UserID: *user})
	return s.Run(ctx, &mcp.StdioTransport{})
}
