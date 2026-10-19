package network

// The hooks a site's server gets from the registry: wake-ups for every
// site, the single sign-on client for a tenant (web.SSOClient) and the
// network itself for the portal (web.Portal). They translate between the
// network's types and errors and the web's.

import (
	"context"
	"errors"
	"slices"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// hooks connect a site's server to the registry.
type hooks struct {
	r  *Registry
	id int64
}

func (h hooks) Wakeup(ctx context.Context, at time.Time) {
	if err := h.r.net.WakeAt(ctx, h.id, at); err != nil {
		h.r.log.Error("schedule wake-up", "site", h.id, "err", err)
	}
}

// siteHooks are the hooks of a tenant: it signs users in through the
// portal.
type siteHooks struct{ hooks }

var _ web.SSOClient = siteHooks{}

func (h siteHooks) SSOAuthorize() (string, int64) { return h.r.cfg.AbsURL("/sso/authorize"), h.id }

func (h siteHooks) RedeemSSO(ctx context.Context, code, verifier, redirectURI string) (*web.SSOGrant, error) {
	g, err := h.r.net.RedeemSSOCode(ctx, h.id, code, verifier, redirectURI)
	if errors.Is(err, ErrSSOCode) {
		return nil, errors.Join(web.ErrSSOInvalid, err)
	}
	if err != nil {
		return nil, ssoErr(err)
	}
	return &web.SSOGrant{AccountID: g.AccountID, Operator: g.Operator, Emergency: g.Emergency, Role: g.Role,
		Username: g.Username, Email: g.Email, DisplayName: g.DisplayName}, nil
}

func (h siteHooks) CheckSSO(ctx context.Context, g *web.SSOGrant) (string, error) {
	role, err := h.r.net.CheckSSOGrant(ctx, h.id, g.AccountID, g.Operator, g.Emergency)
	return role, ssoErr(err)
}

// ssoErr maps the refusals of single sign-on to the web's.
func ssoErr(err error) error {
	switch {
	case errors.Is(err, ErrNoSupport):
		return errors.Join(web.ErrSSONoSupport, err)
	case errors.Is(err, ErrNoEmergency):
		return errors.Join(web.ErrSSONoEmergency, err)
	case errors.Is(err, ErrSSODenied):
		return errors.Join(web.ErrSSODenied, err)
	}
	return err
}

// portalHooks are the hooks of the portal: the network's accounts and
// sites.
type portalHooks struct{ hooks }

var _ web.Portal = portalHooks{}

func (h portalHooks) SSOSite(ctx context.Context, siteID int64) (*web.SSOSite, error) {
	s, err := h.r.net.Site(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if s.Kind != KindTenant || !slices.Contains(routable, s.Status) {
		return nil, store.ErrNotFound
	}
	domains, err := h.r.net.Domains(ctx, siteID)
	if err != nil {
		return nil, err
	}
	out := &web.SSOSite{ID: s.ID, Slug: s.Slug, Status: s.Status}
	for _, d := range domains {
		if d.VerifiedAt != nil {
			out.RedirectURIs = append(out.RedirectURIs, h.r.origin(d.Host)+"/sso/callback")
		}
	}
	return out, nil
}

func (h portalHooks) SSOAccess(ctx context.Context, siteID, accountID int64) (web.SiteAccess, error) {
	a, err := h.r.net.SSOAccess(ctx, siteID, accountID)
	if err != nil {
		return web.SiteAccess{}, ssoErr(err)
	}
	return web.SiteAccess{Role: a.Role, Operator: a.Operator, Emergency: a.Emergency}, nil
}

func (h portalHooks) IssueSSOCode(ctx context.Context, c web.SSOCode) (string, error) {
	return h.r.net.IssueSSOCode(ctx, SSOCode{SiteID: c.SiteID, AccountID: c.AccountID, Challenge: c.Challenge,
		RedirectURI: c.RedirectURI, Operator: c.Operator, Emergency: c.Emergency})
}

func (h portalHooks) IsNetworkAdmin(ctx context.Context, accountID int64) (bool, error) {
	return h.r.net.IsNetworkAdmin(ctx, accountID)
}

func (h portalHooks) CheckAccountChange(ctx context.Context, accountID int64, remove bool) error {
	err := h.r.net.CheckAccountChange(ctx, accountID, remove)
	switch {
	case errors.Is(err, ErrState):
		return errors.Join(web.ErrLastNetworkAdmin, err)
	case errors.Is(err, ErrOwned):
		return errors.Join(web.ErrOwnsSites, err)
	}
	return err
}

func (h portalHooks) AccountChanged(ctx context.Context, accountID int64, removed bool) error {
	return h.r.net.AccountChanged(ctx, accountID, removed)
}

// origin is the URL of a host of the network, with its scheme and port.
func (r *Registry) origin(host string) string {
	o := r.cfg.BaseURL.Scheme + "://" + host
	if port := r.cfg.BaseURL.Port(); port != "" {
		o += ":" + port
	}
	return o
}

// billingNotice has the portal tell an account about its billing, in the
// portal's language.
func (r *Registry) billingNotice(ctx context.Context, n BillingNotice) {
	t := r.table.Load()
	if t == nil || t.network == nil {
		return
	}
	e, err := r.acquireFor(ctx, t.network, loadTimeout, false)
	if err != nil {
		r.log.Error("billing notice", "account", n.AccountID, "err", err)
		return
	}
	defer r.releaseFor(e, false)
	if err := e.app.Server().NotifyBilling(ctx, web.BillingNotice{Kind: n.Kind, AccountID: n.AccountID, Slug: n.Slug, Host: n.Host,
		GraceDays: n.GraceDays}); err != nil {
		r.log.Error("billing notice", "account", n.AccountID, "site", n.SiteID, "err", err)
	}
}
