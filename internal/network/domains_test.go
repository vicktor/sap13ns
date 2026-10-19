package network

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// TestDomainCheck: a custom domain is verified only with its own TXT
// record and an address of the network; DNS answers are untrusted.
func TestDomainCheck(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	a, b := f.site("blog", alice, "pro"), f.site("shop", alice, "pro")
	d, err := f.n.AddDomain(f.ctx, a.ID, "www.example.org")
	if err != nil {
		t.Fatal(err)
	}
	host, good := d.Host, TXTValue(d.VerifyToken)
	if d.VerifyToken != f.n.ClaimToken(a.ID, host) || d.VerifyToken == f.n.ClaimToken(b.ID, host) ||
		d.VerifyToken == f.n.ClaimToken(a.ID, "example.org") || len(d.VerifyToken) != 32 {
		t.Fatalf("token %q", d.VerifyToken)
	}
	check := func(want string) *Domain {
		t.Helper()
		got, chk, err := f.n.CheckDomain(f.ctx, a.ID, host)
		if err != nil || chk.Problem != want || got.CheckError != want || got.CheckedAt == nil {
			t.Fatalf("check = %+v %+v %v, want %q", got, chk, err, want)
		}
		return got
	}
	unverified := func(got *Domain) {
		t.Helper()
		if got.VerifiedAt != nil {
			t.Fatalf("verified: %+v", got)
		}
	}

	// No record, another site's record, near misses.
	l := listen(t)
	unverified(check(CheckTXT))
	if got := l.next(); got != strconv.FormatInt(a.ID, 10) {
		t.Fatalf("notification %q", got)
	}
	for _, v := range [][]string{{TXTValue(f.n.ClaimToken(b.ID, host))}, {good + "x"}, {"x" + good}, {strings.ToUpper(good)},
		{VerifyValuePrefix}, {}} {
		f.dns.set(func(f *fakeDNS) { f.txt[TXTName(host)] = v })
		unverified(check(CheckTXT))
	}
	// The record among others: then the address counts.
	f.dns.set(func(f *fakeDNS) { f.txt[TXTName(host)] = []string{"v=spf1 -all", " " + good + " "} })
	unverified(check(CheckAddress))
	foreign := netip.MustParseAddr("198.51.100.7")
	for name, change := range map[string]func(*fakeDNS){
		"a CNAME elsewhere": func(f *fakeDNS) {
			f.cname[host], f.addrs["evil.example"] = "evil.example", []netip.Addr{foreign}
		},
		"a CNAME to a look-alike": func(f *fakeDNS) { f.cname[host] = "evilsap13ns.site" },
		"a CNAME to itself":       func(f *fakeDNS) { f.cname[host] = host },
		"another address":         func(f *fakeDNS) { f.addrs[host] = []netip.Addr{foreign} },
		"one address elsewhere":   func(f *fakeDNS) { f.addrs[host] = []netip.Addr{netAddr4, foreign} },
	} {
		f.dns.set(func(f *fakeDNS) {
			delete(f.cname, host)
			delete(f.addrs, host)
			change(f)
		})
		if got, chk, err := f.n.CheckDomain(f.ctx, a.ID, host); err != nil || chk.Problem != CheckAddress || got.VerifiedAt != nil {
			t.Fatalf("%s: %+v %v", name, chk, err)
		}
	}
	// A resolver failure is no answer.
	f.dns.set(func(f *fakeDNS) { f.fail[TXTName(host)] = true })
	unverified(check(CheckDNS))
	f.dns.set(func(f *fakeDNS) { f.fail[TXTName(host)], f.fail[host] = false, true })
	unverified(check(CheckDNS))

	// The addresses of the network (also IPv4-mapped) verify it...
	f.dns.set(func(f *fakeDNS) {
		f.fail[host] = false
		delete(f.cname, host)
		f.addrs[host] = []netip.Addr{netAddr6, netip.AddrFrom16(netAddr4.As16())}
	})
	got := check(CheckOK)
	if got.VerifiedAt == nil || got.CheckFailures != 0 || got.LapsedAt != nil {
		t.Fatalf("verified: %+v", got)
	}
	verifiedAt := *got.VerifiedAt
	// ...and so does a CNAME to the target or to a host of the network.
	for _, c := range []string{"SAP13NS.site", "blog.sap13ns.site", "x.y.sap13ns.site"} {
		f.dns.set(func(f *fakeDNS) { delete(f.addrs, host); f.cname[host] = c })
		check(CheckOK)
	}
	// A check that fails on a verified domain is recorded, and only the
	// daily re-check counts it.
	f.dns.clear(host)
	if got := check(CheckTXT); got.VerifiedAt == nil || !got.VerifiedAt.Equal(verifiedAt) || got.CheckFailures != 0 {
		t.Fatalf("after a failed check: %+v", got)
	}

	// SAP13NS_DOMAIN_ADDRESSES replaces the addresses of the target.
	f.n.targetAddrs = []netip.Addr{netip.MustParseAddr("203.0.113.5")}
	f.dns.point(host, d.VerifyToken)
	f.dns.set(func(f *fakeDNS) { delete(f.cname, host); f.addrs[host] = []netip.Addr{netAddr4} })
	check(CheckAddress)
	f.dns.set(func(f *fakeDNS) { f.addrs[host] = []netip.Addr{netip.MustParseAddr("203.0.113.5")} })
	check(CheckOK)

	// Another site's host is not found; a default host needs no check.
	if _, _, err := f.n.CheckDomain(f.ctx, b.ID, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another site's host: %v", err)
	}
	if _, _, err := f.n.CheckDomain(f.ctx, a.ID, "nope.example"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a host of no site: %v", err)
	}
	if dd, chk, err := f.n.CheckDomain(f.ctx, a.ID, "blog.sap13ns.site"); err != nil || !chk.OK() || dd.Kind != DomainDefault {
		t.Fatalf("default host: %+v %+v %v", dd, chk, err)
	}
}

// TestDomainClaim: a host another site added and never verified moves to
// the site that proves control of it; a verified host never moves.
func TestDomainClaim(t *testing.T) {
	f := setup(t)
	squat, rightful := f.site("squat", f.account("mallory"), "pro"), f.site("real", f.account("alice"), "pro")
	const host = "www.example.org"
	if _, err := f.n.AddDomain(f.ctx, squat.ID, host); err != nil {
		t.Fatal(err)
	}
	claimed := func(why string) {
		t.Helper()
		if _, err := f.n.AddDomain(f.ctx, rightful.ID, host); !errors.Is(err, ErrClaimed) || !errors.Is(err, store.ErrConflict) {
			t.Fatalf("%s: %v", why, err)
		}
		if d, _ := f.n.Domain(f.ctx, host); d.SiteID != squat.ID {
			t.Fatalf("%s: the host moved", why)
		}
	}
	claimed("without a record")
	f.dns.set(func(x *fakeDNS) { x.txt[TXTName(host)] = []string{TXTValue(f.n.ClaimToken(squat.ID, host))} })
	claimed("with the squatter's record")
	f.dns.set(func(x *fakeDNS) { x.fail[TXTName(host)] = true })
	claimed("with a failing resolver")

	f.dns.clear(host)
	f.dns.point(host, f.n.ClaimToken(rightful.ID, host))
	d, err := f.n.AddDomain(f.ctx, rightful.ID, host)
	if err != nil || d.SiteID != rightful.ID || d.TakenFrom != squat.ID || d.VerifiedAt != nil || d.VerifyToken != f.n.ClaimToken(rightful.ID, host) {
		t.Fatalf("take over: %+v %v", d, err)
	}
	if _, _, err := f.n.CheckDomain(f.ctx, squat.ID, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the squatter checks it: %v", err)
	}
	if _, err := f.n.AddDomain(f.ctx, squat.ID, host); !errors.Is(err, ErrClaimed) {
		t.Fatalf("the squatter takes it back: %v", err)
	}
	if _, chk, err := f.n.CheckDomain(f.ctx, rightful.ID, host); err != nil || !chk.OK() {
		t.Fatalf("verify: %+v %v", chk, err)
	}
	// Verified, it stays, whatever records another site shows.
	f.dns.set(func(x *fakeDNS) {
		x.txt[TXTName(host)] = append(x.txt[TXTName(host)], TXTValue(f.n.ClaimToken(squat.ID, host)))
	})
	if _, err := f.n.AddDomain(f.ctx, squat.ID, host); !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrClaimed) {
		t.Fatalf("a verified host: %v", err)
	}
	if _, err := f.n.AddDomain(f.ctx, rightful.ID, host); !errors.Is(err, store.ErrConflict) || errors.Is(err, ErrClaimed) {
		t.Fatalf("its own host again: %v", err)
	}
	// Taking a host over still needs room in the plan.
	other := f.site("other", f.account("bob"), PlanPersonal)
	f.dns.point("shop.example.org", f.n.ClaimToken(other.ID, "shop.example.org"))
	if _, err := f.n.AddDomain(f.ctx, squat.ID, "shop.example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.AddDomain(f.ctx, other.ID, "shop.example.org"); !errors.Is(err, ErrLimit) {
		t.Fatalf("a take-over beyond the plan: %v", err)
	}
}

// TestCustomDomainRouting: only verified hosts are routed; HSTS never
// covers the subdomains of a custom domain; the RP ID host lists the
// custom primary as a related origin of its passkeys.
func TestCustomDomainRouting(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	const host = "www.alpha.example"
	d, err := e.n.AddDomain(e.ctx, a.ID, host)
	if err != nil {
		t.Fatal(err)
	}
	e.refresh()
	paths := []string{"/", "/admin/", "/login", "/.well-known/acme-challenge/x", "/.well-known/webauthn"}
	notServed := func(why string) {
		t.Helper()
		for _, p := range paths {
			if rec := e.do("GET", "https://"+host+p); rec.Code != http.StatusNotFound {
				t.Fatalf("%s: %s answers %d", why, p, rec.Code)
			}
		}
	}
	notServed("added")
	// Records in place, but not checked yet; then a failing check.
	e.dns.point(host, d.VerifyToken)
	e.refresh()
	notServed("not checked")
	e.dns.set(func(f *fakeDNS) { delete(f.cname, host) })
	if _, chk, err := e.n.CheckDomain(e.ctx, a.ID, host); err != nil || chk.Problem != CheckAddress {
		t.Fatalf("check: %+v %v", chk, err)
	}
	e.refresh()
	notServed("check failed")

	// Verified, it is an alias of the site at once (the notification).
	e.dns.point(host, d.VerifyToken)
	if _, chk, err := e.n.CheckDomain(e.ctx, a.ID, host); err != nil || !chk.OK() {
		t.Fatalf("check: %+v %v", chk, err)
	}
	waitFor(t, "the alias", func() bool { return e.do("GET", "https://"+host+"/").Code == http.StatusMovedPermanently })
	if rec := e.do("GET", "https://"+host+"/a?b=1"); rec.Header().Get("Location") != "https://alpha.sap13ns.site/a?b=1" {
		t.Fatalf("alias: %s", rec.Header().Get("Location"))
	}

	// HSTS: with subdomains on the network's hosts only.
	const (
		withSubs = "max-age=63072000; includeSubDomains"
		noSubs   = "max-age=63072000"
	)
	hsts := func(target, want string) {
		t.Helper()
		if got := e.do("GET", target).Header().Get("Strict-Transport-Security"); got != want {
			t.Fatalf("HSTS of %s: %q, want %q", target, got, want)
		}
	}
	hsts("https://alpha.sap13ns.site/", withSubs)
	hsts("https://sap13ns.site/", withSubs)
	hsts("https://"+host+"/.well-known/acme-challenge/x", noSubs) // answered by the site, on the alias
	// The default primary has no related origins.
	if rec := e.do("GET", "https://alpha.sap13ns.site/.well-known/webauthn"); rec.Code != http.StatusNotFound {
		t.Fatalf("related origins of a default primary: %d", rec.Code)
	}

	// A custom primary.
	if err := e.n.SetPrimaryDomain(e.ctx, a.ID, host); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	e.reg.Sweep(e.ctx)
	hsts("https://"+host+"/", noSubs)
	hsts("https://alpha.sap13ns.site/.well-known/webauthn", noSubs)
	rec := e.do("GET", "https://alpha.sap13ns.site/.well-known/webauthn")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" ||
		strings.TrimSpace(rec.Body.String()) != `{"origins":["https://www.alpha.example"]}` {
		t.Fatalf("related origins: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if cfg := e.entry(a.ID).app.Config(); cfg.PasskeyRPID != "alpha.sap13ns.site" ||
		!slices.Equal(cfg.PasskeyOrigins, []string{"https://www.alpha.example", "https://alpha.sap13ns.site"}) {
		t.Fatalf("passkeys: %s %v", cfg.PasskeyRPID, cfg.PasskeyOrigins)
	}
	// Only the RP ID host serves the file.
	if rec := e.do("GET", "https://"+host+"/.well-known/webauthn"); rec.Code != http.StatusNotFound {
		t.Fatalf("related origins on the custom domain: %d", rec.Code)
	}
	if rec := e.do("GET", "https://alpha.sap13ns.site/x"); rec.Code != http.StatusMovedPermanently {
		t.Fatalf("old primary: %d", rec.Code)
	}
}

// day moves the clock a day on and runs the daily maintenance.
func (e *netEnv) day() {
	e.clock.add(24 * time.Hour)
	e.reg.Maintain(e.ctx)
}

func (e *netEnv) domain(host string) *Domain {
	e.t.Helper()
	d, err := e.n.Domain(e.ctx, host)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

// TestDomainLapse: a verified custom domain whose daily re-checks keep
// failing lapses; a lapsed primary gives way to the default host, and the
// owner is told.
func TestDomainLapse(t *testing.T) {
	e, box := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	const host, shop = "www.alpha.example", "shop.alpha.example"
	for _, h := range []string{host, shop} {
		d, err := e.n.AddDomain(e.ctx, a.ID, h)
		if err != nil {
			t.Fatal(err)
		}
		e.dns.point(h, d.VerifyToken)
		if _, chk, err := e.n.CheckDomain(e.ctx, a.ID, h); err != nil || !chk.OK() {
			t.Fatalf("verify %s: %+v %v", h, chk, err)
		}
	}
	if err := e.n.SetPrimaryDomain(e.ctx, a.ID, host); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	e.served(host, http.StatusOK)
	start := e.clock.now()
	failures := func(day string, n int, problem string) {
		t.Helper()
		d := e.domain(host)
		if d.VerifiedAt == nil || d.CheckFailures != n || d.CheckError != problem || d.CheckedAt == nil || !d.CheckedAt.Equal(e.clock.now()) {
			t.Fatalf("%s: %+v", day, d)
		}
	}

	// A second run the same day (a restart) checks nothing again.
	e.reg.Maintain(e.ctx)
	if d := e.domain(host); !d.CheckedAt.Equal(start) {
		t.Fatalf("checked again the same day: %v", d.CheckedAt)
	}
	e.day()
	failures("day 1", 0, CheckOK)
	// A failure, then a passing check: clear again.
	txt := e.domain(host)
	e.dns.set(func(f *fakeDNS) { delete(f.txt, TXTName(host)) })
	e.day()
	failures("day 2", 1, CheckTXT)
	if since := e.domain(host).FailingSince; since == nil || !since.Equal(e.clock.now()) {
		t.Fatalf("failing since %v", since)
	}
	e.dns.point(host, txt.VerifyToken)
	e.day()
	failures("day 3", 0, CheckOK)
	if e.domain(host).FailingSince != nil {
		t.Fatal("still failing")
	}
	// A failing resolver is not counted.
	e.dns.set(func(f *fakeDNS) { f.fail[TXTName(host)] = true })
	e.day()
	failures("day 4", 0, CheckDNS)
	// Pointing elsewhere: three failures, but not for long enough yet...
	e.dns.set(func(f *fakeDNS) { f.fail[TXTName(host)] = false; f.cname[host] = "elsewhere.example" })
	for i, day := range []string{"day 5", "day 6", "day 7"} {
		e.day()
		failures(day, i+1, CheckAddress)
	}
	e.served(host, http.StatusOK)
	if err := e.reg.HostPolicy(e.ctx, host); err != nil {
		t.Fatalf("host policy before the lapse: %v", err)
	}
	// ...then it lapses.
	e.day()
	d := e.domain(host)
	if d.VerifiedAt != nil || d.Primary || d.LapsedAt == nil || !d.LapsedAt.Equal(e.clock.now()) || d.CheckFailures != 4 {
		t.Fatalf("lapsed: %+v", d)
	}
	if s, _ := e.n.Site(e.ctx, a.ID); s.PrimaryHost != "alpha.sap13ns.site" {
		t.Fatalf("primary after the lapse: %s", s.PrimaryHost)
	}
	if d := e.domain(shop); d.VerifiedAt == nil || d.CheckFailures != 0 {
		t.Fatalf("the other domain: %+v", d)
	}
	if rec := e.do("GET", "https://"+host+"/"); rec.Code != http.StatusNotFound {
		t.Fatalf("a lapsed domain answers %d", rec.Code)
	}
	if err := e.reg.HostPolicy(e.ctx, host); err == nil {
		t.Fatal("a certificate for a lapsed domain")
	}
	e.reg.Sweep(e.ctx)
	e.served("alpha.sap13ns.site", http.StatusOK)
	if rec := e.do("GET", "https://"+shop+"/a"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://alpha.sap13ns.site/a" {
		t.Fatalf("the other domain: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if n := audits(t, e.portal, "network.domain_lapsed"); n != 1 {
		t.Fatalf("%d lapse audits", n)
	}
	mails := box.String()
	for _, want := range []string{"to=owner@example.com", "Tu dominio www.alpha.example ya no se sirve", "Ya no apunta a la red.",
		"Tu sitio vuelve a servirse en alpha.sap13ns.site.", "https://sap13ns.site/admin/sites/" + strconv.FormatInt(a.ID, 10) + "/domains"} {
		if !strings.Contains(mails, want) {
			t.Fatalf("the e-mail lacks %q:\n%s", want, mails)
		}
	}
	if strings.Contains(mails, shop) {
		t.Fatal("an e-mail about the other domain")
	}

	// Lapsed, it is not checked again; its owner verifies it again.
	e.day()
	if d := e.domain(host); !d.CheckedAt.Equal(e.clock.now().Add(-24 * time.Hour)) {
		t.Fatalf("a lapsed domain was checked: %v", d.CheckedAt)
	}
	e.dns.point(host, txt.VerifyToken)
	if d, chk, err := e.n.CheckDomain(e.ctx, a.ID, host); err != nil || !chk.OK() || d.VerifiedAt == nil || d.LapsedAt != nil ||
		d.CheckFailures != 0 || d.FailingSince != nil || d.Primary {
		t.Fatalf("verified again: %+v %+v %v", d, chk, err)
	}
	waitFor(t, "the alias", func() bool { return e.do("GET", "https://"+host+"/").Code == http.StatusMovedPermanently })
}

// TestMyDomains: the owner of a site manages its custom domains from
// "My sites", with its second factor; nobody else can.
func TestMyDomains(t *testing.T) {
	e, _ := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	base := "/admin/sites/" + strconv.FormatInt(a.ID, 10) + "/domains"
	b := e.browser()
	csrf := b.portalSignIn(e.owner)
	form := func(host string) url.Values { return url.Values{"csrf": {csrf}, "host": {host}} }

	if rec := b.get(portalURL + base); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	e.mfa(e.owner)
	if body := b.page("/admin/sites"); !strings.Contains(body, `href="`+base+`"`) {
		t.Fatal("My sites has no link to the domains")
	}
	if body := b.page(base); !strings.Contains(body, "alpha.sap13ns.site") || !strings.Contains(body, `action="`+base+`"`) {
		t.Fatalf("the domains page:\n%s", body)
	}

	// Adding one: canonicalised, audited, notified; the page shows its records.
	l := listen(t)
	b.post(base, base, form("WWW.Alpha.Example."))
	d := e.domain("www.alpha.example")
	if d.SiteID != a.ID || d.VerifiedAt != nil {
		t.Fatalf("added: %+v", d)
	}
	if got := drain(l); !slices.Contains(got, strconv.FormatInt(a.ID, 10)) {
		t.Fatalf("notifications %v", got)
	}
	body := b.page(base)
	for _, want := range []string{`value="_sap13ns-verify.www.alpha.example"`, `value="sap13ns-site-verification=` + d.VerifyToken + `"`,
		"Un registro CNAME de www.alpha.example a sap13ns.site."} {
		if !strings.Contains(body, want) {
			t.Fatalf("the page lacks %q:\n%s", want, body)
		}
	}
	if n := audits(t, e.portal, "network.domain_added"); n != 1 {
		t.Fatalf("%d audits", n)
	}
	for host, code := range map[string]int{"x.sap13ns.site": 422, "sap13ns.site": 422, "localhost": 422, "a b.example": 422,
		"www.alpha.example": 409} {
		if rec := b.do("POST", portalURL+base, form(host)); rec.Code != code || !strings.Contains(rec.Body.String(), `action="`+base+`"`) {
			t.Fatalf("add %q: %d, want %d", host, rec.Code, code)
		}
	}
	for _, p := range []string{base, base + "/check", base + "/remove"} {
		if rec := b.do("POST", portalURL+p, url.Values{"host": {"www.alpha.example"}}); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without a CSRF token: %d", p, rec.Code)
		}
	}

	// Verifying: first without the records.
	b.post(base+"/check", base, form("www.alpha.example"))
	if d := e.domain("www.alpha.example"); d.VerifiedAt != nil || d.CheckError != CheckTXT {
		t.Fatalf("a failed check: %+v", d)
	}
	if n := audits(t, e.portal, "network.domain_check_failed"); n != 1 {
		t.Fatalf("%d audits", n)
	}
	if body := b.page(base); !strings.Contains(body, "Última comprobación: falta el registro TXT o tiene otro valor.") {
		t.Fatal("the page does not say why")
	}
	b.post(base+"/primary", base, form("www.alpha.example"))
	if s, _ := e.n.Site(e.ctx, a.ID); s.PrimaryHost != "alpha.sap13ns.site" {
		t.Fatal("an unverified primary")
	}
	e.dns.point("www.alpha.example", d.VerifyToken)
	b.post(base+"/check", base, form("www.alpha.example"))
	if n := audits(t, e.portal, "network.domain_verified"); n != 1 {
		t.Fatalf("%d audits", n)
	}
	waitFor(t, "the alias", func() bool { return e.do("GET", "https://www.alpha.example/").Code == http.StatusMovedPermanently })

	// The primary and the default host stay.
	b.post(base+"/primary", base, form("www.alpha.example"))
	if s, _ := e.n.Site(e.ctx, a.ID); s.PrimaryHost != "www.alpha.example" || audits(t, e.portal, "network.domain_primary") != 1 {
		t.Fatalf("primary %s", s.PrimaryHost)
	}
	b.post(base+"/remove", base, form("www.alpha.example"))
	b.post(base+"/remove", base, form("alpha.sap13ns.site"))
	if ds, _ := e.n.Domains(e.ctx, a.ID); len(ds) != 2 {
		t.Fatalf("domains %+v", ds)
	}
	b.post(base+"/primary", base, form("alpha.sap13ns.site"))
	b.post(base+"/remove", base, form("www.alpha.example"))
	if _, err := e.n.Domain(e.ctx, "www.alpha.example"); !errors.Is(err, store.ErrNotFound) || audits(t, e.portal, "network.domain_removed") != 1 {
		t.Fatalf("removed: %v", err)
	}

	// Another site's host, an unknown operation, another site: 404.
	bob := e.account("bob")
	beta := e.site("beta", bob, "pro")
	if _, err := e.n.AddDomain(e.ctx, beta.ID, "www.beta.example"); err != nil {
		t.Fatal(err)
	}
	betaBase := "/admin/sites/" + strconv.FormatInt(beta.ID, 10) + "/domains"
	for _, r := range []struct{ method, path, host string }{
		{"POST", base + "/remove", "www.beta.example"},
		{"POST", base + "/check", "www.beta.example"},
		{"POST", base + "/delete", "alpha.sap13ns.site"},
		{"GET", betaBase, ""},
		{"POST", betaBase, "www.alpha.example"},
		{"POST", betaBase + "/remove", "www.beta.example"},
	} {
		var f url.Values
		if r.method == "POST" {
			f = form(r.host)
		}
		if rec := b.do(r.method, portalURL+r.path, f); rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s %s: %d", r.method, r.path, r.host, rec.Code)
		}
	}
	// A member of the site, even its administrator, is not its owner.
	ann := e.account("ann")
	if err := e.n.SetMember(e.ctx, a.ID, ann, "admin", 0); err != nil {
		t.Fatal(err)
	}
	e.mfa(ann)
	mb := e.browser()
	mcsrf := mb.portalSignIn(ann)
	if rec := mb.get(portalURL + base); rec.Code != http.StatusNotFound {
		t.Fatalf("a member's GET: %d", rec.Code)
	}
	if rec := mb.do("POST", portalURL+base, url.Values{"csrf": {mcsrf}, "host": {"shop.alpha.example"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("a member's POST: %d", rec.Code)
	}

	// A host another site claimed: the page shows the record that takes it over.
	if _, err := e.n.AddDomain(e.ctx, beta.ID, "blog.example.net"); err != nil {
		t.Fatal(err)
	}
	rec := b.do("POST", portalURL+base, form("blog.example.net"))
	claim := TXTValue(e.n.ClaimToken(a.ID, "blog.example.net"))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `value="`+claim+`"`) {
		t.Fatalf("a claimed host: %d", rec.Code)
	}
	e.dns.set(func(f *fakeDNS) { f.txt[TXTName("blog.example.net")] = []string{claim} })
	b.post(base, base, form("blog.example.net"))
	if d := e.domain("blog.example.net"); d.SiteID != a.ID {
		t.Fatalf("taken over: %+v", d)
	}

	// The plan's limit (Pro: two).
	b.post(base, base, form("shop.alpha.example"))
	rec = b.do("POST", portalURL+base, form("third.alpha.example"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "El plan de este sitio no permite más dominios propios.") {
		t.Fatalf("beyond the limit: %d", rec.Code)
	}
	if body := b.page(base); !strings.Contains(body, "Dominios propios: 2 de 2 que permite el plan.") || strings.Contains(body, `id="domain-host"`) {
		t.Fatal("a full plan shows the form")
	}
	free := e.tenant("free", PlanPersonal)
	freeBase := "/admin/sites/" + strconv.FormatInt(free.ID, 10) + "/domains"
	if rec := b.do("POST", portalURL+freeBase, form("free.example")); rec.Code != http.StatusForbidden {
		t.Fatalf("Personal: %d", rec.Code)
	}
}

// TestNetworkAdminDomains: network admins manage the domains of any site
// from its page, within its plan.
func TestNetworkAdminDomains(t *testing.T) {
	e, _ := newOpenPortal(t)
	a := e.tenant("alpha", "pro")
	_, b, csrf := e.networkAdmin("nina")
	form := func(host string) url.Values { return url.Values{"csrf": {csrf}, "host": {host}} }
	base, back := siteURL(a.ID, "/domains"), siteURL(a.ID, "#domains")
	if body := b.page(siteURL(a.ID, "")); !strings.Contains(body, `action="`+base+`"`) {
		t.Fatal("the site page has no domain form")
	}
	b.post(base, back, form("www.alpha.example"))
	d := e.domain("www.alpha.example")
	if rec := b.do("POST", portalURL+base, form("x.sap13ns.site")); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `action="`+base+`"`) {
		t.Fatalf("a network host: %d", rec.Code)
	}
	e.dns.point(d.Host, d.VerifyToken)
	b.post(base+"/check", back, form(d.Host))
	b.post(base+"/primary", back, form(d.Host))
	if s, _ := e.n.Site(e.ctx, a.ID); s.PrimaryHost != d.Host {
		t.Fatalf("primary %s", s.PrimaryHost)
	}
	b.post(base+"/primary", back, form("alpha.sap13ns.site"))
	b.post(base+"/remove", back, form(d.Host))
	if ds, _ := e.n.Domains(e.ctx, a.ID); len(ds) != 1 {
		t.Fatalf("domains %+v", ds)
	}
	for _, action := range []string{"network.domain_added", "network.domain_verified", "network.domain_primary", "network.domain_removed"} {
		if n := audits(t, e.portal, action); n == 0 {
			t.Fatalf("no %s audit", action)
		}
	}
	// The plan's limit holds for network admins too.
	b.post(base, back, form("a.alpha.example"))
	b.post(base, back, form("b.alpha.example"))
	if rec := b.do("POST", portalURL+base, form("c.alpha.example")); rec.Code != http.StatusForbidden {
		t.Fatalf("beyond the limit: %d", rec.Code)
	}
	// The portal has no custom domains.
	if rec := b.do("POST", portalURL+siteURL(e.portalID(), "/domains"), form("portal.example")); rec.Code != http.StatusNotFound {
		t.Fatalf("a custom domain on the portal: %d", rec.Code)
	}
}
