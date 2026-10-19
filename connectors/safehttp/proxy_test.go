package safehttp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The proxy refuses to reach loopback, plain or tunnelled: a page in the
// browser can't reach the services next to the worker.
func TestProxyPublicOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tls.Close()
	p, err := NewProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	proxy, _ := url.Parse("http://" + p.Addr)
	tr := tls.Client().Transport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(proxy)
	c := &http.Client{Timeout: 5 * time.Second, Transport: tr}

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("http through the proxy: %s", resp.Status)
	}
	if resp, err := c.Get(tls.URL); err == nil {
		resp.Body.Close()
		t.Errorf("https through the proxy: %s", resp.Status)
	}
}
