package network

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// A network admin gives a site a paid plan for a year from its page, as if
// its owner had paid: the plan applies at once, a second grant adds a year,
// the site is held once its year is over, and canceling moves it back to
// the free plan. A site Stripe pays for keeps its subscription. Only a super
// admin (made from the command line) does it.
func TestManualPlan(t *testing.T) {
	e, fs, _ := newBillingEnv(t)
	a := e.personal("alpha")
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
	back := siteURL(a.ID, "#plan")

	if body := nb.page(siteURL(a.ID, "")); !strings.Contains(body, `action="`+siteURL(a.ID, "/plan-year")+`"`) || strings.Contains(body, "/plan-cancel") {
		t.Fatal("the site's page lacks the upgrade form, or offers a cancel")
	}
	nb.post(siteURL(a.ID, "/plan-year"), back, url.Values{"csrf": {csrf}, "plan": {"personal"}})
	if e.row(a.ID).Plan != PlanPersonal {
		t.Fatal("a free plan was granted as a paid one")
	}
	nb.post(siteURL(a.ID, "/plan-year"), back, url.Values{"csrf": {csrf}, "plan": {"pro"}})
	sub := e.subscription(a.ID)
	year := e.clock.now().AddDate(1, 0, 0)
	if e.row(a.ID).Plan != "pro" || sub == nil || !sub.Manual || sub.State != "active" || !sub.PaidUntil.Equal(year) {
		t.Fatalf("after the upgrade: plan %s, %+v", e.row(a.ID).Plan, sub)
	}
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)
	ob := e.browser()
	ob.portalSignIn(e.owner)
	if body := ob.page("/admin/sites"); !strings.Contains(body, "De pago") || !strings.Contains(body, "termina el") {
		t.Errorf("My sites does not show the plan as paid")
	}
	if body := nb.page(siteURL(a.ID, "")); !strings.Contains(body, "concedido a mano hasta el") || !strings.Contains(body, "/plan-cancel") {
		t.Error("the site's page does not show the manual plan and its cancel")
	}

	// Another grant adds a year to the end, and may change the plan.
	e.clock.add(30 * 24 * time.Hour)
	nb.post(siteURL(a.ID, "/plan-year"), back, url.Values{"csrf": {csrf}, "plan": {"business"}})
	if sub := e.subscription(a.ID); e.row(a.ID).Plan != "business" || !sub.PaidUntil.Equal(year.AddDate(1, 0, 0)) {
		t.Fatalf("extended: plan %s, until %v", e.row(a.ID).Plan, sub.PaidUntil)
	}

	// Once the period is over the site is held, as an unpaid one is.
	e.clock.add(e.subscription(a.ID).PaidUntil.Sub(e.clock.now()))
	e.refresh()
	if rec := e.do("GET", "https://"+a.PrimaryHost+"/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after its year: %d", rec.Code)
	}
	if !e.n.Held(true, e.subscription(a.ID), e.clock.now()) {
		t.Fatal("Network.Held disagrees with the host table")
	}
	if body := nb.page(siteURL(a.ID, "")); !strings.Contains(body, "Mejorar durante un año") {
		t.Error("an ended manual plan is not offered again")
	}

	// Canceling: the free plan, served again.
	nb.post(siteURL(a.ID, "/plan-cancel"), back, url.Values{"csrf": {csrf}})
	if r := e.row(a.ID); r.Plan != PlanPersonal || r.SubscriptionID != nil {
		t.Fatalf("after canceling: plan %s, subscription %v", r.Plan, r.SubscriptionID)
	}
	e.refresh()
	e.served(a.PrimaryHost, http.StatusOK)
	nb.post(siteURL(a.ID, "/plan-cancel"), back, url.Values{"csrf": {csrf}}) // nothing to cancel: an error, not a crash
	if e.row(a.ID).Plan != PlanPersonal {
		t.Fatal("a second cancel changed the plan")
	}

	// A network admin added from the portal is not a super admin: no form,
	// and the actions are refused.
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
	if body := xb.page(siteURL(a.ID, "")); strings.Contains(body, "/plan-year") || !strings.Contains(body, "Solo un superadministrador") {
		t.Error("a network admin who is not a super admin is offered the upgrade")
	}
	for _, action := range []string{"/plan-year", "/plan-cancel"} {
		if rec := xb.do("POST", portalURL+siteURL(a.ID, action), url.Values{"csrf": {xcsrf}, "plan": {"pro"}}); rec.Code != http.StatusForbidden {
			t.Errorf("%s by a network admin: %d", action, rec.Code)
		}
	}
	if e.row(a.ID).Plan != PlanPersonal {
		t.Fatal("a network admin who is not a super admin granted a plan")
	}
	// Nor do they revoke a super admin; the super admin revokes them.
	const admins = "/admin/network/admins"
	if body := xb.page(admins); !strings.Contains(body, "Superadministrador") || strings.Contains(body, admins+"/"+strconv.FormatInt(nina, 10)+"/delete") {
		t.Error("the admins page lacks the super admin, or offers to revoke them")
	}
	if rec := xb.do("POST", portalURL+admins+"/"+strconv.FormatInt(nina, 10)+"/delete", url.Values{"csrf": {xcsrf}}); rec.Code != http.StatusForbidden {
		t.Errorf("a super admin revoked by a network admin: %d", rec.Code)
	}
	if ok, _ := e.n.IsSuperAdmin(e.ctx, nina); !ok {
		t.Fatal("nina is no longer a super admin")
	}
	nb.post(admins+"/"+strconv.FormatInt(nina, 10)+"/delete", admins, url.Values{"csrf": {csrf}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, nina); !ok {
		t.Fatal("the last super admin was revoked")
	}
	if err := e.n.CheckAccountChange(e.ctx, nina, false); !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("disabling the last super admin: %v", err)
	}
	nb.post(admins+"/"+strconv.FormatInt(omar, 10)+"/delete", admins, url.Values{"csrf": {csrf}})
	if ok, _ := e.n.IsNetworkAdmin(e.ctx, omar); ok {
		t.Fatal("the super admin could not revoke a network admin")
	}

	// A site Stripe pays for is left to Stripe.
	b := e.personal("bravo")
	e.subscribe(fs, b, "pro:month", "sub_test0020", "price_pro_month")
	nb.post(siteURL(b.ID, "/plan-year"), siteURL(b.ID, "#plan"), url.Values{"csrf": {csrf}, "plan": {"business"}})
	if sub := e.subscription(b.ID); e.row(b.ID).Plan != "pro" || sub.Manual {
		t.Fatalf("a Stripe site was upgraded by hand: plan %s, %+v", e.row(b.ID).Plan, sub)
	}
}
