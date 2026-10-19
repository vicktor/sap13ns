package network

import (
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// allowSupport opens support access to a site as its owner, for d (0:
// until revoked).
func (e *netEnv) allowSupport(s *Site, d time.Duration) {
	e.t.Helper()
	if _, err := e.n.GrantSupport(e.ctx, s.ID, e.owner, d, netip.Addr{}); err != nil {
		e.t.Fatal(err)
	}
}

// auditDetail returns a detail of the last audit entry with an action.
func auditDetail(t *testing.T, st *store.Store, action, key string) string {
	t.Helper()
	var v *string
	if err := st.DB.QueryRow(t.Context(), `SELECT details->>$2 FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, action, key).Scan(&v); err != nil {
		t.Fatalf("audit %s: %v", action, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

// operatorSessions counts the sessions of an account's operator user on
// a site with an amr.
func (e *netEnv) operatorSessions(s *Site, account int64, amr string) int {
	e.t.Helper()
	var n int
	if err := e.siteStore(s).DB.QueryRow(e.ctx, `SELECT count(*) FROM sessions WHERE amr = $3 AND user_id IN
		(SELECT id FROM users WHERE network_account_id = $1 AND network_operator = $2)`, account, true, amr).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// signedInAdmin reports whether the browser opens the site's admin.
func (b *browser) signedInAdmin(host string) bool {
	b.e.t.Helper()
	rec := b.get("https://" + host + "/admin/")
	return rec.Code == http.StatusOK
}

// TestSupportAccess: a network admin who is not a member opens a site only
// while its owner (or one of its administrators) allows it, and every
// session ends with the grant.
func TestSupportAccess(t *testing.T) {
	e, box := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	const host = "alpha.sap13ns.site"
	support := siteURL(a.ID, "")
	mine := "/admin/sites/" + strconv.FormatInt(a.ID, 10) + "/support"
	op, ob, ocsrf := e.networkAdmin("olga")

	// Members are unaffected.
	ed := e.account("edith")
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	if rec := e.browser().signedInSSO(ed, host); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("a member without support access: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// No grant: the portal refuses the operator, and issues no code.
	rec := ob.get(ob.startSSO(host, "/admin/"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "El propietario de este sitio no ha permitido el acceso de soporte.") {
		t.Fatalf("an operator without support access: %d", rec.Code)
	}
	if auditDetail(t, e.portal, "sso.refused", "reason") != "no support access" {
		t.Fatal("the refusal is not audited")
	}
	if body := ob.page(support); !strings.Contains(body, "Acceso de soporte cerrado") || !strings.Contains(body, `action="`+support+`/support/request"`) ||
		strings.Contains(body, "/sso/start") {
		t.Fatal("the network admin's site page: support state, request form, or an admin link it cannot use")
	}

	// Asking the owner e-mails them a link to the grant page; nothing opens.
	ob.post(siteURL(a.ID, "/support/request"), support+"#support", url.Values{"csrf": {ocsrf}, "message": {"Your contact form sends spam."}})
	mails := box.String()
	if !strings.Contains(mails, "to=owner@example.com") || !strings.Contains(mails, "https://sap13ns.site"+mine) || !strings.Contains(mails, "Your contact form sends spam.") {
		t.Fatalf("the request e-mail:\n%s", mails)
	}
	if g, _ := e.n.SupportAccess(e.ctx, a.ID); g != nil || audits(t, e.portal, "network.support_requested") != 1 {
		t.Fatal("the request opened support access, or is not audited")
	}

	// Only the owner and the site's administrators grant it, with a CSRF
	// token; not an editor, nor a network admin.
	edb := e.browser()
	edcsrf := edb.portalSignIn(ed)
	if rec := edb.get(portalURL + mine); rec.Code != http.StatusNotFound {
		t.Fatalf("an editor opens the grant page: %d", rec.Code)
	}
	if rec := edb.do("POST", portalURL+mine, url.Values{"csrf": {edcsrf}, "duration": {"24h"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("an editor grants: %d", rec.Code)
	}
	if rec := ob.do("POST", portalURL+mine, url.Values{"csrf": {ocsrf}, "duration": {"24h"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("a network admin grants it to themselves: %d", rec.Code)
	}
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	if rec := b.do("POST", portalURL+mine, url.Values{"duration": {"24h"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("a grant without a CSRF token: %d", rec.Code)
	}
	if g, _ := e.n.SupportAccess(e.ctx, a.ID); g != nil {
		t.Fatal("a refused grant opened support access")
	}
	if body := b.page("/admin/sites"); !strings.Contains(body, `href="`+mine+`"`) {
		t.Fatal("My sites has no link to the support access")
	}

	// 24 hours.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"24h"}})
	if audits(t, sa, "support.granted") != 1 || audits(t, e.portal, "network.support_granted") != 1 {
		t.Fatal("the grant is not in both audit logs")
	}
	if body := ob.page(support); !strings.Contains(body, "Acceso de soporte abierto") || !strings.Contains(body, "/sso/start") {
		t.Fatal("the network admin's site page does not show the open access")
	}
	if rec := ob.sso(host, "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("operator sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if e.operatorSessions(a, op, store.AMRSSOOperator) != 1 || audits(t, sa, "login.operator") != 1 || !ob.signedInAdmin(host) {
		t.Fatal("no operator session")
	}
	ab := e.browser()
	ab.signedInSSO(e.owner, host)
	if page := ab.get("https://" + host + "/admin/users").Body.String(); !strings.Contains(page, "Acceso de soporte abierto") || !strings.Contains(page, "https://sap13ns.site"+mine) {
		t.Fatal("the site's Users page does not show the support access")
	}

	// A revocation that the site does not hear about (another process)
	// ends the session within the cache's bound.
	if _, err := e.n.db.Exec(e.ctx, `UPDATE support_grants SET revoked_at = $2 WHERE site_id = $1`, a.ID, e.clock.now()); err != nil {
		t.Fatal(err)
	}
	if !ob.signedInAdmin(host) {
		t.Fatal("the cached answer did not hold")
	}
	e.clock.add(operatorCacheTTL + time.Second)
	if ob.signedInAdmin(host) || e.operatorSessions(a, op, store.AMRSSOOperator) != 0 || audits(t, sa, "support.session_ended") != 1 {
		t.Fatal("the session outlived the grant by more than the cache's bound")
	}

	// Expiry, on the network's clock.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"24h"}})
	ob.sso(host, "/admin/")
	e.clock.add(23 * time.Hour)
	if !ob.signedInAdmin(host) {
		t.Fatal("the session ended before the grant")
	}
	e.clock.add(time.Hour + time.Second)
	if ob.signedInAdmin(host) || e.operatorSessions(a, op, store.AMRSSOOperator) != 0 {
		t.Fatal("the session outlived the grant")
	}
	if rec := ob.get(ob.startSSO(host, "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("authorize after the grant expired: %d", rec.Code)
	}

	// The redemption checks again: a code minted while open, redeemed
	// after the revocation, is refused.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"7d"}})
	callback := ob.authorize(ob.startSSO(host, "/admin/"))
	b.post(mine+"/revoke", mine, url.Values{"csrf": {csrf}})
	if rec := ob.get(callback); rec.Code != http.StatusForbidden {
		t.Fatalf("a code redeemed after the revocation: %d", rec.Code)
	}

	// Until revoked; a revocation signs the operator out at once.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"revoked"}})
	ob.sso(host, "/admin/")
	e.clock.add(60 * 24 * time.Hour)
	if !ob.signedInAdmin(host) {
		t.Fatal("an access until revoked ended")
	}
	b.post(mine+"/revoke", mine, url.Values{"csrf": {csrf}})
	if e.operatorSessions(a, op, store.AMRSSOOperator) != 0 || ob.signedInAdmin(host) {
		t.Fatal("the operator is still signed in after the revocation")
	}
	if audits(t, sa, "support.revoked") < 2 || audits(t, e.portal, "network.support_revoked") < 2 {
		t.Fatal("the revocation is not in both audit logs")
	}
	if u, _ := sa.OperatorUser(e.ctx, op); u == nil || u.Status != "disabled" {
		t.Fatal("the operator user is still active")
	}

	// A network admin ends it from the site's page.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"24h"}})
	ob.post(siteURL(a.ID, "/support/end"), support+"#support", url.Values{"csrf": {ocsrf}})
	if g, _ := e.n.SupportAccess(e.ctx, a.ID); g != nil {
		t.Fatal("the network admin did not end it")
	}

	// A suspended site: no grant opens, a revocation still works.
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"24h"}})
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	body := b.page(mine)
	if !strings.Contains(body, "Este sitio está suspendido") || strings.Contains(body, `name="duration"`) {
		t.Fatal("the grant page of a suspended site")
	}
	b.post(mine, mine, url.Values{"csrf": {csrf}, "duration": {"7d"}})
	if g, _ := e.n.SupportAccess(e.ctx, a.ID); g == nil || g.ExpiresAt == nil || g.ExpiresAt.Sub(g.GrantedAt) != SupportDay {
		t.Fatalf("a grant changed on a suspended site: %+v", g)
	}
	b.post(mine+"/revoke", mine, url.Values{"csrf": {csrf}})
	if g, _ := e.n.SupportAccess(e.ctx, a.ID); g != nil {
		t.Fatal("no revocation on a suspended site")
	}
	if strings.Contains(ob.page(support), `/support/request"`) {
		t.Fatal("a suspended site offers to request support access")
	}
}

// TestEmergencyAccess: without the owner's consent, a network admin opens
// a site for a legal order or a security incident, for themselves, for
// four hours at most, with a fresh second factor; the owner is told.
func TestEmergencyAccess(t *testing.T) {
	e, box := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	const host = "alpha.sap13ns.site"
	page := siteURL(a.ID, "")
	start := siteURL(a.ID, "/emergency")
	op, ob, ocsrf := e.networkAdmin("olga")
	otto, xb, _ := e.networkAdmin("otto")
	form := func(category, text string) url.Values {
		return url.Values{"csrf": {ocsrf}, "category": {category}, "justification": {text}}
	}
	refused := func(name string, f url.Values, status int) {
		t.Helper()
		if rec := ob.do("POST", portalURL+start, f); rec.Code != status {
			t.Fatalf("%s: %d, want %d", name, rec.Code, status)
		}
		if e2, _ := e.n.EmergencyAccess(e.ctx, a.ID, op); e2 != nil {
			t.Fatalf("%s opened an emergency access", name)
		}
	}

	// A reason and a justification are required.
	refused("no reason", form("", "Phishing pages under /wp-content."), http.StatusUnprocessableEntity)
	refused("another reason", form("curiosity", "Just looking."), http.StatusUnprocessableEntity)
	refused("no justification", form("security", "   "), http.StatusUnprocessableEntity)
	refused("a long justification", form("security", strings.Repeat("x", 501)), http.StatusUnprocessableEntity)
	refused("no CSRF token", url.Values{"category": {"security"}, "justification": {"x"}}, http.StatusForbidden)

	// A fresh second factor: a sign-in older than ten minutes needs a code
	// from the authenticator app (a password does not do).
	if _, err := e.portal.DB.Exec(e.ctx, `UPDATE sessions SET created_at = now() - interval '1 hour' WHERE user_id = $1`, op); err != nil {
		t.Fatal(err)
	}
	if body := ob.page(page); !strings.Contains(body, `name="code"`) {
		t.Fatal("the form does not ask for a code")
	}
	refused("an old sign-in", form("security", "Phishing pages under /wp-content."), http.StatusUnauthorized)
	f := form("security", "Phishing pages under /wp-content.")
	f.Set("code", "123456")
	refused("a wrong code", f, http.StatusUnauthorized)
	f.Set("password", "x")
	refused("a password", f, http.StatusUnauthorized)
	ocsrf = ob.portalSignIn(op) // signed in again
	if body := ob.page(page); strings.Contains(body, `name="code"`) {
		t.Fatal("the form asks a fresh sign-in for a code")
	}

	// Started: both audit logs, and the owner's e-mail.
	ob.post(start, page+"#emergency", form("legal", "Court order 123/2026 to preserve the comments."))
	acc, err := e.n.EmergencyAccess(e.ctx, a.ID, op)
	if err != nil || acc == nil || acc.ExpiresAt.Sub(acc.StartedAt) != EmergencyTTL || acc.Category != EmergencyLegal {
		t.Fatalf("the emergency access: %+v %v", acc, err)
	}
	if auditDetail(t, sa, "emergency.started", "justification") != "Court order 123/2026 to preserve the comments." ||
		auditDetail(t, sa, "emergency.started", "category") != "legal" ||
		auditDetail(t, e.portal, "network.emergency_started", "category") != "legal" ||
		auditDetail(t, e.portal, "network.emergency_started", "owner_notified") != "true" {
		t.Fatal("the emergency access is not in both audit logs")
	}
	mails := box.String()
	if !strings.Contains(mails, "to=owner@example.com") || !strings.Contains(mails, "Acceso de emergencia a alpha") ||
		!strings.Contains(mails, "Court order 123/2026") || !strings.Contains(mails, "una orden judicial") || !strings.Contains(mails, "olga") {
		t.Fatalf("the owner's e-mail:\n%s", mails)
	}
	if body := ob.page(page); !strings.Contains(body, "Abrirlo con el acceso de emergencia") || !strings.Contains(body, "/emergency/end") {
		t.Fatal("the site's page does not show the emergency access")
	}

	// It opens the site to them, as an emergency session.
	if rec := ob.sso(host, "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("emergency sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if e.operatorSessions(a, op, store.AMRSSOEmergency) != 1 || audits(t, sa, "login.emergency") != 1 || audits(t, e.portal, "sso.emergency") != 1 {
		t.Fatal("no emergency session, or not audited apart")
	}
	// Not to the other network admins.
	if rec := xb.get(xb.startSSO(host, "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("another network admin: %d", rec.Code)
	}
	if e.operatorSessions(a, otto, store.AMRSSOEmergency) != 0 {
		t.Fatal("another network admin got a session")
	}
	// The site's administrators see it.
	ab := e.browser()
	ab.signedInSSO(e.owner, host)
	if body := ab.get("https://" + host + "/admin/users").Body.String(); !strings.Contains(body, "Acceso de emergencia de @olga") || !strings.Contains(body, "Court order") {
		t.Fatal("the site's Users page does not show the emergency access")
	}

	// Four hours at most.
	e.clock.add(EmergencyTTL - time.Minute)
	if !ob.signedInAdmin(host) {
		t.Fatal("the session ended early")
	}
	e.clock.add(time.Minute + time.Second)
	if ob.signedInAdmin(host) || e.operatorSessions(a, op, store.AMRSSOEmergency) != 0 || audits(t, sa, "emergency.session_ended") != 1 {
		t.Fatal("the session outlived the emergency access")
	}
	if rec := ob.get(ob.startSSO(host, "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("authorize after the emergency access: %d", rec.Code)
	}

	// Ended earlier: its sessions end at once.
	ob.post(start, page+"#emergency", form("security", "Malware injected in the theme."))
	ob.sso(host, "/admin/")
	if !ob.signedInAdmin(host) {
		t.Fatal("no second emergency session")
	}
	ob.post(siteURL(a.ID, "/emergency/end"), page+"#emergency", url.Values{"csrf": {ocsrf}})
	if ob.signedInAdmin(host) || e.operatorSessions(a, op, store.AMRSSOEmergency) != 0 {
		t.Fatal("the session outlived the end of the emergency access")
	}
	if audits(t, sa, "emergency.ended") != 1 || audits(t, e.portal, "network.emergency_ended") != 1 ||
		!strings.Contains(box.String(), "Ha terminado el acceso de emergencia a alpha") {
		t.Fatal("the end is not audited, or the owner not told")
	}
}

// TestEmergencyOnSuspendedSite: a suspended site serves its admin to an
// emergency session only; everything else still gets the 503 page.
func TestEmergencyOnSuspendedSite(t *testing.T) {
	e, _ := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	const host = "alpha.sap13ns.site"
	op, ob, ocsrf := e.networkAdmin("olga")
	ed := e.account("edith")
	if err := e.n.SetMember(e.ctx, a.ID, ed, "admin", 0); err != nil {
		t.Fatal(err)
	}
	eb := e.browser()
	eb.signedInSSO(ed, host)
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	e.served(host, http.StatusServiceUnavailable)

	ob.post(siteURL(a.ID, "/emergency"), siteURL(a.ID, "#emergency"), url.Values{"csrf": {ocsrf}, "category": {"security"},
		"justification": {"Phishing kit found during the suspension."}})
	if rec := ob.sso(host, "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("emergency sign-in on a suspended site: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if !ob.signedInAdmin(host) || e.operatorSessions(a, op, store.AMRSSOEmergency) != 1 {
		t.Fatal("the emergency session does not open the admin")
	}
	if rec := ob.get("https://" + host + "/static/admin.css"); rec.Code != http.StatusOK {
		t.Fatalf("the admin's assets: %d", rec.Code)
	}
	// Everyone else: visitors, a member signed in before, a member signing
	// in now.
	if rec := e.do("GET", "https://"+host+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a visitor: %d", rec.Code)
	}
	if rec := eb.get("https://" + host + "/admin/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a member's session: %d", rec.Code)
	}
	nb := e.browser()
	nb.portalSignIn(ed)
	if rec := nb.get(nb.startSSO(host, "/admin/")); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Este sitio está suspendido.") {
		t.Fatalf("a member signing in: %d", rec.Code)
	}
	if rec := ob.get("https://" + host + "/"); rec.Code != http.StatusOK {
		t.Fatalf("the emergency session on the home page: %d", rec.Code)
	}
	// Support access does not reach it either.
	if _, err := e.n.GrantSupport(e.ctx, a.ID, e.owner, SupportDay, netip.Addr{}); err == nil {
		t.Fatal("support access opened on a suspended site")
	}

	// Ended: the site is suspended for everyone again.
	ob.post(siteURL(a.ID, "/emergency/end"), siteURL(a.ID, "#emergency"), url.Values{"csrf": {ocsrf}})
	if rec := ob.get("https://" + host + "/admin/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after the end: %d", rec.Code)
	}
}

// TestPortalRequiresSecondFactor: every account of the portal sets up a
// second factor before anything else, from its first sign-in, unless
// development mode skips it.
func TestPortalRequiresSecondFactor(t *testing.T) {
	e, _ := newOpenPortal(t)
	hash, err := auth.HashPassword("a long portal password")
	if err != nil {
		t.Fatal(err)
	}
	nora, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "nora", Email: "nora@example.com", Role: "subscriber", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	rec := b.do("POST", portalURL+"/login", url.Values{"login": {"nora"}, "password": {"a long portal password"}, "next": {"/admin/sites"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("the first sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body := b.page("/admin/profile/2fa")
	if !strings.Contains(body, "Toda cuenta de sap13ns necesita verificación en dos pasos.") || !strings.Contains(body, "después podrás ver tus sitios y solicitar uno") {
		t.Fatal("the 2FA page does not explain why")
	}
	csrf := ""
	if sess := sessions(t, e.portal, nora); len(sess) == 1 {
		csrf = sess[0].CSRFToken
	}
	for _, p := range []string{"/admin/", "/admin/sites"} {
		if rec := b.get(portalURL + p); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
			t.Fatalf("%s without a second factor: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := b.do("POST", portalURL+"/admin/sites/request", url.Values{"csrf": {csrf}, "slug": {"norasite"}}); rec.Code != http.StatusSeeOther ||
		rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("a request without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if sites, _, _ := e.n.ListSites(e.ctx, SiteQuery{OwnerID: nora}); len(sites) != 0 {
		t.Fatal("a site was requested without a second factor")
	}
	e.mfa(nora)
	b.page("/admin/sites")
}

// TestPortalDevSkip2FA: SAP13NS_DEV_SKIP_2FA lifts it in development mode.
func TestPortalDevSkip2FA(t *testing.T) {
	d := newNetEnv(t, func(o *RegistryOptions) { o.Config.Dev, o.Config.DevSkip2FA = true, true })
	a := d.tenant("alpha", "pro")
	bare := d.bareAccount("bare")
	if err := d.n.SetMember(d.ctx, a.ID, bare, "editor", 0); err != nil {
		t.Fatal(err)
	}
	db := d.browser()
	db.portalSignIn(bare)
	db.page("/admin/sites")
	if rec := db.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("development mode without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}
