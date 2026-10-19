package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// TestSettingsLoginPathCLI: on a single site, settings login-path prints
// the sign-in address, sets one the settings page would take, and puts
// back /login; user create says where to sign in.
func TestSettingsLoginPathCLI(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	for k, v := range map[string]string{
		"DATABASE_URL": testutil.URL(), "MULTISITE": "", "BASE_URL": "https://blog.example.com", "SECRET_KEY": strings.Repeat("ab", 32),
		"DATA_DIR": t.TempDir(), "LOG_LEVEL": "error",
	} {
		t.Setenv("SAP13NS_"+k, v)
	}
	buf := &strings.Builder{}
	prev := out
	out = buf
	t.Cleanup(func() { out = prev })
	run := func(cmd func([]string) error, args ...string) (string, error) {
		buf.Reset()
		err := cmd(args)
		return buf.String(), err
	}
	saved := func() string {
		set, err := st.LoadSettings(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return set.LoginPath
	}
	uid, err := st.CreateUser(ctx, store.NewUser{Username: "admin", Email: "admin@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Now().Add(-time.Hour)
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePage, Status: store.StatusPublished, Title: "Contacto", Slug: "contacto",
		Lang: "es", AuthorID: uid, CommentStatus: "closed", PublishedAt: &pub}); err != nil {
		t.Fatal(err)
	}

	if _, err := run(settingsCmd); err == nil || !strings.Contains(err.Error(), "usage: sap13ns settings login-path") {
		t.Fatalf("no subcommand: %v", err)
	}
	got, err := run(settingsCmd, "login-path")
	if err != nil || !strings.Contains(got, "sign-in address: /login\nhttps://blog.example.com/login\n") ||
		!strings.Contains(got, "hide the administration from visitors: off") {
		t.Fatalf("print: %q %v", got, err)
	}
	for set, want := range map[string]string{
		"/contacto": "is taken", "/admin": "is taken", "/blog/entrar": "is taken", "/en": "one or two words", "/a_b": "one or two words",
	} {
		if _, err := run(settingsCmd, "login-path", "-set", set); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("-set %s: %v", set, err)
		}
	}
	if _, err := run(settingsCmd, "login-path", "-reset", "-set", "/entrar"); err == nil {
		t.Error("-reset with -set")
	}
	if _, err := run(settingsCmd, "login-path", "-site", "alpha"); err == nil || !strings.Contains(err.Error(), "SAP13NS_MULTISITE=true") {
		t.Errorf("-site on a single site: %v", err)
	}
	if p := saved(); p != "/login" {
		t.Fatalf("a refused address was saved: %q", p)
	}

	got, err = run(settingsCmd, "login-path", "-set", " Entrar/ ")
	if err != nil || !strings.Contains(got, "sign-in address: /entrar\nhttps://blog.example.com/entrar\n") ||
		!strings.Contains(got, "changed from /login") || saved() != "/entrar" {
		t.Fatalf("-set: %q %v", got, err)
	}
	if a, _, err := st.ListAudit(ctx, "settings.login_path", 10, 0); err != nil || len(a) != 1 || a[0].Target != "/entrar" ||
		a[0].Details["from"] != "/login" || a[0].Details["by"] != "cli" {
		t.Errorf("audit: %+v %v", a, err)
	}
	if got, _ := run(settingsCmd, "login-path", "-set", "/entrar"); strings.Contains(got, "changed") {
		t.Errorf("unchanged: %q", got)
	}

	// The users created here are told where to sign in.
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("correct horse battery staple 42\n")
	f.Seek(0, io.SeekStart)
	prevIn := os.Stdin
	os.Stdin = f
	got, err = run(userCmd, "create", "-username", "ana", "-email", "ana@example.com", "-role", "author")
	os.Stdin = prevIn
	f.Close()
	if err != nil || !strings.Contains(got, "sign in at https://blog.example.com/entrar\n") {
		t.Fatalf("user create: %q %v", got, err)
	}

	got, err = run(settingsCmd, "login-path", "-reset")
	if err != nil || !strings.Contains(got, "sign-in address: /login\n") || !strings.Contains(got, "changed from /entrar") || saved() != "/login" {
		t.Fatalf("-reset: %q %v", got, err)
	}
}

// TestSettingsLoginPathNetworkCLI: on a network, -site names the site, or
// the portal with -site network; each keeps its own address.
func TestSettingsLoginPathNetworkCLI(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()
	c.must(testPassword+"\n", networkCmd, "init", "-username", "root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	c.must("", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-plan", "pro", "-lang", "en")

	c.fails("name the site with -site", settingsCmd, "login-path")
	if got := c.must("", settingsCmd, "login-path", "-site", "alpha", "-set", "/entrar"); !strings.Contains(got, "https://alpha.sap13ns.test/entrar") {
		t.Fatalf("alpha: %q", got)
	}
	if got := c.must("", settingsCmd, "login-path", "-site", "network", "-set", "/portal-access"); !strings.Contains(got, "https://sap13ns.test/portal-access") {
		t.Fatalf("portal: %q", got)
	}
	n := c.network()
	alpha, err := n.SiteBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	portal, err := n.NetworkSite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for schema, want := range map[string]string{alpha.Schema: "/entrar", portal.Schema: "/portal-access"} {
		if set, err := c.store(schema).LoadSettings(ctx); err != nil || set.LoginPath != want {
			t.Errorf("%s: %q %v", schema, set.LoginPath, err)
		}
	}
	if got := c.must(testPassword, userCmd, "create", "-site", "alpha", "-username", "lena", "-email", "lena@example.test", "-role", "author"); !strings.Contains(got, "sign in at https://alpha.sap13ns.test/entrar") {
		t.Errorf("user create: %q", got)
	}
	if got := c.must("", settingsCmd, "login-path", "-site", "alpha", "-reset"); !strings.Contains(got, "changed from /entrar") {
		t.Errorf("reset: %q", got)
	}
}
