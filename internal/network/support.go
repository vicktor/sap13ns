package network

// Operator access (docs/MULTISITE.md): a network admin who is not a member
// of a site opens it as an operator in two cases only.
//
//   - Support access: while the site's owner, or one of its
//     administrators, allows it, for 24 hours, 7 days or until revoked. It
//     opens the site to every network admin.
//   - Emergency access: without that consent, for a legal order or a
//     serious security incident, started by one network admin for
//     themselves, for at most four hours, with a justification that the
//     owner is e-mailed. It works on a suspended site too, whose admin is
//     then served to that session only.
//
// Both live in the network schema. The single sign-on checks them when it
// issues and when it redeems a code (sso.go), and the site checks them on
// every request of an operator session (web.SSOClient.OperatorAllowed), so
// a session ends with them.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// The durations of a support grant; 0 is until revoked.
const (
	SupportDay  = 24 * time.Hour
	SupportWeek = 7 * 24 * time.Hour
)

// EmergencyTTL is how long an emergency access lasts.
const EmergencyTTL = 4 * time.Hour

// The reasons for an emergency access.
const (
	EmergencyLegal    = "legal"    // a legal order
	EmergencySecurity = "security" // a serious security incident: malware, phishing
)

var (
	// ErrNoSupport refuses an operator: the site does not allow support
	// access now. It is an ErrSSODenied.
	ErrNoSupport = fmt.Errorf("%w: the site does not allow support access", ErrSSODenied)
	// ErrNoEmergency refuses an emergency sign-in whose access ended. It
	// is an ErrSSODenied.
	ErrNoEmergency = fmt.Errorf("%w: the emergency access ended", ErrSSODenied)
	// ErrSupportDenied means the account may not change the operator
	// access of the site: only its owner and its administrators grant and
	// revoke support access, network admins may also revoke it, and only
	// network admins start and end emergency access.
	ErrSupportDenied = errors.New("network: the account may not change the operator access of the site")
	// ErrEmergencyOpen means the account already has a live emergency
	// access to the site.
	ErrEmergencyOpen = errors.New("network: an emergency access to the site is already open")
)

// A SupportGrant is one consent to support access, live or past.
type SupportGrant struct {
	ID            int64      `db:"id"`
	SiteID        int64      `db:"site_id"`
	GrantedBy     *int64     `db:"granted_by"`
	GrantedAt     time.Time  `db:"granted_at"`
	ExpiresAt     *time.Time `db:"expires_at"` // nil: until revoked
	RevokedAt     *time.Time `db:"revoked_at"`
	RevokedBy     *int64     `db:"revoked_by"`
	GrantedByName string     `db:"granted_by_name"` // "" once the account is gone
	RevokedByName string     `db:"revoked_by_name"`
}

// Live reports whether the grant lets operators in at now.
func (g *SupportGrant) Live(now time.Time) bool {
	return g.RevokedAt == nil && (g.ExpiresAt == nil || g.ExpiresAt.After(now))
}

const supportCols = `g.id, g.site_id, g.granted_by, g.granted_at, g.expires_at, g.revoked_at, g.revoked_by,
	coalesce((SELECT u.username FROM users u WHERE u.id = g.granted_by), '') AS granted_by_name,
	coalesce((SELECT u.username FROM users u WHERE u.id = g.revoked_by), '') AS revoked_by_name`

// liveSupport is the condition of a live grant of site $1 at $2.
const liveSupport = `g.site_id = $1 AND g.revoked_at IS NULL AND (g.expires_at IS NULL OR g.expires_at > $2)`

// SupportAccess returns the live grant of a site, nil when support access
// is closed.
func (n *Network) SupportAccess(ctx context.Context, siteID int64) (*SupportGrant, error) {
	rows, err := n.db.Query(ctx, `SELECT `+supportCols+` FROM support_grants g WHERE `+liveSupport+`
		ORDER BY g.granted_at DESC, g.id DESC LIMIT 1`, siteID, n.now())
	if err != nil {
		return nil, err
	}
	g, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[SupportGrant])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return g, err
}

// SupportGrants lists the last grants of a site, newest first.
func (n *Network) SupportGrants(ctx context.Context, siteID int64, limit int) ([]SupportGrant, error) {
	rows, err := n.db.Query(ctx, `SELECT `+supportCols+` FROM support_grants g WHERE g.site_id = $1
		ORDER BY g.granted_at DESC, g.id DESC LIMIT $2`, siteID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SupportGrant])
}

// lockAccessSite locks a tenant site for a change of its operator access
// (FOR NO KEY UPDATE: a redemption holding it FOR SHARE finishes first,
// and a concurrent suspension waits) and reads it: ErrNotFound for any
// other site and for one being created.
func lockAccessSite(ctx context.Context, tx pgx.Tx, siteID int64) (*Site, error) {
	var s Site
	err := tx.QueryRow(ctx, `SELECT id, kind, slug, schema_name, status, coalesce(owner_id, 0) FROM sites WHERE id = $1 FOR NO KEY UPDATE`, siteID).
		Scan(&s.ID, &s.Kind, &s.Slug, &s.Schema, &s.Status, &s.OwnerID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, store.ErrNotFound
	case err != nil:
		return nil, err
	case s.Kind != KindTenant || s.Status == StatusProvisioning:
		return nil, store.ErrNotFound
	}
	return &s, nil
}

// actorActive holds the row of an account and checks that it is active
// (ErrSupportDenied otherwise).
func actorActive(ctx context.Context, tx pgx.Tx, by int64) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR SHARE`, by).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrSupportDenied
	case err != nil:
		return err
	case status != "active":
		return ErrSupportDenied
	}
	return nil
}

// lockedNetworkAdmin reports whether an account is a network admin,
// holding the row.
func lockedNetworkAdmin(ctx context.Context, tx pgx.Tx, by int64) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM network_admins WHERE account_id = $1 FOR SHARE`, by).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// supportSite locks a site for a change of its support access and checks
// that the account may make it: its owner or one of its administrators,
// active, or with anyNetworkAdmin a network admin.
func supportSite(ctx context.Context, tx pgx.Tx, siteID, by int64, anyNetworkAdmin bool) (*Site, error) {
	s, err := lockAccessSite(ctx, tx, siteID)
	if err != nil {
		return nil, err
	}
	if err := actorActive(ctx, tx, by); err != nil {
		return nil, err
	}
	if by == s.OwnerID {
		return s, nil
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT role FROM site_members WHERE site_id = $1 AND account_id = $2 FOR SHARE`, siteID, by).Scan(&role)
	switch {
	case err == nil && role == auth.RoleAdmin:
		return s, nil
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	case !anyNetworkAdmin:
		return nil, ErrSupportDenied
	}
	switch admin, err := lockedNetworkAdmin(ctx, tx, by); {
	case err != nil:
		return nil, err
	case !admin:
		return nil, ErrSupportDenied
	}
	return s, nil
}

// GrantSupport opens support access to a site for d (SupportDay,
// SupportWeek, or 0 until revoked), by its owner or one of its
// administrators (ErrSupportDenied for anyone else). A live grant is
// replaced. Only a served site (active or pending) can be opened:
// ErrState for a suspended or deleted one. The grant is written to the
// site's audit log, with ip; the caller audits it on the portal.
func (n *Network) GrantSupport(ctx context.Context, siteID, by int64, d time.Duration, ip netip.Addr) (*SupportGrant, error) {
	if d != 0 && d != SupportDay && d != SupportWeek {
		return nil, fmt.Errorf("%w: support access for %s", ErrInvalid, d)
	}
	now := n.now()
	var expires *time.Time
	if d > 0 {
		t := now.Add(d)
		expires = &t
	}
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	s, err := supportSite(ctx, tx, siteID, by, false)
	if err != nil {
		return nil, err
	}
	if s.Status != StatusActive && s.Status != StatusPending {
		return nil, fmt.Errorf("%w: support access cannot be opened on a site that is %s", ErrState, s.Status)
	}
	tag, err := tx.Exec(ctx, `UPDATE support_grants g SET revoked_at = $3, revoked_by = $4 WHERE `+liveSupport, siteID, now, now, by)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO support_grants (site_id, granted_by, granted_at, expires_at) VALUES ($1, $2, $3, $4) RETURNING id`,
		siteID, by, now, expires).Scan(&id); err != nil {
		return nil, mapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	g := &SupportGrant{ID: id, SiteID: siteID, GrantedBy: &by, GrantedAt: now, ExpiresAt: expires}
	g.GrantedByName, _ = n.AccountName(ctx, &by)
	details := map[string]string{"network_account": fmt.Sprint(by), "account": g.GrantedByName, "until": "revoked"}
	if expires != nil {
		details["until"] = expires.UTC().Format(time.RFC3339)
	}
	if tag.RowsAffected() > 0 {
		details["replaced"] = "true"
	}
	n.siteAudit(ctx, s, "support.granted", g.GrantedByName, ip, details)
	n.log.Info("support access granted", "site", siteID, "account", by, "until", details["until"])
	return g, nil
}

// RevokeSupport closes the support access of a site, by its owner, one of
// its administrators or a network admin (ErrSupportDenied for anyone
// else), whatever its status. It reports whether a live grant was
// revoked. Either way the operator sessions of the site end at once,
// loaded or not (an emergency session stays). A revocation is written to
// the site's audit log, with ip.
func (n *Network) RevokeSupport(ctx context.Context, siteID, by int64, ip netip.Addr) (bool, error) {
	now := n.now()
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	s, err := supportSite(ctx, tx, siteID, by, true)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE support_grants g SET revoked_at = $2, revoked_by = $3 WHERE `+liveSupport, siteID, now, by)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	revoked := tag.RowsAffected() > 0
	if revoked {
		name, _ := n.AccountName(ctx, &by)
		n.siteAudit(ctx, s, "support.revoked", name, ip, map[string]string{"network_account": fmt.Sprint(by), "account": name})
		n.log.Info("support access revoked", "site", siteID, "account", by)
	}
	return revoked, n.endOperatorSessions(ctx, s, store.AMRSSOOperator)
}

// ------------------------------------------------------------ emergency --

// An EmergencyAccess is one emergency access to a site, live or past.
type EmergencyAccess struct {
	ID            int64      `db:"id"`
	SiteID        int64      `db:"site_id"`
	AccountID     *int64     `db:"account_id"` // nil once the account is gone
	Category      string     `db:"category"`
	Justification string     `db:"justification"`
	StartedAt     time.Time  `db:"started_at"`
	ExpiresAt     time.Time  `db:"expires_at"`
	EndedAt       *time.Time `db:"ended_at"`
	EndedBy       *int64     `db:"ended_by"`
	AccountName   string     `db:"account_name"`
	EndedByName   string     `db:"ended_by_name"`
}

// Live reports whether the access lets its network admin in at now.
func (e *EmergencyAccess) Live(now time.Time) bool { return e.EndedAt == nil && e.ExpiresAt.After(now) }

const emergencyCols = `e.id, e.site_id, e.account_id, e.category, e.justification, e.started_at, e.expires_at, e.ended_at, e.ended_by,
	coalesce((SELECT u.username FROM users u WHERE u.id = e.account_id), '') AS account_name,
	coalesce((SELECT u.username FROM users u WHERE u.id = e.ended_by), '') AS ended_by_name`

// liveEmergency is the condition of a live emergency access to site $1 at
// $2.
const liveEmergency = `e.site_id = $1 AND e.ended_at IS NULL AND e.expires_at > $2`

func (n *Network) emergencies(ctx context.Context, where string, args ...any) ([]EmergencyAccess, error) {
	rows, err := n.db.Query(ctx, `SELECT `+emergencyCols+` FROM emergency_access e WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[EmergencyAccess])
}

// EmergencyAccess returns the live emergency access of an account to a
// site, nil when it has none.
func (n *Network) EmergencyAccess(ctx context.Context, siteID, accountID int64) (*EmergencyAccess, error) {
	list, err := n.emergencies(ctx, liveEmergency+` AND e.account_id = $3 ORDER BY e.expires_at DESC LIMIT 1`, siteID, n.now(), accountID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// Emergencies lists the last emergency accesses to a site, newest first.
func (n *Network) Emergencies(ctx context.Context, siteID int64, limit int) ([]EmergencyAccess, error) {
	return n.emergencies(ctx, `e.site_id = $1 ORDER BY e.started_at DESC, e.id DESC LIMIT $2`, siteID, limit)
}

// CleanJustification is the justification of an emergency access as it is
// stored: trimmed, without control characters but line breaks, 1 to 500
// characters. ok is false for one that is empty or too long.
func CleanJustification(s string) (string, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	n := utf8.RuneCountInString(s)
	return s, n >= 1 && n <= 500
}

// StartEmergency opens emergency access to a site for a network admin, by,
// for EmergencyTTL, for category (EmergencyLegal or EmergencySecurity) and
// a justification (CleanJustification): ErrInvalid for either missing,
// ErrSupportDenied for an account that is not an active network admin,
// ErrEmergencyOpen when it already has one there. It works whatever the
// site's status but deleted (ErrState). It notifies the routers, which
// then serve a suspended site to the emergency session, and writes the
// site's audit log; the caller audits it on the portal and e-mails the
// owner.
func (n *Network) StartEmergency(ctx context.Context, siteID, by int64, category, justification string, ip netip.Addr) (*EmergencyAccess, error) {
	text, ok := CleanJustification(justification)
	switch {
	case !slices.Contains([]string{EmergencyLegal, EmergencySecurity}, category):
		return nil, fmt.Errorf("%w: emergency access for %q", ErrInvalid, category)
	case !ok:
		return nil, fmt.Errorf("%w: an emergency access needs a justification of 1 to 500 characters", ErrInvalid)
	}
	now := n.now()
	var (
		s  *Site
		id int64
	)
	err := n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var err error
		if s, err = lockAccessSite(ctx, tx, siteID); err != nil {
			return err
		}
		if s.Status == StatusDeleted {
			return fmt.Errorf("%w: the site is deleted", ErrState)
		}
		if err := actorActive(ctx, tx, by); err != nil {
			return err
		}
		switch admin, err := lockedNetworkAdmin(ctx, tx, by); {
		case err != nil:
			return err
		case !admin:
			return ErrSupportDenied
		}
		var one int
		switch err := tx.QueryRow(ctx, `SELECT 1 FROM emergency_access e WHERE `+liveEmergency+` AND e.account_id = $3`, siteID, now, by).Scan(&one); {
		case err == nil:
			return ErrEmergencyOpen
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO emergency_access (site_id, account_id, category, justification, started_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, siteID, by, category, text, now, now.Add(EmergencyTTL)).Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	e := &EmergencyAccess{ID: id, SiteID: siteID, AccountID: &by, Category: category, Justification: text, StartedAt: now, ExpiresAt: now.Add(EmergencyTTL)}
	e.AccountName, _ = n.AccountName(ctx, &by)
	n.siteAudit(ctx, s, "emergency.started", e.AccountName, ip, map[string]string{"network_account": fmt.Sprint(by), "account": e.AccountName,
		"category": category, "justification": text, "until": e.ExpiresAt.UTC().Format(time.RFC3339)})
	n.log.Warn("emergency access started", "site", siteID, "account", by, "category", category)
	return e, nil
}

// EndEmergency ends every live emergency access to a site before it
// expires, by a network admin (ErrSupportDenied for anyone else), and
// returns how many it ended. Their sessions end at once, and the routers
// stop serving a suspended site. Each one is written to the site's audit
// log.
func (n *Network) EndEmergency(ctx context.Context, siteID, by int64, ip netip.Addr) (int, error) {
	now := n.now()
	var (
		s     *Site
		ended []EmergencyAccess
	)
	err := n.mutate(ctx, siteID, func(tx pgx.Tx) error {
		var err error
		if s, err = lockAccessSite(ctx, tx, siteID); err != nil {
			return err
		}
		if err := actorActive(ctx, tx, by); err != nil {
			return err
		}
		switch admin, err := lockedNetworkAdmin(ctx, tx, by); {
		case err != nil:
			return err
		case !admin:
			return ErrSupportDenied
		}
		rows, err := tx.Query(ctx, `UPDATE emergency_access e SET ended_at = $2, ended_by = $3 WHERE `+liveEmergency+` RETURNING `+emergencyCols, siteID, now, by)
		if err != nil {
			return err
		}
		ended, err = pgx.CollectRows(rows, pgx.RowToStructByName[EmergencyAccess])
		return err
	})
	if err != nil {
		return 0, err
	}
	name, _ := n.AccountName(ctx, &by)
	for _, e := range ended {
		n.siteAudit(ctx, s, "emergency.ended", e.AccountName, ip, map[string]string{"network_account": fmt.Sprint(by), "account": name,
			"category": e.Category, "opened_by": e.AccountName})
	}
	if len(ended) > 0 {
		n.log.Info("emergency access ended", "site", siteID, "by", by, "count", len(ended))
	}
	return len(ended), n.endOperatorSessions(ctx, s, store.AMRSSOEmergency)
}

// endOperatorSessions applies a closed operator access to a site's
// operator users, loaded or not: one whose account has neither a live
// support grant nor a live emergency access left is disabled and signed
// out; the others lose only their sessions of the closed kind (amr).
func (n *Network) endOperatorSessions(ctx context.Context, s *Site, amr string) error {
	if !hasSchema(s) {
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
	support, err := n.SupportAccess(ctx, s.ID)
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range linked {
		if !u.Operator || u.Status != "active" {
			continue
		}
		emergency, err := n.EmergencyAccess(ctx, s.ID, u.AccountID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if support == nil && emergency == nil {
			errs = append(errs, n.revokeOperator(ctx, s, u.AccountID))
			continue
		}
		if _, err := st.DeleteNetworkSessions(ctx, u.AccountID, true, amr); err != nil {
			errs = append(errs, fmt.Errorf("site %d: %w", s.ID, err))
		}
	}
	return errors.Join(errs...)
}

// siteAudit writes an entry to a site's audit log; a failure is logged.
func (n *Network) siteAudit(ctx context.Context, s *Site, action, target string, ip netip.Addr, details map[string]string) {
	st, err := n.siteStore(s)
	if err == nil {
		err = st.Audit(ctx, 0, action, target, ip, details)
	}
	if err != nil {
		n.log.Error("site audit", "site", s.ID, "action", action, "err", err)
	}
}
