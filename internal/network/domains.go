package network

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
)

// Domain kinds.
const (
	DomainDefault = "default" // <slug>.<SitesDomain>, or the portal's host
	DomainCustom  = "custom"
)

type Domain struct {
	Host          string     `db:"host"`
	SiteID        int64      `db:"site_id"`
	Kind          string     `db:"kind"`
	Primary       bool       `db:"is_primary"`
	VerifyToken   string     `db:"verify_token"` // the TXT record value proves control of a custom domain
	VerifiedAt    *time.Time `db:"verified_at"`  // nil: not routed
	CheckedAt     *time.Time `db:"checked_at"`
	CheckFailures int        `db:"check_failures"`
	CheckError    string     `db:"check_error"`
	CreatedAt     time.Time  `db:"created_at"`
}

const domainCols = `host, site_id, kind, is_primary, verify_token, verified_at, checked_at, check_failures, check_error, created_at`

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

// AddDomain adds a custom domain to a tenant site. It is not routed until
// verified. The hosts at, under or above SAP13NS_SITES_DOMAIN and the
// portal's host belong to the network, and a host already used by any
// site is ErrConflict.
func (n *Network) AddDomain(ctx context.Context, siteID int64, host string) (*Domain, error) {
	host, err := CanonicalHost(host)
	if err != nil {
		return nil, err
	}
	switch {
	case !strings.Contains(host, "."):
		return nil, fmt.Errorf("%w: %s is not a domain name", ErrInvalid, host)
	case under(host, n.sitesDomain) || under(host, n.baseHost) || under(n.sitesDomain, host) || under(n.baseHost, host):
		return nil, fmt.Errorf("%w: %s belongs to the network", ErrInvalid, host)
	}
	err = n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM sites WHERE id = $1 AND kind = 'tenant' FOR UPDATE`, siteID).Scan(&status); err != nil {
			return err
		}
		if status == StatusDeleted || status == StatusProvisioning {
			return fmt.Errorf("%w: the site is %s", ErrState, status)
		}
		// Counted under the site's lock; a downgrade keeps what is there.
		if err := customDomainsLeft(ctx, tx, siteID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO domains (host, site_id, kind, verify_token) VALUES ($1, $2, 'custom', $3)`,
			host, siteID, auth.NewToken(24))
		return err
	})
	if err != nil {
		return nil, err
	}
	return n.Domain(ctx, host)
}

// VerifyDomain records that control of a custom domain was proven, which
// makes the network route it.
func (n *Network) VerifyDomain(ctx context.Context, host string) error {
	d, err := n.Domain(ctx, host)
	if err != nil {
		return err
	}
	return n.mutate(ctx, d.SiteID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE domains SET verified_at = coalesce(verified_at, now()), checked_at = now(),
			check_failures = 0, check_error = '' WHERE host = $1`, host)
		return err
	})
}

// SetPrimaryDomain makes a verified host the one a site is served on; its
// other hosts redirect to it. The passkey RP ID does not change.
func (n *Network) SetPrimaryDomain(ctx context.Context, host string) error {
	d, err := n.Domain(ctx, host)
	if err != nil {
		return err
	}
	if d.VerifiedAt == nil {
		return fmt.Errorf("%w: %s is not verified", ErrState, host)
	}
	return n.mutate(ctx, d.SiteID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE domains SET is_primary = false WHERE site_id = $1 AND is_primary AND host <> $2`,
			d.SiteID, host); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE domains SET is_primary = true WHERE host = $1 AND site_id = $2 AND verified_at IS NOT NULL`,
			host, d.SiteID)
		if err == nil && tag.RowsAffected() == 0 {
			return fmt.Errorf("%w: %s changed meanwhile", ErrState, host)
		}
		return err
	})
}

// RemoveDomain removes a custom domain that is not the primary one. The
// default domain stays for the life of the site: it is the passkey RP ID.
func (n *Network) RemoveDomain(ctx context.Context, host string) error {
	d, err := n.Domain(ctx, host)
	if err != nil {
		return err
	}
	switch {
	case d.Kind != DomainCustom:
		return fmt.Errorf("%w: the default domain of a site cannot be removed", ErrState)
	case d.Primary:
		return fmt.Errorf("%w: make another domain primary first", ErrState)
	}
	return n.mutate(ctx, d.SiteID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM domains WHERE host = $1 AND kind = 'custom' AND NOT is_primary`, host)
		return err
	})
}
