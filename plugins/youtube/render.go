package main

import (
	"html"
	"net/url"
	"strings"
	"time"
)

// View is how one gallery is shown.
type View struct {
	Layout     string // layoutGrid or layoutCarousel
	Count      int    // 1 to maxVideos
	Heading    string // optional heading (shortcodes)
	Widget     bool   // inside a widget: the host gives the frame and the title
	Thumbnails bool   // load thumbnails from i.ytimg.com
	Notice     string // privacy note; "" for none
	PrivacyURL string // link after the note; "" for none
	Text       texts
	Loc        *time.Location
}

const (
	layoutGrid     = "grid"
	layoutCarousel = "carousel"
)

// Gallery renders the videos of rec. Structure (see assets/youtube.css and
// assets/youtube.js):
//
//	section.card.ytg.ytg-grid|ytg-carousel   (div.ytg in a widget)
//	  header.ytg-head > h2.ytg-heading       only with a heading
//	  ul.ytg-list > li.ytg-item
//	    a.ytg-link                            the video on YouTube: thumbnail and title
//	    span.ytg-meta > time                  the publication date
//	    a.ytg-player                          the youtube-nocookie player; hidden, read by the script
//	  footer.ytg-footer                       the privacy note and the source's page
//	  span.ytg-ui                             hidden labels and icons for the carousel buttons
//
// Without JavaScript every item is a link to YouTube. The script turns the
// thumbnails into play buttons and creates the player only on a click.
func Gallery(src Source, rec record, v View) string {
	videos := rec.Videos
	if len(videos) > v.Count {
		videos = videos[:v.Count]
	}
	t := v.Text
	var b strings.Builder
	class := "ytg ytg-" + v.Layout
	if v.Widget {
		b.WriteString(`<div class="` + class + ` ytg-widget" data-consent="youtube">`)
	} else {
		b.WriteString(`<section class="card ` + class + `" data-consent="youtube" aria-label="` + attr(t.Label(src, rec.Title)) + `">`)
		if h := strings.TrimSpace(v.Heading); h != "" {
			b.WriteString(`<header class="ytg-head"><h2 class="ytg-heading">` + html.EscapeString(cleanText(h, 200)) + `</h2></header>`)
		}
	}
	b.WriteString(`<ul class="ytg-list" role="list">`)
	for _, vid := range videos {
		item(&b, vid, v)
	}
	b.WriteString(`</ul>`)
	footer(&b, src, v)
	if v.Layout == layoutCarousel {
		b.WriteString(`<span class="ytg-ui" aria-hidden="true"><span class="ytg-ui-prev" aria-label="` + attr(t.Prev()) + `"><span data-icon="chevron-left"></span></span>` +
			`<span class="ytg-ui-next" aria-label="` + attr(t.Next()) + `"><span data-icon="chevron-right"></span></span></span>`)
	}
	if v.Widget {
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`</section>`)
	}
	return b.String()
}

func item(b *strings.Builder, vid Video, v View) {
	t := v.Text
	watch := url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: url.Values{"v": {vid.ID}}.Encode()}
	if vid.Short {
		watch = url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/shorts/" + vid.ID}
	}
	player := url.URL{Scheme: "https", Host: "www.youtube-nocookie.com", Path: "/embed/" + vid.ID, RawQuery: "autoplay=1"}
	title := vid.Title
	if title == "" {
		title = "YouTube"
	}
	b.WriteString(`<li class="ytg-item"><a class="ytg-link" href="` + html.EscapeString(watch.String()) + `"><span class="ytg-thumb">`)
	if v.Thumbnails {
		// hqdefault (480×360) exists for every video; the stylesheet crops
		// its letterbox to 16:9.
		thumb := url.URL{Scheme: "https", Host: "i.ytimg.com", Path: "/vi/" + vid.ID + "/hqdefault.jpg"}
		b.WriteString(`<img src="` + html.EscapeString(thumb.String()) + `" alt="" width="480" height="360" loading="lazy">`)
	}
	b.WriteString(`<span class="ytg-badge" aria-hidden="true"><span data-icon="play"></span></span>`)
	spoken := ""
	switch {
	case vid.Live != "":
		b.WriteString(`<span class="ytg-duration ytg-live" aria-hidden="true">` + html.EscapeString(t.Live(vid.Live)) + `</span>`)
		spoken = t.Live(vid.Live)
	case vid.Duration > 0:
		b.WriteString(`<span class="ytg-duration" aria-hidden="true">` + Clock(vid.Duration) + `</span>`)
		spoken = Spoken(vid.Duration)
	}
	b.WriteString(`</span><span class="ytg-body"><span class="ytg-title">` + html.EscapeString(title) + `</span>`)
	if spoken != "" {
		b.WriteString(`<span class="sr-only"> (` + html.EscapeString(spoken) + `)</span>`)
	}
	b.WriteString(`</span></a>`)
	if vid.Published > 0 {
		d := time.Unix(vid.Published, 0).In(v.Loc)
		b.WriteString(`<span class="ytg-meta"><time datetime="` + d.UTC().Format(time.RFC3339) + `">` + html.EscapeString(t.Date(d)) + `</time></span>`)
	}
	b.WriteString(`<a class="ytg-player" href="` + html.EscapeString(player.String()) + `" aria-label="` + attr(t.Play(title)) + `"></a></li>`)
}

func footer(b *strings.Builder, src Source, v View) {
	b.WriteString(`<footer class="ytg-footer">`)
	if v.Notice != "" {
		b.WriteString(`<p class="ytg-notice" data-consent-note>` + html.EscapeString(v.Notice))
		if v.PrivacyURL != "" {
			b.WriteString(` <a href="` + html.EscapeString(v.PrivacyURL) + `">` + html.EscapeString(v.Text.Policy()) + `</a>`)
		}
		b.WriteString(`</p>`)
	}
	b.WriteString(`<a class="ytg-more" href="` + html.EscapeString(src.Page()) + `">` + html.EscapeString(v.Text.More(src)) + `</a></footer>`)
}

// Empty renders the link that stands in for a gallery whose videos are not
// known yet (before the first fetch) or are too old to show.
func Empty(src Source, name string, v View) string {
	link := `<a class="ytg-more" href="` + html.EscapeString(src.Page()) + `"><span data-icon="play"></span>` + html.EscapeString(v.Text.Empty(src, name)) + `</a>`
	if v.Widget {
		return `<div class="ytg ytg-empty ytg-widget">` + link + `</div>`
	}
	return `<section class="card ytg ytg-empty" aria-label="` + attr(v.Text.Label(src, name)) + `">` + link + `</section>`
}

// attr escapes an aria-label value, after dropping the characters the
// host sanitizer refuses there (<, > and ") and cutting it to 200
// characters. URLs are only escaped.
func attr(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == '"' {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return html.EscapeString(s)
}
