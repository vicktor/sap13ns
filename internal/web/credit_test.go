package web

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/license"
)

// creditLimits is a site whose plan may leave out the credit.
type creditLimits struct {
	planLimits
	hide bool
}

func (c creditLimits) HideCredit(context.Context) (bool, error) { return c.hide, nil }

// The "Powered by sap13ns" credit: on a single-site install, on a free
// plan and on a paid plan the super administrator has not changed, every
// page shows it; a paid plan set to leave it out (Network → Plans) does,
// and a held one shows it again, cached pages included.
func TestCreditFollowsThePlan(t *testing.T) {
	e := newPluginEnv(t)
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	home := func() string {
		t.Helper()
		return e.do("GET", "/", nil, "").Body.String()
	}
	setPlan := func(hide bool) {
		e.s.limits = creditLimits{planLimits: planLimits{plan: AIPlan{Plan: "P", Paid: true}}, hide: hide}
		e.s.credit.mu.Lock()
		e.s.credit.at = time.Time{}
		e.s.credit.mu.Unlock()
	}
	if !strings.Contains(home(), credit) {
		t.Fatal("a single-site install lacks the credit")
	}
	e.s.limits = planLimits{plan: AIPlan{Plan: "P", Paid: true}}
	if !strings.Contains(home(), credit) {
		t.Error("limits that cannot tell lack the credit")
	}
	setPlan(false)
	if !strings.Contains(home(), credit) {
		t.Error("a plan that keeps the credit lacks it")
	}
	setPlan(true)
	for i := range 2 { // rendered, then from the page cache
		if body := home(); strings.Contains(body, credit) || !strings.Contains(body, `class="footer-meta"`) {
			t.Errorf("plan without the credit, request %d: credit shown or footer lost", i)
		}
	}
	setPlan(false) // held, or switched back on
	if !strings.Contains(home(), credit) {
		t.Error("the cached page of a plan that shows the credit again lacks it")
	}
	// The answer is kept for a minute.
	e.s.limits = creditLimits{hide: true}
	if !strings.Contains(home(), credit) {
		t.Error("the plan's answer was not cached")
	}
}

// An uploaded theme is checked for the credit on every page; on a plan
// that leaves it out, its pages are still served.
func TestCreditUploadedTheme(t *testing.T) {
	e := newPluginEnv(t)
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	if rec := e.uploadTheme("tiny.zip", zipOf(t, "tiny", tinyTheme("tiny-one"), nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.admin("/admin/appearance/themes/tiny/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}
	e.s.limits = creditLimits{planLimits: planLimits{plan: AIPlan{Plan: "P", Paid: true}}, hide: true}
	for _, path := range []string{"/", "/no-such-page"} {
		rec := e.do("GET", path, nil, "")
		if body := rec.Body.String(); rec.Code >= 500 || strings.Contains(body, credit) || !strings.Contains(body, "tiny-one") {
			t.Errorf("%s: %d, credit shown or not the uploaded theme\n%s", path, rec.Code, body)
		}
	}
}

// A single-site install with a white-label licence from the licensor for
// its domain leaves out the credit; an expired licence, one for another
// domain or one signed by another key keeps it, and so does a network's
// site (its plan decides).
func TestCreditFollowsTheLicence(t *testing.T) {
	e := newPluginEnv(t)
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	use := func(name string, l *license.License, key ed25519.PublicKey) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sap13ns.license")
		data, err := license.Encode(l, priv)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(path, data, 0o600)
		f := license.NewFile(path)
		f.Key = key
		e.s.license = f
	}
	lic := func(domain string, expires time.Time) *license.License {
		return &license.License{ID: "t", Type: license.TypeWhiteLabel, Licensee: "Test",
			Domains: []string{domain}, Issued: now.Add(-time.Hour), Expires: expires}
	}
	home := func() string { t.Helper(); return e.do("GET", "/", nil, "").Body.String() }

	use("valid", lic("example.test", now.AddDate(1, 0, 0)), pub)
	for i := range 2 { // rendered, then from the page cache
		if body := home(); strings.Contains(body, credit) || !strings.Contains(body, `class="footer-meta"`) {
			t.Errorf("valid licence, request %d: credit shown or footer lost", i)
		}
	}
	for name, l := range map[string]*license.License{
		"expired":        lic("example.test", now.Add(-time.Minute)),
		"another domain": lic("other.test", now.AddDate(1, 0, 0)),
	} {
		use(name, l, pub)
		if !strings.Contains(home(), credit) {
			t.Errorf("%s licence: the credit is left out", name)
		}
	}
	other, _, _ := ed25519.GenerateKey(nil)
	use("forged", lic("example.test", now.AddDate(1, 0, 0)), other)
	if !strings.Contains(home(), credit) {
		t.Error("a licence the key does not verify leaves out the credit")
	}
	// A plan that keeps the credit wins over a licence.
	use("valid", lic("example.test", now.AddDate(1, 0, 0)), pub)
	e.s.limits = creditLimits{planLimits: planLimits{plan: AIPlan{Plan: "P", Paid: true}}, hide: false}
	if !strings.Contains(home(), credit) {
		t.Error("a network's site follows the licence instead of its plan")
	}
}
