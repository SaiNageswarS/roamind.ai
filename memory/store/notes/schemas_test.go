package notes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// schemaDir holds the schema notes.
const schemaDir = memoryDir + "/basic-memory-schemas"

// Schema notes are indexed like any other note: an observation line ("- ")
// or a wikilink in one would be recalled as a fact about every user.
func TestSchemasHaveNoFactsOrLinks(t *testing.T) {
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(schemaDir, e.Name()))
		body := string(b)
		if i := strings.Index(body[3:], "---"); i >= 0 {
			body = body[i+6:] // skip frontmatter
		}
		// Example values are indexed too: a search on their words returns the
		// schema as if it held facts about the user.
		if strings.Contains(string(b), "e.g.") {
			t.Errorf("%s: has an example; describe the format instead", e.Name())
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
	entries, err := os.ReadDir(schemaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := os.ReadFile(filepath.Join(schemaDir, e.Name()))
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
