package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sap13ns/internal/customize"
	"sap13ns/internal/store"
)

// builderCSS saves the custom CSS the way builder.js does.
func (e *customEnv) builderCSS(css string, cookie *http.Cookie, csrf string) (*httptest.ResponseRecorder, cssResult) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"css": css})
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder/css", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-CSRF-Token", csrf)
	rec := e.do(req, cookie)
	var res cssResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return rec, res
}

func (e *customEnv) design(raw []byte) *customize.Design {
	e.t.Helper()
	d := &customize.Design{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, d); err != nil {
			e.t.Fatal(err)
		}
	}
	return d
}

// TestBuilderHTMLBlock: an HTML block in the header and on the home page is
// sanitised, reaches visitors only once published, and sections and blocks
// carry their CSS classes.
func TestBuilderHTMLBlock(t *testing.T) {
	e := newCustomEnv(t)
	body := `{"layout": {
	  "header": {"sections": [{"id": "h", "layout": "1", "columns": [[{"id": "hdr", "type": "site-header"},
	    {"id": "hb", "type": "html", "settings": {"html": "<p class=\"top-note\">Header html marker</p>"}}]]}]},
	  "pages": {"home": {"sections": [{"id": "s1", "layout": "1", "class": "promo  wide", "columns": [[
	    {"id": "x", "type": "html", "settings": {"class": "offer", "prose": "false", "html": "<section id=\"deal\"><h2>Deal marker</h2><p onclick=\"alert(1)\" style=\"color:red\">Buy <a href=\"javascript:alert(2)\">now</a></p><script>alert(3)</script><iframe src=\"https://evil.example\"></iframe><form action=\"/x\"><input name=\"q\"></form><img src=\"/uploads/a.png\" onerror=\"alert(4)\" alt=\"A\"></section>"}},
	    {"id": "pl", "type": "post-list"}]]}]}}}}`
	rec, res := e.layout(body)
	if rec.Code != 200 || !res.OK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(string(e.layoutRow().Draft), "script") || strings.Contains(string(e.layoutRow().Draft), "onclick") {
		t.Errorf("stored unsanitised: %s", e.layoutRow().Draft)
	}
	if strings.Contains(e.get("/", nil).Body.String(), "Deal marker") {
		t.Fatal("a visitor saw the draft")
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	page := e.get("/", nil).Body.String()
	for _, want := range []string{"Deal marker", "Header html marker", `class="sap-b sap-b-html offer"`, `<section id="deal">`, "promo wide",
		`<img src="/uploads/a.png" alt="A">`, "Buy now"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	for _, bad := range []string{"alert(", "onclick", "onerror", "<iframe", "<form", `style="color`, "javascript:"} {
		if strings.Contains(page, bad) {
			t.Errorf("page contains %q", bad)
		}
	}
	// A refused class is reported, not saved.
	rec, res = e.layout(`{"layout": {"pages": {"home": {"sections": [{"id": "s1", "layout": "1", "class": "x\" onmouseover=\"alert(1)", "columns": [[]]}]}}}}`)
	if rec.Code != http.StatusUnprocessableEntity || len(res.Errors) == 0 || res.Errors[0].Field != "pages.home.sections[0].class" {
		t.Errorf("bad class: %d %s", rec.Code, rec.Body)
	}
}

// TestBuilderCustomCSS: the builder edits the design's custom CSS in its
// draft; Publish puts it live (keeping the design's other unpublished
// changes in the draft) and Discard takes it back.
func TestBuilderCustomCSS(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()

	// Only appearance managers, with the CSRF token, as JSON.
	if rec, _ := e.builderCSS(".a { color: red }", e.author, e.acsrf); rec.Code == 200 {
		t.Error("an author saved custom CSS")
	}
	if rec, _ := e.builderCSS(".a { color: red }", e.admin, ""); rec.Code == 200 {
		t.Error("custom CSS saved without the CSRF token")
	}
	req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/"+e.slug+"/builder/css", strings.NewReader("css=.a{}"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", e.csrf)
	if rec := e.do(req, e.admin); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form body: %d", rec.Code)
	}
	for _, css := range []string{
		`@import url(/x.css);`, `.a { background: url(https://evil.example/a.png) }`, `</style><script>alert(1)</script>`,
		`.a { width: expression(alert(1)) }`, `.a { behavior: url(x.htc) }`, `.a { -moz-binding: url(x.xml) }`,
		`.a { background: url(javascript:alert(1)) }`, `.a { color: red`, strings.Repeat("a", customize.MaxCSS+1),
	} {
		if rec, res := e.builderCSS(css, e.admin, e.csrf); rec.Code != http.StatusUnprocessableEntity || len(res.Errors) == 0 {
			t.Errorf("accepted %.60q: %d", css, rec.Code)
		}
	}
	if e.row().Draft != nil {
		t.Fatal("refused CSS was saved")
	}

	// A colour in the customiser's draft, then CSS from the builder.
	if rec, _ := e.draft(`{"design": {"light": {"primary": "#123456"}}}`); rec.Code != 200 {
		t.Fatalf("design draft: %d", rec.Code)
	}
	css := `html { scroll-behavior: smooth } .promo { background: url(data:image/png;base64,AAAA) } .marker-css { color: red }`
	rec, res := e.builderCSS(css, e.admin, e.csrf)
	if rec.Code != 200 || !res.OK || !res.Pending {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if d := e.design(e.row().Draft); d.CSS != css || d.Light["primary"] != "#123456" {
		t.Fatalf("draft: %+v", d)
	}
	// Visitors keep the published stylesheet (none yet).
	if u := customCSS(e.get("/", nil).Body.String()); u != "" {
		t.Errorf("visitors got a stylesheet before publishing: %s", u)
	}
	page := e.get("/admin/appearance/themes/"+e.slug+"/builder", e.admin).Body.String()
	if !strings.Contains(page, "marker-css") || !strings.Contains(page, `id="bl-css-input"`) {
		t.Error("the builder does not show the draft CSS")
	}

	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	row := e.row()
	if pub := e.design(row.Published); pub.CSS != css || pub.Light["primary"] != "" {
		t.Errorf("published: %+v", pub)
	}
	if d := e.design(row.Draft); row.Draft == nil || d.Light["primary"] != "#123456" {
		t.Errorf("the colour left the draft: %s", row.Draft)
	}
	if n := len(e.revisions()); n != 1 {
		t.Errorf("%d design revisions", n)
	}
	u := customCSS(e.get("/", nil).Body.String())
	if u == "" {
		t.Fatal("no stylesheet after publishing")
	}
	sheet := e.get(u, nil)
	if sheet.Code != 200 || !strings.Contains(sheet.Body.String(), ".marker-css") || sheet.Header().Get("Content-Type") != "text/css; charset=utf-8" ||
		sheet.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("stylesheet: %d %v %q", sheet.Code, sheet.Header(), sheet.Body)
	}

	// Discard takes the CSS back to the published one; the colour stays.
	if rec, _ := e.builderCSS(".other { color: blue }", e.admin, e.csrf); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := e.builderPost("/discard"); rec.Code != http.StatusSeeOther {
		t.Fatalf("discard: %d", rec.Code)
	}
	if d := e.design(e.row().Draft); d.CSS != css || d.Light["primary"] != "#123456" {
		t.Errorf("after discard: %+v", d)
	}
	// With only the CSS in the draft, discarding drops the draft.
	if err := e.st.DiscardCustomDraft(ctx, e.slug, store.CustomDesign); err != nil {
		t.Fatal(err)
	}
	e.builderCSS(".other { color: blue }", e.admin, e.csrf)
	e.builderPost("/discard")
	if e.row().Draft != nil {
		t.Errorf("draft left: %s", e.row().Draft)
	}
	// Nothing pending: publishing says so.
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Errorf("publish without changes: %d", rec.Code)
	}
	if n := len(e.revisions()); n != 1 {
		t.Errorf("%d design revisions after an empty publish", n)
	}
}
