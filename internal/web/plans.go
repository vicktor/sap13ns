package web

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// A PlanFeature is one line of a plan's feature list: a catalogue string,
// with Arg for its %s when it has one.
type PlanFeature struct {
	Text string
	Arg  string
}

// planViews lists the hosting plans the theme's pricing table shows (the
// "plans" template function): on a network's portal the plans it sells,
// in development sample plans so that themes can be designed, and none
// otherwise.
func (s *Server) planViews() []PlanView {
	if s.portal != nil {
		return s.plans.get(s.portalPlans, s)
	}
	if !s.cfg.Dev {
		return nil
	}
	return samplePlans
}

var samplePlans = []PlanView{
	{Slug: "personal", Name: "Personal", Description: "For your blog, portfolio or CV.", Free: true, SignupURL: "/register",
		Features: []PlanFeature{{Text: "1 site"}, {Text: "sap13ns.site subdomain"}, {Text: "1 GB storage"}, {Text: "All themes"}}},
	{Slug: "pro", Name: "Pro", Description: "For freelancers and small businesses.", Price: "9 €", Interval: "month", Commercial: true, Highlight: true,
		SignupURL: "/register", Features: []PlanFeature{{Text: "Custom domain with HTTPS"}, {Text: "10 GB storage"}, {Text: "Your own themes and plugins"}, {Text: "Daily backups"}}},
	{Slug: "business", Name: "Business", Description: "For teams that publish a lot.", Price: "30 €", Interval: "month", Commercial: true,
		SignupURL: "/register", Features: []PlanFeature{{Text: "Everything in Pro"}, {Text: "50 GB storage"}, {Text: "AI assistant credits"}, {Text: "Priority support"}}},
}

// planCacheTTL bounds how long the portal's pricing table may lag behind
// its plans and prices, which change rarely.
const planCacheTTL = time.Minute

// planCache keeps the portal's plan views: the pricing table is rendered
// by a template function, which has no request context to query with.
type planCache struct {
	mu    sync.Mutex
	at    time.Time
	views []PlanView
}

// get returns the cached views, loading them again once they are older
// than planCacheTTL. A failed load keeps the previous views.
func (c *planCache) get(load func(context.Context) ([]PlanView, error), s *Server) []PlanView {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < planCacheTTL {
		return c.views
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	views, err := load(ctx)
	if err != nil {
		s.log.Error("list the plans", "err", err)
		return c.views
	}
	c.views, c.at = views, time.Now()
	return views
}

// portalPlans builds the pricing table from the network's plans. A paid
// plan is listed only with a price to sell it at, so a network without
// billing shows its free plans alone.
func (s *Server) portalPlans(ctx context.Context) ([]PlanView, error) {
	plans, err := s.portal.Plans(ctx)
	if err != nil {
		return nil, err
	}
	prices := map[string]PlanOffer{}
	if s.billing != nil {
		offers, err := s.billing.PlanOffers(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range offers {
			if cur, ok := prices[o.Plan]; !ok || cur.Yearly && !o.Yearly {
				prices[o.Plan] = o // the monthly price when there is one
			}
		}
	}
	signup := ""
	if s.settings().AllowRegistration {
		signup = "/register"
	}
	var out []PlanView
	highlighted := false
	for _, p := range plans {
		v := PlanView{Slug: p.Slug, Name: p.Name, Free: !p.Paid, Commercial: p.Commercial, SignupURL: signup,
			Features: planFeatures(p, s.cfg.SitesDomain)}
		if p.Paid {
			o, ok := prices[p.Slug]
			if !ok {
				continue
			}
			v.Price, v.Interval = o.Price(), "month"
			if o.Yearly {
				v.Interval = "year"
			}
			if !highlighted {
				v.Highlight, highlighted = true, true
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// planFeatures describes what a plan gives, from its limits. The sap13ns
// theme's "plan-feature" partial writes each format that takes an Arg.
func planFeatures(p NetPlan, sitesDomain string) []PlanFeature {
	var f []PlanFeature
	switch {
	case p.SitesPerGrant == 1:
		f = append(f, PlanFeature{Text: "1 site"})
	case p.SitesPerGrant > 1:
		f = append(f, PlanFeature{Text: "%s sites", Arg: strconv.Itoa(p.SitesPerGrant)})
	}
	switch {
	case p.CustomDomains == 1:
		f = append(f, PlanFeature{Text: "Custom domain with HTTPS"})
	case p.CustomDomains > 1:
		f = append(f, PlanFeature{Text: "Up to %s custom domains with HTTPS", Arg: strconv.Itoa(p.CustomDomains)})
	default:
		f = append(f, PlanFeature{Text: "An address under %s", Arg: sitesDomain})
	}
	if p.StorageBytes > 0 {
		f = append(f, PlanFeature{Text: "%s storage", Arg: filesize(p.StorageBytes)})
	}
	if p.PluginUploads {
		f = append(f, PlanFeature{Text: "Upload your own plugins, reviewed before they run"})
	} else {
		f = append(f, PlanFeature{Text: "Plugins from the network's catalogue"})
	}
	if p.ThemeUploads {
		f = append(f, PlanFeature{Text: "Upload your own themes"})
	}
	if p.HandApproved {
		f = append(f, PlanFeature{Text: "Reviewed by a person before it goes live"})
	}
	return f
}
