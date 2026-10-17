// Package safehttp is an HTTP client for URLs that come from the agent or
// third-party pages: it connects only to public addresses and reads
// bodies up to a cap.
package safehttp

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// New returns a client that gives up after timeout and connects only to
// public addresses. The worker and MCP server run outside the sandbox,
// next to Hatchet, the search models and the tailnet; the check is made on
// the address actually dialled, so it also covers redirects and DNS
// answers.
func New(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: PublicOnly}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil // a proxy would dial on our behalf
	tr.DialContext = dialer.DialContext
	return &http.Client{Timeout: timeout, Transport: tr}
}

// cgnat is the shared address space, which Tailscale uses.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// PublicOnly is a net.Dialer Control that refuses connections to loopback,
// private, shared (CGNAT), link-local, multicast and unspecified
// addresses.
func PublicOnly(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip) {
		return fmt.Errorf("%s is not a public address", ip)
	}
	return nil
}

// ReadAll reads r, failing when it holds more than max bytes.
func ReadAll(r io.Reader, max int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf("larger than %d MB", max>>20)
	}
	return body, nil
}
