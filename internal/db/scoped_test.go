package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/db"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// sites migrates the site schemas named and gives each one user named
// after its schema ("u_site_1"), so a read shows where it went.
func sites(t *testing.T, names ...string) *pgxpool.Pool {
	t.Helper()
	pool := testutil.Schemas(t)
	for _, n := range names {
		st := testutil.SiteStore(t, pool, n)
		if _, err := st.CreateUser(context.Background(), store.NewUser{
			Username: owner(n), Email: n + "@example.com", Role: "admin", PasswordHash: "x",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func owner(schema string) string { return "u_" + schema }

func shared(t *testing.T, maxConns int32) *pgxpool.Pool {
	t.Helper()
	p, err := db.OpenShared(context.Background(), testutil.URL(), maxConns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func scope(t *testing.T, p *pgxpool.Pool, schema string) *db.Scoped {
	t.Helper()
	s, err := db.Scope(p, schema)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// where reports the schema a querier runs in and the users it sees.
func where(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (schema, users string, err error) {
	err = q.QueryRow(ctx, `SELECT current_schema(), (SELECT coalesce(string_agg(username, ',' ORDER BY id), '') FROM users)`).
		Scan(&schema, &users)
	return schema, users, err
}

func expectSite(ctx context.Context, s *db.Scoped) error {
	schema, users, err := where(ctx, s)
	if err != nil {
		return err
	}
	if schema != s.Schema() || users != owner(s.Schema()) {
		return fmt.Errorf("%s handle ran in %s and saw users %q", s.Schema(), schema, users)
	}
	return nil
}

// 1. Two sites interleaved from 32 goroutines on one or two connections:
// every statement runs in its own schema and sees only its own rows.
func TestScopedInterleaved(t *testing.T) {
	sites(t, "site_1", "site_2")
	for _, maxConns := range []int32{1, 2} {
		t.Run(fmt.Sprintf("MaxConns=%d", maxConns), func(t *testing.T) {
			p := shared(t, maxConns)
			handles := []*db.Scoped{scope(t, p, "site_1"), scope(t, p, "site_2")}
			var wg sync.WaitGroup
			errs := make(chan error, 32)
			for g := range 32 {
				s := handles[g%2]
				st := store.New(s)
				wg.Go(func() {
					errs <- func() error {
						ctx := context.Background()
						for i := range 20 {
							if err := expectSite(ctx, s); err != nil {
								return err
							}
							users, total, err := st.ListUsers(ctx, store.UserListQuery{Limit: 10})
							if err != nil {
								return err
							}
							if total != 1 || users[0].Username != owner(s.Schema()) {
								return fmt.Errorf("%s: ListUsers = %d users, first %q", s.Schema(), total, users[0].Username)
							}
							bio := fmt.Sprintf("%s-%d-%d", s.Schema(), g, i)
							if _, err := s.Exec(ctx, `UPDATE users SET bio = $1`, bio); err != nil {
								return err
							}
							if err := st.InTx(ctx, func(tx pgx.Tx) error {
								if _, err := tx.Exec(ctx, `UPDATE users SET website = $1`, bio); err != nil {
									return err
								}
								var schema, site string
								if err := tx.QueryRow(ctx, `SELECT current_schema(), website FROM users`).Scan(&schema, &site); err != nil {
									return err
								}
								if schema != s.Schema() || site != bio {
									return fmt.Errorf("%s: transaction in %s read %q", s.Schema(), schema, site)
								}
								return nil
							}); err != nil {
								return err
							}
						}
						return nil
					}()
				})
			}
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Error(err)
				}
			}
			// Each site's rows were written only by its own goroutines.
			for _, s := range handles {
				var bio string
				if err := s.QueryRow(context.Background(), `SELECT bio FROM users`).Scan(&bio); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(bio, s.Schema()+"-") {
					t.Errorf("%s: bio %q was written by another site", s.Schema(), bio)
				}
			}
		})
	}
}

// 2. Statements on the shared pool without a scope fail closed, and the
// refused connection goes back to the pool unharmed.
func TestUnscopedFailsClosed(t *testing.T) {
	sites(t, "site_1")
	p := shared(t, 1)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `SELECT 1`); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Exec: err = %v", err)
	}
	if _, err := p.Query(ctx, `SELECT 1`); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Query: err = %v", err)
	}
	var n int
	if err := p.QueryRow(ctx, `SELECT 1`).Scan(&n); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("QueryRow: err = %v", err)
	}
	if _, err := p.Begin(ctx); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Begin: err = %v", err)
	}
	if err := p.Ping(ctx); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Ping: err = %v", err)
	}
	if _, err := p.Acquire(ctx); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Acquire: err = %v", err)
	}
	// The pool satisfies store.DB, so a store built on it by mistake fails too.
	if _, err := store.New(p).CountUsers(ctx); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("store on the raw pool: err = %v", err)
	}

	// Its only connection was refused six times and still serves a site.
	s := scope(t, p, "site_1")
	if err := expectSite(ctx, s); err != nil {
		t.Fatal(err)
	}
	// Using it for a site did not leave the pool scoped.
	if _, err := p.Exec(ctx, `SELECT 1`); !errors.Is(err, db.ErrUnscoped) {
		t.Errorf("Exec after a scoped statement: err = %v", err)
	}
	if st := p.Stat(); st.TotalConns() != 1 {
		t.Errorf("TotalConns = %d, want the one connection kept", st.TotalConns())
	}

	plain, err := db.Open(ctx, testutil.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := db.Scope(plain, "site_1"); err == nil {
		t.Error("Scope accepted a pool from Open, whose search_path is the server default")
	}
	for _, bad := range []string{"", "public", "site_0", "site_01", "site_1;", `site_1", "public`, "Site_1", "network2", "pg_catalog",
		"site_1234567890123456789"} {
		if _, err := db.Scope(p, bad); err == nil {
			t.Errorf("Scope accepted schema %q", bad)
		}
	}
}

// 3. A transaction keeps its schema while the other site runs on another
// connection, and leaves nothing behind after rollback, an error, a
// savepoint or SET LOCAL.
func TestScopedTransactions(t *testing.T) {
	sites(t, "site_1", "site_2")
	ctx := context.Background()

	t.Run("concurrent", func(t *testing.T) {
		p := shared(t, 2)
		a, b := scope(t, p, "site_1"), scope(t, p, "site_2")
		tx, err := a.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO users (username, email, role, password_hash) VALUES ('in_tx', 'tx@example.com', 'author', 'x')`); err != nil {
			t.Fatal(err)
		}
		// B can only use the second connection while the transaction holds the first.
		for range 20 {
			if err := expectSite(ctx, b); err != nil {
				t.Fatal(err)
			}
		}
		schema, users, err := where(ctx, tx)
		if err != nil || schema != "site_1" || users != owner("site_1")+",in_tx" {
			t.Fatalf("inside the transaction: schema %s, users %q, err %v", schema, users, err)
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		for range 10 {
			for _, s := range []*db.Scoped{a, b} {
				if err := expectSite(ctx, s); err != nil {
					t.Fatal(err)
				}
			}
		}
	})

	// One connection, so every statement below reuses the one the
	// transaction ran on, with the schema cached by PrepareConn.
	t.Run("session state after the transaction", func(t *testing.T) {
		p := shared(t, 1)
		a, b := scope(t, p, "site_1"), scope(t, p, "site_2")
		st := store.New(a)
		err := st.InTx(ctx, func(tx pgx.Tx) error {
			for _, q := range []string{`SET LOCAL statement_timeout = '120s'`, `SET LOCAL search_path TO site_2`} {
				if _, err := tx.Exec(ctx, q); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		// The transaction's settings ended with it, so the cached schema is right.
		var timeout string
		if err := a.QueryRow(ctx, `SHOW statement_timeout`).Scan(&timeout); err != nil || timeout != "15s" {
			t.Fatalf("statement_timeout after the transaction = %q (%v), want the pool's 15s", timeout, err)
		}
		if err := expectSite(ctx, a); err != nil {
			t.Fatal(err)
		}

		// A failed statement aborts the transaction; after rollback the
		// connection is back on its site.
		tx, err := a.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT 1/0`); err == nil {
			t.Fatal("division by zero succeeded")
		}
		tx.Rollback(ctx)
		if err := expectSite(ctx, a); err != nil {
			t.Fatal(err)
		}

		// A rolled-back savepoint keeps the outer transaction in its schema.
		tx, err = b.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sp.Exec(ctx, `SET LOCAL search_path TO site_1`); err != nil {
			t.Fatal(err)
		}
		if err := sp.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if schema, _, err := where(ctx, tx); err != nil || schema != "site_2" {
			t.Fatalf("after the savepoint: schema %s, err %v", schema, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for _, s := range []*db.Scoped{a, b, a} {
			if err := expectSite(ctx, s); err != nil {
				t.Fatal(err)
			}
		}
	})
}

// 4. The same SQL text on the same connection, first for one site and then
// for the other, returns the second site's rows: PostgreSQL re-parses a
// cached prepared statement when search_path changes.
func TestCachedStatementFollowsSchema(t *testing.T) {
	sites(t, "site_1", "site_2")
	p := shared(t, 1)
	if mode := p.Config().ConnConfig.DefaultQueryExecMode; mode != pgx.QueryExecModeCacheStatement {
		t.Fatalf("query exec mode %v: the test needs the statement cache", mode)
	}
	ctx := context.Background()
	a, b := scope(t, p, "site_1"), scope(t, p, "site_2")
	const q = `SELECT username FROM users ORDER BY id`
	read := func(s *db.Scoped) []string {
		t.Helper()
		rows, err := s.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		names, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	for _, s := range []*db.Scoped{a, a, a, b, a, b, b} {
		if got := read(s); len(got) != 1 || got[0] != owner(s.Schema()) {
			t.Fatalf("%s read %v", s.Schema(), got)
		}
		u, err := store.New(s).UserByUsername(ctx, owner(s.Schema()))
		if err != nil || u.Email != s.Schema()+"@example.com" {
			t.Fatalf("%s: UserByUsername = %+v, %v", s.Schema(), u, err)
		}
		if _, err := store.New(s).UserByUsername(ctx, owner(other(s.Schema()))); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s found the other site's user: %v", s.Schema(), err)
		}
	}
}

func other(schema string) string {
	if schema == "site_1" {
		return "site_2"
	}
	return "site_1"
}

// 5. A user left in public by a single-site install is invisible to sites,
// even to a site whose schema lacks the table.
func TestPublicInvisible(t *testing.T) {
	pool := sites(t, "site_1")
	ctx := context.Background()
	plain, err := db.Open(ctx, testutil.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if err := db.Migrate(ctx, plain, testutil.Log()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(plain).CreateUser(ctx, store.NewUser{Username: "sentinel", Email: "s@example.com", Role: "admin", PasswordHash: "x"}); err != nil {
		t.Fatal(err)
	}

	s := scope(t, pool, "site_1")
	if _, err := store.New(s).UserByUsername(ctx, "sentinel"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("site_1 found the public sentinel: %v", err)
	}
	var path []string
	if err := s.QueryRow(ctx, `SELECT current_schemas(false)::text[]`).Scan(&path); err != nil || len(path) != 1 || path[0] != "site_1" {
		t.Fatalf("search_path = %v (%v), want only site_1", path, err)
	}

	// An empty site schema: the query fails instead of reading public.users.
	if _, err := s.Exec(ctx, `CREATE SCHEMA site_9`); err != nil {
		t.Fatal(err)
	}
	var n int
	err = scope(t, pool, "site_9").QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if pg, ok := errors.AsType[*pgconn.PgError](err); !ok || pg.Code != "42P01" {
		t.Fatalf("users in an empty schema: n = %d, err = %v, want undefined_table", n, err)
	}

	// A schema that does not exist (a deleted site): nothing is created
	// anywhere, in particular not in pg_temp, which later statements of
	// other sites on the connection would search.
	missing := scope(t, pool, "site_8")
	_, err = missing.Exec(ctx, `CREATE TABLE IF NOT EXISTS stray (id int)`)
	if pg, ok := errors.AsType[*pgconn.PgError](err); !ok || pg.Code != "3F000" {
		t.Fatalf("CREATE TABLE in a missing schema: err = %v, want invalid_schema_name", err)
	}
	var temp int
	if err := missing.QueryRow(ctx, `SELECT pg_my_temp_schema()::int`).Scan(&temp); err != nil || temp != 0 {
		t.Fatalf("a temporary schema exists (%d, %v)", temp, err)
	}

	// A connection that bypasses PrepareConn starts on a schema that does
	// not exist, so it finds no table at all.
	raw, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close(ctx)
	err = raw.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	if pg, ok := errors.AsType[*pgconn.PgError](err); !ok || pg.Code != "42P01" {
		t.Fatalf("users on a fresh connection: n = %d, err = %v, want undefined_table", n, err)
	}
	var schema *string
	if err := raw.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || schema != nil {
		t.Fatalf("current_schema of a fresh connection = %v (%v), want none", schema, err)
	}
}

// MigrateSchema migrates one schema once even when several processes start
// together, keeps sets apart by their tracking tables, and never runs on a
// schema that does not exist or through a plain pool.
func TestMigrateSchema(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	s := scope(t, pool, "site_4")
	if _, err := s.Exec(ctx, `CREATE SCHEMA site_4`); err != nil {
		t.Fatal(err)
	}
	set, err := db.SiteMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- db.MigrateSchema(ctx, pool, "site_4", set, testutil.Log()) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var applied int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied < 9 {
		t.Fatalf("schema_migrations has %d rows", applied)
	}

	// A second set in the same schema, tracked in its own table.
	extra, err := db.LoadMigrations(fstest.MapFS{
		"m/0001_one.sql": {Data: []byte(`CREATE TABLE extra_one (id int)`)},
		"m/0002_two.sql": {Data: []byte(`CREATE TABLE extra_two (id int REFERENCES users ON DELETE CASCADE)`)},
	}, "m", "extra_migrations")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.MigrateSchema(ctx, pool, "site_4", extra, testutil.Log()); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.QueryRow(ctx, `SELECT (SELECT count(*) FROM extra_migrations) + (SELECT count(*) FROM extra_two)`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("extra set: %d, %v", n, err)
	}
	var again int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&again); err != nil || again != applied {
		t.Fatalf("the extra set changed schema_migrations: %d -> %d (%v)", applied, again, err)
	}

	if err := db.MigrateSchema(ctx, pool, "site_5", set, testutil.Log()); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing schema: err = %v", err)
	}
	if err := db.MigrateSchema(ctx, pool, "public", set, testutil.Log()); err == nil {
		t.Fatal("MigrateSchema accepted public")
	}
	plain, err := db.Open(ctx, testutil.URL(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if err := db.MigrateSchema(ctx, plain, "site_4", set, testutil.Log()); err == nil {
		t.Fatal("MigrateSchema accepted a plain pool")
	}

	// A failing migration rolls back alone and is retried next time.
	broken, err := db.LoadMigrations(fstest.MapFS{
		"m/0003_bad.sql": {Data: []byte(`CREATE TABLE extra_three (id int); SELECT 1/0`)},
	}, "m", "extra_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateSchema(ctx, pool, "site_4", broken, testutil.Log()); err == nil || !strings.Contains(err.Error(), "0003_bad.sql") {
		t.Fatalf("broken migration: err = %v", err)
	}
	if err := s.QueryRow(ctx, `SELECT count(*) FROM extra_three`).Scan(&n); err == nil {
		t.Fatal("the failed migration left its table")
	}
}

func TestLoadMigrations(t *testing.T) {
	set, err := db.SiteMigrations()
	if err != nil || set == nil {
		t.Fatal(err)
	}
	ok := fstest.MapFS{"m/0002_b.sql": {Data: []byte("SELECT 2")}, "m/0001_a.sql": {Data: []byte("SELECT 1")}}
	if _, err := db.LoadMigrations(ok, "m", "network_migrations"); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		fs    fstest.MapFS
		table string
	}{
		"bad name":        {fstest.MapFS{"m/init.sql": {}}, "t_migrations"},
		"bad version":     {fstest.MapFS{"m/v1_init.sql": {}}, "t_migrations"},
		"not sql":         {fstest.MapFS{"m/0001_init.txt": {}}, "t_migrations"},
		"duplicate":       {fstest.MapFS{"m/0001_a.sql": {}, "m/1_b.sql": {}}, "t_migrations"},
		"bad table":       {ok, `x"; DROP TABLE users; --`},
		"qualified table": {ok, "public.t"},
		"missing dir":     {ok, ""},
	} {
		dir := "m"
		if name == "missing dir" {
			dir, tc.table = "nope", "t_migrations"
		}
		if _, err := db.LoadMigrations(tc.fs, dir, tc.table); err == nil {
			t.Errorf("%s: LoadMigrations succeeded", name)
		}
	}
}
