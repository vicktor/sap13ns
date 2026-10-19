package network

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// verifiedTenant creates a site with a verified custom domain and an
// unverified one.
func (e *netEnv) verifiedTenant(slug, host, pending string) *Site {
	e.t.Helper()
	s := e.tenant(slug, "pro")
	for _, h := range []string{host, pending} {
		d, err := e.n.AddDomain(e.ctx, s.ID, h)
		if err != nil {
			e.t.Fatal(err)
		}
		if h == host {
			e.dns.point(h, d.VerifyToken)
			if _, chk, err := e.n.CheckDomain(e.ctx, s.ID, h); err != nil || !chk.OK() {
				e.t.Fatalf("verify %s: %+v %v", h, chk, err)
			}
		}
	}
	e.refresh()
	return s
}

// TestTLSAsk: Caddy's on-demand TLS gets 200 for the hosts the network
// routes, and only when it asks itself, from loopback or a trusted proxy.
func TestTLSAsk(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	})
	a := e.verifiedTenant("alpha", "www.alpha.example", "shop.alpha.example")
	ask := func(method, remote, domain string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "http://127.0.0.1:8176"+AskPath+"?domain="+domain, nil)
		req.RemoteAddr = remote
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		e.reg.ServeHTTP(rec, req)
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", rec.Header().Get("Cache-Control"))
		}
		return rec
	}
	denied := ask("GET", "192.0.2.10:4000", "unknown.example").Body.String()
	for _, remote := range []string{"127.0.0.1:5000", "[::1]:5000", "10.1.2.3:5000"} {
		for domain, code := range map[string]int{
			"www.alpha.example": 200, "WWW.Alpha.Example.": 200, "alpha.sap13ns.site": 200, "sap13ns.site": 200,
			"shop.alpha.example": 404, "unknown.example": 404, "": 404, "bad%20host": 404, "www.alpha.example:443": 404,
		} {
			if rec := ask("GET", remote, domain); rec.Code != code {
				t.Fatalf("%s asks for %q: %d, want %d", remote, domain, rec.Code, code)
			}
		}
	}
	if rec := ask("HEAD", "127.0.0.1:5000", "www.alpha.example"); rec.Code != 200 {
		t.Fatalf("HEAD: %d", rec.Code)
	}
	// Anyone else, or a request relayed for someone else, learns nothing:
	// the same answer as for a host the network does not have.
	for _, r := range []struct {
		method, remote string
		header         []string
	}{
		{"GET", "192.0.2.10:4000", nil},
		{"GET", "[2001:db8::1]:4000", nil},
		{"GET", "11.0.0.1:4000", nil},
		{"GET", "@", nil}, // a Unix socket: the proxy relaying clients
		{"GET", "", nil},
		{"GET", "127.0.0.1:5000", []string{"X-Forwarded-For", "192.0.2.10"}},
		{"GET", "127.0.0.1:5000", []string{"X-Real-IP", "192.0.2.10"}},
		{"GET", "10.1.2.3:5000", []string{"Forwarded", "for=192.0.2.10"}},
		{"POST", "127.0.0.1:5000", nil},
	} {
		rec := ask(r.method, r.remote, "www.alpha.example", r.header...)
		if rec.Code != http.StatusNotFound || rec.Body.String() != denied {
			t.Fatalf("%s from %q %v: %d %q", r.method, r.remote, r.header, rec.Code, rec.Body.String())
		}
	}
	// On any host, before the site: the path is the network's.
	req := httptest.NewRequest("GET", "https://www.alpha.example"+AskPath+"?domain=www.alpha.example", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	e.reg.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("on a site's host: %d", rec.Code)
	}
	// Removed, it is no longer asked for.
	if err := e.n.RemoveDomain(e.ctx, a.ID, "www.alpha.example"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if rec := ask("GET", "127.0.0.1:5000", "www.alpha.example"); rec.Code != http.StatusNotFound {
		t.Fatalf("a removed domain: %d", rec.Code)
	}
}

// TestHostPolicy: the built-in ACME client gets certificates for the
// verified custom domains the network routes, and nothing else.
func TestHostPolicy(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.verifiedTenant("alpha", "www.alpha.example", "shop.alpha.example")
	for host, ok := range map[string]bool{
		"www.alpha.example": true, "WWW.alpha.example.": true,
		"shop.alpha.example": false, "alpha.sap13ns.site": false, "sap13ns.site": false, "new.sap13ns.site": false,
		"unknown.example": false, "": false, "bad host": false, "127.0.0.1": false,
	} {
		if err := e.reg.HostPolicy(e.ctx, host); (err == nil) != ok {
			t.Fatalf("HostPolicy(%q) = %v", host, err)
		}
	}
	// A suspended site still answers on its domain (with its notice).
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if err := e.reg.HostPolicy(e.ctx, "www.alpha.example"); err != nil {
		t.Fatalf("a suspended site: %v", err)
	}
	if err := e.n.DeleteSite(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if err := e.reg.HostPolicy(e.ctx, "www.alpha.example"); err == nil {
		t.Fatal("a certificate for a deleted site")
	}
}

// writeCert writes a self-signed certificate for names, and its key.
func writeCert(t *testing.T, dir string, serial int64, names ...string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cert, keyFile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600); err != nil {
		t.Fatal(err)
	}
	return cert, keyFile
}

func serialOf(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

// TestAutocert: SAP13NS_TLS=autocert serves the wildcard certificate for
// the network's hosts and goes to the ACME CA for verified custom domains
// only. The CA here is a local server that counts what reaches it.
func TestAutocert(t *testing.T) {
	var calls atomic.Int32
	ca := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	t.Cleanup(ca.Close)
	dir := t.TempDir()
	cert, key := writeCert(t, dir, 1, "*.sap13ns.site", "sap13ns.site")
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Config.TLS, o.Config.TLSCert, o.Config.TLSKey = "autocert", cert, key
		o.Config.ACMEEmail, o.Config.ACMEDirectory = "ops@example.com", ca.URL+"/directory"
	})
	e.verifiedTenant("alpha", "www.alpha.example", "shop.alpha.example")

	ac, err := e.reg.NewAutocert()
	if err != nil {
		t.Fatal(err)
	}
	if ac.Manager.Client == nil || ac.Manager.Client.DirectoryURL != ca.URL+"/directory" || ac.Manager.Email != "ops@example.com" {
		t.Fatalf("manager: %+v", ac.Manager)
	}
	cfg := ac.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS12 || !slices.Contains(cfg.NextProtos, acme.ALPNProto) || !slices.Contains(cfg.NextProtos, "h2") {
		t.Fatalf("TLS config: %v %v", cfg.MinVersion, cfg.NextProtos)
	}
	// The network's own names, and a client without SNI: the static
	// certificate, without the CA.
	for _, name := range []string{"sap13ns.site", "alpha.sap13ns.site", "Beta.Sap13ns.Site.", ""} {
		c, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil || serialOf(t, c) != 1 {
			t.Fatalf("certificate for %q: %v", name, err)
		}
	}
	// Hosts the network does not route never reach the CA.
	for _, name := range []string{"shop.alpha.example", "unknown.example", "x.example.net"} {
		if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err == nil {
			t.Fatalf("a certificate for %s", name)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d requests reached the CA", n)
	}
	// A verified custom domain goes to the ACME client.
	var asked []string
	get := ac.getCertificate(func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		asked = append(asked, h.ServerName)
		return nil, errors.New("fake CA")
	})
	if _, err := get(&tls.ClientHelloInfo{ServerName: "www.alpha.example"}); err == nil || !slices.Equal(asked, []string{"www.alpha.example"}) {
		t.Fatalf("asked %v: %v", asked, err)
	}

	// Plain HTTP: the routed hosts go to https, others get 404; ACME
	// challenges only for the hosts of the policy.
	h := ac.HTTPHandler()
	plain := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := plain("GET", "http://www.alpha.example/a?b=1"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://www.alpha.example/a?b=1" {
		t.Fatalf("GET: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := plain("POST", "http://alpha.sap13ns.site/login"); rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "https://alpha.sap13ns.site/login" {
		t.Fatalf("POST: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for _, target := range []string{"http://shop.alpha.example/", "http://unknown.example/"} {
		if rec := plain("GET", target); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", target, rec.Code)
		}
	}
	if rec := plain("GET", "http://alpha.sap13ns.site/.well-known/acme-challenge/x"); rec.Code == http.StatusOK || rec.Code/100 == 3 {
		t.Fatalf("a challenge for a static host: %d", rec.Code)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("%d requests reached the CA", n)
	}

	// The wildcard certificate is read again when it changes; one that
	// does not load leaves the previous one in use.
	c := ac.static
	later := time.Now().Add(time.Hour)
	if err := os.WriteFile(cert, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(cert, later, later)
	c.checked = time.Time{}
	if p, err := c.get(); err != nil || serialOf(t, p) != 1 {
		t.Fatalf("a broken renewal: %v", err)
	}
	writeCert(t, dir, 2, "*.sap13ns.site", "sap13ns.site")
	later = later.Add(time.Hour)
	os.Chtimes(cert, later, later)
	if p, _ := c.get(); serialOf(t, p) != 1 {
		t.Fatal("read again within the minute")
	}
	c.checked = time.Time{}
	if p, err := c.get(); err != nil || serialOf(t, p) != 2 {
		t.Fatalf("the renewal: %v", err)
	}

	// Without a certificate it does not start.
	e.cfg.TLSKey = filepath.Join(dir, "missing.pem")
	if _, err := e.reg.NewAutocert(); err == nil || !bytes.Contains([]byte(err.Error()), []byte("SAP13NS_TLS_CERT")) {
		t.Fatalf("a missing key: %v", err)
	}
}
