// Package memory gives each user a Basic Memory project, with its own bm
// config and database, and hands claude Basic Memory's own MCP server,
// pinned to that project with `bm mcp --project`. bm enforces the pinning itself: project arguments and
// search_all_projects are ignored, and project management is disabled.
package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ServerName is the key under mcpServers; claude names the tools
// mcp__memory__<tool>.
const ServerName = "memory"

// AllowedTools gives the model full read/write access to its project:
// every tool on the bm server.
var AllowedTools = []string{"mcp__" + ServerName}

// DisallowedTools hides bm's project and workspace management. --project
// already refuses them; hiding them keeps the model from trying.
var DisallowedTools = []string{
	"mcp__memory__list_memory_projects",
	"mcp__memory__create_memory_project",
	"mcp__memory__delete_project",
	"mcp__memory__list_workspaces",
}

// Config locates Basic Memory's data, kept apart from any personal
// ~/.basic-memory. Every project is self-contained, with its own bm config
// and SQLite database (index, full-text search and embeddings), so users
// share nothing but the read-only embedding model:
//
//	Dir/fastembed_cache/           embedding model, downloaded once
//	Dir/users/<project>/config/    config.json (only this project), memory.db
//	Dir/users/<project>/notes/     the markdown notes
type Config struct {
	Dir string
	// Binary is the bm executable; claude starts it, so it is resolved to an
	// absolute path by New.
	Binary string
	// Search configures bm's semantic search models; the zero value keeps
	// bm's CPU defaults.
	Search Search

	mu    sync.Mutex
	ready map[string]bool
}

// Search points bm's embeddings and reranking at OpenAI-compatible servers
// (vLLM on the local GPU, see deploy/bin/run-vllm.sh) through bm's litellm
// provider.
type Search struct {
	EmbedBase   string // e.g. http://127.0.0.1:8800/v1
	EmbedModel  string // litellm model, e.g. hosted_vllm/qwen3-embedding-0.6b
	EmbedDims   int
	QueryPrefix string // instruction prepended to search queries
	RerankBase  string // e.g. http://127.0.0.1:8801
	RerankModel string // litellm model, e.g. hosted_vllm/qwen3-reranker-0.6b
}

// DefaultSearch is Qwen3-Embedding-0.6B and Qwen3-Reranker-0.6B served by
// vLLM on this machine's GPU.
var DefaultSearch = Search{
	EmbedBase:   "http://127.0.0.1:8800/v1",
	EmbedModel:  "hosted_vllm/qwen3-embedding-0.6b",
	EmbedDims:   1024,
	QueryPrefix: "Instruct: Given a question about the user's notes on people, goals, preferences and scientific work, retrieve notes that answer it\nQuery: ",
	RerankBase:  "http://127.0.0.1:8801",
	RerankModel: "hosted_vllm/qwen3-reranker-0.6b",
}

// settings are the bm config.json keys Search manages.
func (s Search) settings() map[string]any {
	m := map[string]any{}
	if s.EmbedModel != "" {
		m["semantic_search_enabled"] = true
		m["semantic_embedding_provider"] = "litellm"
		m["semantic_embedding_model"] = s.EmbedModel
		m["semantic_embedding_api_base"] = s.EmbedBase
		m["semantic_embedding_api_key"] = "EMPTY" // local server, no auth
		m["semantic_embedding_dimensions"] = s.EmbedDims
		m["semantic_embedding_query_prefix"] = s.QueryPrefix
		m["semantic_embedding_batch_size"] = 32
	}
	if s.RerankModel != "" {
		m["reranker_enabled"] = true
		m["reranker_provider"] = "litellm"
		m["reranker_model"] = s.RerankModel
		m["reranker_api_base"] = s.RerankBase
		m["reranker_api_key"] = "EMPTY"
	}
	return m
}

// New returns a config for dir, finding bin ("" means "bm") on PATH.
func New(dir, bin string) (*Config, error) {
	if bin == "" {
		bin = "bm"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, fmt.Errorf("basic memory: %w", err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return &Config{Dir: dir, Binary: path, ready: map[string]bool{}}, nil
}

func (c *Config) configDir(project string) string {
	return filepath.Join(c.Dir, "users", project, "config")
}

func (c *Config) notesDir(project string) string {
	return filepath.Join(c.Dir, "users", project, "notes")
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ProjectBase is the project name for a user, before the index that keeps
// it unique: bm-u-<slugified name>.
func ProjectBase(userName string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(userName), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		s = "user"
	}
	return "bm-u-" + s
}

var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Ensure sets up project's directory (once per process): its bm config,
// listing only this project, and its schema notes.
func (c *Config) Ensure(project string) error {
	if !projectRe.MatchString(project) {
		return fmt.Errorf("invalid memory project %q", project)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready[project] {
		return nil
	}
	for _, d := range []string{c.configDir(project), c.notesDir(project), filepath.Join(c.Dir, "fastembed_cache")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	if err := c.writeBMConfig(project); err != nil {
		return err
	}
	if err := seedSchemas(c.notesDir(project)); err != nil {
		return err
	}
	c.ready[project] = true
	return nil
}

// writeBMConfig writes the project's bm config.json, creating it with only
// this project or, if it exists (bm fills in its defaults and may rewrite
// it), updating just the keys Roamind manages, so model changes reach
// existing users. Writing it directly, rather than `bm project add`, saves a
// slow CLI start and keeps bm's implicit "main" project out.
func (c *Config) writeBMConfig(project string) error {
	path := filepath.Join(c.configDir(project), "config.json")
	cfg := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	cfg["projects"] = map[string]any{
		project: map[string]any{"path": c.notesDir(project), "mode": "local"},
	}
	cfg["default_project"] = project
	cfg["project_root"] = c.notesDir(project)
	cfg["semantic_embedding_cache_dir"] = filepath.Join(c.Dir, "fastembed_cache")
	for k, v := range c.Search.settings() {
		cfg[k] = v
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.configDir(project), "config-*.json")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// MCPConfig is the --mcp-config JSON giving claude bm pinned to project.
func (c *Config) MCPConfig(project string) (string, error) {
	b, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			ServerName: map[string]any{
				"type":    "stdio",
				"command": c.Binary,
				"args":    []string{"mcp", "--project", project},
				// The project's own config, and with it its own database.
				"env": map[string]string{"BASIC_MEMORY_CONFIG_DIR": c.configDir(project)},
			},
		},
	})
	return string(b), err
}
