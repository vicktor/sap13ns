package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/i18n"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/ui"
)

// TestPermSummary: permissions in plain words, with the hosts of http:
// and the sources of each csp: directive on one line each.
func TestPermSummary(t *testing.T) {
	cat, err := i18n.Load(ui.FS, "i18n")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{i18n: cat}
	s.state.Store(&siteState{S: store.Settings{Language: "en"}})
	got := s.permSummary(nil, []string{"kv", "http:a.example", "posts:read", "http:b.example", "csp:frame-src:https://x.example",
		"csp:frame-src:https://y.example", "csp:img-src:https://z.example", "http:*"})
	want := []pluginPerm{
		{Name: "kv", Risk: "low", Help: "Store its own small key-value data."},
		{Name: "http:", Risk: "medium", Help: "Make HTTPS requests to these hosts only: a.example, b.example."},
		{Name: "posts:read", Risk: "low", Help: "Read published posts (never drafts or private data)."},
		{Name: "csp:frame-src", Risk: "high", Help: "Embed frames from https://x.example, https://y.example in your pages."},
		{Name: "csp:img-src:https://z.example", Risk: "high", Help: "Load images from https://z.example."},
		{Name: "http:*", Risk: "high", Help: "Make HTTPS requests to any public address."},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if got := s.permSummary(nil, []string{"http:a.example"}); len(got) != 1 || got[0].Name != "http:a.example" || got[0].Help != "Make HTTPS requests to this host only." {
		t.Fatalf("one host: %+v", got)
	}
}

// officialPackage rewrites the manifest of a package: the official
// plugins' author and licence, the version, and edit.
func officialPackage(t *testing.T, pkg []byte, version string, edit func(map[string]any)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == "plugin.json" {
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			m["version"], m["author"], m["license"] = version, "sap13ns", "MIT"
			if edit != nil {
				edit(m)
			}
			body, _ = json.Marshal(m)
		}
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// officialBundle is a bundle of official plugins with these packages.
func officialBundle(t *testing.T, pkgs ...[]byte) *plugin.Official {
	t.Helper()
	fsys := fstest.MapFS{"README.md": {Data: []byte("# Packages\n")}}
	for i, p := range pkgs {
		fsys[string(rune('a'+i))+".zip"] = &fstest.MapFile{Data: p}
	}
	o, err := plugin.LoadOfficial(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// TestOfficialPlugins: Plugins → Official plugins installs a plugin of the
// bundle as an upload (scanned, disabled, source "official"), offers its
// updates, and lists it again once it is uninstalled. Only the plugins
// capability, with the session's CSRF token, can install.
func TestOfficialPlugins(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	e := newPluginEnv(t)
	ctx := context.Background()
	base := hooksPackage(t)
	page := func() string {
		t.Helper()
		rec := e.do("GET", "/admin/plugins", nil, "", e.cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("plugins page: %d", rec.Code)
		}
		return rec.Body.String()
	}
	const form = `action="/admin/plugins/official/hooks/install"`
	install := func() *http.Response {
		t.Helper()
		return e.admin("/admin/plugins/official/hooks/install", url.Values{}).Result()
	}

	// No bundle: no section, and nothing to install.
	if body := page(); strings.Contains(body, `id="official"`) {
		t.Fatal("an empty bundle shows the section")
	}
	if rec := install(); rec.StatusCode != http.StatusNotFound {
		t.Fatalf("install without a bundle: %d", rec.StatusCode)
	}

	// The bundle is listed with the permissions it asks for.
	v1 := officialPackage(t, base, "1.0.0", nil)
	e.s.official = officialBundle(t, v1)
	body := page()
	for _, want := range []string{`id="official"`, form, "v1.0.0", "sap13ns", "MIT", e.s.t("Read published posts (never drafts or private data)."),
		e.s.t("Embed frames from %s in your pages.", "https://www.youtube-nocookie.com"), e.s.t("High risk.")} {
		if !strings.Contains(body, want) {
			t.Errorf("the official plugins lack %q", want)
		}
	}
	// With nothing installed they come before the upload form.
	if o, u := strings.Index(body, `id="official"`), strings.Index(body, `id="upload"`); u < 0 || o > u {
		t.Errorf("official at %d, upload at %d", o, u)
	}

	// The CSRF token and the plugins capability are required.
	rec := e.do("POST", "/admin/plugins/official/hooks/install", strings.NewReader(""), "application/x-www-form-urlencoded", e.cookie)
	if rec.Code != http.StatusForbidden || e.host.Get("hooks") != nil {
		t.Fatalf("without the CSRF token: %d", rec.Code)
	}
	editor, err := e.st.CreateUser(ctx, store.NewUser{Username: "editor", Email: "editor@example.test", Role: "editor", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := e.st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: editor, CSRFToken: csrf,
		ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	rec = e.do("POST", "/admin/plugins/official/hooks/install", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()),
		"application/x-www-form-urlencoded", &http.Cookie{Name: e.s.cookieName(), Value: token})
	if rec.Code != http.StatusForbidden || e.host.Get("hooks") != nil {
		t.Fatalf("an editor: %d", rec.Code)
	}
	if rec := e.admin("/admin/plugins/official/nope/install", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("not in the bundle: %d", rec.Code)
	}

	// Installed disabled, scanned, from the official plugins.
	if rec := install(); rec.StatusCode != http.StatusSeeOther || rec.Header.Get("Location") != "/admin/plugins/hooks" {
		t.Fatalf("install: %d %s", rec.StatusCode, rec.Header.Get("Location"))
	}
	p := e.host.Get("hooks")
	if p == nil || p.Status != plugin.StatusAvailable || p.Record.Source != plugin.SourceOfficial || p.Record.ScannedAt == nil ||
		p.Report == nil || p.Report.PackageSHA256 != e.s.official.Get("hooks").SHA256 || *p.Record.UploadedBy != e.adminID {
		t.Fatalf("after the install: %+v", p)
	}
	entries, _, err := e.st.ListAudit(ctx, "plugin.installed", 10, 0)
	if err != nil || len(entries) != 1 || entries[0].Details["source"] != plugin.SourceOfficial || entries[0].Details["verdict"] == "" {
		t.Fatalf("audit: %+v %v", entries, err)
	}
	if body := page(); strings.Contains(body, form) {
		t.Fatal("an installed plugin is still offered")
	}
	if body := e.do("GET", "/admin/plugins/hooks", nil, "", e.cookie).Body.String(); !strings.Contains(body, e.s.t("Official plugins")) {
		t.Fatal("the plugin's page does not say where it came from")
	}

	// Approved and enabled like any other.
	approve := func() {
		t.Helper()
		e.admin("/admin/plugins/hooks/approve", url.Values{"pin": {e.host.Get("hooks").ReviewPin()}, "confirm_risk": {"1"}})
		if p := e.host.Get("hooks"); p.Status != plugin.StatusEnabled {
			t.Fatalf("approve: %s %s", p.Status, p.Err)
		}
	}
	approve()

	// A newer version in the bundle is an update; one that asks for more
	// stops the plugin until it is approved again.
	kv := func(m map[string]any) { m["permissions"] = []string{"posts:read", "mail", "kv"} }
	e.s.official = officialBundle(t, officialPackage(t, base, "1.1.0", kv))
	body = page()
	if !strings.Contains(body, form) || !strings.Contains(body, e.s.t("Update available")) || !strings.Contains(body, "v1.0.0") {
		t.Fatal("no update offered")
	}
	// Once something is installed, the installed list comes first.
	if l, o := strings.Index(body, `data-status="enabled"`), strings.Index(body, `id="official"`); l < 0 || o < l {
		t.Errorf("installed list at %d, official at %d", l, o)
	}
	if rec := install(); rec.StatusCode != http.StatusSeeOther {
		t.Fatalf("update: %d", rec.StatusCode)
	}
	if p := e.host.Get("hooks"); p.Version != "1.1.0" || p.Status != plugin.StatusModified || p.Record.Source != plugin.SourceOfficial ||
		e.audited("plugin.updated") != 1 {
		t.Fatalf("after the update: %s %s", p.Version, p.Status)
	}
	if strings.Contains(page(), form) {
		t.Fatal("the update is still offered")
	}
	approve()
	// The same build in a newer version stays enabled.
	e.s.official = officialBundle(t, officialPackage(t, base, "1.2.0", kv))
	install()
	if p := e.host.Get("hooks"); p.Version != "1.2.0" || p.Status != plugin.StatusEnabled {
		t.Fatalf("the same build: %s %s", p.Version, p.Status)
	}
	// An older bundle offers nothing, and does not downgrade.
	e.s.official = officialBundle(t, v1)
	if strings.Contains(page(), form) {
		t.Fatal("an older version is offered")
	}
	install()
	if p := e.host.Get("hooks"); p.Version != "1.2.0" {
		t.Fatalf("downgraded to %s", p.Version)
	}

	// Uninstalled, it is offered again, and installs again.
	e.admin("/admin/plugins/hooks/disable", url.Values{})
	e.admin("/admin/plugins/hooks/uninstall", url.Values{})
	if e.host.Get("hooks") != nil || !strings.Contains(page(), form) {
		t.Fatal("not offered after the uninstall")
	}
	if rec := install(); rec.StatusCode != http.StatusSeeOther {
		t.Fatalf("install again: %d", rec.StatusCode)
	}
	if p := e.host.Get("hooks"); p == nil || p.Status != plugin.StatusAvailable || p.Record.Source != plugin.SourceOfficial {
		t.Fatalf("installed again: %+v", p)
	}

	// A plugin with the same id from elsewhere is neither offered nor
	// replaced.
	e.admin("/admin/plugins/hooks/uninstall", url.Values{"delete_data": {"1"}})
	if rec := e.upload("hooks.zip", base); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", rec.Code)
	}
	if strings.Contains(page(), form) {
		t.Fatal("offered over an upload")
	}
	install()
	if p := e.host.Get("hooks"); p.Record.Source != "upload" {
		t.Fatalf("an upload replaced: %s", p.Record.Source)
	}
}
