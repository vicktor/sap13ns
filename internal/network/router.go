package network

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"sap13ns/internal/web"
)

// ServeHTTP routes a request of a multisite install to its site:
//
//  1. /healthz answers on any host (DEPLOY.md checks it with Host: x);
//  2. the host is canonicalised (lower case, no port or trailing dot,
//     A-label) and looked up in the host table;
//  3. an unknown host gets a 404 page, or a 302 to the network with
//     SAP13NS_UNKNOWN_HOST=redirect;
//  4. another host of a site redirects to its primary (301, or 308 for
//     other methods than GET and HEAD), except ACME challenges and the
//     WebAuthn related origins file;
//  5. a suspended site only answers a 503 page; a site waiting for
//     approval, or held for its billing (Network.Held), a 503 page too,
//     except its sign-in and admin;
//  6. otherwise the site, loaded if needed, serves the request, with at
//     most MaxInflight requests at once.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path == "/healthz" {
		r.health(w, req)
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
		r.statusPage(w, req, web.StatusUnknown, http.StatusNotFound)
		return
	case host != rt.Primary && !aliasServed(req.URL.Path):
		r.toPrimary(w, req, rt)
		return
	case rt.Status == StatusSuspended:
		r.statusPage(w, req, web.StatusSuspended, http.StatusServiceUnavailable)
		return
	case rt.Status == StatusPending && !pendingServed(req.URL.Path):
		r.statusPage(w, req, web.StatusPending, http.StatusServiceUnavailable)
		return
	case rt.Held && !pendingServed(req.URL.Path):
		r.statusPage(w, req, web.StatusBilling, http.StatusServiceUnavailable)
		return
	}
	e, err := r.acquire(req.Context(), rt, r.o.LoadWait)
	if err != nil {
		if req.Context().Err() == nil && !errors.Is(err, errBusy) && !errors.Is(err, errUnavailable) {
			r.log.Error("site unavailable", "site", rt.ID, "host", host, "err", err)
		}
		r.statusPage(w, req, web.StatusBusy, http.StatusServiceUnavailable)
		return
	}
	defer r.release(e)
	e.app.ServeHTTP(w, req)
}

// statusPage answers for a host without a running site; the stylesheet
// and scripts of the page come from the router too.
func (r *Registry) statusPage(w http.ResponseWriter, req *http.Request, kind string, status int) {
	if strings.HasPrefix(req.URL.Path, "/static/") {
		r.pages.ServeStatic(w, req)
		return
	}
	r.pages.Serve(w, req, kind, status, r.cfg.BaseURL.Scheme == "https")
}

// aliasServed are the paths a site answers on all its hosts: ACME HTTP-01
// challenges, and the WebAuthn related origins file its RP ID host serves
// when the primary is a custom domain.
func aliasServed(path string) bool {
	return strings.HasPrefix(path, "/.well-known/acme-challenge/") || path == "/.well-known/webauthn"
}

// pendingServed are the paths of a site waiting for approval (or held for
// its billing) that work:
// sign-in (also through the network, /sso) and the admin, with the assets
// their pages use.
func pendingServed(path string) bool {
	for _, p := range []string{"/login", "/logout", "/sso", "/admin", "/forgot-password", "/reset-password"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "/static/")
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
