package network

// The portal's network admin area and "My sites" (web.PortalAdmin): the
// network's sites, members, admins and plans, in the web's types.

import (
	"context"
	"errors"

	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

var _ web.PortalAdmin = portalHooks{}

// webErr maps the network's errors to the web's.
func webErr(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return errors.Join(web.ErrNetworkInvalid, err)
	case errors.Is(err, ErrState):
		return errors.Join(web.ErrNetworkState, err)
	}
	return err
}

func (h portalHooks) netSite(ctx context.Context, s *Site) (web.NetSite, error) {
	approver, err := h.r.net.AccountName(ctx, s.ApprovedBy)
	if err != nil {
		return web.NetSite{}, err
	}
	out := web.NetSite{ID: s.ID, Slug: s.Slug, Status: s.Status, Plan: s.Plan, Host: s.PrimaryHost, OwnerID: s.OwnerID,
		Owner: s.OwnerUsername, StorageBytes: s.StorageBytes, StorageCheckedAt: s.StorageCheckedAt,
		SuspendedReason: s.SuspendedReason, SuspendedAt: s.SuspendedAt, ApprovedAt: s.ApprovedAt, ApprovedBy: approver,
		CreatedAt: s.CreatedAt, DeletedAt: s.DeletedAt}
	if s.PrimaryHost != "" {
		out.URL = h.r.origin(s.PrimaryHost)
	}
	return out, nil
}

func (h portalHooks) netSites(ctx context.Context, sites []Site) ([]web.NetSite, error) {
	out := make([]web.NetSite, 0, len(sites))
	for i := range sites {
		s, err := h.netSite(ctx, &sites[i])
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (h portalHooks) Sites(ctx context.Context, q web.NetSiteQuery) ([]web.NetSite, int, error) {
	sites, total, err := h.r.net.ListSites(ctx, SiteQuery{Status: q.Status, Kind: KindTenant, Search: q.Search, Limit: q.Limit, Offset: q.Offset})
	if err != nil {
		return nil, 0, err
	}
	out, err := h.netSites(ctx, sites)
	return out, total, err
}

func netPlan(p *Plan) *web.NetPlan {
	return &web.NetPlan{Slug: p.Slug, Name: p.Name, Paid: p.Paid, HandApproved: p.HandApproved, Commercial: p.Commercial,
		SitesPerGrant: p.SitesPerGrant, StorageBytes: p.StorageBytes, CustomDomains: p.CustomDomains,
		PluginUploads: p.PluginUploads, ThemeUploads: p.ThemeUploads}
}

func (h portalHooks) SiteDetail(ctx context.Context, siteID int64) (*web.NetSiteDetail, error) {
	s, err := h.r.net.Site(ctx, siteID)
	if err != nil {
		return nil, err
	}
	if s.Kind != KindTenant {
		return nil, store.ErrNotFound
	}
	site, err := h.netSite(ctx, s)
	if err != nil {
		return nil, err
	}
	d := &web.NetSiteDetail{Site: site}
	if p, err := h.r.net.Plan(ctx, s.Plan); err == nil {
		d.Plan = netPlan(p)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	domains, err := h.r.net.Domains(ctx, siteID)
	if err != nil {
		return nil, err
	}
	for _, x := range domains {
		d.Domains = append(d.Domains, web.NetDomain{Host: x.Host, Kind: x.Kind, Primary: x.Primary, VerifiedAt: x.VerifiedAt})
	}
	members, err := h.r.net.Members(ctx, siteID)
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		d.Members = append(d.Members, web.NetMember{AccountID: m.AccountID, Username: m.Username, Email: m.Email, Role: m.Role,
			Owner: m.AccountID == s.OwnerID, LastSSOAt: m.LastSSOAt, CreatedAt: m.CreatedAt})
	}
	return d, nil
}

func (h portalHooks) ApproveSite(ctx context.Context, siteID, by int64) error {
	return webErr(h.r.net.ApproveSite(ctx, siteID, by))
}

func (h portalHooks) RejectSite(ctx context.Context, siteID int64) error {
	return webErr(h.r.net.RejectSite(ctx, siteID))
}

func (h portalHooks) SuspendSite(ctx context.Context, siteID int64, reason string) error {
	return webErr(h.r.net.SuspendSite(ctx, siteID, reason))
}

func (h portalHooks) ResumeSite(ctx context.Context, siteID int64) error {
	return webErr(h.r.net.ResumeSite(ctx, siteID))
}

func (h portalHooks) DeleteSite(ctx context.Context, siteID int64) error {
	return webErr(h.r.net.DeleteSite(ctx, siteID))
}

func (h portalHooks) SetMember(ctx context.Context, siteID, accountID int64, role string, by int64) error {
	return webErr(h.r.net.SetMember(ctx, siteID, accountID, role, by))
}

func (h portalHooks) RemoveMember(ctx context.Context, siteID, accountID int64) error {
	return webErr(h.r.net.RemoveMember(ctx, siteID, accountID))
}

func (h portalHooks) NetworkAdmins(ctx context.Context) ([]web.NetAdmin, error) {
	admins, err := h.r.net.NetworkAdmins(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.NetAdmin, 0, len(admins))
	for _, a := range admins {
		by, err := h.r.net.AccountName(ctx, a.GrantedBy)
		if err != nil {
			return nil, err
		}
		out = append(out, web.NetAdmin{AccountID: a.AccountID, Username: a.Username, Email: a.Email, GrantedBy: by, CreatedAt: a.CreatedAt})
	}
	return out, nil
}

func (h portalHooks) AddNetworkAdmin(ctx context.Context, accountID, by int64) error {
	return h.r.net.AddNetworkAdmin(ctx, accountID, by)
}

func (h portalHooks) RemoveNetworkAdmin(ctx context.Context, accountID int64) error {
	err := h.r.net.RemoveNetworkAdmin(ctx, accountID)
	if errors.Is(err, ErrState) {
		return errors.Join(web.ErrLastNetworkAdmin, err)
	}
	return err
}

func (h portalHooks) Plans(ctx context.Context) ([]web.NetPlan, error) {
	plans, err := h.r.net.Plans(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.NetPlan, 0, len(plans))
	for i := range plans {
		out = append(out, *netPlan(&plans[i]))
	}
	return out, nil
}

func (h portalHooks) AccountSites(ctx context.Context, accountID int64) ([]web.NetSite, error) {
	sites, err := h.r.net.MemberSites(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]web.NetSite, 0, len(sites))
	for i := range sites {
		s, err := h.netSite(ctx, &sites[i].Site)
		if err != nil {
			return nil, err
		}
		s.Role = sites[i].Role
		out = append(out, s)
	}
	return out, nil
}

func (h portalHooks) RequestSite(ctx context.Context, accountID int64, slug string) (*web.NetSite, error) {
	s, err := h.r.net.RequestSite(ctx, accountID, slug)
	if err != nil {
		return nil, webErr(err)
	}
	out, err := h.netSite(ctx, s)
	return &out, err
}
