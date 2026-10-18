package customize

import (
	"fmt"
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
//   - url() only for site paths ("/uploads/x.png"): no schemes, no
//     protocol-relative "//host", no backslashes or whitespace;
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
	t := &cssTokenizer{src: src, line: 1}
	if err := t.check(); err != nil {
		return "", err
	}
	return src, nil
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

// checkURL enforces site paths only.
func (t *cssTokenizer) checkURL(u string) error {
	if u == "" {
		return t.errf("empty url()")
	}
	for _, r := range u {
		if r <= 0x20 || r == 0x7f || r == '\\' {
			return t.errf("url() may not contain spaces, control characters or backslashes")
		}
	}
	if u[0] != '/' || strings.HasPrefix(u, "//") {
		return t.errf("url() must point to a file on this site, such as /uploads/… (got %q)", truncate(u, 60))
	}
	return nil
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
	if strings.Contains(l, "footer-meta") {
		return t.errf("the footer attribution (footer-meta) cannot be restyled")
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
			if strings.Contains(strings.ToLower(s), "footer-meta") {
				return t.errf("the footer attribution (footer-meta) cannot be restyled")
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
