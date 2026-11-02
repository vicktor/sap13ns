package network

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// The super administrator lets a paid plan leave out the credit; a free
// plan cannot, and its sites keep it.
func TestPlanHideCredit(t *testing.T) {
	e := newNetEnv(t, nil)
	ctx := context.Background()
	pro := e.tenant("credit-pro", "pro")
	free := e.tenant("credit-free", PlanPersonal)
	hides := func(id int64) bool {
		t.Helper()
		h, err := siteHooks{r: e.reg, id: id}.HideCredit(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if hides(pro.ID) || hides(free.ID) {
		t.Fatal("a plan leaves out the credit by default")
	}
	if err := e.reg.net.SetPlanHideCredit(ctx, "pro", true); err != nil {
		t.Fatal(err)
	}
	if !hides(pro.ID) {
		t.Error("the paid plan still shows the credit")
	}
	if err := e.reg.net.SetPlanHideCredit(ctx, PlanPersonal, true); !errors.Is(err, ErrFreePlanCredit) {
		t.Errorf("free plan: %v", err)
	}
	if hides(free.ID) {
		t.Error("the free plan leaves out the credit")
	}
	if err := e.reg.net.SetPlanHideCredit(ctx, "nope", true); err == nil {
		t.Error("an unknown plan was changed")
	}
	if err := e.reg.net.SetPlanHideCredit(ctx, "pro", false); err != nil || hides(pro.ID) {
		t.Errorf("switching it back on: %v", err)
	}
}

// From Network → Plans: only a super admin has the switch, and the sites
// of the plan follow it.
func TestPlanCreditSwitch(t *testing.T) {
	e := newNetEnv(t, nil)
	pro := e.tenant("switch-pro", "pro")
	const credit = `<a href="https://sap13ns.site">sap13ns</a>`
	const plans, action = "/admin/network/plans", "/admin/network/plans/pro/credit"
	nina, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "nina", Email: "nina@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, nina, 0); err != nil {
		t.Fatal(err)
	}
	e.mfa(nina)
	nb := e.browser()
	csrf := nb.portalSignIn(nina)
	body := nb.page(plans)
	if !strings.Contains(body, `action="`+action+`"`) || strings.Contains(body, "/plans/personal/credit") {
		t.Fatal("the switch is missing on the paid plan, or offered on the free one")
	}
	if rec := nb.do("POST", portalURL+"/admin/network/plans/personal/credit", url.Values{"csrf": {csrf}, "hide": {"1"}}); rec.Code != http.StatusSeeOther {
		t.Errorf("free plan: %d", rec.Code)
	}
	nb.post(action, plans, url.Values{"csrf": {csrf}, "hide": {"1"}})
	if p, _ := e.n.Plan(e.ctx, PlanPersonal); p.HideCredit {
		t.Error("the free plan leaves out the credit")
	}
	if home := e.do("GET", "https://"+pro.PrimaryHost+"/").Body.String(); strings.Contains(home, credit) || !strings.Contains(home, "footer-meta") {
		t.Error("the pro site still shows the credit")
	}

	// A network admin who is not a super admin sees the state, not the switch.
	omar, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "omar", Email: "omar@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, omar, nina); err != nil {
		t.Fatal(err)
	}
	e.mfa(omar)
	xb := e.browser()
	xcsrf := xb.portalSignIn(omar)
	if body := xb.page(plans); strings.Contains(body, action) || !strings.Contains(body, "Quitado") {
		t.Error("a network admin who is not a super admin has the switch, or no state")
	}
	if rec := xb.do("POST", portalURL+action, url.Values{"csrf": {xcsrf}, "hide": {"0"}}); rec.Code != http.StatusForbidden {
		t.Errorf("a network admin switched it: %d", rec.Code)
	}
	if p, _ := e.n.Plan(e.ctx, "pro"); !p.HideCredit {
		t.Error("the plan changed")
	}
	nb.post(action, plans, url.Values{"csrf": {csrf}, "hide": {"0"}})
	if p, _ := e.n.Plan(e.ctx, "pro"); p.HideCredit {
		t.Error("the credit was not switched back on")
	}
}
