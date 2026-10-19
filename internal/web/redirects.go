package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

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
		m[redirectKey(r.Source)] = r
	}
	t.mu.Lock()
	t.bySrc, t.loaded = m, true
	t.mu.Unlock()
	rd, ok = m[path]
	return rd, ok
}

// redirectKey is the form a source is matched in: its path percent-decoded,
// like r.URL.Path (WordPress exports and pasted addresses escape it), and its
// query as sent.
func redirectKey(source string) string {
	p, q, hasQuery := strings.Cut(source, "?")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if hasQuery {
		return p + "?" + q
	}
	return p
}

// matchRedirect finds the rule for a request path (decoded) and raw query.
// The query is tried first, for old plain permalinks such as /?p=12, then the
// path with and without a trailing slash. A rule that would send the visitor
// back to the path they asked for (WordPress's /about/ -> /about) is ignored.
func matchRedirect(path, rawQuery string, lookup func(string) (store.Redirect, bool)) (store.Redirect, bool) {
	keys := make([]string, 0, 3)
	if rawQuery != "" {
		keys = append(keys, path+"?"+rawQuery)
	}
	keys = append(keys, path)
	if len(path) > 1 {
		if p, ok := strings.CutSuffix(path, "/"); ok {
			keys = append(keys, p)
		} else {
			keys = append(keys, path+"/")
		}
	}
	for _, k := range keys {
		rd, ok := lookup(k)
		if !ok {
			continue
		}
		if rd.Status != http.StatusGone && redirectKey(strings.SplitN(rd.Target, "?", 2)[0]) == path {
			continue
		}
		return rd, true
	}
	return store.Redirect{}, false
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
		rd, ok := matchRedirect(r.URL.Path, r.URL.RawQuery, func(key string) (store.Redirect, bool) {
			return s.lookupRedirect(r.Context(), key)
		})
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

var (
	errRedirectInvalid  = errors.New("invalid redirect")
	errRedirectReserved = errors.New("the site serves this address itself")
)

// checkRedirect validates a rule, from the redirects page or an import,
// and returns it normalised. Sources are local paths that are not
// reserved (errRedirectReserved); targets are local paths or http(s)
// URLs, which an import does not accept (wpSink).
func checkRedirect(source, target string, status int) (string, string, error) {
	source, target = strings.TrimSpace(source), strings.TrimSpace(target)
	if u, err := url.Parse(source); err == nil && u.IsAbs() {
		source = u.EscapedPath()
		if u.RawQuery != "" {
			source += "?" + u.RawQuery
		}
	}
	if !strings.HasPrefix(source, "/") || strings.HasPrefix(source, "//") || len(source) > 1000 ||
		strings.ContainsAny(source, " #\\") || strings.ContainsFunc(source, unicode.IsControl) || strings.Count(source, "?") > 1 {
		return "", "", errRedirectInvalid
	}
	if reservedRedirectSource(source) {
		return "", "", errRedirectReserved
	}
	if !slices.Contains([]int{301, 302, 307, 308, 410}, status) {
		return "", "", errRedirectInvalid
	}
	if status == http.StatusGone {
		return source, "", nil
	}
	if len(target) > 2000 || strings.ContainsRune(target, '\\') || strings.ContainsFunc(target, unicode.IsControl) || target == source {
		return "", "", errRedirectInvalid
	}
	if localRedirectTarget(target) {
		return source, target, nil
	}
	if !validHTTPURL(target) {
		return "", "", errRedirectInvalid
	}
	return source, target, nil
}

// localRedirectTarget reports whether target is a path of the site, also
// once percent-decoded: one leading slash, no backslash ("/\host" is
// "//host" to a browser).
func localRedirectTarget(target string) bool {
	local := func(s string) bool {
		return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && !strings.ContainsRune(s, '\\')
	}
	if !local(target) {
		return false
	}
	p, _, _ := strings.Cut(target, "?")
	p, _, _ = strings.Cut(p, "#")
	d, err := url.PathUnescape(p)
	return err == nil && local(d) && !strings.ContainsFunc(d, unicode.IsControl)
}

// reservedRoutes are the first path segments of the addresses the site
// serves itself (routes.go), which no redirect may take.
var reservedRoutes = map[string]bool{
	"admin": true, "login": true, "logout": true, "register": true, "setup": true, "forgot-password": true,
	"reset-password": true, "sso": true, "api": true, "mcp": true, "billing": true, "static": true, "themes": true,
	"uploads": true, "plugin-assets": true, "plugin-forms": true, "og": true, "healthz": true, "search": true,
	"ask": true, "feed": true, ".well-known": true, "robots.txt": true, "manifest.webmanifest": true,
}

// reservedRedirectSource reports whether a redirect from source would take
// an address the site serves itself: the home page and the language roots,
// sign-in and the other account pages, the admin, single sign-on, the APIs,
// feeds, sitemaps, robots.txt, llms.txt, /.well-known/, assets and uploads,
// the health check, search and the assistant, also under a language
// prefix. Redirects are matched before routing, so such a rule would
// hijack the route. The path is read the way requests match it: decoded,
// cleaned and in any case. A query on the home page or a language root
// (WordPress's plain permalinks, /?p=12) only matches that query, and is
// allowed.
func reservedRedirectSource(source string) bool {
	p, query, _ := strings.Cut(source, "?")
	if d, err := url.PathUnescape(p); err == nil {
		p = d
	}
	p = strings.ToLower(path.Clean("/" + p))
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if validLang(segs[0]) {
		segs = segs[1:]
	}
	if len(segs) == 0 || segs[0] == "" {
		return query == ""
	}
	first, last := segs[0], segs[len(segs)-1]
	_, firstFeed := feedTypes[first]
	_, lastFeed := feedTypes[last]
	return reservedRoutes[first] || firstFeed || lastFeed || (first == "blog" && len(segs) == 1) ||
		strings.HasPrefix(first, "sitemap") || (strings.HasPrefix(first, "llms") && strings.HasSuffix(first, ".txt"))
}

// sourceKey identifies the sources that the same requests match:
// matchRedirect tries a path with and without its trailing slash.
func sourceKey(source string) string {
	p, q, hasQuery := strings.Cut(redirectKey(source), "?")
	if len(p) > 1 {
		p = strings.TrimSuffix(p, "/")
	}
	if hasQuery {
		return p + "?" + q
	}
	return p
}

// contentAt reports whether a post or page answers at a redirect source.
// A source with a query only matches that query, and shadows no post.
func (s *Server) contentAt(ctx context.Context, source string) (bool, error) {
	if strings.Contains(source, "?") {
		return false, nil
	}
	lang, rest := s.mainLang(), strings.TrimPrefix(sourceKey(source), "/")
	if code, after, ok := strings.Cut(rest, "/"); ok && validLang(code) {
		lang, rest = code, after
	}
	typ, slug := store.TypePage, rest
	if post, ok := strings.CutPrefix(rest, "blog/"); ok {
		typ, slug = store.TypePost, post
	}
	if slug == "" || strings.Contains(slug, "/") {
		return false, nil
	}
	return s.store.SlugExists(ctx, typ, lang, slug, 0)
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
	src, dst, err := checkRedirect(form.Source, form.Target, status)
	switch {
	case errors.Is(err, errRedirectReserved):
		s.renderRedirects(w, r, http.StatusUnprocessableEntity, form,
			s.t("The site serves this address itself (its home page, sign-in, admin, feeds or files), so it cannot be redirected."))
		return
	case err != nil:
		s.renderRedirects(w, r, http.StatusUnprocessableEntity, form,
			s.t("Use a path starting with / as the source, and a path or an http(s) address as the target."))
		return
	}
	added := true
	if id, ok := pathID(r); ok {
		err = s.store.UpdateRedirect(r.Context(), id, src, dst, status)
	} else {
		added, err = s.store.AddRedirect(r.Context(), src, dst, status, false)
	}
	if err != nil || !added {
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
