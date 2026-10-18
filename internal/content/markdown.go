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
		parser.WithASTTransformers(util.Prioritized(lazyImages{}, 500)),
	),
	// html.WithUnsafe() is deliberately NOT enabled: raw HTML in Markdown is
	// dropped and dangerous URLs (javascript:, vbscript:, data:) are removed.
	goldmark.WithRendererOptions(html.WithXHTML(),
		// Before the table extension (500), so that it wins for tables.
		renderer.WithNodeRenderers(util.Prioritized(tableScroll{}, 400))),
)

var (
	contentPolicyOnce sync.Once
	contentPolicy     *bluemonday.Policy
	pluginPolicyOnce  sync.Once
	pluginPolicy      *bluemonday.Policy
)

var safeClass = regexp.MustCompile(`^[a-zA-Z0-9 _:./\[\]-]{1,200}$`)

// ContentPolicy sanitizes rendered Markdown (defence in depth on top of
// goldmark's own escaping).
func ContentPolicy() *bluemonday.Policy {
	contentPolicyOnce.Do(func() {
		p := bluemonday.UGCPolicy()
		p.AllowAttrs("id").Matching(regexp.MustCompile(`^[a-zA-Z0-9_:-]{1,120}$`)).OnElements("h1", "h2", "h3", "h4", "h5", "h6", "li", "sup")
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^(language-[a-zA-Z0-9_+-]{1,40}|footnote-ref|footnote-backref|footnotes|task-list-item|contains-task-list|table-scroll)$`)).Globally()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
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

// Render converts Markdown to sanitized HTML. When shortcode is non-nil,
// registered shortcodes are expanded first.
func Render(src string, isRegistered func(name string) bool, shortcode ShortcodeFunc, icons IconRenderer) template.HTML {
	var replacements map[string]string
	if isRegistered != nil && shortcode != nil && strings.Contains(src, "[") {
		src, replacements = expandShortcodes(src, isRegistered, shortcode, icons)
	}
	var buf bytes.Buffer
	pc := parser.NewContext(parser.WithIDs(headingIDs{}))
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
