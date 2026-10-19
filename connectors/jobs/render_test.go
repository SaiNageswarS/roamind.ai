package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// chrome is a renderer on this machine's Chrome, or skips the test.
func chrome(t *testing.T) *Chrome {
	t.Helper()
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(name); err == nil {
			c, err := NewChrome(path, 30*time.Second, 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
			return c
		}
	}
	t.Skip("no Chrome")
	return nil
}

// Scripts run: the text they add is in the HTML.
func TestChromeRunsScripts(t *testing.T) {
	c := chrome(t)
	html, err := c.Render(context.Background(),
		`data:text/html,<title>Job</title><div id="r"></div><script>setTimeout(function(){document.getElementById("r").textContent="Filled in by script"},300)</script>`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Text(html), "Filled in by script") {
		t.Errorf("rendered %q", html)
	}
}

// A page can't reach addresses next to the worker: the browser's proxy
// refuses loopback, so a local server is never asked.
func TestChromePublicOnly(t *testing.T) {
	c := chrome(t)
	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.Write([]byte("<p>internal</p>"))
	}))
	defer srv.Close()
	html, _ := c.Render(context.Background(), srv.URL)
	if asked || strings.Contains(html, "internal") {
		t.Errorf("loopback reached: %q", html)
	}
	// Nor through a page's script.
	html, _ = c.Render(context.Background(),
		`data:text/html,<div id="r">waiting</div><script>fetch("`+srv.URL+`").then(r=>r.text()).then(t=>{document.getElementById("r").textContent=t}).catch(()=>{document.getElementById("r").textContent="refused"})</script>`)
	if asked || strings.Contains(html, "internal") {
		t.Errorf("loopback reached by a script: %q", html)
	}
}
