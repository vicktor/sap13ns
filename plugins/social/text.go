package main

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Text limits of the networks.
const (
	blueskyLimit   = 300 // graphemes; runes are a safe upper bound
	mastodonLimit  = 500 // characters, every URL counting as mastodonURLLen
	mastodonURLLen = 23
	maxTagRunes    = 64 // Bluesky's limit for one hashtag
)

// Hashtags turns tag names into hashtags: characters other than letters,
// digits, combining marks and "_" are removed, multi-word tags are
// CamelCased ("open source" → #OpenSource, "inteligencia artificial" →
// #InteligenciaArtificial), single words keep their case (#iOS), and empty,
// all-digit, overlong or repeated (ignoring case) tags are skipped.
func Hashtags(tags []string, limit int) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		if len(out) >= limit {
			break
		}
		words := strings.FieldsFunc(t, func(r rune) bool { return !tagRune(r) })
		var b strings.Builder
		for _, w := range words {
			if len(words) > 1 {
				r, size := utf8.DecodeRuneInString(w)
				b.WriteRune(unicode.ToUpper(r))
				w = w[size:]
			}
			b.WriteString(w)
		}
		tag := b.String()
		key := strings.ToLower(tag)
		if tag == "" || allDigits(tag) || utf8.RuneCountInString(tag) > maxTagRunes || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, "#"+tag)
	}
	return out
}

func tagRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '_'
}

func allDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

// Fields are the values of the template placeholders.
type Fields struct {
	Title, URL, Excerpt, Site string
	Hashtags                  []string
}

// Network is a text limit: the maximum length and how text is measured.
type Network struct {
	Name  string
	Limit int
	Len   func(string) int
}

var (
	blueskyNet  = Network{Name: "Bluesky", Limit: blueskyLimit, Len: utf8.RuneCountInString}
	mastodonNet = Network{Name: "Mastodon", Limit: mastodonLimit, Len: mastodonLen}
)

var urlRe = regexp.MustCompile(`https?://[^\s]+`)

// mastodonLen counts characters the way Mastodon does: every link counts
// as 23 characters, whatever its length.
func mastodonLen(s string) int {
	n := utf8.RuneCountInString(s)
	for _, u := range urlRe.FindAllString(s, -1) {
		n += mastodonURLLen - utf8.RuneCountInString(u)
	}
	return n
}

// Compose renders the template and fits it into the network's limit:
// the excerpt is shortened first, then the title (both with "…"), then
// trailing hashtags are dropped. The URL and the hashtags are never cut in
// half.
func Compose(tpl string, f Fields, net Network) string {
	fits := func(s string) bool { return net.Len(s) <= net.Limit }
	render := func(title, excerpt string, tags []string) string {
		return renderTemplate(tpl, Fields{Title: title, URL: f.URL, Excerpt: excerpt, Site: f.Site, Hashtags: tags})
	}
	title, excerpt, tags := oneLine(f.Title), oneLine(f.Excerpt), f.Hashtags
	text := render(title, excerpt, tags)
	if fits(text) {
		return text
	}
	if strings.Contains(tpl, "{excerpt}") && excerpt != "" {
		if s, ok := shrink(excerpt, func(e string) bool { return fits(render(title, e, tags)) }); ok {
			return render(title, s, tags)
		}
		excerpt = ""
		if text = render(title, "", tags); fits(text) {
			return text
		}
	}
	if strings.Contains(tpl, "{title}") && title != "" {
		if s, ok := shrink(title, func(t string) bool { return fits(render(t, excerpt, tags)) }); ok {
			return render(s, excerpt, tags)
		}
		title = string([]rune(title)[:1]) + "…"
	}
	for len(tags) > 0 {
		tags = tags[:len(tags)-1]
		if text = render(title, excerpt, tags); fits(text) {
			return text
		}
	}
	// A template with too much fixed text: keep whole words that fit.
	var b strings.Builder
	for _, tok := range strings.SplitAfter(text, " ") {
		if !fits(b.String() + strings.TrimRight(tok, " ") + "…") {
			break
		}
		b.WriteString(tok)
	}
	return strings.TrimSpace(b.String()) + "…"
}

// shrink returns the longest prefix of s (cut at a word boundary when
// possible, with "…") for which ok is true.
func shrink(s string, ok func(string) bool) (string, bool) {
	r := []rune(s)
	lo, hi, best := 1, len(r)-1, ""
	for lo <= hi {
		mid := (lo + hi) / 2
		cand := cut(r, mid)
		if ok(cand) {
			best, lo = cand, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best, best != ""
}

// cut keeps the first n runes, backing up to a space when one is near, and
// adds "…".
func cut(r []rune, n int) string {
	s := string(r[:n])
	if i := strings.LastIndexByte(s, ' '); i > len(s)*2/3 {
		s = s[:i]
	}
	return strings.TrimRightFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) + "…"
}

var manyNewlines = regexp.MustCompile(`\n{3,}`)

// renderTemplate replaces the placeholders and tidies the whitespace: the
// lines of an empty placeholder disappear and at most one blank line is
// kept between paragraphs.
func renderTemplate(tpl string, f Fields) string {
	text := strings.NewReplacer(
		"{title}", f.Title,
		"{url}", f.URL,
		"{excerpt}", f.Excerpt,
		"{site}", f.Site,
		"{hashtags}", strings.Join(f.Hashtags, " "),
	).Replace(strings.ReplaceAll(tpl, "\r\n", "\n"))
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRightFunc(l, unicode.IsSpace)
	}
	text = manyNewlines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(text)
}

// oneLine collapses whitespace, so a title or excerpt cannot break the
// layout of the template.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Truncate shortens s to at most n runes, with "…".
func Truncate(s string, n int) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return cut([]rune(s), n-1)
}
