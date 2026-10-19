package web

// The custom domains of the network's sites, on the portal: the owner of a
// site manages them from "My sites" (/admin/sites/{id}/domains), a network
// admin from the site's page (/admin/network/sites/{id}). Both go through
// PortalDomains, which enforces the plan's limit and runs the DNS checks;
// every action is audited, and the network notifies the routers.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"sap13ns/internal/store"
)

// PortalDomains manages the custom domains of tenant sites. A host must be
// one of the site's: store.ErrNotFound otherwise.
type PortalDomains interface {
	// DomainTarget is what custom domains point to.
	DomainTarget() NetDomainTarget
	// AddDomain adds a custom domain, not routed until verified:
	// ErrNetworkInvalid for a host that is not a domain name or belongs to
	// the network, ErrNetworkLimit beyond the plan's custom domains,
	// store.ErrConflict for one in use, ErrDomainClaimed (and the record
	// that would take it over) for one another site added and has not
	// verified, ErrNetworkState for a deleted site.
	AddDomain(ctx context.Context, siteID int64, host string) (*NetDomain, error)
	// CheckDomain runs the DNS check of a custom domain, and verifies it
	// (the network routes it) when it passes. The outcome is CheckError.
	CheckDomain(ctx context.Context, siteID int64, host string) (*NetDomain, error)
	// SetPrimaryDomain serves the site on a verified host (ErrNetworkState
	// for another one).
	SetPrimaryDomain(ctx context.Context, siteID int64, host string) error
	// RemoveDomain removes a custom domain (ErrNetworkState for the
	// default and the primary ones).
	RemoveDomain(ctx context.Context, siteID int64, host string) error
}

// NetDomainTarget is what a custom domain points to: a CNAME to Host, or
// A/AAAA records with Addresses (with those of Host when empty).
type NetDomainTarget struct {
	Host      string
	Addresses []string
}

var (
	// ErrNetworkLimit is an addition beyond the limits of the site's plan.
	ErrNetworkLimit = errors.New("network: over the plan's limit")
	// ErrDomainClaimed is a domain another site added and has not verified.
	ErrDomainClaimed = errors.New("network: the domain is claimed by another site")
)

// Outcomes of a DNS check (NetDomain.CheckError).
const (
	checkTXT     = "txt_missing"
	checkAddress = "not_pointing"
	checkDNS     = "dns_error"
)

// domainsView is what the "site-domains" partial shows.
type domainsView struct {
	SiteID   int64
	Base     string // where its forms post
	Domains  []NetDomain
	Target   NetDomainTarget
	Editable bool // the site is not deleted
	Custom   int  // custom domains it has
	Limit    int  // that its plan allows
	Host     string
	Error    string
	Claim    *NetDomain // the record that takes over a claimed host
}

// Room is how many more custom domains the plan allows.
func (v domainsView) Room() int { return v.Limit - v.Custom }

func netDomainsBase(siteID int64) string {
	return fmt.Sprintf("/admin/network/sites/%d/domains", siteID)
}
func myDomainsBase(siteID int64) string { return fmt.Sprintf("/admin/sites/%d/domains", siteID) }

func (s *Server) domainsView(d *NetSiteDetail, base string) domainsView {
	v := domainsView{SiteID: d.Site.ID, Base: base, Domains: d.Domains, Target: s.portal.DomainTarget(),
		Editable: d.Site.Status != "deleted" && d.Site.Status != "provisioning"}
	for _, x := range d.Domains {
		if x.Custom() {
			v.Custom++
		}
	}
	if d.Plan != nil {
		v.Limit = d.Plan.CustomDomains
	}
	return v
}

// ownSite loads a site of the current account for "My sites": 404 for any
// other site, and for a deleted one.
func (s *Server) ownSite(w http.ResponseWriter, r *http.Request) (*NetSiteDetail, bool) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return nil, false
	}
	if d.Site.OwnerID != uid(r) || d.Site.Status == "deleted" {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return d, true
}

type siteDomainsData struct {
	Site    NetSite
	Plan    *NetPlan
	Domains domainsView
}

func (s *Server) adminMyDomains(w http.ResponseWriter, r *http.Request) {
	d, ok := s.ownSite(w, r)
	if !ok {
		return
	}
	s.renderMyDomains(w, r, http.StatusOK, d, "", "", nil)
}

func (s *Server) renderMyDomains(w http.ResponseWriter, r *http.Request, status int, d *NetSiteDetail, host, msg string, claim *NetDomain) {
	v := s.domainsView(d, myDomainsBase(d.Site.ID))
	v.Host, v.Error, v.Claim = host, msg, claim
	s.renderAdminStatus(w, r, status, "site_domains", s.t("Domains of %s", d.Site.Slug), "my-sites",
		siteDomainsData{Site: d.Site, Plan: d.Plan, Domains: v})
}

// renderNetSite renders a site's page in the network admin area, with the
// outcome of a failed domain addition.
func (s *Server) renderNetSite(w http.ResponseWriter, r *http.Request, status int, d *NetSiteDetail, host, msg string, claim *NetDomain) {
	v := s.domainsView(d, netDomainsBase(d.Site.ID))
	v.Host, v.Error, v.Claim = host, msg, claim
	s.renderNetSiteData(w, r, status, d, v, nil)
}

// renderNetSiteData renders the site's page of the network admin area,
// with its domains card and, when ef is set, the emergency access form as
// it was sent.
func (s *Server) renderNetSiteData(w http.ResponseWriter, r *http.Request, status int, d *NetSiteDetail, v domainsView, ef *emergencyForm) {
	u := currentUser(r)
	mine := d.Support.LiveEmergency(u.ID)
	if ef == nil {
		ef = &emergencyForm{}
	}
	ef.Reauth, ef.TOTP = recentLogin(r), u.TOTPEnabled
	s.renderAdminStatus(w, r, status, "network_site", d.Site.Slug, "net-sites", netSiteData{NetSiteDetail: d, Roles: assignableRoles, DomainCard: v,
		CanOpen: d.Site.Served() && (d.Support.Open != nil || d.Member(u.ID)) ||
			mine != nil && (d.Site.Served() || d.Site.Status == "suspended"),
		MyEmergency: mine, EmergencyForm: ef})
}

func (s *Server) adminMyDomainAdd(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.ownSite(w, r); ok {
		s.domainAdd(w, r, d, myDomainsBase(d.Site.ID))
	}
}

func (s *Server) adminMyDomainAction(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.ownSite(w, r); ok {
		s.domainAction(w, r, d, myDomainsBase(d.Site.ID))
	}
}

func (s *Server) adminNetDomainAdd(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadNetSite(w, r); ok {
		s.domainAdd(w, r, d, netDomainsBase(d.Site.ID))
	}
}

func (s *Server) adminNetDomainAction(w http.ResponseWriter, r *http.Request) {
	if d, ok := s.loadNetSite(w, r); ok {
		s.domainAction(w, r, d, netDomainsBase(d.Site.ID))
	}
}

// domainBack is the page a domain form returns to.
func domainBack(base string) string {
	if strings.HasPrefix(base, "/admin/network/") {
		return strings.TrimSuffix(base, "/domains") + "#domains"
	}
	return base
}

func (s *Server) domainDetails(d *NetSiteDetail, host string) map[string]string {
	return map[string]string{"site": strconv.FormatInt(d.Site.ID, 10), "slug": d.Site.Slug, "host": host}
}

func (s *Server) domainAdd(w http.ResponseWriter, r *http.Request, d *NetSiteDetail, base string) {
	back := domainBack(base)
	host := strings.ToLower(strings.TrimSpace(r.FormValue("host")))
	fail := func(status int, msg string, claim *NetDomain) {
		if strings.HasPrefix(base, "/admin/network/") {
			s.renderNetSite(w, r, status, d, truncateRunes(host, 253), msg, claim)
			return
		}
		s.renderMyDomains(w, r, status, d, truncateRunes(host, 253), msg, claim)
	}
	if !s.domainChecks.Allow(strconv.FormatInt(uid(r), 10)) {
		fail(http.StatusTooManyRequests, s.t("Too many attempts. Try again later."), nil)
		return
	}
	nd, err := s.portal.AddDomain(r.Context(), d.Site.ID, host)
	switch {
	case errors.Is(err, ErrNetworkInvalid):
		fail(http.StatusUnprocessableEntity, s.t("Enter a domain name you control, such as www.example.com. The network's own hosts cannot be added."), nil)
		return
	case errors.Is(err, ErrNetworkLimit):
		fail(http.StatusForbidden, s.t("The plan of this site allows no more custom domains."), nil)
		return
	case errors.Is(err, ErrDomainClaimed):
		fail(http.StatusConflict, s.t("Another site added this domain and has not verified it. If it is yours, create the TXT record below and add it again: proving that you control it moves it to this site."), nd)
		return
	case errors.Is(err, store.ErrConflict):
		fail(http.StatusConflict, s.t("A site of the network already uses this domain."), nil)
		return
	case errors.Is(err, ErrNetworkState):
		fail(http.StatusConflict, s.siteStateError(d.Site.Status), nil)
		return
	case errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	details := s.domainDetails(d, nd.Host)
	msg := s.t("%s was added. Create its DNS records, then verify it.", nd.Host)
	if nd.TakenFrom {
		details["taken_over"] = "true"
		msg = s.t("%s was added: its TXT record proves that you control it, so it moved from the site that had claimed it. Verify it to have it served.", nd.Host)
	}
	s.audit(r, uid(r), "network.domain_added", nd.Host, details)
	s.redirect(w, r, back, "success", msg)
}

func (s *Server) domainAction(w http.ResponseWriter, r *http.Request, d *NetSiteDetail, base string) {
	back := domainBack(base)
	host := strings.ToLower(strings.TrimSpace(r.FormValue("host")))
	known := false
	for _, x := range d.Domains {
		known = known || x.Host == host
	}
	if !known {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	ctx, details := r.Context(), s.domainDetails(d, host)
	var err error
	switch r.PathValue("op") {
	case "check":
		if !s.domainChecks.Allow(strconv.FormatInt(uid(r), 10)) {
			s.redirect(w, r, back, "error", s.t("Too many attempts. Try again later."))
			return
		}
		var nd *NetDomain
		if nd, err = s.portal.CheckDomain(ctx, d.Site.ID, host); err == nil {
			s.checkOutcome(w, r, d, back, nd)
			return
		}
	case "primary":
		if err = s.portal.SetPrimaryDomain(ctx, d.Site.ID, host); errors.Is(err, ErrNetworkState) {
			s.redirect(w, r, back, "error", s.t("Only a verified domain can be the primary one."))
			return
		}
		if err == nil {
			s.audit(r, uid(r), "network.domain_primary", host, details)
			s.redirect(w, r, back, "success", s.t("%s is now the primary domain of %s.", host, d.Site.Slug))
			return
		}
	case "remove":
		if err = s.portal.RemoveDomain(ctx, d.Site.ID, host); errors.Is(err, ErrNetworkState) {
			s.redirect(w, r, back, "error", s.t("The default domain and the primary domain cannot be removed."))
			return
		}
		if err == nil {
			s.audit(r, uid(r), "network.domain_removed", host, details)
			s.redirect(w, r, back, "success", s.t("%s was removed from %s.", host, d.Site.Slug))
			return
		}
	default:
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		s.redirect(w, r, back, "error", s.t("This domain changed meanwhile. Check the list again."))
		return
	}
	s.serverError(w, r, err)
}

// checkOutcome audits a DNS check and says how it went.
func (s *Server) checkOutcome(w http.ResponseWriter, r *http.Request, d *NetSiteDetail, back string, nd *NetDomain) {
	details := s.domainDetails(d, nd.Host)
	if nd.CheckError != "" {
		details["problem"] = nd.CheckError
		s.audit(r, uid(r), "network.domain_check_failed", nd.Host, details)
		s.redirect(w, r, back, "error", s.checkMessage(nd))
		return
	}
	was := false
	for _, x := range d.Domains {
		was = was || x.Host == nd.Host && x.VerifiedAt != nil
	}
	if !was {
		s.audit(r, uid(r), "network.domain_verified", nd.Host, details)
		s.redirect(w, r, back, "success", s.t("%s is verified: the network serves it now.", nd.Host))
		return
	}
	s.redirect(w, r, back, "success", s.t("%s still points to the network.", nd.Host))
}

func (s *Server) checkMessage(d *NetDomain) string {
	switch d.CheckError {
	case checkTXT:
		return s.t("%s is not verified yet: the TXT record %s was not found, or has another value. DNS changes can take a while to spread.", d.Host, d.TXTName)
	case checkAddress:
		return s.t("%s is not verified yet: it does not point to the network.", d.Host)
	}
	return s.t("The DNS lookup of %s failed. Try again in a few minutes.", d.Host)
}

// DomainNotice tells the owner of a site that one of its custom domains
// lapsed.
type DomainNotice struct {
	AccountID int64
	SiteID    int64
	Slug      string
	Host      string
	Problem   string // checkTXT or checkAddress
	Fallback  string // the default host, when the lapsed one was the primary
}

// NotifyDomain e-mails the owner of a site, in the portal's language, that
// a custom domain lapsed, with a link to its domains.
func (s *Server) NotifyDomain(ctx context.Context, n DomainNotice) error {
	u, err := s.store.UserByID(ctx, n.AccountID)
	if err != nil {
		return err
	}
	title := s.settings().SiteTitle
	body := s.t("The domain %s of your site %s kept failing its daily DNS check, so the network no longer serves it.", n.Host, n.Slug)
	switch n.Problem {
	case checkTXT:
		body += " " + s.t("Its TXT record %s is gone or has another value.", VerifyTXTName(n.Host))
	case checkAddress:
		body += " " + s.t("It no longer points to the network.")
	}
	if n.Fallback != "" {
		body += "\n\n" + s.t("Your site is served on %s again.", n.Fallback)
	}
	body += "\n\n" + s.t("Check its DNS records and verify it again from your sites:") + "\n" + s.adminLink(myDomainsBase(n.SiteID))
	return s.mailer.Send(u.Email, s.t("Your domain %s is no longer served", n.Host)+" · "+title, body)
}

// VerifyTXTName is the name of the TXT record that verifies a custom
// domain (network.TXTName).
func VerifyTXTName(host string) string { return "_sap13ns-verify." + host }
