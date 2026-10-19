package main

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sap13ns/internal/testutil"
)

// zipDir archives a folder under the name prefix.
func zipDir(t *testing.T, root, prefix string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		w, err := zw.Create(prefix + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), prefix+".zip")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestThemeImportCLI: "theme import" converts a WordPress block theme
// and installs it through the checks of "theme install".
func TestThemeImportCLI(t *testing.T) {
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
	wp := zipDir(t, filepath.Join("..", "..", "internal", "themeimport", "testdata", "fixture"), "fixture")

	// -o only converts.
	dest := filepath.Join(t.TempDir(), "converted.zip")
	got, err := run("import", "-o", dest, wp)
	if err != nil || !strings.Contains(got, "Fixture 1.0 by The sap13ns tests") || !strings.Contains(got, "wrote "+dest) {
		t.Fatalf("import -o: %v\n%s", err, got)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}

	// install points to import; import installs.
	if _, err := run("install", wp); err == nil || !strings.Contains(err.Error(), "theme import") {
		t.Fatalf("install of a WordPress theme: %v", err)
	}
	got, err = run("import", wp)
	if err != nil || !strings.Contains(got, "wp-fixture 1.0 installed") || !strings.Contains(got, "The navigation block becomes the menu location") {
		t.Fatalf("import: %v\n%s", err, got)
	}
	info, err := os.ReadFile(filepath.Join(data, "themes", "wp-fixture", ".sap13ns-upload.json"))
	if err != nil || !strings.Contains(string(info), `"imported_from":"wordpress-block"`) {
		t.Fatalf("upload info: %s %v", info, err)
	}
	// The converted archive installs as any theme.
	if got, err := run("install", dest); err != nil || !strings.Contains(got, "replaced version 1.0") {
		t.Fatalf("install of the converted theme: %v\n%s", err, got)
	}
	if got, err := run("import", "-slug", "my-copy", wp); err != nil || !strings.Contains(got, "my-copy 1.0 installed") {
		t.Fatalf("import -slug: %v\n%s", err, got)
	}

	// What it cannot convert.
	classic := filepath.Join(t.TempDir(), "classic")
	os.MkdirAll(classic, 0o750)
	os.WriteFile(filepath.Join(classic, "style.css"), []byte("/*\nTheme Name: Classic\n*/"), 0o600)
	os.WriteFile(filepath.Join(classic, "index.php"), []byte("<?php get_header();"), 0o600)
	if _, err := run("import", zipDir(t, classic, "classic")); err == nil || !strings.Contains(err.Error(), "wordpress-classic") {
		t.Fatalf("a classic theme: %v", err)
	}
	if _, err := run("import", cliTheme(t, "native", "1.0.0", true)); err == nil || !strings.Contains(err.Error(), "sap13ns theme already") {
		t.Fatalf("a sap13ns theme: %v", err)
	}

	ctx := context.Background()
	for action, want := range map[string]int{"theme.uploaded": 2, "theme.updated": 1} {
		if _, n, err := st.ListAudit(ctx, action, 10, 0); err != nil || n != want {
			t.Errorf("%s: %d entries, want %d (%v)", action, n, want, err)
		}
	}
}
