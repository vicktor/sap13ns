package content

import (
	"html/template"
	"regexp"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	xhtml "golang.org/x/net/html"
)

// The page builder's HTML block (internal/builder) lets the site owner
// write markup by hand. It goes through HTMLBlockPolicy when it is saved
// and again when it is rendered:
//
//   - structural and text elements, links, images, lists, tables, figure,
//     details and summary, as in the Markdown pipeline;
//   - class and id attributes, a fixed set of aria-* attributes and role,
//     and data-* attributes other than the ones this site's own scripts
//     and templates act on (reservedData);
//   - never script, style (elements or attributes), event handlers,
//     iframes, objects, forms or javascript: and data: URLs. The CSP forbids
//     inline scripts and styles anyway; the policy keeps them out of the
//     page in the first place. Videos keep their own path (Markdown links).

var (
	htmlBlockPolicyOnce sync.Once
	htmlBlockPolicy     *bluemonday.Policy

	htmlBlockClass = regexp.MustCompile(`^[A-Za-z0-9 _:./\[\]-]{1,300}$`)
	htmlBlockID    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	htmlBlockText  = regexp.MustCompile(`^[^\x00-\x1f<>]{1,300}$`)
	htmlBlockIDRef = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ -]{0,200}$`)
	htmlBlockRole  = regexp.MustCompile(`^(?:note|region|group|list|listitem|img|presentation|none|separator|navigation|complementary|figure|heading|status|term|definition|doc-subtitle)$`)
	htmlBlockData  = regexp.MustCompile(`^data-[a-z][a-z0-9-]{0,40}$`)
)

// reservedData are the data-* attributes (exact names, or prefixes ending
// in "-") that the site's scripts, Basecoat or the server act on: the video
// player turns data-video-src into an iframe, data-form becomes a plugin's
// form, and so on. An HTML block may not use them.
var reservedData = []string{
	"data-video-", "data-form", "data-icon", "data-assistant", "data-reply-", "data-nav-", "data-theme",
	"data-copy-", "data-sticky", "data-editor", "data-endpoint", "data-lang", "data-confirm",
	"data-dialog", "data-overlay", "data-popover", "data-builder", "data-plugin", "data-sap",
	"data-csrf", "data-state", "data-side", "data-align", "data-tooltip", "data-keep-mobile-sidebar-open",
}

// HTMLBlockPolicy is the sanitiser of hand-written HTML blocks.
func HTMLBlockPolicy() *bluemonday.Policy {
	htmlBlockPolicyOnce.Do(func() {
		p := ugcWithoutImages()
		p.AllowElements("header", "footer", "nav", "main", "address", "kbd", "small", "details", "caption")
		p.AllowAttrs("class").Matching(htmlBlockClass).Globally()
		p.AllowAttrs("id").Matching(htmlBlockID).Globally()
		p.AllowAttrs("role").Matching(htmlBlockRole).Globally()
		p.AllowAttrs("aria-label", "aria-roledescription").Matching(htmlBlockText).Globally()
		p.AllowAttrs("aria-labelledby", "aria-describedby", "aria-controls", "aria-details").Matching(htmlBlockIDRef).Globally()
		p.AllowAttrs("aria-hidden", "aria-expanded").Matching(regexp.MustCompile(`^(?:true|false)$`)).Globally()
		p.AllowAttrs("aria-current").Matching(regexp.MustCompile(`^(?:page|step|location|date|time|true|false)$`)).Globally()
		p.AllowAttrs("aria-level").Matching(regexp.MustCompile(`^[1-6]$`)).Globally()
		p.AllowDataAttributes()
		p.AllowAttrs("loading").Matching(regexp.MustCompile(`^lazy$`)).OnElements("img")
		p.AllowAttrs("decoding").Matching(regexp.MustCompile(`^async$`)).OnElements("img")
		p.AllowAttrs("width", "height").Matching(imgSize).OnElements("img")
		p.AllowAttrs("target").Matching(regexp.MustCompile(`^_blank$`)).OnElements("a")
		p.AllowAttrs("rel").Matching(regexp.MustCompile(`^(?:nofollow|noopener|noreferrer|sponsored|ugc|external|me)(?: (?:nofollow|noopener|noreferrer|sponsored|ugc|external|me)){0,6}$`)).OnElements("a")
		p.AllowAttrs("colspan", "rowspan").Matching(regexp.MustCompile(`^[1-9][0-9]?$`)).OnElements("td", "th")
		p.AllowAttrs("scope").Matching(regexp.MustCompile(`^(?:row|col|rowgroup|colgroup)$`)).OnElements("th")
		p.AllowURLSchemes("tel")
		p.RequireNoFollowOnLinks(false)
		p.RequireNoFollowOnFullyQualifiedLinks(false)
		p.AddTargetBlankToFullyQualifiedLinks(false)
		// A link that opens a new tab never gets a handle on this page.
		p.RequireNoReferrerOnFullyQualifiedLinks(false)
		htmlBlockPolicy = p
	})
	return htmlBlockPolicy
}

// SanitizeHTMLBlock cleans the markup of an HTML block.
func SanitizeHTMLBlock(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	clean := HTMLBlockPolicy().Sanitize(raw)
	if strings.Contains(clean, "data-") || strings.Contains(clean, `target="_blank"`) {
		clean = filterHTMLBlockAttrs(clean)
	}
	return strings.TrimSpace(clean)
}

// RenderHTMLBlock is SanitizeHTMLBlock for a template.
func RenderHTMLBlock(raw string) template.HTML { return template.HTML(SanitizeHTMLBlock(raw)) }

func reservedDataAttr(name string) bool {
	if !htmlBlockData.MatchString(name) {
		return true
	}
	for _, r := range reservedData {
		if name == r || (strings.HasSuffix(r, "-") && strings.HasPrefix(name, r)) || strings.HasPrefix(name, r+"-") {
			return true
		}
	}
	return false
}

// filterHTMLBlockAttrs drops the data-* attributes an HTML block may not
// use and adds rel="noopener" to links that open a new tab. The input is
// the sanitiser's output, which is well formed.
func filterHTMLBlockAttrs(s string) string {
	var b strings.Builder
	z := xhtml.NewTokenizer(strings.NewReader(s))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			break
		}
		if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
			b.Write(z.Raw())
			continue
		}
		tok := z.Token()
		attrs := tok.Attr[:0]
		blank, rel := false, -1
		for _, a := range tok.Attr {
			if strings.HasPrefix(a.Key, "data-") && reservedDataAttr(a.Key) {
				continue
			}
			if a.Key == "target" && a.Val == "_blank" {
				blank = true
			}
			if a.Key == "rel" {
				rel = len(attrs)
			}
			attrs = append(attrs, a)
		}
		if blank && tok.Data == "a" {
			if rel < 0 {
				attrs = append(attrs, xhtml.Attribute{Key: "rel", Val: "noopener"})
			} else if !strings.Contains(" "+attrs[rel].Val+" ", " noopener ") {
				attrs[rel].Val += " noopener"
			}
		}
		tok.Attr = attrs
		b.WriteString(tok.String())
	}
	return b.String()
}
