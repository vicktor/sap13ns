package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/ai"
	"sap13ns/internal/store"
)

// fakeDesignAI answers the design assistant with canned answers, one per
// call, and keeps what it was asked.
type fakeDesignAI struct {
	mu      sync.Mutex
	answers []string
	reqs    []ai.TextRequest
}

func (f *fakeDesignAI) Name() string { return "fake" }
func (f *fakeDesignAI) Image(context.Context, ai.ImageRequest) ([]ai.Image, error) {
	return nil, ai.ErrUnsupported
}
func (f *fakeDesignAI) Text(_ context.Context, req ai.TextRequest) (*ai.TextResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	a := f.answers[0]
	f.answers = f.answers[1:]
	return &ai.TextResponse{Text: a, StopReason: ai.StopEnd}, nil
}

func designAnswer(reply, design, layout string, edits ...map[string]string) string {
	if edits == nil {
		edits = []map[string]string{}
	}
	b, _ := json.Marshal(map[string]any{"reply": reply, "design_patch": design, "layout_patch": layout, "block_edits": edits})
	return string(b)
}

// The design assistant sees the theme's catalogue and the texts its pages
// show, changes the draft through the editors' validation, and gets what
// the site refused back to fix; nothing is published.
func TestDesignAI(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	set, err := e.st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.Language, set.Languages, set.FrontPage = "es", []string{"es", "en"}, "theme"
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	files := tinyTheme("aity")
	delete(files, "templates/home.html")
	files["theme.json"] = `{"name": "Aity", "version": "1.0.0",
  "content": [{"id": "g", "label": "Front", "fields": [{"key": "hello", "label": "Greeting", "type": "text", "default": "Welcome"}]}],
  "sections": [{"id": "intro", "label": "Opening", "fields": [{"key": "title", "label": "Title", "type": "text", "content": "hello"}]}],
  "front_sections": ["intro"]}`
	files["i18n/es.json"] = `{"Welcome": "Bienvenida"}`
	files["templates/front.html"] = `{{define "content"}}{{range .Site.FrontSections $}}{{template "shared/theme-section" .}}{{end}}{{end}}`
	files["templates/sections/intro.html"] = `{{define "section/intro"}}<h1>{{.Text "title"}}</h1>{{end}}`
	if rec := e.uploadTheme("aity.zip", zipOf(t, "aity", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("/admin/appearance/themes/aity/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}

	// Without the site's AI, no panel and no endpoint.
	if strings.Contains(e.do("GET", "/admin/appearance/themes/aity/customize", nil, "", e.cookie).Body.String(), "data-design-ai") {
		t.Error("the panel shows without AI")
	}
	ask := func(text string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(designAIReq{Messages: []ai.Message{{Role: "user", Content: text}}, Lang: "es"})
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/aity/design/ai", strings.NewReader(string(body)))
		req.RemoteAddr = "192.0.2.10:4000"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	if rec := ask("hola"); rec.Code != http.StatusForbidden {
		t.Errorf("without AI: %d", rec.Code)
	}

	fake := &fakeDesignAI{answers: []string{
		// An unknown block: refused, sent back.
		designAnswer("Hecho", "", `{"pages": {"front": {"sections": [{"id": "s1", "layout": "1", "columns": [[{"id": "b1", "type": "nope"}]]}]}}}`),
		// Fixed: the theme's text in Spanish, and a front page with the
		// theme's section and a rich text, animated.
		designAnswer("He cambiado «Bienvenida» por «¡Aloha!» y he añadido una sección.", `{"content": {"hello": "¡Aloha!"}, "motion": {"level": "expressive"}}`,
			`{"pages": {"front": {"sections": [{"id": "s1", "layout": "1", "columns": [[{"id": "b1", "type": "theme-intro"}]]},
			  {"id": "s2", "layout": "1", "anchor": "more", "style": {"animation": "fade-up"}, "columns": [[{"id": "b2", "type": "rich-text", "settings": {"text": "Más"}}]]}]}}}`),
		// A block edit, in English.
		designAnswer("Done.", "", "", map[string]string{"block_id": "b2", "lang": "en", "settings": `{"text": "More"}`}),
	}}
	e.s.ai.mu.Lock()
	e.s.ai.settings = &store.AISettings{Enabled: true, Provider: "anthropic", TextModel: "claude-test", MinRole: "author"}
	e.s.ai.provider = fake
	e.s.ai.mu.Unlock()

	page := e.do("GET", "/admin/appearance/themes/aity/builder", nil, "", e.cookie).Body.String()
	for _, w := range []string{"data-design-ai", "/design/ai", "claude-test", "design-ai.js"} {
		if !strings.Contains(page, w) {
			t.Errorf("the structure tab lacks %q", w)
		}
	}
	wait := func(rec *httptest.ResponseRecorder) aiJob {
		t.Helper()
		if rec.Code != http.StatusAccepted {
			t.Fatalf("ask: %d %s", rec.Code, rec.Body)
		}
		var j struct{ Job string }
		json.Unmarshal(rec.Body.Bytes(), &j)
		for range 200 {
			e.s.ai.mu.Lock()
			job := *e.s.ai.jobs[j.Job]
			e.s.ai.mu.Unlock()
			if job.Done {
				return job
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the job did not finish")
		return aiJob{}
	}
	job := wait(ask("cambia el texto de la portada que pone bienvenida por ¡Aloha! y añade una sección"))
	res, ok := job.Result.(*designAIResult)
	if job.Error != "" || !ok || !res.Design || !res.Layout || !strings.Contains(res.Reply, "Aloha") {
		t.Fatalf("job: %+v %+v", job, res)
	}
	if len(fake.reqs) != 2 {
		t.Fatalf("%d calls", len(fake.reqs))
	}
	sys := fake.reqs[0].System
	for _, w := range []string{`"hello":"Bienvenida"`, `"hello":"Welcome"`, `"type":"theme-intro"`, `"type":"rich-text"`, "fade-up", "the theme's front page (the front layout)"} {
		if !strings.Contains(sys, w) {
			t.Errorf("the prompt lacks %q", w)
		}
	}
	if !fake.reqs[0].JSON || len(fake.reqs[0].Schema) == 0 {
		t.Error("not asked for JSON")
	}
	retry := fake.reqs[1].Messages
	if last := retry[len(retry)-1]; last.Role != "user" || !strings.Contains(last.Content, "refused") || !strings.Contains(last.Content, "nope") {
		t.Errorf("the refusal was not sent back: %+v", last)
	}

	// The draft has it; visitors do not, until it is published.
	if body := e.do("GET", "/", nil, "").Body.String(); strings.Contains(body, "Aloha") {
		t.Error("visitors see the draft")
	}
	if body := e.do("GET", "/?customize=draft", nil, "", e.cookie).Body.String(); !strings.Contains(body, "<h1>¡Aloha!</h1>") || !strings.Contains(body, "Más") || !strings.Contains(body, `data-sap-animate="fade-up"`) {
		t.Errorf("the draft preview lacks the changes:\n%s", body)
	}

	job = wait(ask("y en inglés pon More"))
	if res := job.Result.(*designAIResult); job.Error != "" || res.Design || !res.Layout {
		t.Fatalf("edit: %+v", job)
	}
	if rec := e.admin("/admin/appearance/themes/aity/customize/publish", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	if body := e.do("GET", "/en/", nil, "").Body.String(); !strings.Contains(body, "More") || !strings.Contains(body, "<h1>Welcome</h1>") {
		t.Errorf("published /en/:\n%s", body)
	}
	if body := e.do("GET", "/", nil, "").Body.String(); !strings.Contains(body, "<h1>¡Aloha!</h1>") || !strings.Contains(body, "/static/motion.css") {
		t.Errorf("published /:\n%s", body)
	}
}

// html_regions: the model writes sections as HTML and the site's CSS, as
// when a site is built by hand; on the last attempt, what the site still
// refuses is left out and the rest is saved.
func TestDesignAIHTML(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	set, _ := e.st.LoadSettings(ctx)
	set.Language, set.Languages, set.FrontPage = "es", []string{"es", "en"}, "theme"
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if rec := e.admin("/admin/appearance/themes/onepage/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}
	html := func(reply, css string, sections ...map[string]any) string {
		b, _ := json.Marshal(map[string]any{"reply": reply, "css": css, "design_patch": "", "layout_patch": "", "block_edits": []any{},
			"html_regions": []any{map[string]any{"region": "front", "sections": sections}}})
		return string(b)
	}
	sec := func(anchor, es, en string) map[string]any {
		return map[string]any{"anchor": anchor, "html": es, "translations": []any{map[string]any{"lang": "en", "html": en}}}
	}
	bad := designAnswer("x", "", `{"pages": {"front": {"sections": [{"id": "s1", "layout": "1", "columns": [[{"id": "b1", "type": "nope"}]]}]}}}`)
	fake := &fakeDesignAI{answers: []string{
		html("Hecho: portada nueva.", ".x-hero { display: grid; color: var(--primary); }",
			sec("inicio", `<section class="x-hero"><h1>Ariel</h1><script>alert(1)</script><p style="color:red">Limpio</p></section>`, `<section class="x-hero"><h1>Ariel EN</h1></section>`),
			sec("precio", `<section class="x-price"><h2>Precio</h2></section>`, "")),
		// Two refusals, then a third answer that still has an unknown block:
		// it is left out, the HTML section is saved.
		bad, bad,
		`{"reply": "Hecho.", "css": "", "design_patch": "", "block_edits": [], "html_regions": [],
		  "layout_patch": "{\"pages\": {\"front\": {\"sections\": [{\"id\": \"k1\", \"layout\": \"1\", \"columns\": [[{\"id\": \"k1b\", \"type\": \"html\", \"settings\": {\"html\": \"<p>Muestra gratis</p>\"}}, {\"id\": \"k2b\", \"type\": \"nope\"}]]}]}}}"}`,
	}}
	e.s.ai.mu.Lock()
	e.s.ai.settings = &store.AISettings{Enabled: true, Provider: "anthropic", TextModel: "claude-test", MinRole: "author"}
	e.s.ai.provider = fake
	e.s.ai.mu.Unlock()
	if _, err := e.st.CreateMedia(ctx, &store.Media{Path: "2026/10/ariel.jpg", OriginalName: "ariel.jpg", MIME: "image/jpeg", Width: 800, Height: 600, Alt: "Ariel", Variants: map[string]store.MediaVariant{}}); err != nil {
		t.Fatal(err)
	}

	run := func(text string) *designAIResult {
		t.Helper()
		body, _ := json.Marshal(designAIReq{Messages: []ai.Message{{Role: "user", Content: text}}, Lang: "es"})
		req := httptest.NewRequest("POST", "http://example.test/admin/appearance/themes/onepage/design/ai", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		req.AddCookie(e.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("ask: %d %s", rec.Code, rec.Body)
		}
		var j struct{ Job string }
		json.Unmarshal(rec.Body.Bytes(), &j)
		for range 200 {
			e.s.ai.mu.Lock()
			job := *e.s.ai.jobs[j.Job]
			e.s.ai.mu.Unlock()
			if job.Done {
				res, _ := job.Result.(*designAIResult)
				if job.Error != "" || res == nil {
					t.Fatalf("job: %+v", job)
				}
				return res
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the job did not finish")
		return nil
	}
	if res := run("un sitio sobre el jabón Ariel"); !res.Layout || !res.Design || res.Dropped != 0 {
		t.Fatalf("html: %+v", res)
	}
	sys := fake.reqs[0].System
	for _, w := range []string{"html_regions", "MEDIA", "/uploads/2026/10/ariel.jpg"} {
		if !strings.Contains(sys, w) {
			t.Errorf("the prompt lacks %q", w)
		}
	}
	body := e.do("GET", "/?customize=draft", nil, "", e.cookie).Body.String()
	if !strings.Contains(body, `<h1>Ariel</h1>`) || !strings.Contains(body, `id="precio"`) || strings.Contains(body, "alert(1)") || strings.Contains(body, "color:red") {
		t.Errorf("the draft front page:\n%s", body)
	}
	if en := e.do("GET", "/en/?customize=draft", nil, "", e.cookie).Body.String(); !strings.Contains(en, "Ariel EN") {
		t.Error("the translation is not shown")
	}
	if d, _, _ := e.s.draftDesign(ctx, e.s.themes.Active("onepage"), e.s.mediaSet(ctx)); !strings.Contains(d.CSS, "x-hero") {
		t.Errorf("the CSS: %q", d.CSS)
	}

	// Pages: created as drafts, and the reply says so; an answer that saves
	// nothing says that, whatever the model claims.
	fake.answers = append([]string{
		`{"reply": "He creado las páginas.", "css": "", "design_patch": "", "layout_patch": "", "block_edits": [], "html_regions": [],
		  "new_pages": [{"title": "Precio", "slug": "precio", "markdown": "## 7 € / 5 L"}, {"title": "Promoción", "slug": "", "markdown": "**2x1**"}]}`,
		`{"reply": "He creado cuatro páginas nuevas.", "css": "", "design_patch": "", "layout_patch": "", "block_edits": [], "html_regions": [], "new_pages": []}`,
	}, fake.answers...)
	if res := run("crea las páginas de precio y promoción"); len(res.Pages) != 2 || !strings.Contains(res.Reply, "«Precio», «Promoción»") {
		t.Fatalf("pages: %+v", res)
	}
	if p, err := e.st.PostBySlug(ctx, "page", "es", "promocion"); err != nil || p.Status != store.StatusDraft || p.Content != "**2x1**" {
		t.Fatalf("the page: %+v %v", p, err)
	}
	if res := run("¿y las otras?"); !strings.Contains(res.Reply, "No se ha guardado nada") {
		t.Fatalf("a claim with nothing saved: %+v", res)
	}

	res := run("añade la muestra gratuita")
	if !res.Layout || res.Dropped == 0 || !strings.Contains(res.Reply, "dejado fuera") {
		t.Fatalf("lenient: %+v", res)
	}
	if body := e.do("GET", "/?customize=draft", nil, "", e.cookie).Body.String(); !strings.Contains(body, "Muestra gratis") {
		t.Error("the part that passed was not saved")
	}
}
