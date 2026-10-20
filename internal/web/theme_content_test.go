package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"sap13ns/internal/store"
)

// A theme's texts and images (theme.json "content"): the defaults in the
// theme's translations, the texts edited per language and an image from
// the library, drafted in the customiser and shown to visitors once
// published; the customiser lists them, and refuses what the theme lacks.
func TestThemeContent(t *testing.T) {
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
	img, err := e.st.CreateMedia(ctx, &store.Media{Path: "2026/10/abcdefabcdefabcdefab.jpg", OriginalName: "photo.jpg", MIME: "image/jpeg", Size: 10,
		Width: 800, Height: 600, Alt: "A photo", SHA256: strings.Repeat("ab", 32), Variants: map[string]store.MediaVariant{
			"thumb": {Path: "2026/10/abcdefabcdefabcdefab-thumb.jpg", Width: 480, Height: 360}}})
	if err != nil {
		t.Fatal(err)
	}

	files := tinyTheme("texty")
	files["theme.json"] = `{"name": "Texty", "version": "1.0.0",
  "content": [{"id": "intro", "label": "Intro", "fields": [
    {"key": "title", "label": "Title", "type": "text", "default": "Hello world"},
    {"key": "steps", "label": "Steps", "type": "lines", "default": "One\nTwo"},
    {"key": "photo", "label": "Photo", "type": "image", "default": "img/photo.webp"}]}]}`
	files["i18n/es.json"] = `{"Hello world": "Hola mundo", "One": "Uno", "Two": "Dos"}`
	files["templates/home.html"] = `{{define "content"}}<h1 class="t" data-sap-edit="title">{{.Site.Text "title"}}</h1>
<ul>{{range .Site.Lines "steps"}}<li>{{.}}</li>{{end}}</ul>
{{with .Site.Image "photo"}}<img class="photo" src="{{.Medium}}" alt="{{.Alt}}">{{else}}<img class="photo" src="{{$.Site.Asset "img/photo.webp"}}" alt="">{{end}}{{end}}`
	if rec := e.uploadTheme("texty.zip", zipOf(t, "texty", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("/admin/appearance/themes/texty/activate", url.Values{}); rec.Code != http.StatusSeeOther {
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
	has("/ before", page("/"), ">Hola mundo</h1>", "<li>Uno</li><li>Dos</li>", `src="/themes/texty/static/img/photo.webp`)
	has("/en/ before", page("/en/"), ">Hello world</h1>", "<li>One</li>")

	// The customiser lists the fields with what the page shows.
	b := page("/admin/appearance/themes/texty/customize", e.cookie)
	has("customiser", b, "data-cz-content", `name="content.title" value="Hola mundo" data-default="Hola mundo"`,
		`name="content_i18n.en.title" value="Hello world"`, `name="content.photo"`, `id="media-dialog"`)

	send := func(design string) int {
		t.Helper()
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/texty/customize/draft", strings.NewReader(design))
		req.RemoteAddr = "192.0.2.10:4000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(`{"content": {"nope": "x"}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("unknown text: %d", code)
	}
	if code := send(`{"content_i18n": {"en": {"photo": "` + strconv.FormatInt(img, 10) + `"}}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a translated image: %d", code)
	}
	if code := send(`{"content": {"photo": "999999"}}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a missing image: %d", code)
	}
	design := `{"content": {"title": "Bienvenidos", "steps": "Primero\n\nSegundo\n", "photo": "` + strconv.FormatInt(img, 10) + `"},
  "content_i18n": {"en": {"title": "Welcome"}}}`
	if code := send(design); code != http.StatusOK {
		t.Fatalf("draft: %d", code)
	}
	// Visitors keep the published page; the preview shows the draft.
	has("/ with a draft", page("/"), ">Hola mundo</h1>")
	has("preview", page("/?preview_theme=texty&customize=draft", e.cookie), ">Bienvenidos</h1>", "<li>Primero</li><li>Segundo</li>", `alt="A photo"`)
	if rec := e.admin("/admin/appearance/themes/texty/customize/publish", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	has("/ published", page("/"), ">Bienvenidos</h1>", "<li>Primero</li>", `alt="A photo"`)
	has("/en/ published", page("/en/"), ">Welcome</h1>", "<li>One</li><li>Two</li>", `alt="A photo"`)
}
