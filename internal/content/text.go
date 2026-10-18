package content

import (
	"html/template"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/unicode/norm"
)

var translit = strings.NewReplacer("ñ", "n", "Ñ", "n", "ß", "ss", "æ", "ae", "Æ", "ae", "ø", "o", "Ø", "o", "œ", "oe", "Œ", "oe", "ł", "l", "Ł", "l", "đ", "d", "&", " y ")

// Slugify builds a URL slug: lowercase ASCII letters, digits and hyphens.
func Slugify(s string) string {
	s = translit.Replace(s)
	s = norm.NFD.String(s)
	var b strings.Builder
	dash := false
loop:
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
		if b.Len() >= 120 {
			break loop
		}
	}
	return strings.Trim(b.String(), "-")
}

// PlainText strips tags from HTML and collapses whitespace.
func PlainText(h template.HTML) string {
	z := html.NewTokenizer(strings.NewReader(string(h)))
	var b strings.Builder
	skip := 0
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			// io.EOF or malformed input: return what was collected.
			return strings.Join(strings.Fields(b.String()), " ")
		case html.TextToken:
			if skip == 0 {
				b.Write(z.Text())
			}
		case html.StartTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "sup":
				skip++
			case "p", "br", "li", "h1", "h2", "h3", "h4", "h5", "h6", "div", "tr", "pre", "blockquote":
				b.WriteByte(' ')
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "sup":
				if skip > 0 {
					skip--
				}
			case "p", "li", "h1", "h2", "h3", "h4", "h5", "h6", "div", "td", "th":
				b.WriteByte(' ')
			}
		}
	}
}

// Truncate shortens text at a word boundary, adding an ellipsis.
func Truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	cut := max
	for i := max; i > max*2/3; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(runes[:cut]), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) + "…"
}

// Summary returns the explicit excerpt or the start of the content.
func Summary(excerpt, markdown string, max int) string {
	if strings.TrimSpace(excerpt) != "" {
		return Truncate(strings.Join(strings.Fields(excerpt), " "), max)
	}
	return Truncate(PlainText(RenderPlain(markdown)), max)
}

// WordCount counts words in rendered text.
func WordCount(s string) int { return len(strings.Fields(s)) }

// ReadingMinutes estimates reading time at 220 words per minute.
func ReadingMinutes(markdown string) int {
	n := WordCount(markdown)
	m := (n + 219) / 220
	if m < 1 {
		m = 1
	}
	return m
}
