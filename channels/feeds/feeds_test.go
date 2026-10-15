package feeds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const rss = `<?xml version="1.0"?><rss version="2.0"><channel><title>Good &amp; Science</title>
<item><title>Older</title><link>https://a.org/old</link><pubDate>Mon, 12 Oct 2026 08:00:00 GMT</pubDate><description>&lt;p&gt;An &lt;b&gt;older&lt;/b&gt;   story.&lt;/p&gt;</description></item>
<item><title>Newer</title><link>https://a.org/new</link><pubDate>Tue, 13 Oct 2026 08:00:00 GMT</pubDate><description>` + "%s" + `</description></item>
<item><title></title><link>https://a.org/untitled</link></item>
</channel></rss>`

const atom = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>Blog</title>
<entry><title>Agents</title><link href="https://b.org/agents"/><updated>2026-10-13T09:00:00Z</updated><summary>On agents.</summary></entry>
</feed>`

func TestFetch(t *testing.T) {
	long := strings.Repeat("word ", 200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rss":
			w.Write([]byte(strings.Replace(rss, "%s", long, 1)))
		case "/atom":
			w.Write([]byte(atom))
		default:
			w.Write([]byte("<html>not a feed</html>"))
		}
	}))
	defer srv.Close()
	// The test server is on loopback, which New refuses.
	c := &Client{HTTP: srv.Client()}
	ctx := context.Background()

	es, err := c.Fetch(ctx, srv.URL+"/rss")
	if err != nil || len(es) != 2 {
		t.Fatalf("rss: %+v %v", es, err)
	}
	if es[0].Title != "Newer" || es[1].Content != "An older story." || es[1].Feed != "Good & Science" {
		t.Errorf("rss entries = %+v", es)
	}
	if len(es[0].Content) > MaxContent+len("…") || !strings.HasSuffix(es[0].Content, "word…") {
		t.Errorf("long content = %q", es[0].Content)
	}
	if es, err := c.Fetch(ctx, srv.URL+"/atom"); err != nil || len(es) != 1 || es[0].URL != "https://b.org/agents" || es[0].Content != "On agents." {
		t.Fatalf("atom: %+v %v", es, err)
	}
	if _, err := c.Fetch(ctx, srv.URL+"/page"); err == nil {
		t.Fatal("an HTML page parsed as a feed")
	}
}

func TestPublicOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(atom))
	}))
	defer srv.Close()
	c := New(5 * time.Second)
	if _, err := c.Fetch(context.Background(), srv.URL+"/atom"); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("loopback fetched: %v", err)
	}
	// The check is on the address dialled, so it covers redirects too.
	for addr, public := range map[string]bool{
		"127.0.0.1:80": false, "[::1]:443": false, "10.1.2.3:80": false, "172.16.0.9:80": false,
		"192.168.1.5:80": false, "100.101.102.103:443": false, "169.254.169.254:80": false,
		"0.0.0.0:80": false, "[fd7a:115c:a1e0::1]:443": false, "[::ffff:127.0.0.1]:80": false,
		"93.184.216.34:443": true, "[2606:4700::1111]:443": true,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != public {
			t.Errorf("%s: %v, want public %v", addr, err, public)
		}
	}
}

func TestMaxBody(t *testing.T) {
	big := strings.Repeat("x", MaxBody+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>` + big + `</title></channel></rss>`))
	}))
	defer srv.Close()
	if _, err := (&Client{HTTP: srv.Client()}).Fetch(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized feed: %v", err)
	}
}
