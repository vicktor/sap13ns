package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/web"
)

// ServeHTTP routes a request of a multisite install to its site:
//
//  1. /healthz answers on any host (DEPLOY.md checks it with Host: x), and
//     so does AskPath, to loopback and the trusted proxies only (tls.go);
//  2. the host is canonicalised (lower case, no port or trailing dot,
//     A-label) and looked up in the host table;
//  3. an unknown host gets a 404 page, or a 302 to the network with
//     SAP13NS_UNKNOWN_HOST=redirect;
//  4. another host of a site redirects to its primary (301, or 308 for
//     other methods than GET and HEAD), except ACME challenges and the
//     WebAuthn related origins file;
//  5. a suspended site only answers a 503 page, but to the session of a
//     network admin's emergency access while one is live (and the sign-in
//     that opens it); a site waiting for approval, or held for its billing
//     (Network.Held), a 503 page too, except its sign-in and admin;
//  6. otherwise the site, loaded if needed, serves the request, with at
//     most MaxInflight requests at once.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/healthz":
		r.health(w, req)
		return
	case AskPath:
		r.ask(w, req)
		return
	}
	t := r.table.Load()
	host, ok := hostOf(req.Host)
	var rt *siteRoute
	if ok && t != nil {
		rt = t.hosts[host]
	}
	switch {
	case rt == nil:
		if r.cfg.UnknownHost == "redirect" {
			http.Redirect(w, req, r.cfg.BaseURL.String()+"/", http.StatusFound)
			return
		}
		r.statusPage(w, req, nil, web.StatusUnknown, http.StatusNotFound)
		return
	case host != rt.Primary && !aliasServed(req.URL.Path):
		r.toPrimary(w, req, rt)
		return
	case rt.Status == StatusSuspended && !rt.Emergency:
		r.statusPage(w, req, rt, web.StatusSuspended, http.StatusServiceUnavailable)
		return
	case rt.Status == StatusSuspended:
		// An emergency access is live: the site serves its session (and
		// the sign-in that opens it), and the 503 page to everything else.
		req = req.WithContext(web.EmergencyOnly(req.Context(), http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.statusPage(w, req, rt, web.StatusSuspended, http.StatusServiceUnavailable)
		})))
	case rt.Status == StatusPending || rt.Held:
		// Its sign-in pages and admin only. Its sign-in address is its own
		// (Settings → Accounts & security): the site tells them apart, and
		// answers the rest with the 503 page too (web.SignInOnly). An
		// address no site could sign in at gets it without a load.
		kind := web.StatusPending
		if rt.Status != StatusPending {
			kind = web.StatusBilling
		}
		closed := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			r.statusPage(w, req, rt, kind, http.StatusServiceUnavailable)
		})
		if !web.NotOpenServes(req.URL.Path) {
			closed(w, req)
			return
		}
		req = req.WithContext(web.SignInOnly(req.Context(), closed))
	}
	e, err := r.acquire(req.Context(), rt, r.o.LoadWait)
	if err != nil {
		if req.Context().Err() == nil && !errors.Is(err, errBusy) && !errors.Is(err, errUnavailable) {
			r.log.Error("site unavailable", "site", rt.ID, "host", host, "err", err)
		}
		r.statusPage(w, req, rt, web.StatusBusy, http.StatusServiceUnavailable)
		return
	}
	defer r.release(e)
	e.app.ServeHTTP(w, req)
}

// statusPage answers for a host without a running site, rt when the host
// is a site's, in the site's language; the stylesheet and scripts of the
// page come from the router too.
func (r *Registry) statusPage(w http.ResponseWriter, req *http.Request, rt *siteRoute, kind string, status int) {
	if strings.HasPrefix(req.URL.Path, "/static/") {
		r.pages.ServeStatic(w, req)
		return
	}
	var langs []string
	if rt != nil {
		langs = r.langs.get(req.Context(), r.net, rt.ID, r.o.Now())
	}
	r.pages.Serve(w, req, kind, status, r.cfg.BaseURL.Scheme == "https", langs)
}

// How long the languages of a site are kept for its status pages, and
// how many sites' languages at most.
const (
	siteLangsTTL     = 10 * time.Minute
	siteLangsRetry   = 30 * time.Second // after a failed read
	siteLangsMax     = 4096
	siteLangsTimeout = time.Second
)

// siteLangs keeps the languages of the sites whose status pages were
// served (a site with a status page is not loaded, so its settings are
// read from its schema): a held site answers every visitor with one.
type siteLangs struct {
	mu sync.Mutex
	m  map[int64]siteLangsEntry
}

type siteLangsEntry struct {
	langs []string
	until time.Time
}

// get returns the languages of a site, its main one first; nil when they
// cannot be read, and the page follows the visitor.
func (c *siteLangs) get(ctx context.Context, n *Network, id int64, now time.Time) []string {
	c.mu.Lock()
	e, ok := c.m[id]
	c.mu.Unlock()
	if ok && now.Before(e.until) {
		return e.langs
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), siteLangsTimeout)
	defer cancel()
	langs, err := n.SiteLanguages(ctx, id)
	e = siteLangsEntry{langs: langs, until: now.Add(siteLangsTTL)}
	if err != nil {
		e = siteLangsEntry{until: now.Add(siteLangsRetry)}
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= siteLangsMax {
		c.m = map[int64]siteLangsEntry{}
	}
	c.m[id] = e
	c.mu.Unlock()
	return e.langs
}

// aliasServed are the paths a site answers on all its hosts: ACME HTTP-01
// challenges, and the WebAuthn related origins file its RP ID host serves
// when the primary is a custom domain.
func aliasServed(path string) bool {
	return strings.HasPrefix(path, "/.well-known/acme-challenge/") || path == "/.well-known/webauthn"
}

// toPrimary redirects to the same path on the primary host, keeping the
// network's scheme and port.
func (r *Registry) toPrimary(w http.ResponseWriter, req *http.Request, rt *siteRoute) {
	target := r.cfg.BaseURL.Scheme + "://" + rt.Primary
	if port := r.cfg.BaseURL.Port(); port != "" {
		target += ":" + port
	}
	uri := req.URL.RequestURI()
	if !strings.HasPrefix(uri, "/") {
		uri = "/"
	}
	code := http.StatusMovedPermanently
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		code = http.StatusPermanentRedirect // keeps the method and body
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.Redirect(w, req, target+uri, code)
}

func (r *Registry) health(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	if err := r.net.db.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprint(w, "ok\n")
}
