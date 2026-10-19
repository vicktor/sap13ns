package customize

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SanitizeCSS checks custom CSS and returns it trimmed, or an error naming
// the first problem and its line. It tokenizes the stylesheet the way
// browsers do (CSS Syntax Level 3), decoding escapes in names, so tricks
// such as "@\69mport" or "u\72l(" are caught. Rules:
//
//   - only the at-rules in allowedAtRules (no @import, @charset, @namespace);
//   - url() only for site paths ("/uploads/x.png"), paths relative to the
//     stylesheet, fragments and data: images: no other schemes, no
//     protocol-relative "//host", no backslashes or whitespace;
//   - no javascript: or vbscript: and no "</style" anywhere, escaped or not
//     (the stylesheet is never inlined, but it stays inert if it ever is);
//   - no functions that fetch or build resources from strings (image(),
//     image-set(), cross-fade(), element(), src()) and no expression();
//   - no legacy script hooks (behavior, -moz-binding);
//   - nothing that names the footer attribution (footer-meta), which the
//     licence requires to stay visible;
//   - balanced brackets, terminated strings and comments, no NUL bytes;
//   - at most MaxCSS bytes.
//
// The stylesheet is served as its own file and never inlined.
func SanitizeCSS(src string) (string, error) {
	src = strings.TrimSpace(strings.ReplaceAll(src, "\r\n", "\n"))
	if src == "" {
		return "", nil
	}
	if len(src) > MaxCSS {
		return "", fmt.Errorf("custom CSS is limited to %d KB", MaxCSS>>10)
	}
	if !utf8.ValidString(src) {
		return "", fmt.Errorf("custom CSS must be UTF-8 text")
	}
	if strings.ContainsRune(src, 0) {
		return "", fmt.Errorf("custom CSS contains a NUL byte")
	}
	if m := scriptText.FindString(src); m != "" {
		return "", fmt.Errorf("line %d: %s is not allowed", 1+strings.Count(src[:strings.Index(src, m)], "\n"), strings.ToLower(m))
	}
	t := &cssTokenizer{src: src, line: 1, rules: customRules}
	if err := t.check(); err != nil {
		return "", err
	}
	return src, nil
}

// customRules are the url() rules of the operator's custom CSS: a relative
// path or a fragment has neither scheme nor host, so it stays on this site,
// and a data: image never runs scripts when CSS draws it.
var customRules = CSSRules{RelativeURLs: true, DataImages: true, scriptText: true}

// scriptText is text that has no business in a stylesheet: script URLs and
// the end of an inline <style> element. The tokenizer also checks decoded
// strings and names for it, so escapes do not hide it.
var scriptText = regexp.MustCompile(`(?i)javascript:|vbscript:|</style`)

// CSSRules relax the rules of SanitizeCSS for a stylesheet that is not the
// operator's custom CSS: the compiled stylesheet of an uploaded theme.
type CSSRules struct {
	// RelativeURLs accepts url() paths relative to the stylesheet
	// ("fonts/x.woff2") and fragments ("#filter"), besides site paths.
	// Neither has a scheme or a host, so neither leaves the site.
	RelativeURLs bool
	// DataImages accepts url("data:image/…"), which compiled stylesheets
	// use for small icons. An image, SVG included, never runs scripts when
	// CSS draws it, and the CSP allows data: for images only.
	DataImages bool
	// FooterMeta lets the stylesheet name footer-meta: a theme styles the
	// credit line too (shared.css does). Hiding the credit still breaches
	// the licence; the operator answers for that, as with custom CSS.
	FooterMeta bool
	// scriptText rejects javascript:, vbscript: and </style in decoded
	// strings and names too (SanitizeCSS).
	scriptText bool
}

// CheckCSS applies the rules of SanitizeCSS, relaxed by r, to a whole
// stylesheet. It has no size limit: the caller sets one.
func CheckCSS(src string, r CSSRules) error {
	if !utf8.ValidString(src) {
		return fmt.Errorf("the stylesheet must be UTF-8 text")
	}
	if strings.ContainsRune(src, 0) {
		return fmt.Errorf("the stylesheet contains a NUL byte")
	}
	t := &cssTokenizer{src: src, line: 1, rules: r}
	return t.check()
}

var allowedAtRules = map[string]bool{
	"media": true, "supports": true, "container": true, "layer": true, "scope": true, "starting-style": true,
	"keyframes": true, "-webkit-keyframes": true, "font-face": true, "property": true, "counter-style": true,
	"font-feature-values": true, "font-palette-values": true, "page": true, "position-try": true, "view-transition": true,
	// inside @font-feature-values
	"swash": true, "annotation": true, "ornaments": true, "stylistic": true, "styleset": true,
	"character-variant": true, "historical-forms": true,
	// inside @page
	"top-left-corner": true, "top-left": true, "top-center": true, "top-right": true, "top-right-corner": true,
	"bottom-left-corner": true, "bottom-left": true, "bottom-center": true, "bottom-right": true, "bottom-right-corner": true,
	"left-top": true, "left-middle": true, "left-bottom": true, "right-top": true, "right-middle": true, "right-bottom": true,
}

var blockedFunctions = map[string]bool{
	"image": true, "image-set": true, "-webkit-image-set": true, "cross-fade": true, "-webkit-cross-fade": true,
	"element": true, "-moz-element": true, "src": true, "expression": true,
}

var blockedIdents = map[string]bool{"behavior": true, "-moz-binding": true}

type cssTokenizer struct {
	src   string
	pos   int
	line  int
	stack []byte
	rules CSSRules
}

func (t *cssTokenizer) errf(format string, args ...any) error {
	return fmt.Errorf("line %d: %s", t.line, fmt.Sprintf(format, args...))
}

func (t *cssTokenizer) peek(n int) byte {
	if t.pos+n < len(t.src) {
		return t.src[t.pos+n]
	}
	return 0
}

func (t *cssTokenizer) next() byte {
	c := t.src[t.pos]
	t.pos++
	if c == '\n' {
		t.line++
	}
	return c
}

func isNameStart(c byte) bool {
	return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') || c >= 0x80
}
func isName(c byte) bool         { return isNameStart(c) || c == '-' || (c >= '0' && c <= '9') }
func isHex(c byte) bool          { return (c >= '0' && c <= '9') || (c|0x20 >= 'a' && c|0x20 <= 'f') }
func isSpace(c byte) bool        { return c == ' ' || c == '\t' || c == '\n' || c == '\f' }
func validEscape(a, b byte) bool { return a == '\\' && b != '\n' && b != 0 }

// startsIdent reports whether an identifier starts at pos+off.
func (t *cssTokenizer) startsIdent(off int) bool {
	a, b, c := t.peek(off), t.peek(off+1), t.peek(off+2)
	switch {
	case a == '-':
		return isNameStart(b) || b == '-' || validEscape(b, c)
	case isNameStart(a):
		return true
	case a == '\\':
		return validEscape(a, b)
	}
	return false
}

// escape consumes an escape (after the backslash) and returns the rune.
func (t *cssTokenizer) escape() rune {
	if t.pos >= len(t.src) {
		return utf8.RuneError
	}
	if isHex(t.peek(0)) {
		start := t.pos
		for t.pos-start < 6 && t.pos < len(t.src) && isHex(t.peek(0)) {
			t.pos++
		}
		n, _ := strconv.ParseUint(t.src[start:t.pos], 16, 32)
		if t.pos < len(t.src) && isSpace(t.peek(0)) {
			t.next()
		}
		if n == 0 || n > utf8.MaxRune || (n >= 0xd800 && n <= 0xdfff) {
			return utf8.RuneError
		}
		return rune(n)
	}
	r, size := utf8.DecodeRuneInString(t.src[t.pos:])
	for i := 0; i < size; i++ {
		t.next()
	}
	return r
}

// name consumes an identifier sequence and returns it decoded.
func (t *cssTokenizer) name() string {
	var b strings.Builder
	for t.pos < len(t.src) {
		c := t.peek(0)
		switch {
		case isName(c):
			b.WriteByte(t.next())
		case validEscape(c, t.peek(1)):
			t.next()
			b.WriteRune(t.escape())
		default:
			return b.String()
		}
	}
	return b.String()
}

// str consumes a quoted string (after the quote) and returns it decoded.
func (t *cssTokenizer) str(q byte) (string, error) {
	var b strings.Builder
	for t.pos < len(t.src) {
		c := t.next()
		switch {
		case c == q:
			return b.String(), nil
		case c == '\n':
			return "", t.errf("unterminated string")
		case c == '\\':
			if t.pos >= len(t.src) {
				return b.String(), nil
			}
			if t.peek(0) == '\n' {
				t.next()
				continue
			}
			b.WriteRune(t.escape())
		default:
			b.WriteByte(c)
		}
	}
	return "", t.errf("unterminated string")
}

// checkURL enforces site paths only (with a theme's rules, also relative
// paths, fragments and data: images).
func (t *cssTokenizer) checkURL(u string) error {
	if u == "" {
		return t.errf("empty url()")
	}
	if t.rules.DataImages && len(u) > len("data:image/") && strings.EqualFold(u[:len("data:image/")], "data:image/") {
		return nil
	}
	for _, r := range u {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return t.errf("url() may not contain spaces, control characters or backslashes")
		}
	}
	switch {
	case strings.HasPrefix(u, "//"):
	case u[0] == '/':
		return nil
	case t.rules.RelativeURLs && !hasScheme(u):
		return nil
	}
	return t.errf("url() must point to a file on this site, such as /uploads/… (got %q)", truncate(u, 60))
}

// hasScheme reports whether a URL reference starts with a scheme: a colon
// before any slash, question mark or hash.
func hasScheme(u string) bool {
	end := strings.IndexAny(u, "/?#")
	if end < 0 {
		end = len(u)
	}
	return strings.Contains(u[:end], ":")
}

// unquotedURL consumes the rest of url( with an unquoted argument.
func (t *cssTokenizer) unquotedURL() (string, error) {
	for t.pos < len(t.src) && isSpace(t.peek(0)) {
		t.next()
	}
	var b strings.Builder
	for t.pos < len(t.src) {
		c := t.next()
		switch {
		case c == ')':
			return b.String(), nil
		case isSpace(c):
			for t.pos < len(t.src) && isSpace(t.peek(0)) {
				t.next()
			}
			if t.pos < len(t.src) && t.peek(0) == ')' {
				t.next()
				return b.String(), nil
			}
			return "", t.errf("malformed url()")
		case c == '"' || c == '\'' || c == '(' || c < 0x20 || c == 0x7f:
			return "", t.errf("malformed url()")
		case c == '\\':
			if !validEscape(c, t.peek(0)) {
				return "", t.errf("malformed url()")
			}
			b.WriteRune(t.escape())
		default:
			b.WriteByte(c)
		}
	}
	return "", t.errf("unterminated url()")
}

func (t *cssTokenizer) push(c byte) { t.stack = append(t.stack, c) }

func (t *cssTokenizer) pop(c byte) error {
	want := map[byte]byte{'}': '{', ')': '(', ']': '['}[c]
	if len(t.stack) == 0 || t.stack[len(t.stack)-1] != want {
		return t.errf("unexpected %q", c)
	}
	t.stack = t.stack[:len(t.stack)-1]
	return nil
}

// checkName applies the identifier rules to a decoded name.
func (t *cssTokenizer) checkName(n string) error {
	l := strings.ToLower(n)
	if blockedIdents[l] {
		return t.errf("%s is not allowed", l)
	}
	if !t.rules.FooterMeta && strings.Contains(l, "footer-meta") {
		return t.errf("the footer attribution (footer-meta) cannot be restyled")
	}
	return t.checkText(n)
}

// checkText applies scriptText to decoded text.
func (t *cssTokenizer) checkText(s string) error {
	if t.rules.scriptText {
		if m := scriptText.FindString(s); m != "" {
			return t.errf("%s is not allowed", strings.ToLower(m))
		}
	}
	return nil
}

func (t *cssTokenizer) check() error {
	// pendingURLFn is set after a url( function whose argument is a string.
	urlFn := false
	for t.pos < len(t.src) {
		c := t.peek(0)
		switch {
		case c == '/' && t.peek(1) == '*':
			end := strings.Index(t.src[t.pos+2:], "*/")
			if end < 0 {
				return t.errf("unterminated comment")
			}
			for i := 0; i < end+4; i++ {
				t.next()
			}
			continue
		case isSpace(c):
			t.next()
			continue
		case c == '"' || c == '\'':
			t.next()
			s, err := t.str(c)
			if err != nil {
				return err
			}
			if urlFn {
				if err := t.checkURL(s); err != nil {
					return err
				}
			}
			if !t.rules.FooterMeta && strings.Contains(strings.ToLower(s), "footer-meta") {
				return t.errf("the footer attribution (footer-meta) cannot be restyled")
			}
			if err := t.checkText(s); err != nil {
				return err
			}
		case c == '@':
			t.next()
			if !t.startsIdent(0) {
				continue
			}
			n := strings.ToLower(t.name())
			if !allowedAtRules[n] {
				return t.errf("@%s is not allowed", n)
			}
		case c == '#':
			t.next()
			if isName(t.peek(0)) || validEscape(t.peek(0), t.peek(1)) {
				if err := t.checkName(t.name()); err != nil {
					return err
				}
			}
		case c == '{' || c == '(' || c == '[':
			t.next()
			t.push(c)
		case c == '}' || c == ')' || c == ']':
			t.next()
			if err := t.pop(c); err != nil {
				return err
			}
		case c == '\\' && !validEscape(c, t.peek(1)):
			return t.errf("stray backslash")
		case t.startsIdent(0):
			n := t.name()
			if err := t.checkName(n); err != nil {
				return err
			}
			if t.pos < len(t.src) && t.peek(0) == '(' {
				t.next()
				fn := strings.ToLower(n)
				if blockedFunctions[fn] {
					return t.errf("%s() is not allowed", fn)
				}
				if fn == "url" {
					// url( followed by optional spaces and a quote is an
					// ordinary function with a string argument.
					p := t.pos
					for p < len(t.src) && isSpace(t.src[p]) {
						p++
					}
					if p < len(t.src) && (t.src[p] == '"' || t.src[p] == '\'') {
						t.push('(')
						urlFn = true
						continue
					}
					u, err := t.unquotedURL()
					if err != nil {
						return err
					}
					if err := t.checkText(u); err != nil {
						return err
					}
					if err := t.checkURL(u); err != nil {
						return err
					}
					continue
				}
				t.push('(')
			}
		default:
			t.next()
		}
		urlFn = false
	}
	if len(t.stack) > 0 {
		return t.errf("unclosed %q", t.stack[len(t.stack)-1])
	}
	return nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}
