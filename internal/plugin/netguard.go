package plugin

import (
	"errors"
	"net/http"
	"net/netip"
	"time"

	"sap13ns/internal/netguard"
)

// addrAllowed reports whether plugins may reach the address (SSRF
// protection): public unicast addresses only.
func addrAllowed(a netip.Addr) bool { return netguard.Public(a) }

// safeDialContext resolves the host itself and only connects to public
// addresses, which also defeats DNS rebinding (the checked IP is the one
// dialled).
var safeDialContext = netguard.Dialer(addrAllowed, nil, 5*time.Second)

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
