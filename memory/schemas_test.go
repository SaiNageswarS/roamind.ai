package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSeedSchemas(t *testing.T) {
	dir := t.TempDir()
	if err := seedSchemas(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Person.md", "Place.md", "Preference.md", "Event.md", "Goal.md", "Progress.md", "Task.md"} {
		if _, err := os.Stat(filepath.Join(dir, "schemas", name)); err != nil {
			t.Fatal(err)
		}
	}
	// Edited schemas survive reseeding.
	p := filepath.Join(dir, "schemas", "Person.md")
	if err := os.WriteFile(p, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedSchemas(dir); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "edited" {
		t.Fatalf("Person.md = %q", b)
	}
}

// Schema notes are indexed like any other note: an observation line ("- ")
// or a wikilink in one would be recalled as a fact about every user.
func TestSchemasHaveNoFactsOrLinks(t *testing.T) {
	entries, err := schemas.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := schemas.ReadFile("schemas/" + e.Name())
		body := string(b)
		if i := strings.Index(body[3:], "---"); i >= 0 {
			body = body[i+6:] // skip frontmatter
		}
		for _, line := range strings.Split(body, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "- ") || strings.Contains(line, "[[") {
				t.Errorf("%s: body line would be indexed as a fact or link: %q", e.Name(), line)
			}
		}
	}
}

// A schema whose frontmatter is not valid YAML is silently ignored by bm.
func TestSchemaFrontmatterParses(t *testing.T) {
	entries, err := schemas.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := schemas.ReadFile("schemas/" + e.Name())
		parts := strings.SplitN(string(b), "---\n", 3)
		if len(parts) != 3 || parts[0] != "" {
			t.Errorf("%s: no frontmatter", e.Name())
			continue
		}
		var fm struct {
			Type   string         `yaml:"type"`
			Entity string         `yaml:"entity"`
			Schema map[string]any `yaml:"schema"`
		}
		if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
			t.Errorf("%s: %v", e.Name(), err)
			continue
		}
		if fm.Type != "schema" || fm.Entity == "" || len(fm.Schema) == 0 {
			t.Errorf("%s: frontmatter = %+v", e.Name(), fm)
		}
	}
}
