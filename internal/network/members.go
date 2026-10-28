package network

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// A Member is a network account with a role on a site. The membership is
// the source of truth for the role of the account's linked user there.
type Member struct {
	SiteID    int64      `db:"site_id"`
	AccountID int64      `db:"account_id"`
	Role      string     `db:"role"`
	InvitedBy *int64     `db:"invited_by"`
	LastSSOAt *time.Time `db:"last_sso_at"`
	CreatedAt time.Time  `db:"created_at"`
	Username  string     `db:"username"`
	Email     string     `db:"email"`
}

const memberCols = `m.site_id, m.account_id, m.role, m.invited_by, m.last_sso_at, m.created_at, u.username, u.email`

// Members lists the members of a site by role, then name.
func (n *Network) Members(ctx context.Context, siteID int64) ([]Member, error) {
	rows, err := n.db.Query(ctx, `SELECT `+memberCols+` FROM site_members m JOIN users u ON u.id = m.account_id
		WHERE m.site_id = $1 ORDER BY array_position(ARRAY['admin','editor','author','contributor','subscriber'], m.role), u.username`, siteID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Member])
}

// Member returns the membership of an account on a site.
func (n *Network) Member(ctx context.Context, siteID, accountID int64) (*Member, error) {
	rows, err := n.db.Query(ctx, `SELECT `+memberCols+` FROM site_members m JOIN users u ON u.id = m.account_id
		WHERE m.site_id = $1 AND m.account_id = $2`, siteID, accountID)
	if err != nil {
		return nil, err
	}
	m, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Member])
	return m, mapErr(err)
}

// SetMember adds an account to a tenant site, or changes its role there.
// invitedBy is 0 when nobody invited them (the CLI). The owner stays an
// admin. The account's linked user on the site, if any, takes the role
// at once (its sessions end, so the new role needs a new sign-in).
func (n *Network) SetMember(ctx context.Context, siteID, accountID int64, role string, invitedBy int64) error {
	if !auth.ValidRole(role) {
		return fmt.Errorf("%w: role %q", ErrInvalid, role)
	}
	err := n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var owner int64
		if err := tx.QueryRow(ctx, `SELECT owner_id FROM sites WHERE id = $1 AND kind = 'tenant'`, siteID).Scan(&owner); err != nil {
			return err
		}
		if accountID == owner && role != auth.RoleAdmin {
			return fmt.Errorf("%w: the owner of a site is always an admin", ErrState)
		}
		// A network admin who could add themselves would open any site
		// without the owner's consent: support access is the way in.
		if invitedBy != 0 && accountID == invitedBy && invitedBy != owner {
			return ErrSupportDenied
		}
		_, err := tx.Exec(ctx, `INSERT INTO site_members (site_id, account_id, role, invited_by) VALUES ($1, $2, $3, nullif($4::bigint, 0))
			ON CONFLICT (site_id, account_id) DO UPDATE SET role = EXCLUDED.role`, siteID, accountID, role, invitedBy)
		return err
	})
	if err != nil {
		return err
	}
	// The owner sees in the site's own log whom the network added.
	if s, err := n.Site(ctx, siteID); err == nil && invitedBy != 0 && invitedBy != s.OwnerID {
		by, _ := n.AccountName(ctx, &invitedBy)
		who, _ := n.AccountName(ctx, &accountID)
		n.siteAudit(ctx, s, "network.member_set", who, netip.Addr{}, map[string]string{"role": role, "by": by,
			"network_account": fmt.Sprint(accountID)})
	}
	return n.syncSite(ctx, siteID, accountID)
}

// syncSite applies the access of an account to its user on a site.
func (n *Network) syncSite(ctx context.Context, siteID, accountID int64) error {
	s, err := n.Site(ctx, siteID)
	if err != nil {
		return err
	}
	if err := n.syncAccess(ctx, s, accountID); err != nil {
		return fmt.Errorf("network: apply the membership to the site's user: %w", err)
	}
	return nil
}

// RemoveMember removes an account from a site; the owner cannot be removed.
// The account's linked user there is disabled and signed out, whether or
// not the site is loaded.
func (n *Network) RemoveMember(ctx context.Context, siteID, accountID int64) error {
	err := n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var owner int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(owner_id, 0) FROM sites WHERE id = $1`, siteID).Scan(&owner); err != nil {
			return err
		}
		if accountID == owner {
			return fmt.Errorf("%w: the owner of a site cannot be removed", ErrState)
		}
		tag, err := tx.Exec(ctx, `DELETE FROM site_members WHERE site_id = $1 AND account_id = $2`, siteID, accountID)
		if err == nil && tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return err
	})
	if errors.Is(err, store.ErrNotFound) {
		// Not a member (any more): make sure a revocation that failed
		// before is applied now.
		if serr := n.syncSite(ctx, siteID, accountID); serr != nil && !errors.Is(serr, store.ErrNotFound) {
			return errors.Join(err, serr)
		}
		return err
	}
	if err != nil {
		return err
	}
	return n.syncSite(ctx, siteID, accountID)
}

// AccountName is the username of an account, "" for none.
func (n *Network) AccountName(ctx context.Context, accountID *int64) (string, error) {
	if accountID == nil {
		return "", nil
	}
	var name string
	err := n.db.QueryRow(ctx, `SELECT username FROM users WHERE id = $1`, *accountID).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// A MemberSite is a site an account is a member of, with its role there.
type MemberSite struct {
	Site
	Role string `db:"role"`
}

// MemberSites lists the served sites (pending, active or suspended) an
// account is a member of, by slug.
func (n *Network) MemberSites(ctx context.Context, accountID int64) ([]MemberSite, error) {
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+`, m.role FROM sites s JOIN site_members m ON m.site_id = s.id
		WHERE m.account_id = $1 AND s.kind = 'tenant' AND s.status = ANY($2) ORDER BY s.slug`,
		accountID, []string{StatusPending, StatusActive, StatusSuspended})
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[MemberSite])
}

type NetworkAdmin struct {
	AccountID int64     `db:"account_id"`
	GrantedBy *int64    `db:"granted_by"`
	Super     bool      `db:"super"`
	CreatedAt time.Time `db:"created_at"`
	Username  string    `db:"username"`
	Email     string    `db:"email"`
}

func (n *Network) NetworkAdmins(ctx context.Context) ([]NetworkAdmin, error) {
	rows, err := n.db.Query(ctx, `SELECT a.account_id, a.granted_by, a.super, a.created_at, u.username, u.email
		FROM network_admins a JOIN users u ON u.id = a.account_id ORDER BY a.created_at, a.account_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[NetworkAdmin])
}

func (n *Network) IsNetworkAdmin(ctx context.Context, accountID int64) (bool, error) {
	var ok bool
	err := n.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM network_admins WHERE account_id = $1)`, accountID).Scan(&ok)
	return ok, err
}

// IsSuperAdmin reports whether an account is a super admin: a network
// admin made from the command line.
func (n *Network) IsSuperAdmin(ctx context.Context, accountID int64) (bool, error) {
	var ok bool
	err := n.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM network_admins WHERE account_id = $1 AND super)`, accountID).Scan(&ok)
	return ok, err
}

// ErrLastSuperAdmin refuses to revoke the network's last super admin.
var ErrLastSuperAdmin = fmt.Errorf("%w: the network needs a super admin", ErrState)

// AddNetworkAdmin makes an account a network admin; grantedBy is 0 from
// the CLI, which makes a super admin, also of an existing admin. Adding
// an existing admin from the portal changes nothing.
func (n *Network) AddNetworkAdmin(ctx context.Context, accountID, grantedBy int64) error {
	return n.mutate(ctx, 0, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO network_admins (account_id, granted_by, super) VALUES ($1, nullif($2::bigint, 0), $2::bigint = 0)
			ON CONFLICT (account_id) DO UPDATE SET super = network_admins.super OR excluded.super`, accountID, grantedBy)
		return err
	})
}

// RemoveNetworkAdmin revokes a network admin, but never the last one nor
// the last super admin. The
// operator users they had on the sites are disabled and signed out.
func (n *Network) RemoveNetworkAdmin(ctx context.Context, accountID int64) error {
	err := n.mutate(ctx, 0, func(tx pgx.Tx) error {
		// Locking every row makes two concurrent removals see each other.
		rows, err := tx.Query(ctx, `SELECT account_id, super FROM network_admins FOR UPDATE`)
		if err != nil {
			return err
		}
		type admin struct {
			ID    int64 `db:"account_id"`
			Super bool  `db:"super"`
		}
		admins, err := pgx.CollectRows(rows, pgx.RowToStructByName[admin])
		if err != nil {
			return err
		}
		found, super, supers := false, false, 0
		for _, a := range admins {
			if a.ID == accountID {
				found, super = true, a.Super
			}
			if a.Super {
				supers++
			}
		}
		switch {
		case !found:
			return pgx.ErrNoRows
		case len(admins) == 1:
			return fmt.Errorf("%w: the network needs at least one admin", ErrState)
		case super && supers == 1:
			return ErrLastSuperAdmin
		}
		_, err = tx.Exec(ctx, `DELETE FROM network_admins WHERE account_id = $1`, accountID)
		return err
	})
	if err != nil {
		return err
	}
	return n.revokeOperatorEverywhere(ctx, accountID)
}
