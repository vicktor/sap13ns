package network

// The site switcher of the admin (web.SiteSwitcher): every site of the
// network, the portal included, lists the sites an account is a member of.
// The list comes from the network tables, and each site's title from that
// site's own schema, through its scoped store.

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"sap13ns/internal/content"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// switcherMaxSites is how many sites the switcher lists; "My sites" on
// the portal lists them all.
const switcherMaxSites = 50

var _ web.SiteSwitcher = hooks{}

// SwitcherEpoch changes with every change this process makes to the
// network tables, and with every reload of the host table, which follows
// the changes other processes make (the CLI).
func (h hooks) SwitcherEpoch() uint64 {
	return h.r.net.changes.Load() + uint64(h.r.refreshes.Load())
}

// SwitcherSites lists the sites an account opens as a member: pending or
// active, never suspended or deleted ones, and never those it would open
// only as an operator, with support or emergency access.
func (h hooks) SwitcherSites(ctx context.Context, accountID int64) (*web.AccountSites, error) {
	member, err := h.r.net.MemberSites(ctx, accountID)
	if err != nil {
		return nil, err
	}
	out := &web.AccountSites{PortalURL: h.r.origin(h.r.cfg.BaseURL.Hostname())}
	personal := false
	for i := range member {
		s := &member[i].Site
		// One Personal site per account, as "My sites" counts it.
		personal = personal || s.OwnerID == accountID && s.Plan == PlanPersonal
		if s.Status != StatusActive && s.Status != StatusPending {
			continue
		}
		if len(out.Sites) == switcherMaxSites {
			out.More++
			continue
		}
		title, err := h.r.net.SiteTitle(ctx, s)
		if err != nil {
			return nil, err
		}
		out.Sites = append(out.Sites, web.SwitcherSite{ID: s.ID, Slug: s.Slug, Title: title, Host: s.PrimaryHost,
			URL: h.r.origin(s.PrimaryHost), Role: member[i].Role, Status: s.Status, Current: s.ID == h.id})
	}
	slices.SortStableFunc(out.Sites, func(a, b web.SwitcherSite) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name())), strings.Compare(a.Slug, b.Slug))
	})
	if out.NetworkAdmin, err = h.r.net.IsNetworkAdmin(ctx, accountID); err != nil {
		return nil, err
	}
	set, err := store.New(h.r.net.DB()).LoadSettings(ctx)
	if err != nil {
		return nil, err
	}
	out.CanRequest = set.AllowRegistration && !personal
	return out, nil
}

// SiteTitle is the title a site's administrators gave it, read from the
// site's own schema (at most 80 characters of it).
func (n *Network) SiteTitle(ctx context.Context, s *Site) (string, error) {
	st, err := n.siteStore(s)
	if err != nil {
		return "", err
	}
	set, err := st.LoadSettings(ctx)
	if err != nil {
		return "", err
	}
	return content.Truncate(strings.TrimSpace(set.SiteTitle), 80), nil
}
