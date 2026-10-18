// Package testutil provides a migrated PostgreSQL database for tests.
//
// Tests use SAP13NS_TEST_DATABASE_URL (default: the local socket database
// sap13ns_test) and are skipped when it is unreachable. The schema is dropped
// and recreated, so never point it at real data. A PostgreSQL advisory lock
// serialises tests across packages, which go test runs in parallel processes.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

var mu sync.Mutex

// lockKey is the advisory lock held while a test uses the database.
const lockKey = 0x5a913e5

func URL() string {
	if u := os.Getenv("SAP13NS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres:///sap13ns_test?host=/tmp"
}

// Log discards everything.
func Log() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// lock takes the database for the rest of the test and returns the
// connection holding the lock.
func lock(t testing.TB) *pgx.Conn {
	t.Helper()
	mu.Lock()
	t.Cleanup(mu.Unlock)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, URL())
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) }) // releases the lock
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey); err != nil {
		t.Fatal(err)
	}
	return conn
}

// Store returns a store over a freshly reset database. Tests using it must
// not run in parallel with each other.
func Store(t testing.TB) *store.Store {
	t.Helper()
	lock(t)
	ctx := context.Background()
	pool, err := db.Open(ctx, URL(), 4)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	reset(t, pool)
	if err := db.Migrate(ctx, pool, Log()); err != nil {
		t.Fatal(err)
	}
	return store.New(pool)
}

func reset(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	if err != nil {
		t.Fatal(err)
	}
}

// Schemas returns a shared multisite pool (db.OpenShared) over a database
// without the network and site_N schemas of earlier tests; public is reset
// as Store does. Like Store, it holds the database until the test ends.
func Schemas(t testing.TB) *pgxpool.Pool {
	t.Helper()
	conn := lock(t)
	ctx := context.Background()
	rows, err := conn.Query(ctx, `SELECT nspname::text FROM pg_namespace WHERE nspname = 'network' OR nspname ~ '^site_[0-9]+$'`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if _, err := conn.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{n}.Sanitize()+` CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	pool, err := db.OpenShared(ctx, URL(), 8)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// SiteStore creates schema on a pool from Schemas, applies the site
// migrations to it and returns its store.
func SiteStore(t testing.TB, pool *pgxpool.Pool, schema string) *store.Store {
	t.Helper()
	ctx := context.Background()
	h, err := db.Scope(pool, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	set, err := db.SiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateSchema(ctx, pool, schema, set, Log()); err != nil {
		t.Fatal(err)
	}
	return store.New(h)
}
