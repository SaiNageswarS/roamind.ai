// Package tools holds the tools of Roamind's MCP server, one file per
// domain. claude starts the server per chat turn over stdio, pinned to the
// turn's user with --user, the way bm is pinned to the user's memory
// project; every tool reads and writes only that user's data. Its tools
// come in tool sets, and it serves those the turn's task gets, by name
// with --tools:
//
//   - interests (interests.go): the news interests the user follows from
//     the catalog, and new catalog interests.
//   - jobs (jobs.go): asking the job assistant to draft applications for
//     job links.
//   - items (items.go): the state of the user's tasks and events, and when
//     to remind them of each.
package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"roamind.ai/connectors/feeds"
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

// Tool sets, by their names for Register and --tools. The agent names them
// alike (protocol.ServerInterests and the rest).
const (
	Interests = "interests"
	Jobs      = "jobs"
	Items     = "items"
)

// toolSets add each tool set's tools to a server.
var toolSets = map[string]func(*mcp.Server, Deps){
	Interests: interests,
	Jobs:      jobs,
	Items:     items,
}

// Register adds the named tool sets' tools to the server.
func Register(s *mcp.Server, d Deps, sets []string) error {
	for _, name := range sets {
		add, ok := toolSets[name]
		if !ok {
			return fmt.Errorf("unknown tool set %q", name)
		}
		add(s, d)
	}
	return nil
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
