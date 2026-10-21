package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Any theme's interface texts (what its templates write with T) can be
// typed over: the draft preview marks each one, the draft changes it per
// language, visitors see it once published, and the licence credit stays.
func TestThemeTexts(t *testing.T) {
	e := newPluginEnv(t)
	files := tinyTheme("plain")
	files["i18n/es.json"] = `{"Our plans": "Nuestros planes"}`
	files["templates/home.html"] = `{{define "content"}}<h2 title="{{T "Our plans"}}">{{T "Our plans"}}</h2><p>{{T "Two posts"}}</p>{{end}}`
	if rec := e.uploadTheme("plain.zip", zipOf(t, "plain", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("/admin/appearance/themes/plain/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}
	page := func(target string, cookies ...*http.Cookie) string {
		t.Helper()
		rec := e.do("GET", target, nil, "", cookies...)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", target, rec.Code)
		}
		return rec.Body.String()
	}
	send := func(design string) int {
		t.Helper()
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/plain/customize/draft", strings.NewReader(design))
		req.RemoteAddr = "192.0.2.10:4000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}

	if b := page("/"); strings.Contains(b, "data-sap-text") || !strings.Contains(b, "<h2 title=\"Nuestros planes\">Nuestros planes</h2>") {
		t.Fatalf("visitors:\n%s", b)
	}
	// The customiser offers editing even without theme.json "content".
	if b := page("/admin/appearance/themes/plain/customize", e.cookie); !strings.Contains(b, "data-edit-mode") {
		t.Error("no edit button")
	}
	if code := send(`{"texts": {"es": {"Powered by": "Made with"}}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("the credit: %d", code)
	}
	if code := send(`{"texts": {"xx": {"Our plans": "?"}}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown language: %d", code)
	}
	if code := send(`{"texts": {"es": {"Our plans": "Plans & prices", "Two posts": "  "}}}`); code != http.StatusOK {
		t.Fatalf("draft: %d", code)
	}
	preview := page("/?preview_theme=plain&customize=draft", e.cookie)
	for _, w := range []string{`<h2 title="Plans &amp; prices"><span data-sap-text="Our plans">Plans &amp; prices</span></h2>`,
		`<span data-sap-text="Two posts">Two posts</span>`, `<a href="https://sap13ns.site">sap13ns</a>`} {
		if !strings.Contains(preview, w) {
			t.Errorf("preview lacks %s", w)
		}
	}
	if strings.ContainsAny(preview, "") {
		t.Error("marks left in the preview")
	}
	if b := page("/"); strings.Contains(b, "Plans") {
		t.Error("visitors see the draft")
	}
	if rec := e.admin("/admin/appearance/themes/plain/customize/publish", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	if b := page("/"); !strings.Contains(b, "<h2 title=\"Plans &amp; prices\">Plans &amp; prices</h2>") || strings.Contains(b, "data-sap-text") {
		t.Errorf("published:\n%s", b)
	}

	// A built-in theme too, in its draft preview.
	if b := page("/?preview_theme=bento&customize=draft", e.cookie); !strings.Contains(b, "data-sap-text=") || strings.ContainsAny(b, "") {
		t.Error("the built-in theme's preview is not marked")
	}
}
