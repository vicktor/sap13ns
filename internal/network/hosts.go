package network

import (
	"context"
	"net"
	"slices"
	"strconv"
	"strings"
)

// A siteRoute is what the router knows of a site: enough to route a
// request and to build the site's configuration.
type siteRoute struct {
	ID      int64
	Kind    string
	Slug    string
	Status  string
	Primary string   // the host the site is served on
	Custom  bool     // Primary is a custom domain
	Hosts   []string // every routed host, the primary first
}

// key identifies the configuration a site was loaded with: when it
// changes (another primary host, a host added or removed), a loaded site
// is reloaded.
func (s *siteRoute) key() string {
	return s.Primary + "|" + strconv.FormatBool(s.Custom) + "|" + strings.Join(s.Hosts, ",")
}

// aliases are the routed hosts other than the primary.
func (s *siteRoute) aliases() []string { return slices.Clone(s.Hosts[1:]) }

// A hostTable is an immutable snapshot of the routed hosts.
type hostTable struct {
	hosts   map[string]*siteRoute
	sites   map[int64]*siteRoute
	network *siteRoute // the portal; nil before `sap13ns network init`
}

// routable are the statuses whose sites the router knows: a pending site
// still serves its sign-in and admin, a suspended one a 503 page. Sites
// being provisioned or deleted are unknown hosts.
var routable = []string{StatusPending, StatusActive, StatusSuspended}

// hostTable reads the verified domains of the routable sites. The portal
// is always served on the SAP13NS_BASE_URL host, which becomes its
// primary if the operator changed the base URL after `network init`; its
// other hosts then redirect there.
func (n *Network) hostTable(ctx context.Context) (*hostTable, error) {
	rows, err := n.db.Query(ctx, `SELECT s.id, s.kind, s.slug, s.status, d.host, d.kind = 'custom', d.is_primary
		FROM domains d JOIN sites s ON s.id = d.site_id
		WHERE d.verified_at IS NOT NULL AND s.status = ANY($1)
		ORDER BY s.id, d.is_primary DESC, d.host`, routable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &hostTable{hosts: map[string]*siteRoute{}, sites: map[int64]*siteRoute{}}
	for rows.Next() {
		var (
			s             siteRoute
			host          string
			custom, prime bool
		)
		if err := rows.Scan(&s.ID, &s.Kind, &s.Slug, &s.Status, &host, &custom, &prime); err != nil {
			return nil, err
		}
		cur := t.sites[s.ID]
		if cur == nil {
			if !prime {
				continue // no primary host (not possible with the constraints): not served
			}
			s.Primary, s.Custom = host, custom
			cur = &s
			t.sites[s.ID] = cur
		}
		cur.Hosts = append(cur.Hosts, host)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, s := range t.sites {
		if s.Kind == KindNetwork {
			if s.Primary != n.baseHost {
				s.Hosts = append([]string{n.baseHost}, slices.DeleteFunc(s.Hosts, func(h string) bool { return h == n.baseHost })...)
				s.Primary, s.Custom = n.baseHost, false
			}
			t.network = s
		}
		for _, h := range s.Hosts {
			t.hosts[h] = s
		}
	}
	if t.network != nil {
		t.hosts[n.baseHost] = t.network // never a tenant's
	}
	return t, nil
}

// hostOf returns the canonical host of a request's Host header, without
// port; ok is false for an IP address or a name that is not valid.
func hostOf(hostport string) (string, bool) {
	if strings.HasPrefix(hostport, "[") {
		return "", false // an IPv6 literal
	}
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	c, err := CanonicalHost(strings.ToLower(h))
	return c, err == nil
}
