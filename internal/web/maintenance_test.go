package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
)

// TestMaintenance: with maintenance mode on, visitors get the maintenance
// page (503) everywhere but sign-in, the administration and assets; the
// people who write on the site browse it as usual; only administrators
// switch it.
func TestMaintenance(t *testing.T) {
	e := newCustomEnv(t)
	sub, _ := e.login("reader", auth.RoleSubscriber)
	set := func(cookie *http.Cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form.Set("csrf", csrf)
		req := httptest.NewRequest("POST", "http://example.test/admin/settings/maintenance", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return e.do(req, cookie)
	}
	if rec := e.get("/", nil); rec.Code != http.StatusOK {
		t.Fatalf("home before: %d", rec.Code)
	}

	// An author cannot turn it on.
	if rec := set(e.author, e.acsrf, url.Values{"action": {"on"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("author: %d", rec.Code)
	}
	if e.s.settings().Maintenance.On {
		t.Fatal("an author turned maintenance mode on")
	}
	// Without the CSRF token, nothing changes.
	if set(e.admin, "wrong", url.Values{"action": {"on"}}); e.s.settings().Maintenance.On {
		t.Fatal("turned on without the CSRF token")
	}
	if rec := set(e.admin, e.csrf, url.Values{"action": {"on"}}); rec.Code != http.StatusSeeOther || !e.s.settings().Maintenance.On {
		t.Fatalf("turn on: %d %v", rec.Code, e.s.settings().Maintenance)
	}

	// Visitors and subscribers get the page with the built-in texts.
	title := html.EscapeString(e.s.t(maintenanceTitle))
	for _, c := range []*http.Cookie{nil, sub} {
		for _, path := range []string{"/", "/blog/hello", "/feed.xml", "/sitemap.xml", "/robots.txt", "/search?q=x"} {
			rec := e.get(path, c)
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
				!strings.Contains(rec.Body.String(), title) {
				t.Fatalf("%s (signed in %v): %d %q", path, c != nil, rec.Code, rec.Header().Get("Retry-After"))
			}
			if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
				t.Errorf("%s: cacheable or indexable: %v", path, rec.Header())
			}
		}
	}
	req := httptest.NewRequest("POST", "http://example.test/api/assistant", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if rec := e.do(req, nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("API during maintenance: %d", rec.Code)
	}
	// Sign-in, the administration and the assets answer everyone.
	if rec := e.get("/login", nil); rec.Code != http.StatusOK {
		t.Errorf("sign-in page: %d", rec.Code)
	}
	if rec := e.get("/admin/", nil); rec.Code == http.StatusServiceUnavailable {
		t.Errorf("admin: %d", rec.Code)
	}
	if rec := e.get("/static/admin.css", nil); rec.Code != http.StatusOK {
		t.Errorf("assets: %d", rec.Code)
	}
	if rec := e.get("/healthz", nil); rec.Code == http.StatusServiceUnavailable {
		t.Errorf("health check: %d", rec.Code)
	}
	// Whoever writes on the site browses it.
	for name, c := range map[string]*http.Cookie{"admin": e.admin, "author": e.author} {
		if rec := e.get("/", c); rec.Code != http.StatusOK {
			t.Errorf("%s during maintenance: %d", name, rec.Code)
		}
	}
	if rec := e.get("/admin/", e.admin); !strings.Contains(rec.Body.String(), `href="/admin/settings/maintenance"`) {
		t.Error("the admin does not show that maintenance mode is on")
	}

	// Own texts, saved without changing the mode; paragraphs split on
	// blank lines.
	if rec := set(e.admin, e.csrf, url.Values{"action": {"save"}, "title": {"Closed for works"}, "text": {"First part.\r\n\r\nSecond <b>part</b>."}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save texts: %d", rec.Code)
	}
	m := e.s.settings().Maintenance
	if !m.On || m.Title != "Closed for works" {
		t.Fatalf("after saving the texts: %+v", m)
	}
	body := e.get("/", nil).Body.String()
	if !strings.Contains(body, "Closed for works") || !strings.Contains(body, "<p>First part.</p>") ||
		!strings.Contains(body, "<p>Second &lt;b&gt;part&lt;/b&gt;.</p>") {
		t.Fatalf("own texts: %s", body)
	}
	if rec := set(e.admin, e.csrf, url.Values{"action": {"save"}, "title": {strings.Repeat("x", 121)}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a title too long: %d", rec.Code)
	}
	if rec := e.get("/admin/settings/maintenance/preview", e.admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Closed for works") {
		t.Errorf("preview: %d", rec.Code)
	}

	// The credit shows as on the public pages: a single-site install and
	// a free plan keep it, a paid plan that leaves it out does not.
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	if !strings.Contains(body, credit) {
		t.Error("a single-site install lacks the credit")
	}
	if !strings.Contains(body, `class="brand-mark"`) || strings.Contains(body, "/site-icon/") {
		t.Error("the page uses a site icon the site does not have")
	}
	e.s.limits = creditLimits{planLimits: planLimits{plan: AIPlan{Plan: "P", Paid: true}}, hide: true}
	e.s.credit.mu.Lock()
	e.s.credit.at = time.Time{}
	e.s.credit.mu.Unlock()
	// The site icon replaces the sap13ns one.
	e.setIcon(42)
	body = e.get("/", nil).Body.String()
	if strings.Contains(body, credit) {
		t.Error("a paid plan without the credit shows it")
	}
	if !strings.Contains(body, `<link rel="icon" href="/site-icon/192.png?v=42"`) || !strings.Contains(body, `class="maintenance-logo" src="/site-icon/192.png?v=42"`) ||
		strings.Contains(body, "brand/favicon-32.png") {
		t.Errorf("the site icon: %s", body)
	}

	// Off: the site is open again, and the texts stay for next time.
	if rec := set(e.admin, e.csrf, url.Values{"action": {"off"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("turn off: %d", rec.Code)
	}
	if m := e.s.settings().Maintenance; m.On || m.Title != "Closed for works" {
		t.Fatalf("after turning off: %+v", m)
	}
	if rec := e.get("/", nil); rec.Code != http.StatusOK {
		t.Fatalf("home after: %d", rec.Code)
	}
}

// During maintenance, an administration hidden from visitors and the
// address of an icon the site does not have are closed like the rest:
// neither shows the theme's page for an unknown address, with the site's
// menus and widgets on it.
func TestMaintenanceClosesUnknownAddresses(t *testing.T) {
	e := newCustomEnv(t)
	ctx := t.Context()
	set := e.s.settings()
	set.Maintenance.On, set.HideAdmin = true, true
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	e.s.ReloadSettings(ctx)
	for _, path := range []string{"/admin", "/admin/", "/admin/posts", "/favicon.ico"} {
		if rec := e.get(path, nil); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}
	// Whoever writes on the site still gets in.
	if rec := e.get("/admin/", e.admin); rec.Code != http.StatusOK {
		t.Errorf("the administrator's admin: %d", rec.Code)
	}
	// The billing webhook of a network's portal is not turned away.
	if !e.s.maintenanceServes(httptest.NewRequest("POST", "http://example.test/billing/webhook", nil)) {
		t.Error("the billing webhook is closed")
	}
	// One piece of an archive fits in a request (transfer.go).
	if n := e.s.maxBody(httptest.NewRequest("POST", "http://example.test/admin/tools/transfer/upload?offset=0", nil)); n < transferChunk {
		t.Errorf("an upload chunk of %d does not fit in a body of %d", transferChunk, n)
	}
}
