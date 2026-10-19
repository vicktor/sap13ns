package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"sap13ns/internal/config"
	"sap13ns/internal/i18n"
	"sap13ns/internal/mail"
	"sap13ns/internal/testutil"
	"sap13ns/ui"
)

// The admin language is each user's: the language menu posts the choice,
// which then drives the templates, the strings built in Go and <html lang>,
// while other users and the public site keep the site's language (es).
func TestAdminLanguageChoice(t *testing.T) {
	e := newCustomEnv(t)
	if l := e.s.lang(); l != "es" {
		t.Fatalf("site language %q, want the default es", l)
	}
	lang := func(name string) string { return e.user(name).Language }
	choose := func(cookie *http.Cookie, csrf, l, next string) *http.Response {
		t.Helper()
		form := url.Values{"lang": {l}, "next": {next}}
		if csrf != "" {
			form.Set("csrf", csrf)
		}
		return e.postAs("/admin/profile/language", cookie, form).Result()
	}

	page := e.get("/admin/posts", e.admin).Body.String()
	for _, want := range []string{`<html lang="es" dir="ltr">`, "<title>Entradas ·", `action="/admin/profile/language"`,
		`name="next" value="/admin/posts"`, `role="menuitemradio" aria-checked="true" form="lang-form" name="lang" value="es"`,
		`role="menuitemradio" aria-checked="false" form="lang-form" name="lang" value="en"`} {
		if !strings.Contains(page, want) {
			t.Errorf("before choosing, the posts page lacks %q", want)
		}
	}

	// The form needs the CSRF token.
	if res := choose(e.admin, "", "en", "/admin/posts"); res.StatusCode != http.StatusForbidden || lang("admin") != "" {
		t.Fatalf("without CSRF: %d, language %q", res.StatusCode, lang("admin"))
	}
	// A language without a catalog, or not a language at all, is refused,
	// in the user's language.
	for _, bad := range []string{"fr", "xx", "en-gb", "../es", strings.Repeat("e", 300)} {
		rec := e.postAs("/admin/profile/language", e.admin, url.Values{"csrf": {e.csrf}, "lang": {bad}, "next": {"/admin/"}})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Ese idioma no está disponible.") || lang("admin") != "" {
			t.Errorf("lang %q: %d, language %q", bad, rec.Code, lang("admin"))
		}
	}

	res := choose(e.admin, e.csrf, "en", "/admin/posts?status=draft")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/posts?status=draft" {
		t.Fatalf("choose en: %d to %q", res.StatusCode, res.Header.Get("Location"))
	}
	if lang("admin") != "en" {
		t.Fatalf("stored language %q, want en", lang("admin"))
	}
	page = e.get("/admin/posts", e.admin).Body.String()
	for _, want := range []string{`<html lang="en" dir="ltr">`, "<title>Posts ·", ">Content<", `aria-label="Toggle dark mode"`,
		`aria-checked="true" form="lang-form" name="lang" value="en"`} {
		if !strings.Contains(page, want) {
			t.Errorf("after choosing en, the posts page lacks %q", want)
		}
	}
	if strings.Contains(page, "Entradas") {
		t.Error("after choosing en, the posts page still has Spanish")
	}

	// Strings built in Go follow it too: a flash after a save.
	rec := e.postAs("/admin/profile", e.admin, url.Values{"csrf": {e.csrf}, "email": {"admin@example.test"}, "display_name": {"Admin"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("profile save: %d", rec.Code)
	}
	get := httptest.NewRequest("GET", "http://example.test/admin/profile", nil)
	for _, c := range rec.Result().Cookies() { // the flash
		get.AddCookie(c)
	}
	prof := e.do(get, e.admin).Body.String()
	for _, want := range []string{"<h2>Saved.</h2>", `<input type="radio" class="input" name="lang" value="en" checked>`,
		"The site&#39;s language (Español)", `name="next" value="/admin/profile#language"`} {
		if !strings.Contains(prof, want) {
			t.Errorf("the profile after a save lacks %q", want)
		}
	}

	// Another user, and the public site, keep the site's language.
	if page := e.get("/admin/posts", e.author).Body.String(); !strings.Contains(page, `<html lang="es"`) || !strings.Contains(page, "<title>Entradas ·") {
		t.Error("the author's admin is not in the site's language")
	}
	if home := e.get("/", e.admin).Body.String(); !strings.Contains(home, `lang="es"`) || strings.Contains(home, `<html lang="en"`) {
		t.Error("the public site followed the admin language")
	}

	// next goes through safeNext; "" goes back to the site's language.
	for _, next := range []string{"https://evil.example/", "//evil.example/admin", "javascript:alert(1)"} {
		if res := choose(e.admin, e.csrf, "en", next); res.Header.Get("Location") != "/admin/" {
			t.Errorf("next %q: redirected to %q", next, res.Header.Get("Location"))
		}
	}
	if res := choose(e.admin, e.csrf, "", "/admin/"); res.StatusCode != http.StatusSeeOther || lang("admin") != "" {
		t.Fatalf("back to the site's language: %d, %q", res.StatusCode, lang("admin"))
	}
	if page := e.get("/admin/", e.admin).Body.String(); !strings.Contains(page, `<html lang="es"`) || !strings.Contains(page, "Escritorio") {
		t.Error("with no choice, the admin is not in the site's language")
	}

	// A choice the binary has no catalog for (an older binary, a removed
	// catalog) falls back to the site's language.
	if err := e.st.SetUserLanguage(context.Background(), e.user("admin").ID, "fr"); err != nil {
		t.Fatal(err)
	}
	if page := e.get("/admin/", e.admin).Body.String(); !strings.Contains(page, `<html lang="es"`) {
		t.Error("an unknown stored language did not fall back to the site's")
	}
}

// The sign-in pages take ?lang=, without storing anything, and carry it in
// their forms and links; the language menu keeps next.
func TestSignInLanguage(t *testing.T) {
	e := newCustomEnv(t)
	e.s.mailer = mail.New(config.SMTP{}, e.s.log, false) // the sign-in page asks it
	page := e.get("/login?lang=en&next=%2Fadmin%2Fposts", nil)
	body := page.Body.String()
	for _, want := range []string{`<html lang="en" dir="ltr">`, ">Log in<", `action="/login?lang=en"`,
		`href="/login?lang=es&amp;next=%2Fadmin%2Fposts"`, `aria-checked="true" href="/login?lang=en&amp;next=%2Fadmin%2Fposts"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/login?lang=en lacks %q", want)
		}
	}
	if len(page.Result().Cookies()) != 0 {
		t.Error("?lang= set a cookie")
	}
	for _, target := range []string{"/login", "/login?lang=zz", "/login?lang=fr"} {
		if body := e.get(target, nil).Body.String(); !strings.Contains(body, `<html lang="es"`) || !strings.Contains(body, "Iniciar sesión") ||
			!strings.Contains(body, `action="/login"`) {
			t.Errorf("%s is not in the site's language", target)
		}
	}
	// A failed sign-in answers in the page's language.
	rec := e.postAs("/login?lang=en", nil, url.Values{"login": {"nobody"}, "password": {"wrong-password-123"}, "next": {"/admin/"}})
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Invalid username or password.") ||
		!strings.Contains(rec.Body.String(), `href="/login?lang=es&amp;next=%2Fadmin%2F"`) {
		t.Errorf("failed sign-in with ?lang=en: %d", rec.Code)
	}
	// The admin pages ignore ?lang=: only the user's choice counts there.
	if body := e.get("/admin/?lang=en", e.admin).Body.String(); !strings.Contains(body, `<html lang="es"`) {
		t.Error("?lang= changed the language of an admin page")
	}

	// At a sign-in address of the site's own choosing (signin.go), the
	// same, under it.
	e.setSignIn("/entrar", false)
	for target, wants := range map[string][]string{
		"/entrar?lang=en": {">Log in<", `action="/entrar?lang=en"`, `href="/entrar/forgot?lang=en"`, `href="/entrar?lang=es"`},
		"/entrar/forgot?lang=en": {" Back to log in</a>", `action="/entrar/forgot?lang=en"`, `href="/entrar?lang=en"`,
			`href="/entrar/forgot?lang=es"`},
	} {
		body := e.get(target, nil).Body.String()
		if !strings.Contains(body, `<html lang="en" dir="ltr">`) {
			t.Errorf("%s is not in English", target)
		}
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", target, want)
			}
		}
	}
	if body := e.get("/entrar", nil).Body.String(); !strings.Contains(body, `<html lang="es"`) || !strings.Contains(body, `action="/entrar"`) {
		t.Error("/entrar is not in the site's language")
	}
}

// On a site of a network, a user signed in through single sign-on chooses
// the language there, and the next sign-in, which syncs the user with the
// network account, keeps it.
func TestAdminLanguageSSO(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	sso := &fakeSSO{grant: SSOGrant{AccountID: 42, Role: "editor", Username: "nina", Email: "nina@example.com", DisplayName: "Nina"}}
	h := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sso }).Handler()
	signed := ssoSignIn(t, h)
	page := adminGet(t, h, "https://blog.sap13ns.site/admin/posts", signed).Body.String()
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `<html lang="es"`) {
		t.Fatal("no CSRF token, or not in the site's language")
	}
	req := httptest.NewRequest("POST", "https://blog.sap13ns.site/admin/profile/language",
		strings.NewReader(url.Values{"csrf": {m[1]}, "lang": {"en"}, "next": {"/admin/posts"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range signed.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/posts" {
		t.Fatalf("choose en: %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	again := ssoSignIn(t, h)
	if u, err := st.UserByNetworkAccount(ctx, 42); err != nil || u.Language != "en" {
		t.Fatalf("after signing in again: %+v %v", u, err)
	}
	if page := adminGet(t, h, "https://blog.sap13ns.site/admin/posts", again).Body.String(); !strings.Contains(page, `<html lang="en"`) ||
		!strings.Contains(page, "<title>Posts ·") {
		t.Error("the new session is not in the chosen language")
	}
}

func TestUILanguages(t *testing.T) {
	for lang, want := range map[string]string{"en": "ltr", "es": "ltr", "ar": "rtl", "he": "rtl", "pt-br": "ltr", "": "ltr"} {
		if got := langDir(lang); got != want {
			t.Errorf("langDir(%q) = %q, want %q", lang, got, want)
		}
	}
	s := &Server{}
	var err error
	if s.i18n, err = i18n.Load(ui.FS, "i18n"); err != nil {
		t.Fatal(err)
	}
	got := s.uiLanguages("es")
	if len(got) < 2 || got[0].Code != "en" || got[0].Name != "English" || got[0].Current || got[1].Code != "es" || got[1].Name != "Español" || !got[1].Current {
		t.Errorf("uiLanguages: %+v", got)
	}
	for _, l := range []string{"", "fr", "xx", "EN"} {
		if s.uiLanguage(l) {
			t.Errorf("%q is an admin language", l)
		}
	}
}
