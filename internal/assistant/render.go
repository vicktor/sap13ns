package assistant

import (
	"bytes"
	"html/template"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// Answer is a reply rendered for the visitor.
type Answer struct {
	HTML    template.HTML
	Sources []Source // the sources the reply cites, by number
}

// The model's Markdown: no raw HTML (goldmark drops it), no tables or
// images (the policy drops them), links only to pages of this site.
var answerMD = goldmark.New(goldmark.WithExtensions(extension.Strikethrough))

var (
	answerPolicyOnce sync.Once
	answerPolicy     *bluemonday.Policy
	// citation matches [1], [1, 2], [1][2] is two of them; 【1】 too.
	citation = regexp.MustCompile(`(?:\[|【)\s*(\d{1,2}(?:\s*[,;]\s*\d{1,2})*)\s*(?:\]|】)(\([^)\s]*\))?`)
	// marker is the placeholder a citation becomes while Markdown is
	// rendered: private-use characters no reply can contain.
	marker = regexp.MustCompile("\uE000(\\d{1,2})\uE001")
)

func policy() *bluemonday.Policy {
	answerPolicyOnce.Do(func() {
		p := bluemonday.NewPolicy()
		p.AllowElements("p", "br", "ul", "ol", "li", "strong", "em", "b", "i", "del", "code", "pre", "blockquote")
		p.AllowAttrs("href").Matching(regexp.MustCompile(`^/(?:[^/\\]|$)`)).OnElements("a")
		p.RequireParseableURLs(true)
		p.AllowRelativeURLs(true)
		answerPolicy = p
	})
	return answerPolicy
}

// RenderAnswer turns the model's Markdown into safe HTML and its [n]
// citations into links to the numbered sources. Brackets with no valid
// source number are left as written.
func RenderAnswer(text string, sources []Source) Answer {
	byN := map[int]Source{}
	for _, s := range sources {
		byN[s.N] = s
	}
	text = strings.NewReplacer("\uE000", "", "\uE001", "").Replace(text)
	cited := map[int]bool{}
	text = citation.ReplaceAllStringFunc(text, func(m string) string {
		sub := citation.FindStringSubmatch(m)
		var b strings.Builder
		for _, f := range strings.FieldsFunc(sub[1], func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			n, _ := strconv.Atoi(f)
			if _, ok := byN[n]; ok {
				cited[n] = true
				b.WriteString("\uE000" + strconv.Itoa(n) + "\uE001")
			}
		}
		if b.Len() == 0 {
			return m // not a citation: code such as a[0], or a made-up number
		}
		return b.String()
	})
	var buf bytes.Buffer
	if err := answerMD.Convert([]byte(text), &buf); err != nil {
		buf.Reset()
		buf.WriteString("<p>" + template.HTMLEscapeString(text) + "</p>")
	}
	html := policy().Sanitize(buf.String())
	html = marker.ReplaceAllStringFunc(html, func(m string) string {
		n, _ := strconv.Atoi(marker.FindStringSubmatch(m)[1])
		s := byN[n]
		return `<a class="assistant-cite" href="` + template.HTMLEscapeString(s.URL) + `" title="` +
			template.HTMLEscapeString(s.Title) + `">` + strconv.Itoa(n) + `</a>`
	})
	out := Answer{HTML: template.HTML(html), Sources: []Source{}}
	for n := range cited {
		out.Sources = append(out.Sources, byN[n])
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].N < out.Sources[j].N })
	return out
}
