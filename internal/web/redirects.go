package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/store"
)

// Redirects are matched on the exact path a visitor requested (language
// prefix included), before routing. The table is small and read on every
// request, so it lives in memory and is reloaded after each change; hit
// counts are batched and written by the background loop.

type redirectTable struct {
	mu     sync.RWMutex
	loaded bool
	bySrc  map[string]store.Redirect
	hits   map[int64]int64
}

func (t *redirectTable) reset() {
	t.mu.Lock()
	t.loaded, t.bySrc = false, nil
	t.mu.Unlock()
}

func (s *Server) lookupRedirect(ctx context.Context, path string) (store.Redirect, bool) {
	t := &s.redirects
	t.mu.RLock()
	loaded := t.loaded
	rd, ok := t.bySrc[path]
	t.mu.RUnlock()
	if loaded {
		return rd, ok
	}
	list, err := s.store.Redirects(ctx)
	if err != nil {
		s.log.Error("load redirects", "err", err)
		return store.Redirect{}, false
	}
	m := make(map[string]store.Redirect, len(list))
	for _, r := range list {
		m[r.Source] = r
	}
	t.mu.Lock()
	t.bySrc, t.loaded = m, true
	t.mu.Unlock()
	rd, ok = m[path]
	return rd, ok
}

func (s *Server) countRedirectHit(id int64) {
	t := &s.redirects
	t.mu.Lock()
	if t.hits == nil {
		t.hits = map[int64]int64{}
	}
	t.hits[id]++
	t.mu.Unlock()
}

// flushRedirectHits writes the batched hit counts.
func (s *Server) flushRedirectHits(ctx context.Context) {
	t := &s.redirects
	t.mu.Lock()
	hits := t.hits
	t.hits = nil
	t.mu.Unlock()
	if len(hits) == 0 {
		return
	}
	if err := s.store.RedirectHits(ctx, hits); err != nil {
		s.log.Error("redirect hits", "err", err)
	}
}

// withRedirects answers GET and HEAD requests whose path has a redirect.
func (s *Server) withRedirects(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || strings.HasPrefix(r.URL.Path, "/admin") ||
			strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		var rd store.Redirect
		ok := false
		if r.URL.RawQuery != "" { // old plain permalinks such as /?p=12
			rd, ok = s.lookupRedirect(r.Context(), r.URL.Path+"?"+r.URL.RawQuery)
		}
		if !ok {
			rd, ok = s.lookupRedirect(r.Context(), r.URL.Path)
		}
		if !ok && len(r.URL.Path) > 1 && strings.HasSuffix(r.URL.Path, "/") {
			rd, ok = s.lookupRedirect(r.Context(), strings.TrimSuffix(r.URL.Path, "/"))
		}
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		s.countRedirectHit(rd.ID)
		if rd.Status == http.StatusGone {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			th, pd, err := s.basePage(r, "404")
			if err != nil {
				http.Error(w, http.StatusText(http.StatusGone), http.StatusGone)
				return
			}
			pd.Title = s.tr(r, "This page has been removed")
			pd.NoIndex = true
			s.renderPage(w, r, th, "404", pd, http.StatusGone)
			return
		}
		target := rd.Target
		if r.URL.RawQuery != "" && !strings.Contains(target, "?") && !strings.Contains(rd.Source, "?") {
			target += "?" + r.URL.RawQuery
		}
		if rd.Status == http.StatusMovedPermanently || rd.Status == http.StatusPermanentRedirect {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		http.Redirect(w, r, target, rd.Status)
	})
}

// normalizeRedirect validates an admin-entered rule. Sources are local
// paths; targets are local paths or http(s) URLs.
func normalizeRedirect(source, target string, status int) (string, string, bool) {
	source, target = strings.TrimSpace(source), strings.TrimSpace(target)
	if u, err := url.Parse(source); err == nil && u.IsAbs() {
		source = u.EscapedPath()
	}
	if !strings.HasPrefix(source, "/") || strings.HasPrefix(source, "//") || len(source) > 1000 ||
		strings.ContainsAny(source, " \t\r\n#") || strings.Count(source, "?") > 1 || strings.HasPrefix(source, "/admin") || strings.HasPrefix(source, "/static/") {
		return "", "", false
	}
	if !slices.Contains([]int{301, 302, 307, 308, 410}, status) {
		return "", "", false
	}
	if status == http.StatusGone {
		return source, "", true
	}
	if len(target) > 2000 || strings.ContainsAny(target, "\r\n") || target == source {
		return "", "", false
	}
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return source, target, true
	}
	if !validHTTPURL(target) {
		return "", "", false
	}
	return source, target, true
}

// ------------------------------------------------------------------ admin --

type redirectsData struct {
	Redirects []store.Redirect
	Error     string
	Form      store.Redirect
}

func (s *Server) adminRedirects(w http.ResponseWriter, r *http.Request) {
	s.renderRedirects(w, r, http.StatusOK, store.Redirect{Status: 301}, "")
}

func (s *Server) renderRedirects(w http.ResponseWriter, r *http.Request, status int, form store.Redirect, msg string) {
	list, err := s.store.Redirects(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "redirects", s.t("Redirects"), "redirects", redirectsData{Redirects: list, Error: msg, Form: form})
}

func (s *Server) adminRedirectSave(w http.ResponseWriter, r *http.Request) {
	status, _ := strconv.Atoi(r.FormValue("status"))
	form := store.Redirect{Source: r.FormValue("source"), Target: r.FormValue("target"), Status: status}
	src, dst, ok := normalizeRedirect(form.Source, form.Target, status)
	if !ok {
		s.renderRedirects(w, r, http.StatusUnprocessableEntity, form,
			s.t("Use a path starting with / as the source, and a path or an http(s) address as the target."))
		return
	}
	var err error
	if id, ok := pathID(r); ok {
		err = s.store.UpdateRedirect(r.Context(), id, src, dst, status)
	} else {
		err = s.store.SaveRedirect(r.Context(), src, dst, status, false)
	}
	if err != nil {
		s.renderRedirects(w, r, http.StatusUnprocessableEntity, form, s.t("A redirect for that path already exists."))
		return
	}
	s.redirects.reset()
	s.audit(r, uid(r), "redirect.saved", src, map[string]string{"target": dst, "status": strconv.Itoa(status)})
	s.redirect(w, r, "/admin/tools/redirects", "success", s.t("Redirect saved."))
}

func (s *Server) adminRedirectDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	if err := s.store.DeleteRedirect(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirects.reset()
	s.audit(r, uid(r), "redirect.deleted", strconv.FormatInt(id, 10), nil)
	s.redirect(w, r, "/admin/tools/redirects", "success", s.t("Redirect deleted."))
}

// postMoved keeps old links working after a published post or page changes
// address (slug, type or language).
func (s *Server) postMoved(ctx context.Context, before, after *store.Post) {
	if before == nil || !before.IsPublic(time.Now()) {
		return
	}
	oldPath, newPath := s.postURL(before), s.postURL(after)
	if oldPath == newPath {
		return
	}
	if err := s.store.MovePath(ctx, oldPath, newPath); err != nil {
		s.log.Error("auto redirect", "from", oldPath, "to", newPath, "err", err)
		return
	}
	s.redirects.reset()
}
