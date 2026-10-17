package safehttp

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublicOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if _, err := New(5 * time.Second).Do(req); err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("loopback fetched: %v", err)
	}
	// The check is on the address dialled, so it covers redirects too.
	for addr, public := range map[string]bool{
		"127.0.0.1:80": false, "[::1]:443": false, "10.1.2.3:80": false, "172.16.0.9:80": false,
		"192.168.1.5:80": false, "100.101.102.103:443": false, "169.254.169.254:80": false,
		"0.0.0.0:80": false, "[fd7a:115c:a1e0::1]:443": false, "[::ffff:127.0.0.1]:80": false,
		"93.184.216.34:443": true, "[2606:4700::1111]:443": true,
	} {
		if err := PublicOnly("tcp", addr, nil); (err == nil) != public {
			t.Errorf("%s: %v, want public %v", addr, err, public)
		}
	}
}

func TestReadAll(t *testing.T) {
	if b, err := ReadAll(bytes.NewReader([]byte("abc")), 3); err != nil || string(b) != "abc" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err := ReadAll(bytes.NewReader([]byte("abcd")), 3); err == nil {
		t.Fatal("oversized body read")
	}
}
