package agent

import (
	"encoding/json"
	"strings"
)

// workspaceTools are the built-in claude tools each turn gets: the web, and
// files and commands in the user's workspace (the turn's working directory).
var workspaceTools = []string{
	"Bash", "Read", "Write", "Edit", "Glob", "Grep", "WebSearch", "WebFetch", "TodoWrite",
}

// allowedWorkspaceTools are the permission rules for workspaceTools in the
// workspace dir (absolute). In dontAsk mode anything not allowed is denied,
// so file edits (Edit rules cover Write and Edit) are allowed only inside
// the workspace; a bare "Write" or "Edit" rule would allow any path.
func allowedWorkspaceTools(dir string) []string {
	var rules []string
	for _, t := range workspaceTools {
		switch t {
		case "Write", "Edit":
		default:
			rules = append(rules, t)
		}
	}
	// "//" starts an absolute path in permission rules.
	return append(rules, "Edit(/"+strings.TrimSuffix(dir, "/")+"/**)")
}

// workspaceSettings confines those tools to the user's workspace:
//   - the file tools refuse reads outside the working directory, and edits
//     are allowed only inside it (allowedWorkspaceTools);
//   - Bash runs in Claude Code's bubblewrap sandbox: writes only to the
//     workspace and $TMPDIR, no reads of the home directory (so no other
//     user's workspace or memory, data/, .env or ~/.claude), never
//     unsandboxed, and the turn fails rather than run without a sandbox;
//   - the network is open for WebFetch and sandboxed commands
//     (WebFetch(domain:*)), but the sandbox proxy still refuses local
//     addresses such as Hatchet's and Basic Memory's ports.
var workspaceSettings = mustJSON(map[string]any{
	"permissions": map[string]any{
		"blockReadsOutsideWorkingDirectories": true,
		"allow":                               []string{"WebFetch(domain:*)"},
	},
	"sandbox": map[string]any{
		"enabled":                  true,
		"failIfUnavailable":        true,
		"allowUnsandboxedCommands": false,
		"autoAllowBashIfSandboxed": true,
	},
})

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
