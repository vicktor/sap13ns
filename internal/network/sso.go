package network

// The network side of single sign-on (see internal/web/sso.go for the
// flow) and of the access of network accounts to the sites: the users
// linked to an account follow its membership and its status, applied
// through a scoped store of each site, loaded or not.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// SSOCodeTTL is how long a single sign-on code can be redeemed.
const SSOCodeTTL = time.Minute

var (
	// ErrSSOCode is a code that cannot be redeemed: unknown, used, expired,
	// issued for another site or callback, or with another verifier.
	ErrSSOCode = errors.New("network: invalid single sign-on code")
	// ErrSSODenied means the account may not sign in to the site.
	ErrSSODenied = errors.New("network: the account has no access to the site")
)

// SSOCode is a code to issue: who signs in to which site, through which
// callback, for the verifier whose sha256 is Challenge.
type SSOCode struct {
	SiteID      int64
	AccountID   int64
	Challenge   []byte
	RedirectURI string
	Operator    bool // a network admin who is not a member
}

// IssueSSOCode stores a code, only its hash, for SSOCodeTTL, and returns
// it. Codes do not change routing, so nothing is notified.
func (n *Network) IssueSSOCode(ctx context.Context, c SSOCode) (string, error) {
	if len(c.Challenge) != sha256.Size || c.RedirectURI == "" {
		return "", fmt.Errorf("%w: malformed single sign-on code request", ErrInvalid)
	}
	code := auth.NewToken(32)
	_, err := n.db.Exec(ctx, `INSERT INTO sso_codes (code_hash, site_id, account_id, challenge, redirect_uri, operator, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + $7::bigint * interval '1 second')`,
		auth.HashToken(code), c.SiteID, c.AccountID, c.Challenge, c.RedirectURI, c.Operator, int64(SSOCodeTTL/time.Second))
	return code, mapErr(err)
}

// An SSOGrant is a redeemed code.
type SSOGrant struct {
	AccountID   int64
	Operator    bool
	Role        string
	Username    string
	Email       string
	DisplayName string
}

// RedeemSSOCode uses a code once, for the site and callback it was issued
// for, checking the verifier, and then the access again: the site served,
// the account active and still a member (or, for an operator, still a
// network admin). A failed check after the code was used still uses it,
// so a code cannot be tried twice.
func (n *Network) RedeemSSOCode(ctx context.Context, siteID int64, code, verifier, redirectURI string) (*SSOGrant, error) {
	var (
		g         SSOGrant
		challenge []byte
	)
	err := n.db.QueryRow(ctx, `UPDATE sso_codes SET used_at = now()
		WHERE code_hash = $1 AND site_id = $2 AND redirect_uri = $3 AND used_at IS NULL AND expires_at > now()
		RETURNING account_id, challenge, operator`, auth.HashToken(code), siteID, redirectURI).Scan(&g.AccountID, &challenge, &g.Operator)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSSOCode
	}
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare(sum[:], challenge) != 1 {
		return nil, ErrSSOCode
	}
	s, err := n.Site(ctx, siteID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrSSODenied
	} else if err != nil {
		return nil, err
	}
	if s.Kind != KindTenant || s.Status != StatusActive && s.Status != StatusPending {
		return nil, ErrSSODenied
	}
	var status string
	err = n.db.QueryRow(ctx, `SELECT username, email, display_name, status FROM users WHERE id = $1`, g.AccountID).
		Scan(&g.Username, &g.Email, &g.DisplayName, &status)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "active" {
		return nil, ErrSSODenied
	} else if err != nil {
		return nil, err
	}
	role, operator, err := n.SSOAccess(ctx, siteID, g.AccountID)
	if err != nil {
		return nil, err
	}
	if operator != g.Operator {
		// The membership changed since the code was issued: sign in again.
		return nil, ErrSSODenied
	}
	g.Role = role
	if !operator {
		if _, err := n.db.Exec(ctx, `UPDATE site_members SET last_sso_at = now() WHERE site_id = $1 AND account_id = $2`, siteID, g.AccountID); err != nil {
			return nil, err
		}
	}
	return &g, nil
}

// SSOAccess returns the role an account signs in to a tenant site with:
// its membership's or, for a network admin who is not a member, admin as
// an operator. ErrSSODenied otherwise.
func (n *Network) SSOAccess(ctx context.Context, siteID, accountID int64) (role string, operator bool, err error) {
	m, err := n.Member(ctx, siteID, accountID)
	if err == nil {
		return m.Role, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", false, err
	}
	admin, err := n.IsNetworkAdmin(ctx, accountID)
	if err != nil {
		return "", false, err
	}
	if !admin {
		return "", false, ErrSSODenied
	}
	return auth.RoleAdmin, true, nil
}

// siteStore is a store of a site's schema, whether or not the site is
// loaded.
func (n *Network) siteStore(s *Site) (*store.Store, error) {
	h, err := db.Scope(n.pool, s.Schema)
	if err != nil {
		return nil, err
	}
	return store.New(h), nil
}

// hasSchema reports whether a site's schema can be used: not while it is
// being created.
func hasSchema(s *Site) bool { return s.Status != StatusProvisioning }

// accountActive reports whether a network account exists and is active.
func (n *Network) accountActive(ctx context.Context, accountID int64) (bool, error) {
	var status string
	err := n.db.QueryRow(ctx, `SELECT status FROM users WHERE id = $1`, accountID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return status == "active", err
}

// syncAccess applies the access an account has to a site to its linked
// user there: the membership's role while it is a member with an active
// account, disabled otherwise. The change is recorded in the site's audit
// log. A site without such a user changes nothing.
func (n *Network) syncAccess(ctx context.Context, s *Site, accountID int64) error {
	if !hasSchema(s) {
		return nil
	}
	role, status := "", "disabled"
	active, err := n.accountActive(ctx, accountID)
	if err != nil {
		return err
	}
	if active {
		switch m, err := n.Member(ctx, s.ID, accountID); {
		case err == nil:
			role, status = m.Role, "active"
		case !errors.Is(err, store.ErrNotFound):
			return err
		}
	}
	return n.applyAccess(ctx, s, accountID, false, role, status)
}

// revokeOperator disables the operator user of an account on a site.
func (n *Network) revokeOperator(ctx context.Context, s *Site, accountID int64) error {
	if !hasSchema(s) {
		return nil
	}
	return n.applyAccess(ctx, s, accountID, true, "", "disabled")
}

func (n *Network) applyAccess(ctx context.Context, s *Site, accountID int64, operator bool, role, status string) error {
	st, err := n.siteStore(s)
	if err != nil {
		return err
	}
	username, changed, err := st.SetNetworkAccess(ctx, accountID, operator, role, status)
	if errors.Is(err, store.ErrNotFound) || err == nil && !changed {
		return nil
	}
	if err != nil {
		return fmt.Errorf("site %d: %w", s.ID, err)
	}
	details := map[string]string{"network_account": fmt.Sprint(accountID), "status": status}
	if role != "" {
		details["role"] = role
	}
	if operator {
		details["operator"] = "true"
	}
	if err := st.Audit(ctx, 0, "user.network_access", username, netip.Addr{}, details); err != nil {
		n.log.Error("site audit", "site", s.ID, "err", err)
	}
	n.log.Info("network access applied", "site", s.ID, "account", accountID, "operator", operator, "status", status, "role", role)
	return nil
}

// accountSites lists the sites with a schema whose members include an
// account.
func (n *Network) accountSites(ctx context.Context, accountID int64) ([]Site, error) {
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s JOIN site_members m ON m.site_id = s.id
		WHERE m.account_id = $1 AND s.kind = 'tenant' AND s.status <> 'provisioning' ORDER BY s.id`, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Site])
}

// revokeOperatorEverywhere disables the operator users of an account on
// every site, which the network admin may have opened.
func (n *Network) revokeOperatorEverywhere(ctx context.Context, accountID int64) error {
	sites, err := n.schemaSites(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range sites {
		errs = append(errs, n.revokeOperator(ctx, &sites[i], accountID))
	}
	return errors.Join(errs...)
}

// schemaSites lists the tenants that have a schema: served or deleted but
// not yet purged.
func (n *Network) schemaSites(ctx context.Context) ([]Site, error) {
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s WHERE s.kind = 'tenant' AND s.status <> 'provisioning' ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Site])
}

// CheckAccountChange tells whether an account may be disabled, or deleted
// when remove: never the last network admin, and an account that owns a
// site (even a deleted one, until it is purged) stays.
func (n *Network) CheckAccountChange(ctx context.Context, accountID int64, remove bool) error {
	var admin bool
	var admins, owned int
	err := n.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM network_admins WHERE account_id = $1),
		(SELECT count(*) FROM network_admins), (SELECT count(*) FROM sites WHERE owner_id = $1)`, accountID).Scan(&admin, &admins, &owned)
	switch {
	case err != nil:
		return err
	case admin && admins == 1:
		return fmt.Errorf("%w: the network needs at least one admin", ErrState)
	case remove && owned > 0:
		return fmt.Errorf("%w: the account owns %d sites", ErrOwned, owned)
	}
	return nil
}

// ErrOwned means an account owns sites, so it cannot be deleted.
var ErrOwned = errors.New("network: the account owns sites")

// AccountChanged applies the status of an account to its users on every
// site it is a member of, and disables its operator users when it is
// disabled. removed revokes everything, before the account is deleted.
func (n *Network) AccountChanged(ctx context.Context, accountID int64, removed bool) error {
	sites, err := n.accountSites(ctx, accountID)
	if err != nil {
		return err
	}
	var errs []error
	for i := range sites {
		if removed {
			errs = append(errs, n.applyAccess(ctx, &sites[i], accountID, false, "", "disabled"))
		} else {
			errs = append(errs, n.syncAccess(ctx, &sites[i], accountID))
		}
	}
	active, err := n.accountActive(ctx, accountID)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if removed || !active {
		errs = append(errs, n.revokeOperatorEverywhere(ctx, accountID))
	}
	return errors.Join(errs...)
}

// ReconcileAccess checks every user of a network account on a site
// against the network and applies what it finds: a revocation that did
// not reach the site (an error, a crash) is applied by the next sweep.
func (n *Network) ReconcileAccess(ctx context.Context, s *Site) error {
	if s.Kind != KindTenant || !hasSchema(s) {
		return nil
	}
	st, err := n.siteStore(s)
	if err != nil {
		return err
	}
	linked, err := st.LinkedUsers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range linked {
		if u.Status != "active" {
			continue // only access is revoked here; a sign-in grants it
		}
		if !u.Operator {
			errs = append(errs, n.syncAccess(ctx, s, u.AccountID))
			continue
		}
		active, err := n.accountActive(ctx, u.AccountID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		admin, err := n.IsNetworkAdmin(ctx, u.AccountID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !active || !admin {
			errs = append(errs, n.revokeOperator(ctx, s, u.AccountID))
		}
	}
	return errors.Join(errs...)
}
