package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/ui"
)

func TestMergeCSP(t *testing.T) {
	base := "default-src 'self'; img-src 'self' data: https:; frame-src 'none'; frame-ancestors 'none'"
	if got := mergeCSP(base, nil); got != base {
		t.Fatalf("no additions changed the policy: %s", got)
	}
	got := mergeCSP(base, map[string][]string{
		"frame-src":  {"https://www.youtube-nocookie.com", "https://player.vimeo.com"},
		"img-src":    {"https://i.ytimg.com", "data:"},
		"worker-src": {"https://cdn.example.com"},
	})
	want := "default-src 'self'; img-src 'self' data: https: https://i.ytimg.com; frame-src https://www.youtube-nocookie.com https://player.vimeo.com; " +
		"frame-ancestors 'none'; worker-src 'self' https://cdn.example.com"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestValidateFormFields(t *testing.T) {
	f := &plugin.FormDecl{ID: "f", Fields: []plugin.FormField{
		{Name: "name", Type: "text", Required: true, MaxLength: 5},
		{Name: "email", Type: "email", Required: true},
		{Name: "site", Type: "url"},
		{Name: "phone", Type: "tel"},
		{Name: "age", Type: "number"},
		{Name: "topic", Type: "select", Choices: []string{"a", "b"}},
		{Name: "terms", Type: "checkbox", Required: true},
		{Name: "msg", Type: "textarea"},
	}}
	T := func(msg string, args ...any) string { return msg }
	out, errs := validateFormFields(f, map[string]string{
		"name": " Ada\x00 ", "email": "ada@example.test", "site": "https://ada.example", "phone": "+34 600 000 000",
		"age": "36", "topic": "b", "terms": "true", "msg": "line 1\r\nline\x07 2\t ",
	}, T)
	if len(errs) != 0 || out["name"] != "Ada" || out["msg"] != "line 1\nline 2" {
		t.Fatalf("valid input: %v %q", errs, out)
	}
	_, errs = validateFormFields(f, map[string]string{
		"name": "Ada Lovelace", "email": "Ada <ada@example.test>", "site": "javascript:alert(1)", "phone": "call me",
		"age": "old", "topic": "c", "terms": "false",
	}, T)
	for _, field := range []string{"name", "email", "site", "phone", "age", "topic", "terms"} {
		if errs[field] == "" {
			t.Errorf("%s: no error", field)
		}
	}
	if _, errs := validateFormFields(f, map[string]string{"terms": "true"}, T); errs["name"] == "" || errs["email"] == "" || len(errs) != 2 {
		t.Errorf("required: %v", errs)
	}
}

func TestSafePublicPath(t *testing.T) {
	for in, want := range map[string]string{
		"/blog/hola?x=1": "/blog/hola?x=1", "": "/", "https://evil.example/": "/", "//evil.example": "/",
		"/admin/plugins": "/", "/plugin-forms/a/b": "/", `/\evil`: "/", "/es/contacto": "/es/contacto",
	} {
		if got := safePublicPath(in); got != want {
			t.Errorf("safePublicPath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := setQuery("/blog/hola?x=1&pf=old#c", "pf", "p.f"); got != "/blog/hola?pf=p.f&x=1" {
		t.Errorf("setQuery = %q", got)
	}
}

func TestFormTokens(t *testing.T) {
	s := &Server{keys: auth.NewKeys([]byte("0123456789abcdef0123456789abcdef"))}
	use := func(key, tok string) (time.Duration, bool) {
		nonce, age, ok := s.checkFormToken(key, tok)
		return age, ok && s.spendFormNonce(nonce)
	}
	tok := s.newFormToken("pp.f")
	if _, age, ok := s.checkFormToken("pp.f", tok); !ok || age > time.Second {
		t.Fatalf("fresh token: %v %s", ok, age)
	}
	if age, ok := use("pp.f", tok); !ok || age > time.Second {
		t.Fatalf("checked token not usable: %v %s", ok, age)
	}
	if _, ok := use("pp.f", tok); ok {
		t.Fatal("a token was accepted twice")
	}
	if _, ok := use("other.f", s.newFormToken("pp.f")); ok {
		t.Fatal("a token was accepted for another form")
	}
	tok = s.newFormToken("pp.f")
	for _, bad := range []string{"", "x~y", tok[:len(tok)-2] + "zz", strings.Replace(tok, tok[:4], "0000", 1)} {
		if _, ok := use("pp.f", bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	body := s.fillFormTokens([]byte(`<input value="` + formTokenSlot + `pp.f"><input value="` + formTokenSlot + `pp.f">`))
	m := regexp.MustCompile(`value="([^"]+)"`).FindAllSubmatch(body, -1)
	if len(m) != 2 || bytes.Equal(m[0][1], m[1][1]) || bytes.Contains(body, []byte(formTokenSlot)) {
		t.Fatalf("filled body %s", body)
	}
	if _, ok := use("pp.f", string(m[1][1])); !ok {
		t.Fatal("filled token rejected")
	}

	// A full generation is rotated out instead of refusing new nonces.
	spent := fmt.Sprintf("%024d", 0)
	for i := range maxNonces {
		if !s.spendFormNonce(fmt.Sprintf("%024d", i)) {
			t.Fatalf("nonce %d refused", i)
		}
	}
	if !s.spendFormNonce("n-after-a-full-generation") {
		t.Fatal("a new nonce was refused once the table was full")
	}
	if s.spendFormNonce(spent) {
		t.Fatal("a nonce of the previous generation was accepted again")
	}
}

func TestIPKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":                   "203.0.113.7",
		"::ffff:203.0.113.7":            "203.0.113.7",
		"2001:db8:1:2:3:4:5:6":          "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:1": "2001:db8:1:2::/64",
	} {
		if got := ipKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("ipKey(%s) = %s, want %s", in, got, want)
		}
	}
}

// ------------------------------------------------------------ end to end --

type webMailer struct {
	mu   sync.Mutex
	sent []string
}

func (m *webMailer) SendMessage(to, replyTo, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, to+" "+replyTo+" "+subject)
	return nil
}

func (m *webMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sent)
}

type pluginEnv struct {
	t       *testing.T
	s       *Server
	st      *store.Store
	host    *plugin.Host
	mailer  *webMailer
	cookie  *http.Cookie
	csrf    string
	adminID int64
}

type webLinks struct{ s **Server }

func (l webLinks) PostInfo(p *store.Post) plugin.PostInfo { return (*l.s).PostInfo(p) }

func newPluginEnv(t *testing.T) *pluginEnv {
	t.Helper()
	ctx := context.Background()
	st := testutil.Store(t)
	base, _ := url.Parse("http://example.test")
	cfg := &config.Config{BaseURL: base, SecretKey: []byte("0123456789abcdef0123456789abcdef"), DataDir: t.TempDir(),
		UploadsDir: t.TempDir(), PluginsDir: t.TempDir(), ThemesDir: t.TempDir(), MaxUploadBytes: 10 << 20,
		Dev: true, DevSkip2FA: true}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var s *Server
	host, err := plugin.NewHost(ctx, cfg.PluginsDir, "", st, log, func() plugin.SiteInfo { return s.SiteInfo() }, webLinks{&s})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close(context.Background()) })
	mailer := &webMailer{}
	host.SetMailer(mailer)
	host.SetSecrets(auth.NewKeys(cfg.SecretKey))
	if s, err = New(ctx, Deps{Config: cfg, Log: log, Store: st, UI: ui.FS, Plugins: host}); err != nil {
		t.Fatal(err)
	}
	if err := host.Load(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser(ctx, store.NewUser{Username: "admin", Email: "admin@example.test", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: id, CSRFToken: csrf,
		ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	return &pluginEnv{t: t, s: s, st: st, host: host, mailer: mailer, cookie: &http.Cookie{Name: s.cookieName(), Value: token}, csrf: csrf, adminID: id}
}

func (e *pluginEnv) do(method, target string, body io.Reader, ctype string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, "http://example.test"+target, body)
	req.RemoteAddr = "192.0.2.10:4000"
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// admin posts an urlencoded admin form with the session and CSRF token.
func (e *pluginEnv) admin(target string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	form.Set("csrf", e.csrf)
	return e.do("POST", target, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", e.cookie)
}

func (e *pluginEnv) upload(name string, pkg []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ := mw.CreateFormFile("package", name)
	fw.Write(pkg)
	mw.Close()
	return e.do("POST", "/admin/plugins/upload", &buf, mw.FormDataContentType(), e.cookie)
}

func (e *pluginEnv) audited(action string) int {
	e.t.Helper()
	_, n, err := e.st.ListAudit(context.Background(), action, 50, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// hooksPackage builds internal/plugin/testdata/hooks and packs it with a
// stylesheet and a CSP addition.
func hooksPackage(t *testing.T) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	out := filepath.Join(t.TempDir(), "plugin.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", out, "./internal/plugin/testdata/hooks")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	wasm, _ := os.ReadFile(out)
	raw, _ := os.ReadFile(filepath.Join(root, "internal", "plugin", "testdata", "hooks", "plugin.json"))
	var m map[string]any
	json.Unmarshal(raw, &m)
	m["assets"] = map[string]any{"css": []string{"assets/hooks.css"}}
	m["csp"] = map[string]any{"frame-src": []string{"https://www.youtube-nocookie.com"}}
	manifest, _ := json.Marshal(m)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct {
		name string
		body []byte
	}{{"plugin.json", manifest}, {"plugin.wasm", wasm}, {"assets/hooks.css", []byte(".plugin-form{gap:1rem}")},
		{"README.md", []byte("# Hooks\n")}, {"LICENSE", []byte("MIT\n")}} {
		w, _ := zw.Create(f.name)
		w.Write(f.body)
	}
	zw.Close()
	return buf.Bytes()
}

var tokenRe = regexp.MustCompile(`name="_token" value="([^"]+)"`)

func TestPluginsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	e := newPluginEnv(t)
	ctx := context.Background()

	// The admin list offers the upload form.
	if rec := e.do("GET", "/admin/plugins", nil, "", e.cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="package"`) {
		t.Fatalf("plugins page: %d", rec.Code)
	}
	if rec := e.do("GET", "/admin/plugins", nil, ""); rec.Code != http.StatusSeeOther {
		t.Fatalf("plugins page without a session: %d", rec.Code)
	}

	// A hostile archive is refused with its report, and nothing is installed.
	bad, err := os.ReadFile(filepath.Join("..", "plugin", "testdata", "archives", "zip-slip.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if rec := e.upload("evil.zip", bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "package.zip-slip") {
		t.Fatalf("zip-slip upload: %d", rec.Code)
	}
	if e.audited("plugin.refused") != 1 || len(e.host.List()) != 0 {
		t.Fatal("refused upload not audited, or installed")
	}
	if rec := e.upload("notes.txt", []byte("hello")); rec.Code != http.StatusSeeOther || len(e.host.List()) != 0 {
		t.Fatalf("non-zip upload: %d", rec.Code)
	}

	// Upload: installed disabled, with its provenance.
	rec := e.upload("hooks-1.0.0.zip", hooksPackage(t))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/plugins/hooks" {
		t.Fatalf("upload: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	p := e.host.Get("hooks")
	if p == nil || p.Status != plugin.StatusAvailable || p.Record.Source != "upload" || *p.Record.UploadedBy != e.adminID || e.audited("plugin.installed") != 1 {
		t.Fatalf("after upload: %+v", p)
	}
	if rec := e.do("GET", "/admin/plugins/hooks", nil, "", e.cookie); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="confirm_risk"`) {
		t.Fatalf("plugin page: %d", rec.Code)
	}

	// High-risk permissions (the CSP addition) need the confirmation box.
	rec = e.admin("/admin/plugins/hooks/approve", url.Values{"pin": {p.ReviewPin()}})
	if rec.Code != http.StatusSeeOther || e.host.Get("hooks").Status != plugin.StatusAvailable {
		t.Fatalf("approve without confirmation: %d %s", rec.Code, e.host.Get("hooks").Status)
	}
	if rec := e.admin("/admin/plugins/hooks/approve", url.Values{"pin": {strings.Repeat("0", 64)}, "confirm_risk": {"1"}}); e.host.Get("hooks").Status != plugin.StatusAvailable {
		t.Fatalf("approved another build: %d", rec.Code)
	}
	rec = e.admin("/admin/plugins/hooks/approve", url.Values{"pin": {p.ReviewPin()}, "confirm_risk": {"1"}, "back": {"list"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/plugins" || e.host.Get("hooks").Status != plugin.StatusEnabled {
		t.Fatalf("approve: %d %s %s", rec.Code, rec.Header().Get("Location"), e.host.Get("hooks").Status)
	}
	if e.audited("plugin.approved") != 1 {
		t.Fatal("approval not audited")
	}

	// A public page with the plugin's form.
	at := time.Now().Add(-time.Hour)
	postID, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Contact", Slug: "contact",
		Lang: e.s.mainLang(), Content: "Write to us.\n\n[forms]", AuthorID: e.adminID, CommentStatus: "open", PublishedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	post, err := e.st.PostByID(ctx, postID)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(e.s.PostInfo(post).URL)
	page := func(cookies ...*http.Cookie) (*httptest.ResponseRecorder, string) {
		t.Helper()
		rec := e.do("GET", pu.Path, nil, "", cookies...)
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d", pu.Path, rec.Code)
		}
		return rec, rec.Body.String()
	}
	rec, body := page()
	for _, want := range []string{`action="/plugin-forms/hooks/contact"`, `id="form-hooks-contact"`, `name="_hp"`,
		`<script type="application/ld+json">`, `\u003c/script\u003e`, `<meta name="author" content="Ada">`, `/plugin-assets/hooks/`,
		`<meta name="keywords" content="published=true updated=true tags=[]">`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s", want)
		}
	}
	if strings.Contains(body, "BlogPosting") {
		t.Error("the host's JSON-LD was written next to the plugin's")
	}
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, formTokenSlot) || strings.Contains(body, `data-form="hooks.contact"`) {
		t.Fatal("unsafe or unexpanded output on the page")
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src https://www.youtube-nocookie.com") {
		t.Fatalf("public CSP: %s", csp)
	}
	if csp := e.do("GET", "/admin/plugins", nil, "", e.cookie).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src 'none'") {
		t.Fatalf("admin CSP: %s", csp)
	}

	// The stylesheet is served immutable from the site.
	css := regexp.MustCompile(`href="(/plugin-assets/hooks/[^"]+)"`).FindStringSubmatch(body)
	if css == nil {
		t.Fatal("no stylesheet link")
	}
	rec = e.do("GET", css[1], nil, "")
	if rec.Code != 200 || rec.Body.String() != ".plugin-form{gap:1rem}" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("asset: %d %v", rec.Code, rec.Header())
	}
	if rec := e.do("GET", "/plugin-assets/hooks/0000000000000000/assets/hooks.css", nil, ""); rec.Code != 404 {
		t.Fatalf("stale asset version: %d", rec.Code)
	}

	// Form submissions.
	_, b1 := page()
	_, b2 := page() // a cached page gets a new token each time
	tok1, tok2 := tokenRe.FindStringSubmatch(b1), tokenRe.FindStringSubmatch(b2)
	if tok1 == nil || tok2 == nil || tok1[1] == tok2[1] {
		t.Fatal("no fresh tokens on the page")
	}
	fetched := time.Now()
	submit := func(fields url.Values, ctype string, headers ...string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "http://example.test/plugin-forms/hooks/contact", strings.NewReader(fields.Encode()))
		req.RemoteAddr = "198.51.100.7:5000"
		req.Header.Set("Content-Type", ctype)
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	outcome := func(rec *httptest.ResponseRecorder) string {
		t.Helper()
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusSeeOther || !strings.HasSuffix(loc, "pf=hooks.contact#form-hooks-contact") {
			t.Fatalf("submission: %d %s", rec.Code, loc)
		}
		var flash *http.Cookie
		for _, c := range rec.Result().Cookies() {
			if c.Name == formCookie {
				flash = c
			}
		}
		u, _ := url.Parse(loc)
		got := e.do("GET", u.RequestURI(), nil, "", flash)
		return got.Body.String()
	}
	fields := func(tok string) url.Values {
		return url.Values{"_token": {tok}, "_back": {pu.Path}, "_lang": {e.s.mainLang()}, "email": {"visitor@example.test"}}
	}
	form := "application/x-www-form-urlencoded"
	// The messages are in the form's language, whatever the test site uses.
	tr := func(key string) string { return e.s.i18n.T(e.s.mainLang(), key) }
	if body := outcome(submit(fields(tok1[1]), form)); !strings.Contains(body, tr("That was too fast. Wait a moment and send the form again.")) {
		t.Fatal("a form sent at once was accepted")
	}
	if rec := submit(fields(tok2[1]), "multipart/form-data; boundary=x"); rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("multipart: %d", rec.Code)
	}
	if rec := submit(fields(tok2[1]), form, "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site: %d", rec.Code)
	}
	if rec := e.do("POST", "/plugin-forms/hooks/nope", strings.NewReader("a=1"), form); rec.Code != 404 {
		t.Fatalf("unknown form: %d", rec.Code)
	}
	hp := fields("")
	hp.Set("_hp", "http://spam.example")
	if body := outcome(submit(hp, form)); !strings.Contains(body, tr("Thanks! Your message was sent.")) || e.mailer.count() != 0 {
		t.Fatal("honeypot")
	}
	time.Sleep(time.Until(fetched.Add(formMinAge + 100*time.Millisecond)))
	if body := outcome(submit(fields(tok2[1]), form)); !strings.Contains(body, "sent 1 in "+e.s.mainLang()) {
		t.Fatalf("submission result not shown")
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.mailer.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.mailer.count() != 1 || e.mailer.sent[0] != "admin@example.test visitor@example.test Contact" {
		t.Fatalf("mails %v", e.mailer.sent)
	}
	if body := outcome(submit(fields(tok2[1]), form)); !strings.Contains(body, tr("This form expired. Check the fields and send it again.")) {
		t.Fatal("a token was used twice")
	}

	// comment.check: the plugin holds comments that say "hold", even with
	// moderation off, and never sees the commenter's e-mail address (it
	// would answer spam).
	settings := e.s.settings()
	settings.CommentModeration = "none"
	if err := e.st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	e.s.ReloadSettings(ctx)
	ctok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(b1)
	if ctok == nil {
		t.Fatal("no comment token")
	}
	for _, text := range []string{"Great post", "Please hold this one"} {
		f := url.Values{"token": {ctok[1]}, "name": {"Visitor"}, "email": {"visitor@example.test"}, "content": {text}}
		if rec := e.do("POST", pu.Path+"/comments", strings.NewReader(f.Encode()), form); rec.Code != http.StatusSeeOther {
			t.Fatalf("comment: %d", rec.Code)
		}
	}
	for status, want := range map[string]string{"approved": "Great post", "pending": "Please hold this one"} {
		list, _, err := e.st.ListComments(ctx, store.CommentQuery{Status: status, PostID: postID, Limit: 10})
		if err != nil || len(list) != 1 || list[0].Content != want {
			t.Fatalf("%s comments: %v %+v", status, err, list)
		}
	}

	// Disable, then uninstall with its data.
	if rec := e.admin("/admin/plugins/hooks/uninstall", url.Values{"delete_data": {"1"}}); e.host.Get("hooks") == nil {
		t.Fatalf("uninstalled an enabled plugin: %d", rec.Code)
	}
	e.admin("/admin/plugins/hooks/disable", url.Values{})
	if _, body := page(); strings.Contains(body, "plugin-form") {
		t.Fatal("form still shown after disabling")
	}
	if rec := e.do("GET", css[1], nil, ""); rec.Code != 404 {
		t.Fatalf("asset of a disabled plugin: %d", rec.Code)
	}
	e.admin("/admin/plugins/hooks/uninstall", url.Values{"delete_data": {"1"}})
	if e.host.Get("hooks") != nil || e.audited("plugin.uninstalled") != 1 || e.audited("plugin.disabled") != 1 {
		t.Fatal("uninstall")
	}
	if _, err := os.Stat(filepath.Join(e.s.cfg.PluginsDir, "hooks")); !os.IsNotExist(err) {
		t.Fatal("plugin files left behind")
	}
}
