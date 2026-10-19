package main

import (
	"strings"
	"testing"
	"time"
)

var (
	madrid, _ = time.LoadLocation("Europe/Madrid")
	sample    = record{S: "c:" + gfdChannel, Title: "Google for Developers", Videos: []Video{
		{ID: "LJ2v4-FvPcA", Title: `Tom & "Jerry" <b>`, Published: time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC).Unix(), Duration: 253, Short: true},
		{ID: "i3X7Fv0Z3Uc", Title: "Live now", Published: time.Date(2026, 10, 15, 16, 0, 0, 0, time.UTC).Unix(), Live: "live"},
		{ID: "nSwmcs37Jd4", Title: "", Published: 0},
	}}
	gfd = Source{kindChannel, gfdChannel}
)

func view(lang string) View {
	return View{Layout: layoutGrid, Count: 6, Thumbnails: true, Notice: "Note.", Text: textsFor(lang), Loc: madrid}
}

func TestGalleryExactOutput(t *testing.T) {
	rec := sample
	rec.Videos = rec.Videos[:1]
	v := view("en")
	v.PrivacyURL = "/privacy?a=1&b=2"
	got := Gallery(gfd, rec, v)
	want := `<section class="card ytg ytg-grid" data-consent="youtube" aria-label="Latest videos from Google for Developers on YouTube">` +
		`<ul class="ytg-list" role="list">` +
		`<li class="ytg-item"><a class="ytg-link" href="https://www.youtube.com/shorts/LJ2v4-FvPcA"><span class="ytg-thumb">` +
		`<img src="https://i.ytimg.com/vi/LJ2v4-FvPcA/hqdefault.jpg" alt="" width="480" height="360" loading="lazy">` +
		`<span class="ytg-badge" aria-hidden="true"><span data-icon="play"></span></span>` +
		`<span class="ytg-duration" aria-hidden="true">4:13</span></span>` +
		`<span class="ytg-body"><span class="ytg-title">Tom &amp; &#34;Jerry&#34; &lt;b&gt;</span><span class="sr-only"> (4 min 13 s)</span></span></a>` +
		`<span class="ytg-meta"><time datetime="2026-10-16T23:30:00Z">Oct 17, 2026</time></span>` +
		`<a class="ytg-player" href="https://www.youtube-nocookie.com/embed/LJ2v4-FvPcA?autoplay=1" aria-label="Play video: Tom &amp; Jerry b (YouTube)"></a></li>` +
		`</ul><footer class="ytg-footer"><p class="ytg-notice" data-consent-note>Note. <a href="/privacy?a=1&amp;b=2">Privacy policy</a></p>` +
		`<a class="ytg-more" href="https://www.youtube.com/channel/` + gfdChannel + `">More videos on YouTube</a></footer></section>`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestGallery(t *testing.T) {
	es := view("es")
	es.Layout, es.Heading = layoutCarousel, "Nuestros <vídeos>"
	noThumbs := view("en")
	noThumbs.Thumbnails, noThumbs.Notice = false, ""
	two := view("en")
	two.Count = 2
	widget := view("en")
	widget.Widget, widget.Layout = true, layoutCarousel
	playlist := Source{kindPlaylist, "PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ"}

	for _, tc := range []struct {
		name    string
		src     Source
		v       View
		want    []string
		without []string
		items   int
	}{
		{name: "spanish carousel", src: gfd, v: es, items: 3, want: []string{
			`<section class="card ytg ytg-carousel" data-consent="youtube" aria-label="Últimos vídeos de Google for Developers en YouTube">`,
			`<header class="ytg-head"><h2 class="ytg-heading">Nuestros &lt;vídeos&gt;</h2></header>`,
			`<time datetime="2026-10-15T16:00:00Z">15 oct 2026</time>`,
			`<span class="ytg-duration ytg-live" aria-hidden="true">En directo</span>`, `<span class="sr-only"> (En directo)</span>`,
			`aria-label="Reproducir vídeo: Live now (YouTube)"`, `>Más vídeos en YouTube</a>`,
			`<span class="ytg-ui" aria-hidden="true"><span class="ytg-ui-prev" aria-label="Vídeos anteriores"><span data-icon="chevron-left"></span></span><span class="ytg-ui-next" aria-label="Vídeos siguientes"><span data-icon="chevron-right"></span></span></span></section>`,
			`href="https://www.youtube.com/watch?v=i3X7Fv0Z3Uc"`}},
		{name: "untitled video without a date", src: gfd, v: view("en"), items: 3,
			want:    []string{`<span class="ytg-title">YouTube</span>`, `aria-label="Play video: YouTube (YouTube)"`},
			without: []string{`ytg-meta"><time datetime="1970`}},
		{name: "no thumbnails, no note", src: gfd, v: noThumbs, items: 3, without: []string{"<img", "ytg-notice", "i.ytimg.com"}},
		{name: "count", src: gfd, v: two, items: 2},
		{name: "widget", src: gfd, v: widget, items: 3, want: []string{`<div class="ytg ytg-carousel ytg-widget" data-consent="youtube"><ul class="ytg-list" role="list">`, `</span></span></span></div>`},
			without: []string{"<section", "card", "<h2"}},
		{name: "playlist", src: playlist, v: view("en"), items: 3, want: []string{
			`aria-label="Videos in the playlist Google for Developers on YouTube"`,
			`<a class="ytg-more" href="https://www.youtube.com/playlist?list=PLOU2XLYxmsIJ8ItHmK4bRlY4GCzMgXLAJ">Full playlist on YouTube</a>`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Gallery(tc.src, sample, tc.v)
			if n := strings.Count(got, `<li class="ytg-item">`); n != tc.items {
				t.Errorf("%d items, want %d", n, tc.items)
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
			if strings.Contains(got, "<script") || strings.Contains(got, "style=") || strings.Contains(got, "<iframe") || strings.Contains(got, "<button") {
				t.Errorf("markup the host would strip:\n%s", got)
			}
		})
	}
}

func TestEmpty(t *testing.T) {
	if got, want := Empty(gfd, "", view("en")), `<section class="card ytg ytg-empty" aria-label="Latest videos on YouTube">`+
		`<a class="ytg-more" href="https://www.youtube.com/channel/`+gfdChannel+`"><span data-icon="play"></span>Watch the videos on YouTube</a></section>`; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	w := view("es")
	w.Widget = true
	if got, want := Empty(Source{kindHandle, "GoogleDevelopers"}, "Google for Developers", w), `<div class="ytg ytg-empty ytg-widget">`+
		`<a class="ytg-more" href="https://www.youtube.com/@GoogleDevelopers"><span data-icon="play"></span>Ver los vídeos de Google for Developers en YouTube</a></div>`; got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestNotices(t *testing.T) {
	for _, tc := range []struct {
		lang   string
		thumbs bool
		start  string
	}{
		{"en", true, "Thumbnails are loaded from YouTube."},
		{"en-GB", false, "Nothing is loaded from YouTube until you press play."},
		{"es-MX", true, "Las miniaturas se cargan desde YouTube."},
		{"es", false, "No se carga nada de YouTube hasta que pulsas reproducir."},
		{"fr", true, "Thumbnails are loaded from YouTube."},
	} {
		if got := textsFor(tc.lang).Notice(tc.thumbs); !strings.HasPrefix(got, tc.start) {
			t.Errorf("%s thumbnails=%v: %q", tc.lang, tc.thumbs, got)
		}
	}
}
