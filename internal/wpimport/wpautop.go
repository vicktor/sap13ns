package wpimport

import (
	"regexp"
	"strconv"
	"strings"
)

// This file is a line-by-line port of wpautop() from WordPress 6.x
// (wp-includes/formatting.php), which turns double line breaks into
// paragraphs and single ones into <br />. Classic-editor content is stored
// without <p> tags and needs it before conversion. RE2 has neither
// look-behind nor back-references, so the two regexes that use them are
// implemented by hand with the same semantics.

const allBlocks = `(?:table|thead|tfoot|caption|col|colgroup|tbody|tr|td|th|div|dl|dd|dt|ul|ol|li|pre|form|map|area|blockquote|address|style|p|h[1-6]|hr|fieldset|legend|section|article|aside|hgroup|header|footer|nav|figure|figcaption|details|menu|summary)`

var (
	apBrBr           = regexp.MustCompile(`<br\s*/?>\s*<br\s*/?>`)
	apBlockOpen      = regexp.MustCompile(`(<` + allBlocks + `[\s/>])`)
	apBlockClose     = regexp.MustCompile(`(</` + allBlocks + `>)`)
	apHr             = regexp.MustCompile(`(<hr\s*?/?>)`)
	apOptionOpen     = regexp.MustCompile(`\s*<option`)
	apOptionClose    = regexp.MustCompile(`</option>\s*`)
	apObjectOpen     = regexp.MustCompile(`(<object[^>]*>)\s*`)
	apObjectClose    = regexp.MustCompile(`\s*</object>`)
	apParamEmbed     = regexp.MustCompile(`\s*(</?(?:param|embed)[^>]*>)\s*`)
	apMediaOpen      = regexp.MustCompile(`([<\[](?:audio|video)[^>\]]*[>\]])\s*`)
	apMediaClose     = regexp.MustCompile(`\s*([<\[]/(?:audio|video)[>\]])`)
	apSourceTrack    = regexp.MustCompile(`\s*(<(?:source|track)[^>]*>)\s*`)
	apFigcapOpen     = regexp.MustCompile(`\s*(<figcaption[^>]*>)`)
	apFigcapClose    = regexp.MustCompile(`</figcaption>\s*`)
	apManyBreaks     = regexp.MustCompile(`\n\n+`)
	apParaSplit      = regexp.MustCompile(`\n\s*\n`)
	apEmptyP         = regexp.MustCompile(`<p>\s*</p>`)
	apUnclosedP      = regexp.MustCompile(`<p>([^<]+)</(div|address|form)>`)
	apWrappedBlock   = regexp.MustCompile(`<p>\s*(</?` + allBlocks + `[^>]*>)\s*</p>`)
	apWrappedLi      = regexp.MustCompile(`<p>(<li.+?)</p>`)
	apPBlockquote    = regexp.MustCompile(`(?i)<p><blockquote([^>]*)>`)
	apPBeforeBlock   = regexp.MustCompile(`<p>\s*(</?` + allBlocks + `[^>]*>)`)
	apPAfterBlock    = regexp.MustCompile(`(</?` + allBlocks + `[^>]*>)\s*</p>`)
	apBrAfterBlock   = regexp.MustCompile(`(</?` + allBlocks + `[^>]*>)\s*<br />`)
	apBrBeforeBlock  = regexp.MustCompile(`<br />(\s*</?(?:p|li|div|dl|dd|dt|th|pre|td|ul|ol)[^>]*>)`)
	apFinalNewline   = regexp.MustCompile(`\n</p>(\n?)$`)
	apPrePlaceholder = regexp.MustCompile(`<pre wp-pre-tag-(\d+)></pre>`)
	apPreserve       = []*regexp.Regexp{
		regexp.MustCompile(`(?s)<script.*?</script>`),
		regexp.MustCompile(`(?s)<style.*?</style>`),
		regexp.MustCompile(`(?s)<svg.*?</svg>`),
		regexp.MustCompile(`(?s)<math.*?</math>`),
	}
)

// phpTrim is PHP's trim() default character set.
const phpTrim = " \t\n\r\x00\x0B"

// wpautop replaces double line breaks with paragraphs and, when br is set,
// remaining single line breaks with <br />.
func wpautop(text string, br bool) string {
	if strings.Trim(text, phpTrim) == "" {
		return ""
	}
	// Just to make things a little easier, pad the end.
	text += "\n"

	// Pre tags shouldn't be touched by autop: swap them for placeholders.
	var preTags []string
	if strings.Contains(text, "<pre") {
		parts := strings.Split(text, "</pre>")
		last := parts[len(parts)-1]
		var b strings.Builder
		for _, part := range parts[:len(parts)-1] {
			start := strings.Index(part, "<pre")
			if start < 0 { // Malformed HTML?
				b.WriteString(part)
				continue
			}
			name := "<pre wp-pre-tag-" + strconv.Itoa(len(preTags)) + "></pre>"
			preTags = append(preTags, name, part[start:]+"</pre>")
			b.WriteString(part[:start])
			b.WriteString(name)
		}
		b.WriteString(last)
		text = b.String()
	}

	// Change multiple <br>'s into two line breaks, which will turn into paragraphs.
	text = apBrBr.ReplaceAllString(text, "\n\n")

	// Add a double line break above block-level opening tags and below closing ones.
	text = apBlockOpen.ReplaceAllString(text, "\n\n${1}")
	text = apBlockClose.ReplaceAllString(text, "${1}\n\n")
	// Add a double line break after hr tags, which are self closing.
	text = apHr.ReplaceAllString(text, "${1}\n\n")

	// Standardize newline characters to "\n".
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	// Find newlines in all elements and add placeholders.
	text = replaceInHTMLTags(text, "\n", " <!-- wpnl --> ")

	// Collapse line breaks before and after <option> elements so they don't get autop'd.
	if strings.Contains(text, "<option") {
		text = apOptionOpen.ReplaceAllString(text, "<option")
		text = apOptionClose.ReplaceAllString(text, "</option>")
	}
	// Collapse line breaks inside <object> before <param> or <embed>.
	if strings.Contains(text, "</object>") {
		text = apObjectOpen.ReplaceAllString(text, "${1}")
		text = apObjectClose.ReplaceAllString(text, "</object>")
		text = apParamEmbed.ReplaceAllString(text, "${1}")
	}
	// Collapse line breaks inside <audio> and <video> elements, before and
	// after <source> and <track> elements.
	if strings.Contains(text, "<source") || strings.Contains(text, "<track") {
		text = apMediaOpen.ReplaceAllString(text, "${1}")
		text = apMediaClose.ReplaceAllString(text, "${1}")
		text = apSourceTrack.ReplaceAllString(text, "${1}")
	}
	// Collapse line breaks before and after <figcaption> elements.
	if strings.Contains(text, "<figcaption") {
		text = apFigcapOpen.ReplaceAllString(text, "${1}")
		text = apFigcapClose.ReplaceAllString(text, "</figcaption>")
	}

	// Remove more than two contiguous line breaks.
	text = apManyBreaks.ReplaceAllString(text, "\n\n")

	// Split up the contents into paragraphs and wrap every bit with a <p>.
	var b strings.Builder
	for _, para := range apParaSplit.Split(text, -1) {
		if para == "" {
			continue
		}
		b.WriteString("<p>")
		b.WriteString(strings.Trim(para, "\n"))
		b.WriteString("</p>\n")
	}
	text = b.String()

	// Under certain strange conditions it could create a P of entirely whitespace.
	text = apEmptyP.ReplaceAllString(text, "")
	// Add a closing <p> inside <div>, <address>, or <form> tag if missing.
	text = apUnclosedP.ReplaceAllString(text, "<p>${1}</p></${2}>")
	// If an opening or closing block element tag is wrapped in a <p>, unwrap it.
	text = apWrappedBlock.ReplaceAllString(text, "${1}")
	// In some cases <li> may get wrapped in <p>, fix them.
	text = apWrappedLi.ReplaceAllString(text, "${1}")
	// If a <blockquote> is wrapped with a <p>, move it inside the <blockquote>.
	text = apPBlockquote.ReplaceAllString(text, "<blockquote${1}><p>")
	text = strings.ReplaceAll(text, "</blockquote></p>", "</p></blockquote>")
	// If an opening or closing block element tag is preceded by an opening
	// <p> tag, or followed by a closing one, remove it.
	text = apPBeforeBlock.ReplaceAllString(text, "${1}")
	text = apPAfterBlock.ReplaceAllString(text, "${1}")

	if br {
		// Replace newlines that shouldn't be touched with a placeholder.
		for _, re := range apPreserve {
			text = re.ReplaceAllStringFunc(text, func(m string) string {
				return strings.ReplaceAll(m, "\n", "<WPPreserveNewline />")
			})
		}
		// Normalize <br>.
		text = strings.ReplaceAll(text, "<br>", "<br />")
		text = strings.ReplaceAll(text, "<br/>", "<br />")
		// Replace any new line characters that aren't preceded by a <br /> with a <br />.
		text = brNewlines(text)
		// Replace newline placeholders with newlines.
		text = strings.ReplaceAll(text, "<WPPreserveNewline />", "\n")
	}

	// If a <br /> tag is after an opening or closing block tag, remove it.
	text = apBrAfterBlock.ReplaceAllString(text, "${1}")
	// If a <br /> tag is before a subset of opening or closing block tags, remove it.
	text = apBrBeforeBlock.ReplaceAllString(text, "${1}")
	text = apFinalNewline.ReplaceAllString(text, "</p>${1}")

	// Replace placeholder <pre> tags with their original content (in one
	// pass; WordPress's str_replace loop is quadratic).
	if len(preTags) > 0 {
		text = apPrePlaceholder.ReplaceAllStringFunc(text, func(m string) string {
			i, err := strconv.Atoi(apPrePlaceholder.FindStringSubmatch(m)[1])
			if err != nil || 2*i+1 >= len(preTags) {
				return m
			}
			return preTags[2*i+1]
		})
	}
	// Restore newlines in all elements.
	if strings.Contains(text, "<!-- wpnl -->") {
		text = strings.ReplaceAll(text, " <!-- wpnl --> ", "\n")
		text = strings.ReplaceAll(text, "<!-- wpnl -->", "\n")
	}
	return text
}

func isPCRESpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// brNewlines implements preg_replace('|(?<!<br />)\s*\n|', "<br />\n", $text):
// each run of whitespace that contains a newline and does not start right
// after "<br />" is replaced, up to its last newline, by "<br />\n".
func brNewlines(s string) string {
	var b strings.Builder
	b.Grow(len(s) + len(s)/8)
	i := 0
	for i < len(s) {
		if !isPCRESpace(s[i]) || strings.HasSuffix(s[:i], "<br />") {
			b.WriteByte(s[i])
			i++
			continue
		}
		j, lastNL := i, -1
		for j < len(s) && isPCRESpace(s[j]) {
			if s[j] == '\n' {
				lastNL = j
			}
			j++
		}
		if lastNL < 0 {
			b.WriteString(s[i:j])
			i = j
			continue
		}
		b.WriteString("<br />\n")
		i = lastNL + 1
	}
	return b.String()
}

// htmlSplit is wp_html_split(): it separates HTML into text and markup
// (tags, comments, CDATA). Odd indexes of the result hold the markup.
func htmlSplit(s string) []string {
	var out []string
	textStart := 0
	i := 0
	for i < len(s) {
		if s[i] != '<' {
			i++
			continue
		}
		var end int
		rest := s[i+1:]
		switch {
		case strings.HasPrefix(rest, "!--"):
			// A comment ends at the first "-->" after "<!".
			if k := strings.Index(s[i+2:], "-->"); k >= 0 {
				end = i + 2 + k + 3
			} else {
				end = len(s)
			}
		case strings.HasPrefix(rest, "![CDATA["):
			if k := strings.Index(s[i+9:], "]]>"); k >= 0 {
				end = i + 9 + k + 3
			} else {
				end = len(s)
			}
		default:
			if k := strings.IndexByte(rest, '>'); k >= 0 {
				end = i + 1 + k + 1
			} else {
				end = len(s)
			}
		}
		out = append(out, s[textStart:i], s[i:end])
		textStart, i = end, end
	}
	return append(out, s[textStart:])
}

// replaceInHTMLTags is wp_replace_in_html_tags() for a single pair: needle is
// replaced only inside markup, never in text.
func replaceInHTMLTags(s, needle, repl string) string {
	if !strings.Contains(s, needle) {
		return s
	}
	parts := htmlSplit(s)
	for i := 1; i < len(parts); i += 2 {
		parts[i] = strings.ReplaceAll(parts[i], needle, repl)
	}
	return strings.Join(parts, "")
}
