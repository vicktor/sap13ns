package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testDSN = "postgres://app:hunter2-secret@db.internal:5433/sap13ns?sslmode=require"

var testNow = time.Date(2026, 10, 18, 3, 30, 0, 0, time.UTC)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// testLog shows the package's logs with go test -v or on failure.
func testLog(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(t.Output(), nil))
}

// shellQuote quotes s for /bin/sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// writeScript writes an executable shell script standing in for pg_dump or
// pg_restore.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake PostgreSQL tools are shell scripts")
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeTools holds fake pg_dump/pg_restore scripts that record their
// arguments and environment.
type fakeTools struct {
	dir       string
	fixture   []byte
	pgDump    string
	pgRestore string
}

func newFakeTools(t *testing.T) *fakeTools {
	t.Helper()
	ft := &fakeTools{dir: t.TempDir(), fixture: append([]byte("PGDMP\x01\x10\x00"), randBytes(200<<10)...)}
	fixture := filepath.Join(ft.dir, "fixture.dump")
	if err := os.WriteFile(fixture, ft.fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	q := func(name string) string { return shellQuote(filepath.Join(ft.dir, name)) }
	ft.pgDump = writeScript(t, ft.dir, "pg_dump", `printf '%s\n' "$@" > `+q("dump.args")+`
env > `+q("dump.env")+`
exec cat `+q("fixture.dump")+"\n")
	ft.pgRestore = writeScript(t, ft.dir, "pg_restore", `printf '%s\n' "$@" > `+q("restore.args")+`
env > `+q("restore.env")+`
for last; do :; done
cp "$last" `+q("restored.dump")+"\n")
	return ft
}

func (ft *fakeTools) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ft.dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func emptyDB(ctx context.Context, dsn string) (*serverInfo, error) {
	return &serverInfo{Version: "18.0 (Debian 18.0-1.pgdg13+1)", Size: 8_000_000}, nil
}

func busyDB(ctx context.Context, dsn string) (*serverInfo, error) {
	return &serverInfo{Version: "18.0", Size: 9_000_000, Relations: 42}, nil
}

// writeTree creates files from a map of slash paths to contents.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns the regular files below root (symlinks excluded).
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range des {
		names = append(names, d.Name())
	}
	return names
}

// testSite is an uploads directory with what Create must keep and skip.
func testSite(t *testing.T) (uploads string, want map[string]string) {
	t.Helper()
	uploads = filepath.Join(t.TempDir(), "uploads")
	want = map[string]string{
		"2026/10/0a1b2c.jpg":       string(randBytes(150 << 10)),
		"2026/10/0a1b2c-thumb.jpg": string(randBytes(20 << 10)),
		"2026/09/notes.txt":        "hello\n",
		"empty.pdf":                "",
	}
	writeTree(t, uploads, want)
	writeTree(t, uploads, map[string]string{
		".tmp/upload-123":      "in progress",
		"2026/10/half.jpg.tmp": "being written",
	})
	if err := os.Symlink("/etc/passwd", filepath.Join(uploads, "2026", "passwd.jpg")); err != nil {
		t.Fatal(err)
	}
	return uploads, want
}

func TestCreateVerifyListRestore(t *testing.T) {
	for _, pass := range []string{"", testPassphrase} {
		t.Run("encrypted="+boolStr(pass != ""), func(t *testing.T) {
			cleanPGEnv(t)
			t.Setenv("SAP13NS_SECRET_KEY", "must-not-reach-pg_dump")
			ctx := context.Background()
			ft := newFakeTools(t)
			uploads, want := testSite(t)
			out := filepath.Join(t.TempDir(), "backups")
			opts := Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: out, Passphrase: pass, AppVersion: "1.2.3",
				PgDump: ft.pgDump, Now: fixedClock(testNow), Log: testLog(t), probe: emptyDB, kdf: testKDF}

			path, m, err := Create(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			wantName := "sap13ns-20261018-033000.tar.gz"
			if pass != "" {
				wantName += ".enc"
			}
			if filepath.Base(path) != wantName {
				t.Errorf("archive name = %s, want %s", filepath.Base(path), wantName)
			}
			if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("archive mode = %v, %v; want 0600", fi.Mode().Perm(), err)
			}
			if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o700 {
				t.Errorf("backup dir mode = %v, want 0700", fi.Mode().Perm())
			}
			if names := dirNames(t, out); !slices.Equal(names, []string{wantName}) {
				t.Errorf("backup dir holds %v; temporary files left behind?", names)
			}

			// Manifest.
			if m.Format != 1 || m.AppVersion != "1.2.3" || !m.Created.Equal(testNow) || m.Database != "sap13ns" ||
				m.PGVersion != "18.0 (Debian 18.0-1.pgdg13+1)" || m.DBSize != 8_000_000 {
				t.Errorf("manifest header = %+v", m)
			}
			if m.DB.Size != int64(len(ft.fixture)) || m.DB.SHA256 != sha256Hex(ft.fixture) {
				t.Errorf("manifest db = %+v", m.DB)
			}
			var names []string
			total := m.DB.Size
			for _, f := range m.Files {
				names = append(names, f.Name)
				total += f.Size
				if content := want[strings.TrimPrefix(f.Name, "uploads/")]; f.SHA256 != sha256Hex([]byte(content)) {
					t.Errorf("%s: wrong hash", f.Name)
				}
			}
			wantNames := []string{"uploads/2026/09/notes.txt", "uploads/2026/10/0a1b2c-thumb.jpg", "uploads/2026/10/0a1b2c.jpg", "uploads/empty.pdf"}
			if !slices.Equal(names, wantNames) || m.FileCount != 4 || m.TotalBytes != total {
				t.Errorf("manifest files = %v (count %d, total %d)", names, m.FileCount, m.TotalBytes)
			}

			// pg_dump was called with the right flags and no password in argv.
			args := ft.read(t, "dump.args")
			for _, a := range []string{"--format=custom", "--no-owner", "--no-privileges", "--no-password", "--dbname=dbname='sap13ns'"} {
				if !strings.Contains(args, a+"\n") {
					t.Errorf("pg_dump args lack %s:\n%s", a, args)
				}
			}
			if strings.Contains(args, "hunter2") {
				t.Errorf("password on pg_dump's command line:\n%s", args)
			}
			env := ft.read(t, "dump.env")
			for _, kv := range []string{"PGPASSWORD=hunter2-secret", "PGHOST=db.internal", "PGPORT=5433", "PGUSER=app", "PGDATABASE=sap13ns", "PGSSLMODE=require"} {
				if !strings.Contains(env, kv+"\n") {
					t.Errorf("pg_dump env lacks %s", kv)
				}
			}
			if strings.Contains(env, "SAP13NS_") {
				t.Errorf("SAP13NS_* variables leaked to pg_dump")
			}

			// Verify and List.
			vm, err := Verify(path, pass)
			if err != nil {
				t.Fatal(err)
			}
			if vm.DB != m.DB || !slices.Equal(vm.Files, m.Files) {
				t.Errorf("Verify manifest differs from Create's")
			}
			if pass != "" {
				if _, err := Verify(path, ""); !errors.Is(err, ErrPassphraseRequired) {
					t.Errorf("Verify without passphrase: %v", err)
				}
				if _, err := Verify(path, "not the passphrase"); !errors.Is(err, ErrWrongPassphrase) {
					t.Errorf("Verify with wrong passphrase: %v", err)
				}
			}
			entries, err := List(out)
			if err != nil || len(entries) != 1 {
				t.Fatalf("List = %v, %v", entries, err)
			}
			e := entries[0]
			if e.Name != wantName || e.Encrypted != (pass != "") || !e.Created.Equal(testNow) || e.Err != nil {
				t.Errorf("List entry = %+v", e)
			}
			if (e.Manifest != nil) == (pass != "") {
				t.Errorf("List manifest = %v for encrypted=%v", e.Manifest, pass != "")
			}

			// Restore over an existing uploads directory.
			target := filepath.Join(t.TempDir(), "site", "uploads")
			writeTree(t, target, map[string]string{"old/kept.txt": "previous"})
			os.Chmod(target, 0o750)
			restoredAt := testNow.Add(26 * time.Hour)
			err = Restore(ctx, path, RestoreOptions{DatabaseURL: testDSN, UploadsDir: target, Passphrase: pass,
				PgRestore: ft.pgRestore, TempDir: t.TempDir(), Now: fixedClock(restoredAt), Log: testLog(t), probe: emptyDB})
			if err != nil {
				t.Fatal(err)
			}
			if got := ft.read(t, "restored.dump"); got != string(ft.fixture) {
				t.Errorf("pg_restore got a different dump (%d bytes, want %d)", len(got), len(ft.fixture))
			}
			rargs := ft.read(t, "restore.args")
			for _, a := range []string{"--clean", "--if-exists", "--no-owner", "--no-privileges", "--single-transaction", "--dbname=dbname='sap13ns'"} {
				if !strings.Contains(rargs, a+"\n") {
					t.Errorf("pg_restore args lack %s:\n%s", a, rargs)
				}
			}
			if strings.Contains(rargs, "hunter2") {
				t.Errorf("password on pg_restore's command line")
			}
			if !strings.Contains(ft.read(t, "restore.env"), "PGPASSWORD=hunter2-secret\n") {
				t.Errorf("pg_restore did not get the password through its environment")
			}
			if got := readTree(t, target); !maps.Equal(got, want) {
				t.Errorf("restored uploads = %v", keys(got))
			}
			if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o750 {
				t.Errorf("restored uploads mode = %v, want the previous 0750", fi.Mode().Perm())
			}
			if fi, _ := os.Stat(filepath.Join(target, "2026", "10", "0a1b2c.jpg")); fi.Mode().Perm() != 0o644 {
				t.Errorf("restored file mode = %v", fi.Mode().Perm())
			}
			bak := target + ".bak-20261019-053000"
			if got := readTree(t, bak); got["old/kept.txt"] != "previous" || len(got) != 1 {
				t.Errorf("previous uploads not kept in %s: %v", bak, got)
			}
			if names := dirNames(t, filepath.Dir(target)); !slices.Equal(names, []string{"uploads", "uploads.bak-20261019-053000"}) {
				t.Errorf("restore left %v next to the uploads", names)
			}
		})
	}
}

func TestRestoreRefusesNonEmptyDatabase(t *testing.T) {
	cleanPGEnv(t)
	ctx := context.Background()
	ft := newFakeTools(t)
	uploads, _ := testSite(t)
	path, _, err := Create(ctx, Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: t.TempDir(),
		PgDump: ft.pgDump, Now: fixedClock(testNow), probe: emptyDB})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "uploads")
	writeTree(t, target, map[string]string{"live.jpg": "live"})
	ro := RestoreOptions{DatabaseURL: testDSN, UploadsDir: target, PgRestore: ft.pgRestore, Now: fixedClock(testNow), probe: busyDB}

	err = Restore(ctx, path, ro)
	if !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("err = %v, want ErrNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(ft.dir, "restore.args")); err == nil {
		t.Error("pg_restore ran despite the refusal")
	}
	if got := readTree(t, target); len(got) != 1 || got["live.jpg"] != "live" {
		t.Errorf("uploads changed: %v", got)
	}
	if names := dirNames(t, filepath.Dir(target)); len(names) != 1 {
		t.Errorf("refused restore left %v", names)
	}

	ro.Force = true
	if err := Restore(ctx, path, ro); err != nil {
		t.Fatalf("forced restore: %v", err)
	}
	if ft.read(t, "restored.dump") != string(ft.fixture) {
		t.Error("forced restore did not run pg_restore with the dump")
	}
}

func TestRestoreSkips(t *testing.T) {
	cleanPGEnv(t)
	ctx := context.Background()
	ft := newFakeTools(t)
	uploads, want := testSite(t)
	path, _, err := Create(ctx, Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: t.TempDir(),
		PgDump: ft.pgDump, Now: fixedClock(testNow), probe: emptyDB})
	if err != nil {
		t.Fatal(err)
	}

	// Uploads only: no database url needed, pg_restore never runs.
	target := filepath.Join(t.TempDir(), "uploads")
	if err := Restore(ctx, path, RestoreOptions{UploadsDir: target, SkipDB: true, PgRestore: "/nonexistent/pg_restore", probe: busyDB}); err != nil {
		t.Fatal(err)
	}
	if got := readTree(t, target); !maps.Equal(got, want) {
		t.Errorf("uploads = %v", keys(got))
	}

	// Database only: uploads untouched.
	other := filepath.Join(t.TempDir(), "uploads")
	writeTree(t, other, map[string]string{"live.jpg": "live"})
	if err := Restore(ctx, path, RestoreOptions{DatabaseURL: testDSN, UploadsDir: other, SkipUploads: true, PgRestore: ft.pgRestore, probe: emptyDB}); err != nil {
		t.Fatal(err)
	}
	if ft.read(t, "restored.dump") != string(ft.fixture) {
		t.Error("pg_restore did not receive the dump")
	}
	if got := readTree(t, other); len(got) != 1 {
		t.Errorf("SkipUploads changed the uploads: %v", got)
	}

	if err := Restore(ctx, path, RestoreOptions{SkipDB: true, SkipUploads: true}); err == nil {
		t.Error("restoring nothing should fail")
	}
}

func TestRestoreWrongPassphraseChangesNothing(t *testing.T) {
	cleanPGEnv(t)
	ctx := context.Background()
	ft := newFakeTools(t)
	uploads, _ := testSite(t)
	path, _, err := Create(ctx, Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: t.TempDir(), Passphrase: testPassphrase,
		PgDump: ft.pgDump, Now: fixedClock(testNow), probe: emptyDB, kdf: testKDF})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "uploads")
	writeTree(t, target, map[string]string{"live.jpg": "live"})
	err = Restore(ctx, path, RestoreOptions{DatabaseURL: testDSN, UploadsDir: target, Passphrase: "not the right one",
		PgRestore: ft.pgRestore, probe: emptyDB})
	if !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(ft.dir, "restore.args")); err == nil {
		t.Error("pg_restore ran")
	}
	if names := dirNames(t, filepath.Dir(target)); len(names) != 1 {
		t.Errorf("failed restore left %v", names)
	}
}

func TestCreatePgDumpFails(t *testing.T) {
	cleanPGEnv(t)
	dir := t.TempDir()
	bin := writeScript(t, dir, "pg_dump", `echo 'pg_dump: error: connection to server at "db.internal" failed: FATAL:  password authentication failed for user "app"' >&2
printf 'PGDMP partial'
exit 1
`)
	uploads, _ := testSite(t)
	out := filepath.Join(t.TempDir(), "backups")
	_, _, err := Create(context.Background(), Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: out,
		PgDump: bin, Now: fixedClock(testNow), probe: emptyDB})
	if err == nil || !strings.Contains(err.Error(), "password authentication failed") {
		t.Fatalf("err = %v, want pg_dump's message", err)
	}
	if names := dirNames(t, out); len(names) != 0 {
		t.Errorf("failed backup left %v", names)
	}
}

func TestCreateRefusesBadDumpAndSpecialFiles(t *testing.T) {
	cleanPGEnv(t)
	ctx := context.Background()
	dir := t.TempDir()
	notADump := writeScript(t, dir, "pg_dump", "echo 'CREATE TABLE plain_sql ();'\n")
	uploads, _ := testSite(t)
	out := t.TempDir()
	if _, _, err := Create(ctx, Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: out, PgDump: notADump, probe: emptyDB}); err == nil ||
		!strings.Contains(err.Error(), "custom-format") {
		t.Errorf("plain SQL dump: err = %v", err)
	}

	ft := newFakeTools(t)
	if err := syscall.Mkfifo(filepath.Join(uploads, "2026", "pipe.jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(ctx, Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: out, PgDump: ft.pgDump, probe: emptyDB}); err == nil ||
		!strings.Contains(err.Error(), "pipe.jpg") {
		t.Errorf("FIFO in uploads: err = %v", err)
	}
	if names := dirNames(t, out); len(names) != 0 {
		t.Errorf("failed backups left %v", names)
	}
}

func TestCreateChecks(t *testing.T) {
	cleanPGEnv(t)
	ctx := context.Background()
	ft := newFakeTools(t)
	uploads, _ := testSite(t)
	out := t.TempDir()
	opts := Options{DatabaseURL: testDSN, UploadsDir: uploads, OutDir: out, PgDump: ft.pgDump, Now: fixedClock(testNow), probe: emptyDB, kdf: testKDF}

	weak := opts
	weak.Passphrase = "short"
	if _, _, err := Create(ctx, weak); !errors.Is(err, ErrWeakPassphrase) {
		t.Errorf("weak passphrase: %v", err)
	}
	missing := opts
	missing.UploadsDir = filepath.Join(out, "nope")
	if _, _, err := Create(ctx, missing); err == nil {
		t.Error("missing uploads directory accepted")
	}
	public := opts
	public.OutDir = filepath.Join(uploads, "2026", "backups")
	if _, _, err := Create(ctx, public); err == nil || !strings.Contains(err.Error(), "inside the uploads") {
		t.Errorf("backups inside the published uploads: %v", err)
	}
	if _, err := os.Stat(public.OutDir); err == nil {
		t.Errorf("refused backup created %s", public.OutDir)
	}
	link := filepath.Join(t.TempDir(), "via-symlink")
	os.Symlink(filepath.Join(uploads, "2026"), link)
	public.OutDir = link
	if _, _, err := Create(ctx, public); err == nil || !strings.Contains(err.Error(), "inside the uploads") {
		t.Errorf("backups inside the uploads through a symlink: %v", err)
	}
	if _, _, err := Create(ctx, opts); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(ctx, opts); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second backup in the same second: %v", err)
	}
}

func TestUploadChangedDetected(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.jpg": "original"})
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files, err := scanUploads(context.Background(), root, testLog(t))
	if err != nil || len(files) != 1 {
		t.Fatalf("scan = %v, %v", files, err)
	}
	// Replaced through a rename, as the media package writes files.
	writeTree(t, dir, map[string]string{"a.jpg.new": "changed!"})
	os.Rename(filepath.Join(dir, "a.jpg.new"), filepath.Join(dir, "a.jpg"))
	if err := copyUpload(root, &files[0], io.Discard); !errors.Is(err, errUploadChanged) {
		t.Errorf("replaced file: err = %v", err)
	}
	os.Remove(filepath.Join(dir, "a.jpg"))
	if err := copyUpload(root, &files[0], io.Discard); !errors.Is(err, errUploadChanged) {
		t.Errorf("removed file: err = %v", err)
	}
}

func TestListSkipsForeignFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"notes.txt", "sap13ns-2026.tar.gz", "sap13ns-20261318-000000.tar.gz", ".sap13ns-123.tmp"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
	}
	os.Mkdir(filepath.Join(dir, "sap13ns-20261018-000000.tar.gz"), 0o700)
	os.WriteFile(filepath.Join(dir, "sap13ns-20261017-000000.tar.gz"), []byte("garbage"), 0o600)
	entries, err := List(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	if !errors.Is(entries[0].Err, ErrCorrupt) || entries[0].Manifest != nil {
		t.Errorf("garbage archive: %+v", entries[0])
	}
	if entries, err := List(filepath.Join(dir, "missing")); err != nil || entries != nil {
		t.Errorf("missing dir: %v, %v", entries, err)
	}
}

func boolStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func keys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
