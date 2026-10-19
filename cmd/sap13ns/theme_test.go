package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sap13ns/internal/testutil"
)

// cliTheme writes a theme archive (folder slug) to a file: without the
// footer credit when credit is false.
func cliTheme(t *testing.T, slug, version string, credit bool) string {
	return cliThemeNamed(t, slug, "CLI theme", version, credit)
}

func cliThemeNamed(t *testing.T, slug, name, version string, credit bool) string {
	t.Helper()
	footer := `{{template "shared/footer-meta" .}}`
	if !credit {
		footer = ""
	}
	files := map[string]string{
		"theme.json": `{"name": "` + name + `", "version": "` + version + `"}`,
		"templates/layout.html": `{{define "layout"}}<!doctype html><html lang="{{.Site.Lang}}"><head>{{template "shared/head" .}}</head>
<body><main>{{template "content" .}}</main><footer>` + footer + `</footer></body></html>{{end}}`,
		"templates/home.html":   `{{define "content"}}<p>home</p>{{end}}`,
		"templates/single.html": `{{define "content"}}{{with .Post}}<h1>{{.Title}}</h1>{{end}}{{end}}`,
		"templates/404.html":    `{{define "content"}}<p>not found</p>{{end}}`,
		"static/theme.css":      `body{margin:0}`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(slug + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), slug+".zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestThemeCLI: list, install, replace, a refusal, remove, on a single
// site.
func TestThemeCLI(t *testing.T) {
	st := testutil.Store(t)
	data := t.TempDir()
	for k, v := range map[string]string{
		"DATABASE_URL": testutil.URL(), "MULTISITE": "", "BASE_URL": "http://127.0.0.1:8177", "SECRET_KEY": strings.Repeat("ab", 32),
		"DATA_DIR": data, "THEMES_DIR": "", "LOG_LEVEL": "error", "UPDATE_CHECK": "false",
	} {
		t.Setenv("SAP13NS_"+k, v)
	}
	buf := &bytes.Buffer{}
	prev := out
	out = buf
	t.Cleanup(func() { out = prev })
	run := func(args ...string) (string, error) {
		t.Helper()
		buf.Reset()
		err := themeCmd(args)
		return buf.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		s, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, s)
		}
		return s
	}
	fails := func(want string, args ...string) string {
		t.Helper()
		s, err := run(args...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want an error with %q\n%s", args, err, want, s)
		}
		return s
	}
	dir := filepath.Join(data, "themes")

	fails("usage", "nope")
	fails("usage", "install")
	fails("usage", "list", "extra")
	fails("works on a network only", "list", "-site", "alpha")

	got := must("list")
	if !strings.Contains(got, "apple") || !strings.Contains(got, "built-in") || !strings.Contains(got, "yes") {
		t.Fatalf("list: %s", got)
	}

	// Install: not active; the argument may come before the flags.
	got = must("install", cliTheme(t, "clitheme", "1.0.0", true))
	if !strings.Contains(got, "clitheme 1.0.0 installed") || !strings.Contains(got, "It is not active") {
		t.Fatalf("install: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "clitheme", "templates", "layout.html")); err != nil {
		t.Fatal(err)
	}
	got = must("list")
	if !strings.Contains(got, "clitheme") || !strings.Contains(got, "CLI theme") || !strings.Contains(got, "uploaded") {
		t.Fatalf("list after the install: %q", got)
	}
	got = must("install", cliTheme(t, "clitheme", "1.1.0", true))
	if !strings.Contains(got, "replaced version 1.0.0") {
		t.Fatalf("replace: %q", got)
	}

	// A refusal lists its problems and leaves the installed version.
	got = fails("refused: 1 problems", "install", cliTheme(t, "clitheme", "2.0.0", false))
	if !strings.Contains(got, "footer credit") || !strings.Contains(got, "templates/layout.html") {
		t.Fatalf("refusal: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "clitheme", "theme.json")); !strings.Contains(string(b), "1.1.0") {
		t.Fatalf("the refusal replaced the theme: %s", b)
	}
	got = fails("refused", "install", cliThemeNamed(t, "clitheme", `CLI \u001b[31mtheme`, "3.0.0", true))
	if !strings.Contains(got, "control characters") || strings.Contains(got, "\x1b") {
		t.Fatalf("a control character: %q", got)
	}
	fails("not a theme of the site's own directory", "remove", "apple")

	// Remove: never the active theme.
	ctx := context.Background()
	set, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.ActiveTheme = "clitheme"
	if err := st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	fails("is the active theme", "remove", "clitheme")
	if !strings.Contains(must("list"), "clitheme") {
		t.Fatal("removed the active theme")
	}
	set.ActiveTheme = "apple"
	if err := st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if got := must("remove", "clitheme"); !strings.Contains(got, "removed clitheme") {
		t.Fatalf("remove: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "clitheme")); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
	fails("not a theme of the site's own directory", "remove", "clitheme")

	// Audited, as from the admin.
	for action, want := range map[string]int{"theme.uploaded": 1, "theme.updated": 1, "theme.refused": 2, "theme.deleted": 1} {
		if _, n, err := st.ListAudit(ctx, action, 10, 0); err != nil || n != want {
			t.Errorf("%s: %d entries, want %d (%v)", action, n, want, err)
		}
	}
}

// TestThemeCLINetwork: on a network the commands work on one site's
// directory, named with -site.
func TestThemeCLINetwork(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()
	c.must(testPassword+"\n", networkCmd, "init", "-username", "Root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	c.must("", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-plan", "pro")
	c.must("", siteCmd, "create", "-slug", "gamma", "-owner", "owner@example.test", "-plan", "pro")
	n := c.network()
	alpha, err := n.SiteBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	c.fails("name the site with -site", themeCmd, "list")
	pkg := cliTheme(t, "clitheme", "1.0.0", true)
	if got := c.must("", themeCmd, "install", "-site", "alpha", pkg); !strings.Contains(got, "installed") {
		t.Fatalf("install: %q", got)
	}
	if _, err := os.Stat(filepath.Join(n.Dir(alpha.ID), "themes", "clitheme", "theme.json")); err != nil {
		t.Fatal(err)
	}
	if got := c.must("", themeCmd, "list", "-site", "alpha"); !strings.Contains(got, "clitheme") {
		t.Fatalf("list: %q", got)
	}
	if got := c.must("", themeCmd, "list", "-site", "gamma"); strings.Contains(got, "clitheme") {
		t.Fatalf("another site lists it: %q", got)
	}
	c.fails("no site", themeCmd, "list", "-site", "nope")
	if got := c.must("", themeCmd, "remove", "-site", "alpha", "clitheme"); !strings.Contains(got, "removed clitheme") {
		t.Fatalf("remove: %q", got)
	}
}
