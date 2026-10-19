package web

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// signInForm is the settings page as it is saved now (settingsForm), in
// English and with the sign-in address p.
func (e *customEnv) signInForm(p string) url.Values {
	f := settingsForm(e.s.settings())
	f.Set("language", "en")
	f.Set("login_path", p)
	return f
}

// signInEnv is newCustomEnv with a mailer that prints what it sends into
// the returned buffer.
func signInEnv(t *testing.T) (*customEnv, *bytes.Buffer) {
	t.Helper()
	e := newCustomEnv(t)
	var box bytes.Buffer
	e.s.mailer = mail.New(config.SMTP{}, slog.New(slog.NewTextHandler(&box, nil)), true)
	return e, &box
}

// passwordUser adds a user who signs in with password.
func (e *customEnv) passwordUser(name, password, role string) int64 {
	e.t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		e.t.Fatal(err)
	}
	id, err := e.st.CreateUser(context.Background(), store.NewUser{Username: name, Email: name + "@example.test", Role: role, PasswordHash: hash})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// signInAt posts the sign-in form at target, from address ip.
func (e *customEnv) signInAt(target, login, password, ip string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", "http://example.test"+target, strings.NewReader(url.Values{"login": {login}, "password": {password}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	req.RemoteAddr = ip + ":4000"
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// setSignIn saves a sign-in address and the hide-admin option as the
// command line does: straight into the store.
func (e *customEnv) setSignIn(p string, hide bool) {
	e.t.Helper()
	ctx := context.Background()
	st, err := e.st.LoadSettings(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	st.LoginPath, st.HideAdmin = p, hide
	if err := e.st.SaveSettings(ctx, st); err != nil {
		e.t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		e.t.Fatal(err)
	}
	e.s.contentChanged()
}

// sameAsUnknown checks that the site answers method path as it answers an
// address it never served: the same one with another first segment, never
// asked before (the page cache would tell them apart otherwise).
func (e *customEnv) sameAsUnknown(method, path string) {
	e.t.Helper()
	unknownSeq++
	first, rest, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	other := fmt.Sprintf("/nosuch%d", unknownSeq)
	if strings.Contains(path[1:], "/") {
		other += "/" + rest
	} else if _, q, ok := strings.Cut(first, "?"); ok {
		other += "?" + q
	}
	do := func(p string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://example.test"+p, nil)
		if method == "POST" {
			req = httptest.NewRequest(method, "http://example.test"+p, strings.NewReader("login=a&password=b"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return e.do(req, nil)
	}
	got, want := do(path), do(other)
	if got.Code != want.Code || got.Code < 400 {
		e.t.Errorf("%s %s: %d, %s %s: %d", method, path, got.Code, method, other, want.Code)
		return
	}
	gp, _, _ := strings.Cut(path, "?")
	op, _, _ := strings.Cut(other, "?")
	if gb, wb := strings.ReplaceAll(got.Body.String(), gp, "PATH"), strings.ReplaceAll(want.Body.String(), op, "PATH"); gb != wb {
		e.t.Errorf("%s %s does not answer as %s:\n%s\n---\n%s", method, path, other, gb, wb)
	}
	for _, h := range []string{"Content-Type", "Cache-Control", "Allow", "Location", "X-Cache", "X-Robots-Tag"} {
		if got.Header().Get(h) != want.Header().Get(h) {
			e.t.Errorf("%s %s: %s %q, %s %q", method, path, h, got.Header().Get(h), other, want.Header().Get(h))
		}
	}
}

var unknownSeq int

func TestSignInDefaultAddress(t *testing.T) {
	e, _ := signInEnv(t)
	if p := e.s.settings().LoginPath; p != sitecheck.DefaultLoginPath {
		t.Fatalf("default sign-in address %q", p)
	}
	rec := e.get("/login", nil)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, `action="/login"`) || !strings.Contains(body, `href="/login/forgot"`) ||
		!strings.Contains(body, `data-begin="/login/passkey/begin" data-finish="/login/passkey/finish"`) {
		t.Fatalf("GET /login: %d", rec.Code)
	}
	// The pages under it, and the addresses of before for e-mails already sent.
	for _, p := range []string{"/login/forgot", "/forgot-password", "/login/reset?token=x", "/reset-password?token=x"} {
		if rec := e.get(p, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form") {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
	if rec := e.get("/login/setup?token=x", nil); rec.Code != http.StatusNotFound {
		t.Errorf("setup with a bad token: %d", rec.Code)
	}
	if body := e.get("/robots.txt", nil).Body.String(); !strings.Contains(body, "Disallow: /login\n") {
		t.Errorf("robots.txt:\n%s", body)
	}
	if body := e.get("/", nil).Body.String(); !strings.Contains(body, `<a href="/login" rel="nofollow">`) {
		t.Error("the footer has no sign-in link")
	}
	if loc := e.get("/admin/posts", nil).Header().Get("Location"); loc != "/login?next=%2Fadmin%2Fposts" {
		t.Errorf("admin redirect to %q", loc)
	}
	e.passwordUser("ana", "a long local password", auth.RoleAuthor)
	if rec := e.signInAt("/login", "ana", "a long local password", "192.0.2.20"); rec.Code != http.StatusSeeOther {
		t.Errorf("POST /login: %d %s", rec.Code, rec.Body)
	}
}

func TestSignInAddressMoves(t *testing.T) {
	ctx := context.Background()
	e, box := signInEnv(t)
	e.passwordUser("ana", "a long local password", auth.RoleAuthor)

	// The home page is cached with the old link first.
	e.get("/", nil)
	if rec := e.get("/", nil); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("home page not cached: %q", rec.Header().Get("X-Cache"))
	}

	// Saved from the settings page, forgiving the slashes and capitals.
	page := e.get("/admin/settings", e.admin).Body.String()
	if !strings.Contains(page, `name="login_path" value="/login"`) || !strings.Contains(page, "http://example.test/login") ||
		!strings.Contains(page, "sap13ns settings login-path") || !strings.Contains(page, `name="hide_admin"`) {
		t.Fatal("the settings page has no sign-in address")
	}
	rec := e.postForm("/admin/settings", e.signInForm("Entrar/"))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "The sign-in address is now http://example.test/entrar") ||
		!strings.Contains(body, "Bookmark it: /login will stop working.") || !strings.Contains(body, `value="/entrar"`) {
		t.Fatalf("save: %d %s", rec.Code, body)
	}
	if got := e.s.settings().LoginPath; got != "/entrar" {
		t.Fatalf("saved %q", got)
	}
	if a := e.audited("settings.login_path"); len(a) != 1 || a[0].Target != "/entrar" {
		t.Errorf("audit: %+v", a)
	}
	// Saving it again, unchanged, is an ordinary save.
	if rec := e.postForm("/admin/settings", e.signInForm("/entrar")); rec.Code != http.StatusSeeOther {
		t.Errorf("save unchanged: %d", rec.Code)
	}

	// The sign-in pages answer at the new address only.
	rec = e.get("/entrar", nil)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, `action="/entrar"`) || !strings.Contains(body, `href="/entrar/forgot"`) ||
		!strings.Contains(body, `data-begin="/entrar/passkey/begin" data-finish="/entrar/passkey/finish"`) || strings.Contains(body, `"/login`) {
		t.Fatalf("GET /entrar: %d", rec.Code)
	}
	for _, p := range []string{"/entrar/forgot", "/entrar/reset?token=x"} {
		if rec := e.get(p, nil); rec.Code != 200 {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
	e.sameAsUnknown("GET", "/login")
	e.sameAsUnknown("POST", "/login")
	for _, p := range []string{"/login/forgot", "/login/reset", "/login/2fa", "/login/setup"} {
		e.sameAsUnknown("GET", p)
	}
	for _, p := range []string{"/forgot-password", "/reset-password", "/setup"} {
		e.sameAsUnknown("GET", p)
	}
	e.sameAsUnknown("POST", "/login/passkey/begin")

	// Every link follows: the footer (the cache was dropped), the admin's
	// way in, robots.txt, which does not give the new address away.
	if body := e.get("/", nil).Body.String(); !strings.Contains(body, `<a href="/entrar" rel="nofollow">`) || strings.Contains(body, `href="/login"`) {
		t.Error("footer link")
	}
	if loc := e.get("/admin/posts", nil).Header().Get("Location"); loc != "/entrar?next=%2Fadmin%2Fposts" {
		t.Errorf("admin redirect to %q", loc)
	}
	if body := e.get("/robots.txt", nil).Body.String(); strings.Contains(body, "/login") || strings.Contains(body, "/entrar") {
		t.Errorf("robots.txt:\n%s", body)
	}

	// The comment form, when comments need an account.
	st := e.s.settings()
	st.CommentsEnabled, st.CommentsRequireLogin = true, true
	if err := e.st.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	post := e.publishPost("Hola mundo", "Texto.")
	if body := e.get(post, nil).Body.String(); !strings.Contains(body, `<a href="/entrar" rel="nofollow">`) {
		t.Errorf("comment form at %s has no link to /entrar", post)
	}

	// Signing in, and the password reset e-mail.
	if rec := e.signInAt("/entrar", "ana", "a long local password", "192.0.2.21"); rec.Code != http.StatusSeeOther || rec.Result().Cookies() == nil {
		t.Errorf("POST /entrar: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "http://example.test/entrar/forgot", strings.NewReader("email=ana%40example.test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := e.do(req, nil); rec.Code != 200 {
		t.Fatalf("forgot: %d", rec.Code)
	}
	m := regexp.MustCompile(`http://example\.test(/[a-z/]+)\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(box.String())
	if m == nil || m[1] != "/entrar/reset" {
		t.Fatalf("reset e-mail link: %v\n%s", m, box.String())
	}
	if body := e.get("/entrar/reset?token="+m[2], nil).Body.String(); strings.Contains(body, "invalid or has expired") || !strings.Contains(body, `action="/entrar/reset"`) {
		t.Error("the reset link does not open the reset page")
	}
	e.sameAsUnknown("GET", "/reset-password?token="+m[2])
}

func TestSignInAddressRefused(t *testing.T) {
	ctx := context.Background()
	e, _ := signInEnv(t)
	e.setLangs("es", "en")
	admin := e.userID("admin")
	pub := time.Now().Add(-time.Hour)
	for _, p := range []store.PostInput{
		{Type: store.TypePage, Status: store.StatusPublished, Title: "Contacto", Slug: "contacto", Lang: "es", AuthorID: admin, CommentStatus: "closed", PublishedAt: &pub},
		{Type: store.TypePage, Status: store.StatusPublished, Title: "Team", Slug: "team", Lang: "en", AuthorID: admin, CommentStatus: "closed", PublishedAt: &pub},
		{Type: store.TypePage, Status: store.StatusDraft, Title: "Borrador", Slug: "borrador", Lang: "es", AuthorID: admin, CommentStatus: "closed"},
	} {
		if _, err := e.st.CreatePost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range []string{"/viejo", "/equipo/acceso/antiguo"} {
		if err := e.st.SaveRedirect(ctx, src, "/", 301, false); err != nil {
			t.Fatal(err)
		}
	}
	taken := []string{
		// The site's own addresses, and under them.
		"/admin", "/admin/entrar", "/api", "/static", "/uploads", "/themes", "/plugin-assets", "/plugin-forms",
		"/feed", "/feeds", "/feed-json", "/sitemap", "/sitemaps", "/llms", "/llms-full", "/ask", "/mcp", "/sso", "/sso/entrar",
		"/blog", "/blog/entrar", "/search", "/tag", "/category", "/author", "/archive", "/page", "/page/two", "/register",
		"/logout", "/setup", "/forgot-password", "/reset-password", "/healthz", "/billing", "/wp-admin", "/login/other",
		// Language prefixes, configured or not (the others are too short).
		"/pt-br", "/pt-br/entrar",
		// A page, in any language or status (the form drops English), and
		// redirects at or under the address.
		"/contacto", "/team", "/borrador", "/viejo", "/equipo", "/equipo/acceso",
	}
	bad := []string{"/og", "/en", "/ab", "/a_b", "/entrar/x/y", "/-abc", "/über", "/entrar.php", "/" + strings.Repeat("a", 42), "/a b"}
	for _, p := range append(taken, bad...) {
		if free, err := SignInPathFree(ctx, e.st, p); err != nil || free {
			t.Errorf("SignInPathFree(%s) = %v, %v", p, free, err)
		}
		rec := e.postForm("/admin/settings", e.signInForm(p))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("save %s: %d", p, rec.Code)
			continue
		}
		msg := html.EscapeString(e.s.t("%s is taken: the site uses it for one of its own addresses, a page or a redirect. Choose another sign-in address.", p))
		if !strings.Contains(strings.Join(taken, " ")+" ", p+" ") {
			msg = e.s.t("The sign-in address must be one or two words of lowercase letters, numbers and dashes, of 3 to 41 characters each, such as /entrar.")
		}
		if !strings.Contains(rec.Body.String(), msg) {
			t.Errorf("save %s: no %q message", p, msg)
		}
	}
	if got := e.s.settings().LoginPath; got != "/login" {
		t.Errorf("saved %q", got)
	}
	for _, p := range []string{"/entrar", "/equipo/acceso-nuevo", "/viejo/entrar", "/team/access", "/login", "/acceso-123", "/es-mx"} {
		if free, err := SignInPathFree(ctx, e.st, p); err != nil || !free {
			t.Errorf("SignInPathFree(%s) = %v, %v", p, free, err)
		}
	}

	// Then neither a redirect nor a page can take it.
	e.setSignIn("/equipo/acceso-nuevo", false)
	for _, src := range []string{"/equipo/acceso-nuevo", "/Equipo/Acceso-Nuevo/", "/equipo/acceso-nuevo/forgot", "/equipo/acceso-nuevo?x=1"} {
		if rec := e.postRedirect(src, "/elsewhere", 301); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("redirect from %s: %d", src, rec.Code)
		}
	}
	if rec := e.postRedirect("/equipo", "/elsewhere", 301); rec.Code != http.StatusSeeOther {
		t.Errorf("redirect from /equipo: %d", rec.Code)
	}
	e.setSignIn("/entrar", false)
	if slug, err := e.s.uniqueSlug(ctx, store.TypePage, "es", "entrar", "", 0); err != nil || slug != "entrar-2" {
		t.Errorf("page slug %q, %v", slug, err)
	}
	if slug, err := e.s.uniqueSlug(ctx, store.TypePost, "es", "entrar", "", 0); err != nil || slug != "entrar" {
		t.Errorf("post slug %q, %v", slug, err)
	}
}

// TestSignInReservesRoutes keeps signInReserved in step with routes.go:
// every first segment of a route is refused as a sign-in address.
func TestSignInReservesRoutes(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range regexp.MustCompile(`"(?:[A-Z]+ | )/([a-z0-9][a-z0-9-]*)[/"{]`).FindAllStringSubmatch(string(src), -1) {
		seen++
		if p := "/" + m[1]; sitecheck.LoginPath.MatchString(p) && !signInReserved(p) {
			t.Errorf("the route %s is not reserved", p)
		}
	}
	if seen < 40 {
		t.Fatalf("found %d routes only", seen)
	}
}

func TestSignInHideAdmin(t *testing.T) {
	ctx := context.Background()
	e, _ := signInEnv(t)
	e.setSignIn("/entrar", true)
	// Whatever the address and method: no redirect, no 405 that would
	// tell an address of the administration from one that does not exist.
	e.sameAsUnknown("GET", "/admin")
	e.sameAsUnknown("POST", "/admin")
	for _, p := range []string{"/admin/", "/admin/posts", "/admin/settings", "/admin/network", "/admin/tools/backups/restore", "/admin/x/y"} {
		e.sameAsUnknown("GET", p)
		e.sameAsUnknown("POST", p)
	}
	if body := e.get("/", nil).Body.String(); strings.Contains(body, `href="/entrar"`) || strings.Contains(body, `href="/login"`) {
		t.Error("the footer links to the sign-in page")
	}
	if rec := e.get("/entrar", nil); rec.Code != 200 {
		t.Errorf("GET /entrar: %d", rec.Code)
	}
	// Signed in, nothing changes.
	for _, p := range []string{"/admin/", "/admin/settings"} {
		if rec := e.get(p, e.admin); rec.Code != 200 {
			t.Errorf("GET %s signed in: %d", p, rec.Code)
		}
	}
	if rec := e.get("/admin/settings", e.author); rec.Code != http.StatusForbidden {
		t.Errorf("an author on the settings: %d", rec.Code)
	}
	if body := e.get("/admin/settings", e.admin).Body.String(); !strings.Contains(body, `name="hide_admin" value="1" checked`) {
		t.Error("the switch is not on")
	}
	// Halfway, between the password and the second step: on to it.
	token := auth.NewToken(32)
	if err := e.st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: e.userID("admin"), CSRFToken: auth.NewToken(24),
		MFAPending: true, ExpiresAt: time.Now().Add(5 * time.Minute), UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/admin/posts", &http.Cookie{Name: e.s.cookieName(), Value: token}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/entrar/2fa" {
		t.Errorf("halfway: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Off again: the way to the sign-in page is back.
	e.setSignIn("/entrar", false)
	if rec := e.get("/admin/posts", nil); rec.Header().Get("Location") != "/entrar?next=%2Fadmin%2Fposts" {
		t.Errorf("not hidden: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSignInRateLimitAtNewAddress(t *testing.T) {
	e, _ := signInEnv(t)
	e.passwordUser("ana", "a long local password", auth.RoleAuthor)
	e.setSignIn("/equipo/acceso", false)
	// Per account: from many addresses, the account is held.
	limited := 0
	for i := range 12 {
		rec := e.signInAt("/equipo/acceso", "ana", "wrong password", "198.51.100."+string(rune('1'+i%9)))
		if rec.Code == http.StatusTooManyRequests {
			limited = i
			break
		}
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, rec.Code)
		}
	}
	if limited == 0 {
		t.Fatal("the account is never held")
	}
	if rec := e.signInAt("/equipo/acceso", "ana", "a long local password", "198.51.100.200"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("right password while held: %d", rec.Code)
	}
	// Per address: one client trying many accounts.
	for i := range 30 {
		rec := e.signInAt("/equipo/acceso", "nobody"+string(rune('a'+i)), "x", "203.0.113.9")
		if rec.Code == http.StatusTooManyRequests {
			return
		}
	}
	t.Error("one address is never held")
}

func TestSignInFollowsTheCommandLine(t *testing.T) {
	ctx := context.Background()
	e, _ := signInEnv(t)
	e.get("/", nil) // cached, linking to /login
	st, err := e.st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.LoginPath = "/acceso"
	if err := e.st.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/acceso", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before the minute: %d", rec.Code)
	}
	e.s.followSettings(ctx)
	if rec := e.get("/acceso", nil); rec.Code != 200 {
		t.Errorf("GET /acceso: %d", rec.Code)
	}
	e.sameAsUnknown("GET", "/login")
	if body := e.get("/", nil).Body.String(); !strings.Contains(body, `href="/acceso"`) {
		t.Error("the cached home page still links to /login")
	}
	stamp := e.s.state.Load().Stamp
	e.s.followSettings(ctx) // unchanged: nothing to do
	if e.s.state.Load().Stamp != stamp {
		t.Error("reloaded without a change")
	}
}
