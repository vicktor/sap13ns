// Package content turns author Markdown into safe HTML, expands plugin
// shortcodes and provides text helpers (slugs, excerpts, reading time).
package content

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"html/template"
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	xhtml "golang.org/x/net/html"
)

// lazyImages marks every image as lazily loaded and asynchronously decoded.
type lazyImages struct{}

func (lazyImages) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if img, ok := n.(*ast.Image); ok && entering {
			img.SetAttributeString("loading", []byte("lazy"))
			img.SetAttributeString("decoding", []byte("async"))
		}
		return ast.WalkContinue, nil
	})
}

// headingIDs gives headings readable ids for any language: accents are
// folded like slugs ("Detalle técnico" -> "detalle-tecnico"; goldmark's own
// generator drops the letters), and text with nothing to transliterate gets a
// hash, which stays stable when other headings change. Duplicates get -2, -3.
type headingIDs map[string]bool

func (ids headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := Slugify(string(value))
	if base == "" {
		h := fnv.New32a()
		h.Write(bytes.TrimSpace(value))
		base = fmt.Sprintf("section-%08x", h.Sum32())
	}
	id := base
	for n := 2; ids[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	ids[id] = true
	return []byte(id)
}

func (ids headingIDs) Put(value []byte) { ids[string(value)] = true }

// tableScroll renders every GFM table inside <div class="table-scroll">. The
// wrapper scrolls sideways when a table is wider than the column, so the
// table itself keeps display: table, and with it its row and column
// semantics for screen readers (a table set to display: block loses them in
// some browsers). It replaces the table extension's renderer for the table
// element only; rows and cells are still rendered by the extension.
type tableScroll struct{}

func (tableScroll) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(east.KindTable, func(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			_, _ = w.WriteString(`<div class="table-scroll"><table`)
			if n.Attributes() != nil {
				html.RenderAttributes(w, n, extension.TableAttributeFilter)
			}
			_, _ = w.WriteString(">\n")
		} else {
			_, _ = w.WriteString("</table></div>\n")
		}
		return ast.WalkContinue, nil
	})
}

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(lazyImages{}, 500), util.Prioritized(imageAttrs{}, 510), util.Prioritized(videos{}, 520)),
	),
	// html.WithUnsafe() is deliberately NOT enabled: raw HTML in Markdown is
	// dropped and dangerous URLs (javascript:, vbscript:, data:) are removed.
	goldmark.WithRendererOptions(html.WithXHTML(),
		// Before the table extension (500), so that it wins for tables.
		renderer.WithNodeRenderers(util.Prioritized(tableScroll{}, 400), util.Prioritized(videoRenderer{}, 400))),
)

var (
	contentPolicyOnce sync.Once
	contentPolicy     *bluemonday.Policy
	pluginPolicyOnce  sync.Once
	pluginPolicy      *bluemonday.Policy
)

var safeClass = regexp.MustCompile(`^[a-zA-Z0-9 _:./\[\]-]{1,200}$`)

// Image and video markup the renderer emits, and nothing looser: sizes are
// whole pixels, srcset only lists this site's uploads, classes are the
// alignments and the player's own.
var (
	imgSize       = regexp.MustCompile(`^(?:[1-9][0-9]{0,3}|1[0-5][0-9]{3}|16[0-2][0-9]{2}|163[0-7][0-9]|1638[0-4])$`)
	imgSrcset     = regexp.MustCompile(`^/uploads/[A-Za-z0-9/_.-]{1,100} [1-9][0-9]{0,4}w(?:, /uploads/[A-Za-z0-9/_.-]{1,100} [1-9][0-9]{0,4}w){0,7}$`)
	imgSizes      = regexp.MustCompile(`^\(max-width: 640px\) 100vw, [1-9][0-9]{0,3}px$`)
	imgAlt        = regexp.MustCompile(`^[^\x00-\x08\x0b\x0c\x0e-\x1f]{0,1000}$`)
	imgClass      = regexp.MustCompile(`^(?:sap-video-poster|align-(?:left|right|center|full))$`)
	videoClass    = regexp.MustCompile(`^sap-video sap-video-(?:youtube|vimeo)(?: sap-video-portrait)?$`)
	videoSpan     = regexp.MustCompile(`^sap-video-(?:play|caption|title|source)$`)
	videoText     = regexp.MustCompile(`^[^\x00-\x1f]{1,400}$`)
	videoProvider = regexp.MustCompile(`^(?:youtube|vimeo)$`)
)

// ugcWithoutImages is bluemonday's UGCPolicy (v1.0.27) without its image
// rules, which let any number or percentage through as width and height;
// attribute rules only add up, so they cannot be narrowed afterwards. Image
// maps are left out too: Markdown has none.
func ugcWithoutImages() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowStandardAttributes()
	p.AllowStandardURLs()
	p.AllowElements("article", "aside", "figure", "section", "summary", "hgroup")
	p.AllowAttrs("open").Matching(regexp.MustCompile(`(?i)^(|open)$`)).OnElements("details")
	p.AllowElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("cite").OnElements("blockquote")
	p.AllowElements("br", "div", "hr", "p", "span", "wbr")
	p.AllowAttrs("href").OnElements("a")
	p.AllowElements("abbr", "acronym", "cite", "code", "dfn", "em", "figcaption", "mark", "s", "samp", "strong", "sub", "sup", "var")
	p.AllowAttrs("cite").OnElements("q")
	p.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("time")
	p.AllowElements("b", "i", "pre", "small", "strike", "tt", "u")
	p.AllowAttrs("dir").Matching(bluemonday.Direction).OnElements("bdi", "bdo")
	p.AllowElements("rp", "rt", "ruby")
	p.AllowAttrs("cite").Matching(bluemonday.Paragraph).OnElements("del", "ins")
	p.AllowAttrs("datetime").Matching(bluemonday.ISO8601).OnElements("del", "ins")
	p.AllowLists()
	p.AllowTables()
	p.AllowAttrs("value", "min", "max", "low", "high", "optimum").Matching(bluemonday.Number).OnElements("meter")
	p.AllowAttrs("value", "max").Matching(bluemonday.Number).OnElements("progress")
	// Any text: UGCPolicy's alt pattern drops alt text with a colon, a
	// question mark or an ampersand, and attribute values are escaped anyway.
	p.AllowAttrs("alt").Matching(imgAlt).OnElements("img")
	p.AllowAttrs("title").Matching(imgAlt).OnElements("img", "a")
	p.AllowAttrs("src").OnElements("img")
	return p
}

// ContentPolicy sanitizes rendered Markdown (defence in depth on top of
// goldmark's own escaping).
func ContentPolicy() *bluemonday.Policy {
	contentPolicyOnce.Do(func() {
		p := ugcWithoutImages()
		p.AllowAttrs("id").Matching(regexp.MustCompile(`^[a-zA-Z0-9_:-]{1,120}$`)).OnElements("h1", "h2", "h3", "h4", "h5", "h6", "li", "sup")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^(language-[a-zA-Z0-9_+-]{1,40}|footnote-ref|footnote-backref|footnotes|task-list-item|contains-task-list|table-scroll)$`)).Globally()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
		allowImageLayout(p)
		// The click-to-load video player (video.go). data-video-src is the
		// only URL that becomes an iframe, and it must be a player URL.
		p.AllowAttrs("class").Matching(videoClass).OnElements("figure")
		p.AllowAttrs("data-video-provider").Matching(videoProvider).OnElements("figure")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^sap-video-link$`)).OnElements("a")
		p.AllowAttrs("data-video-src").Matching(PlayerURLPattern).OnElements("a")
		p.AllowAttrs("data-video-title", "data-video-label").Matching(videoText).OnElements("a")
		p.AllowAttrs("class").Matching(videoSpan).OnElements("span")
		p.AllowAttrs("aria-hidden").Matching(regexp.MustCompile(`^true$`)).OnElements("span")
		p.AllowAttrs("role").Matching(regexp.MustCompile(`^(doc-noteref|doc-backlink|doc-endnotes)$`)).Globally()
		p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
		p.AllowAttrs("checked", "disabled").OnElements("input")
		p.AllowElements("input")
		// Posts are written by staff, so their links are endorsed: UGCPolicy's
		// rel="nofollow" would also hide internal links from search engines.
		// Comments never come through here (they are plain text).
		p.RequireNoFollowOnLinks(false)
		p.AllowURLSchemes("tel")
		p.RequireNoReferrerOnLinks(false)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		p.RequireNoFollowOnFullyQualifiedLinks(false)
		contentPolicy = p
	})
	return contentPolicy
}

// allowImageLayout lets the size, alignment and srcset of
// ![alt](src){width=… align=…} through.
func allowImageLayout(p *bluemonday.Policy) {
	p.AllowAttrs("width", "height").Matching(imgSize).OnElements("img")
	p.AllowAttrs("class").Matching(imgClass).OnElements("img")
	p.AllowAttrs("srcset").Matching(imgSrcset).OnElements("img")
	p.AllowAttrs("sizes").Matching(imgSizes).OnElements("img")
}

// PluginPolicy sanitizes every fragment returned by a plugin. Plugins may use
// Basecoat component classes and data attributes, never scripts, styles,
// iframes, forms or event handlers.
func PluginPolicy() *bluemonday.Policy {
	pluginPolicyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("class").Matching(safeClass).Globally()
		p.AllowAttrs("data-variant", "data-size", "data-category", "data-icon").Matching(regexp.MustCompile(`^[a-z0-9-]{1,40}$`)).Globally()
		p.AllowAttrs("role").Matching(regexp.MustCompile(`^(alert|status|note|group|list|listitem|progressbar|img|separator|region|navigation)$`)).Globally()
		p.AllowAttrs("aria-label").Matching(regexp.MustCompile(`^[^<>"]{1,200}$`)).Globally()
		p.AllowAttrs("aria-hidden").Matching(regexp.MustCompile(`^(true|false)$`)).Globally()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
		// A filter returns the whole post, whose images keep their srcset.
		allowImageLayout(p)
		p.AllowElements("section", "header", "footer", "aside", "figure", "figcaption", "time", "progress", "meter", "details", "summary", "mark", "small")
		p.AllowAttrs("datetime").Matching(regexp.MustCompile(`^[0-9T:.+Z-]{4,40}$`)).OnElements("time")
		p.AllowAttrs("value", "max", "min").Matching(regexp.MustCompile(`^[0-9.]{1,12}$`)).OnElements("progress", "meter")
		p.AllowAttrs("open").OnElements("details")
		p.AllowElements("nav", "kbd")
		// Form placeholders, already qualified by the host ("plugin.form") and
		// replaced with the host-rendered form when the page is served.
		p.AllowAttrs("data-form").Matching(regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,49}\.[a-z][a-z0-9_-]{0,39}$`)).OnElements("div")
		// Link relations are the plugin's call (external-links sets them);
		// nothing is forced, so internal links stay followable.
		p.AllowAttrs("rel").Matching(regexp.MustCompile(`^(?:nofollow|noopener|noreferrer|sponsored|ugc|external|me)(?: (?:nofollow|noopener|noreferrer|sponsored|ugc|external|me)){0,6}$`)).OnElements("a")
		p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
		p.RequireNoFollowOnLinks(false)
		p.RequireNoFollowOnFullyQualifiedLinks(false)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		p.AllowURLSchemes("tel")
		pluginPolicy = p
	})
	return pluginPolicy
}

// IconRenderer returns inline SVG markup for an icon name ("" if unknown).
type IconRenderer func(name string) string

var iconPlaceholder = regexp.MustCompile(`<span data-icon="([a-z0-9-]{1,40})"(?: class="([a-zA-Z0-9 _:./\[\]-]{1,200})")?></span>`)

// SanitizePluginHTML cleans plugin output and expands icon placeholders
// (`<span data-icon="check"></span>`) into Lucide SVGs. With nil icons the
// placeholders are kept, for HTML that is sanitised again later (content
// filters): <svg> is not allowed, so an expanded icon would be stripped.
func SanitizePluginHTML(raw string, icons IconRenderer) template.HTML {
	return template.HTML(ExpandIcons(PluginPolicy().Sanitize(raw), icons))
}

// ExpandIcons replaces icon placeholders left in sanitised HTML.
func ExpandIcons(clean string, icons IconRenderer) string {
	if icons == nil || !strings.Contains(clean, "data-icon=") {
		return clean
	}
	return iconPlaceholder.ReplaceAllStringFunc(clean, func(m string) string {
		return icons(iconPlaceholder.FindStringSubmatch(m)[1])
	})
}

// ShortcodeFunc renders one shortcode occurrence. The returned HTML is
// sanitized by the caller.
type ShortcodeFunc func(sc Shortcode) (string, error)

// Options configure RenderWith.
type Options struct {
	// IsShortcode and Shortcode expand registered shortcodes first.
	IsShortcode func(name string) bool
	Shortcode   ShortcodeFunc
	Icons       IconRenderer
	// Video turns YouTube and Vimeo links into players (nil: links).
	Video *VideoOptions
	// Images describes media-library images by URL, for the height and
	// srcset of sized images ({width=…}). It must not block.
	Images func(src string) (ImageInfo, bool)
}

// Render converts Markdown to sanitized HTML. When shortcode is non-nil,
// registered shortcodes are expanded first.
func Render(src string, isRegistered func(name string) bool, shortcode ShortcodeFunc, icons IconRenderer) template.HTML {
	return RenderWith(src, Options{IsShortcode: isRegistered, Shortcode: shortcode, Icons: icons})
}

// RenderWith converts Markdown to sanitized HTML.
func RenderWith(src string, o Options) template.HTML {
	var replacements map[string]string
	isRegistered := o.IsShortcode
	if isRegistered != nil && o.Video != nil {
		// [video] is the core player's while video embeds are on.
		registered := isRegistered
		isRegistered = func(name string) bool { return name != "video" && registered(name) }
	}
	if isRegistered != nil && o.Shortcode != nil && strings.Contains(src, "[") {
		src, replacements = expandShortcodes(src, isRegistered, o.Shortcode, o.Icons)
	}
	var buf bytes.Buffer
	pc := parser.NewContext(parser.WithIDs(headingIDs{}))
	if o.Video != nil {
		pc.Set(videoOptionsKey, o.Video)
	}
	if o.Images != nil {
		pc.Set(imageOptionsKey, o.Images)
	}
	if err := md.Convert([]byte(src), &buf, parser.WithContext(pc)); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	out := ContentPolicy().SanitizeBytes(buf.Bytes())
	if len(replacements) == 0 {
		return template.HTML(out)
	}
	return template.HTML(substituteTokens(string(out), replacements))
}

// substituteTokens puts each shortcode's HTML where its token is, in text
// only. A token inside a tag (an author can put a shortcode in a link title
// or an image's alt text) is dropped: HTML spliced into an attribute value
// would break out of it.
func substituteTokens(s string, replacements map[string]string) string {
	for token := range replacements {
		// Block-level shortcodes end up wrapped in a paragraph. "<p>" is
		// always markup here: the sanitizer escapes "<" in attribute values.
		s = strings.ReplaceAll(s, "<p>"+token+"</p>", token)
	}
	var b strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break // io.EOF: the sanitizer's output is well formed
		}
		raw := string(z.Raw())
		for token, h := range replacements {
			if !strings.Contains(raw, token) {
				continue
			}
			if tt == xhtml.TextToken {
				raw = strings.ReplaceAll(raw, token, h)
			} else {
				raw = strings.ReplaceAll(raw, token, "")
			}
		}
		b.WriteString(raw)
	}
	return b.String()
}

// RenderPlain renders Markdown without shortcodes (previews of excerpts,
// widget text, feeds when plugins are unavailable).
func RenderPlain(src string) template.HTML { return Render(src, nil, nil, nil) }
