package transfer_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"sap13ns/internal/auth"
	"sap13ns/internal/backup"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/internal/transfer"
)

const pass = "correct horse battery staple"

func write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A site of a network moves, whole, to a single-site install: rows,
// secrets, files and the network's themes; what tied it to the network is
// cut.
func TestNetworkSiteToSingleSite(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Schemas(t)
	src := testutil.SiteStore(t, pool, "site_3")
	secret := bytes.Repeat([]byte{7}, 32)
	keys := auth.NewKeys(secret)

	admin, err := src.CreateUser(ctx, store.NewUser{Username: "owner", Email: "owner@example.com", Role: "admin", PasswordHash: "$argon2id$local"})
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (username, email, role, password_hash, network_account_id) VALUES ('editor', 'ed@example.com', 'editor', '!sso', 5)`,
		`INSERT INTO users (username, email, role, password_hash, network_account_id, network_operator) VALUES ('operator', 'op@example.com', 'admin', '!sso', 9, true)`,
		`INSERT INTO sessions (token_hash, user_id, csrf_token, expires_at, ip, user_agent) SELECT '\x01', id, 'c', now() + interval '1 day', '127.0.0.1', 'ua' FROM users WHERE username = 'owner'`,
	} {
		if _, err := src.DB.Exec(ctx, q); err != nil {
			t.Fatal(q, err)
		}
	}
	var postIDs []int64
	for _, title := range []string{"Hola", "Adiós"} {
		id, err := src.CreatePost(ctx, store.PostInput{Type: "post", Status: store.StatusPublished, Title: title, Slug: map[string]string{"Hola": "hola", "Adiós": "adios"}[title],
			Lang: "es", Content: "Texto de **" + title + "**", AuthorID: admin, CommentStatus: "open"})
		if err != nil {
			t.Fatal(err)
		}
		postIDs = append(postIDs, id)
	}
	smtpPass, _ := keys.Encrypt([]byte("smtp-secret"), []byte("site-smtp-password"))
	mail := store.MailSettings{Enabled: true, Host: "smtp.socratex.example", Port: 587, Username: "web", PasswordEnc: smtpPass, From: "Socratex <web@socratex.example>"}
	if err := src.SaveMailSettings(ctx, mail); err != nil {
		t.Fatal(err)
	}
	ai := store.DefaultAISettings()
	ai.Enabled, ai.Source = true, store.AISourceNetwork
	if err := src.SaveAISettings(ctx, ai); err != nil {
		t.Fatal(err)
	}

	srcDir, netThemes := t.TempDir(), t.TempDir()
	write(t, filepath.Join(srcDir, "uploads", "2026", "11", "foto.jpg"), "jpeg")
	write(t, filepath.Join(srcDir, "plugins", "forms", "plugin.wasm"), "wasm")
	write(t, filepath.Join(srcDir, "themes", "own", "theme.json"), `{"name":"site"}`)
	write(t, filepath.Join(netThemes, "own", "theme.json"), `{"name":"network"}`)
	write(t, filepath.Join(netThemes, "agency", "theme.json"), `{"name":"agency"}`)
	write(t, filepath.Join(netThemes, "agency", "templates", "home.html"), `home`)

	h, err := db.Scope(pool, "site_3")
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "socratex.transfer")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	m, err := transfer.Export(ctx, h, f, transfer.ExportOptions{
		Source:     transfer.Source{URL: "https://socratex.example", Label: "socratex", Network: "net.example", SiteID: 3, RPID: "socratex.example"},
		SecretKey:  secret,
		SMTP:       &transfer.SMTP{Host: "mail.net.example", Port: 587, Username: "net", Password: "pw", From: "net@net.example"},
		NetworkAI:  &transfer.NetworkAI{Provider: "anthropic", APIKey: "sk-ant-network", TextModel: "claude-sonnet-5-5"},
		UploadsDir: filepath.Join(srcDir, "uploads"), PluginsDir: filepath.Join(srcDir, "plugins"), ThemesDir: filepath.Join(srcDir, "themes"),
		NetworkThemesDir: netThemes, Passphrase: pass, TmpDir: t.TempDir(), AppVersion: "test", Log: testutil.Log(),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if !slices.Equal(m.NetworkThemes, []string{"agency"}) {
		t.Errorf("network themes %v, want [agency] (the site's own copy of own wins)", m.NetworkThemes)
	}
	if len(m.Files) != 5 {
		t.Errorf("%d files, want 5: %v", len(m.Files), m.Files)
	}

	if _, err := transfer.Open(ctx, archive, "wrong passphrase, really", t.TempDir()); !errors.Is(err, backup.ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	damaged := filepath.Join(t.TempDir(), "damaged")
	b := []byte(read(t, archive))
	b[len(b)/2] ^= 1
	os.WriteFile(damaged, b, 0o600)
	if _, err := transfer.Open(ctx, damaged, pass, t.TempDir()); err == nil {
		t.Error("a damaged archive opened")
	}

	// The new install: its own database (public) and data directory.
	dst, err := db.Open(ctx, testutil.URL(), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	dataDir := t.TempDir()
	serverKey := bytes.Repeat([]byte{9}, 32)
	opts := transfer.ApplyOptions{UploadsDir: filepath.Join(dataDir, "uploads"), PluginsDir: filepath.Join(dataDir, "plugins"),
		ThemesDir: filepath.Join(dataDir, "themes"), ServerKey: serverKey, Log: testutil.Log(),
		Keep: &transfer.KeepUser{Username: "importer", Email: "ED@example.com", PasswordHash: "$argon2id$importer"}}
	os.MkdirAll(opts.UploadsDir, 0o755) // empty: fine
	staged, err := transfer.Open(ctx, archive, pass, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := staged.Apply(ctx, dst, opts)
	staged.Close()
	if err != nil {
		t.Fatal(err)
	}
	// The importer's account took over editor's, which had no password.
	if rep.Kept != "editor" || rep.KeptAdded || len(rep.SSOUsers) != 0 {
		t.Errorf("kept %q (added %v), SSO users %+v", rep.Kept, rep.KeptAdded, rep.SSOUsers)
	}
	if rep.SMTP {
		t.Error("the network's SMTP replaced the site's own")
	}
	if !slices.Equal(rep.Operators, []string{"operator"}) {
		t.Errorf("operators %v", rep.Operators)
	}

	st := store.New(dst)
	if err := db.Migrate(ctx, dst, testutil.Log()); err != nil {
		t.Fatal("migrations after the import:", err)
	}
	for _, id := range postIDs {
		p, err := st.PostByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if p.AuthorID == nil || *p.AuthorID != admin {
			t.Errorf("post %d by %d, want %d", id, p.AuthorID, admin)
		}
	}
	id, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: store.StatusDraft, Title: "Nuevo", Slug: "nuevo", Lang: "es", AuthorID: admin, CommentStatus: "open"})
	if err != nil {
		t.Fatal("a new post after the import:", err)
	}
	if id <= postIDs[1] {
		t.Errorf("new post id %d reuses an imported one", id)
	}
	u, err := st.UserByUsername(ctx, "owner")
	if err != nil || u.PasswordHash != "$argon2id$local" {
		t.Errorf("owner %+v %v", u, err)
	}
	if ed, err := st.UserByUsername(ctx, "editor"); err != nil || ed.PasswordHash != "$argon2id$importer" || ed.Role != "admin" {
		t.Errorf("editor %+v %v", ed, err)
	}
	// The site keeps its key, sealed with the server's.
	if k, err := transfer.SiteKey(ctx, dst, serverKey); err != nil || !bytes.Equal(k, secret) {
		t.Errorf("site key %x %v", k, err)
	}
	if _, err := transfer.SiteKey(ctx, dst, bytes.Repeat([]byte{1}, 32)); err == nil {
		t.Error("the site key opened with another server key")
	}
	var rec transfer.Record
	if err := st.GetJSON(ctx, transfer.RecordKey, &rec); err != nil || rec.From != "https://socratex.example" || len(rec.NoPassword) != 0 {
		t.Errorf("record %+v %v", rec, err)
	}
	var sessions, linked int
	var opStatus string
	dst.QueryRow(ctx, `SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM users WHERE network_account_id IS NOT NULL),
		(SELECT status FROM users WHERE username = 'operator')`).Scan(&sessions, &linked, &opStatus)
	if sessions != 0 || linked != 0 || opStatus != "disabled" {
		t.Errorf("sessions %d, linked users %d, operator %s", sessions, linked, opStatus)
	}
	gotMail, err := st.MailSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pw, err := keys.Decrypt(gotMail.PasswordEnc, []byte("site-smtp-password")); err != nil || string(pw) != "smtp-secret" {
		t.Errorf("SMTP password %q %v", pw, err)
	}
	gotAI, err := st.AISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if k, err := keys.Decrypt(gotAI.APIKeyEnc, []byte("ai-api-key")); gotAI.Source != "" || gotAI.Provider != "anthropic" || err != nil || string(k) != "sk-ant-network" {
		t.Errorf("AI settings %+v, key %q %v", gotAI, k, err)
	}
	for name, want := range map[string]string{
		"uploads/2026/11/foto.jpg":          "jpeg",
		"plugins/forms/plugin.wasm":         "wasm",
		"themes/own/theme.json":             `{"name":"site"}`,
		"themes/agency/templates/home.html": "home",
	} {
		if got := read(t, filepath.Join(dataDir, name)); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if entries, _ := filepath.Glob(filepath.Join(dataDir, ".sap13ns-transfer-*")); len(entries) != 0 {
		t.Errorf("staging left behind: %v", entries)
	}

	// A second import needs -replace, which keeps what it replaces.
	staged, err = transfer.Open(ctx, archive, pass, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if _, err := staged.Apply(ctx, dst, opts); !errors.Is(err, transfer.ErrNotEmpty) {
		t.Fatalf("import over a site: %v", err)
	}
	opts.Replace, opts.Keep = true, nil
	rep, err = staged.Apply(ctx, dst, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.MovedAside) != 3 {
		t.Errorf("moved aside %v", rep.MovedAside)
	}
	if len(rep.SSOUsers) != 1 || rep.SSOUsers[0].Username != "editor" {
		t.Errorf("SSO users %+v, want editor", rep.SSOUsers)
	}
	if _, err := st.PostByID(ctx, id); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the post made after the first import survived the replace: %v", err)
	}
}
