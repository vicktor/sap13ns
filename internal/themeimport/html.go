package themeimport

import (
	"crypto/rand"
	"encoding/hex"
	"html"
	"regexp"
	"slices"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// The HTML of static blocks. A static block saved its HTML between its
// delimiters, and that HTML is what it shows; the importer keeps it, but
// only through this allowlist: the elements of rich text and layout, the
// attributes of links, images and accessibility, and classes. Everything
// else is left out (scripts, styles, frames, forms, media players, event
// handlers, data-* attributes), style="" becomes a generated class, and
// every "{" of the theme's text is written as &#123;, so that no text of
// the theme can open a template action.

// Marks are private-use runes the importer writes into a theme's text:
// the sentinels of the PHP evaluator (php.go), bound to this conversion
// by a random nonce, and the places of inner blocks. The theme's own text
// is stripped of these runes when it is read (stripSentinels), and
// whatever an entity such as &#xE0A0; decodes to lacks the nonce, so
// only the importer's marks are read as marks.
const (
	holeOpen  = ""
	holeClose = ""
)

type marks struct{ nonce string }

func newMarks() marks {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return marks{nonce: hex.EncodeToString(b)}
}

func (m marks) sentinel(key string) string { return sentinelOpen + key + ":" + m.nonce + sentinelClose }
func (m marks) hole(i int) string          { return holeOpen + strconv.Itoa(i) + ":" + m.nonce + holeClose }

// bind turns the evaluator's sentinels into this conversion's. s is text
// that went through keepSentinels, where every sentinel is the
// evaluator's.
func (m marks) bind(s string) string {
	if !strings.Contains(s, sentinelOpen) {
		return s
	}
	for _, k := range sentinelKeys {
		s = strings.ReplaceAll(s, sentinel(k), m.sentinel(k))
	}
	return s
}

// markRe finds this conversion's marks in decoded text.
func (m marks) re() *regexp.Regexp {
	return regexp.MustCompile("(" + sentinelOpen + "(theme|year|title|home):" + m.nonce + sentinelClose + ")|(" +
		holeOpen + "([0-9]{1,6}):" + m.nonce + holeClose + ")")
}

// stripMarks removes every mark rune.
func stripMarks(s string) string {
	if !strings.ContainsAny(s, sentinelOpen+sentinelClose+holeOpen+holeClose) {
		return s
	}
	return strings.NewReplacer(sentinelOpen, "", sentinelClose, "", holeOpen, "", holeClose, "").Replace(s)
}

// escapeText escapes text of the theme for a template: HTML-escaped, and
// "{" as &#123;, so that no "{{" reaches the template parser.
func escapeText(s string) string {
	return strings.ReplaceAll(html.EscapeString(stripMarks(s)), "{", "&#123;")
}

// safeURL: a path of the site, an anchor, or an http(s), mailto or tel
// URL. No other scheme, no backslash, no control character or space.
func safeURL(u string) bool {
	if u == "" || len(u) > 2000 || strings.ContainsFunc(u, func(r rune) bool { return r <= 0x20 || r == 0x7f || r == '\\' }) {
		return false
	}
	if strings.HasPrefix(u, "//") {
		return false
	}
	if u[0] == '/' || u[0] == '#' || u[0] == '?' {
		return true
	}
	scheme, _, ok := strings.Cut(u, ":")
	if !ok || strings.ContainsAny(scheme, "/?#") {
		return !strings.Contains(u, ":") // a relative path
	}
	switch strings.ToLower(scheme) {
	case "http", "https", "mailto", "tel":
		return true
	}
	return false
}

// keepElements are the elements a static block keeps.
var keepElements = map[string]bool{
	"a": true, "abbr": true, "address": true, "article": true, "aside": true, "b": true, "bdi": true, "bdo": true,
	"blockquote": true, "br": true, "caption": true, "cite": true, "code": true, "col": true, "colgroup": true,
	"data": true, "dd": true, "del": true, "details": true, "dfn": true, "div": true, "dl": true, "dt": true,
	"em": true, "figcaption": true, "figure": true, "footer": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "header": true, "hgroup": true, "hr": true, "i": true, "img": true, "ins": true,
	"kbd": true, "li": true, "main": true, "mark": true, "nav": true, "ol": true, "p": true, "pre": true, "q": true,
	"rp": true, "rt": true, "ruby": true, "s": true, "samp": true, "section": true, "small": true, "span": true,
	"strong": true, "sub": true, "summary": true, "sup": true, "table": true, "tbody": true, "td": true,
	"tfoot": true, "th": true, "thead": true, "time": true, "tr": true, "u": true, "ul": true, "var": true, "wbr": true,
}

// dropElements are left out with their content.
var dropElements = map[string]bool{
	"script": true, "style": true, "iframe": true, "frame": true, "frameset": true, "object": true, "embed": true,
	"applet": true, "noscript": true, "template": true, "textarea": true, "select": true, "option": true,
	"optgroup": true, "form": true, "input": true, "button": true, "svg": true, "math": true, "video": true,
	"audio": true, "canvas": true, "source": true, "track": true, "map": true, "area": true, "base": true,
	"link": true, "meta": true, "title": true, "head": true, "dialog": true, "portal": true, "slot": true,
	"picture": true, "xmp": true, "plaintext": true, "listing": true, "noembed": true, "noframes": true,
	"fencedframe": true, "search": true, "label": true, "fieldset": true, "legend": true, "output": true,
	"datalist": true, "meter": true, "progress": true, "marquee": true,
}

var voidElements = map[string]bool{"br": true, "hr": true, "img": true, "wbr": true, "col": true}

// droppedVoid are the void elements of dropElements: they have no
// content to skip, and no end tag would end the skipping.
var droppedVoid = map[string]bool{"input": true, "source": true, "track": true, "area": true, "base": true,
	"link": true, "meta": true, "embed": true, "param": true, "keygen": true, "frame": true}

var (
	classRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,79}$`)
	idAttrRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,59}$`)
	digitsRe = regexp.MustCompile(`^[0-9]{1,5}$`)
)

var relTokens = map[string]bool{
	"nofollow": true, "noopener": true, "noreferrer": true, "external": true, "tag": true, "me": true,
	"prev": true, "next": true, "author": true, "license": true, "bookmark": true, "help": true, "sponsored": true, "ugc": true,
}

// reservedClass: classes of the server's own markup a theme's HTML
// cannot wear.
func reservedClass(c string) bool {
	l := strings.ToLower(c)
	return strings.HasPrefix(l, "sap-") || strings.Contains(l, "footer-meta") || l == "skip-link" || l == "sr-only-focusable"
}

// fragment is one static block's HTML being rewritten.
type fragment struct {
	e     *emitter
	re    *regexp.Regexp
	holes []string // the inner blocks, already templates
	// first: classes added to the block's first element.
	first      []string
	extra      map[string][]string // class on an element -> classes to add
	seenFirst  bool
	out        strings.Builder
	open       []string // elements written and not closed yet
	skip       string   // the dropped element being skipped
	skipDepth  int
	images     int // images kept
	lostImages int // images left out: no file of the theme
}

// rewriteHTML rewrites a static block's HTML, with the inner blocks'
// templates at their holes.
func (e *emitter) rewriteHTML(src string, holes []string, first []string, extra map[string][]string) (string, *fragment) {
	f := &fragment{e: e, re: e.c.markRe, holes: holes, first: first, extra: extra}
	z := xhtml.NewTokenizer(strings.NewReader(src))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		switch tt {
		case xhtml.TextToken:
			if f.skip != "" {
				continue
			}
			f.text(string(z.Text()))
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			tok := z.Token()
			f.start(tok, tt == xhtml.SelfClosingTagToken)
		case xhtml.EndTagToken:
			tok := z.Token()
			f.end(tok.Data)
		}
	}
	for i := len(f.open) - 1; i >= 0; i-- {
		f.out.WriteString("</" + f.open[i] + ">")
	}
	return f.out.String(), f
}

// text writes decoded text: the marks become template actions or inner
// blocks, the rest is escaped.
func (f *fragment) text(s string) {
	f.out.WriteString(f.e.markText(s, f.re, f.holes))
}

// markText escapes decoded text, turning this conversion's marks into
// actions; holes is nil outside a block's own HTML.
func (e *emitter) markText(s string, re *regexp.Regexp, holes []string) string {
	var b strings.Builder
	last := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(escapeText(s[last:m[0]]))
		last = m[1]
		switch {
		case m[4] >= 0: // a sentinel
			switch s[m[4]:m[5]] {
			case "year":
				b.WriteString("{{$.Site.Year}}")
			case "title":
				b.WriteString("{{$.Site.Title}}")
			case "home":
				b.WriteString(`{{link "/"}}`)
			}
		case m[8] >= 0: // a hole
			i, _ := strconv.Atoi(s[m[8]:m[9]])
			if i < len(holes) {
				b.WriteString(holes[i])
			}
		}
	}
	b.WriteString(escapeText(s[last:]))
	return b.String()
}

func (f *fragment) start(tok xhtml.Token, selfClosing bool) {
	name := tok.Data
	if f.skip != "" {
		if name == f.skip && !selfClosing && !voidElements[name] {
			f.skipDepth++
		}
		return
	}
	if dropElements[name] {
		if !selfClosing && !voidElements[name] && !droppedVoid[name] {
			f.skip, f.skipDepth = name, 1
		}
		f.e.note("html.dropped", name)
		return
	}
	if !keepElements[name] {
		return // unknown element: its content stays
	}
	tag := name
	if tag == "main" {
		tag = "div" // the layout has the page's <main>
	}
	isFirst := !f.seenFirst
	f.seenFirst = true
	attrs, ok := f.attrs(name, tok.Attr, isFirst)
	if !ok {
		return
	}
	f.out.WriteString("<" + tag + attrs + ">")
	if !voidElements[tag] {
		if selfClosing {
			f.out.WriteString("</" + tag + ">")
		} else {
			f.open = append(f.open, tag)
		}
	}
}

func (f *fragment) end(name string) {
	if f.skip != "" {
		if name == f.skip {
			if f.skipDepth--; f.skipDepth == 0 {
				f.skip = ""
			}
		}
		return
	}
	if name == "main" {
		name = "div"
	}
	// Close up to the matching open element; a stray end tag is dropped.
	for i := len(f.open) - 1; i >= 0; i-- {
		if f.open[i] == name {
			for j := len(f.open) - 1; j >= i; j-- {
				f.out.WriteString("</" + f.open[j] + ">")
			}
			f.open = f.open[:i]
			return
		}
	}
}

// attrs writes the kept attributes of an element; ok is false when the
// element itself is left out (an image without a file).
func (f *fragment) attrs(tag string, in []xhtml.Attribute, first bool) (string, bool) {
	var classes []string
	var out []string
	var rel []string
	blank := false
	for _, a := range in {
		key := strings.ToLower(a.Key)
		if a.Namespace != "" {
			continue
		}
		val := a.Val
		switch {
		case key == "class":
			for _, c := range strings.Fields(stripMarks(val)) {
				if classRe.MatchString(c) && !reservedClass(c) && !slices.Contains(classes, c) {
					classes = append(classes, c)
				}
			}
		case key == "style":
			if c := f.e.styleClass(stripMarks(val)); c != "" {
				classes = append(classes, c)
			}
		case key == "id":
			v := stripMarks(val)
			if idAttrRe.MatchString(v) && v != "main" && !strings.HasPrefix(strings.ToLower(v), "sap-") && v != "primary-menu" {
				out = append(out, `id="`+v+`"`)
			}
		case key == "href" && tag == "a":
			if u := f.e.linkURL(val, f.re); u != "" {
				out = append(out, `href="`+u+`"`)
			}
		case key == "src" && tag == "img":
			u, ok := f.e.imageSrc(val, f.re)
			if !ok {
				f.lostImages++
				f.e.note("html.image-dropped", truncateText(stripMarks(val), 80))
				return "", false
			}
			f.images++
			out = append(out, `src="`+u+`"`)
		case key == "target" && tag == "a":
			if strings.EqualFold(val, "_blank") {
				blank = true
			}
		case key == "rel" && tag == "a":
			for _, t := range strings.Fields(strings.ToLower(val)) {
				if relTokens[t] && !slices.Contains(rel, t) {
					rel = append(rel, t)
				}
			}
		case key == "width" || key == "height":
			if (tag == "img" || tag == "col" || tag == "td" || tag == "th") && digitsRe.MatchString(val) {
				out = append(out, key+`="`+val+`"`)
			}
		case key == "colspan" || key == "rowspan" || key == "start" || key == "value" || key == "span":
			if digitsRe.MatchString(val) {
				out = append(out, key+`="`+val+`"`)
			}
		case key == "loading":
			if val == "lazy" || val == "eager" {
				out = append(out, key+`="`+val+`"`)
			}
		case key == "decoding":
			if val == "async" || val == "sync" || val == "auto" {
				out = append(out, key+`="`+val+`"`)
			}
		case key == "reversed" || key == "open":
			out = append(out, key)
		case key == "type" && (tag == "ol" || tag == "li"):
			if slices.Contains([]string{"1", "a", "A", "i", "I"}, val) {
				out = append(out, `type="`+val+`"`)
			}
		case key == "scope":
			if slices.Contains([]string{"row", "col", "rowgroup", "colgroup"}, val) {
				out = append(out, `scope="`+val+`"`)
			}
		case key == "dir":
			if slices.Contains([]string{"ltr", "rtl", "auto"}, val) {
				out = append(out, `dir="`+val+`"`)
			}
		case key == "role", key == "lang", key == "datetime", key == "headers", key == "abbr",
			key == "alt", key == "title", strings.HasPrefix(key, "aria-") && len(key) < 30 && classRe.MatchString(key):
			out = append(out, key+`="`+f.e.markText(val, f.re, nil)+`"`)
		}
	}
	if tag == "a" && blank {
		out = append(out, `target="_blank"`)
		for _, t := range []string{"noopener", "noreferrer"} {
			if !slices.Contains(rel, t) {
				rel = append(rel, t)
			}
		}
	}
	if len(rel) > 0 {
		out = append(out, `rel="`+strings.Join(rel, " ")+`"`)
	}
	if first {
		for _, c := range f.first {
			if c != "" && !slices.Contains(classes, c) {
				classes = append(classes, c)
			}
		}
	}
	for _, c := range slices.Clone(classes) {
		for _, x := range f.extra[c] {
			if x != "" && !slices.Contains(classes, x) {
				classes = append(classes, x)
			}
		}
	}
	if tag == "img" && !slices.ContainsFunc(out, func(s string) bool { return strings.HasPrefix(s, "src=") }) {
		f.lostImages++
		return "", false
	}
	var b strings.Builder
	if len(classes) > 0 {
		b.WriteString(` class="` + strings.Join(classes, " ") + `"`)
	}
	for _, a := range out {
		b.WriteString(" " + a)
	}
	return b.String(), true
}

// linkURL rewrites a link of the theme: the site's home, a path, an
// anchor or a safe URL; "" when it is none of these.
func (e *emitter) linkURL(v string, re *regexp.Regexp) string {
	v = strings.TrimSpace(v)
	if m := re.FindStringSubmatchIndex(v); m != nil && m[4] >= 0 {
		if m[0] == 0 && v[m[4]:m[5]] == "home" {
			rest := stripMarks(v[m[1]:])
			if rest == "" || rest == "/" {
				return `{{link "/"}}`
			}
			if safeURL(rest) && strings.HasPrefix(rest, "/") {
				return `{{link "` + escapeAttrPath(rest) + `"}}`
			}
		}
		return ""
	}
	v = stripMarks(v)
	if !safeURL(v) {
		return ""
	}
	return escapeText(v)
}

// escapeAttrPath keeps a site path for a {{link "…"}} action: the
// characters of a URL path only.
func escapeAttrPath(p string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-_.~#?=&%", r) {
			return r
		}
		return -1
	}, p)
}

// imageSrc maps an image of the theme to its bundled file. A theme
// names its images with the theme's URL (PHP: get_template_directory_uri)
// or a path into the theme; an image elsewhere is not the theme's to ship.
func (e *emitter) imageSrc(v string, re *regexp.Regexp) (string, bool) {
	v = strings.TrimSpace(v)
	file := ""
	if m := re.FindStringSubmatchIndex(v); m != nil && m[4] >= 0 {
		if m[0] != 0 || v[m[4]:m[5]] != "theme" {
			return "", false
		}
		file = stripMarks(v[m[1]:])
	} else {
		file = themeFileOf(stripMarks(v), e.c.folder)
	}
	if file == "" {
		return "", false
	}
	out, ok := e.c.asset(file)
	if !ok {
		return "", false
	}
	if strings.HasPrefix(out, "data:") {
		return out, true
	}
	return `{{$.Site.Asset "` + out + `"}}`, true
}

// themeFileOf reads a path into the theme from an URL a theme may write:
// "/wp-content/themes/<folder>/assets/x.png", "./assets/x.png" or
// "assets/x.png".
func themeFileOf(u, folder string) string {
	if i := strings.Index(u, "/wp-content/themes/"); i >= 0 {
		rest := u[i+len("/wp-content/themes/"):]
		s, file, ok := strings.Cut(rest, "/")
		if !ok || folder != "" && s != folder {
			return ""
		}
		return file
	}
	if strings.Contains(u, ":") || strings.HasPrefix(u, "/") {
		return ""
	}
	return strings.TrimPrefix(u, "./")
}

// styleClass turns a style attribute into a generated class: its
// declarations, with url()s mapped to bundled files, each checked as the
// theme's stylesheets are. "" when nothing is left.
func (e *emitter) styleClass(style string) string {
	var ds []decl
	for _, d := range splitDecls(style) {
		prop, val, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		prop = strings.ToLower(strings.TrimSpace(prop))
		val = strings.TrimSpace(val)
		if !cssPropRe.MatchString(prop) || val == "" || strings.ContainsAny(val, "{}<>@!\\") {
			continue
		}
		if strings.Contains(strings.ToLower(val), "url(") {
			val = rewriteURLs(val, e.c.cssURL(""))
		}
		if checkDecl(prop, val) {
			ds = append(ds, decl{prop, val})
		}
	}
	return e.c.classFor("wpi", ds)
}

var cssPropRe = regexp.MustCompile(`^(--)?[a-z][a-z0-9-]{0,60}$`)

// checkDecl checks one declaration as the theme's stylesheets are checked.
func checkDecl(prop, val string) bool {
	return cleanStatement(".x{"+prop+":"+val+"}") != ""
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
