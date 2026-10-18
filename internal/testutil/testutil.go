// Package testutil provides a migrated PostgreSQL database for tests.
//
// Tests use SAP13NS_TEST_DATABASE_URL (default: the local socket database
// sap13ns_test) and are skipped when it is unreachable. The schema is dropped
// and recreated, so never point it at real data.
package testutil

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/db"
	"sap13ns/internal/store"
)

var mu sync.Mutex

func URL() string {
	if u := os.Getenv("SAP13NS_TEST_DATABASE_URL"); u != "" {
		return u
	}
	return "postgres:///sap13ns_test?host=/tmp"
}

// Store returns a store over a freshly reset database. Tests using it must
// not run in parallel with each other.
func Store(t testing.TB) *store.Store {
	t.Helper()
	mu.Lock()
	t.Cleanup(mu.Unlock)
	ctx := context.Background()
	pool, err := db.Open(ctx, URL(), 4)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	t.Cleanup(pool.Close)
	reset(t, pool)
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
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
