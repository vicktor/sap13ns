package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// ------------------------------------------------------------ manifest --

func oauthManifest() map[string]any {
	return map[string]any{
		"abi": 1, "id": "linkedin", "name": "LinkedIn", "version": "1.0.0", "wasm": "plugin.wasm",
		"hooks": []string{"event"}, "events": []string{"post.published"},
		"permissions": []string{"oauth:linkedin", "http:www.linkedin.com", "http:api.linkedin.com"},
		"settings": []map[string]any{
			{"key": "client_id", "label": "Client ID", "type": "text"},
			{"key": "client_secret", "label": "Client secret", "type": "secret"},
		},
		"oauth": []map[string]any{{
			"id": "linkedin", "name": "LinkedIn",
			"authorize_url":     "https://www.linkedin.com/oauth/v2/authorization",
			"token_url":         "https://www.linkedin.com/oauth/v2/accessToken",
			"userinfo_url":      "https://api.linkedin.com/v2/userinfo",
			"scopes":            []string{"openid", "profile", "w_member_social"},
			"client_id_setting": "client_id", "client_secret_setting": "client_secret",
		}},
	}
}

func parseOAuthManifest(t *testing.T, m map[string]any) (*Manifest, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return ParseManifest(raw)
}

func TestOAuthManifest(t *testing.T) {
	m, err := parseOAuthManifest(t, oauthManifest())
	if err != nil {
		t.Fatal(err)
	}
	if d := m.OAuthProvider("linkedin"); d == nil || d.Separator() != " " || d.AuthorizeOrigin() != "https://www.linkedin.com" {
		t.Fatalf("provider: %+v", d)
	}
	if PermissionRisk("oauth:linkedin") != "medium" {
		t.Fatal("oauth permissions are medium risk")
	}
	pinned := m.Pinned()
	if !slices.ContainsFunc(pinned, func(s string) bool { return strings.HasPrefix(s, "oauth-provider:linkedin:") }) ||
		!slices.Contains(pinned, "oauth:linkedin") {
		t.Fatalf("pinned: %v", pinned)
	}
	// A change of scopes or URLs needs a new approval.
	changed := oauthManifest()
	changed["oauth"].([]map[string]any)[0]["scopes"] = []string{"openid", "profile", "w_member_social", "w_organization_social"}
	m2, err := parseOAuthManifest(t, changed)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Equal(m.Pinned(), m2.Pinned()) {
		t.Fatal("a new scope does not change what the approval pins")
	}

	edit := func(f func(m map[string]any, d map[string]any)) map[string]any {
		m := oauthManifest()
		f(m, m["oauth"].([]map[string]any)[0])
		return m
	}
	bad := map[string]map[string]any{
		"no permission": edit(func(m, d map[string]any) {
			m["permissions"] = []string{"http:www.linkedin.com", "http:api.linkedin.com"}
		}),
		"permission without provider": edit(func(m, d map[string]any) {
			m["permissions"] = append(m["permissions"].([]string), "oauth:other")
		}),
		"token host not granted": edit(func(m, d map[string]any) {
			m["permissions"] = []string{"oauth:linkedin", "http:api.linkedin.com"}
		}),
		"plain http":        edit(func(m, d map[string]any) { d["token_url"] = "http://www.linkedin.com/oauth/v2/accessToken" }),
		"port":              edit(func(m, d map[string]any) { d["token_url"] = "https://www.linkedin.com:8443/token" }),
		"credentials":       edit(func(m, d map[string]any) { d["authorize_url"] = "https://u:p@www.linkedin.com/a" }),
		"ip address":        edit(func(m, d map[string]any) { d["userinfo_url"] = "https://127.0.0.1/userinfo" }),
		"fragment":          edit(func(m, d map[string]any) { d["authorize_url"] = "https://www.linkedin.com/a#x" }),
		"secret not secret": edit(func(m, d map[string]any) { d["client_secret_setting"] = "client_id" }),
		"unknown setting":   edit(func(m, d map[string]any) { d["client_id_setting"] = "nope" }),
		"no secret without pkce": edit(func(m, d map[string]any) {
			delete(d, "client_secret_setting")
		}),
		"bad scope":     edit(func(m, d map[string]any) { d["scopes"] = []string{"a b"} }),
		"comma scope":   edit(func(m, d map[string]any) { d["scope_separator"] = ","; d["scopes"] = []string{"a,b"} }),
		"bad separator": edit(func(m, d map[string]any) { d["scope_separator"] = ";" }),
		"bad auth":      edit(func(m, d map[string]any) { d["token_auth"] = "jwt" }),
		"bad id":        edit(func(m, d map[string]any) { d["id"] = "Linked In" }),
		"no name":       edit(func(m, d map[string]any) { d["name"] = "" }),
		"refresh token": edit(func(m, d map[string]any) {
			d["refresh"] = map[string]any{"url": "https://www.linkedin.com/r", "grant_type": "x", "token": "id_token"}
		}),
		"exchange without grant": edit(func(m, d map[string]any) {
			d["exchange"] = map[string]any{"url": "https://www.linkedin.com/x"}
		}),
	}
	for name, m := range bad {
		if _, err := parseOAuthManifest(t, m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A public client with PKCE needs no secret.
	if _, err := parseOAuthManifest(t, edit(func(m, d map[string]any) {
		delete(d, "client_secret_setting")
		d["pkce"] = true
	})); err != nil {
		t.Errorf("public client: %v", err)
	}
}

func TestParseTokenResponse(t *testing.T) {
	tok, err := parseTokenResponse(200, []byte(`{"access_token":"a","refresh_token":"r","expires_in":3600,"refresh_token_expires_in":"7200","token_type":"Bearer","scope":"a b"}`))
	if err != nil || tok.Access != "a" || tok.Refresh != "r" || tok.ExpiresIn != 3600 || tok.RefreshExpiresIn != 7200 || tok.Scope != "a b" {
		t.Fatalf("flat: %+v %v", tok, err)
	}
	// Instagram wraps the code exchange in "data" and names the scopes
	// "permissions"; its user id is a number or a string.
	tok, err = parseTokenResponse(200, []byte(`{"data":[{"access_token":"ig","user_id":17841400000000000,"permissions":"basic,publish"}]}`))
	if err != nil || tok.Access != "ig" || tok.UserID != "17841400000000000" || tok.Scope != "basic,publish" {
		t.Fatalf("wrapped: %+v %v", tok, err)
	}
	if _, err := parseTokenResponse(200, []byte(`{"access_token":"a","token_type":"mac"}`)); err == nil {
		t.Fatal("a mac token was accepted")
	}
	if _, err := parseTokenResponse(200, []byte(`{"access_token":"a b"}`)); err == nil {
		t.Fatal("a token with a space was accepted")
	}
	for body, want := range map[string]string{
		`{"error":"invalid_grant","error_description":"expired"}`:                                  "HTTP 400: invalid_grant: expired",
		`{"error":{"message":"Error validating access token","type":"OAuthException"}}`:            "HTTP 400: OAuthException: Error validating access token",
		`{"error_type":"OAuthException","code":400,"error_message":"Matching code was not found"}`: "HTTP 400: OAuthException: Matching code was not found",
		`<html>secret-token-in-a-page</html>`:                                                      "HTTP 400",
	} {
		_, err := parseTokenResponse(400, []byte(body))
		var oe *OAuthError
		if !errors.As(err, &oe) || err.Error() != want {
			t.Errorf("%s: %v, want %s", body, err, want)
		}
	}
}

func TestOAuthDue(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		c    store.PluginOAuth
		want bool
	}{
		{store.PluginOAuth{Refreshable: true, ConnectedAt: now.Add(-10 * time.Minute), ExpiresAt: at(50 * time.Minute)}, false},
		{store.PluginOAuth{Refreshable: true, ConnectedAt: now.Add(-40 * time.Minute), ExpiresAt: at(20 * time.Minute)}, true},
		{store.PluginOAuth{Refreshable: true, ConnectedAt: now.Add(-29 * 24 * time.Hour), ExpiresAt: at(31 * 24 * time.Hour)}, false},
		{store.PluginOAuth{Refreshable: true, ConnectedAt: now.Add(-31 * 24 * time.Hour), ExpiresAt: at(29 * 24 * time.Hour)}, true},
		{store.PluginOAuth{Refreshable: false, ConnectedAt: now.Add(-40 * time.Minute), ExpiresAt: at(20 * time.Minute)}, false},
		{store.PluginOAuth{Refreshable: true, ConnectedAt: now.Add(-time.Hour)}, false},
	}
	for i, c := range cases {
		if got := oauthDue(&c.c, now); got != c.want {
			t.Errorf("case %d: due = %v", i, got)
		}
	}
}

// TestOAuthClientGuarded: the token requests go through the SSRF guard.
func TestOAuthClientGuarded(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the guarded client reached a loopback server")
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/token", nil)
	if _, err := newOAuthClient().Do(req); err == nil {
		t.Fatal("the OAuth client reached a loopback address")
	}
}

// ------------------------------------------------------- fake provider --

type fakeProvider struct {
	t  *testing.T
	mu sync.Mutex

	challenge  string // from the authorization URL
	access     string // the current access token
	refresh    string
	expiresIn  int
	refreshErr string // answers refreshes with this error code
	requests   []string
	hosts      map[string]bool
}

type handlerTransport struct{ h http.Handler }

func (ht handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	ht.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r.ParseForm()
	f.requests = append(f.requests, r.Method+" "+r.URL.Host+r.URL.Path)
	f.hosts[r.URL.Host] = true
	w.Header().Set("Content-Type", "application/json")
	write := func(status int, v any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Host + r.URL.Path {
	case "auth.example.test/token": // "demo": PKCE, Basic client authentication, rotating refresh tokens
		id, secret, ok := r.BasicAuth()
		if !ok || id != "demo-client" || secret != "demo-secret" || r.PostForm.Get("client_secret") != "" {
			write(401, map[string]string{"error": "invalid_client"})
			return
		}
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
			if r.PostForm.Get("code") != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge ||
				r.PostForm.Get("redirect_uri") != "https://example.test/admin/plugins/oauthdemo/oauth/demo/callback" {
				write(400, map[string]string{"error": "invalid_grant", "error_description": "bad code or verifier"})
				return
			}
			f.access, f.refresh = "at1", "rt1"
		case "refresh_token":
			if f.refreshErr != "" {
				write(400, map[string]string{"error": f.refreshErr})
				return
			}
			if r.PostForm.Get("refresh_token") != f.refresh {
				write(400, map[string]string{"error": "invalid_grant"})
				return
			}
			n := len(f.requests)
			f.access, f.refresh = "at-"+string(rune('a'+n)), "rt-"+string(rune('a'+n))
		default:
			write(400, map[string]string{"error": "unsupported_grant_type"})
			return
		}
		write(200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": f.expiresIn,
			"refresh_token_expires_in": 365 * 86400, "token_type": "Bearer", "scope": "openid profile w_member_social"})
	case "api.example.test/userinfo":
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			write(401, map[string]string{"error": "invalid_token"})
			return
		}
		write(200, map[string]any{"sub": "abc123", "name": "Ada Lovelace", "locale": map[string]string{"country": "US"}})

	case "api.example.test/oauth/access_token": // "meta": secret in the body, data-wrapped answer
		if r.Method != http.MethodPost || r.PostForm.Get("client_id") != "meta-app" || r.PostForm.Get("client_secret") != "meta-secret" ||
			r.PostForm.Get("code") != "meta-code" || r.PostForm.Get("code_verifier") != "" {
			write(400, map[string]any{"error_type": "OAuthException", "code": 400, "error_message": "Matching code was not found"})
			return
		}
		write(200, map[string]any{"data": []map[string]any{{"access_token": "short", "user_id": 1784, "permissions": "basic,content_publish"}}})
	case "graph.example.test/access_token":
		q := r.URL.Query()
		if q.Get("grant_type") != "ig_exchange_token" || q.Get("client_secret") != "meta-secret" || q.Get("access_token") != "short" {
			write(400, map[string]any{"error": map[string]string{"message": "bad exchange", "type": "OAuthException"}})
			return
		}
		f.access = "long1"
		write(200, map[string]any{"access_token": f.access, "token_type": "bearer", "expires_in": 5184000})
	case "graph.example.test/refresh_access_token":
		q := r.URL.Query()
		if q.Get("grant_type") != "ig_refresh_token" || q.Get("access_token") != f.access || q.Get("client_secret") != "" {
			write(400, map[string]any{"error": map[string]string{"message": "bad refresh", "type": "OAuthException"}})
			return
		}
		f.access = f.access + "+"
		write(200, map[string]any{"access_token": f.access, "token_type": "bearer", "expires_in": 5184000})
	case "graph.example.test/v1/me":
		q := r.URL.Query()
		if q.Get("access_token") != f.access || q.Get("fields") != "user_id,username" {
			write(400, map[string]any{"error": map[string]string{"message": "bad token"}})
			return
		}
		write(200, map[string]any{"data": []map[string]any{{"user_id": "1784", "username": "ada.ig"}}})
	default:
		write(404, map[string]string{"error": "not_found"})
	}
}

func (f *fakeProvider) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

// oauthHost installs testdata/oauth, enabled and configured, with a fake
// provider behind the OAuth client.
func oauthHost(t *testing.T) (*Host, *store.Store, *fakeProvider, int64, []byte) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	st := testutil.Store(t)
	dir := t.TempDir()
	installPlugin(t, dir, "./internal/plugin/testdata/oauth", "oauthdemo")
	ctx := context.Background()
	h, err := NewHost(ctx, dir, "", st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() SiteInfo { return SiteInfo{URL: "https://example.test", Title: "Test", Language: "en"} }, testLinks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	h.SetSecrets(auth.NewKeys([]byte("test-secret-key-test-secret-key!")))
	f := &fakeProvider{t: t, expiresIn: 3600, hosts: map[string]bool{}}
	h.SetOAuthTransport(handlerTransport{f})
	if err := h.Load(ctx); err != nil {
		t.Fatal(err)
	}
	uid := adminUser(t, st)
	if err := h.Approve(ctx, "oauthdemo", "", uid); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveSettings(ctx, "oauthdemo", map[string]string{"client_id": "demo-client", "client_secret": "demo-secret",
		"meta_app_id": "meta-app", "meta_app_secret": "meta-secret"}); err != nil {
		t.Fatal(err)
	}
	session := auth.HashToken("session-token")
	if err := st.CreateSession(ctx, store.Session{TokenHash: session, UserID: uid, CSRFToken: "c", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return h, st, f, uid, session
}

// waitLog waits for a log line of the plugin that starts with prefix
// and returns it.
func waitLog(t *testing.T, p *Plugin, after int, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		logs := p.Logs()
		for _, l := range logs[min(after, len(logs)):] {
			if strings.HasPrefix(l.Text, prefix) {
				return l.Text
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no log line %q in %+v", prefix, p.Logs())
	return ""
}

// tokenLog asks the plugin for the tokens of providers (joined with "+")
// from an event and returns its log lines.
func tokenLog(t *testing.T, h *Host, providers string) []string {
	t.Helper()
	p := h.Get("oauthdemo")
	n := len(p.Logs())
	h.Emit("post.published", map[string]any{"slug": providers})
	want := strings.Count(providers, "+") + 1
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var out []string
		for _, l := range p.Logs()[n:] {
			if strings.HasPrefix(l.Text, "token ") || strings.HasPrefix(l.Text, "oauth ") {
				out = append(out, l.Text)
			}
		}
		if len(out) >= want {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the plugin did not log %d tokens: %+v", want, p.Logs()[n:])
	return nil
}

func TestOAuthFlow(t *testing.T) {
	h, st, f, uid, session := oauthHost(t)
	ctx := context.Background()
	redirect := "https://example.test/admin/plugins/oauthdemo/oauth/demo/callback"
	state := func() string { return randomURLToken(32) }

	begin := func(provider, redirect, s string) *url.URL {
		t.Helper()
		raw, err := h.OAuthBegin(ctx, "oauthdemo", provider, redirect, s, session, uid)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	// Not connected yet.
	if got := tokenLog(t, h, "demo"); !strings.Contains(got[0], "not connected") {
		t.Fatalf("before connecting: %q", got)
	}

	s1 := state()
	u := begin("demo", redirect, s1)
	q := u.Query()
	if u.Host != "auth.example.test" || u.Path != "/authorize" || q.Get("prompt") != "consent" || q.Get("response_type") != "code" ||
		q.Get("client_id") != "demo-client" || q.Get("redirect_uri") != redirect || q.Get("state") != s1 ||
		q.Get("scope") != "openid profile w_member_social" || q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 {
		t.Fatalf("authorization URL %s", u)
	}
	if strings.Contains(u.RawQuery, "+") {
		t.Fatalf("spaces must be %%20: %s", u.RawQuery)
	}
	f.challenge = q.Get("code_challenge")

	// The state is bound to the session, the user, the plugin, the
	// provider and the redirect URI, and serves one callback: a forged or
	// replayed one is refused.
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, "forged-"+s1, "good-code", session, uid); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("forged state: %v", err)
	}
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s1, "good-code", auth.HashToken("other session"), uid); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("other session: %v", err)
	}
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s1, "good-code", session, uid); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("a state tried once is used up: %v", err)
	}
	for name, try := range map[string]func(s string) error{
		"user": func(s string) error {
			_, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s, "good-code", session, uid+1)
			return err
		},
		"provider": func(s string) error {
			_, err := h.OAuthFinish(ctx, "oauthdemo", "meta", redirect, s, "good-code", session, uid)
			return err
		},
		"redirect": func(s string) error {
			_, err := h.OAuthFinish(ctx, "oauthdemo", "demo", "https://evil.test/cb", s, "good-code", session, uid)
			return err
		},
	} {
		s := state()
		f.challenge = begin("demo", redirect, s).Query().Get("code_challenge")
		if err := try(s); !errors.Is(err, ErrOAuthState) {
			t.Errorf("another %s: %v", name, err)
		}
	}
	if n := f.count("POST auth.example.test/token"); n != 0 {
		t.Fatalf("refused callbacks reached the token endpoint %d times", n)
	}

	// A wrong verifier (the challenge of another authorization) fails at
	// the provider.
	s2 := state()
	begin("demo", redirect, s2)
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s2, "good-code", session, uid); err == nil ||
		!strings.Contains(err.Error(), "invalid_grant") {
		t.Fatalf("wrong verifier: %v", err)
	}

	// The right one connects.
	s3 := state()
	f.challenge = begin("demo", redirect, s3).Query().Get("code_challenge")
	c, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s3, "good-code", session, uid)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountID != "abc123" || c.AccountName != "Ada Lovelace" || !c.Refreshable || c.ExpiresAt == nil ||
		c.ExpiresAt.Sub(time.Now()) < 59*time.Minute || c.RefreshExpiresAt == nil || c.ConnectedBy != "admin" {
		t.Fatalf("connection %+v", c)
	}
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s3, "good-code", session, uid); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("replay: %v", err)
	}
	waitLog(t, h.Get("oauthdemo"), 0, "connected demo")

	// Sealed at rest: neither token appears in the table.
	var tokens string
	if err := st.DB.QueryRow(ctx, `SELECT tokens FROM plugin_oauth WHERE plugin_id = 'oauthdemo' AND provider = 'demo'`).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	var row string
	st.DB.QueryRow(ctx, `SELECT o::text FROM plugin_oauth o WHERE plugin_id = 'oauthdemo'`).Scan(&row)
	if !strings.HasPrefix(tokens, "enc1:") || strings.Contains(row, "at1") || strings.Contains(row, "rt1") {
		t.Fatalf("tokens are not sealed: %s", row)
	}
	// Bound to the plugin and the provider: copied to another row they
	// cannot be opened.
	if _, err := h.openTokens("oauthdemo", "meta", tokens); err == nil {
		t.Fatal("the tokens opened under another provider")
	}

	// The plugin gets the access token, never the refresh token.
	got := tokenLog(t, h, "demo")
	if !strings.HasPrefix(got[0], "token demo at1 ") || !strings.Contains(got[0], " abc123 Ada Lovelace openid,profile,w_member_social") ||
		strings.Contains(got[0], "rt1") {
		t.Fatalf("token: %q", got)
	}
	// Not while a page renders.
	if html, _ := h.Shortcode(ctx, "token", nil, "", nil); !strings.Contains(html, "permission denied") {
		t.Fatalf("render hook got a token: %q", html)
	}
	// At most four calls per request.
	if got := tokenLog(t, h, "demo+demo+demo+demo+demo"); !strings.Contains(got[4], "too many host calls") {
		t.Fatalf("limit: %q", got)
	}

	// Renewed once half of its life has passed, with the rotated refresh
	// token stored for the next renewal.
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET connected_at = now() - interval '40 minutes', expires_at = now() + interval '20 minutes'`+
		` WHERE provider = 'demo'`)
	got = tokenLog(t, h, "demo")
	if strings.Contains(got[0], " at1 ") || !strings.HasPrefix(got[0], "token demo at-") {
		t.Fatalf("not renewed: %q", got)
	}
	first := strings.Fields(got[0])[2]
	if again := tokenLog(t, h, "demo"); strings.Fields(again[0])[2] != first {
		t.Fatalf("renewed again although not due: %q", again)
	}
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET refreshed_at = now() - interval '40 minutes', expires_at = now() + interval '20 minutes'`+
		` WHERE provider = 'demo'`)
	if again := tokenLog(t, h, "demo"); strings.Fields(again[0])[2] == first {
		t.Fatalf("the rotated refresh token was not used: %q", again)
	}
	if n := f.count("POST auth.example.test/token"); n != 4 { // wrong verifier, code, two renewals
		t.Fatalf("token requests: %d (%v)", n, f.requests)
	}

	// Renewed from the hourly cron too.
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET refreshed_at = now() - interval '40 minutes', expires_at = now() + interval '20 minutes'`+
		` WHERE provider = 'demo'`)
	if !h.WantsCron() {
		t.Fatal("a site with a renewable connection must get its hourly cron")
	}
	h.Cron(time.Now())
	deadline := time.Now().Add(10 * time.Second)
	for f.count("POST auth.example.test/token") != 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := f.count("POST auth.example.test/token"); n != 5 {
		t.Fatalf("the cron did not renew: %d", n)
	}

	// A refused refresh token ends the renewals; the token is used while it
	// lasts and the administrator is warned.
	f.refreshErr = "invalid_grant"
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET refreshed_at = now() - interval '40 minutes', expires_at = now() + interval '20 minutes'`+
		` WHERE provider = 'demo'`)
	if got := tokenLog(t, h, "demo"); !strings.HasPrefix(got[0], "token demo at-") {
		t.Fatalf("after a failed renewal: %q", got)
	}
	conns, _ := h.OAuthConnections(ctx, "oauthdemo")
	if c := conns["demo"]; c == nil || c.Refreshable || !strings.Contains(c.RefreshError, "invalid_grant") {
		t.Fatalf("failed renewal not recorded: %+v", c)
	}
	alerts, err := h.OAuthAlerts(ctx)
	if err != nil || len(alerts) != 1 || alerts[0].Provider != "demo" || alerts[0].Ended || alerts[0].Account != "Ada Lovelace" {
		t.Fatalf("alerts: %+v %v", alerts, err)
	}
	if !hasLog(h.Get("oauthdemo"), "renewing the Demo token failed") {
		t.Fatal("the failed renewal was not logged")
	}
	for _, l := range h.Get("oauthdemo").Logs() {
		if strings.Contains(l.Text, "rt-") {
			t.Fatalf("a refresh token reached the log: %q", l.Text)
		}
	}
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET expires_at = now() - interval '1 minute' WHERE provider = 'demo'`)
	if got := tokenLog(t, h, "demo"); !strings.Contains(got[0], "connection expired") {
		t.Fatalf("expired: %q", got)
	}
	if alerts, _ := h.OAuthAlerts(ctx); len(alerts) != 1 || !alerts[0].Ended {
		t.Fatalf("ended alert: %+v", alerts)
	}

	// The permission is needed: without the grant the host refuses.
	st.DB.Exec(ctx, `UPDATE plugins SET granted_permissions = array_remove(granted_permissions, 'oauth:meta') WHERE id = 'oauthdemo'`)
	h.Load(ctx)
	if h.Get("oauthdemo").Status == StatusEnabled {
		// A narrower grant than the manifest asks for stops the plugin.
		t.Fatal("the plugin runs without its approved permissions")
	}
	if _, err := h.OAuthBegin(ctx, "oauthdemo", "meta", redirect, state(), session, uid); !errors.Is(err, ErrOAuthUnavailable) {
		t.Fatalf("begin without the permission: %v", err)
	}
	if err := h.Approve(ctx, "oauthdemo", "", uid); err != nil {
		t.Fatal(err)
	}

	// Disconnect.
	if ok, err := h.OAuthDisconnect(ctx, "oauthdemo", "demo"); !ok || err != nil {
		t.Fatalf("disconnect: %v %v", ok, err)
	}
	if got := tokenLog(t, h, "demo"); !strings.Contains(got[0], "not connected") {
		t.Fatalf("after disconnecting: %q", got)
	}
	if ok, _ := h.OAuthDisconnect(ctx, "oauthdemo", "demo"); ok {
		t.Fatal("disconnected twice")
	}

	// Meta: a secret in the body, no PKCE, a data-wrapped answer, a
	// long-lived token and renewals with the access token itself.
	metaRedirect := "https://example.test/admin/plugins/oauthdemo/oauth/meta/callback"
	s4 := state()
	mu := begin("meta", metaRedirect, s4)
	if mq := mu.Query(); mq.Get("scope") != "basic,content_publish" || mq.Get("code_challenge") != "" || mq.Get("client_id") != "meta-app" {
		t.Fatalf("meta authorization URL %s", mu)
	}
	c, err = h.OAuthFinish(ctx, "oauthdemo", "meta", metaRedirect, s4, "meta-code", session, uid)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccountID != "1784" || c.AccountName != "ada.ig" || !c.Refreshable || c.ExpiresAt == nil ||
		c.ExpiresAt.Sub(time.Now()) < 59*24*time.Hour || strings.Join(c.Scopes, " ") != "basic content_publish" {
		t.Fatalf("meta connection %+v", c)
	}
	if got := tokenLog(t, h, "meta"); !strings.HasPrefix(got[0], "token meta long1 ") {
		t.Fatalf("meta token: %q", got)
	}
	st.DB.Exec(ctx, `UPDATE plugin_oauth SET connected_at = now() - interval '31 days', expires_at = now() + interval '29 days'`+
		` WHERE provider = 'meta'`)
	if got := tokenLog(t, h, "meta"); !strings.HasPrefix(got[0], "token meta long1+ ") {
		t.Fatalf("meta renewal: %q", got)
	}
	conns, _ = h.OAuthConnections(ctx, "oauthdemo")
	if c := conns["meta"]; c == nil || !c.Refreshable || c.ExpiresAt.Sub(time.Now()) < 59*24*time.Hour || c.RefreshedAt == nil {
		t.Fatalf("meta after renewal: %+v", c)
	}
	for host := range f.hosts {
		if !strings.HasSuffix(host, ".example.test") {
			t.Fatalf("contacted %s", host)
		}
	}

	// Uninstalling deletes the connections, whatever happens to the data.
	if err := h.Disable(ctx, "oauthdemo"); err != nil {
		t.Fatal(err)
	}
	if err := h.Uninstall(ctx, "oauthdemo", false); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.PluginOAuthConnections(ctx, ""); len(list) != 0 {
		t.Fatalf("connections survived the uninstall: %+v", list)
	}
}

// TestOAuthStateExpiry: a state older than ten minutes is refused, and a
// state dies with its session.
func TestOAuthStateExpiry(t *testing.T) {
	h, st, _, uid, session := oauthHost(t)
	ctx := context.Background()
	redirect := "https://example.test/admin/plugins/oauthdemo/oauth/demo/callback"
	s := randomURLToken(32)
	if _, err := h.OAuthBegin(ctx, "oauthdemo", "demo", redirect, s, session, uid); err != nil {
		t.Fatal(err)
	}
	st.DB.Exec(ctx, `UPDATE plugin_oauth_states SET expires_at = now() - interval '1 second'`)
	if _, err := h.OAuthFinish(ctx, "oauthdemo", "demo", redirect, s, "good-code", session, uid); !errors.Is(err, ErrOAuthState) {
		t.Fatalf("expired state: %v", err)
	}
	s = randomURLToken(32)
	h.OAuthBegin(ctx, "oauthdemo", "demo", redirect, s, session, uid)
	st.DB.Exec(ctx, `DELETE FROM sessions`)
	var n int
	st.DB.QueryRow(ctx, `SELECT count(*) FROM plugin_oauth_states`).Scan(&n)
	if n != 0 {
		t.Fatal("a state outlived its session")
	}
	// Without the client's settings nothing starts.
	h.SaveSettings(ctx, "oauthdemo", map[string]string{"client_id": ""})
	if _, err := h.OAuthBegin(ctx, "oauthdemo", "demo", redirect, randomURLToken(32), session, uid); !errors.Is(err, ErrOAuthNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
}
