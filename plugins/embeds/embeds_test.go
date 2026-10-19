package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

const (
	yt = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	tt = "https://www.tiktok.com/@scout2015/video/6718335390845095173"
)

var english = Options{Enabled: all, Notice: "Note about {provider}."}

func TestFilterExactOutput(t *testing.T) {
	got, changed := Filter(`<p><a href="`+yt+`">`+yt+`</a></p>`, english)
	want := `<figure class="card embed embed-youtube" data-category="youtube" data-consent="youtube">` +
		`<a class="embed-link" href="` + yt + `"><span class="embed-icon" aria-hidden="true"><span data-icon="play"></span></span>` +
		`<span class="embed-text"><strong class="embed-title">YouTube video</strong><small class="embed-source">Watch on YouTube</small></span></a>` +
		`<a class="embed-player" href="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1" aria-label="Play YouTube video"><span class="embed-provider">YouTube</span></a>` +
		`<figcaption class="embed-notice" data-consent-note="">Note about YouTube.</figcaption></figure>`
	if !changed || got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestFilter(t *testing.T) {
	spanish := english
	spanish.Spanish = true
	withPolicy := english
	withPolicy.PrivacyURL = "/privacy?a=1&b=2"
	onlyVimeo := english
	onlyVimeo.Enabled = map[string]bool{"vimeo": true}

	for _, tc := range []struct {
		name    string
		in      string
		opt     Options
		want    []string // substrings of the output
		embeds  int      // number of figures; 0 means unchanged
		without []string // substrings that must not appear
	}{
		{name: "bare autolinked URL", in: `<p><a href="` + yt + `">` + yt + `</a></p>`, opt: english, embeds: 1,
			want: []string{`<strong class="embed-title">YouTube video</strong>`, `aria-label="Play YouTube video"`}},
		{name: "URL text without link", in: "<p>\n  https://youtu.be/dQw4w9WgXcQ \n</p>", opt: english, embeds: 1,
			want: []string{`href="https://youtu.be/dQw4w9WgXcQ"`}},
		{name: "titled link", in: `<p><a href="https://vimeo.com/76979871"><strong>Our</strong> launch   film</a></p>`, opt: english, embeds: 1,
			want: []string{`<strong class="embed-title">Our launch film</strong>`, `aria-label="Play video: Our launch film (Vimeo)"`, `data-category="vimeo"`, `?autoplay=1&amp;dnt=1"`}},
		{name: "title attribute names a bare link", in: `<p><a href="` + yt + `" title="Keynote 2026">` + yt + `</a></p>`, opt: english, embeds: 1,
			want: []string{`<strong class="embed-title">Keynote 2026</strong>`}},
		{name: "link text that is the URL without scheme", in: `<p><a href="` + yt + `">www.youtube.com/watch?v=dQw4w9WgXcQ</a></p>`, opt: english, embeds: 1,
			want: []string{`>YouTube video<`}},
		{name: "untitled embeds are numbered per provider", opt: english, embeds: 3,
			in:   `<p>` + yt + `</p><p>https://youtu.be/aBcDeFgHiJk</p><p>https://vimeo.com/76979871</p>`,
			want: []string{`>YouTube video<`, `>YouTube video 2<`, `aria-label="Play YouTube video 2"`, `>Vimeo video<`}, without: []string{"Vimeo video 2"}},
		{name: "spanish", in: `<p>` + yt + `</p><p>` + yt + `</p>`, opt: spanish, embeds: 2,
			want: []string{`>Vídeo de YouTube<`, `>Vídeo de YouTube 2<`, `>Ver en YouTube<`, `aria-label="Reproducir vídeo de YouTube 2"`}},
		{name: "spanish titled", in: `<p><a href="` + yt + `">Charla</a></p>`, opt: spanish, embeds: 1,
			want: []string{`aria-label="Reproducir vídeo: Charla (YouTube)"`}},
		{name: "privacy link escaped", in: `<p>` + yt + `</p>`, opt: withPolicy, embeds: 1,
			want: []string{`Note about YouTube. <a href="/privacy?a=1&amp;b=2">Privacy policy</a></figcaption>`}},
		{name: "shorts are portrait", in: `<p>https://www.youtube.com/shorts/aBcDeFgHiJk</p>`, opt: english, embeds: 1,
			want: []string{`class="card embed embed-youtube embed-portrait"`}},
		{name: "tiktok names its author", in: `<p>` + tt + `</p><p>` + tt + `</p><p>https://www.tiktok.com/embed/v2/6718335390845095173</p>`, opt: english, embeds: 3,
			want: []string{`class="card embed embed-tiktok embed-portrait" data-category="tiktok"`, `>TikTok video by @scout2015<`, `>TikTok video 2 by @scout2015<`,
				`aria-label="Play TikTok video by @scout2015"`, `>TikTok video 3<`, `>Watch on TikTok<`,
				`href="https://www.tiktok.com/player/v1/6718335390845095173?autoplay=1&amp;description=1&amp;rel=0"`, `Note about TikTok.`}},
		{name: "tiktok in spanish", in: `<p>` + tt + `</p>`, opt: spanish, embeds: 1,
			want: []string{`>Vídeo de TikTok de @scout2015<`, `aria-label="Reproducir vídeo de TikTok de @scout2015"`, `>Ver en TikTok<`}},
		{name: "titled tiktok", in: `<p><a href="` + tt + `">Scout &amp; Suki</a></p>`, opt: english, embeds: 1,
			want: []string{`<strong class="embed-title">Scout &amp; Suki</strong>`, `aria-label="Play video: Scout &amp; Suki (TikTok)"`}},
		{name: "tiktok short link stays a link", in: `<p>https://vm.tiktok.com/ZMabc123/</p>`, opt: english},
		{name: "title is escaped and cleaned for aria-label", in: `<p><a href="` + yt + `">&lt;b&gt;"Hi" &amp; bye</a></p>`, opt: english, embeds: 1,
			want: []string{`<strong class="embed-title">&lt;b&gt;&#34;Hi&#34; &amp; bye</strong>`, `aria-label="Play video: bHi &amp; bye (YouTube)"`}},
		{name: "disabled provider stays a link", in: `<p>` + yt + `</p>`, opt: onlyVimeo},
		{name: "paragraph with other text", in: `<p>Watch <a href="` + yt + `">this</a> now</p>`, opt: english},
		{name: "two links in one paragraph", in: `<p><a href="` + yt + `">a</a> <a href="` + yt + `">b</a></p>`, opt: english},
		{name: "link wrapping an image", in: `<p><a href="` + yt + `"><img src="/x.jpg" alt="x"></a></p>`, opt: english},
		{name: "inside a list", in: `<ul><li><a href="` + yt + `">` + yt + `</a></li></ul>`, opt: english},
		{name: "inside a quote", in: `<blockquote><p><a href="` + yt + `">` + yt + `</a></p></blockquote>`, opt: english},
		{name: "inside code", in: `<pre><code>` + yt + `</code></pre><p><code>` + yt + `</code></p>`, opt: english},
		{name: "inside a table", in: `<table><tr><td><p>` + yt + `</p></td></tr></table>`, opt: english},
		{name: "other site", in: `<p><a href="https://example.com/watch?v=dQw4w9WgXcQ">x</a></p>`, opt: english},
		{name: "text with spaces", in: `<p>` + yt + ` is great</p>`, opt: english},
		{name: "empty", in: ``, opt: english},
		{name: "no links", in: `<p>Hola</p>`, opt: english},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Filter(tc.in, tc.opt)
			if n := strings.Count(got, "<figure"); changed != (tc.embeds > 0) || n != tc.embeds {
				t.Fatalf("changed=%v with %d figures, want %d\n%s", changed, n, tc.embeds, got)
			}
			if !changed && got != tc.in {
				t.Fatalf("unchanged input was rewritten:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in\n%s", w, got)
				}
			}
			for _, w := range tc.without {
				if strings.Contains(got, w) {
					t.Errorf("unexpected %q in\n%s", w, got)
				}
			}
		})
	}
}

func TestFilterKeepsSurroundingContent(t *testing.T) {
	in := "<h2 id=\"a\">A</h2>\n<p>Intro</p>\n<p>" + yt + "</p>\n<p>After <span data-icon=\"info\"></span></p>"
	got, changed := Filter(in, english)
	if !changed || !strings.HasPrefix(got, "<h2 id=\"a\">A</h2>\n<p>Intro</p>\n<figure") ||
		!strings.HasSuffix(got, "</figure>\n<p>After <span data-icon=\"info\"></span></p>") {
		t.Fatalf("surrounding content changed:\n%s", got)
	}
}

func TestFilterIsIdempotent(t *testing.T) {
	once, _ := Filter(`<p>`+yt+`</p>`, english)
	if twice, changed := Filter(once, english); changed || twice != once {
		t.Fatalf("second run changed the output:\n%s", twice)
	}
}

func TestOptionsFrom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		lang     string
		check    func(Options) bool
		warns    int
	}{
		{"defaults", map[string]string{}, "en", func(o Options) bool {
			return o.Enabled["youtube"] && o.Enabled["vimeo"] && o.Enabled["tiktok"] && !o.Spanish && strings.HasPrefix(o.Notice, "Nothing is loaded from {provider}") && o.PrivacyURL == ""
		}, 0},
		{"spanish default note", map[string]string{}, "es-MX", func(o Options) bool {
			return o.Spanish && strings.HasPrefix(o.Notice, "No se carga nada de {provider}")
		}, 0},
		{"providers off", map[string]string{"youtube": "false", "vimeo": "false", "tiktok": "false"}, "en", func(o Options) bool {
			return !o.Enabled["youtube"] && !o.Enabled["vimeo"] && !o.Enabled["tiktok"]
		}, 0},
		{"per-language overrides", map[string]string{"notice": "N", "notice_es": "NE", "privacy_url": "/p", "privacy_url_es": "https://e.test/es"}, "es", func(o Options) bool {
			return o.Notice == "NE" && o.PrivacyURL == "https://e.test/es"
		}, 0},
		{"generic values for english", map[string]string{"notice": " N ", "notice_es": "NE", "privacy_url": "/p"}, "en", func(o Options) bool {
			return o.Notice == "N" && o.PrivacyURL == "/p"
		}, 0},
		{"unsafe privacy url dropped", map[string]string{"privacy_url": "javascript:alert(1)"}, "en", func(o Options) bool {
			return o.PrivacyURL == ""
		}, 1},
		{"protocol-relative privacy url dropped", map[string]string{"privacy_url": "//evil.test"}, "en", func(o Options) bool {
			return o.PrivacyURL == ""
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, warns := OptionsFrom(tc.settings, tc.lang)
			if !tc.check(o) || len(warns) != tc.warns {
				t.Errorf("got %+v, warnings %q", o, warns)
			}
		})
	}
}

func TestHandler(t *testing.T) {
	env := sdktest.New(t)
	post := &pluginsdk.Post{ID: 1, Type: "post", Lang: "es"}
	got, err := env.Filter(`<p>`+yt+`</p>`, post)
	if err != nil || !strings.Contains(got, "Reproducir vídeo de YouTube") {
		t.Fatalf("got %q, %v", got, err)
	}
	// Nothing to embed: the handler returns no HTML, so the host keeps it.
	r, err := env.Call(pluginsdk.HookContentFilter, pluginsdk.ContentFilter{HTML: "<p>x</p>", Post: post})
	if err != nil || strings.Contains(string(r.Raw), `"html"`) {
		t.Fatalf("unchanged content returned HTML: %s, %v", r.Raw, err)
	}
	// The site language is used when the post has none.
	env.Site.Language = "es"
	if got, _ := env.Filter(`<p>`+yt+`</p>`, nil); !strings.Contains(got, "Ver en YouTube") {
		t.Fatalf("site language not used: %s", got)
	}
	// A bad privacy URL is logged once per instance, never returned as an error.
	env.Plugin.Settings["privacy_url"] = "ftp://x"
	for range 2 {
		if _, err := env.Filter(`<p>`+yt+`</p>`, post); err != nil {
			t.Fatal(err)
		}
	}
	if len(env.Logs) != 1 {
		t.Fatalf("logs: %+v", env.Logs)
	}
	if r, err := env.Call(pluginsdk.HookWidget, map[string]string{}); err != nil || len(r.Raw) != 2 {
		t.Fatalf("other hooks: %s, %v", r.Raw, err)
	}
}
