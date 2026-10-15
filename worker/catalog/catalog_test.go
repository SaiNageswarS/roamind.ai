package catalog

import (
	"net/url"
	"strings"
	"testing"
)

func TestInterests(t *testing.T) {
	ins, err := Interests()
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	seen := map[string]bool{}
	for _, in := range ins {
		name := strings.ToLower(in.Name)
		if in.Name == "" || seen[name] {
			t.Errorf("missing or duplicate interest %q", in.Name)
		}
		seen[name] = true
		if in.Default {
			defaults++
		}
		if len(in.Sources) == 0 || !strings.Contains(in.Rubric, "0-10") || !strings.Contains(in.Rubric, "include (true/false)") {
			t.Errorf("%s: %d sources, rubric %q", in.Name, len(in.Sources), in.Rubric)
		}
		for _, s := range in.Sources {
			if u, err := url.Parse(s); err != nil || u.Scheme != "https" {
				t.Errorf("%s: source %q", in.Name, s)
			}
		}
	}
	if defaults == 0 {
		t.Error("no default interests")
	}
}
