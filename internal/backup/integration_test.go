package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// scratchDB creates an empty database next to the one SAP13NS_TEST_DATABASE_URL
// names and returns a URL for it. That database itself is never modified.
func scratchDB(t *testing.T, ctx context.Context, admin *pgx.Conn, base string) string {
	t.Helper()
	b := make([]byte, 6)
	rand.Read(b)
	name := "sap13ns_backup_test_" + hex.EncodeToString(b)
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
		t.Skipf("cannot create a scratch database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
	})
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		return u.String()
	}
	return base + " dbname=" + name
}

func TestIntegrationPostgres(t *testing.T) {
	base := os.Getenv("SAP13NS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("set SAP13NS_TEST_DATABASE_URL to run against PostgreSQL with the real pg_dump and pg_restore")
	}
	for _, bin := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("test database unavailable: %v", err)
	}
	// Registered first, so it runs after the scratch databases are dropped.
	t.Cleanup(func() { admin.Close(context.Background()) })

	dsn := scratchDB(t, ctx, admin, base)
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `CREATE TABLE posts (id bigserial PRIMARY KEY, title text NOT NULL);
		INSERT INTO posts (title) VALUES ('uno'), ('dos'), ('tres ñ');
		CREATE INDEX posts_title ON posts (title);`); err != nil {
		t.Fatal(err)
	}

	uploads, want := testSite(t)
	path, m, err := Create(ctx, Options{DatabaseURL: dsn, UploadsDir: uploads, OutDir: t.TempDir(),
		Passphrase: testPassphrase, AppVersion: "test", Log: testLog(t), kdf: testKDF})
	if err != nil {
		t.Fatal(err)
	}
	if m.PGVersion == "" || m.DBSize <= 0 || m.DB.Size <= 0 || m.FileCount != len(want) {
		t.Errorf("manifest = %+v", m)
	}
	if _, err := Verify(path, testPassphrase); err != nil {
		t.Fatal(err)
	}

	// Lose the data, then restore over the damaged database.
	if _, err := conn.Exec(ctx, `DROP TABLE posts; CREATE TABLE intruder (x int)`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "uploads")
	ro := RestoreOptions{DatabaseURL: dsn, UploadsDir: target, Passphrase: testPassphrase, Log: testLog(t)}
	if err := Restore(ctx, path, ro); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("restore over a non-empty database: err = %v, want ErrNotEmpty", err)
	}
	ro.Force = true
	if err := Restore(ctx, path, ro); err != nil {
		t.Fatal(err)
	}
	checkPosts(t, ctx, dsn)
	if got := readTree(t, target); len(got) != len(want) {
		t.Errorf("restored %d uploads, want %d", len(got), len(want))
	}

	// A fresh, empty database needs no Force.
	fresh := scratchDB(t, ctx, admin, base)
	if err := Restore(ctx, path, RestoreOptions{DatabaseURL: fresh, SkipUploads: true, Passphrase: testPassphrase}); err != nil {
		t.Fatal(err)
	}
	checkPosts(t, ctx, fresh)
}

func checkPosts(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, _ := conn.Query(ctx, `SELECT title FROM posts ORDER BY id`)
	titles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || strings.Join(titles, ",") != "uno,dos,tres ñ" {
		t.Errorf("posts after restore = %v, %v", titles, err)
	}
	var next int64
	if err := conn.QueryRow(ctx, `SELECT nextval('posts_id_seq')`).Scan(&next); err != nil || next != 4 {
		t.Errorf("sequence after restore = %d, %v", next, err)
	}
}
