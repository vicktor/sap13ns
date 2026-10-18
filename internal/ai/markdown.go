package ai

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Markdown helpers for Translate: fenced code blocks are swapped for
// placeholders before sending (the model never sees, and cannot alter,
// code), long bodies are split at top-level headings, and the result is
// compared with the original.

var ErrStructure = errors.New("translation broke the Markdown structure")

// StructureError lists what CheckStructure found broken.
type StructureError struct {
	Problems []string
}

func (e *StructureError) Error() string {
	return ErrStructure.Error() + ": " + strings.Join(e.Problems, "; ")
}

func (e *StructureError) Unwrap() error { return ErrStructure }

const codePlaceholder = "@@CODE_%d@@"

var placeholderRe = regexp.MustCompile(`@@CODE_(\d+)@@`)

// fenceOf returns the fence that opens a fenced code block on this line
// ("```" or "~~~", possibly longer), or "".
func fenceOf(line string) string {
	t := strings.TrimRight(line, "\r\n")
	trimmed := strings.TrimLeft(t, " ")
	if len(t)-len(trimmed) > 3 || len(trimmed) < 3 {
		return ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 || c == '`' && strings.ContainsRune(trimmed[n:], '`') {
		return ""
	}
	return trimmed[:n]
}

func closesFence(line, fence string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == ""
}

// protectCode replaces each fenced code block with a placeholder line.
func protectCode(md string) (string, []string) {
	var out strings.Builder
	var blocks []string
	var block strings.Builder
	fence := ""
	for _, line := range strings.SplitAfter(md, "\n") {
		if fence != "" {
			block.WriteString(line)
			if closesFence(line, fence) {
				fence = ""
				blocks = append(blocks, strings.TrimRight(block.String(), "\n"))
				block.Reset()
			}
			continue
		}
		if f := fenceOf(line); f != "" {
			fence = f
			block.WriteString(line)
			fmt.Fprintf(&out, codePlaceholder+"\n", len(blocks))
			continue
		}
		out.WriteString(line)
	}
	if fence != "" { // unclosed fence: the rest of the document is code
		blocks = append(blocks, strings.TrimRight(block.String(), "\n"))
	}
	return out.String(), blocks
}

// restoreCode puts the code blocks back; every placeholder must appear
// exactly once.
func restoreCode(md string, blocks []string) (string, error) {
	seen := make([]int, len(blocks))
	var bad []string
	out := placeholderRe.ReplaceAllStringFunc(md, func(m string) string {
		i, err := strconv.Atoi(placeholderRe.FindStringSubmatch(m)[1])
		if err != nil || i >= len(blocks) {
			bad = append(bad, "unknown code placeholder "+m)
			return m
		}
		seen[i]++
		return blocks[i]
	})
	for i, n := range seen {
		if n != 1 {
			bad = append(bad, fmt.Sprintf("code block %d appears %d times", i+1, n))
		}
	}
	if len(bad) > 0 {
		return "", &StructureError{Problems: bad}
	}
	return out, nil
}

var headingRe = regexp.MustCompile(`^(#{1,6})[ \t]`)

// splitSections splits Markdown (code already protected) before every
// heading of the highest level present, then packs the sections into chunks
// of at most max bytes. A section larger than max is split at blank lines,
// and as a last resort at line breaks.
func splitSections(md string, max int) []string {
	lines := strings.SplitAfter(md, "\n")
	top := 7
	for _, l := range lines {
		if m := headingRe.FindStringSubmatch(l); m != nil {
			top = min(top, len(m[1]))
		}
	}
	var sections []string
	var cur strings.Builder
	for _, l := range lines {
		if m := headingRe.FindStringSubmatch(l); m != nil && len(m[1]) == top && cur.Len() > 0 {
			sections = append(sections, cur.String())
			cur.Reset()
		}
		cur.WriteString(l)
	}
	if cur.Len() > 0 {
		sections = append(sections, cur.String())
	}

	var pieces []string
	for _, s := range sections {
		if len(s) <= max {
			pieces = append(pieces, s)
			continue
		}
		for _, p := range splitAfterSep(s, "\n\n") {
			if len(p) <= max {
				pieces = append(pieces, p)
				continue
			}
			for _, l := range splitAfterSep(p, "\n") {
				for len(l) > max {
					cut := max
					for cut > 0 && (l[cut]&0xC0) == 0x80 { // keep UTF-8 sequences whole
						cut--
					}
					pieces = append(pieces, l[:cut])
					l = l[cut:]
				}
				pieces = append(pieces, l)
			}
		}
	}
	return pack(pieces, max)
}

func splitAfterSep(s, sep string) []string {
	parts := strings.SplitAfter(s, sep)
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func pack(pieces []string, max int) []string {
	var chunks []string
	var cur strings.Builder
	for _, p := range pieces {
		if cur.Len() > 0 && cur.Len()+len(p) > max {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

var (
	urlRe        = regexp.MustCompile(`(?:https?|mailto):[^\s<>()\[\]"'` + "`" + `]+`)
	linkRe       = regexp.MustCompile(`!?\[[^\]\n]*\]\(([^)\s]+)(?:\s+"[^"\n]*")?\)`)
	inlineCodeRe = regexp.MustCompile("`[^`\n]+`")
	// Shortcode tags as parsed by internal/content: [name], [name k="v" …],
	// [name /] and [/name]. Link labels ("[text](…)", "[text][ref]") are
	// excluded by the caller.
	shortcodeRe = regexp.MustCompile(`\[/?[a-z][a-z0-9_-]{0,39}(?:\s+[a-z][a-z0-9_-]*=(?:"[^"\]\n]*"|'[^'\]\n]*'|[^\s\]"']+))*\s*/?\]`)
)

type mdStats struct {
	fences     int
	headings   int
	inlineCode int
	links      []string // link and image targets
	urls       []string
	shortcodes []string
}

func markdownStats(md string) mdStats {
	var st mdStats
	var prose strings.Builder
	fence := ""
	for _, line := range strings.SplitAfter(md, "\n") {
		if fence != "" {
			if closesFence(line, fence) {
				fence = ""
				st.fences++
			}
			continue
		}
		if f := fenceOf(line); f != "" {
			fence = f
			st.fences++
			continue
		}
		if headingRe.MatchString(line) {
			st.headings++
		}
		prose.WriteString(line)
	}
	text := prose.String()
	st.inlineCode = len(inlineCodeRe.FindAllString(text, -1))
	text = inlineCodeRe.ReplaceAllString(text, "``")
	for _, m := range linkRe.FindAllStringSubmatch(text, -1) {
		st.links = append(st.links, m[1])
	}
	st.urls = urlRe.FindAllString(text, -1)
	// A bare "[word]" counts only when "[/word]" closes it somewhere, so
	// that bracketed prose such as "[sic]" may be translated.
	var tags []string
	closing := map[string]bool{}
	for _, loc := range shortcodeRe.FindAllStringIndex(text, -1) {
		if loc[1] < len(text) && (text[loc[1]] == '(' || text[loc[1]] == '[' || text[loc[1]] == ':') {
			continue // a link label or reference definition
		}
		tag := text[loc[0]:loc[1]]
		if strings.HasPrefix(tag, "[/") {
			closing[strings.Trim(tag, "[/]")] = true
		}
		tags = append(tags, tag)
	}
	for _, tag := range tags {
		bare := strings.Trim(tag, "[]")
		if strings.HasPrefix(tag, "[/") || strings.ContainsAny(bare, " \t=/") || closing[bare] {
			st.shortcodes = append(st.shortcodes, tag)
		}
	}
	return st
}

// missing counts the items of want that have no counterpart in got.
func missing(want, got []string) int {
	have := map[string]int{}
	for _, g := range got {
		have[g]++
	}
	n := 0
	for _, w := range want {
		if have[w] > 0 {
			have[w]--
		} else {
			n++
		}
	}
	return n
}

// tolerance allows small slips on long documents.
func tolerance(n, divisor int) int { return n / divisor }

// CheckStructure compares a translation with its source and fails when the
// Markdown structure is badly broken: code fences must match exactly; link
// targets, URLs, shortcode tags, headings and inline code may differ by a
// small fraction on long documents.
func CheckStructure(src, dst string) error {
	a, b := markdownStats(src), markdownStats(dst)
	var problems []string
	if a.fences != b.fences {
		problems = append(problems, fmt.Sprintf("code fences: %d, expected %d", b.fences, a.fences))
	}
	if d := abs(a.headings - b.headings); d > tolerance(a.headings, 10) {
		problems = append(problems, fmt.Sprintf("headings: %d, expected %d", b.headings, a.headings))
	}
	if d := abs(a.inlineCode - b.inlineCode); d > max(1, tolerance(a.inlineCode, 5)) {
		problems = append(problems, fmt.Sprintf("inline code spans: %d, expected %d", b.inlineCode, a.inlineCode))
	}
	if m := missing(a.links, b.links); m > tolerance(len(a.links), 10) || abs(len(a.links)-len(b.links)) > tolerance(len(a.links), 10) {
		problems = append(problems, fmt.Sprintf("links and images: %d of %d targets changed or missing", max(m, abs(len(a.links)-len(b.links))), len(a.links)))
	}
	if m := missing(a.urls, b.urls); m > tolerance(len(a.urls), 10) {
		problems = append(problems, fmt.Sprintf("URLs: %d of %d changed or missing", m, len(a.urls)))
	}
	if m := missing(a.shortcodes, b.shortcodes); m > 0 || len(b.shortcodes) != len(a.shortcodes) {
		problems = append(problems, fmt.Sprintf("shortcodes: %d of %d tags changed or missing", max(m, abs(len(a.shortcodes)-len(b.shortcodes))), len(a.shortcodes)))
	}
	if len(problems) > 0 {
		return &StructureError{Problems: problems}
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// unwrapFence removes a code fence wrapped around the whole answer
// ("```markdown … ```"), a common habit of chat models.
func unwrapFence(s string) string {
	t := strings.TrimSpace(s)
	first, rest, ok := strings.Cut(t, "\n")
	if !ok || !strings.HasPrefix(first, "```") || !strings.HasSuffix(rest, "```") {
		return s
	}
	switch strings.ToLower(strings.TrimSpace(strings.TrimPrefix(first, "```"))) {
	case "", "markdown", "md":
	default:
		return s
	}
	inner := strings.TrimSuffix(rest, "```")
	// A fence inside means the answer is a document with code, not a wrapper.
	if strings.Contains(inner, "\n```") {
		return s
	}
	return strings.TrimSpace(inner)
}
