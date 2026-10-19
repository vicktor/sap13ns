package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// oauthFake is the provider of the "demo" connection of
// internal/plugin/testdata/oauth.
type oauthFake struct {
	mu    sync.Mutex
	codes int
}

func (f *oauthFake) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	r.ParseForm()
	switch r.URL.Host + r.URL.Path {
	case "auth.example.test/token":
		if id, secret, _ := r.BasicAuth(); id != "client-1" || secret != "secret-1" || r.PostForm.Get("code") != "good" ||
			r.PostForm.Get("code_verifier") == "" {
			rec.WriteHeader(400)
			rec.WriteString(`{"error":"invalid_grant"}`)
			break
		}
		f.codes++
		rec.WriteString(`{"access_token":"web-at","refresh_token":"web-rt","expires_in":3600,"token_type":"bearer"}`)
	case "api.example.test/userinfo":
		rec.WriteString(`{"sub":"u-1","name":"Ada Lovelace"}`)
	default:
		rec.WriteHeader(404)
	}
	return rec.Result(), nil
}

func (e *pluginEnv) installOAuthFixture() *oauthFake {
	e.t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dst := filepath.Join(e.s.cfg.PluginsDir, "oauthdemo")
	os.MkdirAll(dst, 0o755)
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", filepath.Join(dst, "plugin.wasm"), "./internal/plugin/testdata/oauth")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.t.Fatalf("build: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "internal", "plugin", "testdata", "oauth", "plugin.json"))
	os.WriteFile(filepath.Join(dst, "plugin.json"), raw, 0o644)
	ctx := context.Background()
	if err := e.host.Load(ctx); err != nil {
		e.t.Fatal(err)
	}
	if err := e.host.Approve(ctx, "oauthdemo", "", e.adminID); err != nil {
		e.t.Fatal(err)
	}
	if err := e.host.SaveSettings(ctx, "oauthdemo", map[string]string{"client_id": "client-1", "client_secret": "secret-1"}); err != nil {
		e.t.Fatal(err)
	}
	fake := &oauthFake{}
	e.host.SetOAuthTransport(fake)
	return fake
}

// flashOf decodes the flash message a response set.
func flashOf(rec *httptest.ResponseRecorder) string {
	for _, c := range rec.Result().Cookies() {
		if c.Name == flashCookie {
			val, _, _ := strings.Cut(c.Value, "~")
			raw, _ := base64.RawURLEncoding.DecodeString(val)
			var list []flash
			json.Unmarshal(raw, &list)
			if len(list) > 0 {
				return list[0].Kind + ": " + list[0].Text
			}
		}
	}
	return ""
}

func TestPluginOAuthWeb(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	e := newPluginEnv(t)
	fake := e.installOAuthFixture()
	ctx := context.Background()
	base := "/admin/plugins/oauthdemo/oauth/demo"

	// The page shows the provider, the exact redirect URI and the Connect
	// form, which may post on to the provider.
	rec := e.do("GET", "/admin/plugins/oauthdemo", nil, "", e.cookie)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `id="connections"`) || !strings.Contains(body, `value="http://example.test`+base+`/callback"`) ||
		!strings.Contains(body, `action="`+base+`/connect"`) || !strings.Contains(body, "Sin conectar.") {
		t.Fatalf("plugin page: %d\n%s", rec.Code, body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://auth.example.test;") {
		t.Fatalf("form-action: %s", csp)
	}
	// (The test site speaks Spanish.)
	if got := e.s.permHelpFor(nil, &e.host.Get("oauthdemo").Manifest, "oauth:demo"); got != "Usar la cuenta de Demo que conectas en su página, con estos ámbitos: openid, profile, w_member_social." {
		t.Fatal("the permission is not described")
	}

	// The POSTs need the CSRF token.
	for _, action := range []string{"/connect", "/disconnect"} {
		if rec := e.do("POST", base+action, strings.NewReader(""), "application/x-www-form-urlencoded", e.cookie); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without CSRF: %d", action, rec.Code)
		}
	}

	connect := func() (state string) {
		t.Helper()
		rec := e.admin(base+"/connect", url.Values{})
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if rec.Code != http.StatusSeeOther || loc == nil || loc.Host != "auth.example.test" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("connect: %d %s %s", rec.Code, rec.Header().Get("Location"), flashOf(rec))
		}
		if loc.Query().Get("redirect_uri") != "http://example.test"+base+"/callback" {
			t.Fatalf("redirect_uri %s", loc.Query().Get("redirect_uri"))
		}
		return loc.Query().Get("state")
	}
	callback := func(q url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		return e.do("GET", base+"/callback?"+q.Encode(), nil, "", cookie)
	}
	refused := func(rec *httptest.ResponseRecorder, what string) {
		t.Helper()
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/plugins/oauthdemo#connections" ||
			!strings.HasPrefix(flashOf(rec), "error: ") {
			t.Fatalf("%s: %d %s %q", what, rec.Code, rec.Header().Get("Location"), flashOf(rec))
		}
		if list, _ := e.st.PluginOAuthConnections(ctx, "oauthdemo"); len(list) != 0 {
			t.Fatalf("%s connected", what)
		}
	}

	// A forged state, another session's state, and a refusal at the
	// provider are refused, and a refused state is gone.
	state := connect()
	refused(callback(url.Values{"state": {"x" + state}, "code": {"good"}}, e.cookie), "forged state")
	otherID, _ := e.st.CreateUser(ctx, store.NewUser{Username: "other", Email: "other@example.test", Role: "admin", PasswordHash: "x"})
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	e.st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: otherID, CSRFToken: csrf, ExpiresAt: time.Now().Add(time.Hour)})
	refused(callback(url.Values{"state": {state}, "code": {"good"}}, &http.Cookie{Name: e.s.cookieName(), Value: token}), "another session")
	refused(callback(url.Values{"state": {state}, "error": {"access_denied"}, "error_description": {"The user denied your request"}}, e.cookie),
		"denied at the provider")
	refused(callback(url.Values{"state": {state}, "code": {"good"}}, e.cookie), "a state after its refusal")
	if fake.codes != 0 || e.audited("plugin.oauth_failed") != 4 {
		t.Fatalf("codes %d, failures %d", fake.codes, e.audited("plugin.oauth_failed"))
	}

	// Connected.
	state = connect()
	rec = callback(url.Values{"state": {state}, "code": {"good"}}, e.cookie)
	if rec.Code != http.StatusSeeOther || flashOf(rec) != "success: Conectado a Demo como Ada Lovelace." ||
		rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("callback: %d %q %v", rec.Code, flashOf(rec), rec.Header())
	}
	if e.audited("plugin.oauth_connected") != 1 || fake.codes != 1 {
		t.Fatal("not audited")
	}
	refused2 := callback(url.Values{"state": {state}, "code": {"good"}}, e.cookie)
	if !strings.HasPrefix(flashOf(refused2), "error: ") || fake.codes != 1 {
		t.Fatalf("replay: %q", flashOf(refused2))
	}
	body = e.do("GET", "/admin/plugins/oauthdemo", nil, "", e.cookie).Body.String()
	if !strings.Contains(body, "Ada Lovelace") || !strings.Contains(body, "Volver a conectar") || strings.Contains(body, "web-at") ||
		strings.Contains(body, "web-rt") {
		t.Fatal("the page does not show the connection, or shows a token")
	}

	// An ending connection that cannot renew itself is announced on the
	// dashboard and the Plugins page.
	e.st.DB.Exec(ctx, `UPDATE plugin_oauth SET refreshable = false, expires_at = now() + interval '2 days'`)
	for _, page := range []string{"/admin/", "/admin/plugins"} {
		body := e.do("GET", page, nil, "", e.cookie).Body.String()
		if !strings.Contains(body, `id="oauth-alerts"`) || !strings.Contains(body, `href="/admin/plugins/oauthdemo#connections"`) {
			t.Fatalf("%s has no notice", page)
		}
	}
	if body := e.do("GET", "/admin/plugins/oauthdemo", nil, "", e.cookie).Body.String(); !strings.Contains(body, "La conexión termina pronto") {
		t.Fatal("the plugin page does not warn")
	}

	// Disconnect.
	rec = e.admin(base+"/disconnect", url.Values{})
	if rec.Code != http.StatusSeeOther || flashOf(rec) != "success: Desconectado de Demo." || e.audited("plugin.oauth_disconnected") != 1 {
		t.Fatalf("disconnect: %d %q", rec.Code, flashOf(rec))
	}
	if list, _ := e.st.PluginOAuthConnections(ctx, ""); len(list) != 0 {
		t.Fatal("still connected")
	}
	// The messages follow the administrator's own language, not the site's.
	if err := e.st.SetUserLanguage(ctx, e.adminID, "en"); err != nil {
		t.Fatal(err)
	}
	if rec := e.admin(base+"/disconnect", url.Values{}); flashOf(rec) != "success: Disconnected from Demo." {
		t.Fatalf("disconnect in the admin language: %q", flashOf(rec))
	}
	if body := e.do("GET", "/admin/", nil, "", e.cookie).Body.String(); strings.Contains(body, `id="oauth-alerts"`) {
		t.Fatal("a notice without a connection")
	}

	// Unknown providers are 404, and another host of the site cannot
	// start a connection: the provider would send it back to the primary
	// host, where its session cookie does not exist.
	if rec := e.admin("/admin/plugins/oauthdemo/oauth/nope/connect", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", rec.Code)
	}
	form := url.Values{"csrf": {e.csrf}}
	req := httptest.NewRequest("POST", "http://alias.example.test"+base+"/connect", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(e.cookie)
	rec = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if strings.HasPrefix(rec.Header().Get("Location"), "https://auth.example.test") ||
		!strings.Contains(flashOf(rec), "http://example.test/admin/plugins/oauthdemo#connections") {
		t.Fatalf("connect from another host: %d %s %q", rec.Code, rec.Header().Get("Location"), flashOf(rec))
	}
}
