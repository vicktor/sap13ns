// Package content turns author Markdown into safe HTML, expands plugin
// shortcodes and provides text helpers (slugs, excerpts, reading time).
package content

import (
	"bytes"
	"html/template"
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
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

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM, extension.Footnote),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(lazyImages{}, 500)),
	),
	// html.WithUnsafe() is deliberately NOT enabled: raw HTML in Markdown is
	// dropped and dangerous URLs (javascript:, vbscript:, data:) are removed.
	goldmark.WithRendererOptions(html.WithXHTML()),
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
		p.AllowAttrs("class").Matching(regexp.MustCompile(`^(language-[a-zA-Z0-9_+-]{1,40}|footnote-ref|footnote-backref|footnotes|task-list-item|contains-task-list)$`)).Globally()
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
		p.AllowAttrs("role").Matching(regexp.MustCompile(`^(alert|status|note|group|list|listitem|progressbar|img|separator)$`)).Globally()
		p.AllowAttrs("aria-label").Matching(regexp.MustCompile(`^[^<>"]{1,200}$`)).Globally()
		p.AllowAttrs("aria-hidden").Matching(regexp.MustCompile(`^(true|false)$`)).Globally()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
		p.AllowElements("section", "header", "footer", "aside", "figure", "figcaption", "time", "progress", "meter", "details", "summary", "mark", "small")
		p.AllowAttrs("datetime").Matching(regexp.MustCompile(`^[0-9T:.+Z-]{4,40}$`)).OnElements("time")
		p.AllowAttrs("value", "max", "min").Matching(regexp.MustCompile(`^[0-9.]{1,12}$`)).OnElements("progress", "meter")
		p.AllowAttrs("open").OnElements("details")
		p.RequireNoFollowOnFullyQualifiedLinks(true)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		pluginPolicy = p
	})
	return pluginPolicy
}

// IconRenderer returns inline SVG markup for an icon name ("" if unknown).
type IconRenderer func(name string) string

var iconPlaceholder = regexp.MustCompile(`<span data-icon="([a-z0-9-]{1,40})"(?: class="([a-zA-Z0-9 _:./\[\]-]{1,200})")?></span>`)

// SanitizePluginHTML cleans plugin output and expands icon placeholders
// (`<span data-icon="check"></span>`) into Lucide SVGs.
func SanitizePluginHTML(raw string, icons IconRenderer) template.HTML {
	clean := PluginPolicy().Sanitize(raw)
	if icons != nil && strings.Contains(clean, "data-icon=") {
		clean = iconPlaceholder.ReplaceAllStringFunc(clean, func(m string) string {
			sub := iconPlaceholder.FindStringSubmatch(m)
			if svg := icons(sub[1]); svg != "" {
				return svg
			}
			return ""
		})
	}
	return template.HTML(clean)
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
	if err := md.Convert([]byte(src), &buf); err != nil {
		return template.HTML(template.HTMLEscapeString(src))
	}
	out := ContentPolicy().SanitizeBytes(buf.Bytes())
	if len(replacements) == 0 {
		return template.HTML(out)
	}
	s := string(out)
	for token, html := range replacements {
		// Block-level shortcodes end up wrapped in a paragraph.
		s = strings.ReplaceAll(s, "<p>"+token+"</p>", html)
		s = strings.ReplaceAll(s, token, html)
	}
	return template.HTML(s)
}

// RenderPlain renders Markdown without shortcodes (previews of excerpts,
// widget text, feeds when plugins are unavailable).
func RenderPlain(src string) template.HTML { return Render(src, nil, nil, nil) }
