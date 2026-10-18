package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var errForbiddenAddr = errors.New("destination address is not allowed")

// blockedPrefixes are never reachable from plugins (SSRF protection): loopback,
// private networks, link-local (cloud metadata), CGNAT, multicast, etc.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "::ffff:0:0/96", "64:ff9b::/96", "100::/64", "2001:db8::/32",
		"fc00::/7", "fe80::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func addrAllowed(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// safeDialer resolves the host itself and only connects to public addresses,
// which also defeats DNS rebinding (the checked IP is the one dialled).
func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	var lastErr error = errForbiddenAddr
	for _, ip := range ips {
		if !addrAllowed(ip) {
			continue
		}
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("%s: %w", host, lastErr)
}

type grantKey struct{}

// newHTTPClient returns the client used by plugin http_fetch calls. Redirects
// are re-checked against the calling plugin's permissions.
func newHTTPClient() *http.Client {
	tr := &http.Transport{
		DialContext:            safeDialContext,
		Proxy:                  nil, // never route plugin traffic through environment proxies
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           20,
		IdleConnTimeout:        60 * time.Second,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  8 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many redirects")
			}
			granted, _ := req.Context().Value(grantKey{}).([]string)
			if req.URL.Scheme != "https" || !hostAllowed(granted, req.URL.Hostname()) {
				return errors.New("redirect to a host the plugin may not access")
			}
			return nil
		},
	}
}
