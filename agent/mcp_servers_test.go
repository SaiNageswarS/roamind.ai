package agent

import (
	"path/filepath"
	"reflect"
	"testing"

	"roamind.ai/agent/protocol"
	"roamind.ai/memory/store/notes"
)

func TestMCPServers(t *testing.T) {
	mem, err := notes.New(t.TempDir(), "../memory", "true")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Memory: mem, ToolsServer: []string{"/opt/mcp", "--data", "/srv"}}
	in := protocol.TurnInput{UserID: "u1", MemoryProject: "bm-u-sai-1", ServerTools: []string{protocol.ServerInterests, protocol.ServerJobs}}
	if _, _, _, err := mcpServers(cfg, in, true, false); err == nil {
		t.Fatal("a project not set up got a server")
	}
	if err := mem.Setup(in.MemoryProject); err != nil {
		t.Fatal(err)
	}
	servers, allowed, disallowed, err := mcpServers(cfg, in, true, true)
	if err != nil {
		t.Fatal(err)
	}
	bm := servers[MemoryServerName].(map[string]any)
	env, args := bm["env"].(map[string]string), bm["args"].([]string)
	if env["BASIC_MEMORY_CONFIG_DIR"] != filepath.Join(mem.Dir, "users", "bm-u-sai-1", "config") ||
		!reflect.DeepEqual(args, []string{"mcp", "--project", "bm-u-sai-1"}) || !filepath.IsAbs(bm["command"].(string)) {
		t.Fatalf("memory server = %v", bm)
	}
	if args := servers[ToolsServerName].(map[string]any)["args"]; !reflect.DeepEqual(args, []string{"--data", "/srv", "--user", "u1", "--tools", "interests,jobs"}) {
		t.Fatalf("tools server args = %v", args)
	}
	if !reflect.DeepEqual(allowed, []string{"mcp__memory", "mcp__roamind"}) || len(disallowed) == 0 {
		t.Fatalf("allowed %v, disallowed %v", allowed, disallowed)
	}
	if servers, allowed, _, _ := mcpServers(cfg, in, false, false); len(servers) != 0 || allowed != nil {
		t.Fatalf("no servers wanted: %v %v", servers, allowed)
	}
}
