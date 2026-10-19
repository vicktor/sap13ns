package web

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
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

	// Pro is sold by the month and by the year, VAT included; Business by
	// the year only, VAT on top.
	s.billing = &fakeOffers{offers: []PlanOffer{
		{Plan: "pro", Yearly: true, Amount: 9900, Currency: "eur", TaxBehavior: "inclusive"},
		{Plan: "pro", Amount: 900, Currency: "eur", TaxBehavior: "inclusive"},
		{Plan: "business", Yearly: true, Amount: 33000, Currency: "eur", TaxBehavior: "exclusive"},
	}}
	s.plans = planCache{}
	views = s.planViews()
	if len(views) != 3 {
		t.Fatalf("with billing: %+v", views)
	}
	pro, business := views[1], views[2]
	if pro.Price != "9\u00a0€" || pro.Interval != "month" || pro.YearlyPrice != "99\u00a0€" || pro.FreeMonths != 1 || pro.Tax != "included" ||
		!pro.Highlight || pro.Free || !pro.Commercial {
		t.Fatalf("pro: the monthly price and the yearly one, VAT included, highlighted: %+v", pro)
	}
	if business.Price != "330\u00a0€" || business.Interval != "year" || business.YearlyPrice != "" || business.FreeMonths != 0 ||
		business.Tax != "added" || business.Highlight {
		t.Fatalf("business: only a yearly price, VAT added: %+v", business)
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

// TestPortalPlanPrices: both intervals with both tax behaviours, and the
// prices that save no whole month or say nothing of VAT.
func TestPortalPlanPrices(t *testing.T) {
	s := networkServer(t, testutil.Store(t), nil)
	s.portal = &fakePlansPortal{plans: []NetPlan{
		{Slug: "pro", Name: "Pro", Paid: true, Commercial: true, SitesPerGrant: 1},
		{Slug: "business", Name: "Business", Paid: true, Commercial: true, SitesPerGrant: 1},
		{Slug: "team", Name: "Team", Paid: true, Commercial: true, SitesPerGrant: 1},
		{Slug: "agency", Name: "Agency", Paid: true, Commercial: true, SitesPerGrant: 1},
	}}
	s.billing = &fakeOffers{offers: []PlanOffer{
		{Plan: "pro", Amount: 900, Currency: "eur", TaxBehavior: "inclusive"},
		{Plan: "pro", Yearly: true, Amount: 9900, Currency: "eur", TaxBehavior: "inclusive"},
		{Plan: "business", Amount: 3000, Currency: "eur", TaxBehavior: "exclusive"},
		{Plan: "business", Yearly: true, Amount: 33000, Currency: "eur", TaxBehavior: "exclusive"},
		{Plan: "team", Amount: 1250, Currency: "usd", TaxBehavior: "unspecified"},
		{Plan: "team", Yearly: true, Amount: 10500, Currency: "usd", TaxBehavior: "unspecified"},
		{Plan: "agency", Amount: 5000, Currency: "chf"},
	}}
	views, err := s.portalPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		price, interval, yearly string
		free                    int
		tax                     string
	}
	want := []row{
		{"9\u00a0€", "month", "99\u00a0€", 1, "included"},
		{"30\u00a0€", "month", "330\u00a0€", 1, "added"},
		{"$12.50", "month", "$105", 0, ""}, // saves 45 dollars: not a whole month
		{"50\u00a0CHF", "month", "", 0, ""},
	}
	if len(views) != len(want) {
		t.Fatalf("%d plans: %+v", len(views), views)
	}
	for i, v := range views {
		if got := (row{v.Price, v.Interval, v.YearlyPrice, v.FreeMonths, v.Tax}); got != want[i] {
			t.Errorf("%s: %+v, want %+v", v.Slug, got, want[i])
		}
	}
	if !views[0].Highlight || views[1].Highlight {
		t.Error("the first paid plan is the highlighted one")
	}
}

func TestFilesizeGB(t *testing.T) {
	for n, want := range map[int64]string{1 << 30: "1 GB", 50 << 30: "50 GB", 3 << 29: "1.5 GB", 5 << 20: "5.0 MB"} {
		if got := filesize(n); got != want {
			t.Errorf("filesize(%d) = %q, want %q", n, got, want)
		}
	}
}

// The sap13ns theme writes each feature format that takes an argument in
// quotes (uploaded-theme checks refuse T with a field and arguments), so
// a new format must be added to its plan-feature partial too.
func TestPlanFeatureFormatsInTheme(t *testing.T) {
	b, err := os.ReadFile("../../site/themes/sap13ns/templates/partials/base.html")
	if err != nil {
		t.Fatal(err)
	}
	features := append(planFeatures(NetPlan{SitesPerGrant: 2, CustomDomains: 2, StorageBytes: 1}, "x"), planFeatures(NetPlan{}, "x")...)
	for _, f := range features {
		if f.Arg != "" && !strings.Contains(string(b), `{{T "`+f.Text+`" .Arg}}`) {
			t.Errorf("the plan-feature partial does not write %q", f.Text)
		}
	}
}
