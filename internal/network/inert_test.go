package network

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// TestEmergencyInertSite: a suspended site opened for an emergency access
// runs none of the tenant's code. Its plugins do not load (no hook runs,
// the plugin routes answer 404) and its background loops do not start
// (siteapp.Params.Inert). A normal instance never serves it: the one that
// was loaded when it was suspended, with an emergency access already
// live, is unloaded. The inert one is unloaded when the access ends or
// expires, and when the site is resumed, which loads it with its plugins
// again.
func TestEmergencyInertSite(t *testing.T) {
	src := cronPlugin(t)
	e, _ := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	const host = "alpha.sap13ns.site"
	e.served(host, http.StatusOK)
	e.install(a, src)
	const meta = `content="Ada"`               // the plugin's head.meta hook
	home := "https://" + host + "/?uncached=1" // past the page cache
	if rec := e.do("GET", home); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), meta) {
		t.Fatalf("the plugin's hook on the active site: %d", rec.Code)
	}
	form := func(b *browser) int { return b.do("POST", "https://"+host+"/plugin-forms/hooks/contact", nil).Code }
	inert := func(what string) *entry {
		t.Helper()
		ent := e.entry(a.ID)
		if ent == nil || ent.app == nil || !ent.inert || !ent.app.Inert() || !ent.app.Plugins().Inert() {
			t.Fatalf("%s: the site is not loaded inert", what)
		}
		if ps := ent.app.Plugins().List(); len(ps) != 0 || ent.app.Plugins().Get("hooks") != nil || ent.app.Plugins().Generation() != 0 {
			t.Fatalf("%s: plugins loaded: %v", what, ps)
		}
		return ent
	}
	unloaded := func(what string) {
		t.Helper()
		waitFor(t, what, func() bool { return e.entry(a.ID) == nil })
	}

	// An emergency access on the active site: its plugins run, as they do
	// for everyone there.
	op, ob, ocsrf := e.networkAdmin("olga")
	ob.post(siteURL(a.ID, "/emergency"), siteURL(a.ID, "#emergency"), url.Values{"csrf": {ocsrf}, "category": {"security"},
		"justification": {"Phishing kit reported on the site."}})
	if rec := ob.sso(host, "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("emergency sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if code := form(ob); code == http.StatusNotFound {
		t.Fatal("the plugin's form is not served on the active site")
	}
	normal := e.entry(a.ID)
	if normal == nil || normal.inert {
		t.Fatal("the active site is not loaded normally")
	}

	// Suspended while loaded: that instance goes at once, although the
	// emergency access is live, and an inert one serves the access.
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	unloaded("the normal instance to unload on the suspension")
	if !e.row(a.ID).WantsCron {
		t.Fatal("the plugin's cron was not recorded")
	}
	rec := ob.get("https://" + host + "/")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), meta) {
		t.Fatalf("the emergency session on the suspended site: %d, hook ran: %v", rec.Code, strings.Contains(rec.Body.String(), meta))
	}
	inert("the emergency access")
	if code := form(ob); code != http.StatusNotFound {
		t.Fatalf("the plugin's form on the inert site: %d", code)
	}
	if rec := ob.get("https://" + host + "/plugin-assets/hooks/1.0.0/plugin.json"); rec.Code != http.StatusNotFound {
		t.Fatalf("a plugin asset on the inert site: %d", rec.Code)
	}
	if rec := ob.get("https://" + host + "/admin/plugins"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Los plugins no están cargados") {
		t.Fatalf("the plugins page on the inert site: %d", rec.Code)
	}
	if rec := ob.get("https://" + host + "/admin/plugins/hooks"); rec.Code != http.StatusNotFound {
		t.Fatalf("a plugin's page on the inert site: %d", rec.Code)
	}
	var csrf string
	if err := e.siteStore(a).DB.QueryRow(e.ctx, `SELECT csrf_token FROM sessions WHERE amr = $1`, store.AMRSSOEmergency).Scan(&csrf); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/admin/plugins/reload", "/admin/plugins/upload", "/admin/plugins/catalog/hooks/install"} {
		rec := ob.do("POST", "https://"+host+p, url.Values{"csrf": {csrf}})
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/plugins" {
			t.Fatalf("POST %s on the inert site: %d %s", p, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := ob.get("https://" + host + "/admin/plugins"); !strings.Contains(rec.Body.String(), "Los plugins no se ejecutan en este sitio mientras está suspendido.") {
		t.Fatal("the refusal is not shown")
	}
	// The registry's own cron does not visit it either.
	e.reg.Cron(e.ctx)
	inert("after the cron")

	// Ended: the inert instance goes, and what the plugins want is kept.
	ob.post(siteURL(a.ID, "/emergency/end"), siteURL(a.ID, "#emergency"), url.Values{"csrf": {ocsrf}})
	unloaded("the inert instance to unload when the access ends")
	if !e.row(a.ID).WantsCron {
		t.Fatal("the inert instance forgot the plugin's cron")
	}
	if rec := e.do("GET", "https://"+host+"/"); rec.Code != http.StatusServiceUnavailable || e.entry(a.ID) != nil {
		t.Fatalf("the suspended site after the access: %d", rec.Code)
	}

	// Expired: unloaded at the next reload of the host table.
	if _, err := e.n.StartEmergency(e.ctx, a.ID, op, "legal", "Court order 12/2026.", netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if rec := e.do("GET", "https://"+host+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a visitor during the access: %d", rec.Code)
	}
	inert("a visitor's request during the access")
	e.clock.add(4*time.Hour + time.Minute)
	e.refresh()
	unloaded("the inert instance to unload when the access expires")

	// Resumed while inert: the next request loads it with its plugins,
	// even before the host table is reloaded.
	if _, err := e.n.StartEmergency(e.ctx, a.ID, op, "security", "Malware still served.", netip.Addr{}); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	e.do("GET", "https://"+host+"/")
	stale := inert("before the resumption")
	if err := e.n.ResumeSite(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	// The new host table is in place, but reconcile has not run yet.
	tbl, err := e.n.hostTable(e.ctx, e.reg.o.Now())
	if err != nil {
		t.Fatal(err)
	}
	e.reg.table.Store(tbl)
	if rec := e.do("GET", home); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), meta) {
		t.Fatalf("the plugin's hook after the resumption: %d", rec.Code)
	}
	ent := e.entry(a.ID)
	if ent == nil || ent == stale || ent.inert || ent.app.Inert() {
		t.Fatal("the resumed site is still served inert")
	}
	if p := ent.app.Plugins().Get("hooks"); p == nil || p.Status != plugin.StatusEnabled {
		t.Fatalf("the plugin after the resumption: %+v", p)
	}
	e.refresh()
	if got := e.entry(a.ID); got != ent {
		t.Fatal("the resumed site was loaded again")
	}

	// Suspended and resumed again, this time through the host table.
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	unloaded("the normal instance to unload on the second suspension")
	e.do("GET", "https://"+host+"/")
	inert("the second suspension")
	if err := e.n.ResumeSite(e.ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	unloaded("the inert instance to unload when the site is resumed")
	if rec := e.do("GET", home); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), meta) {
		t.Fatalf("the plugin's hook after the second resumption: %d", rec.Code)
	}
	if ent := e.entry(a.ID); ent == nil || ent.inert {
		t.Fatal("the resumed site is not loaded normally")
	}
}
