package web

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"sap13ns/internal/store"
)

// The site's service worker (ui/static/sw.js, served at /sw.js so that its
// scope is the whole site). It is off unless the site asks for something
// it does: Settings → Site → "Keep the pages a visitor opened readable
// without a connection".
//
// The worker never decides what a page shows while the site can be
// reached: pages are asked from the network first, and a copy is used
// only when that fails. It keeps public responses only (Cache-Control:
// public, which a page rendered for a session never has), and it leaves
// the admin, the API and sign-in alone.
//
// Switching everything off must reach the browsers that already have the
// worker: /sw.js then answers a worker that deletes its caches and
// unregisters itself, and the pages unregister it too (pwa.js).

// workerOn reports whether public pages register the service worker.
func (s *Server) workerOn() bool { return s.settings().Offline }

// workerCSP lets public pages register the site's own worker.
func (s *Server) workerCSP(policy string) string {
	if !s.workerOn() {
		return policy
	}
	return strings.Replace(policy, "worker-src 'none'", "worker-src 'self'", 1)
}

const workerOff = `// sap13ns: this site no longer uses a service worker.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(
  caches.keys().then((keys) => Promise.all(keys.filter((k) => k.startsWith("sap-")).map((k) => caches.delete(k))))
    .then(() => self.registration.unregister())));
`

// handleWorker serves GET /sw.js: the worker with what the site turned
// on, or the one that removes itself.
func (s *Server) handleWorker(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "text/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	if !s.workerOn() {
		fmt.Fprint(w, workerOff)
		return
	}
	src, err := fs.ReadFile(s.ui, "static/sw.js")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	fmt.Fprintf(w, "self.SAP = {offline: %t};\n", s.settings().Offline)
	w.Write(src)
}

// handleOffline renders GET /api/offline, the page the worker shows for a
// page it has no copy of: the theme's page template, in the site's
// language.
func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	if !s.settings().Offline {
		s.notFound(w, r)
		return
	}
	th, pd, err := s.basePage(r, "page")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	title := s.tr(r, "You are offline")
	pv := s.postView(&store.Post{Type: store.TypePage, Title: title, Lang: rctx(r).Lang}, nil)
	pv.Content = template.HTML("<p>" + template.HTMLEscapeString(s.tr(r, "This page was not saved for reading without a connection. Connect again and reload it.")) + "</p>")
	pd.Post, pd.Title, pd.Heading, pd.NoIndex = pv, title, title, true
	s.renderPage(w, r, th, "page", pd, http.StatusOK)
}
