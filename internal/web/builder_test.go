package web

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/builder"
	"sap13ns/internal/store"
)

// layout saves layouts as the draft the way builder.js does.
func (e *customEnv) layout(body string) (*httptest.ResponseRecorder, layoutResult) {
	e.t.Helper()
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder/draft", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", e.csrf)
	rec := e.do(req, e.admin)
	var res layoutResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func (e *customEnv) builderPost(action string) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{"csrf": {e.csrf}}
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder"+action, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, e.admin)
}

func (e *customEnv) layoutRow() *store.ThemeCustom {
	e.t.Helper()
	row, err := e.st.ThemeCustom(context.Background(), e.slug, store.CustomLayout)
	if err != nil {
		e.t.Fatal(err)
	}
	return row
}

// post publishes a post and returns its path.
func (e *customEnv) publishPost(title, content string) string {
	e.t.Helper()
	ctx := context.Background()
	u, err := e.st.UserByUsername(ctx, "admin")
	if err != nil {
		e.t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	id, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: title,
		Slug: strings.ToLower(strings.ReplaceAll(title, " ", "-")), Lang: e.s.mainLang(), Content: content, AuthorID: u.ID,
		CommentStatus: "open", PublishedAt: &at})
	if err != nil {
		e.t.Fatal(err)
	}
	e.s.contentChanged()
	p, err := e.st.PostByID(ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	pu, _ := url.Parse(e.s.PostInfo(p).URL)
	return pu.Path
}

const testLayout = `{"layout": {
  "header": {"sections": [{"id": "h", "layout": "1", "columns": [[{"id": "hdr", "type": "site-header", "settings": {"tagline": "true"}}]]}]},
  "footer": {"sections": [{"id": "f", "layout": "1-1", "columns": [[{"id": "ft", "type": "rich-text", "settings": {"text": "Footer **marker** <script>alert(1)</script>"}}], []]}]},
  "pages": {
    "home": {"sections": [{"id": "s1", "layout": "2-1", "style": {"background": "muted"}, "columns": [
      [{"id": "hero", "type": "hero", "settings": {"title": "Builder home marker", "button_label": "Go", "button_url": "/about"}}, {"id": "pl", "type": "post-list", "settings": {"layout": "bento"}}],
      [{"id": "cta", "type": "button", "settings": {"url": "https://example.com/x", "variant": "outline", "new_tab": "true"}}]]}]},
    "single": {"sections": [{"id": "s2", "layout": "1", "columns": [[{"id": "bc", "type": "breadcrumbs"}, {"id": "c", "type": "single-content"}, {"id": "note", "type": "rich-text", "settings": {"text": "Single marker"}}]]}]}
  }
}}`

// A draft layout is only rendered in the administrator's preview: visitors
// keep the theme's templates, and the page cache never holds the draft.
func TestBuilderDraftNeverReachesVisitors(t *testing.T) {
	e := newCustomEnv(t)
	postPath := e.publishPost("Layout test post", "The post body marker.")

	rec, res := e.layout(testLayout)
	if rec.Code != 200 || !res.OK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	// The footer credit was put back by the server.
	if !res.Layout.Footer.HasBlock(builder.MetaType) {
		t.Errorf("normalised footer has no credits: %s", res.Layout.JSON())
	}

	// The preview shows every region of the draft.
	rec = e.get("/?customize=draft", e.admin)
	body := rec.Body.String()
	for _, want := range []string{"Builder home marker", `class="sap-region sap-region-header"`, `class="sap-region sap-region-footer"`,
		"sap-section sap-background-muted", "sap-cols sap-cols-2-1", "Footer <strong>marker</strong>", `href="https://sap13ns.site"`,
		`class="btn" href="https://example.com/x" data-variant="outline" target="_blank" rel="noopener"`, "sap-posts-bento"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	// The customiser says that its preview shows the builder's draft.
	if cz := e.get("/admin/appearance/themes/"+e.slug+"/customize", e.admin).Body.String(); !strings.Contains(cz, "El constructor de páginas tiene cambios sin publicar") {
		t.Error("the customiser does not mention the page builder's draft")
	}
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, `class="globalnav"`) {
		t.Error("preview: raw script in rich text, or the theme header kept")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("preview cache headers: %v", rec.Header())
	}
	if body := e.get(postPath+"?customize=draft", e.admin).Body.String(); !strings.Contains(body, "Single marker") || !strings.Contains(body, "The post body marker.") ||
		!strings.Contains(body, `class="sap-b sap-b-breadcrumbs"`) {
		t.Errorf("single preview lacks the layout or the content")
	}

	// Visitors and authors get the theme's templates, before and after the
	// cache fills, and after the administrator previewed again.
	check := func(when string) {
		t.Helper()
		for i, target := range []string{"/", "/", "/?customize=draft", postPath, postPath + "?customize=draft"} {
			body := e.get(target, nil).Body.String()
			if strings.Contains(body, "marker") && !strings.Contains(body, "The post body marker.") || strings.Contains(body, "sap-region") {
				t.Errorf("%s: visitor %d (%s) sees the draft layout", when, i, target)
			}
		}
		if body := e.get("/?customize=draft", e.author).Body.String(); strings.Contains(body, "Builder home marker") {
			t.Errorf("%s: an author sees the draft layout", when)
		}
	}
	check("before publishing")
	e.get("/?customize=draft", e.admin)
	if rec := e.get("/", nil); rec.Header().Get("X-Cache") != "HIT" || strings.Contains(rec.Body.String(), "Builder home marker") {
		t.Errorf("cached home: %s, draft %v", rec.Header().Get("X-Cache"), strings.Contains(rec.Body.String(), "Builder home marker"))
	}

	// Publishing replaces the theme's templates for everyone, cache included.
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if e.layoutRow().Draft != nil {
		t.Error("publish kept the draft")
	}
	body = e.get("/", nil).Body.String()
	if !strings.Contains(body, "Builder home marker") || !strings.Contains(body, "sap-region-header") || !strings.Contains(body, `href="https://sap13ns.site"`) {
		t.Error("published home does not use the layout")
	}
	if body := e.get(postPath, nil).Body.String(); !strings.Contains(body, "Single marker") || !strings.Contains(body, "The post body marker.") {
		t.Error("published single does not use the layout")
	}
	// Page types without a layout keep the theme's template, with the
	// layout's header and footer.
	body = e.get("/search?q=post", nil).Body.String()
	if strings.Contains(body, "sap-region-main") || !strings.Contains(body, "sap-region-header") || !strings.Contains(body, "search-form") {
		t.Error("search page: the theme's template should stay, inside the layout's header and footer")
	}
	if _, n, _ := e.st.ListAudit(context.Background(), "theme.layout_published", 1, 0); n == 0 {
		t.Error("publish not audited")
	}

	// A new draft leaves the published layout to visitors; discard drops it.
	if rec, _ := e.layout(`{"layout": {"pages": {"home": {"sections": [{"id": "x", "layout": "1", "columns": [[{"id": "t", "type": "rich-text", "settings": {"text": "Second draft"}}]]}]}}}}`); rec.Code != 200 {
		t.Fatalf("second draft: %d", rec.Code)
	}
	if body := e.get("/", nil).Body.String(); strings.Contains(body, "Second draft") || !strings.Contains(body, "Builder home marker") {
		t.Error("visitors see the second draft")
	}
	if rec := e.builderPost("/discard"); rec.Code != http.StatusSeeOther || e.layoutRow().Draft != nil {
		t.Errorf("discard: %d", rec.Code)
	}
	if body := e.get("/?customize=draft", e.admin).Body.String(); strings.Contains(body, "Second draft") || !strings.Contains(body, "Builder home marker") {
		t.Error("discard did not go back to the published layout")
	}
}

// The licence credit is always in a block footer, whatever is stored.
func TestBuilderFooterCredit(t *testing.T) {
	e := newCustomEnv(t)
	// Stored directly, bypassing the editor and its validation.
	raw := []byte(`{"footer": {"sections": [{"id": "f", "layout": "1", "columns": [[{"id": "t", "type": "rich-text", "settings": {"text": "Only my text"}}]]}]}}`)
	u, err := e.st.UserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.PublishCustom(context.Background(), e.slug, store.CustomLayout, raw, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	e.s.custom.invalidate()
	e.s.contentChanged()
	body := e.get("/", nil).Body.String()
	if !strings.Contains(body, "Only my text") {
		t.Fatal("the stored footer is not used")
	}
	if !strings.Contains(body, "Powered by") || !strings.Contains(body, `href="https://sap13ns.site"`) {
		t.Error("the footer lost the credit")
	}
	if n := strings.Count(body, `href="https://sap13ns.site"`); n != 1 {
		t.Errorf("credit shown %d times", n)
	}
}

func TestBuilderValidationAndAccess(t *testing.T) {
	e := newCustomEnv(t)
	cases := map[string]string{
		"single without content": `{"layout": {"pages": {"single": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "c", "type": "comments"}]]}]}}}}`,
		"script link":            `{"layout": {"pages": {"home": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "b", "type": "button", "settings": {"url": "javascript:alert(1)"}}]]}]}}}}`,
		"unknown block":          `{"layout": {"header": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "b", "type": "iframe"}]]}]}}}`,
		"class injection":        `{"layout": {"pages": {"home": {"sections": [{"id": "s", "layout": "1", "style": {"background": "muted x"}, "columns": [[]]}]}}}}`,
	}
	for name, body := range cases {
		rec, res := e.layout(body)
		if rec.Code != http.StatusUnprocessableEntity || res.OK || len(res.Errors) == 0 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if e.layoutRow().Draft != nil {
		t.Error("a refused layout was saved")
	}
	// Only appearance managers may edit, and only with the CSRF token.
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder/draft", strings.NewReader(testLayout))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", e.acsrf)
	if rec := e.do(req, e.author); rec.Code == 200 {
		t.Error("an author saved a layout")
	}
	req = httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder/draft", strings.NewReader(testLayout))
	req.Header.Set("Content-Type", "application/json")
	if rec := e.do(req, e.admin); rec.Code == 200 {
		t.Error("a layout was saved without the CSRF token")
	}
	if rec := e.get("/admin/appearance/themes/"+e.slug+"/builder", e.author); rec.Code == 200 {
		t.Error("an author opened the builder")
	}
	rec := e.get("/admin/appearance/themes/"+e.slug+"/builder", e.admin)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data-builder") || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-src 'self'") {
		t.Errorf("builder page: %d %v", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := e.get("/admin/appearance/builder", e.admin); rec.Code != http.StatusSeeOther || !strings.HasSuffix(rec.Header().Get("Location"), "/builder") {
		t.Errorf("builder shortcut: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// Layouts travel with the design in the customiser's export file, and
// restoring a published version goes to the draft.
func TestBuilderExportImportRestore(t *testing.T) {
	e := newCustomEnv(t)
	if rec, _ := e.layout(testLayout); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	e.builderPost("/publish")
	rec := e.get("/admin/appearance/themes/"+e.slug+"/customize/export", e.admin)
	var exp designExport
	if err := json.Unmarshal(rec.Body.Bytes(), &exp); err != nil || !strings.Contains(string(exp.Layout), "Builder home marker") {
		t.Fatalf("export: %v %s", err, rec.Body)
	}

	// A second version, published, then the first restored into the draft.
	e.layout(`{"layout": {"pages": {"home": {"sections": []}}}}`)
	e.builderPost("/publish")
	revs, err := e.st.CustomRevisions(context.Background(), e.slug, store.CustomLayout)
	if err != nil || len(revs) != 2 {
		t.Fatalf("revisions: %v %d", err, len(revs))
	}
	if rec := e.builderPost("/revisions/" + itoa(revs[1].ID) + "/restore"); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", rec.Code)
	}
	if d := string(e.layoutRow().Draft); !strings.Contains(d, "Builder home marker") {
		t.Errorf("restored draft: %s", d)
	}
	if strings.Contains(e.get("/", nil).Body.String(), "Builder home marker") {
		t.Error("restore changed the live layout")
	}
	e.builderPost("/discard")

	// Import the export file: the layout lands in the draft, and the form
	// goes back to the builder.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	mw.WriteField("return", "builder")
	fw, _ := mw.CreateFormFile("file", "design.json")
	fw.Write(rec.Body.Bytes())
	mw.Close()
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res := e.do(req, e.admin)
	if res.Code != http.StatusSeeOther || !strings.Contains(res.Header().Get("Location"), "/builder") {
		t.Fatalf("import: %d %q %s", res.Code, res.Header().Get("Location"), res.Body)
	}
	if d := string(e.layoutRow().Draft); !strings.Contains(d, "Builder home marker") {
		t.Errorf("imported draft: %s", d)
	}
	// An import with an unsafe layout is refused whole.
	e.builderPost("/discard")
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ = mw.CreateFormFile("file", "design.json")
	fw.Write([]byte(`{"format": "sap13ns-theme-custom", "design": {}, "layout": {"pages": {"home": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "b", "type": "button", "settings": {"url": "javascript:x"}}]]}]}}}}`))
	mw.Close()
	req = httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/customize/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	e.do(req, e.admin)
	if e.layoutRow().Draft != nil {
		t.Error("an unsafe layout was imported")
	}
}
