package main

import (
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

type alertKind struct {
	category string // data-category understood by the themes
	icon     string // Lucide icon embedded by the host
	en, es   string // titles
}

// alertKinds follows GitHub's five alert types.
var alertKinds = map[string]alertKind{
	"note":      {"info", "info", "Note", "Nota"},
	"tip":       {"success", "sparkles", "Tip", "Consejo"},
	"important": {"important", "message-square", "Important", "Importante"},
	"warning":   {"warning", "triangle-alert", "Warning", "Advertencia"},
	"caution":   {"error", "circle-alert", "Caution", "Precaución"},
}

// FilterAlerts turns blockquotes whose first line is [!NOTE], [!TIP],
// [!IMPORTANT], [!WARNING] or [!CAUTION] (any case) into alerts titled in
// lang ("es" or English). It reports whether anything changed.
func FilterAlerts(src, lang string) (string, bool) {
	if !strings.Contains(src, "[!") || !strings.Contains(src, "<blockquote") {
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
	spanish := lang == "es" || strings.HasPrefix(lang, "es-")
	changed := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode || c.DataAtom == atom.Pre || c.DataAtom == atom.Code {
				continue
			}
			if c.DataAtom == atom.Blockquote {
				if alert := convert(c, spanish); alert != nil {
					n.InsertBefore(alert, c)
					n.RemoveChild(c)
					c = alert
					changed = true
				}
			}
			walk(c)
		}
	}
	walk(root)
	if !changed {
		return src, false
	}
	restoreIcons(root)
	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&b, c)
	}
	return b.String(), true
}

// convert builds the alert for a blockquote, or returns nil if the
// blockquote is not an alert. The blockquote's children move into the
// alert's <section>.
func convert(bq *html.Node, spanish bool) *html.Node {
	p := firstElement(bq)
	if p == nil || p.DataAtom != atom.P || p.FirstChild == nil || p.FirstChild.Type != html.TextNode {
		return nil
	}
	text := p.FirstChild
	trimmed := strings.TrimLeft(text.Data, " \t\n")
	end := strings.IndexByte(trimmed, ']')
	if !strings.HasPrefix(trimmed, "[!") || end < 0 {
		return nil
	}
	name := strings.ToLower(trimmed[2:end])
	kind, ok := alertKinds[name]
	if !ok {
		return nil
	}

	// Remove the marker (and the line break after it) from the paragraph.
	if rest := strings.TrimLeft(trimmed[end+1:], " \t\n"); rest != "" {
		text.Data = rest
	} else {
		p.RemoveChild(text)
		if br := p.FirstChild; br != nil && br.Type == html.ElementNode && br.DataAtom == atom.Br {
			p.RemoveChild(br)
		}
		if f := p.FirstChild; f != nil && f.Type == html.TextNode {
			if f.Data = strings.TrimLeft(f.Data, " \t\n"); f.Data == "" {
				p.RemoveChild(f)
			}
		}
	}
	if p.FirstChild == nil {
		bq.RemoveChild(p)
	}
	if !hasContent(bq) {
		// GitHub does not render empty alerts either; restore the marker.
		restore := &html.Node{Type: html.TextNode, Data: trimmed[:end+1]}
		if p.Parent == nil {
			bq.InsertBefore(p, bq.FirstChild)
		}
		p.InsertBefore(restore, p.FirstChild)
		return nil
	}

	title := kind.en
	if spanish {
		title = kind.es
	}
	div := element(atom.Div, "class", "alert markdown-alert markdown-alert-"+name, "role", "note", "data-category", kind.category)
	if name == "caution" {
		div.Attr = append(div.Attr, html.Attribute{Key: "data-variant", Val: "destructive"})
	}
	div.AppendChild(element(atom.Span, "data-icon", kind.icon))
	strong := element(atom.Strong)
	strong.AppendChild(&html.Node{Type: html.TextNode, Data: title})
	div.AppendChild(strong)
	section := element(atom.Section)
	for c := bq.FirstChild; c != nil; c = bq.FirstChild {
		bq.RemoveChild(c)
		section.AppendChild(c)
	}
	div.AppendChild(section)
	return div
}

func firstElement(n *html.Node) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.ElementNode:
			return c
		case c.Type == html.TextNode && strings.TrimSpace(c.Data) != "":
			return nil
		}
	}
	return nil
}

func hasContent(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode || c.Type == html.TextNode && strings.TrimSpace(c.Data) != "" {
			return true
		}
	}
	return false
}

func element(a atom.Atom, kv ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: a.String(), DataAtom: a}
	for i := 0; i+1 < len(kv); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
	}
	return n
}
