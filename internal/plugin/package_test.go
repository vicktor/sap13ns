package plugin

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"flag"
	"hash/crc32"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sap13ns/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite testdata/archives")

type zipEntry struct {
	name string
	body []byte
	mode fs.FileMode // 0 = regular 0644
	// lie, when set, is the uncompressed size written in the header.
	lie uint64
}

func buildZip(t testing.TB, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate, Modified: packTime}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		} else {
			hdr.SetMode(0o644)
		}
		if e.lie > 0 {
			// CreateRaw keeps whatever sizes the header claims.
			var comp bytes.Buffer
			fw, _ := flate.NewWriter(&comp, flate.BestCompression)
			fw.Write(e.body)
			fw.Close()
			hdr.CRC32 = crc32.ChecksumIEEE(e.body)
			hdr.CompressedSize64 = uint64(comp.Len())
			hdr.UncompressedSize64 = e.lie
			w, err := zw.CreateRaw(hdr)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(comp.Bytes())
			continue
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// testPackage is a valid package: plugin.json for id/version and a module
// that answers html.
func testPackage(t testing.TB, id, version, html string, extra ...zipEntry) []byte {
	m := `{"abi": 1, "id": "` + id + `", "name": "Test", "version": "` + version + `", "author": "Alice",
		"hooks": ["shortcode"], "shortcodes": ["` + id + `"],
		"settings": [{"key": "greeting", "label": "Greeting", "type": "text", "default": "Hi"}]}`
	entries := []zipEntry{{name: "plugin.json", body: []byte(m)}, {name: "plugin.wasm", body: respond(`{"html":"` + html + `"}`).bytes()},
		{name: "README.md", body: []byte("# Test\n")}, {name: "LICENSE", body: []byte("MIT\n")}}
	return buildZip(t, append(entries, extra...)...)
}

// maliciousArchives are the hostile packages kept in testdata/archives, by
// file name, with the rule that refuses each one.
func maliciousArchives(t testing.TB) map[string][]byte {
	base := func(extra ...zipEntry) []byte {
		return buildZip(t, append([]zipEntry{
			{name: "plugin.json", body: []byte(`{"abi": 1, "id": "hostile", "name": "Hostile", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["x"]}`)},
			{name: "plugin.wasm", body: respond(okHTML).bytes()},
		}, extra...)...)
	}
	mib := func(n int) []byte { return make([]byte, n<<20) }
	var many []zipEntry
	for i := range 49 {
		many = append(many, zipEntry{name: "assets/zeros-" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".txt", body: mib(1)})
	}
	return map[string][]byte{
		"zip-slip.zip":       base(zipEntry{name: "../../../../tmp/sap13ns-pwned.sh", body: []byte("#!/bin/sh\nid > /tmp/pwned\n")}),
		"absolute-path.zip":  base(zipEntry{name: "/etc/cron.d/sap13ns", body: []byte("* * * * * root id\n")}),
		"symlink.zip":        base(zipEntry{name: "assets/passwd.txt", body: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}),
		"setuid.zip":         base(zipEntry{name: "assets/tool.txt", body: []byte("x"), mode: fs.ModeSetuid | 0o755}),
		"zip-bomb.zip":       base(zipEntry{name: "assets/zeros.txt", body: mib(2)[:2<<20-1]}),
		"zip-bomb-total.zip": base(many...),
		"lying-header.zip":   base(zipEntry{name: "assets/small.txt", body: mib(3), lie: 100}),
		"oversized.zip": buildZip(t,
			zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "hostile", "name": "Hostile", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["x"]}`)},
			zipEntry{name: "plugin.wasm", body: mib(33)}),
		"disallowed-file.zip":  base(zipEntry{name: "install.sh", body: []byte("#!/bin/sh\n")}),
		"disallowed-asset.zip": base(zipEntry{name: "assets/payload.exe", body: []byte("MZ")}),
		"hidden-file.zip":      base(zipEntry{name: ".htaccess", body: []byte("Options +ExecCGI\n")}),
		"duplicate.zip":        base(zipEntry{name: "README.md", body: []byte("a")}, zipEntry{name: "readme.md", body: []byte("b")}),
		"no-manifest.zip":      buildZip(t, zipEntry{name: "plugin.wasm", body: respond(okHTML).bytes()}),
		"no-wasm.zip":          buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "hostile"}`)}),
	}
}

func TestMaliciousArchives(t *testing.T) {
	archives := filepath.Join("testdata", "archives")
	if *update {
		os.MkdirAll(archives, 0o755)
		for name, b := range maliciousArchives(t) {
			if err := os.WriteFile(filepath.Join(archives, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, rules := range map[string][]string{
		"zip-slip.zip":         {"package.zip-slip"},
		"absolute-path.zip":    {"package.zip-slip"},
		"symlink.zip":          {"package.symlink"},
		"setuid.zip":           {"package.special-file"},
		"zip-bomb.zip":         {"package.zip-bomb"},
		"zip-bomb-total.zip":   {"package.zip-bomb"},
		"lying-header.zip":     {"package.zip-bomb", "package.invalid"},
		"oversized.zip":        {"package.too-large-file"},
		"disallowed-file.zip":  {"package.disallowed-file"},
		"disallowed-asset.zip": {"package.disallowed-file"},
		"hidden-file.zip":      {"package.hidden-file"},
		"duplicate.zip":        {"package.duplicate"},
		"no-manifest.zip":      {"package.no-manifest"},
		"no-wasm.zip":          {"package.no-wasm"},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(archives, name))
			if err != nil {
				t.Fatalf("%v (run go test ./internal/plugin -run TestMaliciousArchives -update)", err)
			}
			root := t.TempDir()
			dest := filepath.Join(root, "a", "b", "c", "staging")
			os.MkdirAll(dest, 0o755)
			err = ExtractPackage(bytes.NewReader(b), int64(len(b)), dest)
			var pe *PackageError
			if !errors.As(err, &pe) || !slices.Contains(rules, pe.Rule) {
				t.Fatalf("got %v, want a %v refusal", err, rules)
			}
			// Nothing escaped the staging directory, and no file is larger
			// than its header claimed.
			filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if rel, _ := filepath.Rel(dest, p); strings.HasPrefix(rel, "..") && !d.IsDir() {
					t.Errorf("file written outside the staging directory: %s", p)
				}
				if fi, err := d.Info(); err == nil && !d.IsDir() && fi.Size() > maxAssetBytes {
					t.Errorf("%s: %d bytes", p, fi.Size())
				}
				return nil
			})
			// The same archive through ScanPackage gives a blocked report.
			rep, err := ScanPackage(context.Background(), bytes.NewReader(b), int64(len(b)), ScanOptions{}, "")
			if err != nil || !rep.Blocked() || !rep.Has(pe.Rule) || rep.PackageSHA256 == "" {
				t.Fatalf("ScanPackage: %v %+v", err, rep)
			}
		})
	}
}

func TestExtractPackage(t *testing.T) {
	extract := func(b []byte) (string, error) {
		dest := t.TempDir()
		return dest, ExtractPackage(bytes.NewReader(b), int64(len(b)), dest)
	}
	files := func(dir string) []string {
		var out []string
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if !d.IsDir() {
				rel, _ := filepath.Rel(dir, p)
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		})
		slices.Sort(out)
		return out
	}

	// A package zipped with its folder, with macOS litter, is accepted.
	wasm := respond(okHTML).bytes()
	b := buildZip(t,
		zipEntry{name: "hello-1.0/", mode: fs.ModeDir | 0o755},
		zipEntry{name: "hello-1.0/plugin.json", body: []byte(`{"abi": 1, "id": "hello"}`)},
		zipEntry{name: "hello-1.0/plugin.wasm", body: wasm},
		zipEntry{name: "hello-1.0/assets/site.css", body: []byte("a{}")},
		zipEntry{name: "hello-1.0/.DS_Store", body: []byte("x")},
		zipEntry{name: "__MACOSX/hello-1.0/._plugin.json", body: []byte("x")})
	dest, err := extract(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := files(dest); !slices.Equal(got, []string{"assets/site.css", "plugin.json", "plugin.wasm"}) {
		t.Fatalf("extracted %v", got)
	}

	// A custom module name from the manifest.
	b = buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "hello", "wasm": "hello.wasm"}`)}, zipEntry{name: "hello.wasm", body: wasm})
	if dest, err := extract(b); err != nil || !slices.Equal(files(dest), []string{"hello.wasm", "plugin.json"}) {
		t.Fatalf("custom wasm name: %v", err)
	}

	var many []zipEntry
	for i := range maxPackageEntries + 1 {
		many = append(many, zipEntry{name: "assets/" + strings.Repeat("a", i+1) + ".txt"})
	}
	for name, tc := range map[string]struct {
		b    []byte
		rule string
	}{
		"not a zip":     {[]byte("PK\x03\x04 not really"), "package.invalid"},
		"too many":      {buildZip(t, many...), "package.too-many-entries"},
		"backslash":     {buildZip(t, zipEntry{name: `assets\..\..\x.css`}), "package.zip-slip"},
		"drive letter":  {buildZip(t, zipEntry{name: `C:/x.css`}), "package.zip-slip"},
		"control chars": {buildZip(t, zipEntry{name: "assets/a\x01.css"}), "package.bad-name"},
		"empty segment": {buildZip(t, zipEntry{name: "assets//a.css"}), "package.bad-name"},
		"two roots": {buildZip(t, zipEntry{name: "a/plugin.json", body: []byte(`{}`)}, zipEntry{name: "a/plugin.wasm", body: wasm},
			zipEntry{name: "b/x.css"}), "package.no-manifest"},
		"source files": {buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{}`)}, zipEntry{name: "plugin.wasm", body: wasm},
			zipEntry{name: "main.go"}), "package.disallowed-file"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extract(tc.b)
			var pe *PackageError
			if !errors.As(err, &pe) || pe.Rule != tc.rule {
				t.Fatalf("got %v, want %s", err, tc.rule)
			}
		})
	}

	// The size cap applies before the archive is read.
	if err := ExtractPackage(bytes.NewReader(nil), MaxPackageBytes+1, t.TempDir()); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized archive: %v", err)
	}
}

func TestPack(t *testing.T) {
	dir := writePlugin(t, manifestFor(`"hooks": ["shortcode"], "shortcodes": ["t"], "assets": {"css": ["assets/css/site.css"]}`), respond(okHTML).bytes(),
		map[string]string{"README.md": "# T\n", "LICENSE": "MIT\n", "CHANGELOG.md": "## 1.0.0\n", "assets/css/site.css": "a{}",
			"assets/logo.svg": "<svg></svg>", "main.go": "package main", "Makefile": "all:", "main_test.go": "package main"})
	var a, b bytes.Buffer
	m, err := Pack(dir, &a)
	if err != nil || m.ID != "t1" {
		t.Fatal(m, err)
	}
	if _, err := Pack(dir, &b); err != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("packing the same files twice gives different archives")
	}
	zr, err := zip.NewReader(bytes.NewReader(a.Bytes()), int64(a.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	want := []string{"plugin.json", "plugin.wasm", "CHANGELOG.md", "LICENSE", "README.md", "assets/css/site.css", "assets/logo.svg"}
	if !slices.Equal(names, want) {
		t.Fatalf("packed %v, want %v", names, want)
	}
	rep, err := ScanPackage(context.Background(), bytes.NewReader(a.Bytes()), int64(a.Len()), ScanOptions{}, "")
	if err != nil || rep.Verdict != VerdictClean || rep.PackageSHA256 == "" {
		t.Fatalf("scan of the packed plugin: %v %s", err, dumpReport(rep))
	}

	// Symbolic links and files outside the allowlist are refused.
	os.Symlink("/etc/passwd", filepath.Join(dir, "assets", "passwd.txt"))
	if _, err := Pack(dir, &bytes.Buffer{}); err == nil {
		t.Fatal("packed a symbolic link")
	}
	os.Remove(filepath.Join(dir, "assets", "passwd.txt"))
	os.WriteFile(filepath.Join(dir, "assets", "run.sh"), []byte("#!/bin/sh"), 0o644)
	if _, err := Pack(dir, &bytes.Buffer{}); err == nil {
		t.Fatal("packed a disallowed asset")
	}
}

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0}, {"1.0.1", "1.0.0", 1}, {"1.10.0", "1.9.0", 1}, {"2.0.0", "10.0.0", -1},
		{"1.0.0-beta.1", "1.0.0", -1}, {"1.0.0-beta.2", "1.0.0-beta.10", -1}, {"1.0.0-beta.10", "1.0.0-beta.10.1", -1},
		{"1.0.0-rc.1", "1.0.0-beta.10", 1}, {"1.0.0-1", "1.0.0-alpha", -1}, {"1.0.0+build.5", "1.0.0", 0},
	} {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestPinnedCapabilities(t *testing.T) {
	m, err := ParseManifest([]byte(`{"abi": 1, "id": "pin", "name": "P", "version": "1.0.0", "permissions": ["kv"],
		"hooks": ["shortcode", "widget", "form.submit"], "shortcodes": ["tip"], "widgets": [{"id": "recent", "title": "Recent"}],
		"forms": [{"id": "contact", "fields": [{"name": "email", "label": "E-mail", "type": "email", "required": true}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(m.Pinned(), " ")
	for _, want := range []string{"kv", "hook:form.submit", "hook:shortcode", "hook:widget", "shortcode:tip", "widget:recent", "form:contact:"} {
		if !strings.Contains(got, want) {
			t.Errorf("pinned %q lacks %q", got, want)
		}
	}
	form := m.Capabilities()[0]
	m.Forms[0].Fields[0].Label = LocalText{"en": "Your e-mail"}
	if m.Capabilities()[0] != form {
		t.Error("a new label changed the form pin")
	}
	m.Forms[0].Fields[0].Type = "textarea"
	if m.Capabilities()[0] == form {
		t.Error("a new field type kept the form pin")
	}
	// Separators inside choices cannot make two field lists look alike.
	one := FormDecl{ID: "f", Fields: []FormField{{Name: "a", Type: "select", Choices: []string{"x\x00y"}}}}
	two := FormDecl{ID: "f", Fields: []FormField{{Name: "a", Type: "select", Choices: []string{"x", "y"}}}}
	if (&Manifest{Forms: []FormDecl{one}}).Capabilities()[0] == (&Manifest{Forms: []FormDecl{two}}).Capabilities()[0] {
		t.Error("different choices share a form pin")
	}
}

func TestInstallLifecycle(t *testing.T) {
	st := testutil.Store(t)
	dir := t.TempDir()
	h := newTestHost(t, st, dir)
	uid := adminUser(t, st)
	ctx := context.Background()
	install := func(b []byte, opts InstallOptions) (*InstallResult, error) {
		return h.Install(ctx, bytes.NewReader(b), int64(len(b)), opts)
	}
	leftovers := func() {
		t.Helper()
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), ".") {
				t.Fatalf("left behind %s", e.Name())
			}
		}
	}

	// A new package is installed disabled, with its provenance.
	res, err := install(testPackage(t, "life", "1.0.0", "v1"), InstallOptions{UserID: uid, Source: "upload"})
	if err != nil || res.ID != "life" || res.Previous != "" || res.Report.Verdict != VerdictClean {
		t.Fatalf("install: %v %+v", err, res)
	}
	p := h.Get("life")
	if p == nil || p.Status != StatusAvailable || p.Record == nil || p.Record.Source != "upload" ||
		p.Record.UploadedBy == nil || *p.Record.UploadedBy != uid || p.Report == nil || p.Record.ReviewRequired {
		t.Fatalf("after install: %+v", p)
	}
	leftovers()
	if err := h.Approve(ctx, "life", strings.Repeat("0", 64), uid); err == nil {
		t.Fatal("approved a build that was not the one reviewed")
	}
	if err := h.Approve(ctx, "life", p.SHA256, uid); !errors.Is(err, ErrChangedSinceReview) {
		t.Fatalf("approved with the module hash instead of the review pin: %v", err)
	}
	if err := h.Approve(ctx, "life", p.ReviewPin(), uid); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveSettings(ctx, "life", map[string]string{"greeting": "Hola"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PluginKVSet(ctx, "life", "count", []byte("7"), 1<<20, 1000); err != nil {
		t.Fatal(err)
	}

	// The same build with a new version stays enabled.
	res, err = install(testPackage(t, "life", "1.1.0", "v1"), InstallOptions{UserID: uid})
	if err != nil || res.Previous != "1.0.0" || h.Get("life").Status != StatusEnabled {
		t.Fatalf("update with the same build: %v %+v %s", err, res, h.Get("life").Status)
	}

	// The same build asking for another hook needs a new approval.
	hooked := buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "life", "name": "Test", "version": "1.1.1",
		"author": "Alice", "hooks": ["shortcode", "event"], "events": ["post.published"], "shortcodes": ["life"],
		"settings": [{"key": "greeting", "label": "Greeting", "type": "text", "default": "Hi"}]}`)},
		zipEntry{name: "plugin.wasm", body: respond(`{"html":"v1"}`).bytes()},
		zipEntry{name: "README.md", body: []byte("# Test\n")}, zipEntry{name: "LICENSE", body: []byte("MIT\n")})
	if _, err := install(hooked, InstallOptions{UserID: uid}); err != nil {
		t.Fatal(err)
	}
	if p := h.Get("life"); p.Status != StatusModified || p.SHA256 != h.Get("life").Record.WasmSHA256 {
		t.Fatalf("a new hook on the same build: %s", p.Status)
	}

	// A new build keeps settings and data but needs a new approval.
	if _, err := install(testPackage(t, "life", "1.2.0", "v2"), InstallOptions{UserID: uid}); err != nil {
		t.Fatal(err)
	}
	p = h.Get("life")
	if p.Status != StatusModified || p.Version != "1.2.0" || p.Record.Settings["greeting"] != "Hola" {
		t.Fatalf("after a changed build: %s %s %v", p.Status, p.Version, p.Record.Settings)
	}
	if v, _ := st.PluginKVGet(ctx, "life", "count"); string(v) != "7" {
		t.Fatalf("kv after update: %q", v)
	}

	// Older versions need an explicit confirmation.
	if _, err := install(testPackage(t, "life", "1.0.0", "v1"), InstallOptions{UserID: uid}); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgrade: %v", err)
	}
	if h.Get("life").Version != "1.2.0" {
		t.Fatal("a refused downgrade replaced the files")
	}
	leftovers()
	if res, err := install(testPackage(t, "life", "1.0.0", "v1"), InstallOptions{UserID: uid, AllowDowngrade: true}); err != nil || res.Previous != "1.2.0" {
		t.Fatalf("confirmed downgrade: %v", err)
	}

	// Uninstalling needs the plugin disabled; data can be kept.
	if err := h.Uninstall(ctx, "life", false); !errors.Is(err, ErrPluginEnabled) {
		t.Fatalf("uninstall while enabled: %v", err)
	}
	if err := h.Disable(ctx, "life"); err != nil {
		t.Fatal(err)
	}
	if err := h.Uninstall(ctx, "life", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "life")); !os.IsNotExist(err) || h.Get("life") != nil {
		t.Fatal("files still there after uninstall")
	}
	if recs, _ := st.ListPluginRecords(ctx); recs["life"].Settings["greeting"] != "Hola" {
		t.Fatal("settings not kept")
	}
	// A reinstall inherits the plain settings and data, not the secrets,
	// e-mail recipients or approval, and the report says so.
	if err := st.SavePluginSettings(ctx, "life", map[string]string{"greeting": "Hola", "token": secretPrefix + "c2VhbGVk",
		"notify": "owner@example.test", emailMark + "notify": "1"}); err != nil {
		t.Fatal(err)
	}
	res, err = install(testPackage(t, "life", "1.0.0", "v1"), InstallOptions{UserID: uid})
	if err != nil || !res.Report.Has("manifest.retained-data") || !res.Report.NeedsConfirmation() {
		t.Fatalf("reinstall over kept data: %v %s", err, dumpReport(res.Report))
	}
	p = h.Get("life")
	if want := map[string]string{"greeting": "Hola"}; !maps.Equal(p.Record.Settings, want) {
		t.Fatalf("reinstall inherited %v", p.Record.Settings)
	}
	if p.Status != StatusAvailable || p.Record.WasmSHA256 != "" || len(p.Record.GrantedPermissions) != 0 || p.Record.ApprovedBy != nil {
		t.Fatalf("reinstall kept the approval: %s %+v", p.Status, p.Record)
	}
	if v, _ := st.PluginKVGet(ctx, "life", "count"); string(v) != "7" {
		t.Fatalf("kv after reinstall: %q", v)
	}
	// An update of an installed plugin is not a reinstall.
	if res, err := install(testPackage(t, "life", "1.2.0", "v2"), InstallOptions{UserID: uid}); err != nil || res.Report.Has("manifest.retained-data") {
		t.Fatalf("update after reinstall: %v %s", err, dumpReport(res.Report))
	}
	if h.Get("life").Record.Settings["greeting"] != "Hola" {
		t.Fatal("update lost the settings")
	}
	if err := h.Uninstall(ctx, "life", false); err != nil {
		t.Fatal(err)
	}
	if _, err := install(testPackage(t, "life", "1.2.0", "v2"), InstallOptions{UserID: uid}); err != nil {
		t.Fatal(err)
	}
	if err := h.Uninstall(ctx, "life", true); err != nil {
		t.Fatal(err)
	}
	if recs, _ := st.ListPluginRecords(ctx); recs["life"].ID != "" {
		t.Fatal("record kept after uninstall with data")
	}
	if v, _ := st.PluginKVGet(ctx, "life", "count"); v != nil {
		t.Fatal("kv kept after uninstall with data")
	}
	leftovers()

	// Refused packages leave nothing behind.
	bad := maliciousArchives(t)["zip-slip.zip"]
	if res, err := install(bad, InstallOptions{UserID: uid}); !errors.Is(err, ErrRefused) || !res.Report.Has("package.zip-slip") {
		t.Fatalf("zip-slip: %v", err)
	}
	evil := buildZip(t, zipEntry{name: "plugin.json", body: []byte(`{"abi": 1, "id": "spy", "name": "Spy", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["spy"]}`)},
		zipEntry{name: "plugin.wasm", body: respondWith(okHTML, []byte("http://169.254.169.254/latest/")).bytes()})
	if res, err := install(evil, InstallOptions{UserID: uid}); !errors.Is(err, ErrRefused) || !res.Report.Has("strings.cloud-metadata") {
		t.Fatalf("scanner refusal: %v", err)
	}
	if h.Get("spy") != nil {
		t.Fatal("refused plugin installed")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("refused packages left %v", ents)
	}

	// Tenant uploads wait for the operator.
	if _, err := install(testPackage(t, "tenant", "1.0.0", "x"), InstallOptions{UserID: uid, ReviewRequired: true}); err != nil {
		t.Fatal(err)
	}
	if err := h.Approve(ctx, "tenant", "", uid); !errors.Is(err, ErrReviewRequired) {
		t.Fatalf("approve before review: %v", err)
	}
	if err := st.ClearPluginReview(ctx, "tenant"); err != nil {
		t.Fatal(err)
	}
	h.Load(ctx)
	if err := h.Approve(ctx, "tenant", "", uid); err != nil {
		t.Fatal(err)
	}

	// Rescan stores a fresh report.
	rep, err := h.Rescan(ctx, "tenant")
	if err != nil || rep.Verdict != VerdictClean || h.Get("tenant").Report == nil {
		t.Fatalf("rescan: %v", err)
	}
}
