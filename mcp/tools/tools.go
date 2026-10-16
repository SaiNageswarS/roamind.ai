// Package tools holds the tools of Roamind's MCP server, one file per
// domain. claude starts the server per chat turn over stdio, pinned to the
// turn's user with --user, the way bm is pinned to the user's memory
// project; every tool reads and writes only that user's data.
//
//   - interests.go: the news interests the user follows from the catalog,
//     and new catalog interests.
package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/channels/feeds"
	"roamind.ai/memory/store"
)

// Deps are what the tools work with.
type Deps struct {
	Store *store.Store
	// Feeds checks the feeds of new interests.
	Feeds *feeds.Client
	// UserID is the user the server is pinned to.
	UserID string
}

// Register adds every tool to the server.
func Register(s *mcp.Server, d Deps) {
	interests(s, d)
}

func oneOf(field, v string, allowed ...string) error {
	return check(slices.Contains(allowed, v), fmt.Sprintf("%s must be one of %s", field, strings.Join(allowed, ", ")))
}

func check(ok bool, msg string) error {
	if ok {
		return nil
	}
	return fmt.Errorf("%s", msg)
}
