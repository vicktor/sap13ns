// The toc plugin adds an automatic table of contents to posts and pages.
//
// Filter collects the h2–h4 headings of the rendered HTML, gives the ones
// without an id a stable, unique, transliterated slug, and inserts an
// <nav class="card toc"> with a nested ordered list after the first
// paragraph, or where the author wrote a [toc] marker. Headings inside code,
// quotes, callouts and other boxed content are ignored.
//
// This file and slug.go are plain Go so they can be tested natively; main.go
// adapts the sap13ns plugin SDK (wasip1 only).
package main

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Options configure one run of Filter.
type Options struct {
	Enabled     bool   // false: only remove [toc] markers
	MinHeadings int    // fewer headings than this: no table
	MaxLevel    int    // deepest heading level listed (2–4)
	Title       string // visible title, also the accessible name
}

// Settings defaults (keep in sync with plugin.json).
const (
	defaultMinHeadings = 3
	defaultMaxLevel    = 4
	defaultPostTypes   = "posts"
)

// OptionsFrom reads the plugin settings for a post of the given type and
// language. siteLang is used when the post has no language.
func OptionsFrom(settings map[string]string, postType, postLang, siteLang string) Options {
	o := Options{MinHeadings: defaultMinHeadings, MaxLevel: defaultMaxLevel}
	if n, err := strconv.Atoi(strings.TrimSpace(settings["min_headings"])); err == nil {
		o.MinHeadings = max(n, 1)
	}
	switch strings.TrimSpace(settings["max_depth"]) {
	case "h2":
		o.MaxLevel = 2
	case "h3":
		o.MaxLevel = 3
	}
	types := strings.TrimSpace(settings["post_types"])
	if types == "" {
		types = defaultPostTypes
	}
	if postType == "" {
		postType = "post"
	}
	o.Enabled = types == "both" || types == "posts" && postType == "post" || types == "pages" && postType == "page"
	o.Title = strings.TrimSpace(settings["title"])
	if o.Title == "" {
		o.Title = defaultTitle(postLang, siteLang)
	}
	return o
}

func defaultTitle(postLang, siteLang string) string {
	lang := postLang
	if lang == "" {
		lang = siteLang
	}
	if lang == "es" || strings.HasPrefix(lang, "es-") {
		return "Índice"
	}
	return "Contents"
}

type heading struct {
	node  *html.Node
	level int
	text  string
}

// Filter returns the HTML with a table of contents and reports whether it
// changed anything. Unparsable input is returned unchanged.
func Filter(src string, o Options) (string, bool) {
	if strings.TrimSpace(src) == "" || o.MaxLevel < 2 {
		return src, false
	}
	root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(src), root)
	if err != nil {
		return src, false
	}
	for _, n := range nodes {
		root.AppendChild(n)
	}

	used := map[string]bool{}
	var headings []heading
	var markers []*html.Node
	hasTOC := false
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.TextNode && c.Parent == root && isMarkerText(c.Data) {
				markers = append(markers, c)
				continue
			}
			if c.Type != html.ElementNode {
				continue
			}
			if id := attr(c, "id"); id != "" {
				used[id] = true
			}
			if hasClass(c, "toc") {
				hasTOC = true
			}
			if skipped(c) {
				collectIDs(c, used)
				continue
			}
			if lvl := headingLevel(c); lvl >= 2 && lvl <= o.MaxLevel {
				if t := headingText(c); t != "" {
					headings = append(headings, heading{node: c, level: lvl, text: t})
				}
			}
			if c.DataAtom == atom.P && isMarkerParagraph(c) {
				markers = append(markers, c)
				continue
			}
			walk(c)
		}
	}
	walk(root)

	show := o.Enabled && !hasTOC && len(headings) >= max(o.MinHeadings, 1)
	if !show {
		if len(markers) == 0 {
			return src, false
		}
		for _, m := range markers {
			m.Parent.RemoveChild(m)
		}
		return render(root), true
	}

	ids := make([]string, len(headings))
	for i, h := range headings {
		id := attr(h.node, "id")
		if id == "" {
			id = uniqueID(slugify(h.text), used)
			setAttr(h.node, "id", id)
		}
		ids[i] = id
	}
	toc := buildTOC(headings, ids, o.Title)

	switch {
	case len(markers) > 0:
		markers[0].Parent.InsertBefore(toc, markers[0])
		for _, m := range markers {
			m.Parent.RemoveChild(m)
		}
	default:
		first := topLevel(headings[0].node, root)
		var para *html.Node
		for c := root.FirstChild; c != nil && c != first; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == atom.P {
				para = c
				break
			}
		}
		if para != nil {
			root.InsertBefore(toc, para.NextSibling)
		} else {
			root.InsertBefore(toc, first)
		}
	}
	return render(root), true
}

// buildTOC renders the nested list. A heading nests under the closest
// previous heading of a higher level, so skipped levels (h2 → h4) only
// indent once and a document starting at h3 still works.
func buildTOC(hs []heading, ids []string, title string) *html.Node {
	nav := element(atom.Nav, "class", "card toc", "aria-label", ariaLabel(title))
	header := element(atom.Header)
	strong := element(atom.Strong)
	strong.AppendChild(&html.Node{Type: html.TextNode, Data: title})
	header.AppendChild(strong)
	nav.AppendChild(header)

	type frame struct {
		level int
		item  *html.Node // the <li> of that heading; nil for the root
	}
	top := element(atom.Ol)
	nav.AppendChild(top)
	stack := []frame{{level: 0}}
	for i, h := range hs {
		for len(stack) > 1 && stack[len(stack)-1].level >= h.level {
			stack = stack[:len(stack)-1]
		}
		list := top
		if parent := stack[len(stack)-1].item; parent != nil {
			if l := parent.LastChild; l != nil && l.DataAtom == atom.Ol {
				list = l
			} else {
				list = element(atom.Ol)
				parent.AppendChild(list)
			}
		}
		li := element(atom.Li)
		a := element(atom.A, "href", "#"+ids[i])
		a.AppendChild(&html.Node{Type: html.TextNode, Data: h.text})
		li.AppendChild(a)
		list.AppendChild(li)
		stack = append(stack, frame{level: h.level, item: li})
	}
	return nav
}

// skipped reports whether headings inside n belong to quoted, code or boxed
// content rather than to the document outline.
func skipped(n *html.Node) bool {
	switch n.DataAtom {
	case atom.Pre, atom.Code, atom.Blockquote, atom.Aside, atom.Nav, atom.Figure,
		atom.Table, atom.Details, atom.Header, atom.Footer, atom.Template, atom.Svg:
		return true
	}
	return hasClass(n, "alert") || hasClass(n, "toc")
}

func headingLevel(n *html.Node) int {
	switch n.DataAtom {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 0
}

// headingText is the visible text of a heading, without footnote references
// or screen-reader-only additions, with whitespace collapsed.
func headingText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch {
			case c.Type == html.TextNode:
				b.WriteString(c.Data)
			case c.Type != html.ElementNode:
			case c.DataAtom == atom.Svg, c.DataAtom == atom.Script, c.DataAtom == atom.Style:
			case hasClass(c, "sr-only"), isFootnoteRef(c):
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func isFootnoteRef(n *html.Node) bool {
	if n.DataAtom != atom.Sup {
		return false
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && hasClass(c, "footnote-ref") {
			return true
		}
	}
	return false
}

func isMarkerText(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "[toc]") }

// isMarkerParagraph matches <p>[toc]</p> exactly (any case, surrounding
// whitespace allowed), so `[toc]` written as code is left alone.
func isMarkerParagraph(p *html.Node) bool {
	c := p.FirstChild
	return c != nil && c.NextSibling == nil && c.Type == html.TextNode && isMarkerText(c.Data)
}

func collectIDs(n *html.Node, used map[string]bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			if id := attr(c, "id"); id != "" {
				used[id] = true
			}
			collectIDs(c, used)
		}
	}
}

func topLevel(n, root *html.Node) *html.Node {
	for n.Parent != nil && n.Parent != root {
		n = n.Parent
	}
	return n
}

// ariaLabel keeps the characters the host sanitizer accepts in aria-label.
func ariaLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == '"' {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	if strings.TrimSpace(s) == "" {
		return "Contents"
	}
	return s
}

func element(a atom.Atom, kv ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: a.String(), DataAtom: a}
	for i := 0; i+1 < len(kv); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
	}
	return n
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Namespace == "" && a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func render(root *html.Node) string {
	restoreIcons(root)
	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&b, c)
	}
	return b.String()
}
