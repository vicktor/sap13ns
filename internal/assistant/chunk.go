// Package assistant answers visitors' questions from the site's own
// content (retrieval-augmented generation). Public posts and pages are cut
// into chunks, indexed for full-text search and, with pgvector and an
// embeddings provider, for semantic search; a question retrieves the best
// chunks of both and the AI provider answers from them alone, citing them.
// The MCP endpoint serves the same search to outside agents.
package assistant

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Piece is one chunk of a post: a heading path ("Title › Section ›
// Subsection") and the Markdown text under it.
type Piece struct {
	Heading string
	Content string
}

// Text is what gets embedded and hashed: the heading path gives every chunk
// its context.
func (p Piece) Text() string { return p.Heading + "\n\n" + p.Content }

// ChunkOptions are sizes in characters (runes).
type ChunkOptions struct {
	Target  int // aim for chunks of about this size
	Max     int // a section up to this size stays whole
	Overlap int // text repeated from the previous chunk when a section is split
}

// DefaultChunking keeps chunks at 2,600–3,000 characters (roughly 600–750
// tokens) with about 15% overlap.
var DefaultChunking = ChunkOptions{Target: 2600, Max: 3000, Overlap: 400}

// headingSep joins the heading path.
const headingSep = " › "

type section struct {
	path []string // heading texts, outermost first; empty before the first heading
	body string
}

var (
	atxHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	setextUnder = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	fenceOpen   = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	image       = regexp.MustCompile(`!\[([^\]\n]*)\]\([^)\n]*\)`)
	inlineLink  = regexp.MustCompile(`\[([^\]\n]+)\]\((?:[^()\n]|\([^()\n]*\))*\)`)
	refDef      = regexp.MustCompile(`^ {0,3}\[[^\]\n]+\]:\s+\S+.*$`)
	htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTag     = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9-]*(?:\s[^<>]*)?/?>`)
	// A plugin shortcode alone on its line ([gallery ids="1,2"], [/note]).
	shortcodeLine = regexp.MustCompile(`^\s*\[/?[a-z][a-z0-9_-]{0,39}(?:\s+[^\]\n]*)?/?\]\s*$`)
	headingMarks  = strings.NewReplacer("*", "", "_", "", "`", "")
)

// Chunk cuts a post into pieces for the index. Sections follow the
// Markdown headings (ATX and setext, ignoring fenced code); small sibling
// sections are merged up to opt.Target, and sections longer than opt.Max
// are split at paragraphs (then sentences, then words) with opt.Overlap
// characters carried over. The excerpt, when the body does not already
// start with it, opens the first piece. A post without text has no pieces.
func Chunk(title, excerpt, markdown string, opt ChunkOptions) []Piece {
	if opt.Target <= 0 {
		opt = DefaultChunking
	}
	opt.Max = max(opt.Max, opt.Target)
	opt.Overlap = min(opt.Overlap, opt.Target/2)
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	if title == "" {
		title = "-"
	}
	sections := splitSections(clean(markdown))
	excerpt = strings.TrimSpace(excerpt)
	if excerpt != "" && (len(sections) == 0 || !strings.HasPrefix(sections[0].body, excerpt)) {
		if len(sections) > 0 && len(sections[0].path) == 0 {
			sections[0].body = excerpt + "\n\n" + sections[0].body
		} else {
			sections = append([]section{{body: excerpt}}, sections...)
		}
	}

	var out []Piece
	emit := func(path []string, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		out = append(out, Piece{Heading: strings.Join(append([]string{title}, path...), headingSep), Content: body})
	}
	// Pending merged sections.
	var group []section
	groupLen := 0
	flush := func() {
		if len(group) == 0 {
			return
		}
		common := group[0].path
		for _, s := range group[1:] {
			common = commonPrefix(common, s.path)
		}
		var b strings.Builder
		for _, s := range group {
			if b.Len() > 0 {
				b.WriteString("\n\n")
			}
			// Headings below the common path stay in the text.
			for i := len(common); i < len(s.path); i++ {
				b.WriteString(strings.Repeat("#", min(i+2, 6)) + " " + s.path[i] + "\n\n")
			}
			b.WriteString(s.body)
		}
		emit(common, b.String())
		group, groupLen = nil, 0
	}
	for _, s := range sections {
		n := runeLen(s.body)
		for i := range s.path {
			n += runeLen(s.path[i]) + 4
		}
		if n > opt.Max {
			flush()
			for _, part := range splitLong(s.body, opt) {
				emit(s.path, part)
			}
			continue
		}
		if len(group) > 0 && groupLen+n > opt.Target {
			flush()
		}
		group = append(group, s)
		groupLen += n
	}
	flush()
	return out
}

// clean drops what carries no meaning for search: images (their alt text
// stays), link targets, reference definitions, HTML and shortcode lines.
// Fenced code is kept as written.
func clean(md string) string {
	md = strings.ReplaceAll(strings.ReplaceAll(md, "\r\n", "\n"), "\r", "\n")
	md = htmlComment.ReplaceAllString(md, "")
	lines := strings.Split(md, "\n")
	fence := ""
	out := lines[:0]
	for _, l := range lines {
		if fence != "" {
			if closesFence(l, fence) {
				fence = ""
			}
			out = append(out, l)
			continue
		}
		if f := fenceOpen.FindStringSubmatch(l); f != nil {
			fence = f[1]
			out = append(out, l)
			continue
		}
		if refDef.MatchString(l) || shortcodeLine.MatchString(l) {
			continue
		}
		l = image.ReplaceAllString(l, "$1")
		l = inlineLink.ReplaceAllString(l, "$1")
		l = htmlTag.ReplaceAllString(l, "")
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// splitSections cuts the document at headings outside fenced code.
func splitSections(md string) []section {
	var (
		out    []section
		stack  []string
		levels []int
		body   []string
		fence  string
	)
	flush := func() {
		b := strings.TrimSpace(strings.Join(body, "\n"))
		if b != "" {
			out = append(out, section{path: append([]string(nil), stack...), body: b})
		}
		body = body[:0]
	}
	heading := func(level int, text string) {
		flush()
		text = strings.TrimSpace(headingMarks.Replace(text))
		for len(levels) > 0 && levels[len(levels)-1] >= level {
			levels, stack = levels[:len(levels)-1], stack[:len(stack)-1]
		}
		if text != "" {
			levels, stack = append(levels, level), append(stack, text)
		}
	}
	for _, l := range strings.Split(md, "\n") {
		if fence != "" {
			if closesFence(l, fence) {
				fence = ""
			}
			body = append(body, l)
			continue
		}
		if f := fenceOpen.FindStringSubmatch(l); f != nil {
			fence = f[1]
			body = append(body, l)
			continue
		}
		if m := atxHeading.FindStringSubmatch(l); m != nil {
			heading(len(m[1]), m[2])
			continue
		}
		// Setext: a paragraph line underlined with === or ---.
		if m := setextUnder.FindStringSubmatch(l); m != nil && len(body) > 0 && isParagraphLine(body[len(body)-1]) &&
			(len(body) == 1 || strings.TrimSpace(body[len(body)-2]) == "") {
			text := body[len(body)-1]
			body = body[:len(body)-1]
			level := 2
			if m[1][0] == '=' {
				level = 1
			}
			heading(level, text)
			continue
		}
		body = append(body, l)
	}
	flush()
	return out
}

// closesFence reports a closing fence for an opening one: the same
// character, at least as long, and nothing after it.
func closesFence(l, open string) bool {
	f := fenceOpen.FindStringSubmatch(l)
	return f != nil && f[1][0] == open[0] && len(f[1]) >= len(open) && strings.TrimSpace(l[len(f[0]):]) == ""
}

// isParagraphLine rejects lines that cannot be a setext heading's text.
func isParagraphLine(l string) bool {
	t := strings.TrimSpace(l)
	if t == "" || strings.HasPrefix(l, "    ") || strings.HasPrefix(l, "\t") {
		return false
	}
	switch t[0] {
	case '>', '-', '*', '+', '|', '#':
		return false
	}
	if i := strings.IndexAny(t, ".)"); i > 0 && i < 10 && strings.TrimLeft(t[:i], "0123456789") == "" {
		return false // ordered list item
	}
	return true
}

func commonPrefix(a, b []string) []string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// splitLong cuts a long section into windows of about opt.Target
// characters. Blocks (paragraphs, lists, fenced code) stay whole when they
// fit; each window after the first starts with the tail of the previous one.
func splitLong(body string, opt ChunkOptions) []string {
	var units []string
	for _, b := range blocks(body) {
		if runeLen(b) <= opt.Target {
			units = append(units, b)
			continue
		}
		units = append(units, splitText(b, opt.Target)...)
	}
	var (
		out []string
		cur []string
		n   int
	)
	for _, u := range units {
		ul := runeLen(u)
		if len(cur) > 0 && n+ul+2 > opt.Target {
			out = append(out, strings.Join(cur, "\n\n"))
			carry := tail(cur, opt.Overlap)
			cur, n = nil, 0
			if carry != "" && runeLen(carry)+ul+2 <= opt.Max {
				cur, n = []string{carry}, runeLen(carry)
			}
		}
		cur = append(cur, u)
		n += ul + 2
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n\n"))
	}
	return out
}

// tail returns about n characters from the end of the window: whole blocks
// when they fit, else the end of the last block cut at a sentence or word.
func tail(window []string, n int) string {
	if n <= 0 {
		return ""
	}
	var keep []string
	size := 0
	for i := len(window) - 1; i >= 0; i-- {
		l := runeLen(window[i])
		if size+l > n {
			if len(keep) == 0 {
				return lastChars(window[i], n)
			}
			break
		}
		keep = append([]string{window[i]}, keep...)
		size += l + 2
	}
	return strings.Join(keep, "\n\n")
}

// lastChars returns the end of s, at most n characters, starting at a
// sentence boundary when there is one, else at a word.
func lastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	t := r[len(r)-n:]
	for i := 0; i < len(t)-1 && i < n/2; i++ {
		if (t[i] == '.' || t[i] == '?' || t[i] == '!' || t[i] == '\n') && unicode.IsSpace(t[i+1]) {
			return strings.TrimSpace(string(t[i+1:]))
		}
	}
	for i := 0; i < len(t) && i < n/2; i++ {
		if unicode.IsSpace(t[i]) {
			return strings.TrimSpace(string(t[i:]))
		}
	}
	return string(t)
}

// blocks splits Markdown at blank lines, keeping fenced code together.
func blocks(md string) []string {
	var (
		out   []string
		cur   []string
		fence string
	)
	flush := func() {
		if b := strings.TrimSpace(strings.Join(cur, "\n")); b != "" {
			out = append(out, b)
		}
		cur = cur[:0]
	}
	for _, l := range strings.Split(md, "\n") {
		if fence != "" {
			cur = append(cur, l)
			if closesFence(l, fence) {
				fence = ""
				flush()
			}
			continue
		}
		if f := fenceOpen.FindStringSubmatch(l); f != nil {
			flush()
			fence = f[1]
			cur = append(cur, l)
			continue
		}
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

// splitText cuts an oversized block into pieces of at most size characters
// at sentence ends, falling back to words, then to a hard cut.
func splitText(s string, size int) []string {
	var out []string
	r := []rune(s)
	for len(r) > size {
		cut := -1
		for i := size - 1; i > size/2; i-- {
			if (r[i] == '.' || r[i] == '?' || r[i] == '!' || r[i] == '\n') && i+1 < len(r) && unicode.IsSpace(r[i+1]) {
				cut = i + 1
				break
			}
		}
		if cut < 0 {
			for i := size - 1; i > size/2; i-- {
				if unicode.IsSpace(r[i]) {
					cut = i
					break
				}
			}
		}
		if cut < 0 {
			cut = size
		}
		if p := strings.TrimSpace(string(r[:cut])); p != "" {
			out = append(out, p)
		}
		r = []rune(strings.TrimLeftFunc(string(r[cut:]), unicode.IsSpace))
	}
	if p := strings.TrimSpace(string(r)); p != "" {
		out = append(out, p)
	}
	return out
}
