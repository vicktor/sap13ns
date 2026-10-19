package web

// The site switcher of the admin (multisite). On every site of a network,
// the portal included, the header of the sidebar opens a menu with the
// sites the signed-in network account is a member of, and its role on
// each. An entry opens that site's admin through single sign-on (its
// /sso/start), never with a session made here. The menu also links to the
// account's sites on the portal, to the network admin area for network
// admins, and to the request for a site when the account may make one.
//
// Only membership counts: a network admin sees the sites they are a member
// of, while support and emergency access stay on the network admin pages.
// A tenant knows the account only through a session that came through the
// portal (its amr), for the user linked to the account; a session opened
// with the site's own password shows no switcher. A single site has no
// SiteSwitcher, so it shows nothing and makes no query.

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/store"
)

// SiteSwitcher lists the sites of a network account for the switcher. The
// hooks of every site of a network implement it.
type SiteSwitcher interface {
	// SwitcherSites returns what the switcher of this site shows an
	// account: the served sites (pending or active) it is a member of,
	// with its role on each, the site itself marked Current, and what it
	// may do on the portal.
	SwitcherSites(ctx context.Context, accountID int64) (*AccountSites, error)
	// SwitcherEpoch changes whenever the network changes in a way that
	// may change what SwitcherSites returns (a membership, a site's
	// status, a network admin): an answer cached in another epoch is
	// stale.
	SwitcherEpoch() uint64
}

// AccountSites is what SwitcherSites returns.
type AccountSites struct {
	Sites        []SwitcherSite
	More         int    // member sites left out of Sites, which has a limit
	PortalURL    string // the portal's origin
	NetworkAdmin bool   // the account administers the network
	CanRequest   bool   // the portal takes requests and the account has no Personal site
}

// SwitcherSite is a site of the account in the switcher.
type SwitcherSite struct {
	ID      int64
	Slug    string
	Title   string // the title its administrators gave it; may be ""
	Host    string // its primary host
	URL     string // its origin
	Role    string // the account's role there
	Status  string // pending or active
	Current bool   // the site whose admin shows the switcher
}

// Name is what the switcher calls the site.
func (x SwitcherSite) Name() string {
	if t := strings.TrimSpace(x.Title); t != "" {
		return t
	}
	return x.Slug
}

// OpenURL opens the site's admin: through single sign-on, or the admin
// itself for the current site.
func (x SwitcherSite) OpenURL() string {
	if x.Current {
		return "/admin/"
	}
	return x.URL + "/sso/start?next=" + url.QueryEscape("/admin/")
}

// switcherView is the switcher as the admin layout renders it.
type switcherView struct {
	Title, Host string // the site whose admin this is
	Sites       []SwitcherSite
	More        int
	MySitesURL  string
	NetworkURL  string // "" unless a network admin
	RequestURL  string // "" unless the account may request a site
}

// switcherTTL is how long a session reuses the list it was shown: a change
// another process makes (the CLI) shows at most this late, and the
// network's own changes start a new epoch at once.
const switcherTTL = 30 * time.Second

const switcherCacheMax = 2048

// switcherCache keeps the list each session was shown, by session.
type switcherCache struct {
	mu  sync.Mutex
	m   map[string]switcherEntry // by the session's token hash
	now func() time.Time         // tests; nil is time.Now
}

type switcherEntry struct {
	account int64
	epoch   uint64
	until   time.Time
	sites   *AccountSites
}

func (c *switcherCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *switcherCache) get(key string, account int64, epoch uint64) (*AccountSites, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || e.account != account || e.epoch != epoch || !c.clock().Before(e.until) {
		return nil, false
	}
	return e.sites, true
}

func (c *switcherCache) put(key string, account int64, epoch uint64, sites *AccountSites) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	if c.m == nil {
		c.m = map[string]switcherEntry{}
	}
	if len(c.m) >= switcherCacheMax {
		for k, e := range c.m {
			if !now.Before(e.until) || e.epoch != epoch {
				delete(c.m, k)
			}
		}
		if len(c.m) >= switcherCacheMax {
			clear(c.m)
		}
	}
	c.m[key] = switcherEntry{account: account, epoch: epoch, until: now.Add(switcherTTL), sites: sites}
}

// switcherAccount is the network account whose sites the switcher of this
// request shows, if any. On the portal it is the signed-in user, once its
// second factor is set up (the portal shows an account its sites only
// then). On a tenant it is the account linked to the user of a session
// that came through the portal; both come from the database, never from
// the request.
func (s *Server) switcherAccount(r *http.Request) (int64, bool) {
	u, sess := currentUser(r), rctx(r).Session
	if s.switcher == nil || u == nil || sess == nil || sess.UserID != u.ID || sess.MFAPending {
		return 0, false
	}
	if s.portal != nil {
		return u.ID, !s.mustEnroll2FA(r, u)
	}
	if !strings.HasPrefix(sess.AMR, store.AMRSSO) || u.NetworkAccountID == nil {
		return 0, false
	}
	return *u.NetworkAccountID, true
}

// siteSwitcher builds the switcher of an admin page: nil when the page
// has none.
func (s *Server) siteSwitcher(r *http.Request) *switcherView {
	account, ok := s.switcherAccount(r)
	if !ok {
		return nil
	}
	sites, err := s.accountSites(r, account)
	if err != nil {
		// The page goes on without the switcher, as on a single site.
		s.log.Error("list the account's sites", "account", account, "err", err)
		return nil
	}
	portal := "" // the portal's own pages are local
	if s.portal == nil {
		portal = sites.PortalURL
	}
	v := &switcherView{Title: s.settings().SiteTitle, Host: s.cfg.BaseURL.Host, Sites: sites.Sites, More: sites.More,
		MySitesURL: portal + "/admin/sites"}
	if sites.NetworkAdmin {
		v.NetworkURL = portal + "/admin/network/sites"
	}
	if sites.CanRequest {
		v.RequestURL = portal + "/admin/sites#request"
	}
	return v
}

// accountSites returns the account's sites for the session of r, from
// the session's cache while it is fresh.
func (s *Server) accountSites(r *http.Request, account int64) (*AccountSites, error) {
	key := string(rctx(r).Session.TokenHash)
	epoch := s.switcher.SwitcherEpoch()
	if sites, ok := s.switchCache.get(key, account, epoch); ok {
		return sites, nil
	}
	sites, err := s.switcher.SwitcherSites(r.Context(), account)
	if err != nil {
		return nil, err
	}
	s.switchCache.put(key, account, epoch, sites)
	return sites, nil
}
