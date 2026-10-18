// Package update tells a running sap13ns about new releases and installs
// them on request.
//
// # Release layout
//
// Every release of DefaultRepo (built by .github/workflows/release.yml from a
// tag vX.Y.Z) carries these assets:
//
//	sap13ns_<version>_<goos>_<goarch>.tar.gz  one per platform, see AssetName;
//	                                          <version> is the tag without "v"
//	SHA256SUMS                                sha256sum output for every tarball
//	SHA256SUMS.sig                            base64 Ed25519 signature over the
//	                                          exact bytes of SHA256SUMS
//
// For example sap13ns_1.4.0_linux_amd64.tar.gz for tag v1.4.0. Each tarball
// holds the sap13ns binary at its root (plus LICENSE, README.md and the
// deploy/ directory when the repository has them); nothing else in it is
// ever written to disk.
//
// # Trust
//
// The Ed25519 public key is compiled into the binary with
//
//	-ldflags "-X sap13ns/internal/update.PublicKey=<base64>"
//
// Apply refuses to install anything when the key is missing, when the
// signature over SHA256SUMS does not verify, or when the tarball's hash is not
// the one SHA256SUMS lists for its name. Because the name includes the
// version, an old (validly signed) SHA256SUMS cannot be replayed for a newer
// release. All traffic is https to GitHub hosts only (see Check and Apply),
// redirects included.
//
// Nothing is stored on disk except the binary swap itself: the Checker keeps
// its result in memory.
package update

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultRepo is the GitHub repository ("owner/name") releases come from.
const DefaultRepo = "vicktor/sap13ns"

// EnvCheck is the environment variable that turns the periodic release check
// off when set to a false value ("false", "0", ...). See EnabledFromEnv.
const EnvCheck = "SAP13NS_UPDATE_CHECK"

// PublicKey is the base64 Ed25519 public key (32 bytes) that release
// checksums must be signed with. It is set at build time:
//
//	go build -ldflags "-X sap13ns/internal/update.PublicKey=<base64>" ./cmd/sap13ns
//
// Builds without it can still check for releases but Apply refuses to
// install them.
var PublicKey string

var (
	// ErrNoRelease means the repository has no published, non-prerelease
	// release (GitHub answers 404), or the latest one is a draft/prerelease.
	ErrNoRelease = errors.New("update: no published release")
	// ErrNoPublicKey means the binary was built without PublicKey.
	ErrNoPublicKey = errors.New("update: this build has no update signing key " +
		"(it was built without -X sap13ns/internal/update.PublicKey), so it refuses unsigned updates; " +
		"install the release manually from GitHub")
	// ErrBadSignature means SHA256SUMS.sig does not verify with the public key.
	ErrBadSignature = errors.New("update: SHA256SUMS signature is not valid for this build's public key")
	// ErrChecksumMismatch means the downloaded tarball does not match SHA256SUMS.
	ErrChecksumMismatch = errors.New("update: downloaded archive does not match SHA256SUMS")
	// ErrNoAsset means the release lacks an asset Apply needs (the tarball
	// for this platform, SHA256SUMS or SHA256SUMS.sig).
	ErrNoAsset = errors.New("update: release is missing a required asset")
)

// EnabledFromEnv reports whether periodic release checks are enabled: true
// unless SAP13NS_UPDATE_CHECK holds a false value (strconv.ParseBool syntax).
// Call it after config.LoadEnvFile so the env file is taken into account.
func EnabledFromEnv() bool {
	v := strings.TrimSpace(os.Getenv(EnvCheck))
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return true
	}
	return b
}

// NewHTTPClient returns a client suitable for Check and Apply: bounded dial,
// TLS and header timeouts but no overall timeout, so large downloads are
// limited by the caller's context instead.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          4,
		ForceAttemptHTTP2:     true,
	}}
}

// Hosts each kind of request may reach, redirects included.
var (
	apiHosts      = map[string]bool{"api.github.com": true}
	downloadHosts = map[string]bool{
		"github.com":                           true,
		"objects.githubusercontent.com":        true,
		"release-assets.githubusercontent.com": true,
	}
)

const maxRedirects = 10

// checkURL accepts only https URLs on the default port of an allowed host.
func checkURL(u *url.URL, hosts map[string]bool) error {
	if u.Scheme != "https" {
		return fmt.Errorf("update: refusing non-https URL %s", shortURL(u))
	}
	if u.User != nil {
		return fmt.Errorf("update: refusing URL with credentials (%s)", shortURL(u))
	}
	if p := u.Port(); p != "" && p != "443" {
		return fmt.Errorf("update: refusing URL with port %s (%s)", p, shortURL(u))
	}
	if !hosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("update: host %q is not an allowed GitHub host", u.Hostname())
	}
	return nil
}

func checkRawURL(raw string, hosts map[string]bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("update: bad URL: %w", err)
	}
	return u, checkURL(u, hosts)
}

// shortURL drops the query (signed download URLs carry tokens there).
func shortURL(u *url.URL) string {
	return u.Scheme + "://" + u.Host + u.EscapedPath()
}

// restrictClient returns a copy of c (or a default client) whose redirects
// are limited to https URLs on hosts. The caller's client is not modified.
func restrictClient(c *http.Client, hosts map[string]bool) *http.Client {
	if c == nil {
		c = NewHTTPClient()
	}
	cc := *c
	prev := c.CheckRedirect
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errors.New("update: too many redirects")
		}
		if err := checkURL(req.URL, hosts); err != nil {
			return err
		}
		if prev != nil {
			return prev(req, via)
		}
		return nil
	}
	return &cc
}

func userAgent(current string) string {
	v := strings.TrimSpace(current)
	if v == "" || strings.ContainsAny(v, " \t\r\n") {
		v = "unknown"
	}
	return "sap13ns/" + v
}

var errTooLarge = errors.New("response too large")

// readLimited reads at most max bytes and fails if r has more.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%w (more than %d bytes)", errTooLarge, max)
	}
	return b, nil
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
