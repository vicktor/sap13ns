package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// fakeSwitcher is a tenant whose network lists fixed sites per account.
type fakeSwitcher struct {
	fakeSSO
	sites map[int64]*AccountSites
	epoch uint64
	err   error
	calls []int64 // the accounts asked for
}

func (f *fakeSwitcher) SwitcherSites(_ context.Context, account int64) (*AccountSites, error) {
	f.calls = append(f.calls, account)
	if f.err != nil {
		return nil, f.err
	}
	if s, ok := f.sites[account]; ok {
		c := *s
		return &c, nil
	}
	return &AccountSites{PortalURL: "https://sap13ns.site"}, nil
}

func (f *fakeSwitcher) SwitcherEpoch() uint64 { return f.epoch }

var _ SiteSwitcher = (*fakeSwitcher)(nil)

// switcherOf is the switcher's markup on a page, "" without one.
func switcherOf(body string) string {
	i := strings.Index(body, `<div id="site-switcher"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], "</header>")
	if j < 0 {
		return body[i:]
	}
	return body[i : i+j]
}

// adminGet opens a page with the cookies an earlier answer set.
func adminGet(t *testing.T, h http.Handler, target string, from *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	for _, c := range from.Result().Cookies() {
		if c.MaxAge >= 0 && c.Value != "" {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func tenantSites() map[int64]*AccountSites {
	return map[int64]*AccountSites{42: {
		PortalURL:  "https://sap13ns.site",
		CanRequest: true,
		Sites: []SwitcherSite{
			{ID: 7, Slug: "blog", Title: "Blog of Nina", Host: "blog.sap13ns.site", URL: "https://blog.sap13ns.site", Role: "admin", Status: "active", Current: true},
			{ID: 9, Slug: "shop", Title: "The <Shop>", Host: "www.shop.example", URL: "https://www.shop.example", Role: "author", Status: "pending"},
			{ID: 11, Slug: "notes", Host: "notes.sap13ns.site", URL: "https://notes.sap13ns.site", Role: "editor", Status: "active"},
		},
	}}
}

// A tenant's admin shows the account's sites, each opened through single
// sign-on on its own host, in the site's language.
func TestSwitcherTenant(t *testing.T) {
	st := testutil.Store(t)
	sw := &fakeSwitcher{fakeSSO: fakeSSO{grant: SSOGrant{AccountID: 42, Role: "admin", Username: "nina", Email: "nina@example.com"}},
		sites: tenantSites()}
	s := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sw })
	h := s.Handler()
	signed := ssoSignIn(t, h)
	if signed.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", signed.Code)
	}
	rec := adminGet(t, h, "https://blog.sap13ns.site/admin/", signed)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	sw2 := switcherOf(rec.Body.String())
	if sw2 == "" {
		t.Fatal("no switcher on a tenant's admin")
	}
	for _, want := range []string{
		// The site itself, by name and host, marked current.
		`<strong>` + s.settings().SiteTitle + `</strong><small>blog.sap13ns.site</small>`,
		`href="/admin/" class="site-switcher-item" aria-current="true"`,
		// The others, through /sso/start on their own hosts.
		`href="https://www.shop.example/sso/start?next=%2Fadmin%2F"`,
		`href="https://notes.sap13ns.site/sso/start?next=%2Fadmin%2F"`,
		// Names escaped, the slug when a site has no title.
		`<strong>The &lt;Shop&gt;</strong>`, `<strong>notes</strong>`,
		// The role, the status, the portal: in the site's language.
		"Tus sitios", "Autor", "Pendiente de aprobación",
		`href="https://sap13ns.site/admin/sites"`, `href="https://sap13ns.site/admin/sites#request"`,
	} {
		if !strings.Contains(sw2, want) {
			t.Errorf("the switcher lacks %s:\n%s", want, sw2)
		}
	}
	if n := strings.Count(sw2, `class="site-switcher-item"`); n != 3 {
		t.Errorf("%d sites listed, want the account's 3", n)
	}
	if strings.Contains(sw2, "/admin/network/") || strings.Count(sw2, "/sso/start") != 2 {
		t.Errorf("unexpected links:\n%s", sw2)
	}
	if strings.Contains(rec.Body.String(), `class="brand"`) {
		t.Error("the plain brand is shown with the switcher")
	}
	if len(sw.calls) != 1 || sw.calls[0] != 42 {
		t.Fatalf("asked for %v, want the session's account 42", sw.calls)
	}

	// A network admin also gets the network admin area.
	sw.sites[42].NetworkAdmin = true
	sw.epoch++
	if got := switcherOf(adminGet(t, h, "https://blog.sap13ns.site/admin/", signed).Body.String()); !strings.Contains(got,
		`href="https://sap13ns.site/admin/network/sites"`) || !strings.Contains(got, "Administración de la red") {
		t.Errorf("a network admin's switcher:\n%s", got)
	}
}

// The list is cached per session for switcherTTL, and for one epoch of
// the network.
func TestSwitcherCache(t *testing.T) {
	st := testutil.Store(t)
	sw := &fakeSwitcher{fakeSSO: fakeSSO{grant: SSOGrant{AccountID: 42, Role: "editor", Username: "nina", Email: "nina@example.com"}},
		sites: tenantSites()}
	s := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sw })
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.switchCache.now = func() time.Time { return now }
	h := s.Handler()
	signed := ssoSignIn(t, h)
	page := func() string {
		t.Helper()
		rec := adminGet(t, h, "https://blog.sap13ns.site/admin/posts", signed)
		if rec.Code != http.StatusOK {
			t.Fatalf("admin: %d", rec.Code)
		}
		return switcherOf(rec.Body.String())
	}
	page()
	page()
	if len(sw.calls) != 1 {
		t.Fatalf("%d queries for two pages", len(sw.calls))
	}
	now = now.Add(switcherTTL - time.Second)
	page()
	if len(sw.calls) != 1 {
		t.Fatal("the list was read again before it expired")
	}
	now = now.Add(time.Second)
	page()
	if len(sw.calls) != 2 {
		t.Fatalf("%d queries once the list expired, want 2", len(sw.calls))
	}

	// A change of the network (a new epoch) shows at once.
	sw.sites[42].Sites = sw.sites[42].Sites[:1]
	if got := page(); strings.Count(got, `class="site-switcher-item"`) != 3 || len(sw.calls) != 2 {
		t.Fatalf("the cached list was not used: %d queries", len(sw.calls))
	}
	sw.epoch++
	if got := page(); strings.Count(got, `class="site-switcher-item"`) != 1 || len(sw.calls) != 3 {
		t.Fatalf("a new epoch did not read the list again: %d queries\n%s", len(sw.calls), got)
	}

	// Another session of the same account has its own entry.
	other := ssoSignIn(t, h)
	adminGet(t, h, "https://blog.sap13ns.site/admin/posts", other)
	if len(sw.calls) != 4 {
		t.Fatalf("%d queries after a second session, want 4", len(sw.calls))
	}

	// A failure is not cached, and the page goes on without the switcher.
	sw.err = errors.New("database down")
	sw.epoch++
	if got := page(); got != "" {
		t.Fatalf("a switcher without its list:\n%s", got)
	}
	sw.err = nil
	if got := page(); got == "" || len(sw.calls) != 6 {
		t.Fatalf("after the failure: %d queries", len(sw.calls))
	}
}

// A tenant knows the account only through a session that came through the
// portal: a linked user signed in with the site's own password sees no
// switcher, and the network is not asked.
func TestSwitcherNeedsPortalSession(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	hash, err := auth.HashPassword("a long local password")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateUser(ctx, store.NewUser{Username: "lena", Email: "lena@example.com", Role: "editor", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkNetworkAccount(ctx, id, 42); err != nil {
		t.Fatal(err)
	}
	sw := &fakeSwitcher{fakeSSO: fakeSSO{grant: SSOGrant{AccountID: 42, Role: "editor", Username: "lena", Email: "lena@example.com"}},
		sites: tenantSites()}
	s := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sw })
	h := s.Handler()
	req := httptest.NewRequest("POST", "https://blog.sap13ns.site/login", strings.NewReader("login=lena&password=a+long+local+password"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "192.0.2.20:4000"
	signed := httptest.NewRecorder()
	h.ServeHTTP(signed, req)
	if signed.Code != http.StatusSeeOther {
		t.Fatalf("password sign-in: %d", signed.Code)
	}
	// Its profile: the role needs a second factor the user has not set up.
	rec := adminGet(t, h, "https://blog.sap13ns.site/admin/profile", signed)
	if rec.Code != http.StatusOK || switcherOf(rec.Body.String()) != "" || !strings.Contains(rec.Body.String(), `class="brand"`) {
		t.Fatalf("a password session: %d, switcher %q", rec.Code, switcherOf(rec.Body.String()))
	}
	if len(sw.calls) != 0 {
		t.Fatalf("the network was asked for %v", sw.calls)
	}
	// Through the portal, the same user has it.
	if got := switcherOf(adminGet(t, h, "https://blog.sap13ns.site/admin/posts", ssoSignIn(t, h)).Body.String()); got == "" {
		t.Fatal("no switcher after signing in through the portal")
	}
}

// A single site has no switcher and asks nothing for one. A network whose
// hooks do not list sites shows the plain header too.
func TestSwitcherSingleSite(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	id, err := st.CreateUser(ctx, store.NewUser{Username: "ada", Email: "ada@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTOTP(ctx, id, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	session := func(s *Server) *httptest.ResponseRecorder {
		token := auth.NewToken(32)
		if err := st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: id, CSRFToken: "c",
			ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		http.SetCookie(rec, &http.Cookie{Name: s.cookieName(), Value: token, Path: "/"})
		return rec
	}

	single := networkServer(t, st, nil)
	if single.switcher != nil {
		t.Fatal("a single site has a switcher")
	}
	h, signed := single.Handler(), session(single)
	for _, p := range []string{"/admin/", "/admin/posts", "/admin/system"} {
		rec := adminGet(t, h, "https://blog.sap13ns.site"+p, signed)
		if rec.Code != http.StatusOK || switcherOf(rec.Body.String()) != "" || !strings.Contains(rec.Body.String(), `class="brand"`) {
			t.Fatalf("%s on a single site: %d, switcher %q", p, rec.Code, switcherOf(rec.Body.String()))
		}
	}

	// Nil-safe: hooks without the switcher's method.
	hooks := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = &fakeHooks{} })
	if hooks.switcher != nil {
		t.Fatal("hooks without SwitcherSites became a switcher")
	}
	h = hooks.Handler()
	signed = session(hooks)
	if rec := adminGet(t, h, "https://blog.sap13ns.site/admin/posts", signed); rec.Code != http.StatusOK || switcherOf(rec.Body.String()) != "" {
		t.Fatalf("a network site without the hook: %d", rec.Code)
	}
}
