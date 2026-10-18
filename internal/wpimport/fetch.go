package wpimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	ErrForbiddenAddress = errors.New("wpimport: destination address is not allowed")
	ErrForbiddenHost    = errors.New("wpimport: host is not allowed")
	ErrForbiddenScheme  = errors.New("wpimport: only http and https URLs can be fetched")
	ErrMediaTooLarge    = errors.New("wpimport: media file too large")
)

// Fetcher downloads media files.
type Fetcher interface {
	// Fetch returns the body of the resource at rawURL; the caller closes it.
	Fetch(ctx context.Context, rawURL string) (io.ReadCloser, error)
}

const (
	defaultFetchTimeout = 60 * time.Second
	defaultMaxMedia     = 32 << 20
	maxRedirects        = 3
)

// HTTPFetcher is the default Fetcher. It only fetches http(s) URLs on the
// allowed hosts, resolves names itself and connects only to public addresses
// (so neither a hostile export nor a redirect nor DNS rebinding can reach
// localhost, the LAN or a cloud metadata endpoint), follows at most three
// redirects, each re-checked, and bounds time and size.
type HTTPFetcher struct {
	// AllowedHosts lists the host names that may be fetched; "www." is
	// ignored when comparing. Empty refuses everything.
	AllowedHosts []string
	Timeout      time.Duration // whole request including the body; default 60s
	MaxBytes     int64         // per response; default 32 MiB
	// AllowAddr decides whether a resolved IP may be dialled. Nil means
	// PublicAddr; tests inject a permissive guard to reach httptest servers.
	AllowAddr func(netip.Addr) bool
	// Resolver defaults to net.DefaultResolver.
	Resolver *net.Resolver

	once   sync.Once
	client *http.Client
}

// NewHTTPFetcher returns a fetcher limited to the given hosts (URLs are
// accepted too: "https://example.com/" → "example.com").
func NewHTTPFetcher(allowedHosts ...string) *HTTPFetcher {
	return &HTTPFetcher{AllowedHosts: allowedHosts}
}

// blockedPrefixes are never reachable (same list as the plugin network
// guard): loopback, private networks, link-local (cloud metadata), CGNAT,
// documentation and benchmark ranges, multicast, reserved.
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

// PublicAddr reports whether a is a public unicast address.
func PublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func normHost(h string) string {
	h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
	if strings.Contains(h, "/") {
		if u, err := url.Parse(h); err == nil && u.Host != "" {
			h = strings.ToLower(u.Hostname())
		}
	} else if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimPrefix(strings.Trim(h, "[]"), "www.")
}

func (f *HTTPFetcher) hostAllowed(host string) bool {
	host = normHost(host)
	for _, h := range f.AllowedHosts {
		if h = normHost(h); h != "" && h == host {
			return true
		}
	}
	return false
}

func (f *HTTPFetcher) check(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrForbiddenScheme
	}
	if u.User != nil || !f.hostAllowed(u.Hostname()) {
		return fmt.Errorf("%w: %s", ErrForbiddenHost, u.Hostname())
	}
	return nil
}

func (f *HTTPFetcher) maxBytes() int64 {
	if f.MaxBytes > 0 {
		return f.MaxBytes
	}
	return defaultMaxMedia
}

// dialContext resolves the host itself and dials only allowed addresses; the
// checked IP is the one connected to, which defeats DNS rebinding.
func (f *HTTPFetcher) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	resolver := f.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	allow := f.AllowAddr
	if allow == nil {
		allow = PublicAddr
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	var lastErr error = ErrForbiddenAddress
	for _, ip := range ips {
		if !allow(ip) {
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

func (f *HTTPFetcher) httpClient() *http.Client {
	f.once.Do(func() {
		timeout := f.Timeout
		if timeout <= 0 {
			timeout = defaultFetchTimeout
		}
		tr := &http.Transport{
			DialContext:            f.dialContext,
			Proxy:                  nil, // a proxy would dial on our behalf, bypassing the guard
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           10,
			IdleConnTimeout:        30 * time.Second,
			TLSHandshakeTimeout:    10 * time.Second,
			ResponseHeaderTimeout:  20 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		}
		f.client = &http.Client{
			Transport: tr,
			Timeout:   timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) > maxRedirects {
					return fmt.Errorf("wpimport: stopped after %d redirects", maxRedirects)
				}
				return f.check(req.URL)
			},
		}
	})
	return f.client
}

// Fetch implements Fetcher. The body fails with ErrMediaTooLarge past
// MaxBytes.
func (f *HTTPFetcher) Fetch(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := f.check(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sap13ns-wpimport/1")
	req.Header.Set("Accept", "image/*,video/*,audio/*,application/pdf,*/*;q=0.5")
	resp, err := f.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("wpimport: %s: HTTP %d", u.Redacted(), resp.StatusCode)
	}
	limit := f.maxBytes()
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, ErrMediaTooLarge
	}
	return &limitedBody{Reader: &limitReader{r: resp.Body, remaining: limit, err: ErrMediaTooLarge}, Closer: resp.Body}, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}
