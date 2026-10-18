package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// addMedia stores a media row. Lookups only need the row, not a file.
func (e *customEnv) addMedia(mime, name string) int64 {
	e.t.Helper()
	m := &store.Media{Path: "2026/10/" + name, OriginalName: name, MIME: mime, Size: 100, SHA256: name,
		Variants: map[string]store.MediaVariant{}}
	if strings.HasPrefix(mime, "image/") {
		m.Width, m.Height = 64, 64
	}
	id, err := e.st.CreateMedia(context.Background(), m)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *customEnv) exec(sql string) {
	e.t.Helper()
	if _, err := e.st.DB.Exec(context.Background(), sql); err != nil {
		e.t.Fatal(err)
	}
}

// cachedEntry returns the theme's cached published design, if current.
func (e *customEnv) cachedEntry() *customEntry {
	c := &e.s.custom
	c.mu.Lock()
	defer c.mu.Unlock()
	if en := c.themes[e.slug]; en != nil && en.gen == c.gen {
		return en
	}
	return nil
}

// importFile posts an export file to the customiser's import, asking for JSON.
func (e *customEnv) importFile(body string) (*httptest.ResponseRecorder, string) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ := mw.CreateFormFile("file", "design.json")
	io.WriteString(fw, body)
	mw.Close()
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	rec := e.do(req, e.admin)
	var res struct {
		Message string `json:"message"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res.Message
}

// A media lookup that fails for any reason but "not found" never strips
// the fonts, the logo or the images from what visitors get. The cache is
// filled without the request's cancellation, a failed fill is neither
// cached nor lets its page into the page cache, and nothing is saved or
// published from one.
func TestCustomizeMediaLookupFailures(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	th, _ := e.s.themes.Get(e.slug)
	font, logo, pic := e.addMedia("font/woff2", "brand.woff2"), e.addMedia("image/png", "logo.png"), e.addMedia("image/png", "pic.png")
	design := fmt.Sprintf(`{"fonts": {"body": {"stack": "media", "media": %d}}, "header": {"brand": "logo", "logo": %d}}`, font, logo)
	if rec, _ := e.draft(design); rec.Code != 200 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if rec := e.post("/publish", e.admin, e.csrf); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	layout := fmt.Sprintf(`{"layout": {"pages": {"home": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "img", "type": "image", "settings": {"image": "%d"}}]]}]}}}}`, pic)
	if rec, _ := e.layout(layout); rec.Code != 200 {
		t.Fatalf("layout: %d %s", rec.Code, rec.Body)
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish layout: %d", rec.Code)
	}
	whole := func(en *customEntry) bool {
		return en.logo != nil && strings.Contains(en.css, "@font-face") && en.mediaByID(pic) != nil
	}

	// A visitor who has already gone away still fills the cache whole.
	e.s.custom.invalidate()
	gone, cancel := context.WithCancel(ctx)
	cancel()
	en := e.s.publishedDesign(gone, th)
	if !whole(en) || e.cachedEntry() != en {
		t.Errorf("fill with a cancelled request: whole %v, cached %v", whole(en), e.cachedEntry() == en)
	}
	css := en.url

	// While media cannot be read, the page is served but cached nowhere.
	e.s.custom.invalidate()
	e.s.contentChanged()
	if rec, _ := e.draft(`{"light": {"primary": "#123456"}, ` + design[1:]); rec.Code != 200 {
		t.Fatalf("second draft: %d", rec.Code)
	}
	e.exec(`ALTER TABLE media RENAME TO media_away`)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			e.exec(`ALTER TABLE media_away RENAME TO media`)
		}
	}
	defer restore()
	if en := e.s.publishedDesign(ctx, th); !en.partial || e.cachedEntry() != nil {
		t.Errorf("a failed fill: partial %v, cached %v", en.partial, e.cachedEntry() != nil)
	}
	for i := 0; i < 2; i++ {
		rec := e.get("/no-such-page", nil)
		if rec.Code != http.StatusNotFound || rec.Header().Get("X-Cache") != "MISS" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("visit %d during the failure: %d, X-Cache %q, Cache-Control %q", i, rec.Code, rec.Header().Get("X-Cache"), rec.Header().Get("Cache-Control"))
		}
	}
	if rec := e.get(css, nil); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("stylesheet during the failure: %d %v", rec.Code, rec.Header())
	}
	// Nothing is saved or published from a failed lookup.
	before := e.row()
	if rec, _ := e.draft(design); rec.Code != http.StatusInternalServerError {
		t.Errorf("draft during the failure: %d", rec.Code)
	}
	if rec := e.post("/publish", e.admin, e.csrf); rec.Code != http.StatusInternalServerError {
		t.Errorf("publish during the failure: %d", rec.Code)
	}
	if rec, _ := e.layout(layout); rec.Code != http.StatusInternalServerError {
		t.Errorf("layout draft during the failure: %d", rec.Code)
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusInternalServerError {
		t.Errorf("layout publish during the failure: %d", rec.Code)
	}
	after := e.row()
	if string(after.Published) != string(before.Published) || string(after.Draft) != string(before.Draft) {
		t.Errorf("a failed lookup changed the design: %s → %s", before.Published, after.Published)
	}

	// Once media can be read again, the next visit caches the whole design.
	restore()
	if en := e.s.publishedDesign(ctx, th); !whole(en) || e.cachedEntry() != en {
		t.Errorf("after the failure: whole %v, cached %v", whole(en), e.cachedEntry() == en)
	}
	e.get("/no-such-page", nil)
	if rec := e.get("/no-such-page", nil); rec.Header().Get("X-Cache") != "HIT" || !strings.Contains(rec.Body.String(), "/uploads/2026/10/logo.png") {
		t.Errorf("after the failure: X-Cache %q", rec.Header().Get("X-Cache"))
	}
	if rec := e.get(css, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/uploads/2026/10/brand.woff2") {
		t.Errorf("stylesheet after the failure: %d", rec.Code)
	}
}

// Fonts end up in the site's stylesheet, so only users who may change the
// site's look can upload them.
func TestFontUploadsNeedAppearance(t *testing.T) {
	e := newCustomEnv(t)
	upload := func(cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("csrf", csrf)
		fw, _ := mw.CreateFormFile("file", "brand.woff2")
		fw.Write(append([]byte("wOF2\x00\x01\x00\x00"), bytes.Repeat([]byte{0}, 64)...))
		mw.Close()
		req := httptest.NewRequest("POST", "http://example.test/admin/media", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		return e.do(req, cookie)
	}
	editor, ecsrf := e.login("editor", auth.RoleEditor)
	for name, who := range map[string]struct {
		c    *http.Cookie
		csrf string
	}{"author": {e.author, e.acsrf}, "editor": {editor, ecsrf}} {
		rec := upload(who.c, who.csrf)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), e.s.t("Only users who can change the site's appearance can upload fonts.")) {
			t.Errorf("%s uploads a font: %d %s", name, rec.Code, rec.Body)
		}
	}
	if fonts, n, _ := e.st.ListMedia(context.Background(), store.MediaQuery{MIME: "font/woff2", Limit: 10}); n != 0 {
		t.Errorf("refused fonts were stored: %v", fonts)
	}
	if rec := upload(e.admin, e.csrf); rec.Code != 200 {
		t.Errorf("admin uploads a font: %d %s", rec.Code, rec.Body)
	}
}

// Imports apply only what the file carries, and their errors are capped
// for the flash cookie.
func TestCustomizeImportScope(t *testing.T) {
	e := newCustomEnv(t)
	if rec, _ := e.draft(`{"light": {"primary": "#123456"}}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	design := string(e.row().Draft)

	// A layouts-only file leaves the design draft alone.
	rec, msg := e.importFile(`{"format": "sap13ns-theme-custom", "layout": {"pages": {"home": {"sections": []}}}}`)
	if rec.Code != 200 {
		t.Fatalf("layouts-only import: %d %s", rec.Code, msg)
	}
	if got := string(e.row().Draft); got != design {
		t.Errorf("a layouts-only import changed the design draft: %s → %s", design, got)
	}
	if e.layoutRow().Draft == nil {
		t.Error("the layouts were not imported")
	}
	// A design-only file leaves the layouts draft alone.
	e.builderPost("/discard")
	if rec, msg := e.importFile(`{"format": "sap13ns-theme-custom", "design": {"light": {"primary": "#654321"}}}`); rec.Code != 200 {
		t.Fatalf("design-only import: %d %s", rec.Code, msg)
	}
	if !strings.Contains(string(e.row().Draft), "#654321") || e.layoutRow().Draft != nil {
		t.Errorf("design-only import: design %s, layouts %s", e.row().Draft, e.layoutRow().Draft)
	}
	// A file with neither is refused.
	if rec, _ := e.importFile(`{"format": "sap13ns-theme-custom", "design": null}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty import: %d", rec.Code)
	}

	// Seven problems: five are listed, then a count.
	bad := `{"design": {"light": {"background": "a", "foreground": "b", "card": "c", "muted": "d", "border": "e", "primary": "f", "accent": "g"}}}`
	rec, msg = e.importFile(bad)
	if rec.Code != http.StatusUnprocessableEntity || strings.Count(msg, "use a hex colour") != 5 || !strings.HasSuffix(msg, "; "+e.s.t("and %d more", 2)) {
		t.Errorf("capped errors: %d %q", rec.Code, msg)
	}
}

// Drafts follow the admin's own second-factor rule: a customiser who still
// has to set it up gets the published design, like the admin redirects them.
func TestCustomizeDraftNeedsSecondFactor(t *testing.T) {
	e := newCustomEnv(t)
	rec, res := e.draft(`{"footer": {"text": "Draft footer"}}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if !strings.Contains(e.get("/?customize=draft", e.admin).Body.String(), "Draft footer") {
		t.Fatal("no draft in the preview")
	}
	e.s.cfg.DevSkip2FA = false // the admin has no second factor
	if strings.Contains(e.get("/?customize=draft", e.admin).Body.String(), "Draft footer") {
		t.Error("draft shown to an admin who must set up two-factor authentication")
	}
	if rec := e.get(res.CSSURL, e.admin); rec.Code != http.StatusNotFound {
		t.Errorf("draft stylesheet: %d", rec.Code)
	}
}
