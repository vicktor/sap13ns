package web

import (
	"bytes"
	"html/template"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"sap13ns/internal/i18n"
)

// StatusPages are the pages a network serves on a host without a running
// site (ui/network/templates/status.html), with the assets of ui/static
// they use. They need no Server: a suspended site is never loaded.
type StatusPages struct {
	ui     fs.FS
	static map[string]string // file -> content hash
	i18n   *i18n.Catalogs
	tmpl   *template.Template
}

// Status page kinds.
const (
	StatusUnknown   = "unknown"   // 404: no site at this host
	StatusPending   = "pending"   // 503: waiting for the network's approval
	StatusSuspended = "suspended" // 503: suspended by a network admin
	StatusBusy      = "busy"      // 503: the site cannot be loaded right now
)

func NewStatusPages(ui fs.FS) (*StatusPages, error) {
	p := &StatusPages{ui: ui, static: hashFiles(ui, "static")}
	var err error
	if p.i18n, err = i18n.Load(ui, "i18n"); err != nil {
		return nil, err
	}
	p.tmpl, err = template.New("status.html").Funcs(template.FuncMap{
		"T":     func(string, ...any) string { return "" }, // bound per request
		"asset": p.assetURL,
	}).ParseFS(ui, "network/templates/status.html")
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (p *StatusPages) assetURL(file string) string {
	if h, ok := p.static[file]; ok {
		return "/static/" + file + "?v=" + h
	}
	return "/static/" + file
}

// Serve renders the page of kind with status, in the first language of
// the visitor's that the UI has. https adds upgrade-insecure-requests to
// the policy, as on a site.
func (p *StatusPages) Serve(w http.ResponseWriter, r *http.Request, kind string, status int, https bool) {
	lang := p.language(r.Header.Get("Accept-Language"))
	t, err := p.tmpl.Clone()
	if err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	t.Funcs(template.FuncMap{"T": func(key string, args ...any) string { return p.i18n.T(lang, key, args...) }})
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]any{"Kind": kind, "Status": status, "Lang": lang}); err != nil {
		http.Error(w, http.StatusText(status), status)
		return
	}
	h := w.Header()
	policy := csp
	if https {
		policy += "; upgrade-insecure-requests"
	}
	h.Set("Content-Security-Policy", policy)
	setStaticHeaders(h)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	h.Set("Content-Language", lang)
	h.Add("Vary", "Accept-Language")
	switch kind {
	case StatusBusy:
		h.Set("Retry-After", "5")
	case StatusPending, StatusSuspended:
		h.Set("Retry-After", "3600")
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(buf.Bytes())
	}
}

// ServeStatic serves a file of ui/static for these pages (GET and HEAD
// /static/...); anything else is a 404.
func (p *StatusPages) ServeStatic(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/static/")
	name := path.Clean("/" + rest)[1:]
	hash, known := p.static[name]
	if !ok || !known || r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	f, err := p.ui.Open("static/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	setStaticHeaders(w.Header())
	serveAsset(w, r, f, name, hash)
}

// setStaticHeaders sets the headers securityHeaders gives every response
// of a site, except its policy.
func setStaticHeaders(h http.Header) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), browsing-topics=(), interest-cohort=()")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
}

// language picks the first catalog language the visitor accepts, ignoring
// weights (browsers list languages by preference), else English.
func (p *StatusPages) language(accept string) string {
	for part := range strings.SplitSeq(accept, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		base, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if base != "" && p.i18n.Has(base) {
			return base
		}
	}
	return "en"
}
