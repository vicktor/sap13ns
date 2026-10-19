package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// contentPack writes a small pack: site.json and one page in two languages.
func contentPack(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"site.json":    `{"title": "Pack site", "language": "en", "languages": ["es"]}`,
		"en/about.md":  "---\ntype: page\nkey: about\ntitle: About\nslug: about\n---\n" + body + "\n",
		"es/acerca.md": "---\ntype: page\nkey: about\ntitle: Acerca\nslug: acerca\n---\nHola.\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestContentLoadPortal: the pack goes into the portal of a network, and a
// second run changes nothing.
func TestContentLoadPortal(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()
	c.must(testPassword+"\n", networkCmd, "init", "-username", "root", "-email", "root@example.test")
	pack := contentPack(t, "Hello.")

	c.fails("-site", contentCmd, "load", pack)
	c.fails("usage", contentCmd, "load")
	c.fails("user nobody", contentCmd, "load", "-site", "network", "-author", "nobody", pack)

	got := c.must("", contentCmd, "load", "-site", "network", pack)
	if !strings.Contains(got, "2 created") {
		t.Fatalf("first load: %s", got)
	}
	st := c.store("network")
	p, err := st.PostBySlug(ctx, store.TypePage, "en", "about")
	if err != nil || p.Status != store.StatusPublished {
		t.Fatalf("the portal's page: %+v %v", p, err)
	}
	if set, err := st.LoadSettings(ctx); err != nil || set.SiteTitle != "Pack site" {
		t.Fatalf("settings: %+v %v", set.SiteTitle, err)
	}
	if got := c.must("", contentCmd, "load", pack, "-site", "network"); !strings.Contains(got, "0 created, 0 updated, 2 unchanged") {
		t.Fatalf("second load: %s", got)
	}
	if got := c.must("", contentCmd, "load", "-site", "network", contentPack(t, "Hello again.")); !strings.Contains(got, "1 updated") {
		t.Fatalf("an edited page: %s", got)
	}
}
