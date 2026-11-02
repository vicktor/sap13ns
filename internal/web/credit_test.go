package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
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
