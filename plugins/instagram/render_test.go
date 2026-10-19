package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

var madrid, _ = time.LoadLocation("Europe/Madrid")

func sampleFeed() Feed {
	return Feed{Account: igID, User: "sap13ns", Fetched: t0.Unix(), Items: []Media{
		{ID: "1", Kind: "image", Image: "https://scontent.cdninstagram.com/1.jpg?a=1&b=2", Link: "https://www.instagram.com/p/AAA/", Alt: `Tom & "Jerry" <b>`},
		{ID: "2", Kind: "video", Image: "https://scontent.cdninstagram.com/2.jpg", Link: "https://www.instagram.com/reel/BBB/", Alt: "A reel"},
		{ID: "3", Kind: "album", Link: "https://www.instagram.com/p/CCC/", Time: time.Date(2026, 10, 15, 23, 30, 0, 0, time.UTC).Unix()},
	}}
}

func view(lang string) View {
	return View{Layout: layoutGrid, Count: 8, Notice: "Note.", Text: textsFor(lang), Loc: madrid}
}

func TestGalleryExactOutput(t *testing.T) {
	v := view("en")
	v.PrivacyURL = "/privacy?a=1&b=2"
	got := Gallery(sampleFeed(), v)
	want := `<section class="card igf igf-grid" aria-label="Latest posts from @sap13ns on Instagram">` +
		`<ul class="igf-list" role="list">` +
		`<li class="igf-item"><a class="igf-link" href="https://www.instagram.com/p/AAA/"><span class="igf-thumb">` +
		`<img src="https://scontent.cdninstagram.com/1.jpg?a=1&amp;b=2" alt="" width="640" height="640" loading="lazy"></span>` +
		`<span class="sr-only">Tom &amp; &#34;Jerry&#34; &lt;b&gt;</span></a></li>` +
		`<li class="igf-item"><a class="igf-link" href="https://www.instagram.com/reel/BBB/"><span class="igf-thumb">` +
		`<img src="https://scontent.cdninstagram.com/2.jpg" alt="" width="640" height="640" loading="lazy"></span><span class="sr-only">A reel</span>` +
		`<span class="igf-badge igf-video" aria-hidden="true"><span data-icon="play"></span></span><span class="sr-only"> (video)</span></a></li>` +
		`<li class="igf-item"><a class="igf-link" href="https://www.instagram.com/p/CCC/">` +
		`<span class="igf-thumb igf-none" aria-hidden="true"><span data-icon="images"></span></span><span class="sr-only">Instagram post of Oct 16, 2026</span>` +
		`<span class="igf-badge" aria-hidden="true"><span data-icon="images"></span></span><span class="sr-only"> (several photos or videos)</span></a></li>` +
		`</ul><footer class="igf-footer"><p class="igf-notice">Note. <a href="/privacy?a=1&amp;b=2">Privacy policy</a></p>` +
		`<a class="igf-more" href="https://www.instagram.com/sap13ns/">@sap13ns on Instagram</a></footer></section>`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestCarouselAndWidget(t *testing.T) {
	v := view("es")
	v.Layout, v.Count, v.Heading, v.Notice = layoutCarousel, 2, "Mis <fotos>", ""
	got := Gallery(sampleFeed(), v)
	for _, s := range []string{
		`<section class="card igf igf-carousel" aria-label="Últimas publicaciones de @sap13ns en Instagram">`,
		`<header class="igf-head"><h2 class="igf-heading">Mis &lt;fotos&gt;</h2></header>`,
		`<span class="sr-only"> (vídeo)</span>`,
		`<footer class="igf-footer"><a class="igf-more" href="https://www.instagram.com/sap13ns/">@sap13ns en Instagram</a></footer>`,
		`<span class="igf-ui" aria-hidden="true"><span class="igf-ui-prev" aria-label="Publicaciones anteriores"><span data-icon="chevron-left"></span></span>` +
			`<span class="igf-ui-next" aria-label="Publicaciones siguientes"><span data-icon="chevron-right"></span></span></span></section>`,
	} {
		if !strings.Contains(got, s) {
			t.Errorf("carousel lacks %s:\n%s", s, got)
		}
	}
	if strings.Count(got, `<li class="igf-item">`) != 2 || strings.Contains(got, "igf-notice") {
		t.Errorf("count or notice: %s", got)
	}
	v.Widget = true
	got = Gallery(sampleFeed(), v)
	if !strings.HasPrefix(got, `<div class="igf igf-carousel igf-widget"><ul`) || !strings.HasSuffix(got, `</span></span></div>`) || strings.Contains(got, "igf-heading") {
		t.Errorf("widget: %s", got)
	}
	if got := (texts{es: true}).Post(sampleFeed().Items[2].Time, madrid); got != "Publicación de Instagram del 16 oct 2026" {
		t.Error(got)
	}
	if got := (texts{}).Post(0, madrid); got != "Instagram post" {
		t.Error(got)
	}
}

func TestEmpty(t *testing.T) {
	if got := Empty("sap13ns", view("en")); got != `<section class="card igf igf-empty" aria-label="Latest posts from @sap13ns on Instagram">`+
		`<a class="igf-more" href="https://www.instagram.com/sap13ns/"><span data-icon="images"></span>See @sap13ns on Instagram</a></section>` {
		t.Error(got)
	}
	v := view("es")
	v.Widget = true
	if got := Empty("sap13ns", v); got != `<div class="igf igf-empty igf-widget"><a class="igf-more" href="https://www.instagram.com/sap13ns/"><span data-icon="images"></span>Ver @sap13ns en Instagram</a></div>` {
		t.Error(got)
	}
	if Empty("", v) != "" || Empty("bad name", v) != "" {
		t.Error("no link without a valid username")
	}
}

func storeFeed(t *testing.T, env *sdktest.Env, f Feed) {
	t.Helper()
	b, _ := json.Marshal(f)
	env.KV[feedKey] = b
}

func TestShortcodeAndWidgetHooks(t *testing.T) {
	env, g := setup(t)
	env.Site.Timezone = "Europe/Madrid"

	// Before the first fetch: nothing, and a log line.
	if html, err := env.Shortcode("instagram_feed", map[string]string{}, ""); err != nil || html != "" {
		t.Errorf("before the first fetch: %q, %v", html, err)
	}
	wantLog(t, env, "[instagram_feed]: nothing to show yet; the feed is fetched when Instagram is connected")

	storeFeed(t, env, sampleFeed())
	r, err := env.Call(pluginsdk.HookShortcode, pluginsdk.Shortcode{Name: "instagram_feed", Attrs: map[string]string{"count": "2", "layout": "grid", "title": "Latest"}})
	if err != nil || r.MaxAge != 300 {
		t.Fatalf("shortcode: max age %d, %v", r.MaxAge, err)
	}
	if !strings.Contains(r.HTML, `igf igf-grid`) || strings.Count(r.HTML, "igf-item") != 2 || !strings.Contains(r.HTML, `<h2 class="igf-heading">Latest</h2>`) ||
		!strings.Contains(r.HTML, "Images are loaded from Instagram (Meta)") {
		t.Errorf("shortcode: %s", r.HTML)
	}
	// The settings' defaults: 8 posts, carousel.
	html, _ := env.Shortcode("instagram_feed", map[string]string{}, "")
	if !strings.Contains(html, "igf-carousel") || strings.Count(html, "igf-item") != 3 {
		t.Errorf("defaults: %s", html)
	}
	// The post's language.
	r, _ = env.Call(pluginsdk.HookShortcode, pluginsdk.Shortcode{Name: "instagram_feed", Attrs: map[string]string{}, Post: &pluginsdk.Post{Lang: "es"}})
	if !strings.Contains(r.HTML, "Las imágenes se cargan desde Instagram") {
		t.Errorf("Spanish: %s", r.HTML)
	}
	// Bad attributes fall back, with one log line each.
	html, _ = env.Shortcode("instagram_feed", map[string]string{"count": "many", "layout": "masonry"}, "")
	if !strings.Contains(html, "igf-carousel") {
		t.Errorf("bad attributes: %s", html)
	}
	wantLog(t, env, `[instagram_feed]: count "many" is not a number from 1 to 24; using 8`, `[instagram_feed]: layout "masonry" is neither carousel nor grid; using carousel`)

	r, err = env.Call(pluginsdk.HookWidget, pluginsdk.Widget{Widget: "feed", Config: map[string]string{"count": "1", "layout": "grid"}})
	if err != nil || r.MaxAge != 0 || !strings.HasPrefix(r.HTML, `<div class="igf igf-grid igf-widget">`) || strings.Count(r.HTML, "igf-item") != 1 {
		t.Errorf("widget: %d %s %v", r.MaxAge, r.HTML, err)
	}

	// Notice off, privacy link.
	env.Plugin.Settings["show_notice"] = "false"
	if html, _ := env.Shortcode("instagram_feed", map[string]string{}, ""); strings.Contains(html, "igf-notice") {
		t.Errorf("notice off: %s", html)
	}
	env.Plugin.Settings["show_notice"] = "true"
	env.Plugin.Settings["privacy_url"] = "javascript:alert(1)"
	if html, _ := env.Shortcode("instagram_feed", map[string]string{}, ""); strings.Contains(html, "javascript") {
		t.Errorf("unsafe privacy URL: %s", html)
	}
	wantLog(t, env, `settings: privacy URL "javascript:alert(1)" is not an http(s) URL or a site path; ignored`)

	// Three days without a refresh: the images have expired, so only the
	// profile link is left.
	setClock(t, t0.Add(73*time.Hour))
	if html, _ := env.Shortcode("instagram_feed", map[string]string{}, ""); !strings.HasPrefix(html, `<section class="card igf igf-empty"`) {
		t.Errorf("stale: %s", html)
	}
	if len(g.reqs) != 0 {
		t.Errorf("rendering fetched: %v", g.paths())
	}
}

func TestSafeURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"/privacy": true, "https://example.test/p": true, "http://example.test/p": true,
		"//evil.example": false, "javascript:alert(1)": false, "https://a b": false, "": false, `/p"x`: false,
	} {
		if safeURL(raw) != ok {
			t.Errorf("safeURL(%q) = %v", raw, !ok)
		}
	}
}
