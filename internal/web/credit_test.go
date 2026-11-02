package web

import (
	"strings"
	"testing"
)

// The "Powered by sap13ns" credit: on a single-site install, on a free
// plan and on a network that has not turned it off, every page shows it;
// with SAP13NS_PAID_PLANS_HIDE_CREDIT a paid plan in good standing leaves
// it out, and a held one (AIPlan.Paid false) shows it again, cached pages
// included.
func TestCreditFollowsThePlan(t *testing.T) {
	e := newPluginEnv(t)
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	home := func() string {
		t.Helper()
		return e.do("GET", "/", nil, "").Body.String()
	}
	setPlan := func(paid bool) {
		e.s.limits = planLimits{plan: AIPlan{Plan: "P", Paid: paid}}
		e.s.ai.mu.Lock()
		e.s.ai.plan = nil
		e.s.ai.mu.Unlock()
	}
	if !strings.Contains(home(), credit) {
		t.Fatal("a single-site install lacks the credit")
	}
	setPlan(false)
	if !strings.Contains(home(), credit) {
		t.Error("a free plan lacks the credit")
	}
	setPlan(true)
	if !strings.Contains(home(), credit) {
		t.Error("a paid plan lacks the credit on a network that keeps it")
	}
	e.s.cfg.PaidHideCredit = true
	for i := range 2 { // rendered, then from the page cache
		if body := home(); strings.Contains(body, credit) || !strings.Contains(body, `class="footer-meta"`) {
			t.Errorf("paid plan, request %d: credit shown or footer lost", i)
		}
	}
	setPlan(false) // held: unpaid, cancelled
	if !strings.Contains(home(), credit) {
		t.Error("a held plan's cached page lacks the credit")
	}
}
