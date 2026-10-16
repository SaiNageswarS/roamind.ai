package notes

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// memoryDir is the repo's memory folder.
const memoryDir = "../.."

func TestSetupIsolatesProject(t *testing.T) {
	n := Notes{Dir: t.TempDir(), Memory: memoryDir}
	for _, p := range []string{"bm-u-sai-1", "bm-u-ravi-1"} {
		if err := n.Setup(p); err != nil {
			t.Fatal(err)
		}
		if !n.Ready(p) {
			t.Fatalf("%s not ready", p)
		}
	}
	var cfg struct {
		Projects       map[string]struct{ Path string } `json:"projects"`
		DefaultProject string                           `json:"default_project"`
		Cache          string                           `json:"semantic_embedding_cache_dir"`
	}
	b, err := os.ReadFile(filepath.Join(n.Dir, "users", "bm-u-sai-1", "config", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects["bm-u-sai-1"].Path != filepath.Join(n.Dir, "users", "bm-u-sai-1", "notes") ||
		cfg.DefaultProject != "bm-u-sai-1" || cfg.Cache != filepath.Join(n.Dir, "fastembed_cache") {
		t.Fatalf("config = %s", b)
	}
	if n.Ready("bm-u-new-1") {
		t.Fatal("a project never set up is ready")
	}
	// A corrupt config needs setting up again.
	cfgPath := filepath.Join(n.ConfigDir("bm-u-sai-1"), "config.json")
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n.Ready("bm-u-sai-1") {
		t.Fatal("a project with a corrupt config is ready")
	}
}

func TestSetupRejectsBadNames(t *testing.T) {
	n := Notes{Dir: t.TempDir(), Memory: memoryDir}
	for _, p := range []string{"", "../x", "a/b", "Upper"} {
		if err := n.Setup(p); err == nil {
			t.Errorf("Setup(%q) accepted", p)
		}
	}
}

func TestSetupWritesSettingsAndKeepsBMKeys(t *testing.T) {
	n := Notes{Dir: t.TempDir(), Memory: memoryDir}
	cfgPath := filepath.Join(n.ConfigDir("bm-u-sai-1"), "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// An existing config, as bm rewrites it, with an old CPU model.
	old := `{"env":"dev","semantic_embedding_provider":"fastembed","semantic_embedding_model":"bge-small-en-v1.5","log_level":"INFO"}`
	if err := os.WriteFile(cfgPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.Setup("bm-u-sai-1"); err != nil {
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

func TestSetupCopiesSchemas(t *testing.T) {
	n := Notes{Dir: t.TempDir(), Memory: memoryDir}
	if err := n.Setup("bm-u-sai-1"); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(n.NotesDir("bm-u-sai-1"), "schemas")
	for _, name := range []string{"Person.md", "Place.md", "Preference.md", "Event.md", "Goal.md", "Progress.md", "Task.md", "Exercise.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	// Edited schemas survive a second setup.
	p := filepath.Join(dir, "Person.md")
	if err := os.WriteFile(p, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := n.Setup("bm-u-sai-1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "edited" {
		t.Fatalf("Person.md = %q", b)
	}
}

// Setup writes a project's config.json as `task init` does with jq, so a
// new project and an existing one end up alike.
func TestSetupMatchesTaskInit(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not installed")
	}
	b, err := os.ReadFile(filepath.Join(memoryDir, "..", "Taskfile.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var taskfile struct {
		Vars struct {
			BMConfig string `yaml:"BM_CONFIG"`
		} `yaml:"vars"`
	}
	if err := yaml.Unmarshal(b, &taskfile); err != nil || taskfile.Vars.BMConfig == "" {
		t.Fatalf("BM_CONFIG in Taskfile.yml: %q %v", taskfile.Vars.BMConfig, err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := Notes{Dir: dir, Memory: memoryDir}
	const project = "bm-u-sai-1"
	cfgPath := filepath.Join(n.ConfigDir(project), "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	// As bm leaves it: its own keys, an old model, stale paths.
	old := `{"env":"dev","log_level":"INFO","semantic_embedding_model":"bge-small-en-v1.5",` +
		`"cloud":{"mode":"off","host":"x"},"projects":{"bm-u-sai-1":{"path":"/moved","mode":"local"}},"project_root":"/moved"}`
	if err := os.WriteFile(cfgPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("jq", "-s", "--arg", "p", project, "--arg", "notes", n.NotesDir(project),
		"--arg", "cache", filepath.Join(dir, "fastembed_cache"), taskfile.Vars.BMConfig,
		cfgPath, filepath.Join(memoryDir, "basic-memory-config.json"))
	byInit, err := cmd.Output()
	if err != nil {
		t.Fatalf("jq: %v", err)
	}
	if err := n.Setup(project); err != nil {
		t.Fatal(err)
	}
	bySetup, _ := os.ReadFile(cfgPath)
	var a, s map[string]any
	if err := json.Unmarshal(byInit, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bySetup, &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, s) {
		t.Fatalf("task init wrote\n%s\nSetup wrote\n%s", byInit, bySetup)
	}
	if s["project_root"] != n.NotesDir(project) || s["log_level"] != "INFO" {
		t.Fatalf("config = %s", bySetup)
	}
}
