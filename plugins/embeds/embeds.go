// The embeds plugin turns standalone YouTube and Vimeo links into
// click-to-load video players.
//
// Filter looks for top-level paragraphs that contain nothing but one link to
// a video (a bare URL, which Markdown autolinks, or a titled link such as
// [My talk](https://youtu.be/…)) and replaces them with a <figure class="card
// embed">. Without JavaScript the figure is a plain link to the video page;
// assets/embeds.js turns it into a real play button and only creates the
// player iframe (youtube-nocookie.com, or Vimeo with dnt=1) after the
// visitor clicks. Nothing is requested from the video site before that: no
// thumbnails, no scripts, no iframes.
//
// This file and providers.go are plain Go so they can be tested natively;
// handler.go adapts the SDK and main.go registers it (wasip1 only).
package main

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Options configure one run of Filter.
type Options struct {
	Enabled    map[string]bool // provider id → on
	Spanish    bool
	Notice     string // privacy note; "{provider}" is replaced by the provider name
	PrivacyURL string // optional link after the note ("" for none)
}

// Filter returns the HTML with video links replaced by embeds and reports
// whether it changed anything.
func Filter(src string, o Options) (string, bool) {
	if strings.TrimSpace(src) == "" || !strings.Contains(src, "http") {
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
	untitled := map[string]int{} // provider id → untitled embeds so far
	changed := false
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.P {
			continue
		}
		href, text, title, ok := standaloneLink(c)
		if !ok {
			continue
		}
		v, ok := match(href, o.Enabled)
		if !ok {
			continue
		}
		name, play := label(text, href, title), ""
		if name == "" {
			untitled[v.provider.id]++
			name, play = untitledName(v.provider.name, o.Spanish, untitled[v.provider.id])
		}
		fig := figure(v, strings.TrimSpace(href), name, play, o)
		root.InsertBefore(fig, c)
		root.RemoveChild(c)
		c = fig
		changed = true
	}
	if !changed {
		return src, false
	}
	restoreIcons(root)
	var b strings.Builder
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return src, false
		}
	}
	return b.String(), true
}

// standaloneLink reports whether p holds nothing but one link (or one bare
// URL) and returns its href, visible text and title attribute.
func standaloneLink(p *html.Node) (href, text, title string, ok bool) {
	var only *html.Node
	for c := p.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode && strings.TrimSpace(c.Data) == "" || c.Type == html.CommentNode {
			continue
		}
		if only != nil {
			return "", "", "", false
		}
		only = c
	}
	switch {
	case only == nil:
		return "", "", "", false
	case only.Type == html.TextNode:
		s := strings.TrimSpace(only.Data)
		if strings.ContainsAny(s, " \t\n\r") {
			return "", "", "", false
		}
		return s, s, "", true
	case only.Type == html.ElementNode && only.DataAtom == atom.A:
		href, ok := attr(only, "href")
		if !ok || !inlineOnly(only) {
			return "", "", "", false
		}
		t, _ := attr(only, "title")
		return href, textOf(only), t, true
	}
	return "", "", "", false
}

// inlineOnly rejects links that wrap images or other embedded content, which
// the figure would drop.
func inlineOnly(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode {
			continue
		}
		switch c.DataAtom {
		case atom.Strong, atom.B, atom.Em, atom.I, atom.Code, atom.Span, atom.Mark, atom.Small, atom.U, atom.S, atom.Del, atom.Ins, atom.Sub, atom.Sup:
			if !inlineOnly(c) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// label is the title of the video: the link text when it is not just the
// URL, else the link's title attribute, else "" (untitled).
func label(text, href, title string) string {
	text = collapse(text)
	if text != "" && !sameURL(text, href) {
		return text
	}
	return collapse(title)
}

func sameURL(text, href string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		if i := strings.Index(s, "://"); i >= 0 {
			s = s[i+3:]
		}
		return strings.TrimSuffix(strings.TrimPrefix(s, "www."), "/")
	}
	return norm(text) == norm(href)
}

// untitledName names the n-th untitled video of a provider ("YouTube video
// 2") and returns the matching play label ("Play YouTube video 2").
func untitledName(provider string, spanish bool, n int) (name, play string) {
	num := ""
	if n > 1 {
		num = " " + strconv.Itoa(n)
	}
	if spanish {
		return "Vídeo de " + provider + num, "Reproducir vídeo de " + provider + num
	}
	return provider + " video" + num, "Play " + provider + " video" + num
}

// figure builds the embed. Structure (see assets/embeds.css and .js):
//
//	figure.card.embed[data-category]
//	  a.embed-link            the original link: icon, title, "Watch on …"
//	  a.embed-player          the privacy player URL; hidden, read by the JS
//	  figcaption.embed-notice the privacy note
//
// play is the button label; "" builds it from the title.
func figure(v video, href, name, play string, o Options) *html.Node {
	p := v.provider
	class := "card embed embed-" + p.id
	if v.portrait {
		class += " embed-portrait"
	}
	fig := element(atom.Figure, "class", class, "data-category", p.id)

	link := element(atom.A, "class", "embed-link", "href", href)
	icon := element(atom.Span, "class", "embed-icon", "aria-hidden", "true")
	icon.AppendChild(element(atom.Span, "data-icon", "play"))
	link.AppendChild(icon)
	text := element(atom.Span, "class", "embed-text")
	strong := element(atom.Strong, "class", "embed-title")
	strong.AppendChild(textNode(name))
	text.AppendChild(strong)
	watch := "Watch on " + p.name
	if o.Spanish {
		watch = "Ver en " + p.name
	}
	small := element(atom.Small, "class", "embed-source")
	small.AppendChild(textNode(watch))
	text.AppendChild(small)
	link.AppendChild(text)
	fig.AppendChild(link)

	switch {
	case play != "":
	case o.Spanish:
		play = "Reproducir vídeo: " + name + " (" + p.name + ")"
	default:
		play = "Play video: " + name + " (" + p.name + ")"
	}
	player := element(atom.A, "class", "embed-player", "href", v.player, "aria-label", ariaLabel(play))
	prov := element(atom.Span, "class", "embed-provider")
	prov.AppendChild(textNode(p.name))
	player.AppendChild(prov)
	fig.AppendChild(player)

	caption := element(atom.Figcaption, "class", "embed-notice")
	caption.AppendChild(textNode(strings.ReplaceAll(o.Notice, "{provider}", p.name)))
	if o.PrivacyURL != "" {
		policy := "Privacy policy"
		if o.Spanish {
			policy = "Política de privacidad"
		}
		caption.AppendChild(textNode(" "))
		a := element(atom.A, "href", o.PrivacyURL)
		a.AppendChild(textNode(policy))
		caption.AppendChild(a)
	}
	fig.AppendChild(caption)
	return fig
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
	return s
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				b.WriteString(c.Data)
			case html.ElementNode:
				if !hasClass(c, "sr-only") {
					walk(c)
				}
			}
		}
	}
	walk(n)
	return b.String()
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func element(a atom.Atom, kv ...string) *html.Node {
	n := &html.Node{Type: html.ElementNode, Data: a.String(), DataAtom: a}
	for i := 0; i+1 < len(kv); i += 2 {
		n.Attr = append(n.Attr, html.Attribute{Key: kv[i], Val: kv[i+1]})
	}
	return n
}

func textNode(s string) *html.Node { return &html.Node{Type: html.TextNode, Data: s} }

func attr(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Namespace == "" && strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
	}
	return "", false
}

func hasClass(n *html.Node, class string) bool {
	v, _ := attr(n, "class")
	for _, c := range strings.Fields(v) {
		if c == class {
			return true
		}
	}
	return false
}
