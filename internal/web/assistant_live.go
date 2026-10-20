package web

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
	"golang.org/x/text/unicode/norm"

	"sap13ns/internal/assistant"
)

// Live sources of the site assistant: what visitors read but the index does
// not hold, because it is not a post or a page.
//
//   - The front page as it renders: a theme's own front page or the page
//     builder's sections (a one-page or landing site) are templates, not
//     posts. The rendered page is split at its h1 and h2 headings; the
//     sections that share the most words with the question go to the prompt.
//   - The hosting plans of a network's portal, with their prices: they come
//     from the billing and change without any post changing, so they are
//     read at every question.

const (
	liveSections   = 2    // front page sections per question, at most
	liveSectionMax = 2400 // characters of one section
	liveTextMax    = 60000
)

// liveUA keeps the front page fetch out of the analytics (it is a bot).
const liveUA = "sap13ns-assistant"

// frontSection is one part of the rendered front page.
type frontSection struct {
	Heading string
	Anchor  string // id to link to, "" for the top
	Text    string
	words   []string
}

type frontPage struct {
	Title    string
	Sections []frontSection
	Plans    string // anchor of the section with the pricing table
}

// frontCache holds the rendered front page per language until the content
// changes (contentChanged).
type frontCache struct {
	mu    sync.Mutex
	pages map[string]*frontPage
}

func (c *frontCache) invalidate() {
	c.mu.Lock()
	c.pages = nil
	c.mu.Unlock()
}

// liveSources returns the live sources for a question, numbered from n.
func (s *Server) liveSources(ctx context.Context, lang, query string, n int) []assistant.Source {
	var out []assistant.Source
	fp := s.frontText(ctx, lang)
	home := s.langPath(lang, "/")
	if plans := s.plansSource(lang); plans != "" {
		u := home
		if fp != nil && fp.Plans != "" {
			u += "#" + fp.Plans
		}
		out = append(out, assistant.Source{N: n, Title: s.i18n.T(lang, "Hosting plans"), URL: u, Lang: lang,
			Snippet: snippetOf(plans, 220), Content: plans})
		n++
	}
	if fp == nil {
		return out
	}
	for _, sec := range rankSections(fp.Sections, query, liveSections) {
		u := home
		if sec.Anchor != "" {
			u += "#" + sec.Anchor
		}
		out = append(out, assistant.Source{N: n, Title: fp.Title, Heading: sec.Heading, URL: u, Lang: lang,
			Snippet: snippetOf(sec.Text, 220), Content: sec.Text})
		n++
	}
	return out
}

// plansSource describes the hosting plans the pricing table shows, in
// words, or "" when the site sells none.
func (s *Server) plansSource(lang string) string {
	if s.portal == nil {
		return ""
	}
	t := func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }
	// The plans' names, descriptions and features are translated by the
	// theme that shows them, as on the page.
	th := s.themes.Active(s.settings().ActiveTheme)
	var b strings.Builder
	for _, p := range s.planViews() {
		b.WriteString("- " + th.Translate(lang, p.Name))
		switch {
		case p.Free:
			b.WriteString(": " + t("free"))
		case p.Price != "" && p.Interval == "year":
			b.WriteString(": " + t("%s per year", p.Price))
		case p.Price != "":
			b.WriteString(": " + t("%s per month", p.Price))
			if p.YearlyPrice != "" {
				b.WriteString(", " + t("or %s per year", p.YearlyPrice))
			}
		}
		switch p.Tax {
		case "included":
			b.WriteString(" (" + t("VAT included") + ")")
		case "added":
			b.WriteString(" (" + t("+ VAT") + ")")
		}
		if p.Description != "" {
			b.WriteString(". " + th.Translate(lang, p.Description))
		}
		var feats []string
		for _, f := range p.Features {
			if f.Arg != "" {
				feats = append(feats, fmt.Sprintf(th.Translate(lang, f.Text), f.Arg))
			} else {
				feats = append(feats, th.Translate(lang, f.Text))
			}
		}
		if len(feats) > 0 {
			b.WriteString(". " + strings.Join(feats, "; "))
		}
		b.WriteString(".\n")
	}
	return strings.TrimSpace(b.String())
}

// frontText renders the front page in lang, as a visitor without a session
// sees it, and splits it into sections. nil when it does not render (a
// private site, an error).
func (s *Server) frontText(ctx context.Context, lang string) *frontPage {
	s.front.mu.Lock()
	defer s.front.mu.Unlock()
	if fp, ok := s.front.pages[lang]; ok {
		return fp
	}
	var fp *frontPage
	u := *s.cfg.BaseURL
	u.Path, u.RawQuery, u.Fragment = s.langPath(lang, "/"), "", ""
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", liveUA)
	req.Header.Set("Accept-Language", lang)
	req.RemoteAddr = "127.0.0.1:0"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK && strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		fp = parseFront(rec.Body.Bytes())
	}
	if ctx.Err() != nil {
		return fp // not kept: a canceled request may have cut it short
	}
	if s.front.pages == nil {
		s.front.pages = map[string]*frontPage{}
	}
	s.front.pages[lang] = fp
	return fp
}

// parseFront extracts the readable text of a page's <main> (the whole body
// without one), split at its h1 and h2 headings.
func parseFront(page []byte) *frontPage {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil
	}
	fp := &frontPage{}
	var main, body *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch {
			case n.DataAtom == atom.Title && fp.Title == "" && n.FirstChild != nil:
				fp.Title = strings.TrimSpace(n.FirstChild.Data)
			case n.DataAtom == atom.Main && main == nil:
				main = n
			case n.DataAtom == atom.Body && body == nil:
				body = n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	root := cmp.Or(main, body)
	if root == nil {
		return nil
	}
	if i := strings.LastIndex(fp.Title, " · "); i > 0 {
		fp.Title = fp.Title[i+len(" · "):]
	}

	cur := &frontSection{}
	var text strings.Builder
	total := 0
	flush := func() {
		cur.Text = limitText(collapse(text.String()), liveSectionMax)
		if cur.Text != "" || cur.Heading != "" {
			cur.words = words(cur.Heading + " " + cur.Text)
			fp.Sections = append(fp.Sections, *cur)
		}
		text.Reset()
	}
	anchor := "" // id of the innermost element with one around the text
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if total > liveTextMax {
			return
		}
		switch n.Type {
		case html.TextNode:
			text.WriteString(n.Data)
			total += len(n.Data)
			return
		case html.ElementNode:
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}
		switch n.DataAtom {
		case atom.Script, atom.Style, atom.Template, atom.Noscript, atom.Svg, atom.Nav, atom.Form, atom.Button, atom.Select, atom.Textarea, atom.Input:
			return
		}
		if attr(n, "aria-hidden") == "true" || hasAttr(n, "hidden") {
			return
		}
		if hasAttr(n, "data-plans") && fp.Plans == "" {
			fp.Plans = cmp.Or(cur.Anchor, anchor)
		}
		prev := anchor
		if id := attr(n, "id"); id != "" && n != root {
			anchor = id
		}
		defer func() { anchor = prev }()
		// A section runs from one h1 or h2 to the next; the headings under
		// it are lines of its text.
		if n.DataAtom == atom.H1 || n.DataAtom == atom.H2 {
			flush()
			var h strings.Builder
			collectText(n, &h)
			cur = &frontSection{Heading: collapse(h.String()), Anchor: anchor}
			return
		}
		block := isBlock(n.DataAtom)
		if block {
			text.WriteString("\n")
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if block {
			text.WriteString("\n")
		}
	}
	walk(root)
	flush()
	if len(fp.Sections) == 0 {
		return nil
	}
	return fp
}

func collectText(n *html.Node, b *strings.Builder) {
	if n.Type == html.TextNode {
		b.WriteString(n.Data)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.DataAtom == atom.Svg || attr(c, "aria-hidden") == "true") {
			continue
		}
		collectText(c, b)
	}
}

func isBlock(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Section, atom.Article, atom.Li, atom.Ul, atom.Ol, atom.Dl, atom.Dt, atom.Dd, atom.Br,
		atom.H3, atom.H4, atom.H5, atom.H6, atom.Header, atom.Footer, atom.Aside, atom.Blockquote, atom.Pre, atom.Table, atom.Tr, atom.Figure, atom.Figcaption:
		return true
	}
	return false
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

// collapse joins the words of each line and drops empty lines.
func collapse(s string) string {
	var lines []string
	for l := range strings.Lines(s) {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

func limitText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func snippetOf(s string, n int) string { return limitText(strings.Join(strings.Fields(s), " "), n) }

// words lists the folded words of s with four letters or more, cut to
// their first six letters: a cheap stem, enough for "precio" and
// "precios", "plan" and "planes".
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(foldText(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 4 || stopWords[w] {
			continue
		}
		if r := []rune(w); len(r) > 6 {
			w = string(r[:6])
		}
		out = append(out, w)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// stopWords are common words of four letters or more, in the languages of
// the admin, that would match any section.
var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`about also because been before being between both does each from have here into just like more most much only other over same some such than that their them then there these they this those very what when where which while will with would your
		algo algun alguna antes aqui cada como con cual cuales cuando desde donde esta estas este esto estos hace hasta hay mas mismo mucho muy nada nos otra otro para pero poco porque puede pueden que quien sea ser sobre solo son su sus tambien tanto tiene tienen todo todos una unas uno unos usted vosotros`) {
		stopWords[w] = true
	}
}

// foldText lowers the case and drops accents.
func foldText(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// rankSections returns up to max sections sharing words with the query,
// most shared first (in page order on ties).
func rankSections(secs []frontSection, query string, max int) []frontSection {
	q := words(query)
	if len(q) == 0 {
		return nil
	}
	type scored struct {
		i, n int
	}
	var hits []scored
	for i, sec := range secs {
		n := 0
		for _, w := range q {
			if _, ok := slices.BinarySearch(sec.words, w); ok {
				n++
			}
		}
		// A heading that names a word of the question counts double.
		for _, w := range words(sec.Heading) {
			if slices.Contains(q, w) {
				n++
			}
		}
		if n > 0 {
			hits = append(hits, scored{i, n})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int { return b.n - a.n })
	var out []frontSection
	for _, h := range hits[:min(len(hits), max)] {
		out = append(out, secs[h.i])
	}
	return out
}
