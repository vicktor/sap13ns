package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/customize"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/ui"
)

// customEnv is a full server (real routing, sessions, CSRF, CSP and page
// cache) over a fresh test database, with an administrator and an author
// signed in.
type customEnv struct {
	t      *testing.T
	s      *Server
	st     *store.Store
	slug   string
	admin  *http.Cookie
	csrf   string
	author *http.Cookie
	acsrf  string
}

func newCustomEnv(t *testing.T) *customEnv {
	t.Helper()
	ctx := context.Background()
	st := testutil.Store(t)
	base, _ := url.Parse("http://example.test")
	cfg := &config.Config{BaseURL: base, SecretKey: []byte("0123456789abcdef0123456789abcdef"), DataDir: t.TempDir(),
		UploadsDir: t.TempDir(), PluginsDir: t.TempDir(), ThemesDir: t.TempDir(), MaxUploadBytes: 10 << 20,
		Dev: true, DevSkip2FA: true} // 2FA is mandatory for admins otherwise
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var s *Server
	host, err := plugin.NewHost(ctx, cfg.PluginsDir, "", st, log, func() plugin.SiteInfo { return s.SiteInfo() }, webLinks{&s})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close(context.Background()) })
	if s, err = New(ctx, Deps{Config: cfg, Log: log, Store: st, UI: ui.FS, Plugins: host}); err != nil {
		t.Fatal(err)
	}
	e := &customEnv{t: t, s: s, st: st, slug: s.themes.Active(s.settings().ActiveTheme).Slug}
	e.admin, e.csrf = e.login("admin", auth.RoleAdmin)
	e.author, e.acsrf = e.login("author", auth.RoleAuthor)
	return e
}

func (e *customEnv) login(name, role string) (*http.Cookie, string) {
	e.t.Helper()
	ctx := context.Background()
	id, err := e.st.CreateUser(ctx, store.NewUser{Username: name, Email: name + "@example.test", Role: role, PasswordHash: "x"})
	if err != nil {
		e.t.Fatal(err)
	}
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := e.st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: id, CSRFToken: csrf,
		ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: e.s.cookieName(), Value: token}, csrf
}

func (e *customEnv) do(req *http.Request, cookie *http.Cookie) *httptest.ResponseRecorder {
	req.RemoteAddr = "192.0.2.10:4000"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

func (e *customEnv) get(target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(httptest.NewRequest("GET", "http://example.test"+target, nil), cookie)
}

// draft saves a design as the draft the way customizer.js does.
func (e *customEnv) draft(design string) (*httptest.ResponseRecorder, draftResult) {
	e.t.Helper()
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/draft", strings.NewReader(design))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	rec := e.do(req, e.admin)
	var res draftResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func (e *customEnv) post(action string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{"csrf": {csrf}}
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize"+action, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, cookie)
}

func (e *customEnv) row() *store.ThemeCustom {
	e.t.Helper()
	row, err := e.st.ThemeCustom(context.Background(), e.slug, store.CustomDesign)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

func (e *customEnv) revisions() []store.CustomRevision {
	e.t.Helper()
	revs, err := e.st.CustomRevisions(context.Background(), e.slug, store.CustomDesign)
	if err != nil {
		e.t.Fatal(err)
	}
	return revs
}

var customLink = regexp.MustCompile(`<link rel="stylesheet" href="(/themes/[a-z0-9-]+/custom/[^"]+)" data-sap-custom>`)

func customCSS(body string) string {
	if m := customLink.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

func TestCustomizeDraftNeverReachesVisitors(t *testing.T) {
	e := newCustomEnv(t)

	// Nothing customised: no extra stylesheet, the theme's own look.
	rec := e.get("/", nil)
	if rec.Code != 200 || customCSS(rec.Body.String()) != "" {
		t.Fatalf("fresh site: %d, custom css %q", rec.Code, customCSS(rec.Body.String()))
	}

	rec, res := e.draft(`{"light": {"background": "#FDF6E3"}, "header": {"sticky": "off"}, "footer": {"text": "Draft footer"}}`)
	if rec.Code != 200 || !res.OK || !strings.HasPrefix(res.CSSURL, "/themes/"+e.slug+"/custom/draft-") || res.Classes != "sap-sticky-off" {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}

	// The administrator's preview renders the draft, uncached and framable
	// by the customiser only.
	rec = e.get("/?customize=draft", e.admin)
	body := rec.Body.String()
	if css := customCSS(body); !strings.HasPrefix(css, "/themes/"+e.slug+"/custom/draft-") {
		t.Errorf("preview links %q", css)
	}
	if !strings.Contains(body, "sap-sticky-off") || !strings.Contains(body, "Draft footer") {
		t.Error("preview does not show the draft")
	}
	if strings.Contains(body, `class="admin-bar"`) {
		t.Error("preview shows the admin bar")
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Frame-Options") != "SAMEORIGIN" || !strings.Contains(h.Get("Content-Security-Policy"), "frame-ancestors 'self'") {
		t.Errorf("preview headers: %v", h)
	}
	if !strings.Contains(body, "Powered by") || !strings.Contains(body, `href="https://sap13ns.site"`) {
		t.Error("custom footer text dropped the attribution")
	}

	// Visitors, with or without asking for the draft, see the published
	// (empty) design, before and after the page cache fills.
	for i, target := range []string{"/", "/", "/?customize=draft", "/?customize=draft&preview_theme=" + e.slug} {
		rec = e.get(target, nil)
		body := rec.Body.String()
		if customCSS(body) != "" || strings.Contains(body, "sap-sticky-off") || strings.Contains(body, "Draft footer") {
			t.Errorf("visitor %d (%s, X-Cache %s) sees the draft", i, target, rec.Header().Get("X-Cache"))
		}
	}
	if got := e.get("/", nil).Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("home page not cached for visitors: %q", got)
	}
	// An author cannot preview drafts either.
	if body := e.get("/?customize=draft", e.author).Body.String(); strings.Contains(body, "Draft footer") {
		t.Error("an author sees the draft")
	}
	// After the administrator previewed again, the cache still holds the
	// published page.
	e.get("/?customize=draft", e.admin)
	if rec := e.get("/", nil); strings.Contains(rec.Body.String(), "Draft footer") {
		t.Errorf("cached page (%s) holds the draft", rec.Header().Get("X-Cache"))
	}

	// The draft stylesheet is private.
	if rec := e.get(res.CSSURL, nil); rec.Code != 404 {
		t.Errorf("draft css for visitors: %d", rec.Code)
	}
	if rec := e.get(res.CSSURL, e.author); rec.Code != 404 {
		t.Errorf("draft css for an author: %d", rec.Code)
	}
	rec = e.get(res.CSSURL, e.admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "--sap-light-background: #fdf6e3;") || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("draft css for the admin: %d %v\n%s", rec.Code, rec.Header(), rec.Body)
	}

	// Publishing makes it public.
	if rec := e.post("/publish", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	rec = e.get("/", nil)
	body = rec.Body.String()
	css := customCSS(body)
	if !regexp.MustCompile(`^/themes/`+e.slug+`/custom/[0-9a-f]{16}\.css$`).MatchString(css) || !strings.Contains(body, "sap-sticky-off") || !strings.Contains(body, "Draft footer") {
		t.Fatalf("published page: css %q, sticky %v", css, strings.Contains(body, "sap-sticky-off"))
	}
	if e.row().Draft != nil {
		t.Error("publish kept the draft")
	}
	rec = e.get(css, nil)
	etag := rec.Header().Get("ETag")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || etag == "" ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") || !strings.Contains(rec.Body.String(), "#fdf6e3") {
		t.Errorf("published css: %d %v", rec.Code, rec.Header())
	}
	req := httptest.NewRequest("GET", "http://example.test"+css, nil)
	req.Header.Set("If-None-Match", etag)
	if rec := e.do(req, nil); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	if rec := e.get("/themes/"+e.slug+"/custom/0123456789abcdef.css", nil); rec.Code != http.StatusFound || rec.Header().Get("Location") != css {
		t.Errorf("outdated hash: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.get("/themes/no-such-theme/custom/0123456789abcdef.css", nil); rec.Code != 404 {
		t.Errorf("unknown theme: %d", rec.Code)
	}
}

func TestCustomizeValidationAndAccess(t *testing.T) {
	e := newCustomEnv(t)
	rec, res := e.draft(`{"css": "@import url(https://evil.example/x.css);", "light": {"background": "red"}, "scale": {"text": "500"}}`)
	if rec.Code != http.StatusUnprocessableEntity || res.OK {
		t.Fatalf("invalid draft accepted: %d %s", rec.Code, rec.Body)
	}
	fields := map[string]bool{}
	for _, fe := range res.Errors {
		fields[fe.Field] = true
	}
	for _, f := range []string{"css", "light.background", "scale.text"} {
		if !fields[f] {
			t.Errorf("no error for %s: %v", f, res.Errors)
		}
	}
	if e.row().Draft != nil {
		t.Error("an invalid design was stored")
	}
	if rec, _ := e.draft(`{"unknown": 1}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown field: %d", rec.Code)
	}

	// No CSRF token, no change.
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/draft", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := e.do(req, e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("draft without CSRF: %d", rec.Code)
	}
	// Authors cannot customise.
	if rec := e.get("/admin/appearance/themes/"+e.slug+"/customize", e.author); rec.Code != http.StatusForbidden {
		t.Errorf("author opens the customiser: %d", rec.Code)
	}
	if rec := e.post("/publish", e.author, e.acsrf); rec.Code != http.StatusForbidden {
		t.Errorf("author publishes: %d", rec.Code)
	}
	// The page renders, with the preview allowed in frame-src.
	rec = e.get("/admin/appearance/themes/"+e.slug+"/customize", e.admin)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-src 'self'") || !strings.Contains(rec.Body.String(), `data-customizer`) {
		t.Errorf("customiser page: %d %s", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := e.get("/admin/appearance/customize", e.admin); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/appearance/themes/"+e.slug+"/customize" {
		t.Errorf("Appearance → Customise: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestCustomizeRevisionsAndExport(t *testing.T) {
	e := newCustomEnv(t)
	colors := []string{"#100000", "#200000", "#300000", "#400000", "#500000", "#600000", "#700000", "#800000", "#900000", "#a00000", "#b00000", "#c00000"}
	for _, c := range colors {
		if rec, _ := e.draft(`{"light": {"primary": "` + c + `"}}`); rec.Code != 200 {
			t.Fatalf("draft %s: %d", c, rec.Code)
		}
		if rec := e.post("/publish", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
			t.Fatalf("publish %s: %d", c, rec.Code)
		}
	}
	revs := e.revisions()
	if len(revs) != store.MaxCustomRevisions {
		t.Fatalf("%d revisions kept, want %d", len(revs), store.MaxCustomRevisions)
	}
	if !strings.Contains(string(revs[0].Data), "#c00000") || !strings.Contains(string(revs[len(revs)-1].Data), "#300000") {
		t.Errorf("revisions not newest first: %s … %s", revs[0].Data, revs[len(revs)-1].Data)
	}
	// Publishing without a draft adds nothing.
	e.post("/publish", e.admin, e.csrf)
	if n := len(e.revisions()); n != store.MaxCustomRevisions {
		t.Errorf("empty publish added a revision: %d", n)
	}

	// Restore copies an old version into the draft; visitors keep the live one.
	old := revs[4]
	if rec := e.post("/revisions/"+itoa(old.ID)+"/restore", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", rec.Code)
	}
	if d := string(e.row().Draft); !strings.Contains(d, "#800000") {
		t.Errorf("restored draft: %s", d)
	}
	if !strings.Contains(e.get(customCSS(e.get("/", nil).Body.String()), nil).Body.String(), "#c00000") {
		t.Error("restore changed the live design")
	}

	// Discard drops the draft.
	if rec := e.post("/discard", e.admin, e.csrf); rec.Code != http.StatusSeeOther || e.row().Draft != nil {
		t.Errorf("discard: %d, draft %s", rec.Code, e.row().Draft)
	}

	// Export → import round trip (into the draft).
	e.draft(`{"light": {"primary": "#123456"}, "scale": {"radius": "large"}, "home": {"layout": "list", "per_page": 4}}`)
	rec := e.get("/admin/appearance/themes/"+e.slug+"/customize/export", e.admin)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "sap13ns-"+e.slug+"-design.json") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	var exp designExport
	if err := json.Unmarshal(rec.Body.Bytes(), &exp); err != nil || exp.Format != exportFormat || exp.Design == nil || exp.Design.Light["primary"] != "#123456" {
		t.Fatalf("export file: %v %s", err, rec.Body)
	}
	e.post("/discard", e.admin, e.csrf)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ := mw.CreateFormFile("file", "design.json")
	fw.Write(rec.Body.Bytes())
	mw.Close()
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if rec := e.do(req, e.admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	d := string(e.row().Draft)
	var got customize.Design
	json.Unmarshal(e.row().Draft, &got)
	if got.Light["primary"] != "#123456" || got.Scale.Radius != "large" || got.Home.PerPage != 4 || got.Home.Layout != "list" {
		t.Errorf("imported draft: %s", d)
	}
	// A broken file is refused and leaves the draft alone.
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ = mw.CreateFormFile("file", "design.json")
	fw.Write([]byte(`{"design": {"css": "a { background: url(https://evil.example/x.png) }"}}`))
	mw.Close()
	req = httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	e.do(req, e.admin)
	if string(e.row().Draft) != d {
		t.Error("a refused import changed the draft")
	}

	// Reset starts a draft with the theme's defaults.
	if rec := e.post("/reset", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("reset: %d", rec.Code)
	}
	if d := string(e.row().Draft); strings.Contains(d, "#123456") || strings.Contains(d, "light") {
		t.Errorf("reset draft: %s", d)
	}
	// Each action is audited.
	for _, a := range []string{"theme.design_published", "theme.design_restored", "theme.design_discarded", "theme.design_imported", "theme.design_reset"} {
		if _, n, _ := e.st.ListAudit(context.Background(), a, 1, 0); n == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}

func TestCustomizeDerive(t *testing.T) {
	e := newCustomEnv(t)
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/derive", strings.NewReader(`{"light": {"background": "#fdf6e3", "primary": "#b58900"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	rec := e.do(req, e.admin)
	var res struct {
		OK   bool              `json:"ok"`
		Dark map[string]string `json:"dark"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK || len(res.Dark) != 11 {
		t.Fatalf("derive: %d %s", rec.Code, rec.Body)
	}
	if e.row().Draft != nil {
		t.Error("derive saved a draft")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
