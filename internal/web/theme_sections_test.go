package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A theme's own sections (theme.json "sections"): its front page renders
// them in front_sections' order, with their defaults in the theme's
// translations and the theme's texts they show; the page builder offers
// them as blocks and starts from the theme's front page, and a layout made
// of them rearranges, repeats and edits them, each as the theme made it.
func TestThemeSections(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	set, err := e.st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.Language, set.Languages = "es", []string{"es", "en"}
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}

	files := tinyTheme("secty")
	delete(files, "templates/home.html")
	files["theme.json"] = `{"name": "Secty", "version": "1.0.0",
  "content": [{"id": "g", "label": "Texts", "fields": [{"key": "hello", "label": "Greeting", "type": "text", "default": "Hello world"}]}],
  "sections": [
    {"id": "intro", "label": "Opening", "anchor": "intro", "icon": "panel-top", "fields": [
      {"key": "title", "label": "Title", "type": "text", "content": "hello"},
      {"key": "steps", "label": "Steps", "type": "lines", "default": "One\nTwo"},
      {"key": "more", "label": "Link", "type": "url", "default": "/about"}]},
    {"id": "plain", "label": "Plain", "where": ["front", "page"], "fields": [{"key": "text", "label": "Text", "type": "textarea", "default": "Plain text"}]}],
  "front_sections": ["intro", "plain"]}`
	files["i18n/es.json"] = `{"Hello world": "Hola mundo", "One": "Uno", "Two": "Dos", "Plain text": "Texto llano", "Opening": "Apertura"}`
	files["templates/front.html"] = `{{define "content"}}{{range .Site.FrontSections $}}{{template "shared/theme-section" .}}{{end}}{{end}}`
	files["templates/sections/intro.html"] = `{{define "section/intro"}}<section class="intro"><h1>{{.Text "title"}}</h1><ol>{{range .Lines "steps"}}<li>{{.}}</li>{{end}}</ol><a class="more" href="{{.URL "more"}}">more</a></section>{{end}}`
	files["templates/sections/plain.html"] = `{{define "section/plain"}}<section class="plain"><p>{{.Text "text"}}</p></section>{{end}}`
	if rec := e.uploadTheme("secty.zip", zipOf(t, "secty", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("/admin/appearance/themes/secty/activate", url.Values{}); rec.Code != http.StatusSeeOther {
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
	has := func(where, body string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %q", where, w)
			}
		}
	}
	has("/", page("/"), `<div class="sap-theme-section sap-anchor" id="intro">`, "<h1>Hola mundo</h1>", "<li>Uno</li><li>Dos</li>", `href="/about"`, "<p>Texto llano</p>")
	has("/en/", page("/en/"), "<h1>Hello world</h1>", `href="/en/about"`, "<p>Plain text</p>")

	// The builder offers the sections, the theme's front page first, with
	// what they show as the texts to edit.
	b := page("/admin/appearance/themes/secty/builder", e.cookie)
	has("builder", b, `&#34;type&#34;:&#34;theme-intro&#34;`, `&#34;recommended&#34;:&#34;theme&#34;`, `&#34;label&#34;:&#34;Apertura&#34;`,
		`&#34;default&#34;:&#34;Hola mundo&#34;`, `&#34;hints&#34;:{&#34;en&#34;:&#34;Hello world&#34;}`)

	send := func(layout string) int {
		t.Helper()
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/secty/builder/draft", strings.NewReader(layout))
		req.RemoteAddr = "192.0.2.10:4000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(`{"layout": {"pages": {"front": {"sections": [{"id": "a", "layout": "1", "columns": [[{"id": "x", "type": "theme-nope"}]]}]}}}}`); code == http.StatusOK {
		t.Error("an unknown section was saved")
	}
	if code := send(`{"layout": {"pages": {"single": {"sections": [{"id": "a", "layout": "1", "columns": [[{"id": "x", "type": "theme-intro"}]]}]}}}}`); code == http.StatusOK {
		t.Error("a section was placed where the theme does not allow it")
	}
	// Plain first, the opening twice (the second with its own title), and
	// a section with a built-in block next to one of the theme's.
	layout := `{"layout": {"pages": {"front": {"sections": [
	  {"id": "s1", "layout": "1", "columns": [[{"id": "b1", "type": "theme-plain", "settings": {"animation": "fade-up"}}]]},
	  {"id": "s2", "layout": "1", "anchor": "intro", "columns": [[{"id": "b2", "type": "theme-intro"}]]},
	  {"id": "s3", "layout": "1", "anchor": "again", "columns": [[{"id": "b3", "type": "theme-intro", "settings": {"title": "Otra vez", "steps": "Solo"}, "i18n": {"en": {"title": "Again"}}}]]},
	  {"id": "s4", "layout": "1-1", "columns": [[{"id": "b4", "type": "theme-plain", "settings": {"text": "Al lado"}}], [{"id": "b5", "type": "rich-text", "settings": {"text": "Rich"}}]]}]}}}}`
	if code := send(layout); code != http.StatusOK {
		t.Fatalf("draft: %d", code)
	}
	if rec := e.admin("/admin/appearance/themes/secty/builder/publish", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	body := page("/")
	has("/ laid out", body, `<div class="sap-theme-section" id="sap-s1" data-sap-animate="fade-up"><section class="plain"><p>Texto llano</p>`,
		`id="intro"><section class="intro"><h1>Hola mundo</h1>`, `id="again"><section class="intro"><h1>Otra vez</h1><ol><li>Solo</li></ol>`,
		`<section class="plain"><p>Al lado</p></section>`, "Rich", "/static/motion.css")
	if strings.Index(body, "Texto llano") > strings.Index(body, "Hola mundo") {
		t.Error("the sections are not in the layout's order")
	}
	has("/en/ laid out", page("/en/"), "<h1>Hello world</h1>", "<h1>Again</h1>", "<p>Plain text</p>")
}
