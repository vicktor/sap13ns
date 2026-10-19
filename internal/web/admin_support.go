package web

// Support access (docs/MULTISITE.md): a network admin who is not a member
// of a site opens it, as an operator, only while the site's owner or one
// of its administrators allows it. It is granted and revoked on the
// portal, from "My sites" (/admin/sites/{id}/support): the people who may
// grant it are network accounts signed in there with a second factor, and
// the page works whatever state the site itself is in, which is when
// support is needed. The site's Users page shows the access and links
// there. On the site's page of the network admin area, a network admin
// sees it, asks the owner for it by e-mail, and ends it when done.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"sap13ns/internal/store"
)

// PortalSupport manages the support access of tenant sites.
type PortalSupport interface {
	// GrantSupport opens support access to a site for d (24 hours, 7 days,
	// or 0 until revoked), replacing an open one, by the site's owner or
	// one of its administrators: ErrSupportDenied for anyone else,
	// ErrNetworkState on a site that is not served (suspended, deleted),
	// ErrNetworkInvalid for another duration. The site's audit log
	// records it.
	GrantSupport(ctx context.Context, siteID, by int64, d time.Duration, ip netip.Addr) (*NetSupportGrant, error)
	// RevokeSupport closes it, by the owner, an administrator or a network
	// admin, and reports whether it was open. Either way the site's
	// operators are signed out at once.
	RevokeSupport(ctx context.Context, siteID, by int64, ip netip.Addr) (bool, error)

	// StartEmergency opens an emergency access to a site for the network
	// admin by, without the owner's consent, for at most four hours:
	// category is legal or security, and the justification 1 to 500
	// characters (ErrNetworkInvalid otherwise). ErrSupportDenied for an
	// account that is not a network admin, ErrEmergencyOpen when it
	// already has one there, ErrNetworkState on a deleted site. It works on
	// a suspended site. The site's audit log records it.
	StartEmergency(ctx context.Context, siteID, by int64, category, justification string, ip netip.Addr) (*NetEmergency, error)
	// EndEmergency ends every live emergency access to a site, by a
	// network admin, and returns how many it ended; their sessions end at
	// once.
	EndEmergency(ctx context.Context, siteID, by int64, ip netip.Addr) (int, error)
}

var (
	// ErrSupportDenied means the account may not change the support access
	// (or the emergency access) of the site.
	ErrSupportDenied = errors.New("network: the account may not change the support access of the site")
	// ErrEmergencyOpen means the account already has a live emergency
	// access to the site.
	ErrEmergencyOpen = errors.New("network: an emergency access to the site is already open")
)

// NetSupport is the support access of a site: the grant open now, if any,
// and the last ones; and its last emergency accesses.
type NetSupport struct {
	Open        *NetSupportGrant
	History     []NetSupportGrant
	Emergencies []NetEmergency
}

// LiveEmergency returns the live emergency access of an account, nil if
// it has none.
func (s NetSupport) LiveEmergency(accountID int64) *NetEmergency {
	for i := range s.Emergencies {
		if e := &s.Emergencies[i]; e.State == "open" && e.AccountID == accountID {
			return e
		}
	}
	return nil
}

// LiveEmergencies are the emergency accesses open now.
func (s NetSupport) LiveEmergencies() []NetEmergency {
	var out []NetEmergency
	for _, e := range s.Emergencies {
		if e.State == "open" {
			out = append(out, e)
		}
	}
	return out
}

// NetEmergency is an emergency access to a site, live or past.
type NetEmergency struct {
	AccountID     int64  // the network admin who opened it; 0 once the account is gone
	By            string // their username
	Category      string // legal or security
	Justification string
	StartedAt     time.Time
	ExpiresAt     time.Time
	EndedAt       *time.Time // ended before it expired
	EndedBy       string
	State         string // open, expired or ended
}

type NetSupportGrant struct {
	GrantedBy string // a username; "" once the account is gone
	GrantedAt time.Time
	ExpiresAt *time.Time // nil: until revoked
	RevokedAt *time.Time // when it was revoked or replaced by a new grant
	RevokedBy string
	State     string // open, expired or revoked
}

// The durations a grant can have, by their form value.
var supportDurations = map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "revoked": 0}

func mySupportPath(siteID int64) string { return fmt.Sprintf("/admin/sites/%d/support", siteID) }

// supportSite loads a site whose support access the current account
// manages: 404 for any other site, and for a deleted one.
func (s *Server) supportSite(w http.ResponseWriter, r *http.Request) (*NetSiteDetail, bool) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return nil, false
	}
	if d.Site.Status == "deleted" || !d.SupportManager(uid(r)) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return d, true
}

func (s *Server) adminMySupport(w http.ResponseWriter, r *http.Request) {
	d, ok := s.supportSite(w, r)
	if !ok {
		return
	}
	s.renderAdmin(w, r, "site_support", s.t("Support access to %s", d.Site.Slug), "my-sites", d)
}

// supportUntil tells until when a grant is open.
func (s *Server) supportUntil(expires *time.Time) string {
	if expires == nil {
		return s.t("until you revoke it")
	}
	return s.t("until %s", s.dateTime(*expires))
}

func (s *Server) adminMySupportGrant(w http.ResponseWriter, r *http.Request) {
	d, ok := s.supportSite(w, r)
	if !ok {
		return
	}
	back := mySupportPath(d.Site.ID)
	dur, ok := supportDurations[r.FormValue("duration")]
	if !ok {
		s.redirect(w, r, back, "error", s.t("Choose how long support access stays open."))
		return
	}
	g, err := s.portal.GrantSupport(r.Context(), d.Site.ID, uid(r), dur, clientIP(r))
	switch {
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.supportStateError(d.Site.Status))
		return
	case errors.Is(err, ErrSupportDenied), errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	until := "revoked"
	if g.ExpiresAt != nil {
		until = g.ExpiresAt.UTC().Format(time.RFC3339)
	}
	s.audit(r, uid(r), "network.support_granted", d.Site.Slug, map[string]string{"site": strconv.FormatInt(d.Site.ID, 10), "until": until})
	s.redirect(w, r, back, "success", s.t("Support access to %s is open %s.", d.Site.Slug, s.supportUntil(g.ExpiresAt)))
}

func (s *Server) adminMySupportRevoke(w http.ResponseWriter, r *http.Request) {
	d, ok := s.supportSite(w, r)
	if !ok {
		return
	}
	s.revokeSupport(w, r, d, mySupportPath(d.Site.ID))
}

// revokeSupport closes the support access of a site and goes back.
func (s *Server) revokeSupport(w http.ResponseWriter, r *http.Request, d *NetSiteDetail, back string) {
	revoked, err := s.portal.RevokeSupport(r.Context(), d.Site.ID, uid(r), clientIP(r))
	switch {
	case errors.Is(err, ErrSupportDenied), errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if !revoked {
		s.redirect(w, r, back, "info", s.t("Support access to %s was not open.", d.Site.Slug))
		return
	}
	s.audit(r, uid(r), "network.support_revoked", d.Site.Slug, map[string]string{"site": strconv.FormatInt(d.Site.ID, 10)})
	s.redirect(w, r, back, "success", s.t("Support access to %s is closed. Network administrators signed in to it were signed out.", d.Site.Slug))
}

// supportStateError tells why support access cannot be opened on a site
// in its status.
func (s *Server) supportStateError(status string) string {
	if status == "suspended" {
		return s.t("Support access cannot be opened while the site is suspended.")
	}
	return s.siteStateError(status)
}

// ------------------------------------------------- network admin area --

func (s *Server) adminNetSupportRequest(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	site := d.Site
	back := fmt.Sprintf("/admin/network/sites/%d#support", site.ID)
	switch {
	case !site.Served():
		s.redirect(w, r, back, "error", s.supportStateError(site.Status))
		return
	case d.Support.Open != nil:
		s.redirect(w, r, back, "info", s.t("Support access to %s is already open.", site.Slug))
		return
	}
	msg := trimmed(r, "message")
	if len(msg) > 500 {
		s.redirect(w, r, back, "error", s.t("The message is too long (500 characters max)."))
		return
	}
	u := currentUser(r)
	body := s.t("%s, an administrator of %s, asks for support access to your site %s.", cmp.Or(u.DisplayName, u.Username), s.settings().SiteTitle, site.Host)
	if msg != "" {
		body += "\n\n" + s.t("Their message:") + "\n" + msg
	}
	body += "\n\n" + s.t("Support access lets the network's administrators open your site's administration, for as long as you allow it. Every sign-in is written to your site's audit log.") +
		"\n\n" + s.t("To allow it for 24 hours, 7 days or until you revoke it:") + "\n" + s.cfg.AbsURL(mySupportPath(site.ID)) +
		"\n\n" + s.t("If you do not allow it, nothing changes.")
	details := map[string]string{"site": strconv.FormatInt(site.ID, 10)}
	if msg != "" {
		details["message"] = msg
	}
	s.audit(r, u.ID, "network.support_requested", site.Slug, details)
	if !s.mailOwner(r.Context(), &site, s.t("Support access to %s requested", site.Slug), body) {
		s.redirect(w, r, back, "warning", s.t("The owner of %s could not be asked by e-mail.", site.Slug))
		return
	}
	s.redirect(w, r, back, "success", s.t("The owner of %s was asked by e-mail.", site.Slug))
}

func (s *Server) adminNetSupportEnd(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	s.revokeSupport(w, r, d, fmt.Sprintf("/admin/network/sites/%d#support", d.Site.ID))
}

// ---------------------------------------------------------- tenant site --

// supportStatus is the support access of a tenant site for its Users
// page, nil elsewhere or when it cannot be read.
func (s *Server) supportStatus(r *http.Request) *SupportStatus {
	if s.sso == nil {
		return nil
	}
	st, err := s.sso.SupportStatus(r.Context())
	if err != nil {
		s.log.Error("support access", "err", err)
		return nil
	}
	return st
}
