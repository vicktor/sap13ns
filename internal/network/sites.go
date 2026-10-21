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

	"sap13ns/internal/config"
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
	OwnerUsername    string     `db:"owner_username"` // "" for the network site
}

const siteCols = `s.id, s.kind, s.slug, s.schema_name, coalesce(s.owner_id, 0) AS owner_id, coalesce(s.plan, '') AS plan,
	s.subscription_id, s.status, s.approved_by, s.approved_at, s.suspended_reason, s.suspended_at, s.secret, s.rp_id,
	s.storage_bytes, s.storage_checked_at, s.next_announce_at, s.wants_cron, s.created_at, s.updated_at, s.deleted_at,
	coalesce((SELECT d.host FROM domains d WHERE d.site_id = s.id AND d.is_primary), '') AS primary_host,
	coalesce((SELECT u.username FROM users u WHERE u.id = s.owner_id), '') AS owner_username`

// PlanPersonal is the free plan an account can ask for on the portal.
const PlanPersonal = "personal"

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
	Kind    string // "" lists both kinds
	Search  string // part of the slug, of a host, or of the owner's username or e-mail address
	OwnerID int64
	Limit   int // default 100, at most 1000
	Offset  int
}

// ServedSites lists the tenants whose schemas are in use (pending, active
// and suspended), oldest first: what `migrate` and the sweeps go through.
func (n *Network) ServedSites(ctx context.Context) ([]Site, error) {
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s WHERE s.kind = 'tenant' AND s.status = ANY($1) ORDER BY s.id`,
		[]string{StatusPending, StatusActive, StatusSuspended})
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Site])
}

// SiteConfig is the configuration a site runs with, from the network's:
// config.ForNetwork for the portal, config.ForSite on its primary host for
// a tenant.
func (n *Network) SiteConfig(ctx context.Context, base *config.Config, s *Site) (*config.Config, error) {
	secret, err := n.SiteSecret(s)
	if err != nil {
		return nil, err
	}
	if s.Kind == KindNetwork {
		return config.ForNetwork(base, s.ID, secret)
	}
	d, err := n.Domain(ctx, s.PrimaryHost)
	if err != nil {
		return nil, fmt.Errorf("network: primary host of site %d: %w", s.ID, err)
	}
	return config.ForSite(base, config.SiteParams{ID: s.ID, Host: d.Host, Custom: d.Kind == DomainCustom, Secret: secret, RPID: s.RPID})
}

// ListSites returns a page of sites, oldest first, and the total.
func (n *Network) ListSites(ctx context.Context, q SiteQuery) ([]Site, int, error) {
	limit := q.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	const where = ` WHERE (($1 = '' AND s.status <> 'deleted') OR s.status = $1) AND ($2 = 0 OR s.owner_id = $2)
		AND ($3 = '' OR s.kind = $3)
		AND ($4 = '' OR s.slug ILIKE $4 OR EXISTS (SELECT 1 FROM domains d WHERE d.site_id = s.id AND d.host ILIKE $4)
			OR EXISTS (SELECT 1 FROM users u WHERE u.id = s.owner_id AND (u.username ILIKE $4 OR u.email ILIKE $4)))`
	search := ""
	if q.Search != "" {
		search = "%" + store.EscapeLike(q.Search) + "%"
	}
	var total int
	if err := n.db.QueryRow(ctx, `SELECT count(*) FROM sites s`+where, q.Status, q.OwnerID, q.Kind, search).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := n.db.Query(ctx, `SELECT `+siteCols+` FROM sites s`+where+` ORDER BY s.id LIMIT $5 OFFSET $6`,
		q.Status, q.OwnerID, q.Kind, search, limit, max(q.Offset, 0))
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
	// What the network's AI gives a site of the plan each day (paid
	// plans only).
	AIRequestsPerDay int `db:"ai_requests_per_day"`
	AIImagesPerDay   int `db:"ai_images_per_day"`
}

const planCols = `slug, name, paid, hand_approved, commercial, sites_per_grant, storage_bytes, custom_domains,
	plugin_uploads, theme_uploads, position, ai_requests_per_day, ai_images_per_day`

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
	// Requested by the owner on the portal: the account must be active and
	// have no other site of the plan that is not deleted, and the site
	// waits for a network admin's approval whatever its plan says.
	Requested bool
	// Grant is the subscription the site joins, which must be the owner's,
	// active, of the plan, and have room for it (sites_per_grant).
	Grant int64
}

// RequestSite creates the Personal site an account asks for on the
// portal, pending until a network admin approves it. An account has one
// at most, not counting deleted (and rejected) ones: ErrState otherwise.
func (n *Network) RequestSite(ctx context.Context, accountID int64, slug string) (*Site, error) {
	return n.CreateSite(ctx, NewSite{Slug: slug, OwnerID: accountID, Plan: PlanPersonal, Requested: true})
}

// Grant is a subscription of an account with room for more sites than
// it pays for now (a plan whose sites_per_grant is above 1).
type Grant struct {
	SubscriptionID int64
	Plan, PlanName string
	Used, Total    int // its sites that are not deleted, and the plan's
}

// Grants returns the active subscriptions of an account that have room
// for another site, oldest first.
func (n *Network) Grants(ctx context.Context, accountID int64) ([]Grant, error) {
	rows, err := n.db.Query(ctx, `SELECT sub.id, p.slug, p.name,
			(SELECT count(*) FROM sites x WHERE x.subscription_id = sub.id AND x.status <> 'deleted')::int AS used, p.sites_per_grant
		FROM subscriptions sub JOIN plans p ON p.slug = sub.plan
		WHERE sub.account_id = $1 AND sub.state = 'active' AND NOT sub.manual AND p.sites_per_grant > 1
		ORDER BY sub.id`, accountID)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) {
		var g Grant
		err := r.Scan(&g.SubscriptionID, &g.Plan, &g.PlanName, &g.Used, &g.Total)
		return g, err
	})
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, g := range all {
		if g.Used < g.Total {
			out = append(out, g)
		}
	}
	return out, nil
}

// AddGrantSite creates a site on a subscription of the account that has
// room for it: it opens at once, on the subscription's plan, with no
// payment and no approval unless the plan is approved by hand. ErrState
// when the subscription is not the account's, not active, or full.
func (n *Network) AddGrantSite(ctx context.Context, accountID, subscriptionID int64, slug string) (*Site, error) {
	sub, err := n.Subscription(ctx, subscriptionID)
	if errors.Is(err, store.ErrNotFound) || err == nil && sub.AccountID != accountID {
		return nil, fmt.Errorf("%w: no subscription %d of the account", ErrState, subscriptionID)
	} else if err != nil {
		return nil, err
	}
	return n.CreateSite(ctx, NewSite{Slug: slug, OwnerID: accountID, Plan: sub.Plan, Grant: sub.ID})
}

// CreateSite provisions a site: the row (with its sealed secret and
// default domain <slug>.<SitesDomain>), the schema site_<id> with every
// site migration, and its directory. A failure in any step undoes the
// earlier ones. The site is then active, or pending when its plan is
// approved by hand or it was requested.
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
		if ns.Requested {
			// Locking the account serialises its requests, so two at once
			// cannot both see that it has no site yet.
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id = $1 FOR UPDATE`, ns.OwnerID).Scan(&status); err != nil {
				return err
			}
			if status != "active" {
				return fmt.Errorf("%w: the account is %s", ErrState, status)
			}
			// The plan's sites per grant: one Personal site per account.
			var has int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM sites WHERE owner_id = $1 AND plan = $2 AND status <> 'deleted'`,
				ns.OwnerID, plan.Slug).Scan(&has); err != nil {
				return err
			}
			if has >= plan.SitesPerGrant {
				return fmt.Errorf("%w: the account already has its %s sites", ErrState, plan.Name)
			}
		}
		var grant *int64
		if ns.Grant != 0 {
			// Locking the subscription serialises the sites added to it.
			var room bool
			err := tx.QueryRow(ctx, `SELECT sub.account_id = $2 AND sub.plan = $3 AND sub.state = 'active' AND NOT sub.manual
					AND (SELECT count(*) FROM sites x WHERE x.subscription_id = sub.id AND x.status <> 'deleted') < $4
				FROM subscriptions sub WHERE sub.id = $1 FOR UPDATE`, ns.Grant, ns.OwnerID, plan.Slug, plan.SitesPerGrant).Scan(&room)
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !room {
				return fmt.Errorf("%w: subscription %d has no room for another %s site", ErrState, ns.Grant, plan.Name)
			} else if err != nil {
				return err
			}
			grant = &ns.Grant
		}
		if err := tx.QueryRow(ctx, `SELECT nextval('sites_id_seq')`).Scan(&id); err != nil {
			return err
		}
		sealed, err := n.wrapSecret(id, newSecret())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO sites (id, kind, slug, schema_name, owner_id, plan, status, secret, rp_id, subscription_id)
			VALUES ($1, 'tenant', $2, $3, $4, $5, 'provisioning', $6, $7, $8)`,
			id, ns.Slug, db.SiteSchema(id), ns.OwnerID, plan.Slug, sealed, host, grant); err != nil {
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
	if plan.HandApproved || ns.Requested {
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

// SetNextAnnounce records when a site's next scheduled post is due, read
// from its schema; nil when none is.
func (n *Network) SetNextAnnounce(ctx context.Context, siteID int64, at *time.Time) error {
	_, err := n.db.Exec(ctx, `UPDATE sites SET next_announce_at = $2 WHERE id = $1`, siteID, at)
	return err
}

// PostponeAnnounce moves a due wake-up d later, after a site failed to
// load, so a broken site is not loaded every minute.
func (n *Network) PostponeAnnounce(ctx context.Context, siteID int64, d time.Duration) error {
	_, err := n.db.Exec(ctx, `UPDATE sites SET next_announce_at = now() + $2::bigint * interval '1 second'
		WHERE id = $1 AND next_announce_at <= now()`, siteID, int64(d/time.Second))
	return err
}

// DueAnnouncements lists the active sites with a scheduled post due, the
// longest waiting first.
func (n *Network) DueAnnouncements(ctx context.Context, limit int) ([]int64, error) {
	rows, err := n.db.Query(ctx, `SELECT id FROM sites WHERE status = 'active' AND next_announce_at <= now()
		ORDER BY next_announce_at, id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// SetWantsCron records whether an enabled plugin of a site handles
// cron.hourly, so that the hourly cron loads it when it is not loaded.
func (n *Network) SetWantsCron(ctx context.Context, siteID int64, wants bool) error {
	_, err := n.db.Exec(ctx, `UPDATE sites SET wants_cron = $2 WHERE id = $1 AND wants_cron <> $2`, siteID, wants)
	return err
}

// CronSites lists the active sites whose plugins want cron.hourly.
func (n *Network) CronSites(ctx context.Context) ([]int64, error) {
	rows, err := n.db.Query(ctx, `SELECT id FROM sites WHERE status = 'active' AND wants_cron ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// SchemaBytes is the disk space of a site's tables, with their indexes and
// TOAST data.
func (n *Network) SchemaBytes(ctx context.Context, schema string) (int64, error) {
	var b int64
	err := n.db.QueryRow(ctx, `SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0)::bigint
		FROM pg_class c JOIN pg_namespace ns ON ns.oid = c.relnamespace
		WHERE ns.nspname = $1 AND c.relkind IN ('r', 'm')`, schema).Scan(&b)
	return b, err
}

// SetStorage records the space a site uses (schema and directory),
// measured while its count was before: what uploads reserved or deletions
// released since is kept (an upload finished during the measurement
// counts twice until the next one, never zero times).
func (n *Network) SetStorage(ctx context.Context, siteID, bytes, before int64) error {
	_, err := n.db.Exec(ctx, `UPDATE sites SET storage_bytes = greatest($2 + storage_bytes - $3, 0), storage_checked_at = now()
		WHERE id = $1`, siteID, bytes, before)
	return err
}

// PurgeSSOCodes deletes the single sign-on codes that expired more than a
// day ago, used or not.
func (n *Network) PurgeSSOCodes(ctx context.Context) (int64, error) {
	tag, err := n.db.Exec(ctx, `DELETE FROM sso_codes WHERE expires_at < now() - interval '1 day'`)
	return tag.RowsAffected(), err
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

// ApproveSite opens a pending site; by is the network admin who approved
// it (0 from the CLI).
func (n *Network) ApproveSite(ctx context.Context, id, by int64) error {
	return n.setStatus(ctx, id, []string{StatusPending},
		`status = 'active', approved_by = nullif($3::bigint, 0), approved_at = now()`, by)
}

// RejectSite refuses a pending site, which is deleted: it is no longer
// served, and its data stays until PurgeSite.
func (n *Network) RejectSite(ctx context.Context, id int64) error {
	return n.setStatus(ctx, id, []string{StatusPending}, `status = 'deleted', deleted_at = now()`)
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
