// Package network is the control plane of a multisite install: the network
// schema (sites, domains, members, plans, network admins), the provisioning
// of site schemas and directories, and the sealed site secrets.
//
// The network portal is itself a site, in the schema "network"; its users
// are the network accounts. All SQL of the network tables lives in this
// package and runs through a db.Scoped handle on that schema. Every
// mutation notifies the channel sap13ns_network when it commits, with the
// site id as payload ("" for changes to the whole network), so running
// servers reload their host table.
package network

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/idna"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

// Schema is the schema of the network tables and of the portal site.
const Schema = "network"

// Channel is the PostgreSQL notification channel of network changes.
const Channel = "sap13ns_network"

var (
	// ErrInvalid wraps input the network refuses, such as a reserved slug.
	ErrInvalid = errors.New("invalid")
	// ErrState means the change is not allowed in the current state, such
	// as resuming a site that is not suspended.
	ErrState = errors.New("not allowed in the current state")
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate creates the network schema if needed and applies the site
// migrations (for the portal) and then the network migrations to it.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	h, err := db.Scope(pool, Schema)
	if err != nil {
		return err
	}
	if _, err := h.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS network`); err != nil {
		// Two processes starting at once can both try to create it.
		if pg, ok := errors.AsType[*pgconn.PgError](err); !ok || pg.Code != "23505" && pg.Code != "42P06" {
			return err
		}
	}
	site, err := db.SiteMigrations()
	if err != nil {
		return err
	}
	if err := db.MigrateSchema(ctx, pool, Schema, site, log); err != nil {
		return err
	}
	set, err := db.LoadMigrations(migrationsFS, "migrations", "network_migrations")
	if err != nil {
		return err
	}
	return db.MigrateSchema(ctx, pool, Schema, set, log)
}

// Network manages the sites of a multisite install.
type Network struct {
	pool        *pgxpool.Pool // shared, for the site schemas
	db          *db.Scoped    // the network schema
	keys        *auth.Keys    // seals the site secrets
	dataDir     string        // site directories are dataDir/sites/<id>
	sitesDomain string
	baseHost    string
	grace       time.Duration // of a failed renewal before a paid site is held
	log         *slog.Logger
	now         func() time.Time // the registry's clock once it runs

	// Custom domains (verify.go): the resolver of their DNS checks, what
	// they must point to, and the key of their verification tokens.
	resolver    Resolver
	target      string
	targetAddrs []netip.Addr
	domainKey   []byte

	// hook runs after each provisioning step; tests make it fail.
	hook func(step string) error
}

// New returns the network of a shared pool (db.OpenShared) whose network
// schema was migrated. cfg is the network's configuration: multisite, with
// the master SAP13NS_SECRET_KEY.
func New(pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) (*Network, error) {
	if !cfg.Multisite {
		return nil, errors.New("network: SAP13NS_MULTISITE is off")
	}
	if len(cfg.SecretKey) != 32 {
		return nil, errors.New("network: SAP13NS_SECRET_KEY is required")
	}
	h, err := db.Scope(pool, Schema)
	if err != nil {
		return nil, err
	}
	n := &Network{
		pool: pool, db: h, keys: sealKeys(cfg.SecretKey), dataDir: cfg.DataDir,
		sitesDomain: cfg.SitesDomain, baseHost: cfg.BaseURL.Hostname(), grace: graceOf(cfg.Stripe), log: log, now: time.Now,
		resolver: net.DefaultResolver, target: cmp.Or(cfg.DomainTarget, cfg.BaseURL.Hostname()),
		targetAddrs: slices.Clone(cfg.DomainAddresses), domainKey: subKey(cfg.SecretKey, "sap13ns:domain-verification"),
	}
	return n, nil
}

// DB is the network schema, for the portal site's store.
func (n *Network) DB() *db.Scoped { return n.db }

// mutate runs fn in a transaction that notifies Channel about siteID (0:
// the whole network) when it commits.
func (n *Network) mutate(ctx context.Context, siteID int64, fn func(pgx.Tx) error) error {
	tx, err := n.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return mapErr(err)
	}
	payload := ""
	if siteID > 0 {
		payload = strconv.FormatInt(siteID, 10)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, payload); err != nil {
		return err
	}
	return mapErr(tx.Commit(ctx))
}

// restoredPrefix starts the notification payload of a site whose content
// a backup restore replaced.
const restoredPrefix = "restored:"

// SiteRestored tells the running servers that a restore from the command
// line replaced the content of a site: a server that has it loaded drops
// its caches and loads it again.
func (n *Network) SiteRestored(ctx context.Context, siteID int64) error {
	_, err := n.db.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, restoredPrefix+strconv.FormatInt(siteID, 10))
	return err
}

// mapErr turns pgx and PostgreSQL errors into the store's: a missing row or
// a dangling reference is ErrNotFound, a unique violation ErrConflict.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if pg, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch pg.Code {
		case "23505":
			return fmt.Errorf("%w: %s", store.ErrConflict, pg.ConstraintName)
		case "23503":
			return fmt.Errorf("%w: %s", store.ErrNotFound, pg.ConstraintName)
		}
	}
	return err
}

// CanonicalHost returns the form of a host name the network stores and
// routes: an A-label in lower case, without trailing dot. A port, an IP
// address or an invalid name is refused.
func CanonicalHost(host string) (string, error) {
	h, err := idna.Lookup.ToASCII(strings.TrimSuffix(host, "."))
	if err != nil || !config.ValidHost(h) {
		return "", fmt.Errorf("%w: host name %q", ErrInvalid, host)
	}
	return h, nil
}
