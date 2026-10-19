package network

// TLS for the hosts of a network. By default a proxy terminates TLS
// (SAP13NS_TLS=proxy): nginx or Caddy with a wildcard certificate for the
// default subdomains, and certificates for custom domains from Caddy's
// on-demand TLS, which first asks AskPath whether a host is served here.
// With SAP13NS_TLS=autocert the process terminates TLS itself: a static
// wildcard certificate covers the portal and the default subdomains, and
// the built-in ACME client gets a certificate for each verified custom
// domain, and only for those (HostPolicy).

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
)

// AskPath answers Caddy's on-demand TLS "ask": 200 when ?domain= is a host
// the network routes, 404 otherwise. Only a direct request from loopback
// or a trusted proxy (SAP13NS_TRUSTED_PROXIES) is answered; anything
// else, including a request relayed by a proxy for a client (it carries
// X-Forwarded-For, X-Real-IP or Forwarded), gets the same 404 whatever the
// domain, so the endpoint tells the internet nothing. The path is
// reserved on every host.
const AskPath = "/_sap13ns/tls-ask"

// ask answers AskPath.
func (r *Registry) ask(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) || !r.askAllowed(req) {
		http.NotFound(w, req)
		return
	}
	host, err := CanonicalHost(strings.ToLower(req.URL.Query().Get("domain")))
	t := r.table.Load()
	if err != nil || t == nil || t.hosts[host] == nil {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

// askAllowed reports whether a request comes straight from this machine
// or a trusted proxy, not relayed for someone else.
func (r *Registry) askAllowed(req *http.Request) bool {
	for _, h := range []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded"} {
		if _, ok := req.Header[h]; ok {
			return false
		}
	}
	ap, err := netip.ParseAddrPort(req.RemoteAddr)
	if err != nil {
		return false // a Unix socket: only the proxy, relaying clients
	}
	a := ap.Addr().Unmap()
	if a.IsLoopback() {
		return true
	}
	for _, p := range r.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// errNoCert is a host the built-in ACME client does not get a
// certificate for.
var errNoCert = errors.New("network: no certificate for this host")

// HostPolicy is the autocert.HostPolicy of a network: only the verified
// custom domains it routes. The portal and the default subdomains are
// covered by the static wildcard certificate, so their names never go to
// the ACME CA (Let's Encrypt allows 50 certificates per registered domain
// a week).
func (r *Registry) HostPolicy(_ context.Context, host string) error {
	h, err := CanonicalHost(strings.ToLower(host))
	if err != nil || r.static(h) {
		return errNoCert
	}
	if t := r.table.Load(); t == nil || t.hosts[h] == nil {
		return errNoCert
	}
	return nil
}

// static reports whether a host is the network's own: the portal host and
// the hosts under SAP13NS_SITES_DOMAIN, which the wildcard certificate
// covers.
func (r *Registry) static(host string) bool {
	return host == "" || under(host, r.cfg.SitesDomain) || under(host, r.cfg.BaseURL.Hostname())
}

// Autocert is the TLS of a network that terminates it itself.
type Autocert struct {
	Manager *autocert.Manager
	static  *certFile
	r       *Registry
}

// NewAutocert prepares SAP13NS_TLS=autocert: the wildcard certificate is
// read now (and again when its files change), and certificates for custom
// domains are cached under $SAP13NS_DATA_DIR/cache/autocert.
func (r *Registry) NewAutocert() (*Autocert, error) {
	cfg := r.cfg
	static := &certFile{cert: cfg.TLSCert, key: cfg.TLSKey, log: r.log}
	if _, err := static.get(); err != nil {
		return nil, fmt.Errorf("network: SAP13NS_TLS_CERT and SAP13NS_TLS_KEY: %w", err)
	}
	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(filepath.Join(cfg.DataDir, "cache", "autocert")),
		HostPolicy: r.HostPolicy,
		Email:      cfg.ACMEEmail,
	}
	if cfg.ACMEDirectory != "" {
		m.Client = &acme.Client{DirectoryURL: cfg.ACMEDirectory}
	}
	return &Autocert{Manager: m, static: static, r: r}, nil
}

// TLSConfig serves the wildcard certificate for the network's own hosts
// (and a client without SNI), and the ACME certificates for custom
// domains, with TLS-ALPN-01 challenges.
func (a *Autocert) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1", acme.ALPNProto},
		GetCertificate: a.getCertificate(a.Manager.GetCertificate),
	}
}

func (a *Autocert) getCertificate(acmeCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := strings.TrimSuffix(strings.ToLower(hello.ServerName), ".")
		if a.r.static(name) {
			return a.static.get()
		}
		return acmeCert(hello)
	}
}

// HTTPHandler answers on plain HTTP: ACME HTTP-01 challenges, and a
// redirect to https for the hosts the network routes (404 for others).
func (a *Autocert) HTTPHandler() http.Handler {
	return a.Manager.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		host, ok := hostOf(req.Host)
		t := a.r.table.Load()
		if !ok || t == nil || t.hosts[host] == nil {
			http.NotFound(w, req)
			return
		}
		target := "https://" + host
		if port := a.r.cfg.BaseURL.Port(); port != "" {
			target += ":" + port
		}
		uri := req.URL.RequestURI()
		if !strings.HasPrefix(uri, "/") {
			uri = "/"
		}
		code := http.StatusMovedPermanently
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			code = http.StatusPermanentRedirect
		}
		http.Redirect(w, req, target+uri, code)
	}))
}

// certFile is a certificate and key on disk, read again at most once a
// minute when either file changed (a renewed wildcard certificate is
// picked up without a restart). If a new pair does not load, the old one
// stays in use.
type certFile struct {
	cert, key string
	log       interface{ Warn(string, ...any) }

	mu      sync.Mutex
	pair    *tls.Certificate
	mod     time.Time
	checked time.Time
}

func (c *certFile) get() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.pair != nil && now.Sub(c.checked) < time.Minute {
		return c.pair, nil
	}
	c.checked = now
	mod := time.Time{}
	for _, f := range []string{c.cert, c.key} {
		if fi, err := os.Stat(f); err == nil && fi.ModTime().After(mod) {
			mod = fi.ModTime()
		}
	}
	if c.pair != nil && !mod.After(c.mod) {
		return c.pair, nil
	}
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		if c.pair != nil {
			c.log.Warn("the TLS certificate changed but does not load; keeping the previous one", "err", err)
			return c.pair, nil
		}
		return nil, err
	}
	c.pair, c.mod = &pair, mod
	return c.pair, nil
}
