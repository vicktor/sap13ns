package main

import (
	"archive/zip"
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/export"
	"sap13ns/internal/store"
)

// TestExportCLI exports one site of a network from the command line.
func TestExportCLI(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()
	c.must(testPassword+"\n", networkCmd, "init", "-username", "Root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	n := c.network()
	for _, slug := range []string{"alpha", "bravo"} {
		c.must("", siteCmd, "create", "-slug", slug, "-owner", "owner@example.test", "-plan", "pro", "-title", slug)
		s, err := n.SiteBySlug(ctx, slug)
		if err != nil {
			t.Fatal(err)
		}
		st := c.store(s.Schema)
		uid, err := st.CreateUser(ctx, store.NewUser{Username: slug + "-writer", Email: slug + "-writer@example.com", Role: "author", PasswordHash: "x"})
		if err != nil {
			t.Fatal(err)
		}
		pub := time.Now().Add(-time.Hour)
		for _, status := range []string{store.StatusPublished, store.StatusDraft} {
			id, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: status, Title: slug + " " + status, Slug: slug + "-" + status,
				Lang: "es", Content: strings.ToUpper(slug + status), AuthorID: uid, CommentStatus: "open", PublishedAt: &pub})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateComment(ctx, store.NewComment{PostID: id, AuthorName: "Reader", AuthorEmail: slug + "-reader@example.com",
				Content: "Nice", Status: "approved", IP: netip.MustParseAddr("192.0.2.9")}); err != nil {
				t.Fatal(err)
			}
		}
	}

	dir := t.TempDir()
	c.fails("must end in .xml or .zip", exportCmd, "-site", "alpha", "-o", filepath.Join(dir, "x.txt"))
	c.fails("usage", exportCmd, "-site", "alpha")
	c.fails("", exportCmd, "-site", "nowhere", "-o", filepath.Join(dir, "x.xml"))

	xmlFile := filepath.Join(dir, "alpha.xml")
	got := c.must("", exportCmd, "-site", "alpha", "-o", xmlFile)
	if !strings.Contains(got, "exported 1 posts") || !strings.Contains(got, "to "+xmlFile) {
		t.Errorf("summary: %q", got)
	}
	b, err := os.ReadFile(xmlFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(xmlFile); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}
	body := string(b)
	if !strings.Contains(body, "ALPHAPUBLISHED") || strings.Contains(body, "ALPHADRAFT") || strings.Contains(body, "BRAVO") ||
		strings.Contains(body, "bravo") || strings.Contains(body, "@") || strings.Contains(body, "192.0.2.") {
		t.Errorf("export of alpha:\n%s", body)
	}

	c.must("", exportCmd, "-site", "alpha", "-drafts", "-pii", "-o", xmlFile)
	b, _ = os.ReadFile(xmlFile)
	if body := string(b); !strings.Contains(body, "ALPHADRAFT") || !strings.Contains(body, "alpha-reader@example.com") ||
		!strings.Contains(body, "192.0.2.9") || strings.Contains(body, "bravo") {
		t.Errorf("export of alpha with drafts and personal data:\n%s", body)
	}

	zipFile := filepath.Join(dir, "alpha.zip")
	if got := c.must("", exportCmd, "-site", "alpha", "-o", zipFile); !strings.Contains(got, "files") {
		t.Errorf("summary: %q", got)
	}
	zr, err := zip.OpenReader(zipFile)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != export.ZipWXR+",posts/es/alpha-published.md" {
		t.Errorf("zip: %v", names)
	}

	// To stdout: the WXR alone, the summary on stderr.
	if got := c.must("", exportCmd, "-site", "alpha", "-o", "-"); !strings.HasPrefix(got, "<?xml") || strings.Contains(got, "exported") {
		t.Errorf("stdout: %.200q", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("left in the directory: %v", entries)
	}
}
