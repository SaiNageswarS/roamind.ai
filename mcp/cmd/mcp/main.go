// Command mcp is Roamind's MCP server, serving the tools in package tools
// over stdio. claude starts it for each turn that gets its tools, pinned
// to the turn's user and serving the tool sets the turn's task gets (see
// agent.Config.ToolsServer):
//
//	mcp --data DIR [--postgres URI] --user ID --tools interests,jobs,items,finance
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
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
	dataDir := fs.String("data", "./data", "data directory holding the users' workspaces")
	postgres := fs.String("postgres", "", "PostgreSQL URI of the store (default: POSTGRES_URI, else "+store.DefaultURI+")")
	user := fs.String("user", "", "the user the tools serve")
	sets := fs.String("tools", "", "the tool sets to serve, comma-separated: interests, jobs, items, finance")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *user == "" {
		return errors.New("--user is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	uri := *postgres
	if uri == "" {
		uri = os.Getenv("POSTGRES_URI")
	}
	if uri == "" {
		uri = store.DefaultURI
	}
	st, err := store.Open(ctx, uri)
	if err != nil {
		return err
	}
	defer st.Close()
	deps := tools.Deps{Store: st, Feeds: feeds.New(15 * time.Second), UserID: *user, DataDir: *dataDir}
	s := mcp.NewServer(&mcp.Implementation{Name: "roamind", Version: "v1"}, nil)
	if err := tools.Register(s, deps, strings.FieldsFunc(*sets, func(r rune) bool { return r == ',' })); err != nil {
		return err
	}
	return s.Run(ctx, &mcp.StdioTransport{})
}
