package safehttp

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

// Proxy is an HTTP forward proxy on loopback that connects only to public
// addresses, for a browser rendering third-party pages: whatever the page
// requests, through redirects, scripts or DNS, is dialled here and checked
// like New's requests.
type Proxy struct {
	// Addr is the proxy's host:port.
	Addr string
	srv  *http.Server
}

// NewProxy starts a proxy on a free loopback port.
func NewProxy() (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: PublicOnly}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	p := &Proxy{Addr: ln.Addr().String()}
	p.srv = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serve(w, r, dialer, tr) }),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go p.srv.Serve(ln)
	return p, nil
}

// Close stops the proxy and its connections.
func (p *Proxy) Close() error { return p.srv.Close() }

func serve(w http.ResponseWriter, r *http.Request, dialer *net.Dialer, tr *http.Transport) {
	if r.Method == http.MethodConnect {
		tunnel(w, r, dialer)
		return
	}
	if r.URL.Host == "" || r.URL.Scheme != "http" {
		http.Error(w, "not a proxy request", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Proxy-Authorization")
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// tunnel serves CONNECT, for HTTPS and WebSockets.
func tunnel(w http.ResponseWriter, r *http.Request, dialer *net.Dialer) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	up, err := dialer.DialContext(ctx, "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "no hijacking", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if _, err := down.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		up.Close()
		down.Close()
		return
	}
	go func() {
		// Bytes the client sent after its CONNECT come first.
		if n := buf.Reader.Buffered(); n > 0 {
			b, _ := buf.Reader.Peek(n)
			up.Write(b)
		}
		io.Copy(up, down)
		up.Close()
	}()
	io.Copy(down, up)
	down.Close()
}
