package main

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var iconName = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// restoreIcons turns the Lucide SVGs that the host expanded earlier in the
// pipeline (shortcodes, previous filters) back into <span data-icon="…">
// placeholders. The host sanitizes the whole document again after every
// content.filter plugin that returns HTML, and <svg> is not allowed, so
// without this those icons would vanish; placeholders are expanded again.
func restoreIcons(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		if c.DataAtom == atom.Svg {
			if name := lucideName(c); name != "" {
				span := &html.Node{Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
					Attr: []html.Attribute{{Key: "data-icon", Val: name}}}
				n.InsertBefore(span, c)
				n.RemoveChild(c)
				c = span
			}
			continue
		}
		restoreIcons(c)
	}
}

// lucideName reads "info" from class="lucide lucide-info …".
func lucideName(svg *html.Node) string {
	for _, a := range svg.Attr {
		if a.Key != "class" {
			continue
		}
		fields := strings.Fields(a.Val)
		if len(fields) < 2 || fields[0] != "lucide" {
			return ""
		}
		if name, ok := strings.CutPrefix(fields[1], "lucide-"); ok && iconName.MatchString(name) {
			return name
		}
	}
	return ""
}
