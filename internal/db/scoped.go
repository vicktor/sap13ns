package db

// The shared pool of a multisite install.
//
// Every site of a network has its own schema (network, site_1, site_2, ...)
// in one database, and all sites share one pool. The search_path of a
// connection decides which site a statement reads, so it is never left to
// the caller:
//
//   - A Scoped handle puts its schema on the context of every statement,
//     overriding anything the caller's context carries.
//   - PrepareConn runs on every acquire and points the connection at that
//     schema. It costs one round trip, and only when the connection was
//     last used for another schema. Without a schema it refuses with
//     ErrUnscoped, so a raw pool call, a background context or a forgotten
//     scope fails closed instead of reading the previous site's data.
//   - Connections start on a schema that never exists (sap13ns_unscoped):
//     SQL that bypassed PrepareConn would find no tables at all.
//   - search_path is the site schema and nothing else. A table left in
//     public by a single-site install can never stand in for a site's, and
//     when the schema is missing a CREATE fails. (Listing pg_temp would be
//     worse: with the schema missing, tables would be created there.)
//
// Site SQL must not change other session state, which would survive into
// the next site's statements on that connection: SET without LOCAL, RESET
// ALL, DISCARD, SET ROLE, temporary tables. lint_test.go rejects them in the
// SQL packages. PgBouncer works in session mode only, never in transaction
// mode.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnscoped is returned for a statement on the shared pool that did not
// go through a Scoped handle.
var ErrUnscoped = errors.New("db: statement on the shared pool without a site schema")

// unscopedSchema is the startup search_path of shared connections. It is
// never created.
const unscopedSchema = "sap13ns_unscoped"

// schemaRe matches the schemas a Scoped handle may use: the network
// portal's and the sites', named after their id.
var schemaRe = regexp.MustCompile(`^(network|site_[1-9][0-9]{0,17})$`)

// ValidSchema reports whether name is a network or site schema name.
func ValidSchema(name string) bool { return schemaRe.MatchString(name) }

// SiteSchema returns the schema name of site id.
func SiteSchema(id int64) string { return "site_" + strconv.FormatInt(id, 10) }

type schemaKey struct{}

func withSchema(ctx context.Context, schema string) context.Context {
	return context.WithValue(ctx, schemaKey{}, schema)
}

// customKey holds, in the connection's custom data, the schema its
// search_path points at.
const customKey = "sap13ns.schema"

// OpenShared creates the pool every site of a network shares. Statements
// only run through a Scoped handle; anything else gets ErrUnscoped.
func OpenShared(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, maxConns)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = unscopedSchema
	cfg.PrepareConn = prepareConn
	return connect(ctx, cfg)
}

// shared reports whether pool was opened by OpenShared.
func shared(pool *pgxpool.Pool) bool {
	return pool.Config().ConnConfig.RuntimeParams["search_path"] == unscopedSchema
}

// prepareConn points a connection at the schema of the handle acquiring it.
// Returning true with an error gives the connection back to the pool
// untouched; returning false destroys it, because its search_path is then
// unknown.
func prepareConn(ctx context.Context, c *pgx.Conn) (bool, error) {
	want, _ := ctx.Value(schemaKey{}).(string)
	if want == "" {
		return true, ErrUnscoped
	}
	if want != unscopedSchema && !ValidSchema(want) {
		return true, fmt.Errorf("db: invalid schema %q", want)
	}
	m := c.PgConn().CustomData()
	if m[customKey] == want {
		return true, nil
	}
	delete(m, customKey)
	if _, err := c.Exec(ctx, `SELECT pg_catalog.set_config('search_path', $1, false)`, pgx.Identifier{want}.Sanitize()); err != nil {
		return false, err
	}
	m[customKey] = want
	return true, nil
}

// Scoped runs every statement in one schema of the shared pool. It
// satisfies store.DB and analytics.DB. Transactions stay on their
// connection, so they keep the schema they started with.
type Scoped struct {
	pool   *pgxpool.Pool
	schema string
}

// Scope returns the handle of schema on a pool from OpenShared. A plain
// pool is refused: its search_path would be whatever the server default is.
func Scope(pool *pgxpool.Pool, schema string) (*Scoped, error) {
	if !ValidSchema(schema) {
		return nil, fmt.Errorf("db: invalid schema %q", schema)
	}
	if !shared(pool) {
		return nil, errors.New("db: Scope needs a pool from OpenShared")
	}
	return &Scoped{pool: pool, schema: schema}, nil
}

func (s *Scoped) Schema() string { return s.schema }

func (s *Scoped) ctx(ctx context.Context) context.Context { return withSchema(ctx, s.schema) }

func (s *Scoped) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return s.pool.Exec(s.ctx(ctx), sql, args...)
}

func (s *Scoped) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return s.pool.Query(s.ctx(ctx), sql, args...)
}

func (s *Scoped) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return s.pool.QueryRow(s.ctx(ctx), sql, args...)
}

func (s *Scoped) SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults {
	return s.pool.SendBatch(s.ctx(ctx), b)
}

func (s *Scoped) Begin(ctx context.Context) (pgx.Tx, error) {
	return s.pool.Begin(s.ctx(ctx))
}

func (s *Scoped) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	return s.pool.BeginTx(s.ctx(ctx), opts)
}

func (s *Scoped) Ping(ctx context.Context) error {
	return s.pool.Ping(s.ctx(ctx))
}

// MigrateSchema applies the pending migrations of set to schema through a
// pool from OpenShared. The schema must exist: creating it is part of
// provisioning a site. The advisory lock is per schema, so processes
// starting together never migrate one schema twice, but can migrate
// different schemas in parallel.
func MigrateSchema(ctx context.Context, pool *pgxpool.Pool, schema string, set *MigrationSet, log *slog.Logger) error {
	if !ValidSchema(schema) {
		return fmt.Errorf("db: invalid schema %q", schema)
	}
	if !shared(pool) {
		return errors.New("db: MigrateSchema needs a pool from OpenShared")
	}
	conn, err := pool.Acquire(withSchema(ctx, schema))
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1, hashtext($2))`, migrationLockID, schema); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1, hashtext($2))`, migrationLockID, schema)

	// current_schemas lists only schemas that exist: a missing schema
	// stops here instead of failing halfway through the first migration.
	var path []string
	if err := conn.QueryRow(ctx, `SELECT current_schemas(false)::text[]`).Scan(&path); err != nil {
		return err
	}
	if len(path) != 1 || path[0] != schema {
		return fmt.Errorf("db: schema %s does not exist", schema)
	}
	return set.apply(ctx, conn, log.With("schema", schema))
}
