package network

import (
	"net/http"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// signInAt gives a site a sign-in address before the registry loads it.
func (e *netEnv) signInAt(s *Site, p string, hide bool) {
	e.t.Helper()
	st := e.siteStore(s)
	set, err := st.LoadSettings(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	set.LoginPath, set.HideAdmin = p, hide
	if err := st.SaveSettings(e.ctx, set); err != nil {
		e.t.Fatal(err)
	}
}

// TestSignInAddressOnNetwork: the portal and each site have a sign-in
// address of their own; single sign-on, and with it the links of the
// switcher and My sites (/sso/start), does not depend on either.
func TestSignInAddressOnNetwork(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) {
		portalSettings(t, o, func(s *store.Settings) { s.LoginPath, s.HideAdmin = "/cuenta", true })
	})
	a := e.tenant("alpha", "pro")
	e.signInAt(a, "/acceso", true)
	e.mfa(e.owner)

	b := e.browser()
	rec := b.get("https://alpha.sap13ns.site/acceso?next=/admin/posts")
	if body := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(body, `action="/acceso"`) ||
		!strings.Contains(body, `href="/sso/start?next=%2fadmin%2fposts"`) {
		t.Fatalf("tenant sign-in page: %d", rec.Code)
	}
	if rec := b.get("https://sap13ns.site/cuenta"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `action="/cuenta"`) {
		t.Fatalf("portal sign-in page: %d", rec.Code)
	}
	for _, u := range []string{"https://alpha.sap13ns.site/login", "https://alpha.sap13ns.site/cuenta", "https://sap13ns.site/login",
		"https://sap13ns.site/acceso", "https://alpha.sap13ns.site/admin/posts", "https://sap13ns.site/admin/sites"} {
		if rec := b.get(u); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %s", u, rec.Code, rec.Header().Get("Location"))
		}
	}

	// The switcher's link: the portal's sign-in first, at its address,
	// although its administration is hidden; then back to the site's.
	authURL := b.startSSO("alpha.sap13ns.site", "/admin/posts")
	if rec := b.get(authURL); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/cuenta?next=%2Fsso%2Fauthorize%3F") {
		t.Fatalf("authorize without a portal session: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	b.portalSignIn(e.owner)
	if rec := b.get(b.authorize(authURL)); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/posts" {
		t.Fatalf("callback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := b.get("https://alpha.sap13ns.site/admin/posts"); rec.Code != http.StatusOK {
		t.Fatalf("admin after SSO: %d", rec.Code)
	}
	if rec := b.get("https://sap13ns.site/admin/sites"); rec.Code != http.StatusOK {
		t.Fatalf("My sites, signed in: %d", rec.Code)
	}
}

// TestSignInAddressNotOpen: a site waiting for approval answers its own
// sign-in pages, wherever they are, and the network's status page to the
// rest, /login included once it moved.
func TestSignInAddressNotOpen(t *testing.T) {
	e := newNetEnv(t, nil)
	g := e.tenant("gamma", "personal")
	e.signInAt(g, "/acceso", false)
	if e.status(g.ID) != StatusPending {
		t.Fatalf("gamma is %s", e.status(g.ID))
	}
	for _, p := range []string{"/acceso", "/acceso/forgot", "/acceso/reset?token=x"} {
		if rec := e.do("GET", "https://gamma.sap13ns.site"+p); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
	if rec := e.do("GET", "https://gamma.sap13ns.site/admin/"); rec.Code != http.StatusSeeOther ||
		!strings.HasPrefix(rec.Header().Get("Location"), "/acceso?next=") {
		t.Errorf("admin: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/", "/login", "/forgot-password", "/login/forgot", "/acceso/x", "/blog/x", "/no-such-page", "/team/access"} {
		rec := e.do("GET", "https://gamma.sap13ns.site"+p)
		if body := rec.Body.String(); rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, `href="/sso/start"`) ||
			strings.Contains(body, `name="password"`) {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}

	// Hidden from visitors: the status page there too.
	d := e.tenant("delta", "personal")
	e.signInAt(d, "/acceso", true)
	if rec := e.do("GET", "https://delta.sap13ns.site/admin/"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("hidden admin of a site not open: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("GET", "https://delta.sap13ns.site/acceso"); rec.Code != http.StatusOK {
		t.Errorf("its sign-in page: %d", rec.Code)
	}
}
