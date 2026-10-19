package wpimport

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ConvertOptions tunes ToMarkdown. The zero value is valid.
type ConvertOptions struct {
	// BaseURL resolves relative links and images, normally the site address.
	BaseURL string
	// RewriteURL maps the absolute http(s) URL of a link, image or media file
	// to its new location (new media URLs, new permalinks). Nil keeps URLs
	// as they are.
	RewriteURL func(old string) string
	// Gallery returns the images of a [gallery] shortcode: ids lists its
	// ids/include attribute and is empty when the gallery shows the images
	// attached to the post. Nil drops galleries.
	Gallery func(ids []int64) []Image
	// Warn receives notes about content that could not be kept (forms,
	// unknown shortcodes…). Nil discards them.
	Warn func(msg string)
}

// Image is one image of a gallery.
type Image struct {
	URL     string
	Alt     string
	Caption string
}

var (
	reBlockComment = regexp.MustCompile(`(?s)<!--\s*/?wp:.*?-->`)
	reMore         = regexp.MustCompile(`<!--more(.*?)?-->`)
	reCodeSC       = regexp.MustCompile(`(?s)\[(code|sourcecode|php|js|javascript|css|html|xml|bash|shell|sql|python|ruby|java|cpp|c|go|json|yaml|text|plain)\b([^\]]*)\](.*?)\[/(?:code|sourcecode|php|js|javascript|css|html|xml|bash|shell|sql|python|ruby|java|cpp|c|go|json|yaml|text|plain)\]`)
	reCaption      = regexp.MustCompile(`(?s)\[(?:caption|wp_caption)\b([^\]]*)\](.*?)\[/(?:caption|wp_caption)\]`)
	reCaptionImg   = regexp.MustCompile(`(?is)^\s*((?:<a [^>]+>\s*)?<img [^>]+>(?:\s*</a>)?)(.*)$`)
	reEmbed        = regexp.MustCompile(`(?s)\[embed\b[^\]]*\](.*?)\[/embed\]`)
	reMediaSC      = regexp.MustCompile(`\[(audio|video)\b([^\]]*)\]`)
	reMediaClose   = regexp.MustCompile(`\[/(?:audio|video)\]`)
	reGallery      = regexp.MustCompile(`\[gallery\b([^\]]*)\]`)
	reShortcode    = regexp.MustCompile(`\[(/?)([a-zA-Z][a-zA-Z0-9_-]+)((?:\s[^\[\]]*)?)\]`)
	reSCAttr       = regexp.MustCompile(`([\w-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'\]]+))`)
	reBareURL      = regexp.MustCompile(`^https?://[^\s<>"]+$`)
	reLang         = regexp.MustCompile(`(?:^|\s)(?:language|lang)-([\w+#-]+)|brush:\s*([\w+#-]+)|lang:([\w+#-]+)`)
	reValidLang    = regexp.MustCompile(`^[a-z0-9_+-]{1,40}$`)
	reListItem     = regexp.MustCompile(`^\s*(?:[-*+]|\d{1,9}[.)])(?:\s|$)`)
	reBlankLines   = regexp.MustCompile(`\n{3,}`)
	reEmptyQuote   = regexp.MustCompile(`^(?:> ?)+ *$`)
	reBlockPrefix  = regexp.MustCompile(`^[ \t]*(?:(?:>|[-*+]|\d{1,9}[.)])[ \t]*)*$`)
	reYouTubeEmbed = regexp.MustCompile(`^/embed/([\w-]{6,20})`)
	reVimeoPlayer  = regexp.MustCompile(`^/video/(\d{1,15})`)
)

var mdConverter = sync.OnceValue(func() *converter.Converter {
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		commonmark.NewCommonmarkPlugin(
			commonmark.WithListEndComment(false),
			commonmark.WithBulletListMarker("-"),
		),
		table.NewTablePlugin(),
		strikethrough.NewStrikethroughPlugin(),
	))
	conv.Register.RendererFor("a", converter.TagTypeInline, renderAutolink, converter.PriorityEarly)
	return conv
})

// renderAutolink writes <https://…> for a link whose text is its own URL,
// instead of the noisier [https://…](https://…).
func renderAutolink(_ converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	href := attr(n, "href")
	if n.FirstChild == nil || n.FirstChild != n.LastChild || n.FirstChild.Type != html.TextNode ||
		strings.TrimSpace(n.FirstChild.Data) != href || !reBareURL.MatchString(href) {
		return converter.RenderTryNext
	}
	w.WriteString("<" + href + ">")
	return converter.RenderSuccess
}

type convState struct {
	opts      ConvertOptions
	base      *url.URL
	nonce     string
	protected []string
	warned    map[string]bool
}

func newNonce() string {
	b := make([]byte, 6)
	rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

func (c *convState) warn(msg string) {
	if c.opts.Warn == nil || c.warned[msg] {
		return
	}
	c.warned[msg] = true
	c.opts.Warn(msg)
}

// ToMarkdown converts WordPress post HTML, classic or Gutenberg, to Markdown.
// When the content has a <!--more--> tag, excerpt is the plain text of the
// teaser before it (WordPress shows the whole content on the single page, so
// md keeps both parts).
func ToMarkdown(src string, opts ConvertOptions) (md string, excerpt string) {
	c := &convState{opts: opts, nonce: newNonce(), warned: map[string]bool{}}
	if opts.BaseURL != "" {
		if u, err := url.Parse(opts.BaseURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			c.base = u
		}
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	if strings.TrimSpace(src) == "" {
		return "", ""
	}

	// Gutenberg content is rendered without wpautop by WordPress itself.
	blocks := strings.Contains(src, "<!-- wp:")
	if blocks {
		src = reBlockComment.ReplaceAllString(src, "")
	}
	marker := "WPIMPORTMORE" + c.nonce + "X"
	more := false
	if loc := reMore.FindStringIndex(src); loc != nil {
		src = src[:loc[0]] + marker + src[loc[1]:]
		more = true
	}
	src = c.shortcodes(src)
	if !blocks {
		src = wpautop(src, true)
	}

	body := parseBody(src)
	c.transform(body)
	if more {
		excerpt, _ = plainText(body, marker)
		excerpt = c.stripShortcodes(excerpt)
	}
	out, err := mdConverter().ConvertNode(body)
	if err != nil {
		c.warn("content could not be converted: " + err.Error())
		return "", excerpt
	}
	if more {
		// The tag stands for a break between teaser and body.
		out = bytes.Replace(out, []byte(marker), []byte("\n\n"), 1)
	}
	return c.tidy(string(out)), excerpt
}

// HTMLToText converts comment or excerpt HTML to plain text: line breaks and
// paragraphs are kept (as "\n" and "\n\n"), tags are dropped and entities
// decoded.
func HTMLToText(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	if strings.TrimSpace(src) == "" {
		return ""
	}
	body := parseBody(wpautop(src, true))
	s, _ := plainText(body, "")
	return s
}

func parseBody(src string) *html.Node {
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), ctx)
	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	if err != nil {
		body.AppendChild(&html.Node{Type: html.TextNode, Data: src})
		return body
	}
	for _, n := range nodes {
		body.AppendChild(n)
	}
	return body
}

// - - - shortcodes - - -

func scAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range reSCAttr.FindAllStringSubmatch(s, -1) {
		out[strings.ToLower(m[1])] = m[2] + m[3] + m[4]
	}
	return out
}

func (c *convState) shortcodes(src string) string {
	if !strings.Contains(src, "[") {
		return src
	}
	// Syntax-highlighter shortcodes become code blocks; their bodies are
	// kept out of the other replacements.
	var codes []string
	src = reCodeSC.ReplaceAllStringFunc(src, func(m string) string {
		sub := reCodeSC.FindStringSubmatch(m)
		a := scAttrs(sub[2])
		lang := a["lang"] + a["language"]
		if sub[1] != "code" && sub[1] != "sourcecode" {
			lang = sub[1]
		}
		body := strings.Trim(sub[3], "\n")
		body = html.EscapeString(html.UnescapeString(body))
		class := ""
		if l := cleanLang(lang); l != "" {
			class = ` class="language-` + l + `"`
		}
		codes = append(codes, "<pre><code"+class+">"+body+"</code></pre>")
		return "\n\n<pre wpimport-code-" + c.nonce + "-" + strconv.Itoa(len(codes)-1) + "></pre>\n\n"
	})

	src = reCaption.ReplaceAllStringFunc(src, func(m string) string {
		sub := reCaption.FindStringSubmatch(m)
		a := scAttrs(sub[1])
		body, caption := sub[2], a["caption"]
		if caption == "" {
			if p := reCaptionImg.FindStringSubmatch(body); p != nil {
				body, caption = p[1], strings.TrimSpace(p[2])
			}
		}
		// The alignment travels on a wrapper, where img finds it.
		open := "<figure>"
		if m := reWPAlign.FindStringSubmatch(a["align"]); m != nil {
			open = `<figure class="align` + m[1] + `">`
		}
		if strings.TrimSpace(caption) == "" {
			return open + strings.TrimSpace(body) + "</figure>"
		}
		return "\n\n" + open + strings.TrimSpace(body) + "<figcaption>" + caption + "</figcaption></figure>\n\n"
	})

	src = reEmbed.ReplaceAllStringFunc(src, func(m string) string {
		u := strings.TrimSpace(html.UnescapeString(reEmbed.FindStringSubmatch(m)[1]))
		if !reBareURL.MatchString(u) {
			return ""
		}
		e := html.EscapeString(u)
		return `<a href="` + e + `">` + e + `</a>`
	})

	src = reMediaSC.ReplaceAllStringFunc(src, func(m string) string {
		a := scAttrs(reMediaSC.FindStringSubmatch(m)[2])
		for _, k := range []string{"src", "mp3", "m4a", "ogg", "wav", "mp4", "m4v", "webm", "ogv", "wmv", "flv"} {
			if v := strings.TrimSpace(a[k]); v != "" {
				e := html.EscapeString(v)
				return `<a href="` + e + `">` + html.EscapeString(fileLabel(v)) + `</a>`
			}
		}
		return ""
	})
	src = reMediaClose.ReplaceAllString(src, "")

	src = reGallery.ReplaceAllStringFunc(src, func(m string) string {
		if c.opts.Gallery == nil {
			return ""
		}
		a := scAttrs(reGallery.FindStringSubmatch(m)[1])
		var ids []int64
		for _, s := range strings.Split(a["ids"]+","+a["include"], ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil && id > 0 {
				ids = append(ids, id)
			}
		}
		var b strings.Builder
		b.WriteString("\n\n<div>")
		for _, img := range c.opts.Gallery(ids) {
			fmt.Fprintf(&b, `<p><img src="%s" alt="%s"></p>`, html.EscapeString(img.URL), html.EscapeString(img.Alt))
			if img.Caption != "" {
				b.WriteString("<p><em>" + html.EscapeString(img.Caption) + "</em></p>")
			}
		}
		b.WriteString("</div>\n\n")
		return b.String()
	})

	// Any other shortcode is kept verbatim (a sap13ns plugin may implement
	// it): protect it from Markdown escaping and restore it in tidy.
	closing := map[string]bool{}
	for _, m := range reShortcode.FindAllStringSubmatch(src, -1) {
		if m[1] == "/" {
			closing[strings.ToLower(m[2])] = true
		}
	}
	var unknown []string
	reported := map[string]bool{}
	src = reShortcode.ReplaceAllStringFunc(src, func(m string) string {
		sub := reShortcode.FindStringSubmatch(m)
		name := strings.ToLower(sub[2])
		if sub[1] == "" && (strings.Contains(sub[3], "=") || closing[name]) && !reported[name] {
			reported[name] = true
			unknown = append(unknown, name)
		}
		c.protected = append(c.protected, m)
		return c.token(len(c.protected) - 1)
	})
	if len(unknown) > 0 {
		if len(unknown) > 10 {
			unknown = append(unknown[:10], "…")
		}
		c.warn("shortcodes kept as text: [" + strings.Join(unknown, "], [") + "]")
	}

	if len(codes) > 0 {
		re := regexp.MustCompile(`<pre wpimport-code-` + c.nonce + `-(\d+)></pre>`)
		src = re.ReplaceAllStringFunc(src, func(m string) string {
			if i, err := strconv.Atoi(re.FindStringSubmatch(m)[1]); err == nil && i < len(codes) {
				return codes[i]
			}
			return m
		})
	}
	return src
}

func (c *convState) token(i int) string {
	return "WPIMPORTSC" + c.nonce + "N" + strconv.Itoa(i) + "E"
}

func (c *convState) tokenRegexp() *regexp.Regexp {
	return regexp.MustCompile(`WPIMPORTSC` + c.nonce + `N(\d+)E`)
}

// stripShortcodes removes kept shortcodes from plain text, as WordPress
// does for excerpts.
func (c *convState) stripShortcodes(s string) string {
	if len(c.protected) == 0 {
		return s
	}
	var paras []string
	for _, p := range strings.Split(c.tokenRegexp().ReplaceAllString(s, ""), "\n\n") {
		var lines []string
		for _, l := range strings.Split(p, "\n") {
			if l = strings.Join(strings.Fields(l), " "); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			paras = append(paras, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(paras, "\n\n")
}

func fileLabel(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		if b := path.Base(u.Path); b != "." && b != "/" {
			if dec, err := url.PathUnescape(b); err == nil {
				return dec
			}
			return b
		}
	}
	return raw
}

func cleanLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "c#":
		return "csharp"
	case "f#":
		return "fsharp"
	case "plain", "text", "none", "":
		return ""
	}
	if !reValidLang.MatchString(s) {
		return ""
	}
	return s
}

// - - - DOM clean-up - - -

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttrs(n *html.Node, kv ...string) {
	n.Attr = n.Attr[:0]
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
		}
	}
}

func elem(tag atom.Atom, kv ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: tag.String(), DataAtom: tag}
	setAttrs(n, kv...)
	return n
}

func textNode(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }

func remove(n *html.Node) {
	if n.Parent != nil {
		n.Parent.RemoveChild(n)
	}
}

func replace(old, repl *html.Node) {
	if old.Parent != nil {
		old.Parent.InsertBefore(repl, old)
		old.Parent.RemoveChild(old)
	}
}

// unwrap replaces n with its children.
func unwrap(n *html.Node) {
	for n.FirstChild != nil {
		ch := n.FirstChild
		n.RemoveChild(ch)
		n.Parent.InsertBefore(ch, n)
	}
	remove(n)
}

func (c *convState) transform(n *html.Node) {
	for ch := n.FirstChild; ch != nil; {
		next := ch.NextSibling
		c.node(ch)
		ch = next
	}
}

var dropped = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "object": true, "embed": true,
	"applet": true, "canvas": true, "svg": true, "math": true, "link": true, "meta": true, "base": true,
	"button": true, "input": true, "select": true, "textarea": true, "frameset": true, "frame": true,
	"head": true, "title": true, "dialog": true, "portal": true,
}

func (c *convState) node(n *html.Node) {
	switch n.Type {
	case html.CommentNode:
		remove(n)
		return
	case html.TextNode:
		c.text(n)
		return
	case html.ElementNode:
	default:
		remove(n)
		return
	}
	name := strings.ToLower(n.Data)
	switch {
	case dropped[name]:
		remove(n)
	case name == "form":
		c.warn("a form was removed")
		remove(n)
	case name == "iframe":
		c.iframe(n)
	case name == "video" || name == "audio":
		c.media(n)
	case name == "img":
		c.img(n)
	case name == "a":
		c.transform(n)
		c.link(n)
	case name == "pre":
		c.pre(n)
	case name == "figcaption":
		c.transform(n)
		if isBlank(n) {
			remove(n)
			return
		}
		p, em := elem(atom.P), elem(atom.Em)
		p.AppendChild(em)
		for n.FirstChild != nil {
			ch := n.FirstChild
			n.RemoveChild(ch)
			em.AppendChild(ch)
		}
		replace(n, p)
	case name == "p":
		c.transform(n)
		if isBlank(n) {
			remove(n)
		}
	default:
		c.transform(n)
	}
}

// isBlank reports an element without images and with only whitespace (or
// non-breaking spaces) as text.
func isBlank(n *html.Node) bool {
	blank := true
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil && blank; ch = ch.NextSibling {
			switch ch.Type {
			case html.TextNode:
				if strings.TrimSpace(strings.ReplaceAll(ch.Data, "\u00a0", " ")) != "" {
					blank = false
				}
			case html.ElementNode:
				if ch.DataAtom == atom.Img || ch.DataAtom == atom.Hr {
					blank = false
				}
				walk(ch)
			}
		}
	}
	walk(n)
	return blank
}

// resolve returns the absolute form of a link, or "" for schemes that must
// not survive (javascript:, data:, vbscript:…).
func (c *convState) resolve(raw string, allowMail bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String()
	case "mailto", "tel":
		if allowMail {
			return u.String()
		}
		return ""
	case "":
		if strings.HasPrefix(raw, "#") {
			return raw
		}
		if c.base != nil {
			return c.base.ResolveReference(u).String()
		}
		return u.String()
	}
	return ""
}

func (c *convState) rewrite(u string) string {
	if c.opts.RewriteURL == nil || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
		return u
	}
	if r := c.opts.RewriteURL(u); r != "" {
		return r
	}
	return u
}

func (c *convState) img(n *html.Node) {
	src := attr(n, "src")
	if src == "" || strings.HasPrefix(src, "data:") {
		// Lazy-loading plugins keep the real URL aside.
		for _, k := range []string{"data-lazy-src", "data-src", "data-orig-file"} {
			if v := attr(n, k); v != "" {
				src = v
				break
			}
		}
	}
	u := c.resolve(src, false)
	if u == "" {
		remove(n)
		return
	}
	layout := imageLayout(n)
	setAttrs(n, "src", c.rewrite(u), "alt", strings.TrimSpace(attr(n, "alt")), "title", strings.TrimSpace(attr(n, "title")))
	if layout != "" && n.Parent != nil {
		// ![alt](src){width=… align=…}: kept out of Markdown escaping like
		// a shortcode and put back in tidy.
		c.protected = append(c.protected, layout)
		n.Parent.InsertBefore(textNode(c.token(len(c.protected)-1)), n.NextSibling)
	}
}

var (
	reWPAlign = regexp.MustCompile(`(?:^|\s)align(left|right|center|none|wide|full)(?:\s|$)`)
	reWPSize  = regexp.MustCompile(`(?:^|\s)size-full(?:\s|$)`)
)

// imageLayout reads WordPress's alignment classes (alignleft, alignright,
// aligncenter, alignwide, alignfull) from the image or the figure, link or
// block around it, and its width, as {width=300 align=left}. A full-size
// image (size-full) keeps its natural width.
func imageLayout(img *html.Node) string {
	classes := attr(img, "class")
	n := img.Parent
	for depth := 0; n != nil && depth < 3; depth, n = depth+1, n.Parent {
		if n.Type != html.ElementNode || (n.DataAtom != atom.Figure && n.DataAtom != atom.Div && n.DataAtom != atom.A && n.DataAtom != atom.Span) {
			break
		}
		classes += " " + attr(n, "class")
	}
	align := ""
	if m := reWPAlign.FindStringSubmatch(classes); m != nil {
		switch m[1] {
		case "left", "right", "center":
			align = m[1]
		case "wide", "full":
			align = "full"
		}
	}
	width := 0
	if w, err := strconv.Atoi(strings.TrimSpace(attr(img, "width"))); err == nil && w > 0 && w <= 4096 && // content.MaxImageWidth
		!reWPSize.MatchString(classes) && align != "full" {
		width = w
	}
	var parts []string
	if width > 0 {
		parts = append(parts, "width="+strconv.Itoa(width))
	}
	if align != "" {
		parts = append(parts, "align="+align)
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func (c *convState) link(n *html.Node) {
	href := attr(n, "href")
	u := c.resolve(href, true)
	if u == "" {
		unwrap(n)
		return
	}
	u = c.rewrite(u)
	setAttrs(n, "href", u, "title", strings.TrimSpace(attr(n, "title")))
	// An image linked to its own file ("Link to: Media File") reads better
	// as the bare image.
	var img *html.Node
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		switch {
		case ch.Type == html.ElementNode && ch.DataAtom == atom.Img && img == nil:
			img = ch
		case ch.Type == html.TextNode && strings.TrimSpace(ch.Data) == "":
		case ch.Type == html.TextNode && img != nil && ch.PrevSibling == img && strings.HasPrefix(ch.Data, "WPIMPORTSC"+c.nonce):
			// The image's {width=… align=…} (img).
		default:
			return
		}
	}
	if img != nil && attr(img, "src") == u {
		unwrap(n)
	}
}

func (c *convState) iframe(n *html.Node) {
	u := c.resolve(attr(n, "src"), false)
	if !strings.HasPrefix(u, "http") {
		remove(n)
		return
	}
	if pu, err := url.Parse(u); err == nil {
		host := strings.TrimPrefix(strings.ToLower(pu.Hostname()), "www.")
		switch {
		case host == "youtube.com" || host == "youtube-nocookie.com":
			if m := reYouTubeEmbed.FindStringSubmatch(pu.Path); m != nil {
				u = "https://www.youtube.com/watch?v=" + m[1]
			}
		case host == "player.vimeo.com":
			if m := reVimeoPlayer.FindStringSubmatch(pu.Path); m != nil {
				u = "https://vimeo.com/" + m[1]
			}
		}
	}
	// The URL itself is the label: iframe titles are mostly generic
	// ("YouTube video player").
	u = c.rewrite(u)
	a := elem(atom.A, "href", u)
	a.AppendChild(textNode(u))
	replace(n, a)
}

func (c *convState) media(n *html.Node) {
	src := attr(n, "src")
	for ch := n.FirstChild; ch != nil && src == ""; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && ch.DataAtom == atom.Source {
			src = attr(ch, "src")
		}
	}
	u := c.resolve(src, false)
	if u == "" {
		remove(n)
		return
	}
	a := elem(atom.A, "href", c.rewrite(u))
	a.AppendChild(textNode(fileLabel(u)))
	replace(n, a)
}

// pre normalises code blocks to <pre><code class="language-x">, taking the
// language from the classes used by Gutenberg and the common highlighter
// plugins (language-x, lang-x, "brush: x", lang:x).
func (c *convState) pre(n *html.Node) {
	lang := ""
	var find func(*html.Node)
	find = func(n *html.Node) {
		if lang != "" || n.Type != html.ElementNode {
			return
		}
		for _, k := range []string{"class", "data-lang", "lang"} {
			v := attr(n, k)
			if k == "class" {
				if m := reLang.FindStringSubmatch(v); m != nil {
					v = m[1] + m[2] + m[3]
				} else {
					v = ""
				}
			}
			if l := cleanLang(v); l != "" {
				lang = l
				return
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			find(ch)
		}
	}
	find(n)
	// Collect the code text: <br> and block boundaries are line breaks.
	var b strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			switch ch.Type {
			case html.TextNode:
				b.WriteString(ch.Data)
			case html.ElementNode:
				switch {
				case ch.DataAtom == atom.Br:
					b.WriteByte('\n')
				case dropped[ch.Data]:
				default:
					collect(ch)
					if ch.DataAtom == atom.Div || ch.DataAtom == atom.P {
						b.WriteByte('\n')
					}
				}
			}
		}
	}
	collect(n)
	code := strings.Trim(b.String(), "\n")
	pre, codeEl := elem(atom.Pre), elem(atom.Code)
	if lang != "" {
		setAttrs(codeEl, "class", "language-"+lang)
	}
	codeEl.AppendChild(textNode(code))
	pre.AppendChild(codeEl)
	replace(n, pre)
}

// text turns a bare URL standing on its own line into a link, as
// WordPress's autoembed does.
func (c *convState) text(n *html.Node) {
	s := strings.TrimSpace(n.Data)
	if !reBareURL.MatchString(s) || n.Parent == nil {
		return
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && (p.DataAtom == atom.A || p.DataAtom == atom.Pre || p.DataAtom == atom.Code) {
			return
		}
	}
	if !lineBoundary(n.PrevSibling) || !lineBoundary(n.NextSibling) {
		return
	}
	u := c.rewrite(s)
	a := elem(atom.A, "href", u)
	a.AppendChild(textNode(u))
	replace(n, a)
}

func lineBoundary(n *html.Node) bool {
	if n == nil {
		return true
	}
	if n.Type == html.ElementNode {
		return n.DataAtom == atom.Br || isBlockAtom(n.DataAtom)
	}
	return n.Type == html.CommentNode
}

func isBlockAtom(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Figure, atom.Blockquote, atom.Ul, atom.Ol, atom.Li, atom.Table, atom.Pre,
		atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6, atom.Hr, atom.Section, atom.Article,
		atom.Header, atom.Footer, atom.Aside, atom.Nav, atom.Dl, atom.Dd, atom.Dt, atom.Figcaption:
		return true
	}
	return false
}

// plainText extracts the text of n in document order, stopping before stop
// when it is not empty. Block elements become paragraphs, <br> a line break.
func plainText(n *html.Node, stop string) (string, bool) {
	const paraMark, brMark = "\x01", "\x02"
	var b strings.Builder
	found := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for ch := n.FirstChild; ch != nil && !found; ch = ch.NextSibling {
			switch ch.Type {
			case html.TextNode:
				t := ch.Data
				if stop != "" {
					if i := strings.Index(t, stop); i >= 0 {
						t, found = t[:i], true
					}
				}
				b.WriteString(t)
			case html.ElementNode:
				switch {
				case dropped[ch.Data]:
				case ch.DataAtom == atom.Br:
					b.WriteString(brMark)
				case isBlockAtom(ch.DataAtom):
					b.WriteString(paraMark)
					walk(ch)
					b.WriteString(paraMark)
				default:
					walk(ch)
				}
			}
		}
	}
	walk(n)
	var paras []string
	for _, para := range strings.Split(b.String(), paraMark) {
		var lines []string
		for _, line := range strings.Split(para, brMark) {
			if l := strings.Join(strings.Fields(strings.ReplaceAll(line, "\u00a0", " ")), " "); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			paras = append(paras, strings.Join(lines, "\n"))
		}
	}
	return strings.Join(paras, "\n\n"), found
}

// - - - Markdown clean-up - - -

// tidy restores protected shortcodes, removes whitespace-only lines and
// strips indentation that Markdown would read as a code block.
func (c *convState) tidy(md string) string {
	if len(c.protected) > 0 {
		re := c.tokenRegexp()
		md = re.ReplaceAllStringFunc(md, func(m string) string {
			if i, err := strconv.Atoi(re.FindStringSubmatch(m)[1]); err == nil && i < len(c.protected) {
				return c.protected[i]
			}
			return m
		})
	}
	lines := strings.Split(md, "\n")
	code := make([]bool, len(lines)) // inside a fenced block, fences included
	fence := ""
	inList := false
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if fence != "" {
			code[i] = true
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			code[i] = true
			fence = trimmed[:3]
			for strings.HasPrefix(trimmed[len(fence):], fence[:1]) {
				fence += fence[:1]
			}
			continue
		}
		if strings.TrimSpace(strings.ReplaceAll(line, "\u00a0", " ")) == "" {
			lines[i] = ""
			continue
		}
		if reEmptyQuote.MatchString(line) {
			lines[i] = strings.ReplaceAll(strings.TrimRight(line, " "), "> ", ">")
		}
		if strings.Contains(line, "&lt;") || strings.Contains(line, "&gt;") {
			lines[i] = unescapeAngles(lines[i])
			line = lines[i]
		}
		switch {
		case reListItem.MatchString(line):
			inList = true
		case indentWidth(line) >= 4 && !inList:
			lines[i] = trimmed
		case indentWidth(line) == 0:
			inList = false
		}
	}
	// A hard line break before a blank line or the end means nothing.
	for i, line := range lines {
		if !code[i] && (i+1 == len(lines) || lines[i+1] == "") {
			lines[i] = strings.TrimRight(line, " ")
		}
	}
	// A blank line between an item and its nested list makes the whole
	// list loose (every item wrapped in <p>); drop it.
	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if line == "" && !code[i] && i > 0 && i+1 < len(lines) && !code[i-1] && !code[i+1] && reListItem.MatchString(lines[i-1]) &&
			reListItem.MatchString(lines[i+1]) && indentWidth(lines[i+1]) > indentWidth(lines[i-1]) {
			continue
		}
		out = append(out, line)
	}
	md = strings.Join(out, "\n")
	return strings.TrimSpace(reBlankLines.ReplaceAllString(md, "\n\n"))
}

// unescapeAngles turns the &lt; and &gt; that html-to-markdown writes for
// every < and > back into plain characters where Markdown cannot read them
// as HTML, an autolink or a blockquote ("a < b", "x -> y"). Code spans are
// left alone: their content is literal.
func unescapeAngles(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); {
		switch {
		case line[i] == '`':
			n := 1
			for i+n < len(line) && line[i+n] == '`' {
				n++
			}
			fence := line[i : i+n]
			end := -1
			for j := i + n; j < len(line); {
				k := strings.Index(line[j:], fence)
				if k < 0 {
					break
				}
				k += j
				m := k + n
				if (k == 0 || line[k-1] != '`') && (m >= len(line) || line[m] != '`') {
					end = m
					break
				}
				for m < len(line) && line[m] == '`' {
					m++
				}
				j = m
			}
			if end < 0 {
				end = i + n
			}
			b.WriteString(line[i:end])
			i = end
		case strings.HasPrefix(line[i:], "&lt;"):
			next := byte(' ')
			if i+4 < len(line) {
				next = line[i+4]
			}
			if isASCIILetter(next) || next == '/' || next == '!' || next == '?' {
				b.WriteString("&lt;")
			} else {
				b.WriteByte('<')
			}
			i += 4
		case strings.HasPrefix(line[i:], "&gt;"):
			// At the start of a block (after quote or list markers) ">"
			// would open a blockquote.
			if b.Len() < 64 && reBlockPrefix.MatchString(b.String()) {
				b.WriteString("&gt;")
			} else {
				b.WriteByte('>')
			}
			i += 4
		default:
			b.WriteByte(line[i])
			i++
		}
	}
	return b.String()
}

func isASCIILetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func indentWidth(s string) int {
	w := 0
	for _, r := range s {
		switch r {
		case ' ':
			w++
		case '\t':
			w += 4 - w%4
		default:
			return w
		}
	}
	return w
}
