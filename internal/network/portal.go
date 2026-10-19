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
	switch {
	case errors.Is(err, ErrSSOCode):
		return nil, errors.Join(web.ErrSSOInvalid, err)
	case errors.Is(err, ErrSSODenied):
		return nil, errors.Join(web.ErrSSODenied, err)
	case err != nil:
		return nil, err
	}
	return &web.SSOGrant{AccountID: g.AccountID, Operator: g.Operator, Role: g.Role,
		Username: g.Username, Email: g.Email, DisplayName: g.DisplayName}, nil
}

func (h siteHooks) CheckSSO(ctx context.Context, g *web.SSOGrant) (string, error) {
	role, err := h.r.net.CheckSSOGrant(ctx, h.id, g.AccountID, g.Operator)
	if errors.Is(err, ErrSSODenied) {
		return "", errors.Join(web.ErrSSODenied, err)
	}
	return role, err
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

func (h portalHooks) SSOAccess(ctx context.Context, siteID, accountID int64) (string, bool, error) {
	role, op, err := h.r.net.SSOAccess(ctx, siteID, accountID)
	if errors.Is(err, ErrSSODenied) {
		return "", false, errors.Join(web.ErrSSODenied, err)
	}
	return role, op, err
}

func (h portalHooks) IssueSSOCode(ctx context.Context, c web.SSOCode) (string, error) {
	return h.r.net.IssueSSOCode(ctx, SSOCode{SiteID: c.SiteID, AccountID: c.AccountID, Challenge: c.Challenge,
		RedirectURI: c.RedirectURI, Operator: c.Operator})
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
