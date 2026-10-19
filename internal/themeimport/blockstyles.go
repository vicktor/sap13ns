package themeimport

import (
	"regexp"
	"strings"
)

// A phpBlockStyle is a block style a theme registers from its PHP:
//
//	register_block_style( 'core/list', array(
//		'name'         => 'checkmark-list',
//		'inline_style' => 'ul.is-style-checkmark-list { … }',
//	) );
//
// Twenty Twenty-Four and Twenty Twenty-Five register theirs this way. The
// call is never run: the scanner below reads its string literals as data,
// and only when they are literals (a variable, a function call or a
// concatenation leaves the style out). The CSS then goes through cleanCSS
// like the theme's style.css.
type phpBlockStyle struct {
	Block, Name, CSS string
}

const (
	maxBlockStyles   = 64
	maxBlockStyleCSS = 16 << 10
)

var (
	blockNameRe  = regexp.MustCompile(`^[a-z0-9-]{1,40}/[a-z0-9-]{1,60}$`)
	styleNameRe  = regexp.MustCompile(`^[a-z0-9-]{1,60}$`)
	styleCallRe  = regexp.MustCompile(`\bregister_block_style\s*\(`)
	styleKeyRe   = regexp.MustCompile(`['"](name|inline_style)['"]\s*=>\s*`)
	arrayStartRe = regexp.MustCompile(`^\s*,\s*(?:array\s*\(|\[)`)
)

// readBlockStyles finds the register_block_style calls of a PHP file.
func readBlockStyles(src string) []phpBlockStyle {
	var out []phpBlockStyle
	for _, loc := range styleCallRe.FindAllStringIndex(src, maxBlockStyles) {
		rest := src[loc[1]:]
		block, n, ok := phpLiteralAt(rest)
		if !ok || !blockNameRe.MatchString(block) {
			continue
		}
		rest = rest[n:]
		m := arrayStartRe.FindStringIndex(rest)
		if m == nil {
			continue
		}
		args := rest[m[1]:]
		end := phpArrayEnd(args)
		if end < 0 {
			continue
		}
		args = args[:end]
		var s phpBlockStyle
		s.Block = block
		for _, k := range styleKeyRe.FindAllStringSubmatchIndex(args, 8) {
			v, _, ok := phpLiteralAt(args[k[1]:])
			if !ok {
				continue
			}
			switch args[k[2]:k[3]] {
			case "name":
				s.Name = v
			case "inline_style":
				s.CSS = v
			}
		}
		if styleNameRe.MatchString(s.Name) && strings.TrimSpace(s.CSS) != "" && len(s.CSS) <= maxBlockStyleCSS {
			out = append(out, s)
		}
	}
	return out
}

// phpLiteralAt reads the string literal at the start of s (after spaces),
// and how far it reaches; ok is false when s does not start with one.
func phpLiteralAt(s string) (string, int, bool) {
	i := 0
	for i < len(s) && isSpaceByte(s[i]) {
		i++
	}
	if i >= len(s) {
		return "", 0, false
	}
	switch s[i] {
	case '\'':
		var b strings.Builder
		j := i + 1
		for ; j < len(s) && s[j] != '\''; j++ {
			if s[j] == '\\' && j+1 < len(s) && (s[j+1] == '\'' || s[j+1] == '\\') {
				j++
			}
			b.WriteByte(s[j])
		}
		if j >= len(s) {
			return "", 0, false
		}
		str, err := literal(b.String())
		return str, j + 1, err == nil
	case '"':
		str, n, err := doubleQuoted(s[i:])
		if err != nil {
			return "", 0, false
		}
		str, err = literal(str)
		return str, i + n, err == nil
	}
	return "", 0, false
}

// phpArrayEnd is where the array whose opening was just read ends: the
// closing parenthesis or bracket at depth zero, outside strings and
// comments; -1 when there is none.
func phpArrayEnd(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' || c == '"':
			_, n, ok := phpLiteralAt(s[i:])
			if !ok {
				return -1
			}
			i += n - 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return -1
			}
			i += j + 3
		case c == '#' || c == '/' && i+1 < len(s) && s[i+1] == '/':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}
