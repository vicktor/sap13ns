package network

import (
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// switcherOf is the site switcher's markup on an admin page, "" without one.
func switcherOf(body string) string {
	i := strings.Index(body, `<div id="site-switcher"`)
	if i < 0 {
		return ""
	}
	if j := strings.Index(body[i:], "</header>"); j >= 0 {
		return body[i : i+j]
	}
	return body[i:]
}

var hrefRe = regexp.MustCompile(`href="([^"]+)"`)

// switcherLinks lists the links of a switcher, unescaped.
func switcherLinks(sw string) []string {
	var out []string
	for _, m := range hrefRe.FindAllStringSubmatch(sw, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

func (e *netEnv) setTitle(s *Site, title string) {
	e.t.Helper()
	st := e.siteStore(s)
	set, err := st.LoadSettings(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	set.SiteTitle = title
	if err := st.SaveSettings(e.ctx, set); err != nil {
		e.t.Fatal(err)
	}
}

// open follows a link of the switcher to another site's admin: single
// sign-on through the portal, then the admin.
func (b *browser) open(link string) string {
	b.e.t.Helper()
	rec := b.get(link)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, portalURL+"/sso/authorize?") {
		b.e.t.Fatalf("open %s: %d %s", link, rec.Code, loc)
	}
	cb := b.authorize(loc)
	rec = b.get(cb)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		b.e.t.Fatalf("callback of %s: %d %s", link, rec.Code, rec.Header().Get("Location"))
	}
	origin := link[:strings.Index(link, "/sso/start")]
	rec = b.get(origin + "/admin/posts")
	if rec.Code != http.StatusOK {
		b.e.t.Fatalf("admin of %s: %d %s", origin, rec.Code, rec.Header().Get("Location"))
	}
	return rec.Body.String()
}

// TestSiteSwitcher: the portal and every site list exactly the sites the
// account is a member of and opens, and each entry opens its site through
// single sign-on.
func TestSiteSwitcher(t *testing.T) {
	e, _ := newOpenPortal(t)
	alpha, beta := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	other := e.account("other")
	gamma := e.site("gamma", other, "pro") // the owner is no member
	delta := e.site("delta", e.owner, "pro")
	if err := e.n.SuspendSite(e.ctx, delta.ID, "spam"); err != nil {
		t.Fatal(err)
	}
	pend := e.site("pend", e.owner, "personal") // waits for approval: still opened
	e.refresh()
	e.setTitle(alpha, "Alpha Journal")
	e.setTitle(beta, "Beta <Shop>")
	e.setTitle(gamma, "Gamma")
	e.setTitle(pend, "Pending Notes")
	const (
		alphaLink = "https://alpha.sap13ns.site/sso/start?next=%2Fadmin%2F"
		betaLink  = "https://beta.sap13ns.site/sso/start?next=%2Fadmin%2F"
		pendLink  = "https://pend.sap13ns.site/sso/start?next=%2Fadmin%2F"
		gammaLink = "https://gamma.sap13ns.site/sso/start?next=%2Fadmin%2F"
	)

	// The portal: the account's sites by name, not the suspended one nor
	// one it is not a member of. It owns a Personal site already, so it
	// cannot request one.
	b := e.browser()
	b.portalSignIn(e.owner)
	sw := switcherOf(b.page("/admin/sites"))
	links := switcherLinks(sw)
	want := []string{alphaLink, betaLink, pendLink, "/admin/sites"}
	if strings.Join(links, " ") != strings.Join(want, " ") {
		t.Fatalf("portal switcher links %v, want %v\n%s", links, want, sw)
	}
	for _, s := range []string{"<strong>Alpha Journal</strong>", "<strong>Beta &lt;Shop&gt;</strong>", "Pendiente de aprobación", "Tus sitios"} {
		if !strings.Contains(sw, s) {
			t.Errorf("portal switcher lacks %s", s)
		}
	}
	if strings.Contains(sw, "aria-current") || strings.Contains(sw, "gamma") || strings.Contains(sw, "delta") {
		t.Fatalf("portal switcher:\n%s", sw)
	}

	// A portal account sees its sites once it has a second factor, as
	// with "My sites".
	bare := e.bareAccount("bare")
	if err := e.n.SetMember(e.ctx, alpha.ID, bare, "editor", 0); err != nil {
		t.Fatal(err)
	}
	nb := e.browser()
	nb.portalSignIn(bare)
	if sw := switcherOf(nb.page("/admin/profile/2fa")); sw != "" {
		t.Fatalf("an account without a second factor has a switcher:\n%s", sw)
	}
	e.mfa(bare)
	if sw := switcherOf(nb.page("/admin/sites")); strings.Join(switcherLinks(sw), " ") != alphaLink+" /admin/sites /admin/sites#request" {
		t.Fatalf("its switcher once it has one: %v", switcherLinks(sw))
	}

	// Into alpha through its entry: alpha is the current site there, and
	// the others go through the portal again.
	sw = switcherOf(b.open(alphaLink))
	links = switcherLinks(sw)
	want = []string{"/admin/", betaLink, pendLink, portalURL + "/admin/sites"}
	if strings.Join(links, " ") != strings.Join(want, " ") {
		t.Fatalf("alpha's switcher links %v, want %v\n%s", links, want, sw)
	}
	if !strings.Contains(sw, `<strong>Alpha Journal</strong><small>alpha.sap13ns.site</small>`) || !strings.Contains(sw, `aria-current="true"`) {
		t.Fatalf("alpha's switcher does not show alpha as the current site:\n%s", sw)
	}

	// A membership given meanwhile shows at once, with its role; removed,
	// it goes at once.
	if err := e.n.SetMember(e.ctx, gamma.ID, e.owner, "author", 0); err != nil {
		t.Fatal(err)
	}
	sw = switcherOf(b.page2("https://alpha.sap13ns.site/admin/posts"))
	if !strings.Contains(sw, `href="`+html.EscapeString(gammaLink)+`"`) || !strings.Contains(sw, "Autor") {
		t.Fatalf("a new membership is not listed:\n%s", sw)
	}
	if err := e.n.RemoveMember(e.ctx, gamma.ID, e.owner); err != nil {
		t.Fatal(err)
	}
	if sw = switcherOf(b.page2("https://alpha.sap13ns.site/admin/posts")); strings.Contains(sw, "gamma") {
		t.Fatalf("a removed membership is still listed:\n%s", sw)
	}

	// A change made by another process (here, straight in the table)
	// shows once the host table is reloaded, as on its notification.
	if _, err := e.n.db.Exec(e.ctx, `INSERT INTO site_members (site_id, account_id, role) VALUES ($1, $2, 'editor')`, gamma.ID, e.owner); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if sw = switcherOf(b.page2("https://alpha.sap13ns.site/admin/posts")); !strings.Contains(sw, "Gamma") {
		t.Fatalf("a change of another process is not listed after a reload:\n%s", sw)
	}

	// From alpha to beta, and back to the portal.
	sw = switcherOf(b.open(betaLink))
	if !strings.Contains(sw, `<strong>Beta &lt;Shop&gt;</strong><small>beta.sap13ns.site</small>`) ||
		!strings.Contains(sw, `href="`+html.EscapeString(alphaLink)+`"`) {
		t.Fatalf("beta's switcher:\n%s", sw)
	}
	if rec := b.get(portalURL + "/admin/sites"); rec.Code != http.StatusOK || switcherOf(rec.Body.String()) == "" {
		t.Fatalf("back to the portal: %d", rec.Code)
	}

	// A session with the site's own password, not through the portal,
	// has no switcher (on its profile: it has no second factor there).
	u, err := e.siteStore(alpha).UserByNetworkAccount(e.ctx, e.owner)
	if err != nil {
		t.Fatal(err)
	}
	local := e.browser()
	local.siteSignIn(alpha, u.ID)
	if rec := local.get("https://alpha.sap13ns.site/admin/profile"); rec.Code != http.StatusOK || switcherOf(rec.Body.String()) != "" {
		t.Fatalf("a local session: %d %q", rec.Code, switcherOf(rec.Body.String()))
	}
}

// TestSiteSwitcherOperator: a network admin sees the sites they are a
// member of. A site they open with support access is not listed, even on
// that site; the network admin area is.
func TestSiteSwitcherOperator(t *testing.T) {
	e, _ := newOpenPortal(t)
	alpha, beta := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	e.setTitle(beta, "Beta")
	ops, ob, _ := e.networkAdmin("olga")
	if err := e.n.SetMember(e.ctx, beta.ID, ops, "editor", 0); err != nil {
		t.Fatal(err)
	}
	e.allowSupport(alpha, 24*time.Hour)

	sw := switcherOf(ob.page("/admin/network/sites"))
	links := switcherLinks(sw)
	want := []string{"https://beta.sap13ns.site/sso/start?next=%2Fadmin%2F", "/admin/sites", "/admin/network/sites", "/admin/sites#request"}
	if strings.Join(links, " ") != strings.Join(want, " ") {
		t.Fatalf("a network admin's switcher on the portal: %v, want %v", links, want)
	}
	if !strings.Contains(sw, "Administración de la red") || !strings.Contains(sw, "Solicitar un sitio") {
		t.Fatalf("a network admin's switcher:\n%s", sw)
	}

	if rec := ob.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("operator sign-in: %d", rec.Code)
	}
	if e.operatorSessions(alpha, ops, store.AMRSSOOperator) != 1 {
		t.Fatal("no operator session")
	}
	rec := ob.get("https://alpha.sap13ns.site/admin/posts")
	if rec.Code != http.StatusOK {
		t.Fatalf("operator admin: %d", rec.Code)
	}
	sw = switcherOf(rec.Body.String())
	links = switcherLinks(sw)
	want = []string{"https://beta.sap13ns.site/sso/start?next=%2Fadmin%2F", portalURL + "/admin/sites", portalURL + "/admin/network/sites",
		portalURL + "/admin/sites#request"}
	if strings.Join(links, " ") != strings.Join(want, " ") || strings.Contains(sw, "aria-current") {
		t.Fatalf("an operator's switcher on alpha: %v, want %v\n%s", links, want, sw)
	}
}

// page2 opens a page anywhere in the network and fails unless it answers
// 200.
func (b *browser) page2(target string) string {
	b.e.t.Helper()
	rec := b.get(target)
	if rec.Code != http.StatusOK {
		b.e.t.Fatalf("GET %s: %d %s", target, rec.Code, rec.Header().Get("Location"))
	}
	return rec.Body.String()
}
