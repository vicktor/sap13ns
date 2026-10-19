package web

// The portal's pages about the network (multisite): the network admin
// area under /admin/network, for network admins with a second factor, and
// "My sites" under /admin/sites, for every account. Only the portal has
// these routes; the network does the work through PortalAdmin.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// PortalAdmin manages the sites of the network for the portal.
type PortalAdmin interface {
	// Sites lists the tenant sites, oldest first, and their total.
	Sites(ctx context.Context, q NetSiteQuery) ([]NetSite, int, error)
	// SiteDetail describes a tenant site: ErrNotFound for any other id.
	SiteDetail(ctx context.Context, siteID int64) (*NetSiteDetail, error)
	// The status changes of a site return ErrNetworkState when its status
	// does not allow them (approving an active site, for one).
	ApproveSite(ctx context.Context, siteID, by int64) error
	RejectSite(ctx context.Context, siteID int64) error
	SuspendSite(ctx context.Context, siteID int64, reason string) error
	ResumeSite(ctx context.Context, siteID int64) error
	DeleteSite(ctx context.Context, siteID int64) error
	// SetMember adds an account to a site or changes its role there, and
	// RemoveMember removes it; the account's user on the site follows at
	// once. The owner stays an admin (ErrNetworkState).
	SetMember(ctx context.Context, siteID, accountID int64, role string, by int64) error
	RemoveMember(ctx context.Context, siteID, accountID int64) error
	NetworkAdmins(ctx context.Context) ([]NetAdmin, error)
	AddNetworkAdmin(ctx context.Context, accountID, by int64) error
	// RemoveNetworkAdmin never removes the last one (ErrLastNetworkAdmin).
	RemoveNetworkAdmin(ctx context.Context, accountID int64) error
	Plans(ctx context.Context) ([]NetPlan, error)
	// AccountSites lists the served sites an account is a member of, with
	// its role on each.
	AccountSites(ctx context.Context, accountID int64) ([]NetSite, error)
	// RequestSite creates the Personal site an account asks for, pending
	// approval: ErrNetworkInvalid for a name that cannot be used,
	// ErrConflict for a taken one, ErrNetworkState when the account has
	// one already.
	RequestSite(ctx context.Context, accountID int64, slug string) (*NetSite, error)

	// The custom domains of tenant sites (admin_domains.go).
	PortalDomains
}

// A NetSite is a tenant site of the network.
type NetSite struct {
	ID               int64
	Slug             string
	Status           string // pending, active, suspended, deleted (provisioning while created)
	Plan             string
	Host             string // the primary host
	URL              string // its origin
	OwnerID          int64
	Owner            string // the owner's username
	StorageBytes     int64
	StorageCheckedAt *time.Time
	SuspendedReason  string
	SuspendedAt      *time.Time
	ApprovedAt       *time.Time
	ApprovedBy       string // a username; "" when approved by nobody (or not at all)
	CreatedAt        time.Time
	DeletedAt        *time.Time
	Role             string      // the account's role, from AccountSites
	PlanName         string      // from SiteDetail and AccountSites
	PlanPaid         bool        // likewise
	Billing          *NetBilling // likewise; nil without a subscription
}

// AdminURL opens the site's administration through single sign-on.
func (s NetSite) AdminURL() string { return s.URL + "/sso/start?next=" + url.QueryEscape("/admin/") }

// Served reports whether the site can be opened (signed in to).
func (s NetSite) Served() bool { return s.Status == "active" || s.Status == "pending" }

type NetSiteQuery struct {
	Status string // "" every site that is not deleted
	Search string
	Limit  int
	Offset int
}

type NetDomain struct {
	Host          string
	Kind          string // default or custom
	Primary       bool
	VerifiedAt    *time.Time // nil: not routed
	CheckedAt     *time.Time
	CheckError    string // the outcome of the last DNS check: "", txt_missing, not_pointing or dns_error
	CheckFailures int    // failed daily checks in a row of a verified domain
	LapsedAt      *time.Time
	TXTName       string // the verification record of a custom domain
	TXTValue      string
	TakenFrom     bool // AddDomain moved it from a site that had not verified it
}

func (d NetDomain) Custom() bool { return d.Kind == "custom" }

// Pending reports a custom domain whose DNS records need the owner's
// attention: not verified yet, or failing its daily checks.
func (d NetDomain) Pending() bool { return d.Custom() && (d.VerifiedAt == nil || d.CheckFailures > 0) }

type NetMember struct {
	AccountID int64
	Username  string
	Email     string
	Role      string
	Owner     bool
	LastSSOAt *time.Time
	CreatedAt time.Time
}

type NetPlan struct {
	Slug          string
	Name          string
	Paid          bool
	HandApproved  bool
	Commercial    bool
	SitesPerGrant int
	StorageBytes  int64
	CustomDomains int
	PluginUploads bool
	ThemeUploads  bool
}

type NetAdmin struct {
	AccountID int64
	Username  string
	Email     string
	GrantedBy string // a username; "" from the CLI
	CreatedAt time.Time
}

type NetSiteDetail struct {
	Site    NetSite
	Plan    *NetPlan // nil if the plan is gone
	Domains []NetDomain
	Members []NetMember
}

// Errors of PortalAdmin.
var (
	ErrNetworkInvalid = errors.New("network: invalid request")
	ErrNetworkState   = errors.New("network: not allowed in the current state")
)

var siteStatuses = []string{"pending", "active", "suspended", "deleted"}

// networkAdmin lets only network admins with a second factor through:
// they open every site of the network.
func (s *Server) networkAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		ok, err := s.portal.IsNetworkAdmin(r.Context(), u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if !ok {
			s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
			return
		}
		if !hasSecondFactor(u) && !(s.cfg.Dev && s.cfg.DevSkip2FA) {
			s.flash(w, "warning", s.t("Set up two-factor authentication to manage the network."))
			http.Redirect(w, r, "/admin/profile/2fa", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// --------------------------------------------------------------- sites --

type netSitesData struct {
	Sites    []NetSite
	Total    int
	Status   string
	Query    string
	Statuses []string
	Page     *Pagination
}

func (s *Server) adminNetSites(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := q.Get("status")
	if !slices.Contains(siteStatuses, status) {
		status = ""
	}
	search := truncateRunes(strings.TrimSpace(q.Get("q")), 100)
	page := listPage(r)
	const per = 30
	sites, total, err := s.portal.Sites(r.Context(), NetSiteQuery{Status: status, Search: search, Limit: per, Offset: (page - 1) * per})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q.Del("page")
	s.renderAdmin(w, r, "network_sites", s.t("Sites"), "net-sites", netSitesData{Sites: sites, Total: total, Status: status,
		Query: search, Statuses: siteStatuses, Page: newPagination("/admin/network/sites", q, page, per, total)})
}

type netSiteData struct {
	*NetSiteDetail
	Roles      []string
	DomainCard domainsView
}

// loadNetSite reads the site of the request, or answers 404.
func (s *Server) loadNetSite(w http.ResponseWriter, r *http.Request) (*NetSiteDetail, bool) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	d, err := s.portal.SiteDetail(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	return d, true
}

func (s *Server) adminNetSite(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	s.renderNetSite(w, r, http.StatusOK, d, "", "", nil)
}

func (s *Server) adminNetSiteAction(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	ctx, site, by := r.Context(), d.Site, uid(r)
	back := fmt.Sprintf("/admin/network/sites/%d", site.ID)
	details := map[string]string{"site": strconv.FormatInt(site.ID, 10)}
	var err error
	var done string
	action := r.PathValue("action")
	switch action {
	case "approve":
		err, done = s.portal.ApproveSite(ctx, site.ID, by), s.t("The site %s was approved.", site.Slug)
	case "reject":
		err, done = s.portal.RejectSite(ctx, site.ID), s.t("The site %s was rejected.", site.Slug)
	case "suspend":
		reason := trimmed(r, "reason")
		if len(reason) > 500 {
			s.redirect(w, r, back, "error", s.t("The reason is too long (500 characters max)."))
			return
		}
		details["reason"] = reason
		err, done = s.portal.SuspendSite(ctx, site.ID, reason), s.t("The site %s was suspended.", site.Slug)
	case "resume":
		err, done = s.portal.ResumeSite(ctx, site.ID), s.t("The site %s was resumed.", site.Slug)
	case "delete":
		if trimmed(r, "confirm") != site.Slug {
			s.redirect(w, r, back, "error", s.t("Type the name of the site, %s, to delete it.", site.Slug))
			return
		}
		err, done = s.portal.DeleteSite(ctx, site.ID), s.t("The site %s was deleted.", site.Slug)
	default:
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	switch {
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.siteStateError(site.Status))
		return
	case errors.Is(err, store.ErrNotFound):
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	past := map[string]string{"approve": "approved", "reject": "rejected", "suspend": "suspended", "resume": "resumed", "delete": "deleted"}[action]
	s.audit(r, by, "network.site_"+past, site.Slug, details)
	kind := "success"
	switch action {
	case "approve":
		if !s.mailOwner(ctx, &site, s.t("Your site %s was approved", site.Slug),
			s.t("Your site %s on %s was approved and is now open.", site.Host, s.settings().SiteTitle)+"\n\n"+
				s.t("Open its administration with your sap13ns account:")+"\n"+site.AdminURL()) {
			kind, done = "warning", s.t("The site %s was approved, but its owner could not be told by e-mail.", site.Slug)
		}
	case "reject":
		if !s.mailOwner(ctx, &site, s.t("Your site %s was not approved", site.Slug),
			s.t("Your request for the site %s on %s was not approved.", site.Host, s.settings().SiteTitle)+"\n\n"+
				s.t("Your sites:")+"\n"+s.cfg.AbsURL("/admin/sites")) {
			kind, done = "warning", s.t("The site %s was rejected, but its owner could not be told by e-mail.", site.Slug)
		}
	}
	s.redirect(w, r, back, kind, done)
}

// mailOwner e-mails the owner of a site and reports whether it was sent.
func (s *Server) mailOwner(ctx context.Context, site *NetSite, subject, body string) bool {
	owner, err := s.store.UserByID(ctx, site.OwnerID)
	if err == nil {
		err = s.mailer.Send(owner.Email, subject+" · "+s.settings().SiteTitle, body)
	}
	if err != nil {
		s.log.Error("e-mail the owner of a site", "site", site.ID, "err", err)
		return false
	}
	return true
}

// siteStateError tells that an action does not apply to a site in its
// status (as the page showed it: it may have changed meanwhile).
func (s *Server) siteStateError(status string) string {
	switch status {
	case "pending":
		return s.t("This cannot be done while the site waits for approval.")
	case "active":
		return s.t("This cannot be done while the site is open.")
	case "suspended":
		return s.t("This cannot be done while the site is suspended.")
	case "deleted":
		return s.t("This cannot be done to a deleted site.")
	}
	return s.t("This cannot be done while the site is being created.")
}

// ------------------------------------------------------------- members --

// accountByForm finds the account a form names: "account" (an id) or
// "login" (a username or an e-mail address).
func (s *Server) accountByForm(r *http.Request) (*store.User, error) {
	if raw := r.FormValue("account"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, store.ErrNotFound
		}
		return s.store.UserByID(r.Context(), id)
	}
	login := strings.ToLower(trimmed(r, "login"))
	if login == "" || len(login) > 254 {
		return nil, store.ErrNotFound
	}
	return s.store.UserByLogin(r.Context(), login)
}

func (s *Server) adminNetMemberSet(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	back := fmt.Sprintf("/admin/network/sites/%d#members", d.Site.ID)
	role := r.FormValue("role")
	if !auth.ValidRole(role) {
		s.redirect(w, r, back, "error", s.t("Invalid request."))
		return
	}
	acct, err := s.accountByForm(r)
	if errors.Is(err, store.ErrNotFound) {
		s.redirect(w, r, back, "error", s.t("No account has that username or e-mail address."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	switch err := s.portal.SetMember(r.Context(), d.Site.ID, acct.ID, role, uid(r)); {
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.t("The owner of a site is always an administrator."))
		return
	case errors.Is(err, store.ErrNotFound):
		s.redirect(w, r, back, "error", s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "network.member_set", d.Site.Slug, map[string]string{"site": strconv.FormatInt(d.Site.ID, 10),
		"account": acct.Username, "role": role})
	s.redirect(w, r, back, "success", s.t("%s now has the role %s on %s.", acct.Username, s.t(roleLabels[role]), d.Site.Slug))
}

func (s *Server) adminNetMemberRemove(w http.ResponseWriter, r *http.Request) {
	d, ok := s.loadNetSite(w, r)
	if !ok {
		return
	}
	back := fmt.Sprintf("/admin/network/sites/%d#members", d.Site.ID)
	account, err := strconv.ParseInt(r.PathValue("account"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	name := strconv.FormatInt(account, 10)
	for _, m := range d.Members {
		if m.AccountID == account {
			name = m.Username
		}
	}
	switch err := s.portal.RemoveMember(r.Context(), d.Site.ID, account); {
	case errors.Is(err, ErrNetworkState):
		s.redirect(w, r, back, "error", s.t("The owner of a site cannot be removed."))
		return
	case errors.Is(err, store.ErrNotFound):
		s.redirect(w, r, back, "error", s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "network.member_removed", d.Site.Slug, map[string]string{"site": strconv.FormatInt(d.Site.ID, 10), "account": name})
	s.redirect(w, r, back, "success", s.t("%s was removed from %s.", name, d.Site.Slug))
}

// -------------------------------------------------------------- admins --

func (s *Server) adminNetAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := s.portal.NetworkAdmins(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "network_admins", s.t("Network administrators"), "net-admins", map[string]any{"Admins": admins})
}

func (s *Server) adminNetAdminAdd(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/network/admins"
	acct, err := s.accountByForm(r)
	if errors.Is(err, store.ErrNotFound) {
		s.redirect(w, r, back, "error", s.t("No account has that username or e-mail address."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if acct.Status != "active" {
		s.redirect(w, r, back, "error", s.t("This account is disabled."))
		return
	}
	if err := s.portal.AddNetworkAdmin(r.Context(), acct.ID, uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "network.admin_granted", acct.Username, nil)
	s.redirect(w, r, back, "success", s.t("%s is now a network administrator.", acct.Username))
}

func (s *Server) adminNetAdminRemove(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/network/admins"
	account, err := strconv.ParseInt(r.PathValue("account"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	acct, err := s.store.UserByID(r.Context(), account)
	if err != nil {
		s.redirect(w, r, back, "error", s.t("Not found."))
		return
	}
	switch err := s.portal.RemoveNetworkAdmin(r.Context(), account); {
	case errors.Is(err, ErrLastNetworkAdmin):
		s.redirect(w, r, back, "error", s.t("This is the last network administrator. Make someone else a network administrator first."))
		return
	case errors.Is(err, store.ErrNotFound):
		s.redirect(w, r, back, "error", s.t("Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "network.admin_revoked", acct.Username, nil)
	if account == uid(r) {
		s.redirect(w, r, "/admin/", "success", s.t("You are no longer a network administrator."))
		return
	}
	s.redirect(w, r, back, "success", s.t("%s is no longer a network administrator.", acct.Username))
}

// --------------------------------------------------------------- plans --

func (s *Server) adminNetPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.portal.Plans(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "network_plans", s.t("Plans"), "net-plans", map[string]any{"Plans": plans})
}

// ------------------------------------------------------------ my sites --

type mySitesData struct {
	Sites      []NetSite
	CanRequest bool   // the portal takes requests and the account has no Personal site
	Open       bool   // the portal takes requests
	Domain     string // of the default hosts
	Slug       string
	Error      string

	// With billing: the sites the account can subscribe, what to, and how
	// the last Checkout ended (?billing=success or canceled).
	Billing    bool
	Upgradable []NetSite
	Offers     []PlanOffer
	Returned   string
}

func (s *Server) mySitesData(r *http.Request) (mySitesData, error) {
	u := currentUser(r)
	sites, err := s.portal.AccountSites(r.Context(), u.ID)
	if err != nil {
		return mySitesData{}, err
	}
	d := mySitesData{Sites: sites, Open: s.settings().AllowRegistration, Domain: s.cfg.SitesDomain}
	d.CanRequest = d.Open && !slices.ContainsFunc(sites, func(x NetSite) bool { return x.OwnerID == u.ID && x.Plan == "personal" })
	if s.billing == nil {
		return d, nil
	}
	d.Billing = true
	if d.Offers, err = s.billing.PlanOffers(r.Context()); err != nil {
		return d, err
	}
	for _, x := range sites {
		// A paid site without a subscription was granted by an operator.
		if x.OwnerID == u.ID && x.Status == "active" && (x.Billing == nil && !x.PlanPaid || x.Billing != nil && x.Billing.State == "canceled") {
			d.Upgradable = append(d.Upgradable, x)
		}
	}
	if v := r.URL.Query().Get("billing"); v == "success" || v == "canceled" {
		d.Returned = v
	}
	return d, nil
}

func (s *Server) adminMySites(w http.ResponseWriter, r *http.Request) {
	d, err := s.mySitesData(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "my_sites", s.t("My sites"), "my-sites", d)
}

func (s *Server) adminSiteRequest(w http.ResponseWriter, r *http.Request) {
	if !s.settings().AllowRegistration {
		s.adminError(w, r, http.StatusForbidden, s.t("This network does not take requests for new sites."))
		return
	}
	u := currentUser(r)
	slug := strings.ToLower(trimmed(r, "slug"))
	fail := func(status int, msg string) {
		d, err := s.mySitesData(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Slug, d.Error = slug, msg
		s.renderAdminStatus(w, r, status, "my_sites", s.t("My sites"), "my-sites", d)
	}
	site, err := s.portal.RequestSite(r.Context(), u.ID, slug)
	switch {
	case errors.Is(err, ErrNetworkInvalid):
		fail(http.StatusUnprocessableEntity, s.t("Choose another name: 1 to 63 lower-case letters, digits and hyphens, not starting or ending with a hyphen, and not a reserved name."))
		return
	case errors.Is(err, store.ErrConflict):
		fail(http.StatusConflict, s.t("That name is taken. Choose another one."))
		return
	case errors.Is(err, ErrNetworkState):
		fail(http.StatusConflict, s.t("You already have a Personal site."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "network.site_requested", site.Slug, map[string]string{"site": strconv.FormatInt(site.ID, 10), "plan": site.Plan})
	s.redirect(w, r, "/admin/sites", "success", s.t("Your site %s was requested. It opens once a network administrator approves it.", site.Host))
}
