package main

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// archiveOf returns the first line of a create's output: the archive.
func archiveOf(t *testing.T, got string) string {
	t.Helper()
	path, _, _ := strings.Cut(got, "\n")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("create printed %q: %v", got, err)
	}
	return path
}

// TestSiteBackupCLI backs up a site of a network and restores it into
// another from the command line.
func TestSiteBackupCLI(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()
	c.must(testPassword+"\n", networkCmd, "init", "-username", "Root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	n := c.network()
	sites := map[string]*store.Store{}
	ids := map[string]int64{}
	for _, slug := range []string{"alpha", "bravo"} {
		c.must("", siteCmd, "create", "-slug", slug, "-owner", "owner@example.test", "-plan", "pro", "-title", "Site "+slug)
		s, err := n.SiteBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		sites[slug], ids[slug] = c.store(s.Schema), s.ID
	}
	alpha := sites["alpha"]
	uid, err := alpha.CreateUser(ctx, store.NewUser{Username: "writer", Email: "writer@example.com", Role: "author", PasswordHash: "secret-hash"})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Now().Add(-time.Hour)
	if _, err := alpha.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Alpha news", Slug: "alpha-news",
		Lang: "en", Content: "ALPHA CONTENT", AuthorID: uid, CommentStatus: "open", PublishedAt: &pub}); err != nil {
		t.Fatal(err)
	}
	buid, err := sites["bravo"].CreateUser(ctx, store.NewUser{Username: "bravo", Email: "bravo@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sites["bravo"].CreatePost(ctx, store.PostInput{Type: store.TypePage, Status: store.StatusPublished, Title: "Bravo page",
		Slug: "bravo-page", Lang: "en", Content: "BRAVO CONTENT", AuthorID: buid, CommentStatus: "closed", PublishedAt: &pub}); err != nil {
		t.Fatal(err)
	}

	// On a network a backup is of one site.
	c.fails("-site SLUG", backupCmd, "create")
	c.fails("-site SLUG", backupCmd, "restore", "x.zip")
	c.fails("-site SLUG", backupCmd, "list", "-format", "site")
	c.fails("-format site", backupCmd, "create", "-format", "full", "-site", "alpha")
	c.fails("-format must be", backupCmd, "create", "-format", "wxr", "-site", "alpha")
	c.fails("WordPress address", backupCmd, "create", "-site", "alpha", "-wp-base", "ftp://new.example")
	c.fails("-wp-base is for backup create", backupCmd, "list", "-site", "alpha", "-wp-base", "https://new.example")

	got := c.must("", backupCmd, "create", "-site", "alpha")
	file := archiveOf(t, got)
	dir := filepath.Join(c.data, "backups", "sites", strconv.FormatInt(ids["alpha"], 10))
	if filepath.Dir(file) != dir || !strings.Contains(got, "1 posts") {
		t.Fatalf("create: %q", got)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o600 {
		t.Errorf("archive mode %v", info.Mode())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v", info.Mode())
	}
	if b, _ := os.ReadFile(file); strings.Contains(string(b), "secret-hash") {
		t.Error("the archive holds a password hash")
	}
	if got := c.must("", backupCmd, "list", "-site", "alpha"); !strings.Contains(got, filepath.Base(file)) || !strings.Contains(got, "last attempt") {
		t.Errorf("list: %q", got)
	}
	if got := c.must("", backupCmd, "verify", file); !strings.Contains(got, "ok: backup of alpha") {
		t.Errorf("verify: %q", got)
	}

	// For a move to WordPress, the media addresses point at its uploads.
	wp := archiveOf(t, c.must("", backupCmd, "create", "-site", "alpha", "-wp-base", "https://new.example/"))
	zr, err := zip.OpenReader(wp)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == sitebackup.NameManifest {
			rc, _ := f.Open()
			var m sitebackup.Manifest
			json.NewDecoder(rc).Decode(&m)
			rc.Close()
			if m.WPBase != "https://new.example" {
				t.Errorf("manifest wp_base %q", m.WPBase)
			}
		}
	}
	zr.Close()

	// Restore into bravo: refused while bravo has content, unless
	// -replace; the confirmation is the site's name.
	c.fails("add -replace", backupCmd, "restore", "-site", "bravo", "-yes", file)
	c.fails("cancelled", backupCmd, "restore", "-site", "bravo", "-replace", file) // empty stdin
	if _, err := c.run("alpha\n", backupCmd, "restore", "-site", "bravo", "-replace", file); err == nil {
		t.Fatal("a restore went ahead with another site's name")
	}
	c.fails("portal", backupCmd, "restore", "-site", "network", "-replace", "-yes", file)

	// The running servers hear about it.
	conn, err := pgx.Connect(ctx, testutil.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "LISTEN sap13ns_network"); err != nil {
		t.Fatal(err)
	}
	got, err = c.run("bravo\n", backupCmd, "restore", "-site", "bravo", "-replace", file)
	if err != nil || !strings.Contains(got, "restored the backup of") || !strings.Contains(got, "what it replaced is in") {
		t.Fatalf("restore: %q %v", got, err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	note, err := conn.WaitForNotification(wctx)
	cancel()
	if err != nil || note.Payload != "restored:"+strconv.FormatInt(ids["bravo"], 10) {
		t.Fatalf("notification %+v %v", note, err)
	}
	bravo := sites["bravo"]
	if p, err := bravo.PostBySlug(ctx, store.TypePost, "en", "alpha-news"); err != nil || p.Content != "ALPHA CONTENT" {
		t.Fatalf("restored post %+v %v", p, err)
	}
	if _, err := bravo.PostBySlug(ctx, store.TypePage, "en", "bravo-page"); err == nil {
		t.Fatal("bravo's own page survived the restore")
	}
	if u, err := bravo.UserByUsername(ctx, "writer"); err != nil || u.PasswordHash != "" {
		t.Fatalf("restored user %+v %v", u, err)
	}
	if got := c.must("", backupCmd, "list", "-site", "bravo"); !strings.Contains(got, "before a restore") {
		t.Errorf("bravo's safety backup: %q", got)
	}
	// alpha is as it was.
	if _, err := alpha.PostBySlug(ctx, store.TypePost, "en", "alpha-news"); err != nil {
		t.Fatal(err)
	}

	// Encrypted, then decrypted.
	pass := filepath.Join(t.TempDir(), "pass")
	os.WriteFile(pass, []byte("a long enough passphrase\n"), 0o600)
	enc := archiveOf(t, c.must("", backupCmd, "create", "-site", "alpha", "-passphrase-file", pass))
	if !strings.HasSuffix(enc, ".zip.enc") {
		t.Fatalf("encrypted archive %s", enc)
	}
	c.fails("passphrase", backupCmd, "verify", enc)
	if got := c.must("", backupCmd, "verify", "-passphrase-file", pass, enc); !strings.Contains(got, "ok:") {
		t.Errorf("verify: %q", got)
	}
	plain := filepath.Join(t.TempDir(), "plain.zip")
	c.must("", backupCmd, "decrypt", "-passphrase-file", pass, "-o", plain, enc)
	c.fails("exists", backupCmd, "decrypt", "-passphrase-file", pass, "-o", plain, enc)
	if got := c.must("", backupCmd, "verify", plain); !strings.Contains(got, "ok: backup of alpha") {
		t.Errorf("verify the decrypted copy: %q", got)
	}
	if info, _ := os.Stat(plain); info.Mode().Perm() != 0o600 {
		t.Errorf("decrypted mode %v", info.Mode())
	}
	if list, _ := sitebackup.List(dir); len(list) != 3 || list[0].Path != enc {
		t.Errorf("alpha's backups: %+v", list)
	}

	// prune keeps the newest when told to keep nothing else.
	got = c.must("", backupCmd, "prune", "-site", "alpha", "-keep-daily", "0", "-keep-weekly", "0")
	if strings.Count(got, "removed") != 2 {
		t.Errorf("prune: %q", got)
	}
	if list, _ := sitebackup.List(dir); len(list) != 1 || list[0].Path != enc {
		t.Errorf("after prune: %+v", list)
	}
}

// TestSiteBackupSingleCLI: on a single site, -format site makes the
// per-site archive, and restore finds the format by itself.
func TestSiteBackupSingleCLI(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	data := t.TempDir()
	for k, v := range map[string]string{
		"DATABASE_URL": testutil.URL(), "MULTISITE": "", "BASE_URL": "https://blog.example.com", "SECRET_KEY": strings.Repeat("ab", 32),
		"DATA_DIR": data, "LOG_LEVEL": "error", "BACKUP_DIR": "", "BACKUP_PASSPHRASE": "", "BACKUP_PASSPHRASE_FILE": "",
	} {
		t.Setenv("SAP13NS_"+k, v)
	}
	buf := &strings.Builder{}
	prev := out
	out = buf
	t.Cleanup(func() { out = prev })
	run := func(args ...string) (string, error) {
		buf.Reset()
		err := backupCmd(args)
		return buf.String(), err
	}
	uid, err := st.CreateUser(ctx, store.NewUser{Username: "admin", Email: "admin@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Now().Add(-time.Hour)
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Hello", Slug: "hello",
		Lang: "en", Content: "Before", AuthorID: uid, CommentStatus: "open", PublishedAt: &pub}); err != nil {
		t.Fatal(err)
	}
	got, err := run("create", "-format", "site")
	if err != nil {
		t.Fatal(err)
	}
	file := archiveOf(t, got)
	if filepath.Dir(file) != filepath.Join(data, "backups", "site") || !strings.HasPrefix(filepath.Base(file), "sap13ns-site-blog.example.com-") {
		t.Fatalf("archive %s", file)
	}
	if _, err := run("create", "-site", "x"); err == nil || !strings.Contains(err.Error(), "SAP13NS_MULTISITE=true") {
		t.Fatalf("-site on a single site: %v", err)
	}

	p, _ := st.PostBySlug(ctx, store.TypePost, "en", "hello")
	if err := st.UpdatePost(ctx, p.ID, store.PostInput{Type: p.Type, Status: p.Status, Title: p.Title, Slug: p.Slug, Lang: p.Lang,
		Content: "After", AuthorID: uid, CommentStatus: p.CommentStatus, PublishedAt: p.PublishedAt}, uid, 0); err != nil {
		t.Fatal(err)
	}
	got, err = run("restore", "-replace", "-yes", file)
	if err != nil || !strings.Contains(got, "systemctl restart sap13ns") {
		t.Fatalf("restore: %q %v", got, err)
	}
	if p, _ := st.PostBySlug(ctx, store.TypePost, "en", "hello"); p == nil || p.Content != "Before" {
		t.Fatalf("restored post %+v", p)
	}
	if got, err := run("list", "-format", "site"); err != nil || !strings.Contains(got, "before a restore") {
		t.Fatalf("list: %q %v", got, err)
	}
}
