package network

// The hooks of support and emergency access (support.go): the check of an
// operator session on every request of a site, cached for a few seconds,
// and the portal's pages that grant, revoke, start and end them.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"time"

	"sap13ns/internal/web"
)

// operatorCacheTTL bounds how long an operator session's check is cached:
// a revocation made by another process (whose sign-out reaches the site's
// sessions in the database at once anyway) is seen at most this late.
// Changes made through this registry forget the site's entries, and no
// entry outlives the grant or the emergency access it was read from.
const operatorCacheTTL = 30 * time.Second

// accessKey is a cached answer: the support access of a site (account 0)
// or an account's emergency access to it.
type accessKey struct{ site, account int64 }

// accessCache holds the answers that let an operator session in, each
// with when it stops being reused. A refusal is never cached.
type accessCache struct {
	mu sync.Mutex
	m  map[accessKey]time.Time
}

func (c *accessCache) allowed(k accessKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.m[k]
	return ok && now.Before(until)
}

func (c *accessCache) put(k accessKey, until, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[accessKey]time.Time{}
	}
	if len(c.m) >= 4096 {
		for k, u := range c.m {
			if !now.Before(u) {
				delete(c.m, k)
			}
		}
	}
	c.m[k] = until
}

// forget drops the answers about a site.
func (c *accessCache) forget(site int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.site == site {
			delete(c.m, k)
		}
	}
}

func (h siteHooks) OperatorAllowed(ctx context.Context, accountID int64, emergency bool) (bool, error) {
	now := h.r.o.Now()
	k := accessKey{site: h.id}
	if emergency {
		k.account = accountID
	}
	if h.r.access.allowed(k, now) {
		return true, nil
	}
	until := now.Add(operatorCacheTTL)
	if emergency {
		e, err := h.r.net.EmergencyAccess(ctx, h.id, accountID)
		if err != nil || e == nil {
			return false, err
		}
		if e.ExpiresAt.Before(until) {
			until = e.ExpiresAt
		}
	} else {
		g, err := h.r.net.SupportAccess(ctx, h.id)
		if err != nil || g == nil {
			return false, err
		}
		if g.ExpiresAt != nil && g.ExpiresAt.Before(until) {
			until = *g.ExpiresAt
		}
	}
	h.r.access.put(k, until, now)
	return true, nil
}

func (h siteHooks) SupportStatus(ctx context.Context) (*web.SupportStatus, error) {
	out := &web.SupportStatus{ManageURL: h.r.cfg.AbsURL(fmt.Sprintf("/admin/sites/%d/support", h.id))}
	g, err := h.r.net.SupportAccess(ctx, h.id)
	if err != nil {
		return nil, err
	}
	if g != nil {
		out.Open, out.ExpiresAt, out.GrantedBy, out.GrantedAt = true, g.ExpiresAt, g.GrantedByName, g.GrantedAt
	}
	list, err := h.r.net.Emergencies(ctx, h.id, 20)
	if err != nil {
		return nil, err
	}
	now := h.r.o.Now()
	for _, e := range list {
		if e.Live(now) {
			out.Emergencies = append(out.Emergencies, web.EmergencyStatus{By: e.AccountName, Category: e.Category,
				Justification: e.Justification, StartedAt: e.StartedAt, ExpiresAt: e.ExpiresAt})
		}
	}
	return out, nil
}

// supportErr maps the errors of support and emergency access to the
// web's.
func supportErr(err error) error {
	switch {
	case errors.Is(err, ErrSupportDenied):
		return errors.Join(web.ErrSupportDenied, err)
	case errors.Is(err, ErrEmergencyOpen):
		return errors.Join(web.ErrEmergencyOpen, err)
	}
	return webErr(err)
}

var _ web.PortalSupport = portalHooks{}

func (h portalHooks) GrantSupport(ctx context.Context, siteID, by int64, d time.Duration, ip netip.Addr) (*web.NetSupportGrant, error) {
	g, err := h.r.net.GrantSupport(ctx, siteID, by, d, ip)
	h.r.access.forget(siteID)
	if err != nil {
		return nil, supportErr(err)
	}
	out := netSupportGrant(g, h.r.o.Now())
	return &out, nil
}

func (h portalHooks) RevokeSupport(ctx context.Context, siteID, by int64, ip netip.Addr) (bool, error) {
	ok, err := h.r.net.RevokeSupport(ctx, siteID, by, ip)
	h.r.access.forget(siteID)
	return ok, supportErr(err)
}

func (h portalHooks) StartEmergency(ctx context.Context, siteID, by int64, category, justification string, ip netip.Addr) (*web.NetEmergency, error) {
	e, err := h.r.net.StartEmergency(ctx, siteID, by, category, justification, ip)
	h.r.access.forget(siteID)
	if err != nil {
		return nil, supportErr(err)
	}
	// Serve a suspended site to the access now, not at the notification.
	if err := h.r.Refresh(ctx); err != nil {
		h.r.log.Error("network host table", "err", err)
	}
	out := netEmergency(e, h.r.o.Now())
	return &out, nil
}

func (h portalHooks) EndEmergency(ctx context.Context, siteID, by int64, ip netip.Addr) (int, error) {
	n, err := h.r.net.EndEmergency(ctx, siteID, by, ip)
	h.r.access.forget(siteID)
	if err == nil && n > 0 {
		if err := h.r.Refresh(ctx); err != nil {
			h.r.log.Error("network host table", "err", err)
		}
	}
	return n, supportErr(err)
}

// support fills in the support and emergency access of a site's detail.
func (h portalHooks) support(ctx context.Context, d *web.NetSiteDetail) error {
	now := h.r.o.Now()
	grants, err := h.r.net.SupportGrants(ctx, d.Site.ID, 10)
	if err != nil {
		return err
	}
	for i := range grants {
		g := netSupportGrant(&grants[i], now)
		if g.State == "open" && d.Support.Open == nil {
			d.Support.Open = &g
		}
		d.Support.History = append(d.Support.History, g)
	}
	list, err := h.r.net.Emergencies(ctx, d.Site.ID, 10)
	if err != nil {
		return err
	}
	for i := range list {
		d.Support.Emergencies = append(d.Support.Emergencies, netEmergency(&list[i], now))
	}
	return nil
}

func netSupportGrant(g *SupportGrant, now time.Time) web.NetSupportGrant {
	out := web.NetSupportGrant{GrantedBy: g.GrantedByName, GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt,
		RevokedAt: g.RevokedAt, RevokedBy: g.RevokedByName, State: "expired"}
	switch {
	case g.Live(now):
		out.State = "open"
	case g.RevokedAt != nil:
		out.State = "revoked"
	}
	return out
}

func netEmergency(e *EmergencyAccess, now time.Time) web.NetEmergency {
	out := web.NetEmergency{By: e.AccountName, Category: e.Category, Justification: e.Justification,
		StartedAt: e.StartedAt, ExpiresAt: e.ExpiresAt, EndedAt: e.EndedAt, EndedBy: e.EndedByName, State: "expired"}
	if e.AccountID != nil {
		out.AccountID = *e.AccountID
	}
	switch {
	case e.Live(now):
		out.State = "open"
	case e.EndedAt != nil:
		out.State = "ended"
	}
	return out
}
