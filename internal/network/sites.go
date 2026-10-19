package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// Site statuses.
const (
	StatusProvisioning = "provisioning" // being created: never routed
	StatusPending      = "pending"      // waiting for a network admin's approval
	StatusActive       = "active"
	StatusSuspended    = "suspended" // by a network admin: only a 503 page
	StatusDeleted      = "deleted"   // kept until purged
)

// Site kinds.
const (
	KindNetwork = "network" // the portal
	KindTenant  = "tenant"
)

type Site struct {
	ID               int64      `db:"id"`
	Kind             string     `db:"kind"`
	Slug             string     `db:"slug"`
	Schema           string     `db:"schema_name"`
	OwnerID          int64      `db:"owner_id"` // 0 for the network site
	Plan             string     `db:"plan"`     // "" for the network site
	SubscriptionID   *int64     `db:"subscription_id"`
	Status           string     `db:"status"`
	ApprovedBy       *int64     `db:"approved_by"`
	ApprovedAt       *time.Time `db:"approved_at"`
	SuspendedReason  string     `db:"suspended_reason"`
	SuspendedAt      *time.Time `db:"suspended_at"`
	Secret           []byte     `db:"secret"` // sealed; see SiteSecret
	RPID             string     `db:"rp_id"`
	StorageBytes     int64      `db:"storage_bytes"`
	StorageCheckedAt *time.Time `db:"storage_checked_at"`
	NextAnnounceAt   *time.Time `db:"next_announce_at"`
	WantsCron        bool       `db:"wants_cron"`
	CreatedAt        time.Time  `db:"created_at"`
	UpdatedAt        time.Time  `db:"updated_at"`
	DeletedAt        *time.Time `db:"deleted_at"`
	PrimaryHost      string     `db:"primary_host"`
}

const siteCols = `s.id, s.kind, s.slug, s.schema_name, coalesce(s.owner_id, 0) AS owner_id, coalesce(s.plan, '') AS plan,
	s.subscription_id, s.status, s.approved_by, s.approved_at, s.suspended_reason, s.suspended_at, s.secret, s.rp_id,
	s.storage_bytes, s.storage_checked_at, s.next_announce_at, s.wants_cron, s.created_at, s.updated_at, s.deleted_at,
	coalesce((SELECT d.host FROM domains d WHERE d.site_id = s.id AND d.is_primary), '') AS primary_host`

// Dir is the data directory of a site: uploads, plugins, themes and caches.
func (n *Network) Dir(siteID int64) string {
	return filepath.Join(n.dataDir, "sites", strconv.FormatInt(siteID, 10))
}

func (n *Network) siteBy(ctx context.Context, where string, arg any) (*Site, error) {
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s WHERE `+where, arg)
	if err != nil {
		return nil, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Site])
	return s, mapErr(err)
}

func (n *Network) Site(ctx context.Context, id int64) (*Site, error) {
	return n.siteBy(ctx, `s.id = $1`, id)
}

func (n *Network) SiteBySlug(ctx context.Context, slug string) (*Site, error) {
	return n.siteBy(ctx, `s.slug = $1`, slug)
}

// NetworkSite returns the portal's site row.
func (n *Network) NetworkSite(ctx context.Context) (*Site, error) {
	return n.siteBy(ctx, `s.kind = $1`, KindNetwork)
}

type SiteQuery struct {
	Status  string // "" lists every site that is not deleted
	OwnerID int64
	Limit   int // default 100, at most 1000
	Offset  int
}

// ListSites returns a page of sites, oldest first, and the total.
func (n *Network) ListSites(ctx context.Context, q SiteQuery) ([]Site, int, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	const where = ` WHERE (($1 = '' AND s.status <> 'deleted') OR s.status = $1) AND ($2 = 0 OR s.owner_id = $2)`
	var total int
	if err := n.db.QueryRow(ctx, `SELECT count(*) FROM sites s`+where, q.Status, q.OwnerID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s`+where+` ORDER BY s.id LIMIT $3 OFFSET $4`,
		q.Status, q.OwnerID, limit, max(q.Offset, 0))
	if err != nil {
		return nil, 0, err
	}
	sites, err := pgx.CollectRows(rows, pgx.RowToStructByName[Site])
	return sites, total, err
}

type Plan struct {
	Slug          string `db:"slug"`
	Name          string `db:"name"`
	Paid          bool   `db:"paid"`
	HandApproved  bool   `db:"hand_approved"`
	Commercial    bool   `db:"commercial"`
	SitesPerGrant int    `db:"sites_per_grant"`
	StorageBytes  int64  `db:"storage_bytes"`
	CustomDomains int    `db:"custom_domains"`
	PluginUploads bool   `db:"plugin_uploads"`
	ThemeUploads  bool   `db:"theme_uploads"`
	Position      int    `db:"position"`
}

const planCols = `slug, name, paid, hand_approved, commercial, sites_per_grant, storage_bytes, custom_domains,
	plugin_uploads, theme_uploads, position`

func (n *Network) Plans(ctx context.Context) ([]Plan, error) {
	rows, err := n.db.Query(ctx, `SELECT `+planCols+` FROM plans ORDER BY position, slug`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Plan])
}

func (n *Network) Plan(ctx context.Context, slug string) (*Plan, error) {
	rows, err := n.db.Query(ctx, `SELECT `+planCols+` FROM plans WHERE slug = $1`, slug)
	if err != nil {
		return nil, err
	}
	p, err := pgx.CollectExactlyOneRow(rows, pgx.RowToAddrOfStructByName[Plan])
	return p, mapErr(err)
}

var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedSlugs cannot name a site: their subdomains belong to the network,
// to mail and DNS conventions, or look like part of the service.
var reservedSlugs = []string{
	"abuse", "account", "accounts", "admin", "api", "app", "assets", "auth", "autoconfig", "autodiscover",
	"billing", "cdn", "dashboard", "dev", "docs", "ftp", "help", "hostmaster", "id", "imap", "isatap",
	"localhost", "login", "mail", "media", "mx", "network", "noc", "ns", "ns1", "ns2", "oauth", "pop",
	"portal", "postmaster", "root", "sap13ns", "security", "signin", "signup", "smtp", "sso", "staging",
	"static", "status", "support", "test", "uploads", "webauthn", "webmail", "webmaster", "wpad", "www",
}

// ValidSlug checks the name of a new site, which is also its subdomain.
func ValidSlug(slug string) error {
	switch {
	case !slugRe.MatchString(slug):
		return fmt.Errorf("%w: a site name has 1 to 63 lower-case letters, digits and inner hyphens, got %q", ErrInvalid, slug)
	case len(slug) >= 4 && slug[2:4] == "--":
		// xn-- is punycode; the DNS reserves every ??-- prefix.
		return fmt.Errorf("%w: a site name cannot have hyphens in its third and fourth characters", ErrInvalid)
	case slices.Contains(reservedSlugs, slug):
		return fmt.Errorf("%w: the site name %q is reserved", ErrInvalid, slug)
	}
	return nil
}

type NewSite struct {
	Slug    string
	OwnerID int64 // a network account; becomes the site's first admin
	Plan    string
}

// CreateSite provisions a site: the row (with its sealed secret and
// default domain <slug>.<SitesDomain>), the schema site_<id> with every
// site migration, and its directory. A failure in any step undoes the
// earlier ones. The site is then active, or pending when its plan is
// approved by hand.
func (n *Network) CreateSite(ctx context.Context, ns NewSite) (*Site, error) {
	if err := ValidSlug(ns.Slug); err != nil {
		return nil, err
	}
	host, err := CanonicalHost(ns.Slug + "." + n.sitesDomain)
	if err != nil || host == n.baseHost {
		return nil, fmt.Errorf("%w: the site name %q does not make a usable host name", ErrInvalid, ns.Slug)
	}
	plan, err := n.Plan(ctx, ns.Plan)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: no plan %q", ErrInvalid, ns.Plan)
	} else if err != nil {
		return nil, err
	}

	var id int64
	err = n.mutate(ctx, 0, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT nextval('sites_id_seq')`).Scan(&id); err != nil {
			return err
		}
		sealed, err := n.wrapSecret(id, newSecret())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sites (id, kind, slug, schema_name, owner_id, plan, status, secret, rp_id)
			VALUES ($1, 'tenant', $2, $3, $4, $5, 'provisioning', $6, $7)`,
			id, ns.Slug, db.SiteSchema(id), ns.OwnerID, plan.Slug, sealed, host); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO domains (host, site_id, kind, is_primary, verified_at)
			VALUES ($1, $2, 'default', true, now())`, host, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO site_members (site_id, account_id, role) VALUES ($1, $2, 'admin')`, id, ns.OwnerID)
		return err
	})
	if err != nil {
		return nil, err
	}

	status := StatusActive
	if plan.HandApproved {
		status = StatusPending
	}
	dirMade, err := n.provision(ctx, id, status)
	if err != nil {
		// Undo without the caller's deadline, which may be what failed.
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if perr := n.purge(cctx, id, dirMade); perr != nil {
			n.log.Error("site provisioning: cleanup failed", "site", id, "err", perr)
			return nil, errors.Join(err, perr)
		}
		return nil, err
	}
	n.log.Info("site created", "site", id, "slug", ns.Slug, "status", status)
	return n.Site(ctx, id)
}

// provision runs the steps after the row exists. dirMade reports whether
// it created the site directory, which only then is removed on failure.
func (n *Network) provision(ctx context.Context, id int64, status string) (dirMade bool, err error) {
	step := func(name string) error {
		if n.hook != nil {
			return n.hook(name)
		}
		return nil
	}
	if err := step("row"); err != nil {
		return false, err
	}
	schema := db.SiteSchema(id)
	if _, err := n.db.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		return false, fmt.Errorf("create schema %s: %w", schema, err)
	}
	if err := step("schema"); err != nil {
		return false, err
	}
	set, err := db.SiteMigrations()
	if err != nil {
		return false, err
	}
	if err := db.MigrateSchema(ctx, n.pool, schema, set, n.log); err != nil {
		return false, fmt.Errorf("migrate %s: %w", schema, err)
	}
	if err := step("migrate"); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(n.Dir(id)), 0o750); err != nil {
		return false, err
	}
	// Ids are never reused, so an existing directory is not ours to keep
	// or to remove.
	if err := os.Mkdir(n.Dir(id), 0o750); err != nil {
		return false, err
	}
	if err := step("dir"); err != nil {
		return true, err
	}
	err = n.mutate(ctx, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sites SET status = $2, updated_at = now() WHERE id = $1 AND status = 'provisioning'`, id, status)
		return err
	})
	if err != nil {
		return true, err
	}
	return true, step("status")
}

// purge removes a tenant site entirely: schema, directory (when removeDir)
// and row, in that order, so an interrupted purge can be run again.
func (n *Network) purge(ctx context.Context, id int64, removeDir bool) error {
	schema := db.SiteSchema(id)
	if _, err := n.db.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
		return fmt.Errorf("drop schema %s: %w", schema, err)
	}
	if removeDir {
		if err := os.RemoveAll(n.Dir(id)); err != nil {
			return err
		}
	}
	return n.mutate(ctx, id, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM sites WHERE id = $1 AND kind = 'tenant'`, id)
		return err
	})
}

// InitNetworkSite creates the portal's site row with its default domain
// host (the SAP13NS_BASE_URL host) and directory, unless it exists. The
// network schema itself is created by Migrate.
func (n *Network) InitNetworkSite(ctx context.Context, host string) (*Site, error) {
	host, err := CanonicalHost(host)
	if err != nil {
		return nil, err
	}
	if s, err := n.NetworkSite(ctx); err == nil {
		return s, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	var id int64
	err = n.mutate(ctx, 0, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT nextval('sites_id_seq')`).Scan(&id); err != nil {
			return err
		}
		sealed, err := n.wrapSecret(id, newSecret())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sites (id, kind, slug, schema_name, status, secret, rp_id)
			VALUES ($1, 'network', 'network', 'network', 'active', $2, $3)`, id, sealed, host); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO domains (host, site_id, kind, is_primary, verified_at)
			VALUES ($1, $2, 'default', true, now())`, host, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(n.Dir(id), 0o750); err != nil {
		return nil, err
	}
	return n.Site(ctx, id)
}

// WakeAt records that a site has a scheduled post due at at, so that it
// is woken then even if unloaded. Routing does not change, so it does not
// notify.
func (n *Network) WakeAt(ctx context.Context, siteID int64, at time.Time) error {
	_, err := n.db.Exec(ctx, `UPDATE sites SET next_announce_at = least(next_announce_at, $2::timestamptz) WHERE id = $1`, siteID, at)
	return err
}

// setStatus moves a tenant site from one of the statuses in from; it
// returns ErrNotFound for no such tenant and ErrState for another status.
func (n *Network) setStatus(ctx context.Context, id int64, from []string, set string, args ...any) error {
	return n.mutate(ctx, id, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sites s SET `+set+`, updated_at = now()
			WHERE s.id = $1 AND s.kind = 'tenant' AND s.status = ANY($2)`, append([]any{id, from}, args...)...)
		if err != nil || tag.RowsAffected() == 1 {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM sites WHERE id = $1 AND kind = 'tenant'`, id).Scan(&status); err != nil {
			return err
		}
		return fmt.Errorf("%w: the site is %s", ErrState, status)
	})
}

// SuspendSite takes a site offline (a 503 page) for reason, which the
// owner sees.
func (n *Network) SuspendSite(ctx context.Context, id int64, reason string) error {
	if len(reason) > 500 {
		return fmt.Errorf("%w: the reason is longer than 500 bytes", ErrInvalid)
	}
	return n.setStatus(ctx, id, []string{StatusActive, StatusPending},
		`status = 'suspended', suspended_reason = $3, suspended_at = now()`, reason)
}

// ResumeSite lifts a suspension. A site whose plan needs approval and that
// was never approved goes back to pending.
func (n *Network) ResumeSite(ctx context.Context, id int64) error {
	return n.setStatus(ctx, id, []string{StatusSuspended},
		`status = CASE WHEN s.approved_at IS NULL AND (SELECT p.hand_approved FROM plans p WHERE p.slug = s.plan)
			THEN 'pending' ELSE 'active' END, suspended_reason = '', suspended_at = NULL`)
}

// DeleteSite stops serving a site. Its data stays until PurgeSite.
func (n *Network) DeleteSite(ctx context.Context, id int64) error {
	return n.setStatus(ctx, id, []string{StatusPending, StatusActive, StatusSuspended},
		`status = 'deleted', deleted_at = now()`)
}

// PurgeSite removes a deleted site for good: its schema, its directory and
// its row with domains and members. A site left in provisioning by a
// crash can be purged too, once it is clearly not being created any more.
func (n *Network) PurgeSite(ctx context.Context, id int64) error {
	s, err := n.Site(ctx, id)
	if err != nil {
		return err
	}
	switch {
	case s.Kind != KindTenant || s.Status != StatusDeleted && s.Status != StatusProvisioning:
		return fmt.Errorf("%w: only a deleted site can be purged", ErrState)
	case s.Status == StatusProvisioning && time.Since(s.CreatedAt) < time.Hour:
		return fmt.Errorf("%w: the site is still being created", ErrState)
	}
	return n.purge(ctx, id, true)
}
