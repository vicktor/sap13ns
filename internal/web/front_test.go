package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/builder"
	"sap13ns/internal/icons"
	"sap13ns/internal/store"
)

// setReading changes the front page mode and the extra languages.
func setReading(t *testing.T, s *Server, st *store.Store, mode string, langs ...string) {
	t.Helper()
	ctx := context.Background()
	set, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set.FrontPage, set.Languages = mode, append([]string{set.Language}, langs...)
	if err := st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	s.contentChanged()
}

const frontLayout = `{"layout": {"pages": {"front": {"sections": [
  {"id": "s1", "layout": "1", "columns": [[{"id": "hero", "type": "hero", "settings": {"title": "Front hero marker", "button_label": "Plans", "button_url": "#pricing"}}]]},
  {"id": "s2", "anchor": "features", "layout": "1", "columns": [[{"id": "ft", "type": "features", "settings": {"title": "Feature grid", "columns": "2"}, "items": [
    {"settings": {"icon": "rocket", "title": "Launch marker", "text": "Quick."}, "i18n": {"en": {"title": "Launch in English"}}},
    {"settings": {"icon": "leaf"}}
  ]}]]},
  {"id": "s3", "anchor": "pricing", "layout": "1", "columns": [[{"id": "pr", "type": "pricing", "items": [
    {"settings": {"name": "Basic", "price": "0 €", "features": "One\n- Two\n\n", "button_label": "Go", "button_url": "#contact"}},
    {"settings": {"name": "Plus", "price": "9 €", "period": "al mes", "highlight": "true"}}
  ]}]]},
  {"id": "s4", "layout": "1-1", "columns": [
    [{"id": "fq", "type": "faq", "settings": {"first_open": "true"}, "items": [{"settings": {"question": "Why?", "answer": "**Because** <script>alert(1)</script>"}}, {"settings": {"answer": "no question"}}]}],
    [{"id": "st", "type": "stats", "items": [{"settings": {"value": "10k+", "label": "Readers"}}]},
     {"id": "tm", "type": "testimonials", "items": [{"settings": {"quote": "Lovely", "name": "Ada Lovelace", "role": "Maths"}}]},
     {"id": "lg", "type": "logos", "items": [{"settings": {"alt": "No image"}}]}]]},
  {"id": "s5", "anchor": "contact", "layout": "1", "columns": [[{"id": "cta", "type": "cta", "settings": {"secondary_label": "Back", "secondary_url": "#features"}}, {"id": "pl", "type": "post-list"}]]}
]}}}}`

// The front page as a builder layout, in a theme without front.html: the
// reading setting, the draft preview, the anchors and the menu links to
// them, the languages, the cache and the sitemap.
func TestFrontPageLayout(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	e.publishPost("Front list post", "Body.")
	setReading(t, e.s, e.st, FrontTheme, "en")
	for _, it := range [][2]string{{"Precios", "#pricing"}, {"Blog", "/blog"}} {
		if err := e.st.AddMenuItem(ctx, "primary", "", it[0], it[1]); err != nil {
			t.Fatal(err)
		}
	}
	e.s.contentChanged()

	// No front template and no front layout: the posts.
	if b := e.get("/", nil).Body.String(); !strings.Contains(b, "Front list post") || strings.Contains(b, "sap-region-front") {
		t.Fatal("without a front page, / should list the posts")
	}

	if rec, res := e.layout(frontLayout); rec.Code != 200 || !res.OK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	// The draft is the administrator's preview only.
	if b := e.get("/?front=1&customize=draft", e.admin).Body.String(); !strings.Contains(b, "Front hero marker") {
		t.Fatal("the builder preview does not show the draft front page")
	}
	for _, target := range []string{"/", "/?front=1&customize=draft", "/?customize=draft"} {
		if b := e.get(target, nil).Body.String(); strings.Contains(b, "Front hero marker") {
			t.Fatalf("a visitor sees the draft at %s", target)
		}
	}
	if b := e.get("/?front=1&customize=draft", e.author).Body.String(); strings.Contains(b, "Front hero marker") {
		t.Fatal("an author sees the draft front page")
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	// The editor offers the front region, its presets and the starters.
	if b := e.get("/admin/appearance/themes/"+e.slug+"/builder", e.admin).Body.String(); !strings.Contains(b, "front_presets") ||
		!strings.Contains(b, "Portada (landing)") || !strings.Contains(b, "starters") || !strings.Contains(b, `data-icon="rocket"`) {
		t.Error("the builder lacks the front region, its presets or the icon picker")
	}

	rec := e.get("/", nil)
	body := rec.Body.String()
	for _, want := range []string{
		"Front hero marker", `<h1 class="sap-hero-title">`, `class="sap-section sap-background-none sap-padding-medium sap-width-content sap-align-start sap-anchor" id="features"`,
		`id="pricing"`, `id="sap-s1"`, "Launch marker", "lucide-rocket", "sap-grid-2",
		`<li class="sap-plan is-highlighted">`, "El más elegido", "al mes", "<span>One</span>", "<span>Two</span>",
		`<details class="sap-faq-item" open>`, "<strong>Because</strong>", `<dt class="sap-stat-label">Readers</dt><dd class="sap-stat-value">10k&#43;</dd>`,
		"Ada Lovelace", `<span class="sap-quote-initial" aria-hidden="true">AL</span>`,
		`href="#features">Back</a>`, "¿Empezamos?", "Front list post", "onepage.js", `"@type":"WebSite"`,
		`href="#pricing">Precios</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("front page lacks %q", want)
		}
	}
	for _, bad := range []string{"<script>alert(1)", "no question", "sap-b-logos", "lucide-leaf", "sap-region-home"} {
		if strings.Contains(body, bad) {
			t.Errorf("front page has %q", bad)
		}
	}
	if rec := e.get("/", nil); rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("the front page is not cached: %q", rec.Header().Get("X-Cache"))
	}
	if !strings.Contains(body, `hreflang="en" href="http://example.test/en/"`) {
		t.Error("no hreflang alternate for the English front page")
	}

	// The posts are at /blog, where a menu link to a section goes to the
	// front page.
	blog := e.get("/blog", nil).Body.String()
	if !strings.Contains(blog, "Front list post") || !strings.Contains(blog, `href="/#pricing">Precios</a>`) || strings.Contains(blog, "Front hero marker") {
		t.Error("/blog: the posts, with menu links to the front page's sections")
	}
	en := e.get("/en/", nil).Body.String()
	if !strings.Contains(en, "Launch in English") || !strings.Contains(en, "Most popular") || !strings.Contains(en, `href="#pricing"`) {
		t.Error("/en/: the English front page")
	}
	if b := e.get("/en/blog", nil).Body.String(); !strings.Contains(b, `href="/en/#pricing"`) {
		t.Error("/en/blog: menu links go to the English front page")
	}
	sitemap := e.get("/sitemap.xml", nil).Body.String()
	if !strings.Contains(sitemap, "<loc>http://example.test/blog</loc>") || !strings.Contains(sitemap, "<loc>http://example.test/en/blog</loc>") {
		t.Error("the sitemap lacks /blog")
	}

	// Back to the posts: the layout stays, unused.
	setReading(t, e.s, e.st, FrontPosts, "en")
	if b := e.get("/", nil).Body.String(); strings.Contains(b, "Front hero marker") || !strings.Contains(b, "Front list post") {
		t.Error("posts mode still shows the front layout")
	}
	if strings.Contains(e.get("/sitemap.xml", nil).Body.String(), "/blog</loc>") {
		t.Error("the sitemap lists /blog while / lists the posts")
	}
}

// A theme may ship only a front page: its blog pages fall back to the
// shared templates, inside its layout, and activating it shows the front
// page at /.
func TestFrontOnlyTheme(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	if _, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Solo post",
		Slug: "solo-post", Lang: "en", Content: "Solo body.", AuthorID: e.adminID, CommentStatus: "open", PublishedAt: &at}); err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	files := tinyTheme("solo-marker")
	delete(files, "templates/home.html")
	delete(files, "templates/single.html")
	files["templates/front.html"] = `{{define "content"}}<section class="solo-front" id="intro"><h1>{{.Site.Title}}</h1>
<p class="marker">front solo-marker</p><a href="{{.Site.Link "#intro"}}">Top</a>
{{range .Posts}}<a href="{{.URL}}">{{.Title}}</a>{{end}}</section>{{end}}`
	if rec := e.uploadTheme("solo.zip", zipOf(t, "solo", files, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	rec := e.admin("/admin/appearance/themes/solo/activate", url.Values{})
	if rec.Code != http.StatusSeeOther || e.s.settings().FrontPage != FrontTheme {
		t.Fatalf("activate: %d, front page %q", rec.Code, e.s.settings().FrontPage)
	}
	get := func(target string) string {
		t.Helper()
		rec := e.do("GET", target, nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", target, rec.Code)
		}
		return rec.Body.String()
	}
	if b := get("/"); !strings.Contains(b, "front solo-marker") || !strings.Contains(b, `href="#intro"`) || !strings.Contains(b, "Solo post") {
		t.Error("/: the theme's front page")
	}
	checks := map[string][]string{
		"/blog":           {`<div class="sap-fallback">`, "Solo post", "<footer>"},
		"/blog/solo-post": {"sap-fallback-entry", "<h1>Solo post</h1>", "Solo body."},
		"/search?q=solo":  {"sap-fallback", "Solo post"},
	}
	for target, wants := range checks {
		b := get(target)
		for _, w := range wants {
			if !strings.Contains(b, w) {
				t.Errorf("%s lacks %q", target, w)
			}
		}
		if !strings.Contains(b, "kind-") || strings.Contains(b, "front solo-marker") {
			t.Errorf("%s: not in the theme's layout, or shows the front page", target)
		}
	}
	if rec := e.do("GET", "/nope", nil, ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "solo-marker") {
		t.Errorf("404: %d", rec.Code)
	}
}

// The built-in one-page theme: the sections its menu scrolls to, options
// that hide sections with their menu links, the blog in its layout, and a
// note for administrators that leads to the page builder.
func TestOnepageTheme(t *testing.T) {
	e := newPluginEnv(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	if _, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Onepage post",
		Slug: "onepage-post", Lang: e.s.settings().Language, Content: "Onepage body.", AuthorID: e.adminID, CommentStatus: "open", PublishedAt: &at}); err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	if rec := e.admin("/admin/appearance/themes/onepage/activate", url.Values{}); rec.Code != http.StatusSeeOther || e.s.settings().FrontPage != FrontTheme {
		t.Fatalf("activate: %d, front page %q", rec.Code, e.s.settings().FrontPage)
	}
	get := func(target string, cookies ...*http.Cookie) string {
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
	lacks := func(where, body string, bads ...string) {
		t.Helper()
		for _, b := range bads {
			if strings.Contains(body, b) {
				t.Errorf("%s has %q", where, b)
			}
		}
	}

	front := get("/")
	has("/", front, `class="accent-vermilion headings-serif`, `data-sticky`, `id="intro"`, `id="features"`, `id="testimonials"`,
		`id="pricing"`, `id="faq"`, `id="contact"`, `href="#features">Características</a>`, `href="#pricing">Precios</a>`,
		`href="#faq">Preguntas</a>`, `href="/blog"`, "onepage.js", "Ver los planes", "Desde el blog", "Onepage post",
		`href="mailto:hello@example.com"`, `<h1 class="op-hero-title">`, `href="https://sap13ns.site">sap13ns</a>`)
	lacks("/ (visitor)", front, "op-note", "/builder")
	has("/ (administrator)", get("/", e.cookie), "op-note", `href="/admin/appearance/themes/onepage/builder?region=front"`)

	// The blog's pages use the shared fallbacks; the menu leads back to the
	// front page's sections.
	blog := get("/blog")
	has("/blog", blog, "sap-fallback", "Onepage post", `href="/#features"`, `href="/#contact"`, "op-footer")
	lacks("/blog", blog, `id="features"`)
	has("/blog/onepage-post", get("/blog/onepage-post"), "sap-fallback-entry", "Onepage body.", `href="/#pricing"`)

	form := url.Values{"opt_accent": {"emerald"}, "opt_headings": {"sans"}, "opt_hero_title": {"Hello marker"},
		"opt_contact": {"/contacto"}, "opt_show_stats": {"true"}, "opt_show_testimonials": {"true"}, "opt_show_faq": {"true"}}
	if rec := e.admin("/admin/appearance/themes/onepage/options", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("options: %d", rec.Code)
	}
	front = get("/")
	has("/ (options)", front, `class="accent-emerald headings-sans`, "Hello marker", `href="/contacto"`, `href="#contact">Contacta con nosotros</a>`, `id="faq"`)
	lacks("/ (options)", front, `id="pricing"`, `href="#pricing"`, "Desde el blog")
}

// Every icon a block or the feature picker can show is compiled in.
func TestBuilderIconsEmbedded(t *testing.T) {
	have := map[string]bool{}
	for _, n := range icons.Names() {
		have[n] = true
	}
	for _, c := range builder.Icons {
		if !have[c.Value] {
			t.Errorf("icon %q is offered but not in scripts/icons.txt", c.Value)
		}
	}
	for _, ty := range builder.Types {
		if !have[ty.Icon] {
			t.Errorf("block %s: icon %q is not in scripts/icons.txt", ty.Type, ty.Icon)
		}
	}
	for _, n := range builderIcons {
		if !have[n] {
			t.Errorf("editor icon %q is not in scripts/icons.txt", n)
		}
	}
}
