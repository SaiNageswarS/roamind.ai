package agent

import (
	"fmt"
	"slices"
	"strings"

	"roamind.ai/agent/protocol"
)

// The MCP servers claude gets for a turn, by their key under mcpServers;
// claude names their tools mcp__<key>__<tool>.
const (
	// MemoryServerName is Basic Memory's own server, `bm mcp --project`,
	// pinned to the user's project. bm enforces the pinning itself: project
	// arguments and search_all_projects are ignored, and project management
	// is disabled.
	MemoryServerName = "memory"
	// ToolsServerName is Roamind's MCP server (bin/mcp), whose tools keep
	// the user's data, such as their news interests.
	ToolsServerName = "roamind"
)

// memoryDisallowedTools hides bm's project and workspace management.
// --project already refuses them; hiding them keeps the model from trying.
// Every other bm tool is allowed: the model has full read/write access to
// its project.
var memoryDisallowedTools = []string{
	"mcp__" + MemoryServerName + "__list_memory_projects",
	"mcp__" + MemoryServerName + "__create_memory_project",
	"mcp__" + MemoryServerName + "__delete_project",
	"mcp__" + MemoryServerName + "__list_workspaces",
}

// RecallTools are the Basic Memory tools a ToolsRecall turn may call: it
// reads notes, and can't change them.
var RecallTools = []string{"read_note", "build_context", "search_notes"}

// mcpServers returns the mcpServers entries for a turn, with the tools to
// allow and disallow on them: Basic Memory with memory (only RecallTools
// for ToolsRecall turns), Roamind's server, serving the turn's tool sets,
// with tools.
func mcpServers(cfg Config, in protocol.TurnInput, memory, tools bool) (servers map[string]any, allowed, disallowed []string, err error) {
	servers = map[string]any{}
	if memory {
		if !cfg.Memory.Ready(in.MemoryProject) {
			return nil, nil, nil, fmt.Errorf("memory project %s is not set up (user-setup workflow)", in.MemoryProject)
		}
		servers[MemoryServerName] = map[string]any{
			"type":    "stdio",
			"command": cfg.Memory.Binary,
			"args":    []string{"mcp", "--project", in.MemoryProject},
			// The project's own config, and with it its own database.
			"env": map[string]string{"BASIC_MEMORY_CONFIG_DIR": cfg.Memory.ConfigDir(in.MemoryProject)},
		}
		if in.Tools == protocol.ToolsRecall {
			// In dontAsk mode, bm's other tools are refused when called.
			for _, t := range RecallTools {
				allowed = append(allowed, "mcp__"+MemoryServerName+"__"+t)
			}
		} else {
			allowed = append(allowed, "mcp__"+MemoryServerName)
		}
		disallowed = memoryDisallowedTools
	}
	if tools {
		servers[ToolsServerName] = map[string]any{
			"type":    "stdio",
			"command": cfg.ToolsServer[0],
			// A new slice: cfg is shared by concurrent turns.
			"args": slices.Concat(cfg.ToolsServer[1:], []string{"--user", in.UserID, "--tools", strings.Join(in.ServerTools, ",")}),
		}
		allowed = append(allowed, "mcp__"+ToolsServerName)
	}
	return servers, allowed, disallowed, nil
}
