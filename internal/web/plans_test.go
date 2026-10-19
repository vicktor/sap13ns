package web

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"sap13ns/internal/config"
	"sap13ns/internal/testutil"
)

type fakePlansPortal struct {
	Portal // only Plans is called
	plans  []NetPlan
	err    error
	calls  int
}

func (f *fakePlansPortal) Plans(context.Context) ([]NetPlan, error) {
	f.calls++
	return f.plans, f.err
}

type fakeOffers struct {
	PortalBilling // only PlanOffers is called
	offers        []PlanOffer
}

func (f *fakeOffers) PlanOffers(context.Context) ([]PlanOffer, error) { return f.offers, nil }

func TestPortalPlans(t *testing.T) {
	s := networkServer(t, testutil.Store(t), func(c *config.Config, _ *Deps) { c.SitesDomain = "sap13ns.site" })
	portal := &fakePlansPortal{plans: []NetPlan{
		{Slug: "personal", Name: "Personal", HandApproved: true, SitesPerGrant: 1, StorageBytes: 1 << 30},
		{Slug: "pro", Name: "Pro", Paid: true, Commercial: true, SitesPerGrant: 1, StorageBytes: 10 << 30, CustomDomains: 2,
			PluginUploads: true, ThemeUploads: true},
		{Slug: "business", Name: "Business", Paid: true, Commercial: true, SitesPerGrant: 3, StorageBytes: 50 << 30, CustomDomains: 1},
	}}
	s.portal = portal

	// Without billing a network only gives free sites: the paid plans have
	// no price to be sold at.
	views := s.planViews()
	if len(views) != 1 || views[0].Slug != "personal" || !views[0].Free || views[0].Highlight {
		t.Fatalf("without billing: %+v", views)
	}
	want := []PlanFeature{{Text: "1 site"}, {Text: "An address under %s", Arg: "sap13ns.site"}, {Text: "%s storage", Arg: "1 GB"},
		{Text: "Plugins from the network's catalogue"}, {Text: "Reviewed by a person before it goes live"}}
	if !reflect.DeepEqual(views[0].Features, want) {
		t.Fatalf("personal features: %+v", views[0].Features)
	}
	if views[0].SignupURL != "" {
		t.Fatalf("registration is closed, yet the plan links to %q", views[0].SignupURL)
	}

	// Cached: the template function does not query on every render.
	s.planViews()
	if portal.calls != 1 {
		t.Fatalf("%d queries for two renders", portal.calls)
	}

	s.billing = &fakeOffers{offers: []PlanOffer{
		{Plan: "pro", Yearly: true, Amount: 5000, Currency: "eur"},
		{Plan: "pro", Amount: 500, Currency: "eur"},
		{Plan: "business", Yearly: true, Amount: 12000, Currency: "eur"},
	}}
	s.plans = planCache{}
	views = s.planViews()
	if len(views) != 3 {
		t.Fatalf("with billing: %+v", views)
	}
	pro, business := views[1], views[2]
	if pro.Price != "5.00 EUR" || pro.Interval != "month" || !pro.Highlight || pro.Free || !pro.Commercial {
		t.Fatalf("pro: the monthly price, highlighted: %+v", pro)
	}
	if business.Price != "120.00 EUR" || business.Interval != "year" || business.Highlight {
		t.Fatalf("business: only a yearly price: %+v", business)
	}
	want = []PlanFeature{{Text: "1 site"}, {Text: "Up to %s custom domains with HTTPS", Arg: "2"}, {Text: "%s storage", Arg: "10 GB"},
		{Text: "Upload your own plugins, reviewed before they run"}, {Text: "Upload your own themes"}}
	if !reflect.DeepEqual(pro.Features, want) {
		t.Fatalf("pro features: %+v", pro.Features)
	}
	if business.Features[0] != (PlanFeature{Text: "%s sites", Arg: "3"}) || business.Features[1] != (PlanFeature{Text: "Custom domain with HTTPS"}) {
		t.Fatalf("business features: %+v", business.Features)
	}

	// A failed load keeps the table it had.
	s.plans.at = s.plans.at.Add(-2 * planCacheTTL)
	portal.err = errors.New("database down")
	if got := s.planViews(); len(got) != 3 {
		t.Fatalf("after a failed load: %+v", got)
	}
}

func TestFilesizeGB(t *testing.T) {
	for n, want := range map[int64]string{1 << 30: "1 GB", 50 << 30: "50 GB", 3 << 29: "1.5 GB", 5 << 20: "5.0 MB"} {
		if got := filesize(n); got != want {
			t.Errorf("filesize(%d) = %q, want %q", n, got, want)
		}
	}
}
