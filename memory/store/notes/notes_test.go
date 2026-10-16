package notes

import "testing"

func TestProjectBase(t *testing.T) {
	for in, want := range map[string]string{"Sai Nageswar": "bm-u-sai-nageswar", "  ": "bm-u-user", "Ravi.K!": "bm-u-ravi-k"} {
		if got := ProjectBase(in); got != want {
			t.Errorf("ProjectBase(%q) = %q, want %q", in, got, want)
		}
	}
}
