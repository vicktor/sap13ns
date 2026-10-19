package network

// The hosts of the sites. Every site has its default host,
// <slug>.<SAP13NS_SITES_DOMAIN>, for life: it is the RP ID of its
// passkeys. A tenant may add custom domains, as many as its plan allows;
// one is routed once a DNS check (verify.go) finds its TXT record and its
// address, and can then be made the primary host. Verified custom domains
// are checked again every day: one whose checks keep failing lapses (it
// is no longer routed, and a lapsed primary gives way to the default
// host).
//
// The changes to the domains of a site run under the site's row lock, so
// they never interleave, and end with a notification so the routers
// reload their host table.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"sap13ns/internal/store"
)

// Domain kinds.
const (
	DomainDefault = "default" // <slug>.<SitesDomain>, or the portal's host
	DomainCustom  = "custom"
)

// Re-checks of verified custom domains.
const (
	// RecheckAfter is how long a verified custom domain goes without a
	// check before the daily maintenance checks it again.
	RecheckAfter = 20 * time.Hour
	// LapseFailures and LapseGrace: a verified custom domain lapses when at
	// least LapseFailures checks in a row failed (a resolver error does
	// not count) and the first of them is at least LapseGrace old. With
	// daily checks, that is the fourth failed check.
	LapseFailures = 3
	LapseGrace    = 72 * time.Hour

	recheckBatch       = 500
	recheckConcurrency = 8
)

type Domain struct {
	Host          string     `db:"host"`
	SiteID        int64      `db:"site_id"`
	Kind          string     `db:"kind"`
	Primary       bool       `db:"is_primary"`
	VerifyToken   string     `db:"verify_token"` // the TXT record value proves control of a custom domain
	VerifiedAt    *time.Time `db:"verified_at"`  // nil: not routed
	CheckedAt     *time.Time `db:"checked_at"`
	CheckFailures int        `db:"check_failures"` // failed re-checks in a row of a verified domain
	CheckError    string     `db:"check_error"`    // the Check* code of the last check
	CreatedAt     time.Time  `db:"created_at"`
	FailingSince  *time.Time `db:"failing_since"` // the first failed re-check of the current run
	LapsedAt      *time.Time `db:"lapsed_at"`     // it lapsed (and is not verified again)

	// TakenFrom is set by AddDomain when the host was another site's,
	// waiting for verification, and this site proved control of it.
	TakenFrom int64 `db:"-"`
}

const domainCols = `host, site_id, kind, is_primary, verify_token, verified_at, checked_at, check_failures, check_error, created_at,
	failing_since, lapsed_at`

// ErrClaimed is a host that another site added and has not verified. A
// site takes it over by proving control: the TXT record of its own token
// (ClaimToken) must be in place when it adds the host.
var ErrClaimed = fmt.Errorf("%w: the domain is claimed by another site", store.ErrConflict)

// Domains lists the hosts of a site, the primary first.
func (n *Network) Domains(ctx context.Context, siteID int64) ([]Domain, error) {
	rows, err := n.db.Query(ctx, `SELECT `+domainCols+` FROM domains WHERE site_id = $1
		ORDER BY is_primary DESC, kind, host`, siteID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Domain])
}

func (n *Network) Domain(ctx context.Context, host string) (*Domain, error) {
	rows, err := n.db.Query(ctx, `SELECT `+domainCols+` FROM domains WHERE host = $1`, host)
	if err != nil {
		return nil, err
	}
	d, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Domain])
	return d, mapErr(err)
}

// under reports whether host is domain or one of its subdomains.
func under(host, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// CustomHost canonicalises a host a site wants as a custom domain. The
// hosts at, under or above SAP13NS_SITES_DOMAIN, the portal's host and
// SAP13NS_DOMAIN_TARGET belong to the network.
func (n *Network) CustomHost(host string) (string, error) {
	host, err := CanonicalHost(strings.ToLower(strings.TrimSpace(host)))
	if err != nil {
		return "", err
	}
	switch {
	case !strings.Contains(host, "."):
		return "", fmt.Errorf("%w: %s is not a domain name", ErrInvalid, host)
	case under(host, n.sitesDomain) || under(n.sitesDomain, host) || under(host, n.baseHost) || under(n.baseHost, host) ||
		under(host, n.target) || under(n.target, host):
		return "", fmt.Errorf("%w: %s belongs to the network", ErrInvalid, host)
	}
	return host, nil
}

// ClaimToken is the verification token a site gets for a host (the value
// of its TXT record is TXTValue of it), also before the site adds it.
func (n *Network) ClaimToken(siteID int64, host string) string { return n.domainToken(siteID, host) }

// lockSite takes the row lock of a tenant site that domains can change
// on: ErrNotFound for another site, ErrState if it is deleted or still
// being created.
func lockSite(ctx context.Context, tx pgx.Tx, siteID int64) error {
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM sites WHERE id = $1 AND kind = 'tenant' FOR UPDATE`, siteID).Scan(&status); err != nil {
		return err
	}
	if status == StatusDeleted || status == StatusProvisioning {
		return fmt.Errorf("%w: the site is %s", ErrState, status)
	}
	return nil
}

// AddDomain adds a custom domain to a tenant site, within its plan's
// limit (ErrLimit). It is not routed until verified (CheckDomain). A host
// of the network is ErrInvalid; one another site uses, or that this site
// has already, is ErrConflict. A host another site added but has not
// verified is ErrClaimed, unless the TXT record of this site's token is
// already in place: the host then moves to this site, unverified
// (Domain.TakenFrom).
func (n *Network) AddDomain(ctx context.Context, siteID int64, host string) (*Domain, error) {
	host, err := n.CustomHost(host)
	if err != nil {
		return nil, err
	}
	token := n.domainToken(siteID, host)
	proven := false
	if d, err := n.Domain(ctx, host); err == nil && d.SiteID != siteID && d.VerifiedAt == nil {
		cctx, cancel := context.WithTimeout(ctx, dnsTimeout)
		proven, err = n.hasTXT(cctx, host, token)
		cancel()
		if err != nil {
			n.log.Warn("custom domain claim: DNS lookup failed", "host", host, "err", err)
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	var from int64
	err = n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		if err := lockSite(ctx, tx, siteID); err != nil {
			return err
		}
		// Counted under the site's lock; a downgrade keeps what is there.
		if err := customDomainsLeft(ctx, tx, siteID); err != nil {
			return err
		}
		var owner int64
		var verified *time.Time
		err := tx.QueryRow(ctx, `SELECT site_id, verified_at FROM domains WHERE host = $1 FOR UPDATE`, host).Scan(&owner, &verified)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case owner == siteID || verified != nil:
			return fmt.Errorf("%w: domains_pkey", store.ErrConflict)
		case !proven:
			return ErrClaimed
		default:
			if _, err := tx.Exec(ctx, `DELETE FROM domains WHERE host = $1 AND site_id = $2 AND verified_at IS NULL`, host, owner); err != nil {
				return err
			}
			from = owner
		}
		_, err = tx.Exec(ctx, `INSERT INTO domains (host, site_id, kind, verify_token) VALUES ($1, $2, 'custom', $3)`,
			host, siteID, token)
		return err
	})
	if err != nil {
		return nil, err
	}
	if from != 0 {
		n.log.Info("custom domain taken over", "host", host, "site", siteID, "from", from)
	}
	d, err := n.Domain(ctx, host)
	if d != nil {
		d.TakenFrom = from
	}
	return d, err
}

// CheckDomain runs the DNS check of a custom domain of a site (ErrNotFound
// for a host of another site). When it passes, an unverified domain is
// verified, which makes the network route it, and a failing one is clear
// again; when it fails, the outcome is recorded, but only the daily
// re-check counts failures towards a lapse. A default domain needs no
// check.
func (n *Network) CheckDomain(ctx context.Context, siteID int64, host string) (*Domain, DomainCheck, error) {
	d, err := n.Domain(ctx, host)
	if err == nil && d.SiteID != siteID {
		err = store.ErrNotFound
	}
	if err != nil || d.Kind != DomainCustom {
		return d, DomainCheck{}, err
	}
	chk := n.checkDNS(ctx, d.Host, d.VerifyToken)
	now := n.now()
	err = n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		if err := lockSite(ctx, tx, siteID); err != nil {
			return err
		}
		var tag pgconn.CommandTag
		var err error
		if chk.OK() {
			tag, err = tx.Exec(ctx, `UPDATE domains SET verified_at = coalesce(verified_at, $3), checked_at = $3, check_failures = 0,
				check_error = '', failing_since = NULL, lapsed_at = NULL WHERE host = $1 AND site_id = $2 AND verify_token = $4`,
				host, siteID, now, d.VerifyToken)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE domains SET checked_at = $3, check_error = $4 WHERE host = $1 AND site_id = $2`,
				host, siteID, now, chk.Problem)
		}
		if err == nil && tag.RowsAffected() == 0 {
			return store.ErrNotFound // removed or taken over meanwhile
		}
		return err
	})
	if err != nil {
		return nil, chk, err
	}
	d, err = n.Domain(ctx, host)
	return d, chk, err
}

// VerifyDomain records that control of a custom domain was proven without
// a DNS check (`site domain verify -force`), which makes the network route
// it.
func (n *Network) VerifyDomain(ctx context.Context, host string) error {
	d, err := n.Domain(ctx, host)
	if err != nil {
		return err
	}
	return n.mutate(ctx, d.SiteID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE domains SET verified_at = coalesce(verified_at, $2), checked_at = $2,
			check_failures = 0, check_error = '', failing_since = NULL, lapsed_at = NULL WHERE host = $1`, host, n.now())
		return err
	})
}

// SetPrimaryDomain makes a verified host of a site the one it is served
// on; its other hosts redirect to it. The passkey RP ID does not change.
// ErrNotFound for a host of another site, ErrState for one not verified.
func (n *Network) SetPrimaryDomain(ctx context.Context, siteID int64, host string) error {
	return n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		if err := lockSite(ctx, tx, siteID); err != nil {
			return err
		}
		var verified *time.Time
		if err := tx.QueryRow(ctx, `SELECT verified_at FROM domains WHERE host = $1 AND site_id = $2 FOR UPDATE`,
			host, siteID).Scan(&verified); err != nil {
			return err
		}
		if verified == nil {
			return fmt.Errorf("%w: %s is not verified", ErrState, host)
		}
		if _, err := tx.Exec(ctx, `UPDATE domains SET is_primary = false WHERE site_id = $1 AND is_primary AND host <> $2`,
			siteID, host); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE domains SET is_primary = true WHERE host = $1 AND site_id = $2`, host, siteID)
		return err
	})
}

// RemoveDomain removes a custom domain of a site that is not its primary
// one. The default domain stays for the life of the site: it is the
// passkey RP ID.
func (n *Network) RemoveDomain(ctx context.Context, siteID int64, host string) error {
	return n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		if err := lockSite(ctx, tx, siteID); err != nil {
			return err
		}
		var kind string
		var primary bool
		if err := tx.QueryRow(ctx, `SELECT kind, is_primary FROM domains WHERE host = $1 AND site_id = $2 FOR UPDATE`,
			host, siteID).Scan(&kind, &primary); err != nil {
			return err
		}
		switch {
		case kind != DomainCustom:
			return fmt.Errorf("%w: the default domain of a site cannot be removed", ErrState)
		case primary:
			return fmt.Errorf("%w: make another domain primary first", ErrState)
		}
		_, err := tx.Exec(ctx, `DELETE FROM domains WHERE host = $1 AND site_id = $2`, host, siteID)
		return err
	})
}

// --------------------------------------------------------------- lapses --

// A Lapse is a custom domain the daily re-check stopped routing.
type Lapse struct {
	SiteID   int64
	Host     string
	Problem  string // the Check* code of the last check
	Primary  bool   // it was the site's primary host...
	Fallback string // ...and this one (the default host) is now
}

// RecheckDomains checks the verified custom domains of the sites in use
// that were not checked for RecheckAfter, and returns those that lapsed.
func (n *Network) RecheckDomains(ctx context.Context) ([]Lapse, error) {
	now := n.now()
	rows, err := n.db.Query(ctx, `SELECT `+prefixed("d.", domainCols)+` FROM domains d JOIN sites s ON s.id = d.site_id
		WHERE d.kind = 'custom' AND d.verified_at IS NOT NULL AND s.status = ANY($1) AND (d.checked_at IS NULL OR d.checked_at <= $2)
		ORDER BY d.checked_at NULLS FIRST, d.host LIMIT $3`, routable, now.Add(-RecheckAfter), recheckBatch)
	if err != nil {
		return nil, err
	}
	due, err := pgx.CollectRows(rows, pgx.RowToStructByName[Domain])
	if err != nil {
		return nil, err
	}
	var (
		mu     sync.Mutex
		lapsed []Lapse
		wg     sync.WaitGroup
		sem    = make(chan struct{}, recheckConcurrency)
	)
	for _, d := range due {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return lapsed, ctx.Err()
		}
		wg.Go(func() {
			defer func() { <-sem }()
			l, err := n.recordRecheck(ctx, &d, n.checkDNS(ctx, d.Host, d.VerifyToken), now)
			if err != nil {
				if ctx.Err() == nil {
					n.log.Error("re-check a custom domain", "host", d.Host, "site", d.SiteID, "err", err)
				}
				return
			}
			if l != nil {
				mu.Lock()
				lapsed = append(lapsed, *l)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return lapsed, ctx.Err()
}

// prefixed qualifies a column list with a table alias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// recordRecheck records the outcome of the re-check of a verified domain
// at now, and lapses it when its failures reached the limit.
func (n *Network) recordRecheck(ctx context.Context, d *Domain, chk DomainCheck, now time.Time) (*Lapse, error) {
	if chk.OK() || chk.Temporary() {
		// Neither changes the routing: no notification. A resolver error
		// is recorded but never counted.
		failures := "0"
		since := "NULL"
		if chk.Temporary() {
			failures, since = "check_failures", "failing_since"
		}
		_, err := n.db.Exec(ctx, `UPDATE domains SET checked_at = $3, check_error = $4, check_failures = `+failures+
			`, failing_since = `+since+` WHERE host = $1 AND site_id = $2 AND verified_at IS NOT NULL`, d.Host, d.SiteID, now, chk.Problem)
		return nil, err
	}
	var failures int
	var since time.Time
	err := n.db.QueryRow(ctx, `UPDATE domains SET checked_at = $3, check_error = $4, check_failures = check_failures + 1,
		failing_since = coalesce(failing_since, $3) WHERE host = $1 AND site_id = $2 AND verified_at IS NOT NULL
		RETURNING check_failures, failing_since`, d.Host, d.SiteID, now, chk.Problem).Scan(&failures, &since)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // removed or no longer verified meanwhile
	}
	if err != nil || failures < LapseFailures || now.Sub(since) < LapseGrace {
		return nil, err
	}
	l := &Lapse{SiteID: d.SiteID, Host: d.Host, Problem: chk.Problem}
	err = n.mutate(ctx, d.SiteID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM sites WHERE id = $1 FOR UPDATE`, d.SiteID); err != nil {
			return err
		}
		// Still verified and still failing, under the site's lock.
		if err := tx.QueryRow(ctx, `SELECT is_primary FROM domains WHERE host = $1 AND site_id = $2 AND verified_at IS NOT NULL
			AND check_failures >= $3 AND failing_since <= $4 FOR UPDATE`,
			d.Host, d.SiteID, LapseFailures, now.Add(-LapseGrace)).Scan(&l.Primary); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE domains SET verified_at = NULL, is_primary = false, lapsed_at = $2 WHERE host = $1`,
			d.Host, now); err != nil {
			return err
		}
		if l.Primary {
			return tx.QueryRow(ctx, `UPDATE domains SET is_primary = true WHERE site_id = $1 AND kind = 'default' RETURNING host`,
				d.SiteID).Scan(&l.Fallback)
		}
		return nil
	})
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // verified again, or changed, meanwhile
	}
	if err != nil {
		return nil, err
	}
	n.log.Warn("custom domain lapsed", "host", d.Host, "site", d.SiteID, "problem", chk.Problem, "primary", l.Primary)
	return l, nil
}
