// Command mcp is Roamind's MCP server, serving the tools in package tools
// over stdio. claude starts it for each turn that gets its tools, pinned
// to the turn's user and serving the tool sets the turn's task gets (see
// agent.Config.ToolsServer):
//
//	mcp --data DIR --user ID --tools interests,jobs,items
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/connectors/feeds"
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
	sets := fs.String("tools", "", "the tool sets to serve, comma-separated: interests, jobs, items")
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
	if err := tools.Register(s, tools.Deps{Store: st, Feeds: feeds.New(15 * time.Second), UserID: *user}, strings.FieldsFunc(*sets, func(r rune) bool { return r == ',' })); err != nil {
		return err
	}
	return s.Run(ctx, &mcp.StdioTransport{})
}
