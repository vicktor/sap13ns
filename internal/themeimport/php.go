package themeimport

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PHP in patterns. A block theme's patterns/*.php are PHP files that
// WordPress runs when it registers them
// (https://developer.wordpress.org/themes/patterns/using-php-in-patterns/).
// Most of that PHP only translates a text or builds the URL of an image of
// the theme:
//
//	<h2><?php esc_html_e( 'Our services', 'mytheme' ); ?></h2>
//	<img src="<?php echo esc_url( get_template_directory_uri() ); ?>/assets/images/hero.webp" alt=""/>
//
// The importer never runs PHP. It reads each <?php … ?> segment with a
// small lexer and evaluates it only when it is made of echo, print and
// printf statements over string literals, concatenation and the pure
// functions of phpFuncs (translation, escaping, the theme's URL). Any
// other segment (a variable, a loop, a call to anything else) is left out
// whole, and the report says where.

// Sentinels stand for values only the site knows; the template writer
// (html.go) turns them into template actions.
const (
	sentinelOpen  = "\uE0A0"
	sentinelClose = "\uE0A1"
)

func sentinel(key string) string { return sentinelOpen + key + sentinelClose }

var (
	sentinelThemeURI = sentinel("theme") // the theme's folder; a path follows
	sentinelYear     = sentinel("year")
	sentinelTitle    = sentinel("title")
	sentinelHome     = sentinel("home")
)

// stripSentinels removes the sentinel runes (and every other mark rune of
// the importer, html.go) from foreign text, so that a theme cannot write
// them itself.
func stripSentinels(s string) string { return stripMarks(s) }

var sentinelKeys = []string{"theme", "year", "title", "home"}

// keepSentinels keeps the whole sentinels the evaluator writes and drops
// any other sentinel rune: whatever a theme's text could still turn into
// one never reaches the template writer as one.
func keepSentinels(s string) string {
	if !strings.ContainsAny(s, sentinelOpen+sentinelClose+holeOpen+holeClose) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], sentinelOpen) {
			rest := s[i+len(sentinelOpen):]
			whole := false
			for _, k := range sentinelKeys {
				if strings.HasPrefix(rest, k+sentinelClose) {
					b.WriteString(sentinel(k))
					i += len(sentinel(k))
					whole = true
					break
				}
			}
			if !whole {
				i += len(sentinelOpen)
			}
			continue
		}
		if strings.HasPrefix(s[i:], sentinelClose) || strings.HasPrefix(s[i:], holeOpen) || strings.HasPrefix(s[i:], holeClose) {
			i += len(sentinelClose) // the mark runes are all three bytes long
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// pattern is a pattern file of the theme.
type pattern struct {
	File       string
	Slug       string
	Title      string
	BlockTypes []string
	Markup     string // the block markup, its PHP evaluated
	Skipped    []int  // lines of the PHP segments left out
	Invalid    bool   // not UTF-8: left out whole
}

// Bounds of the evaluation of one segment.
const (
	maxSegment    = 32 << 10
	maxPHPTokens  = 4000
	maxPHPDepth   = 32
	maxPHPOutput  = 64 << 10
	maxPatternOut = 512 << 10
	// maxPHPSegments is how many <?php … ?> segments one pattern may have
	// (the official themes' largest has a few dozen).
	maxPHPSegments = 1000
)

// parsePattern reads a pattern's header and evaluates its PHP.
func parsePattern(file, src string) *pattern {
	p := &pattern{File: file}
	if !utf8.ValidString(src) {
		p.Invalid = true
		return p
	}
	src = stripSentinels(src)
	var out strings.Builder
	vars := map[string]string{}
	pos := 0
	// The line of each segment, for the report, is counted from the last
	// one: counting from the start every time would be quadratic.
	line, counted := 1, 0
	segments := 0
	for pos < len(src) {
		i := strings.Index(src[pos:], "<?")
		if i < 0 {
			out.WriteString(src[pos:])
			break
		}
		out.WriteString(src[pos : pos+i])
		start := pos + i
		line += strings.Count(src[counted:start], "\n")
		counted = start
		if segments++; segments > maxPHPSegments {
			// Past the bound, the rest of the pattern is left out: its
			// text could hold more PHP that was not read.
			p.Skipped = append(p.Skipped, line)
			break
		}
		code := start + 2
		switch {
		case strings.HasPrefix(src[code:], "php"):
			code += 3
		case strings.HasPrefix(src[code:], "="):
			code++
		}
		end, next := phpSegmentEnd(src, code)
		seg := src[code:end]
		if p.Slug == "" && p.Title == "" {
			p.readHeader(seg)
		}
		echo := strings.HasPrefix(src[start:], "<?=")
		text, err := evalPHP(seg, echo, vars)
		if err != nil {
			p.Skipped = append(p.Skipped, line)
		} else {
			out.WriteString(keepSentinels(text))
		}
		if out.Len() > maxPatternOut {
			break
		}
		pos = next
	}
	p.Markup = keepSentinels(out.String())
	return p
}

// phpSegmentEnd finds the "?>" that ends a PHP segment starting at code,
// outside strings and block comments, and where the text resumes. A line
// comment ends at "?>" too, as in PHP.
func phpSegmentEnd(src string, code int) (end, next int) {
	i := code
	for i < len(src) {
		switch c := src[i]; {
		case c == '?' && i+1 < len(src) && src[i+1] == '>':
			next = i + 2
			if next < len(src) && src[next] == '\n' {
				next++ // PHP eats the newline after ?>
			}
			return i, next
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(src) && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			j := strings.Index(src[i+2:], "*/")
			if j < 0 {
				return len(src), len(src)
			}
			i += j + 4
		case c == '#' || c == '/' && i+1 < len(src) && src[i+1] == '/':
			for i < len(src) && src[i] != '\n' && !(src[i] == '?' && i+1 < len(src) && src[i+1] == '>') {
				i++
			}
		default:
			i++
		}
	}
	return len(src), len(src)
}

// readHeader reads the pattern's header comment: "Title: …", "Slug: …".
func (p *pattern) readHeader(seg string) {
	i := strings.Index(seg, "/*")
	j := strings.Index(seg, "*/")
	if i < 0 || j < i {
		return
	}
	for _, line := range strings.Split(seg[i+2:j], "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*"))
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "title":
			p.Title = v
		case "slug":
			p.Slug = v
		case "block types":
			for _, t := range strings.Split(v, ",") {
				if t = strings.TrimSpace(t); t != "" {
					p.BlockTypes = append(p.BlockTypes, t)
				}
			}
		}
	}
}

var errPHP = errors.New("unsupported PHP")

// evalPHP evaluates a segment that only prints literal text; echo is the
// short tag "<?=", which prints its expression. vars are the variables
// of the pattern, which hold text only.
func evalPHP(seg string, echo bool, vars map[string]string) (string, error) {
	if len(seg) > maxSegment {
		return "", errPHP
	}
	toks, err := lexPHP(seg)
	if err != nil {
		return "", err
	}
	if vars == nil {
		vars = map[string]string{}
	}
	e := &phpEval{toks: toks, vars: vars}
	if echo {
		v, err := e.expr(0)
		if err != nil {
			return "", err
		}
		e.out.WriteString(v)
		e.accept(";")
		if !e.done() {
			return "", errPHP
		}
		return e.out.String(), nil
	}
	for !e.done() {
		if err := e.statement(); err != nil {
			return "", err
		}
		if e.out.Len() > maxPHPOutput {
			return "", errPHP
		}
	}
	return e.out.String(), nil
}

type phpToken struct {
	kind byte // 'i' identifier, 'v' variable, 's' string, 'n' number, 'p' punctuation
	text string
}

func lexPHP(s string) ([]phpToken, error) {
	var toks []phpToken
	i := 0
	for i < len(s) {
		if len(toks) > maxPHPTokens {
			return nil, errPHP
		}
		c := s[i]
		switch {
		case isSpaceByte(c):
			i++
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return nil, errPHP
			}
			i += j + 4
		case c == '#' || c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '\'':
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != '\''; j++ {
				if s[j] == '\\' && j+1 < len(s) && (s[j+1] == '\'' || s[j+1] == '\\') {
					j++
				}
				b.WriteByte(s[j])
			}
			if j >= len(s) {
				return nil, errPHP
			}
			str, err := literal(b.String())
			if err != nil {
				return nil, err
			}
			toks = append(toks, phpToken{'s', str})
			i = j + 1
		case c == '"':
			str, n, err := doubleQuoted(s[i:])
			if err != nil {
				return nil, err
			}
			if str, err = literal(str); err != nil {
				return nil, err
			}
			toks = append(toks, phpToken{'s', str})
			i += n
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			toks = append(toks, phpToken{'n', s[i:j]})
			i = j
		case c == '_' || c == '\\' || c|0x20 >= 'a' && c|0x20 <= 'z':
			j := i
			for j < len(s) && (s[j] == '_' || s[j] == '\\' || s[j]|0x20 >= 'a' && s[j]|0x20 <= 'z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			toks = append(toks, phpToken{'i', strings.TrimPrefix(s[i:j], "\\")})
			i = j
		case c == '$' && i+1 < len(s) && (s[i+1] == '_' || s[i+1]|0x20 >= 'a' && s[i+1]|0x20 <= 'z'):
			j := i + 1
			for j < len(s) && (s[j] == '_' || s[j]|0x20 >= 'a' && s[j]|0x20 <= 'z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			toks = append(toks, phpToken{'v', s[i+1 : j]})
			i = j
		case c == '=' && (i+1 >= len(s) || s[i+1] != '=' && s[i+1] != '>'):
			toks = append(toks, phpToken{'p', "="})
			i++
		case strings.ContainsRune("(),;.", rune(c)):
			toks = append(toks, phpToken{'p', string(c)})
			i++
		default:
			// Variables, operators, arrays: none of it is evaluated.
			return nil, errPHP
		}
	}
	return toks, nil
}

// literal checks a decoded string literal: UTF-8 text, without sentinel
// runes, which only the evaluator's own functions write.
func literal(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errPHP
	}
	return stripSentinels(s), nil
}

// doubleQuoted reads a "…" string with PHP's escapes; a variable in it
// is not supported.
func doubleQuoted(s string) (string, int, error) {
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		switch {
		case c == '"':
			return b.String(), i + 1, nil
		case c == '$' && i+1 < len(s) && (s[i+1] == '{' || s[i+1] == '_' || s[i+1]|0x20 >= 'a' && s[i+1]|0x20 <= 'z'):
			return "", 0, errPHP
		case c == '\\' && i+1 < len(s):
			i++
			switch e := s[i]; e {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'v':
				b.WriteByte('\v')
			case 'f':
				b.WriteByte('\f')
			case '\\', '"', '$':
				b.WriteByte(e)
			default:
				b.WriteByte('\\')
				b.WriteByte(e)
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return "", 0, errPHP
}

type phpEval struct {
	toks []phpToken
	pos  int
	out  strings.Builder
	vars map[string]string
}

const (
	maxPHPVars     = 64
	maxPHPVarBytes = 64 << 10
)

func (e *phpEval) done() bool { return e.pos >= len(e.toks) }

func (e *phpEval) peek() phpToken {
	if e.done() {
		return phpToken{}
	}
	return e.toks[e.pos]
}

func (e *phpEval) accept(p string) bool {
	if t := e.peek(); t.kind == 'p' && t.text == p {
		e.pos++
		return true
	}
	return false
}

func (e *phpEval) statement() error {
	if e.accept(";") {
		return nil
	}
	t := e.peek()
	if t.kind == 'v' {
		// $name = expression; the value is text.
		e.pos++
		if !e.accept("=") {
			return errPHP
		}
		v, err := e.expr(0)
		if err != nil {
			return err
		}
		if _, ok := e.vars[t.text]; !ok && len(e.vars) >= maxPHPVars || len(v) > maxPHPVarBytes {
			return errPHP
		}
		e.vars[t.text] = v
		if !e.accept(";") && !e.done() {
			return errPHP
		}
		return nil
	}
	if t.kind != 'i' {
		return errPHP
	}
	switch strings.ToLower(t.text) {
	case "echo", "print":
		e.pos++
		for {
			v, err := e.expr(0)
			if err != nil {
				return err
			}
			e.out.WriteString(v)
			if !e.accept(",") {
				break
			}
		}
	default:
		name := strings.ToLower(t.text)
		f, ok := phpEchoFuncs[name]
		if !ok {
			return errPHP
		}
		e.pos++
		args, err := e.args(0)
		if err != nil {
			return err
		}
		v, err := f(args)
		if err != nil {
			return err
		}
		e.out.WriteString(v)
	}
	if !e.accept(";") && !e.done() {
		return errPHP
	}
	return nil
}

func (e *phpEval) expr(depth int) (string, error) {
	if depth > maxPHPDepth {
		return "", errPHP
	}
	v, err := e.term(depth)
	if err != nil {
		return "", err
	}
	for e.accept(".") {
		w, err := e.term(depth)
		if err != nil {
			return "", err
		}
		v += w
		if len(v) > maxPHPOutput {
			return "", errPHP
		}
	}
	return v, nil
}

func (e *phpEval) term(depth int) (string, error) {
	t := e.peek()
	switch t.kind {
	case 's', 'n':
		e.pos++
		return t.text, nil
	case 'v':
		v, ok := e.vars[t.text]
		if !ok {
			return "", errPHP
		}
		e.pos++
		return v, nil
	case 'p':
		if e.accept("(") {
			v, err := e.expr(depth + 1)
			if err != nil || !e.accept(")") {
				return "", errPHP
			}
			return v, nil
		}
	case 'i':
		f, ok := phpFuncs[strings.ToLower(t.text)]
		if !ok {
			return "", errPHP
		}
		e.pos++
		args, err := e.args(depth + 1)
		if err != nil {
			return "", err
		}
		return f(args)
	}
	return "", errPHP
}

func (e *phpEval) args(depth int) ([]string, error) {
	if !e.accept("(") {
		return nil, errPHP
	}
	var args []string
	if e.accept(")") {
		return args, nil
	}
	for {
		v, err := e.expr(depth)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
		if e.accept(")") {
			return args, nil
		}
		if !e.accept(",") {
			return nil, errPHP
		}
	}
}

type phpFunc func(args []string) (string, error)

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// escHTML escapes as WordPress's esc_html does: without encoding an
// entity twice ("&nbsp;" stays one). The result is HTML the template
// writer parses again, so an entity it keeps is decoded and escaped there.
func escHTML(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			if m := entityRe.FindString(s[i:]); m != "" {
				b.WriteString(m)
				i += len(m) - 1
			} else {
				b.WriteString("&amp;")
			}
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&#34;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var entityRe = regexp.MustCompile(`^&(?:[A-Za-z][A-Za-z0-9]{1,31}|#[0-9]{1,7}|#[xX][0-9A-Fa-f]{1,6});`)

// escURL keeps a URL only when it is one a theme can link to: a path, an
// anchor, the theme's own files, or http(s), mailto and tel.
func escURL(s string) string {
	t := strings.TrimSpace(s)
	if safeURL(t) {
		return t
	}
	return ""
}

var phpFuncs = map[string]phpFunc{
	"__":           func(a []string) (string, error) { return arg(a, 0), nil },
	"_x":           func(a []string) (string, error) { return arg(a, 0), nil },
	"esc_html__":   func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_attr__":   func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_html_x":   func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_attr_x":   func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_html":     func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_attr":     func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_textarea": func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_url":      func(a []string) (string, error) { return escURL(arg(a, 0)), nil },
	"esc_url_raw":  func(a []string) (string, error) { return escURL(arg(a, 0)), nil },
	"sanitize_url": func(a []string) (string, error) { return escURL(arg(a, 0)), nil },
	"wp_kses_post": func(a []string) (string, error) { return arg(a, 0), nil },
	"wp_kses_data": func(a []string) (string, error) { return arg(a, 0), nil },
	"wp_kses":      func(a []string) (string, error) { return arg(a, 0), nil },
	"trailingslashit": func(a []string) (string, error) {
		return strings.TrimRight(arg(a, 0), "/") + "/", nil
	},
	"_n": func(a []string) (string, error) {
		if n, err := strconv.ParseFloat(arg(a, 2), 64); err == nil && n != 1 {
			return arg(a, 1), nil
		}
		return arg(a, 0), nil
	},
	"get_template_directory_uri":   func([]string) (string, error) { return sentinelThemeURI, nil },
	"get_stylesheet_directory_uri": func([]string) (string, error) { return sentinelThemeURI, nil },
	"get_theme_file_uri": func(a []string) (string, error) {
		return sentinelThemeURI + "/" + strings.TrimLeft(arg(a, 0), "/"), nil
	},
	"get_parent_theme_file_uri": func(a []string) (string, error) {
		return sentinelThemeURI + "/" + strings.TrimLeft(arg(a, 0), "/"), nil
	},
	"home_url": func(a []string) (string, error) {
		p := arg(a, 0)
		if p == "" || p == "/" {
			return sentinelHome, nil
		}
		return "/" + strings.TrimLeft(p, "/"), nil
	},
	"get_bloginfo": func(a []string) (string, error) {
		if arg(a, 0) == "name" {
			return sentinelTitle, nil
		}
		return "", errPHP
	},
	"gmdate":    yearOnly,
	"date":      yearOnly,
	"date_i18n": yearOnly,
	"wp_date":   yearOnly,
	"sprintf": func(a []string) (string, error) {
		if len(a) == 0 {
			return "", errPHP
		}
		return phpSprintf(a[0], a[1:])
	},
}

// phpEchoFuncs print their result.
var phpEchoFuncs = map[string]phpFunc{
	"_e":          func(a []string) (string, error) { return arg(a, 0), nil },
	"_ex":         func(a []string) (string, error) { return arg(a, 0), nil },
	"esc_html_e":  func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_attr_e":  func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_html_ex": func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"esc_attr_ex": func(a []string) (string, error) { return escHTML(arg(a, 0)), nil },
	"bloginfo": func(a []string) (string, error) {
		if arg(a, 0) == "name" {
			return sentinelTitle, nil
		}
		return "", errPHP
	},
	"printf": func(a []string) (string, error) {
		if len(a) == 0 {
			return "", errPHP
		}
		return phpSprintf(a[0], a[1:])
	},
}

func yearOnly(a []string) (string, error) {
	if arg(a, 0) == "Y" {
		return sentinelYear, nil
	}
	return "", errPHP
}

// phpSprintf formats %s, %d, %1$s and %% with string arguments.
func phpSprintf(format string, args []string) (string, error) {
	var b strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(format) {
			return "", errPHP
		}
		if format[i] == '%' {
			b.WriteByte('%')
			continue
		}
		j := i
		for j < len(format) && format[j] >= '0' && format[j] <= '9' {
			j++
		}
		idx := -1
		if j < len(format) && format[j] == '$' && j > i {
			n, _ := strconv.Atoi(format[i:j])
			idx = n - 1
			i = j + 1
		}
		if i >= len(format) {
			return "", errPHP
		}
		if idx < 0 {
			idx = next
			next++
		}
		switch format[i] {
		case 's':
			if idx >= len(args) {
				return "", errPHP
			}
			b.WriteString(args[idx])
		case 'd':
			if idx >= len(args) {
				return "", errPHP
			}
			n, err := strconv.ParseFloat(args[idx], 64)
			if err != nil {
				return "", errPHP
			}
			fmt.Fprintf(&b, "%d", int64(n))
		default:
			return "", errPHP
		}
		if b.Len() > maxPHPOutput {
			return "", errPHP
		}
	}
	return b.String(), nil
}
