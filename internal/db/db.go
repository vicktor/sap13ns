// Package db opens the PostgreSQL pool and applies embedded migrations.
//
// A single-site install uses Open and Migrate on the public schema. A
// multisite install shares one pool between every site (OpenShared), and
// each site reaches its own schema through a Scoped handle (scoped.go).
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open creates a connection pool and verifies connectivity.
func Open(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(url, maxConns)
	if err != nil {
		return nil, err
	}
	return connect(ctx, cfg)
}

func poolConfig(url string, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "sap13ns"
	// Guard against runaway queries holding connections forever.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "30000"
	return cfg, nil
}

// connect builds the pool and verifies connectivity. The ping is scoped to
// the unscoped schema, which a shared pool requires and a plain pool
// ignores.
func connect(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(withSchema(ctx, unscopedSchema), 10*time.Second)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	return pool, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// A MigrationSet is an ordered list of numbered SQL files (NNNN_name.sql)
// and the table that records, in each schema, which of them were applied.
// Every site schema gets the site set; the network schema also gets the
// set of internal/network, tracked in its own table.
type MigrationSet struct {
	table string
	migs  []migration
}

var tableRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// LoadMigrations reads the *.sql files of dir in fsys. table names the
// tracking table, created on first use.
func LoadMigrations(fsys fs.FS, dir, table string) (*MigrationSet, error) {
	if !tableRe.MatchString(table) {
		return nil, fmt.Errorf("bad migration table name %q", table)
	}
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	set := &MigrationSet{table: table}
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		if !ok || !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("bad migration file name %q", name)
		}
		v, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("bad migration version in %q", name)
		}
		if other, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %q and %q share version %d", other, name, v)
		}
		seen[v] = name
		b, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		set.migs = append(set.migs, migration{version: v, name: name, sql: string(b)})
	}
	sort.Slice(set.migs, func(i, j int) bool { return set.migs[i].version < set.migs[j].version })
	return set, nil
}

// SiteMigrations is the set of a site schema: public on a single-site
// install, the network schema and every site_N schema on a multisite one.
func SiteMigrations() (*MigrationSet, error) {
	return LoadMigrations(migrationsFS, "migrations", "schema_migrations")
}

// migrationLockID is an arbitrary constant used with pg_advisory_lock so that
// several instances starting at once never apply migrations concurrently.
const migrationLockID = 7_201_313

// Migrate applies pending migrations, each inside its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	set, err := SiteMigrations()
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	return set.apply(ctx, conn, log)
}

// apply runs the pending migrations of the set on conn, which holds the
// migration lock and is on the target schema.
func (set *MigrationSet) apply(ctx context.Context, conn *pgxpool.Conn, log *slog.Logger) error {
	table := pgx.Identifier{set.table}.Sanitize()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		version integer PRIMARY KEY,
		name text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	applied := map[int]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM `+table)
	if err != nil {
		return err
	}
	vs, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return err
	}
	for _, v := range vs {
		applied[v] = true
	}

	for _, m := range set.migs {
		if applied[m.version] {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (version, name) VALUES ($1, $2)`, m.version, m.name); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		log.Info("migration applied", "version", m.version, "name", m.name)
	}
	return nil
}
