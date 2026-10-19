package main

import (
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

const (
	defaultCount   = 8
	layoutGrid     = "grid"
	layoutCarousel = "carousel"
)

// View is how one feed is shown.
type View struct {
	Layout     string // layoutGrid or layoutCarousel
	Count      int    // 1 to maxItems
	Heading    string // optional heading (shortcodes)
	Widget     bool   // inside a widget: the host gives the frame and the title
	Notice     string // privacy note; "" for none
	PrivacyURL string // link after the note; "" for none
	Text       texts
	Loc        *time.Location
}

// render builds one feed from the attributes of a shortcode or the
// configuration of a widget; empty values use the plugin settings. It
// never fetches: the list comes from storage.
func render(req *pluginsdk.Request, count, layout, heading, lang string, widget bool, where string) string {
	v := viewFor(req, count, layout, lang, widget, where)
	v.Heading = heading
	f, ok, err := loadFeed()
	switch {
	case err != nil:
		logOnce(pluginsdk.Warn, "the plugin's storage is not available: "+err.Error())
		return ""
	case !ok || f.Account == "":
		logOnce(pluginsdk.Info, where+": nothing to show yet; the feed is fetched when Instagram is connected (Admin → Plugins → Instagram → Connections) and then every hour")
		return ""
	case !f.Showable(now()):
		// Nothing yet, or a list too old to show: its image addresses have
		// expired. The profile link stands in for it.
		return Empty(f.User, v)
	}
	return Gallery(f, v)
}

func viewFor(req *pluginsdk.Request, count, layout, lang string, widget bool, where string) View {
	s := req.Plugin.Settings
	t := textsFor(lang)
	v := View{Widget: widget, Text: t, Loc: time.UTC}
	if loc, err := time.LoadLocation(req.Site.Timezone); err == nil && req.Site.Timezone != "" {
		v.Loc = loc
	}
	v.Count = number(s["count"], defaultCount, "settings: posts in the feed")
	if strings.TrimSpace(count) != "" {
		v.Count = number(count, v.Count, where+": count")
	}
	v.Layout = layoutCarousel
	if strings.TrimSpace(s["layout"]) == layoutGrid && !widget {
		v.Layout = layoutGrid // the widget has its own setting
	}
	switch l := strings.ToLower(strings.TrimSpace(layout)); l {
	case "":
	case layoutGrid, layoutCarousel:
		v.Layout = l
	default:
		logOnce(pluginsdk.Warn, where+": layout "+strconv.Quote(layout)+" is neither carousel nor grid; using "+v.Layout)
	}
	if s["show_notice"] != "false" {
		v.Notice = t.Notice()
		if u := strings.TrimSpace(s["privacy_url"]); safeURL(u) {
			v.PrivacyURL = u
		} else if u != "" {
			logOnce(pluginsdk.Warn, "settings: privacy URL "+strconv.Quote(u)+" is not an http(s) URL or a site path; ignored")
		}
	}
	return v
}

// number reads a count from 1 to maxItems; anything else gives def.
func number(raw string, def int, what string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		logOnce(pluginsdk.Warn, what+" "+strconv.Quote(raw)+" is not a number from 1 to "+itoa(maxItems)+"; using "+itoa(def))
		return def
	}
	return min(n, maxItems)
}

// safeURL accepts absolute http(s) URLs and site paths ("/privacy").
func safeURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\"<>\\") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// Gallery renders the feed. Structure (see assets/instagram.css and
// assets/instagram.js):
//
//	section.card.igf.igf-grid|igf-carousel   (div.igf in a widget)
//	  header.igf-head > h2.igf-heading       only with a heading
//	  ul.igf-list > li.igf-item
//	    a.igf-link                            the post on Instagram
//	      span.igf-thumb > img                its image, or a placeholder
//	      span.igf-badge                      videos (play) and albums (images)
//	  footer.igf-footer                       the privacy note and the profile link
//	  span.igf-ui                             hidden labels and icons for the carousel buttons
//
// Without JavaScript every post is a link, and the carousel scrolls by
// touch, trackpad or keyboard; the script adds previous and next buttons.
func Gallery(f Feed, v View) string {
	items := f.Items
	if len(items) > v.Count {
		items = items[:v.Count]
	}
	t := v.Text
	var b strings.Builder
	class := "igf igf-" + v.Layout
	if v.Widget {
		b.WriteString(`<div class="` + class + ` igf-widget">`)
	} else {
		b.WriteString(`<section class="card ` + class + `" aria-label="` + attr(t.Label(f.User)) + `">`)
		if h := strings.TrimSpace(v.Heading); h != "" {
			b.WriteString(`<header class="igf-head"><h2 class="igf-heading">` + html.EscapeString(clip(h, 200)) + `</h2></header>`)
		}
	}
	b.WriteString(`<ul class="igf-list" role="list">`)
	for _, m := range items {
		item(&b, m, v)
	}
	b.WriteString(`</ul>`)
	footer(&b, f.User, v)
	if v.Layout == layoutCarousel {
		b.WriteString(`<span class="igf-ui" aria-hidden="true"><span class="igf-ui-prev" aria-label="` + attr(t.Prev()) + `"><span data-icon="chevron-left"></span></span>` +
			`<span class="igf-ui-next" aria-label="` + attr(t.Next()) + `"><span data-icon="chevron-right"></span></span></span>`)
	}
	if v.Widget {
		b.WriteString(`</div>`)
	} else {
		b.WriteString(`</section>`)
	}
	return b.String()
}

func item(b *strings.Builder, m Media, v View) {
	t := v.Text
	alt := m.Alt
	if alt == "" {
		alt = t.Post(m.Time, v.Loc)
	}
	b.WriteString(`<li class="igf-item"><a class="igf-link" href="` + html.EscapeString(m.Link) + `">`)
	if m.Image != "" {
		// Instagram gives no image sizes: the stylesheet crops every image
		// to a square, as the profile grid does. The description is the
		// link's text rather than the image's alt, because the host
		// sanitizer drops an alt with characters such as "#", "?" or an
		// emoji, which captions are full of.
		b.WriteString(`<span class="igf-thumb"><img src="` + html.EscapeString(m.Image) + `" alt="" width="640" height="640" loading="lazy"></span>`)
	} else {
		b.WriteString(`<span class="igf-thumb igf-none" aria-hidden="true"><span data-icon="images"></span></span>`)
	}
	b.WriteString(`<span class="sr-only">` + html.EscapeString(alt) + `</span>`)
	switch m.Kind {
	case "video":
		b.WriteString(`<span class="igf-badge igf-video" aria-hidden="true"><span data-icon="play"></span></span><span class="sr-only"> (` + t.Video() + `)</span>`)
	case "album":
		b.WriteString(`<span class="igf-badge" aria-hidden="true"><span data-icon="images"></span></span><span class="sr-only"> (` + t.Album() + `)</span>`)
	}
	b.WriteString(`</a></li>`)
}

func footer(b *strings.Builder, user string, v View) {
	b.WriteString(`<footer class="igf-footer">`)
	if v.Notice != "" {
		b.WriteString(`<p class="igf-notice">` + html.EscapeString(v.Notice))
		if v.PrivacyURL != "" {
			b.WriteString(` <a href="` + html.EscapeString(v.PrivacyURL) + `">` + html.EscapeString(v.Text.Policy()) + `</a>`)
		}
		b.WriteString(`</p>`)
	}
	if p := profile(user); p != "" {
		b.WriteString(`<a class="igf-more" href="` + html.EscapeString(p) + `">` + html.EscapeString(v.Text.More(user)) + `</a>`)
	}
	b.WriteString(`</footer>`)
}

// Empty renders the profile link that stands in for a feed whose posts are
// not known yet or are too old to show; "" without a username.
func Empty(user string, v View) string {
	p := profile(user)
	if p == "" {
		return ""
	}
	link := `<a class="igf-more" href="` + html.EscapeString(p) + `"><span data-icon="images"></span>` + html.EscapeString(v.Text.Empty(user)) + `</a>`
	if v.Widget {
		return `<div class="igf igf-empty igf-widget">` + link + `</div>`
	}
	return `<section class="card igf igf-empty" aria-label="` + attr(v.Text.Label(user)) + `">` + link + `</section>`
}

// attr escapes an aria-label value, after dropping the characters the
// host sanitizer refuses there (<, > and ") and cutting it to 200
// characters.
func attr(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == '"' {
			return -1
		}
		return r
	}, s)
	return html.EscapeString(clip(s, 200))
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// The texts visitors see, in English and Spanish (the languages of the
// site's interface); any other language gets English. The script asset has
// no text of its own: it reads its labels from the page.
type texts struct{ es bool }

func textsFor(lang string) texts {
	lang = strings.ToLower(strings.TrimSpace(lang))
	return texts{es: lang == "es" || strings.HasPrefix(lang, "es-") || strings.HasPrefix(lang, "es_")}
}

func (t texts) pick(en, es string) string {
	if t.es {
		return es
	}
	return en
}

// Label names a feed for assistive technology.
func (t texts) Label(user string) string {
	if user == "" {
		return t.pick("Latest posts on Instagram", "Últimas publicaciones en Instagram")
	}
	return t.pick("Latest posts from @"+user+" on Instagram", "Últimas publicaciones de @"+user+" en Instagram")
}

func (t texts) More(user string) string {
	return t.pick("@"+user+" on Instagram", "@"+user+" en Instagram")
}

func (t texts) Empty(user string) string {
	return t.pick("See @"+user+" on Instagram", "Ver @"+user+" en Instagram")
}

func (t texts) Prev() string  { return t.pick("Previous posts", "Publicaciones anteriores") }
func (t texts) Next() string  { return t.pick("Next posts", "Publicaciones siguientes") }
func (t texts) Video() string { return t.pick("video", "vídeo") }
func (t texts) Album() string { return t.pick("several photos or videos", "varias fotos o vídeos") }

func (t texts) Policy() string { return t.pick("Privacy policy", "Política de privacidad") }

func (t texts) Notice() string {
	return t.pick("Images are loaded from Instagram (Meta), which may collect data about your visit.",
		"Las imágenes se cargan desde Instagram (Meta), que puede recoger datos de tu visita.")
}

var spanishMonths = [...]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

// Post is the alt text of a post without a caption: "Instagram post of
// Jan 2, 2026" or "Publicación de Instagram del 2 ene 2026".
func (t texts) Post(unix int64, loc *time.Location) string {
	if unix == 0 {
		return t.pick("Instagram post", "Publicación de Instagram")
	}
	d := time.Unix(unix, 0).In(loc)
	if t.es {
		return fmt.Sprintf("Publicación de Instagram del %d %s %d", d.Day(), spanishMonths[d.Month()-1], d.Year())
	}
	return fmt.Sprintf("Instagram post of %s %d, %d", d.Month().String()[:3], d.Day(), d.Year())
}
