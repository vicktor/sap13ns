package content

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// Video is one YouTube or Vimeo video (or YouTube playlist) named in post
// content. Every field has been validated: ids match the provider's id
// format, so they are safe in URLs and file names.
type Video struct {
	Provider string // "youtube" or "vimeo"
	ID       string // YouTube video id or Vimeo video number ("" for a playlist)
	List     string // YouTube playlist id (playlists only)
	Hash     string // Vimeo hash of an unlisted video
	Start    int    // seconds from the start
	Portrait bool   // YouTube Shorts: shown 9:16
	Title    string // the [video] shortcode's title ("" when not given)
}

var (
	youtubeID   = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	youtubeList = regexp.MustCompile(`^[A-Za-z0-9_-]{10,64}$`)
	vimeoID     = regexp.MustCompile(`^[1-9][0-9]{0,11}$`)
	vimeoHash   = regexp.MustCompile(`^[0-9a-f]{6,20}$`)
	vimeoName   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// 90, 90s, 1m30s, 1h2m3s; and 1:30 or 1:02:03 in the shortcode.
	durationRe = regexp.MustCompile(`^(?:(\d{1,2})h)?(?:(\d{1,4})m)?(?:(\d{1,6})s?)?$`)
	clockRe    = regexp.MustCompile(`^(?:(\d{1,2}):)?(\d{1,3}):(\d{2})$`)
)

// maxStart caps start offsets (99:59:59), which keeps them to six digits.
const maxStart = 99*3600 + 59*60 + 59

// ParseVideoURL recognises the page URL of a YouTube video, Short, live
// stream or playlist, or of a Vimeo video, on the providers' own hosts
// only (no look-alike domains, credentials or ports).
func ParseVideoURL(raw string) (Video, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return Video{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Opaque != "" || u.Port() != "" {
		return Video{}, false
	}
	switch strings.ToLower(u.Host) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com":
		return parseYouTube(u)
	case "youtu.be":
		segs := pathSegments(u)
		if len(segs) != 1 || !youtubeID.MatchString(segs[0]) {
			return Video{}, false
		}
		return Video{Provider: "youtube", ID: segs[0], Start: youtubeStart(u)}, true
	case "vimeo.com", "www.vimeo.com", "player.vimeo.com":
		return parseVimeo(u)
	}
	return Video{}, false
}

func parseYouTube(u *url.URL) (Video, bool) {
	segs := pathSegments(u)
	q := u.Query()
	v := Video{Provider: "youtube", Start: youtubeStart(u)}
	switch {
	case len(segs) == 1 && segs[0] == "watch":
		v.ID = q.Get("v")
	case len(segs) == 1 && segs[0] == "playlist":
		if l := q.Get("list"); youtubeList.MatchString(l) {
			return Video{Provider: "youtube", List: l}, true
		}
		return Video{}, false
	case len(segs) == 2 && segs[0] == "embed" && segs[1] == "videoseries":
		if l := q.Get("list"); youtubeList.MatchString(l) {
			return Video{Provider: "youtube", List: l}, true
		}
		return Video{}, false
	case len(segs) == 2 && (segs[0] == "embed" || segs[0] == "live" || segs[0] == "v"):
		v.ID = segs[1]
	case len(segs) == 2 && segs[0] == "shorts":
		v.ID, v.Portrait = segs[1], true
	default:
		return Video{}, false
	}
	if !youtubeID.MatchString(v.ID) {
		return Video{}, false
	}
	return v, true
}

func parseVimeo(u *url.URL) (Video, bool) {
	segs := pathSegments(u)
	v := Video{Provider: "vimeo", Hash: u.Query().Get("h"), Start: vimeoStart(u)}
	if strings.EqualFold(u.Host, "player.vimeo.com") {
		if len(segs) != 2 || segs[0] != "video" {
			return Video{}, false
		}
		v.ID = segs[1]
	} else {
		switch {
		case len(segs) == 1:
			v.ID = segs[0]
		case len(segs) == 2 && vimeoID.MatchString(segs[0]):
			v.ID, v.Hash = segs[0], segs[1]
		case len(segs) == 3 && segs[0] == "channels" && vimeoName.MatchString(segs[1]):
			v.ID = segs[2]
		case len(segs) == 4 && segs[0] == "groups" && vimeoName.MatchString(segs[1]) && segs[2] == "videos":
			v.ID = segs[3]
		default:
			return Video{}, false
		}
	}
	if !vimeoID.MatchString(v.ID) || (v.Hash != "" && !vimeoHash.MatchString(v.Hash)) {
		return Video{}, false
	}
	return v, true
}

// pathSegments splits the path into its non-empty parts.
func pathSegments(u *url.URL) []string {
	var out []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func youtubeStart(u *url.URL) int {
	q := u.Query()
	for _, k := range []string{"t", "start"} {
		if n := ParseStart(q.Get(k)); n > 0 {
			return n
		}
	}
	return fragmentStart(u.Fragment)
}

func vimeoStart(u *url.URL) int {
	if n := fragmentStart(u.Fragment); n > 0 {
		return n
	}
	return ParseStart(u.Query().Get("t"))
}

func fragmentStart(f string) int {
	for _, part := range strings.Split(f, "&") {
		if t, ok := strings.CutPrefix(part, "t="); ok {
			return ParseStart(t)
		}
	}
	return 0
}

// ParseStart reads a start offset: "90", "90s", "1m30s", "1h2m3s", "1:30"
// or "1:02:03". It returns 0 for anything else or beyond 99:59:59.
func ParseStart(s string) int {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0
	}
	n := 0
	if m := clockRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		sec, _ := strconv.Atoi(m[3])
		if sec > 59 {
			return 0
		}
		n = h*3600 + mi*60 + sec
	} else if m := durationRe.FindStringSubmatch(s); m != nil {
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		sec, _ := strconv.Atoi(m[3])
		n = h*3600 + mi*60 + sec
	}
	if n <= 0 || n > maxStart {
		return 0
	}
	return n
}

// ProviderName is the provider's name as visitors know it.
func (v Video) ProviderName() string {
	if v.Provider == "vimeo" {
		return "Vimeo"
	}
	return "YouTube"
}

// Key names the video in the poster cache: yt-ID, ytpl-LIST or vm-ID.
func (v Video) Key() string {
	switch {
	case v.Provider == "vimeo":
		return "vm-" + v.ID
	case v.List != "":
		return "ytpl-" + v.List
	}
	return "yt-" + v.ID
}

// WatchURL is the video's page on the provider's site.
func (v Video) WatchURL() string {
	switch {
	case v.Provider == "vimeo":
		s := "https://vimeo.com/" + v.ID
		if v.Hash != "" {
			s += "/" + v.Hash
		}
		if v.Start > 0 {
			s += "#t=" + strconv.Itoa(v.Start) + "s"
		}
		return s
	case v.List != "":
		return "https://www.youtube.com/playlist?list=" + v.List
	case v.Portrait:
		return "https://www.youtube.com/shorts/" + v.ID
	}
	s := "https://www.youtube.com/watch?v=" + v.ID
	if v.Start > 0 {
		s += "&t=" + strconv.Itoa(v.Start) + "s"
	}
	return s
}

// PlayerURL is the privacy-friendly player loaded after the visitor's
// click: YouTube's privacy-enhanced mode (youtube-nocookie.com) and Vimeo's
// player with dnt=1 (no session tracking).
//
//   - YouTube's parameters (autoplay, start, list):
//     https://developers.google.com/youtube/player_parameters, and
//     privacy-enhanced mode: https://support.google.com/youtube/answer/171780
//   - Vimeo's (autoplay, dnt, #t): "About Player Parameters",
//     https://help.vimeo.com/hc/en-us/articles/12426260232977-Player-parameters-overview;
//     h is the hash of an unlisted video, as Vimeo's own embed codes carry it.
func (v Video) PlayerURL() string {
	switch {
	case v.Provider == "vimeo":
		s := "https://player.vimeo.com/video/" + v.ID + "?autoplay=1&dnt=1"
		if v.Hash != "" {
			s += "&h=" + v.Hash
		}
		if v.Start > 0 {
			s += "#t=" + strconv.Itoa(v.Start) + "s"
		}
		return s
	case v.List != "":
		return "https://www.youtube-nocookie.com/embed/videoseries?list=" + v.List + "&autoplay=1"
	}
	s := "https://www.youtube-nocookie.com/embed/" + v.ID + "?autoplay=1"
	if v.Start > 0 {
		s += "&start=" + strconv.Itoa(v.Start)
	}
	return s
}

// PlayerURLPattern matches every PlayerURL and nothing else. The sanitizer
// and the player script (ui/static/video.js) check it before a URL becomes
// an iframe.
var PlayerURLPattern = regexp.MustCompile(`^https://(?:www\.youtube-nocookie\.com/embed/(?:[A-Za-z0-9_-]{11}\?autoplay=1(?:&start=[0-9]{1,6})?|videoseries\?list=[A-Za-z0-9_-]{10,64}&autoplay=1)|player\.vimeo\.com/video/[1-9][0-9]{0,11}\?autoplay=1&dnt=1(?:&h=[0-9a-f]{6,20})?(?:#t=[0-9]{1,6}s)?)$`)

// PlayerOrigins are the origins a page with a video must allow in
// frame-src.
var PlayerOrigins = []string{"https://www.youtube-nocookie.com", "https://player.vimeo.com"}

// VideoMode says how videos are rendered.
type VideoMode int

const (
	// VideoPlayer renders the click-to-load player.
	VideoPlayer VideoMode = iota
	// VideoFeed renders a link with the poster, for feed readers.
	VideoFeed
	// VideoWordPress renders the URL alone in a paragraph, which WordPress
	// embeds (export).
	VideoWordPress
)

// VideoMeta is what the poster cache knows about a video.
type VideoMeta struct {
	Title    string // the provider's title
	Poster   string // poster URL on this site ("" when none is cached)
	Width    int    // poster size
	Height   int
	Portrait bool // a vertical video (Vimeo reports its size)
}

// VideoOptions turn video links into players. Without them (nil) a bare
// URL stays a link and the [video] shortcode becomes one.
type VideoOptions struct {
	Mode VideoMode
	// Meta returns the cached title and poster; it must not block.
	Meta func(Video) VideoMeta
	// Found is called for every video rendered (prefetching posters).
	Found func(Video)
	// Tr translates a UI string ("Watch on %s"); nil formats the English.
	Tr func(key string, args ...any) string
}

func (o *VideoOptions) t(key string, args ...any) string {
	if o != nil && o.Tr != nil {
		return o.Tr(key, args...)
	}
	return fmt.Sprintf(key, args...)
}

// videoLine reports whether a paragraph's only line names a video: a bare
// URL (or <URL>), as WordPress embeds, or [video url="…" start="…"
// title="…"]. ok is false for anything else, which stays as it is.
func videoLine(line string) (v Video, shortcode, ok bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[video") && strings.HasSuffix(line, "]") {
		m := openTag.FindStringSubmatch(line)
		if m == nil || m[0] != line || m[1] != "video" {
			return Video{}, false, false
		}
		attrs := parseAttrs(m[2])
		v, ok := ParseVideoURL(attrs["url"])
		if !ok {
			return Video{}, false, false
		}
		if s, set := attrs["start"]; set {
			v.Start = ParseStart(s)
		}
		v.Title = collapseSpace(attrs["title"])
		if len(v.Title) > 200 {
			v.Title = v.Title[:200]
		}
		return v, true, true
	}
	if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
		line = line[1 : len(line)-1]
	}
	if strings.ContainsAny(line, " \t<>\"'") {
		return Video{}, false, false
	}
	v, ok = ParseVideoURL(line)
	return v, false, ok
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// videoHTML renders one video. n numbers untitled videos of a provider
// ("YouTube video 2").
func videoHTML(v Video, meta VideoMeta, o *VideoOptions, n int) string {
	watch := escapeHTMLAttr(v.WatchURL())
	if o != nil && o.Mode == VideoWordPress {
		return "<p>" + watch + "</p>\n"
	}
	provider := v.ProviderName()
	title := v.Title
	if title == "" {
		title = meta.Title
	}
	named := title != ""
	if !named {
		if n > 1 {
			title = o.t("%s video %d", provider, n)
		} else {
			title = o.t("%s video", provider)
		}
	}
	if o == nil {
		return `<p><a href="` + watch + `">` + escapeHTMLText(title) + "</a></p>\n"
	}
	if o.Mode == VideoFeed {
		// Feed readers run no scripts and frame nothing: the poster and the
		// title link to the video. data-video-provider marks it as the
		// core's, so that content filters leave it alone (ProtectVideos).
		label := title
		if named {
			label += " (" + provider + ")"
		}
		link := `<a href="` + watch + `">` + escapeHTMLText(label) + `</a>`
		if meta.Poster == "" {
			return `<figure data-video-provider="` + v.Provider + `">` + link + "</figure>\n"
		}
		return fmt.Sprintf(`<figure data-video-provider="%s"><a href="%s"><img src="%s" alt="%s"%s></a><figcaption>%s</figcaption></figure>`+"\n",
			v.Provider, watch, escapeHTMLAttr(meta.Poster), escapeHTMLAttr(o.t("Watch on %s", provider)+": "+title), sizeAttrs(meta), link)
	}
	frameTitle := title
	if named {
		frameTitle += " (" + provider + ")"
	}
	play := o.t("Play video: %s", frameTitle)
	if !named {
		play = o.t("Play %s", title)
	}
	class := "sap-video sap-video-" + v.Provider
	if v.Portrait || meta.Portrait {
		class += " sap-video-portrait"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="%s" data-video-provider="%s">`, class, v.Provider)
	fmt.Fprintf(&b, `<a class="sap-video-link" href="%s" data-video-src="%s" data-video-title="%s" data-video-label="%s">`,
		watch, escapeHTMLAttr(v.PlayerURL()), escapeHTMLAttr(frameTitle), escapeHTMLAttr(play))
	if meta.Poster != "" {
		fmt.Fprintf(&b, `<img class="sap-video-poster" src="%s" alt=""%s loading="lazy" decoding="async">`, escapeHTMLAttr(meta.Poster), sizeAttrs(meta))
	}
	b.WriteString(`<span class="sap-video-play" aria-hidden="true"></span>`)
	fmt.Fprintf(&b, `<span class="sap-video-caption"><span class="sap-video-title">%s</span><span class="sap-video-source">%s</span></span>`,
		escapeHTMLText(title), escapeHTMLText(o.t("Watch on %s", provider)))
	b.WriteString("</a></figure>\n")
	return b.String()
}

func sizeAttrs(m VideoMeta) string {
	if m.Width <= 0 || m.Height <= 0 || m.Width > 4096 || m.Height > 4096 {
		return ""
	}
	return fmt.Sprintf(` width="%d" height="%d"`, m.Width, m.Height)
}

var (
	attrEscaper = strings.NewReplacer(`&`, "&amp;", `"`, "&#34;", `<`, "&lt;", `>`, "&gt;", `'`, "&#39;")
	textEscaper = strings.NewReplacer(`&`, "&amp;", `<`, "&lt;", `>`, "&gt;")
)

func escapeHTMLAttr(s string) string { return attrEscaper.Replace(s) }
func escapeHTMLText(s string) string { return textEscaper.Replace(s) }

// HasVideo reports whether rendered HTML holds a video player, so that the
// page needs the player script and the providers in frame-src.
func HasVideo(html string) bool { return strings.Contains(html, `<figure class="sap-video `) }

// videoFigure matches one player, or one video of a feed, rendered by
// videoHTML (after the sanitizer, which keeps the markup as it is). They
// never nest.
var videoFigure = regexp.MustCompile(`(?s)<figure (?:class="sap-video [^"]*" )?data-video-provider="(?:youtube|vimeo)">.*?</figure>`)

// ProtectVideos swaps every player for a text token, so that content
// filters (which sanitise their output with PluginPolicy, and could embed
// the link a second time) leave it alone. restore puts the players back
// (restoreVideos). The tokens carry a random nonce, new on every call, so
// post content cannot forge one.
func ProtectVideos(html string) (protected string, restore func(string) string) {
	identity := func(s string) string { return s }
	if !strings.Contains(html, ` data-video-provider="`) {
		return html, identity
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return html, identity
	}
	prefix := "sapvideo" + hex.EncodeToString(b) + "x"
	var saved []string
	protected = videoFigure.ReplaceAllStringFunc(html, func(m string) string {
		saved = append(saved, m)
		return prefix + strconv.Itoa(len(saved)-1)
	})
	if len(saved) == 0 {
		return html, identity
	}
	return protected, func(s string) string { return restoreVideos(s, prefix, saved) }
}

// videoParents are the elements a restored player may be in: a filter can
// wrap the post in a section, a quote or a list, but a player inside a
// link, a heading or a paragraph of text would not be the core's markup.
// videoAncestors may be further out (the list or table of an item or cell).
var (
	videoParents = map[string]bool{
		"div": true, "section": true, "article": true, "aside": true, "header": true, "footer": true, "main": true,
		"blockquote": true, "li": true, "dd": true, "details": true, "td": true, "th": true,
	}
	videoAncestors = map[string]bool{
		"ul": true, "ol": true, "dl": true, "table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true,
	}
)

var voidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true,
	"link": true, "meta": true, "source": true, "track": true, "wbr": true,
}

// restoreVideos puts the players saved by ProtectVideos back into filtered
// HTML, where the filters left their tokens. The filtered HTML is never
// spliced blindly: it is tokenized, and a token becomes its player only
//
//   - in a text node made of tokens and white space alone, or in a <p> that
//     holds nothing else (a filter that wraps text in paragraphs; the
//     paragraph goes),
//   - in one of videoParents, or in none,
//   - the first time it appears (a token a filter copied stays out).
//
// Anywhere else the token is dropped: from other text, and from tags,
// where an attribute that holds one is removed whole (player markup in an
// attribute value would break out of it, and a value with the token cut
// out would be one the sanitizer never checked). A token a filter removed
// stays removed.
func restoreVideos(s, prefix string, saved []string) string {
	if !strings.Contains(s, prefix) {
		return s
	}
	type tok struct {
		tt   xhtml.TokenType
		raw  string
		name string
		t    xhtml.Token // tags holding a token only
	}
	var toks []tok
	z := xhtml.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break // io.EOF: sanitised HTML is well formed
		}
		t := tok{tt: tt, raw: string(z.Raw())}
		switch tt {
		case xhtml.StartTagToken, xhtml.EndTagToken, xhtml.SelfClosingTagToken:
			if strings.Contains(t.raw, prefix) {
				t.t = z.Token()
				t.name = t.t.Data
			} else {
				name, _ := z.TagName()
				t.name = string(name)
			}
		}
		toks = append(toks, t)
	}
	used := make([]bool, len(saved))
	// text writes a text node: its tokens become players when allowed.
	text := func(b *strings.Builder, raw string, allowed bool) {
		allowed = allowed && onlyVideoTokens(raw, prefix)
		for _, part := range splitVideoTokens(raw, prefix) {
			switch {
			case part.index < 0:
				b.WriteString(part.text)
			case allowed && part.index < len(saved) && !used[part.index]:
				used[part.index] = true
				b.WriteString(saved[part.index])
			}
		}
	}
	var open []string
	inFlow := func() bool {
		for i, name := range open {
			if !videoParents[name] && (i == len(open)-1 || !videoAncestors[name]) {
				return false
			}
		}
		return true
	}
	var b strings.Builder
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.tt {
		case xhtml.TextToken:
			if strings.Contains(t.raw, prefix) {
				text(&b, t.raw, inFlow())
				continue
			}
		case xhtml.StartTagToken:
			if t.name == "p" && !strings.Contains(t.raw, prefix) && i+2 < len(toks) &&
				toks[i+1].tt == xhtml.TextToken && strings.Contains(toks[i+1].raw, prefix) &&
				toks[i+2].tt == xhtml.EndTagToken && toks[i+2].name == "p" && inFlow() && onlyVideoTokens(toks[i+1].raw, prefix) {
				text(&b, toks[i+1].raw, true)
				i += 2
				continue
			}
			if !voidElements[t.name] {
				open = append(open, t.name)
			}
		case xhtml.EndTagToken:
			for j := len(open) - 1; j >= 0; j-- {
				if open[j] == t.name {
					open = open[:j]
					break
				}
			}
		}
		if !strings.Contains(t.raw, prefix) {
			b.WriteString(t.raw)
			continue
		}
		// A token in markup: drop the attributes that hold it, or the
		// whole tag or comment when it is anywhere else.
		switch t.tt {
		case xhtml.StartTagToken, xhtml.EndTagToken, xhtml.SelfClosingTagToken:
			if strings.Contains(t.t.Data, prefix) {
				continue
			}
			kept := t.t.Attr[:0]
			for _, a := range t.t.Attr {
				if !strings.Contains(a.Key, prefix) && !strings.Contains(a.Val, prefix) && !strings.Contains(a.Namespace, prefix) {
					kept = append(kept, a)
				}
			}
			t.t.Attr = kept
			b.WriteString(t.t.String())
		}
	}
	return b.String()
}

type videoPart struct {
	text  string
	index int // the saved player's index, -1 for text
}

// splitVideoTokens splits text into tokens (prefix and a number) and the
// text between them.
func splitVideoTokens(s, prefix string) []videoPart {
	var parts []videoPart
	for {
		i := strings.Index(s, prefix)
		if i < 0 {
			if s != "" {
				parts = append(parts, videoPart{text: s, index: -1})
			}
			return parts
		}
		if i > 0 {
			parts = append(parts, videoPart{text: s[:i], index: -1})
		}
		j := i + len(prefix)
		for j < len(s) && j-i-len(prefix) < 6 && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		n, err := strconv.Atoi(s[i+len(prefix) : j])
		if err != nil {
			n = len(s) // no number: a token that matches no player
		}
		parts = append(parts, videoPart{text: s[i:j], index: n})
		s = s[j:]
	}
}

// onlyVideoTokens reports whether text holds tokens and white space alone.
func onlyVideoTokens(s, prefix string) bool {
	for _, part := range splitVideoTokens(s, prefix) {
		if part.index < 0 && strings.TrimSpace(part.text) != "" {
			return false
		}
	}
	return true
}
