package network

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/store"
)

// mailbox collects the e-mail a mailer without SMTP prints to its log.
type mailbox struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (m *mailbox) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.Write(p)
}

func (m *mailbox) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

// portalSettings changes the portal's settings before the registry loads
// it.
func portalSettings(t *testing.T, o *RegistryOptions, change func(*store.Settings)) {
	t.Helper()
	st := store.New(o.Network.DB())
	set, err := st.LoadSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	change(&set)
	if err := st.SaveSettings(context.Background(), set); err != nil {
		t.Fatal(err)
	}
}

// newOpenPortal is a network whose portal takes requests for sites and
// whose mailer prints the e-mail it sends to the mailbox.
func newOpenPortal(t *testing.T) (*netEnv, *mailbox) {
	t.Helper()
	box := &mailbox{}
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Mailer = mail.New(config.SMTP{}, slog.New(slog.NewTextHandler(box, nil)), true)
		portalSettings(t, o, func(s *store.Settings) { s.AllowRegistration = true })
	})
	return e, box
}

const portalURL = "https://sap13ns.site"

// page opens a page of the portal and fails unless it answers 200.
func (b *browser) page(target string) string {
	b.e.t.Helper()
	rec := b.get(portalURL + target)
	if rec.Code != http.StatusOK {
		b.e.t.Fatalf("GET %s: %d %s", target, rec.Code, rec.Header().Get("Location"))
	}
	return rec.Body.String()
}

// post sends a form to the portal and fails unless it redirects to want.
func (b *browser) post(target, want string, form url.Values) {
	b.e.t.Helper()
	rec := b.do("POST", portalURL+target, form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		b.e.t.Fatalf("POST %s: %d %s, want a redirect to %s\n%s", target, rec.Code, rec.Header().Get("Location"), want, rec.Body.String())
	}
}

func (e *netEnv) status(id int64) string {
	e.t.Helper()
	s, err := e.n.Site(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s.Status
}

// served waits for the registry to answer code on the site's home page.
func (e *netEnv) served(host string, code int) {
	e.t.Helper()
	var got int
	waitFor(e.t, host+" to answer "+strconv.Itoa(code), func() bool {
		got = e.do("GET", "https://"+host+"/").Code
		return got == code
	})
}

func siteURL(id int64, rest string) string {
	return "/admin/network/sites/" + strconv.FormatInt(id, 10) + rest
}

// TestNetworkAdminAccess: only network admins with a second factor reach
// the network admin area, which only the portal has.
func TestNetworkAdminAccess(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	pages := []string{"/admin/network/sites", siteURL(a.ID, ""), "/admin/network/admins", "/admin/network/plans"}

	// Without a second factor an ordinary account is first sent to set one
	// up (the portal's policy), so it has one here.
	ordy := e.account("ordy")
	ob := e.browser()
	ocsrf := ob.portalSignIn(ordy)
	if rec := ob.get(portalURL + "/admin/network/sites"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("an ordinary account without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	e.mfa(ordy)
	pam, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "pam", Email: "pam@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	e.mfa(pam)
	pb := e.browser()
	pcsrf := pb.portalSignIn(pam)
	for who, b := range map[string]*browser{"an ordinary account": ob, "a portal admin": pb} {
		for _, p := range append(pages, "/admin/network/") {
			if rec := b.get(portalURL + p); rec.Code != http.StatusForbidden {
				t.Fatalf("%s opens %s: %d", who, p, rec.Code)
			}
		}
	}
	// Nor can they act, with a valid form: grant themselves the network,
	// or suspend a site.
	for who, try := range map[string]struct {
		b    *browser
		csrf string
		id   int64
	}{"an ordinary account": {ob, ocsrf, ordy}, "a portal admin": {pb, pcsrf, pam}} {
		form := url.Values{"csrf": {try.csrf}, "account": {strconv.FormatInt(try.id, 10)}}
		if rec := try.b.do("POST", portalURL+"/admin/network/admins", form); rec.Code != http.StatusForbidden {
			t.Fatalf("%s adds a network admin: %d", who, rec.Code)
		}
		if ok, err := e.n.IsNetworkAdmin(e.ctx, try.id); err != nil || ok {
			t.Fatalf("%s became a network admin: %v %v", who, ok, err)
		}
		form = url.Values{"csrf": {try.csrf}, "reason": {"x"}, "confirm": {"alpha"}, "login": {"ordy"}, "role": {"admin"}}
		for _, p := range []string{siteURL(a.ID, "/suspend"), siteURL(a.ID, "/delete"), siteURL(a.ID, "/members"),
			siteURL(a.ID, "/members/"+strconv.FormatInt(e.owner, 10)+"/delete")} {
			if rec := try.b.do("POST", portalURL+p, form); rec.Code != http.StatusForbidden {
				t.Fatalf("%s posts %s: %d", who, p, rec.Code)
			}
		}
	}
	if e.status(a.ID) != StatusActive {
		t.Fatal("a refused action changed the site")
	}
	if _, err := e.n.Member(e.ctx, a.ID, ordy); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused action added a member: %v", err)
	}

	// A network admin without a second factor sets one up first.
	nina, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "nina", Email: "nina@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, nina, 0); err != nil {
		t.Fatal(err)
	}
	nb := e.browser()
	ncsrf := nb.portalSignIn(nina)
	for _, p := range pages {
		if rec := nb.get(portalURL + p); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
			t.Fatalf("a network admin without a second factor opens %s: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := nb.do("POST", portalURL+siteURL(a.ID, "/suspend"), url.Values{"csrf": {ncsrf}}); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/admin/profile/2fa" || e.status(a.ID) != StatusActive {
		t.Fatalf("a network admin without a second factor suspends: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// With one, every page opens; the menu shows the network.
	e.mfa(nina)
	for _, p := range pages {
		nb.page(p)
	}
	if rec := nb.get(portalURL + "/admin/network/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/network/sites" {
		t.Fatalf("/admin/network/: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := nb.page("/admin/"); !strings.Contains(body, `href="/admin/network/admins"`) {
		t.Fatal("the menu of a network admin has no network section")
	}
	if body := pb.page("/admin/"); strings.Contains(body, `href="/admin/network/sites"`) || !strings.Contains(body, `href="/admin/sites"`) {
		t.Fatal("the menu of a portal admin shows the network admin area, or not their sites")
	}

	// A form without its CSRF token is refused.
	if rec := nb.do("POST", portalURL+siteURL(a.ID, "/suspend"), url.Values{"reason": {"x"}}); rec.Code != http.StatusForbidden || e.status(a.ID) != StatusActive {
		t.Fatalf("a suspension without a CSRF token: %d", rec.Code)
	}
	if rec := nb.do("POST", portalURL+"/admin/network/admins", url.Values{"csrf": {"wrong"}, "login": {"ordy"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("a grant with a wrong CSRF token: %d", rec.Code)
	}
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, ordy); ok {
		t.Fatal("a form without its token granted the network")
	}

	// The portal's own site, and sites that do not exist, are not tenants.
	for _, id := range []int64{e.portalID(), 999999} {
		if rec := nb.get(portalURL + siteURL(id, "")); rec.Code != http.StatusNotFound {
			t.Fatalf("site %d: %d", id, rec.Code)
		}
		if rec := nb.do("POST", portalURL+siteURL(id, "/suspend"), url.Values{"csrf": {ncsrf}}); rec.Code != http.StatusNotFound {
			t.Fatalf("suspend site %d: %d", id, rec.Code)
		}
	}
	if rec := nb.do("POST", portalURL+siteURL(a.ID, "/explode"), url.Values{"csrf": {ncsrf}}); rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown action: %d", rec.Code)
	}

	// A tenant site has none of these routes, even for its admin.
	sa := e.siteStore(a)
	local, err := sa.CreateUser(e.ctx, store.NewUser{Username: "boss", Email: "boss@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sa.SetTOTP(e.ctx, local, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	tb := e.browser()
	tcsrf := tb.siteSignIn(a, local)
	if rec := tb.get("https://alpha.sap13ns.site/admin/"); rec.Code != http.StatusOK {
		t.Fatalf("the site admin's dashboard: %d", rec.Code)
	} else if strings.Contains(rec.Body.String(), "/admin/network/") || strings.Contains(rec.Body.String(), `href="/admin/sites"`) {
		t.Fatal("a tenant's menu links to the network admin area")
	}
	for _, p := range []string{"/admin/network/", "/admin/network/sites", "/admin/network/sites/1", "/admin/network/admins",
		"/admin/network/plans", "/admin/sites"} {
		if rec := tb.get("https://alpha.sap13ns.site" + p); rec.Code != http.StatusNotFound {
			t.Fatalf("tenant %s: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/admin/network/admins", "/admin/network/sites/1/suspend", "/admin/sites/request"} {
		if rec := tb.do("POST", "https://alpha.sap13ns.site"+p, url.Values{"csrf": {tcsrf}, "login": {"boss"}, "slug": {"zeta"}}); rec.Code != http.StatusNotFound &&
			rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("tenant POST %s: %d", p, rec.Code)
		}
	}
}

// TestNetworkAdminNeedsSecondFactor: the network admin area asks for a
// second factor even when the portal's policy does not ask the role for one.
func TestNetworkAdminNeedsSecondFactor(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) {
		portalSettings(t, o, func(s *store.Settings) { s.TwoFactor = "editors" })
	})
	ann, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "ann", Email: "ann@example.com", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, ann, 0); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.portalSignIn(ann)
	b.page("/admin/profile")
	rec := b.get(portalURL + "/admin/network/sites")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := b.page("/admin/profile/2fa"); !strings.Contains(body, "Configura la verificación en dos pasos para administrar la red.") {
		t.Fatal("no message about the network's second factor")
	}
	e.mfa(ann)
	b.page("/admin/network/sites")
}

// TestNetworkAdminSites: requesting a site, then approving, suspending,
// resuming, rejecting and deleting sites from the portal, which the
// registry follows through NOTIFY.
func TestNetworkAdminSites(t *testing.T) {
	e, box := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	boss, nb, csrf := e.networkAdmin("boss")

	// The owner asks for a site, with no second factor: they are not staff
	// on the portal.
	ob := e.browser()
	ocsrf := ob.portalSignIn(e.owner)
	if body := ob.page("/admin/sites"); !strings.Contains(body, `action="/admin/sites/request"`) || !strings.Contains(body, "alpha.sap13ns.site") {
		t.Fatal("My sites shows no request form, or not the owner's site")
	}
	ob.post("/admin/sites/request", "/admin/sites", url.Values{"csrf": {ocsrf}, "slug": {"Delta"}})
	sites, _, err := e.n.ListSites(e.ctx, SiteQuery{OwnerID: e.owner, Search: "delta"})
	if err != nil || len(sites) != 1 {
		t.Fatalf("the requested site: %+v %v", sites, err)
	}
	d := sites[0]
	if d.Slug != "delta" || d.Plan != PlanPersonal || d.Status != StatusPending || d.ApprovedAt != nil {
		t.Fatalf("requested site: %+v", d)
	}
	if audits(t, e.portal, "network.site_requested") != 1 {
		t.Fatal("the request is not audited")
	}
	body := ob.page("/admin/sites")
	if strings.Contains(body, `action="/admin/sites/request"`) || !strings.Contains(body, "delta.sap13ns.site") || !strings.Contains(body, "Pendiente de aprobación") {
		t.Fatal("My sites after the request")
	}
	e.served("delta.sap13ns.site", http.StatusServiceUnavailable)

	// The network admin finds it.
	list := func(query string) string {
		t.Helper()
		return nb.page("/admin/network/sites" + query)
	}
	for query, want := range map[string]bool{
		"": true, "?status=pending": true, "?status=active": false, "?q=DELTA": true, "?q=owner%40example": true,
		"?q=delta.sap13ns": true, "?q=nobody": false, "?status=pending&q=alpha": false, "?status=nonsense": true,
	} {
		if got := strings.Contains(list(query), "delta.sap13ns.site"); got != want {
			t.Fatalf("sites%s lists delta: %v", query, got)
		}
	}
	if !strings.Contains(list("?status=active"), "alpha.sap13ns.site") {
		t.Fatal("the list of sites")
	}
	detail := nb.page(siteURL(d.ID, ""))
	for _, want := range []string{"delta.sap13ns.site", "@owner", "owner@example.com", "Personal", `action="` + siteURL(d.ID, "/approve") + `"`} {
		if !strings.Contains(detail, want) {
			t.Fatalf("the site's page has no %q", want)
		}
	}
	if !strings.Contains(nb.page("/admin/"), `<span>Sitios</span><span class="badge ml-auto" data-variant="secondary">1</span>`) {
		t.Fatal("the menu does not count the pending site")
	}

	// Approving opens the site, records who did, and tells the owner.
	nb.post(siteURL(d.ID, "/approve"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}})
	got, err := e.n.Site(e.ctx, d.ID)
	if err != nil || got.Status != StatusActive || got.ApprovedAt == nil || got.ApprovedBy == nil || *got.ApprovedBy != boss {
		t.Fatalf("approved: %+v %v", got, err)
	}
	if mails := box.String(); !strings.Contains(mails, "to=owner@example.com") || !strings.Contains(mails, "Se ha aprobado tu sitio delta") ||
		!strings.Contains(mails, "https://delta.sap13ns.site/sso/start?next=%2Fadmin%2F") {
		t.Fatalf("the owner's e-mail:\n%s", mails)
	}
	if body := nb.page(siteURL(d.ID, "")); !strings.Contains(body, "Se ha aprobado el sitio delta.") || !strings.Contains(body, "@boss") {
		t.Fatal("the site's page after the approval")
	}
	e.served("delta.sap13ns.site", http.StatusOK)
	if audits(t, e.portal, "network.site_approved") != 1 {
		t.Fatal("the approval is not audited")
	}

	// An action its status does not allow changes nothing.
	nb.post(siteURL(d.ID, "/approve"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}})
	nb.post(siteURL(d.ID, "/resume"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}})
	if body := nb.page(siteURL(d.ID, "")); !strings.Contains(body, "No se puede hacer mientras el sitio está abierto.") {
		t.Fatal("no message for an action the status does not allow")
	}
	if e.status(d.ID) != StatusActive || audits(t, e.portal, "network.site_approved") != 1 || audits(t, e.portal, "network.site_resumed") != 0 {
		t.Fatal("a refused action changed the site")
	}

	// Suspending, with a reason, then resuming.
	nb.post(siteURL(d.ID, "/suspend"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}, "reason": {strings.Repeat("x", 501)}})
	if e.status(d.ID) != StatusActive {
		t.Fatal("suspended with a reason too long")
	}
	nb.post(siteURL(d.ID, "/suspend"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}, "reason": {"Spam, again"}})
	if got, _ := e.n.Site(e.ctx, d.ID); got.Status != StatusSuspended || got.SuspendedReason != "Spam, again" {
		t.Fatalf("suspended: %+v", got)
	}
	e.served("delta.sap13ns.site", http.StatusServiceUnavailable)
	if body := nb.page(siteURL(d.ID, "")); !strings.Contains(body, "Spam, again") || !strings.Contains(body, `action="`+siteURL(d.ID, "/resume")+`"`) {
		t.Fatal("the page of a suspended site")
	}
	nb.post(siteURL(d.ID, "/resume"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}})
	if e.status(d.ID) != StatusActive {
		t.Fatal("not resumed")
	}
	e.served("delta.sap13ns.site", http.StatusOK)

	// Deleting asks for the site's name.
	nb.post(siteURL(d.ID, "/delete"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}, "confirm": {"alpha"}})
	if e.status(d.ID) != StatusActive {
		t.Fatal("deleted with the wrong name")
	}
	nb.post(siteURL(d.ID, "/delete"), siteURL(d.ID, ""), url.Values{"csrf": {csrf}, "confirm": {"delta"}})
	if e.status(d.ID) != StatusDeleted {
		t.Fatal("not deleted")
	}
	e.served("delta.sap13ns.site", http.StatusNotFound)
	if body := nb.page(siteURL(d.ID, "")); strings.Contains(body, `action="`+siteURL(d.ID, "/delete")+`"`) {
		t.Fatal("a deleted site can be deleted again")
	}
	if strings.Contains(list(""), "delta.sap13ns.site") || !strings.Contains(list("?status=deleted"), "delta.sap13ns.site") {
		t.Fatal("the list of deleted sites")
	}

	// The owner may ask again, and a rejection deletes the site.
	ob.post("/admin/sites/request", "/admin/sites", url.Values{"csrf": {ocsrf}, "slug": {"echo"}})
	sites, _, _ = e.n.ListSites(e.ctx, SiteQuery{Status: StatusPending})
	if len(sites) != 1 || sites[0].Slug != "echo" {
		t.Fatalf("pending: %+v", sites)
	}
	echo := sites[0]
	e.served("echo.sap13ns.site", http.StatusServiceUnavailable)
	nb.post(siteURL(echo.ID, "/reject"), siteURL(echo.ID, ""), url.Values{"csrf": {csrf}})
	if e.status(echo.ID) != StatusDeleted {
		t.Fatal("not rejected")
	}
	if mails := box.String(); !strings.Contains(mails, "No se ha aprobado tu sitio echo") {
		t.Fatalf("no e-mail for the rejection:\n%s", mails)
	}
	e.served("echo.sap13ns.site", http.StatusNotFound)
	nb.post(siteURL(echo.ID, "/reject"), siteURL(echo.ID, ""), url.Values{"csrf": {csrf}})
	if !strings.Contains(nb.page(siteURL(echo.ID, "")), "No se puede hacer con un sitio eliminado.") {
		t.Fatal("rejecting a deleted site")
	}

	// A suspended site of the network (alpha) is not served either.
	nb.post(siteURL(a.ID, "/suspend"), siteURL(a.ID, ""), url.Values{"csrf": {csrf}})
	e.served("alpha.sap13ns.site", http.StatusServiceUnavailable)
	if body := ob.page("/admin/sites"); strings.Contains(body, "https://alpha.sap13ns.site/sso/start") {
		t.Fatal("My sites links to the administration of a suspended site")
	}

	for action, want := range map[string]int{"approved": 1, "rejected": 1, "suspended": 2, "resumed": 1, "deleted": 1, "requested": 2} {
		if n := audits(t, e.portal, "network.site_"+action); n != want {
			t.Fatalf("network.site_%s audited %d times, want %d", action, n, want)
		}
	}
}

// TestNetworkAdminApprovalWithoutMail: an approval stands when the owner
// cannot be told, and the network admin is warned.
func TestNetworkAdminApprovalWithoutMail(t *testing.T) {
	e := newNetEnv(t, nil) // no SMTP: the mailer fails
	s := e.tenant("gamma", "personal")
	_, nb, csrf := e.networkAdmin("boss")
	nb.post(siteURL(s.ID, "/approve"), siteURL(s.ID, ""), url.Values{"csrf": {csrf}})
	if e.status(s.ID) != StatusActive {
		t.Fatal("not approved")
	}
	if body := nb.page(siteURL(s.ID, "")); !strings.Contains(body, "Se ha aprobado el sitio gamma, pero no se ha podido avisar a su propietario por correo.") {
		t.Fatal("no warning about the e-mail")
	}
}

// TestNetworkAdminMembers: adding, changing and removing the members of a
// site from the portal.
func TestNetworkAdminMembers(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	_, nb, csrf := e.networkAdmin("boss")
	ann := e.account("ann")
	e.mfa(ann)
	back := siteURL(a.ID, "#members")
	members := siteURL(a.ID, "/members")

	nb.post(members, back, url.Values{"csrf": {csrf}, "login": {"ANN@example.com"}, "role": {"author"}})
	if m, err := e.n.Member(e.ctx, a.ID, ann); err != nil || m.Role != "author" {
		t.Fatalf("added: %+v %v", m, err)
	}
	ab := e.browser()
	ab.portalSignIn(ann)
	if body := ab.page("/admin/sites"); !strings.Contains(body, "alpha.sap13ns.site") {
		t.Fatal("the new member's sites")
	}
	if rec := ab.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("the new member signs in: %d", rec.Code)
	}
	u, err := sa.UserByNetworkAccount(e.ctx, ann)
	if err != nil || u.Role != "author" {
		t.Fatalf("the member's user: %+v %v", u, err)
	}

	// A new role reaches the site's user at once.
	nb.post(members, back, url.Values{"csrf": {csrf}, "account": {strconv.FormatInt(ann, 10)}, "role": {"editor"}})
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Role != "editor" {
		t.Fatalf("after the change: %+v", got)
	}
	if !strings.Contains(nb.page(siteURL(a.ID, "")), "ann tiene ahora el rol") {
		t.Fatal("no message for the new role")
	}

	// Refused: an unknown account, an unknown role, a new role or a
	// removal for the owner.
	for _, form := range []url.Values{
		{"login": {"nobody"}, "role": {"author"}},
		{"account": {"x"}, "role": {"author"}},
		{"login": {"ann"}, "role": {"overlord"}},
		{"account": {strconv.FormatInt(e.owner, 10)}, "role": {"author"}},
	} {
		form.Set("csrf", csrf)
		nb.post(members, back, form)
	}
	nb.post(siteURL(a.ID, "/members/"+strconv.FormatInt(e.owner, 10)+"/delete"), back, url.Values{"csrf": {csrf}})
	if body := nb.page(siteURL(a.ID, "")); !strings.Contains(body, "No se puede quitar al propietario de un sitio.") {
		t.Fatal("no message for removing the owner")
	}
	if m, err := e.n.Member(e.ctx, a.ID, e.owner); err != nil || m.Role != "admin" {
		t.Fatalf("the owner: %+v %v", m, err)
	}
	if m, _ := e.n.Member(e.ctx, a.ID, ann); m.Role != "editor" {
		t.Fatalf("ann: %+v", m)
	}
	if audits(t, e.portal, "network.member_set") != 2 {
		t.Fatal("the refused changes were audited, or the others were not")
	}

	// Removing a member disables their user there and ends their sessions.
	if rec := ab.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || len(sessions(t, sa, u.ID)) != 1 {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	nb.post(siteURL(a.ID, "/members/"+strconv.FormatInt(ann, 10)+"/delete"), back, url.Values{"csrf": {csrf}})
	if _, err := e.n.Member(e.ctx, a.ID, ann); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("still a member: %v", err)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("after the removal: %+v", got)
	}
	if rec := ab.get(ab.startSSO("alpha.sap13ns.site", "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("a removed member signs in again: %d", rec.Code)
	}
	if strings.Contains(ab.page("/admin/sites"), "alpha.sap13ns.site") {
		t.Fatal("My sites still lists the site")
	}
	if audits(t, e.portal, "network.member_removed") != 1 {
		t.Fatal("the removal is not audited")
	}
}

// TestNetworkAdminAdmins: granting and revoking the network, never from
// the last network admin; and the plans.
func TestNetworkAdminAdmins(t *testing.T) {
	e := newNetEnv(t, nil)
	boss, nb, csrf := e.networkAdmin("boss")
	const admins = "/admin/network/admins"
	if body := nb.page(admins); !strings.Contains(body, "@boss") || strings.Contains(body, admins+"/"+strconv.FormatInt(boss, 10)+"/delete") {
		t.Fatal("the only network admin can be revoked, or is not listed")
	}

	// The last network admin stays.
	nb.post(admins+"/"+strconv.FormatInt(boss, 10)+"/delete", admins, url.Values{"csrf": {csrf}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, boss); !ok {
		t.Fatal("the last network admin was revoked")
	}
	if !strings.Contains(nb.page(admins), "Es el último administrador de la red.") {
		t.Fatal("no message about the last network admin")
	}

	carl := e.account("carl")
	dora := e.account("dora")
	if err := e.portal.UpdateUserRoleStatus(e.ctx, dora, "subscriber", "disabled"); err != nil {
		t.Fatal(err)
	}
	nb.post(admins, admins, url.Values{"csrf": {csrf}, "login": {"dora"}})
	nb.post(admins, admins, url.Values{"csrf": {csrf}, "login": {"nobody"}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, dora); ok {
		t.Fatal("a disabled account became a network admin")
	}
	nb.post(admins, admins, url.Values{"csrf": {csrf}, "login": {"carl@example.com"}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, carl); !ok {
		t.Fatal("carl is not a network admin")
	}
	if body := nb.page(admins); !strings.Contains(body, "@carl") || !strings.Contains(body, "carl es ahora administrador de la red.") {
		t.Fatal("the new network admin")
	}
	list, err := e.n.NetworkAdmins(e.ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("network admins: %+v %v", list, err)
	}
	for _, x := range list {
		if x.AccountID == carl && (x.GrantedBy == nil || *x.GrantedBy != boss) {
			t.Fatalf("carl granted by %v", x.GrantedBy)
		}
	}

	// Revoking another one, then oneself.
	nb.post(admins+"/"+strconv.FormatInt(carl, 10)+"/delete", admins, url.Values{"csrf": {csrf}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, carl); ok {
		t.Fatal("carl is still a network admin")
	}
	nb.post(admins, admins, url.Values{"csrf": {csrf}, "login": {"carl"}})
	nb.post(admins+"/"+strconv.FormatInt(boss, 10)+"/delete", "/admin/", url.Values{"csrf": {csrf}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, boss); ok {
		t.Fatal("boss is still a network admin")
	}
	if rec := nb.get(portalURL + admins); rec.Code != http.StatusForbidden {
		t.Fatalf("a former network admin opens the area: %d", rec.Code)
	}
	for action, want := range map[string]int{"network.admin_granted": 2, "network.admin_revoked": 2} {
		if n := audits(t, e.portal, action); n != want {
			t.Fatalf("%s audited %d times, want %d", action, n, want)
		}
	}

	// Plans are read-only.
	cb := e.browser()
	cb.portalSignIn(carl)
	e.mfa(carl)
	body := cb.page("/admin/network/plans")
	for _, want := range []string{"Personal", "personal", "pro", "1024.0 MB"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the plans page has no %q", want)
		}
	}
	if strings.Contains(body, "<form method=\"post\"") {
		t.Fatal("the plans page has a form")
	}
}

// TestMySites: an account's sites on the portal, and its request for one.
func TestMySites(t *testing.T) {
	e, _ := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	ann := e.account("ann")
	if err := e.n.SetMember(e.ctx, a.ID, ann, "subscriber", 0); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	csrf := b.portalSignIn(ann)

	// An account that is not staff starts there, without a second factor.
	if rec := b.get(portalURL + "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sites" {
		t.Fatalf("/admin/: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body := b.page("/admin/sites")
	if !strings.Contains(body, `href="https://alpha.sap13ns.site/sso/start?next=%2Fadmin%2F"`) {
		t.Fatalf("no link to the site's admin:\n%s", body)
	}
	if strings.Contains(body, "/admin/network/") {
		t.Fatal("the menu shows the network admin area")
	}
	// The link signs in through the portal.
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("Go to admin: %d", rec.Code)
	}
	// The exemption from the second factor covers these pages only.
	if rec := b.get(portalURL + "/admin/posts"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("a subscriber without a second factor opens the posts: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Requests: the name must be valid, not reserved, and free.
	for slug, code := range map[string]int{"bad_name": 422, "admin": 422, "-x": 422, "ab--cd": 422, "": 422, "alpha": 409} {
		rec := b.do("POST", portalURL+"/admin/sites/request", url.Values{"csrf": {csrf}, "slug": {slug}})
		if rec.Code != code || !strings.Contains(rec.Body.String(), `action="/admin/sites/request"`) {
			t.Fatalf("request %q: %d, want %d", slug, rec.Code, code)
		}
	}
	if rec := b.do("POST", portalURL+"/admin/sites/request", url.Values{"slug": {"annsite"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("a request without a CSRF token: %d", rec.Code)
	}
	b.post("/admin/sites/request", "/admin/sites", url.Values{"csrf": {csrf}, "slug": {"annsite"}})
	body = b.page("/admin/sites")
	if !strings.Contains(body, "annsite.sap13ns.site") || strings.Contains(body, `action="/admin/sites/request"`) {
		t.Fatal("My sites after the request")
	}
	// One Personal site per account.
	rec := b.do("POST", portalURL+"/admin/sites/request", url.Values{"csrf": {csrf}, "slug": {"another"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Ya tienes un sitio Personal.") {
		t.Fatalf("a second request: %d", rec.Code)
	}
	if n, _, _ := e.n.ListSites(e.ctx, SiteQuery{OwnerID: ann}); len(n) != 1 || n[0].Status != StatusPending || n[0].Plan != PlanPersonal {
		t.Fatalf("ann's sites: %+v", n)
	}

	// Staff on the portal set up their second factor first.
	pam, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "pam", Email: "pam@example.com", Role: "editor", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pb := e.browser()
	pb.portalSignIn(pam)
	if rec := pb.get(portalURL + "/admin/sites"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("staff without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

// TestMySitesClosed: a portal that does not take requests.
func TestMySitesClosed(t *testing.T) {
	e := newNetEnv(t, nil)
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	if body := b.page("/admin/sites"); strings.Contains(body, `action="/admin/sites/request"`) {
		t.Fatal("a closed portal shows the request form")
	}
	if rec := b.do("POST", portalURL+"/admin/sites/request", url.Values{"csrf": {csrf}, "slug": {"zeta"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("a request on a closed portal: %d", rec.Code)
	}
	if sites, _, _ := e.n.ListSites(e.ctx, SiteQuery{OwnerID: e.owner}); len(sites) != 0 {
		t.Fatalf("a closed portal created %+v", sites)
	}
}

// TestRequestSite: the network's rules for a requested site.
func TestRequestSite(t *testing.T) {
	f := setup(t)
	if _, err := f.n.InitNetworkSite(f.ctx, "sap13ns.site"); err != nil {
		t.Fatal(err)
	}
	l := listen(t)
	ann, bob := f.account("ann"), f.account("bob")
	s, err := f.n.RequestSite(f.ctx, ann, "annsite")
	if err != nil || s.Status != StatusPending || s.Plan != PlanPersonal || s.OwnerID != ann || s.OwnerUsername != "ann" {
		t.Fatalf("requested: %+v %v", s, err)
	}
	l.next()
	l.next()
	if _, err := f.n.RequestSite(f.ctx, ann, "second"); !errors.Is(err, ErrState) {
		t.Fatalf("a second Personal site: %v", err)
	}
	// A pro site created by the CLI does not count; another owner does not.
	if _, err := f.n.CreateSite(f.ctx, NewSite{Slug: "annpro", OwnerID: ann, Plan: "pro"}); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"Bad", "www", "annsite"} {
		if _, err := f.n.RequestSite(f.ctx, bob, slug); err == nil {
			t.Fatalf("requested %q", slug)
		}
	}
	if _, err := f.n.RequestSite(f.ctx, bob, "annsite"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a taken name: %v", err)
	}
	if err := f.portal.UpdateUserRoleStatus(f.ctx, bob, "subscriber", "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.RequestSite(f.ctx, bob, "bobsite"); !errors.Is(err, ErrState) {
		t.Fatalf("a disabled account: %v", err)
	}
	if _, err := f.n.RequestSite(f.ctx, 999999, "ghost"); err == nil {
		t.Fatal("an account that does not exist requested a site")
	}

	// Approving records who did; only a pending site is approved or
	// rejected, and both notify.
	drain(l)
	if err := f.n.ApproveSite(f.ctx, s.ID, bob); err != nil {
		t.Fatal(err)
	}
	if got := l.next(); got != strconv.FormatInt(s.ID, 10) {
		t.Fatalf("approval notified %q", got)
	}
	got, _ := f.n.Site(f.ctx, s.ID)
	if got.Status != StatusActive || got.ApprovedBy == nil || *got.ApprovedBy != bob || got.ApprovedAt == nil {
		t.Fatalf("approved: %+v", got)
	}
	if err := f.n.ApproveSite(f.ctx, s.ID, 0); !errors.Is(err, ErrState) {
		t.Fatalf("approved twice: %v", err)
	}
	if err := f.n.RejectSite(f.ctx, s.ID); !errors.Is(err, ErrState) {
		t.Fatalf("rejected an active site: %v", err)
	}
	if err := f.n.ApproveSite(f.ctx, 999999, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved a missing site: %v", err)
	}
	portal, _ := f.n.NetworkSite(f.ctx)
	if err := f.n.ApproveSite(f.ctx, portal.ID, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved the portal: %v", err)
	}

	// A rejected (deleted) site leaves room for another request.
	if err := f.n.DeleteSite(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	again, err := f.n.RequestSite(f.ctx, ann, "annsite2")
	if err != nil {
		t.Fatal(err)
	}
	drain(l)
	if err := f.n.RejectSite(f.ctx, again.ID); err != nil {
		t.Fatal(err)
	}
	if got := l.next(); got != strconv.FormatInt(again.ID, 10) {
		t.Fatalf("rejection notified %q", got)
	}
	if got, _ := f.n.Site(f.ctx, again.ID); got.Status != StatusDeleted || got.DeletedAt == nil {
		t.Fatalf("rejected: %+v", got)
	}

	// The search of the list: the slug, a host, the owner's username or
	// e-mail address, among the sites that are not deleted by default.
	for q, want := range map[string]int{"annpro": 1, "ANN": 1, "bob": 0, "ann@example": 1, "annpro.sap13ns.site": 1, "%": 0, "_": 0, "": 1} {
		sites, total, err := f.n.ListSites(f.ctx, SiteQuery{Kind: KindTenant, Search: q})
		if err != nil || total != want || len(sites) != want {
			t.Fatalf("search %q: %d of %d, want %d, %v", q, len(sites), total, want, err)
		}
	}
	if sites, _, _ := f.n.ListSites(f.ctx, SiteQuery{Kind: KindTenant, Search: "ANN"}); len(sites) != 1 || sites[0].Slug != "annpro" {
		t.Fatalf("search ANN among the sites that are not deleted: %+v", sites)
	}
	if sites, _, _ := f.n.ListSites(f.ctx, SiteQuery{Status: StatusDeleted, Search: "ann@example"}); len(sites) != 2 {
		t.Fatalf("deleted sites of ann: %+v", sites)
	}
	if sites, _, _ := f.n.ListSites(f.ctx, SiteQuery{Search: "%"}); len(sites) != 0 {
		t.Fatalf("a %% matches %+v", sites)
	}
	if sites, _, _ := f.n.ListSites(f.ctx, SiteQuery{Kind: KindNetwork}); len(sites) != 1 || sites[0].Kind != KindNetwork {
		t.Fatalf("the network site: %+v", sites)
	}
}

// drain takes the notifications already sent.
func drain(l *listener) []string {
	var out []string
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		n, err := l.conn.WaitForNotification(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, n.Payload)
	}
}
