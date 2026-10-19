package web

import (
	"archive/zip"
	"bytes"
	"context"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/theme"
	"sap13ns/internal/themeimport"
)

var importTokenRe = regexp.MustCompile(`name="token" value="([0-9a-f]{32})"`)

// TestThemeImportFlow: an uploaded WordPress block theme is converted and
// shown with its report; installing it goes through the checks of any
// upload; the converted theme renders every page within the strict CSP.
func TestThemeImportFlow(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	ctx := context.Background()
	wp := fixtureZip(t)

	rec := e.uploadTheme("fixture.zip", wp)
	b := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, b)
	}
	for _, want := range []string{"Converted from WordPress: Fixture", "wp-fixture", "The navigation block becomes the menu location",
		"templates/archive.html", `action="/admin/appearance/themes/import"`, `name="csrf"`, "Install the converted theme"} {
		if !strings.Contains(b, want) {
			t.Errorf("the report does not show %q", want)
		}
	}
	m := importTokenRe.FindStringSubmatch(b)
	if m == nil {
		t.Fatal("no token")
	}
	if e.audited("theme.converted") != 1 || e.audited("theme.uploaded") != 0 {
		t.Fatal("converting installed the theme, or was not audited")
	}
	if _, ok := e.s.themes.Get("wp-fixture"); ok {
		t.Fatal("installed before the confirmation")
	}
	// Without the CSRF token: refused.
	form := url.Values{"token": {m[1]}, "action": {"install"}}
	if rec := e.do("POST", "/admin/appearance/themes/import", strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", e.cookie); rec.Code == http.StatusSeeOther {
		t.Fatalf("installed without the CSRF token: %d", rec.Code)
	}
	rec = e.admin("/admin/appearance/themes/import", form)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/appearance/themes#theme-wp-fixture" {
		t.Fatalf("install: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, ok := e.s.themes.Get("wp-fixture"); !ok || e.audited("theme.uploaded") != 1 {
		t.Fatal("not installed or not audited")
	}
	// The token works once.
	rec = e.admin("/admin/appearance/themes/import", form)
	if rec.Code != http.StatusSeeOther || e.audited("theme.uploaded")+e.audited("theme.updated") != 1 {
		t.Fatalf("the token installed twice: %d", rec.Code)
	}
	if p := e.do("GET", "/admin/appearance/themes", nil, "", e.cookie).Body.String(); !strings.Contains(p, "Converted from WordPress") {
		t.Error("the list does not say the theme was converted")
	}

	// Cancel drops the conversion.
	b = e.uploadTheme("fixture.zip", wp).Body.String()
	m = importTokenRe.FindStringSubmatch(b)
	if m == nil || !strings.Contains(b, "It replaces an installed theme") {
		t.Fatal("no replacement warning")
	}
	e.admin("/admin/appearance/themes/import", url.Values{"token": {m[1]}, "action": {"cancel"}})
	if rec := e.admin("/admin/appearance/themes/import", url.Values{"token": {m[1]}, "action": {"install"}}); e.audited("theme.updated") != 0 || rec.Code != http.StatusSeeOther {
		t.Fatal("a cancelled conversion was installed")
	}

	// A theme sap13ns cannot convert is refused with a reason.
	var cb bytes.Buffer
	zw := zip.NewWriter(&cb)
	for _, n := range []string{"classic/style.css", "classic/index.php"} {
		w, _ := zw.Create(n)
		w.Write([]byte("/* Theme Name: Classic */"))
	}
	zw.Close()
	if rec := e.uploadTheme("classic.zip", cb.Bytes()); rec.Code != http.StatusSeeOther || e.audited("theme.refused") != 0 {
		t.Fatalf("classic: %d", rec.Code)
	}

	// Active, the converted theme renders its pages under the CSP.
	at := time.Now().Add(-time.Hour)
	u, err := e.st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Imported hello",
		Slug: "imported-hello", Lang: e.s.mainLang(), Content: "Some **text**.", AuthorID: u.ID, CommentStatus: "open", PublishedAt: &at}); err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	if rec := e.admin("/admin/appearance/themes/wp-fixture/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}
	home := e.do("GET", "/", nil, "")
	if home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "Imported hello") || !strings.Contains(home.Body.String(), theme.CreditMarker) {
		t.Fatalf("home: %d", home.Code)
	}
	if csp := home.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("CSP: %q", csp)
	}
	post := regexp.MustCompile(`href="(/[^"]*imported-hello[^"]*)"`).FindStringSubmatch(home.Body.String())
	if post == nil {
		t.Fatal("no link to the post")
	}
	for _, target := range []string{"/", post[1], "/search?q=hello", "/no-such-page"} {
		rec := e.do("GET", target, nil, "")
		body := rec.Body.String()
		if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", target, rec.Code)
		}
		for _, bad := range []string{"<script>", " style=\"", " onclick=", "evil.example"} {
			if strings.Contains(body, bad) {
				t.Errorf("%s has %q", target, bad)
			}
		}
		if !strings.Contains(body, theme.CreditMarker) {
			t.Errorf("%s has no footer credit", target)
		}
	}
	if b := e.do("GET", post[1], nil, "").Body.String(); !strings.Contains(b, "Some <strong>text</strong>.") || !strings.Contains(b, "wp-block-post-content") {
		t.Error("the post does not render its content in the converted template")
	}
}

// The plan's theme_uploads gate holds for conversions too.
func TestThemeImportPlan(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	lim := &themeLimits{policy: ThemePolicy{Plan: "Personal"}}
	e.s.limits = lim
	if rec := e.uploadTheme("fixture.zip", fixtureZip(t)); rec.Code != http.StatusForbidden || e.audited("theme.converted") != 0 {
		t.Fatalf("Personal: %d", rec.Code)
	}
	lim.policy = ThemePolicy{Plan: "Pro", Uploads: true}
	m := importTokenRe.FindStringSubmatch(e.uploadTheme("fixture.zip", fixtureZip(t)).Body.String())
	if m == nil {
		t.Fatal("no token")
	}
	lim.policy = ThemePolicy{Plan: "Personal"} // a downgrade before the confirmation
	if rec := e.admin("/admin/appearance/themes/import", url.Values{"token": {m[1]}, "action": {"install"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("after a downgrade: %d", rec.Code)
	}
	lim.policy = ThemePolicy{Plan: "Pro", Uploads: true}
	m = importTokenRe.FindStringSubmatch(e.uploadTheme("fixture.zip", fixtureZip(t)).Body.String())
	if m == nil {
		t.Fatal("no token")
	}
	if rec := e.admin("/admin/appearance/themes/import", url.Values{"token": {m[1]}, "action": {"install"}}); rec.Code != http.StatusSeeOther || lim.reserved <= 0 {
		t.Fatalf("Pro: %d, %d bytes counted", rec.Code, lim.reserved)
	}
}

// A conversion waits for its own user only, and not for ever.
func TestConvertedThemes(t *testing.T) {
	var c convertedThemes
	for i := range maxConverted + 1 {
		c.put(&convertedTheme{token: string(rune('a' + i)), user: 1, created: time.Now()})
	}
	if c.take("a", 1) != nil {
		t.Error("the oldest beyond the bound was kept")
	}
	if c.take("b", 2) != nil {
		t.Error("another user took a conversion")
	}
	if c.take("b", 1) == nil || c.take("b", 1) != nil {
		t.Error("a conversion is taken once")
	}
	c.put(&convertedTheme{token: "old", user: 1, created: time.Now().Add(-convertedTTL - time.Minute)})
	if c.take("old", 1) != nil {
		t.Error("an expired conversion was kept")
	}
}

// convertAndInstall converts a WordPress block theme and installs it
// with the checks of uploaded themes.
func convertAndInstall(t *testing.T, m *theme.Manager, src []byte) (*themeimport.Result, *theme.InstallResult) {
	t.Helper()
	res, err := themeimport.Convert(bytes.NewReader(src), int64(len(src)), themeimport.Options{})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	pkg, err := res.Zip()
	if err != nil {
		t.Fatal(err)
	}
	ir, err := install(m, pkg, nil)
	if ref, ok := theme.IsRefusal(err); ok {
		var b strings.Builder
		for _, p := range ref.Problems {
			b.WriteString("\n  " + p.Error())
		}
		t.Fatalf("the converted theme %s was refused:%s", res.Slug, b.String())
	}
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	return res, ir
}

// fixtureZip archives internal/themeimport/testdata/fixture, a small
// block theme, under its folder.
func fixtureZip(t *testing.T) []byte {
	t.Helper()
	root := filepath.Join("..", "themeimport", "testdata", "fixture")
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := w.Create("fixture/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The fixture converts into a theme that passes every check of uploaded
// themes (manifest, files, CSS, templates, the footer credit) and renders
// each page.
func TestThemeImportFixtureInstalls(t *testing.T) {
	m, _ := themeTestManager(t)
	res, ir := convertAndInstall(t, m, fixtureZip(t))
	if ir.Slug != "wp-fixture" || res.Slug != "wp-fixture" {
		t.Fatalf("slug = %q", ir.Slug)
	}
	th, ok := m.Get("wp-fixture")
	if !ok || !th.Uploaded {
		t.Fatal("the converted theme is not loaded")
	}
	if len(th.Manifest.Options) != 1 || th.Manifest.Options[0].Key != "style" {
		t.Errorf("options: %+v", th.Manifest.Options)
	}
}

// The real themes of SAP13NS_THEMEIMPORT_DIR (WordPress block theme
// archives) convert and pass the checks of uploaded themes. Skipped
// without it: the themes are not part of the repository.
func TestThemeImportRealThemes(t *testing.T) {
	dir := os.Getenv("SAP13NS_THEMEIMPORT_DIR")
	if dir == "" {
		t.Skip("SAP13NS_THEMEIMPORT_DIR is not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.zip"))
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			m, _ := themeTestManager(t)
			res, _ := convertAndInstall(t, m, src)
			t.Logf("%s\n%s", res.Slug, res.Report.Summary())
			if out := os.Getenv("SAP13NS_THEMEIMPORT_OUT"); out != "" {
				pkg, _ := res.Zip()
				os.WriteFile(filepath.Join(out, res.Slug+".zip"), pkg, 0o644)
			}
		})
	}
}
