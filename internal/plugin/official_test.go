package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"testing/fstest"

	"sap13ns/internal/testutil"
)

// TestLoadOfficial: the bundle lists the packages it can read, by id, the
// newest of each id, and names the ones it cannot.
func TestLoadOfficial(t *testing.T) {
	life := testPackage(t, "life", "1.2.0", "v1")
	sum := sha256.Sum256(life)
	fsys := fstest.MapFS{
		"life.zip":      {Data: life},
		"life-old.zip":  {Data: testPackage(t, "life", "1.0.0", "old")},
		"other.zip":     {Data: testPackage(t, "other", "2.0.0", "x")},
		"broken.zip":    {Data: []byte("not a zip")},
		"reserved.zip":  {Data: testPackage(t, "admin", "1.0.0", "x")},
		"README.md":     {Data: []byte("# Packages\n")},
		"sub/deep.zip":  {Data: testPackage(t, "deep", "1.0.0", "x")},
		"no-module.zip": {Data: buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "nomod", "name": "N", "version": "1.0.0", "hooks": ["content.filter"]}`)})},
	}
	o, err := LoadOfficial(fsys)
	if err == nil || !strings.Contains(err.Error(), "broken.zip") || !strings.Contains(err.Error(), "reserved.zip") ||
		!strings.Contains(err.Error(), "no-module.zip") {
		t.Fatalf("the unreadable packages are not named: %v", err)
	}
	var ids []string
	for _, p := range o.List() {
		ids = append(ids, p.ID+"@"+p.Version)
	}
	if strings.Join(ids, " ") != "life@1.2.0 other@2.0.0" || o.Len() != 2 {
		t.Fatalf("listed %v", ids)
	}
	p := o.Get("life")
	if p == nil || p.SHA256 != hex.EncodeToString(sum[:]) || p.Size != int64(len(life)) || p.Author != "Alice" {
		t.Fatalf("life: %+v", p)
	}
	if b, err := p.Package(); err != nil || !bytes.Equal(b, life) {
		t.Fatalf("package: %v", err)
	}
	if !p.NewerThan("1.1.9") || p.NewerThan("1.2.0") || p.NewerThan("1.10.0") {
		t.Fatal("NewerThan")
	}
	if o.Get("nope") != nil || o.Get("deep") != nil {
		t.Fatal("found a plugin that is not in the bundle")
	}

	// A package that changed after the bundle was read is not installed.
	fsys["life.zip"] = &fstest.MapFile{Data: testPackage(t, "life", "1.2.0", "changed")}
	if _, err := p.Package(); err == nil {
		t.Fatal("a changed package was returned")
	}

	// Empty bundles.
	var none *Official
	if none.List() != nil || none.Get("life") != nil || none.Len() != 0 {
		t.Fatal("nil bundle")
	}
	for _, fsys := range []fstest.MapFS{nil, {"README.md": {Data: []byte("x")}}} {
		if o, err := LoadOfficial(fsys); err != nil || o.Len() != 0 {
			t.Fatalf("empty bundle: %v %d", err, o.Len())
		}
	}
	if o, err := LoadOfficial(nil); err != nil || o.Len() != 0 {
		t.Fatalf("no bundle: %v", err)
	}
}

// TestInstallOfficial: an official package installs like an upload,
// scanned and disabled, recorded with its source.
func TestInstallOfficial(t *testing.T) {
	st := testutil.Store(t)
	h := newTestHost(t, st, t.TempDir())
	uid := adminUser(t, st)
	o, err := LoadOfficial(fstest.MapFS{"life.zip": {Data: testPackage(t, "life", "1.0.0", "v1")}})
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := o.Get("life").Package()
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Install(context.Background(), bytes.NewReader(pkg), int64(len(pkg)),
		InstallOptions{UserID: uid, Source: SourceOfficial, Reviewed: true})
	if err != nil || res.Report == nil || res.Report.PackageSHA256 != o.Get("life").SHA256 {
		t.Fatalf("install: %v %+v", err, res)
	}
	p := h.Get("life")
	if p == nil || p.Status != StatusAvailable || p.Record.Source != SourceOfficial || p.Record.ReviewRequired || p.Report == nil {
		t.Fatalf("after install: %+v", p)
	}
}
