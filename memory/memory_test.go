package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureIsolatesProject(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Binary: "/usr/bin/bm", ready: map[string]bool{}}
	for _, p := range []string{"bm-u-sai-1", "bm-u-ravi-1"} {
		if err := c.Ensure(p); err != nil {
			t.Fatal(err)
		}
	}
	var cfg struct {
		Projects       map[string]struct{ Path string } `json:"projects"`
		DefaultProject string                           `json:"default_project"`
		Cache          string                           `json:"semantic_embedding_cache_dir"`
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, "users", "bm-u-sai-1", "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects["bm-u-sai-1"].Path != filepath.Join(c.Dir, "users", "bm-u-sai-1", "notes") ||
		cfg.DefaultProject != "bm-u-sai-1" || cfg.Cache != filepath.Join(c.Dir, "fastembed_cache") {
		t.Fatalf("config = %s", b)
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "users", "bm-u-ravi-1", "notes", "schemas", "Person.md")); err != nil {
		t.Fatal(err)
	}

	srv := c.MCPServer("bm-u-sai-1")
	env, args := srv["env"].(map[string]string), srv["args"].([]string)
	if env["BASIC_MEMORY_CONFIG_DIR"] != filepath.Join(c.Dir, "users", "bm-u-sai-1", "config") || args[2] != "bm-u-sai-1" {
		t.Fatalf("mcp server = %v", srv)
	}
}

func TestEnsureRejectsBadNames(t *testing.T) {
	c := &Config{Dir: t.TempDir(), ready: map[string]bool{}}
	for _, p := range []string{"", "../x", "a/b", "Upper"} {
		if err := c.Ensure(p); err == nil {
			t.Errorf("Ensure(%q) accepted", p)
		}
	}
}

func TestProjectBase(t *testing.T) {
	for in, want := range map[string]string{"Sai Nageswar": "bm-u-sai-nageswar", "  ": "bm-u-user", "Ravi.K!": "bm-u-ravi-k"} {
		if got := ProjectBase(in); got != want {
			t.Errorf("ProjectBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnsureWritesSearchSettingsAndKeepsBMKeys(t *testing.T) {
	c := &Config{Dir: t.TempDir(), Binary: "/usr/bin/bm", ready: map[string]bool{}, Search: DefaultSearch}
	cfgPath := filepath.Join(c.Dir, "users", "bm-u-sai-1", "config", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// An existing config, as bm rewrites it, with an old CPU model.
	old := `{"env":"dev","semantic_embedding_provider":"fastembed","semantic_embedding_model":"bge-small-en-v1.5","log_level":"INFO"}`
	if err := os.WriteFile(cfgPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.Ensure("bm-u-sai-1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfgPath)
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"semantic_embedding_provider":   "litellm",
		"semantic_embedding_model":      "hosted_vllm/qwen3-embedding-0.6b",
		"semantic_embedding_dimensions": float64(1024),
		"reranker_enabled":              true,
		"reranker_model":                "hosted_vllm/qwen3-reranker-0.6b",
		"log_level":                     "INFO", // bm's own keys survive
		"default_project":               "bm-u-sai-1",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Errorf("%s = %v, want %v", k, cfg[k], v)
		}
	}
}
