package agent

import (
	"encoding/json"
	"reflect"
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

func TestWebSettings(t *testing.T) {
	var s struct {
		Permissions struct{ Allow, Deny []string }
	}
	if err := json.Unmarshal([]byte(webSettings([]string{"jobs.example.org", " "})), &s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Permissions.Allow, []string{"WebFetch(domain:*)"}) ||
		!reflect.DeepEqual(s.Permissions.Deny, []string{"WebFetch(domain:jobs.example.org)", "WebFetch(domain:*.jobs.example.org)"}) {
		t.Fatalf("settings = %+v", s)
	}
}
