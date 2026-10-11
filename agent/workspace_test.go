package agent

import (
	"slices"
	"testing"
)

func TestAllowedWorkspaceToolsScopesEdits(t *testing.T) {
	rules := allowedWorkspaceTools("/data/users/u1/workspace")
	if slices.Contains(rules, "Write") || slices.Contains(rules, "Edit") {
		t.Fatalf("unscoped edit rule in %v", rules)
	}
	if !slices.Contains(rules, "Edit(//data/users/u1/workspace/**)") {
		t.Fatalf("no workspace edit rule in %v", rules)
	}
	for _, want := range []string{"Bash", "Read", "WebSearch", "WebFetch"} {
		if !slices.Contains(rules, want) {
			t.Errorf("%s missing from %v", want, rules)
		}
	}
}
