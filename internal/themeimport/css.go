package themeimport

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"sap13ns/internal/customize"
)

// CSS. Everything the importer writes into static/theme.css comes from
// three places: theme.json (settings and styles, globals.go), the inline
// styles of the theme's blocks, which become generated classes (no
// style="" can reach a template), and the theme's own stylesheets
// (style.css, the "css" strings of theme.json). The foreign CSS is
// cleaned statement by statement: @import goes, url() is rewritten to
// the theme's bundled files or dropped, and every statement must pass the
// checker of uploaded themes (customize.CheckCSS), or it is left out.

// themeCSS are the rules the upload checks apply to static/*.css.
var themeCSS = customize.CSSRules{RelativeURLs: true, DataImages: true, FooterMeta: true}

// foreignCSS is stricter: the theme's own CSS never names the credit.
var foreignCSS = customize.CSSRules{RelativeURLs: true, DataImages: true}

// decl is one CSS declaration.
type decl struct{ prop, value string }

func declString(ds []decl) string {
	var b strings.Builder
	for i, d := range ds {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(d.prop + ":" + d.value)
	}
	return b.String()
}

// kebab converts a slug or a key as WordPress's _wp_to_kebab_case does
// (lodash's kebabCase): words split at case changes and at digits.
func kebab(s string) string {
	var words []string
	var cur []rune
	kind := func(r rune) int {
		switch {
		case unicode.IsLower(r):
			return 1
		case unicode.IsUpper(r):
			return 2
		case unicode.IsDigit(r):
			return 3
		}
		return 0
	}
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		k := kind(r)
		if k == 0 {
			flush()
			continue
		}
		if len(cur) > 0 {
			pk := kind(cur[len(cur)-1])
			switch {
			case pk == 3 && k != 3, pk != 3 && k == 3:
				flush()
			case pk == 1 && k == 2:
				flush()
			case pk == 2 && k == 2 && i+1 < len(rs) && kind(rs[i+1]) == 1:
				flush() // "XMLHttp": "xml", "http"
			}
		}
		cur = append(cur, r)
	}
	flush()
	return strings.Join(words, "-")
}

// cssIdent keeps a slug safe inside a custom property or a class name.
func cssIdent(s string) string {
	s = kebab(s)
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// resolver turns theme.json values into CSS.
type resolver struct {
	root  map[string]any // theme.json, for {"ref": "styles.…"}
	fluid *fluidSettings // nil when fluid typography is off
	// urls maps a url() of the theme's CSS (relative to its root) to a
	// bundled file; nil refuses them all.
	urls    func(string) (string, bool)
	dropped int // statements of the theme's CSS left out
}

// foreign cleans CSS text of the theme (a "css" string of theme.json) as
// cleanCSS cleans its stylesheets, counting what it leaves out.
func (r *resolver) foreign(css string) string {
	urls := r.urls
	if urls == nil {
		urls = func(string) (string, bool) { return "", false }
	}
	out, n := cleanCSS(css, urls)
	r.dropped += n
	return out
}

// value converts a style value: "var:preset|color|base" becomes
// var(--wp--preset--color--base), {"ref": …} is followed, numbers are
// written as they are. "" when the value is unusable.
func (r *resolver) value(v any) string {
	return r.valueDepth(v, 0)
}

func (r *resolver) valueDepth(v any, depth int) string {
	switch v := v.(type) {
	case string:
		return cssValue(v)
	case fmt.Stringer: // json.Number
		return v.String()
	case map[string]any:
		if ref, ok := v["ref"].(string); ok && depth < 5 && r.root != nil {
			keys := strings.Split(ref, ".")
			return r.valueDepth(dig(r.root, keys...), depth+1)
		}
	}
	return ""
}

// safeFunctions are the CSS functions a theme.json value may use: none of
// them loads anything. url(), image-set() and the rest are left out, so a
// value never names a resource; the theme's files reach the stylesheet
// only through cleanCSS, which rewrites every url() to a bundled file.
var safeFunctions = map[string]bool{
	"var": true, "calc": true, "clamp": true, "min": true, "max": true,
	"rgb": true, "rgba": true, "hsl": true, "hsla": true, "hwb": true, "lab": true, "lch": true,
	"oklab": true, "oklch": true, "color-mix": true,
	"linear-gradient": true, "radial-gradient": true, "conic-gradient": true,
	"repeating-linear-gradient": true, "repeating-radial-gradient": true, "repeating-conic-gradient": true,
	"cubic-bezier": true, "minmax": true, "repeat": true, "fit-content": true,
}

const maxValue = 2000

// cssValue rewrites the var: references of a theme.json string, and
// refuses ("") any value that is not plain: one that could end the
// declaration or the rule ({ } ; < > \), hold a string or a comment, mark
// itself !important, or call a function other than safeFunctions.
func cssValue(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "var:") {
		parts := strings.Split(strings.TrimPrefix(s, "var:"), "|")
		if len(parts) > 6 {
			return ""
		}
		for i, p := range parts {
			parts[i] = cssIdent(p)
			if parts[i] == "" {
				return ""
			}
		}
		return "var(--wp--" + strings.Join(parts, "--") + ")"
	}
	if len(s) > maxValue || strings.ContainsAny(s, "{};<>\\\"'`!@") || strings.Contains(s, "/*") ||
		strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			j := i
			for j > 0 && (isNameByte(s[j-1]|0x20) || s[j-1] >= 'A' && s[j-1] <= 'Z') {
				j--
			}
			if name := strings.ToLower(s[j:i]); name != "" && !safeFunctions[name] {
				return ""
			}
			depth++
		case ')':
			if depth--; depth < 0 {
				return ""
			}
		}
	}
	if depth != 0 {
		return ""
	}
	return s
}

var fontNameRe = regexp.MustCompile(`^-?[A-Za-z0-9][A-Za-z0-9 ._-]{0,59}$`)

// cssFontFamily checks a font-family list: names, quoted or not, made of
// letters, digits, spaces, dots, dashes and underscores, and var()s. It
// returns the list with double quotes, or "".
func cssFontFamily(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "var:") {
		return cssValue(s)
	}
	parts := strings.Split(s, ",")
	if len(parts) > 12 {
		return ""
	}
	for i, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case len(p) >= 2 && (p[0] == '"' || p[0] == '\'') && p[len(p)-1] == p[0]:
			inner := strings.TrimSpace(p[1 : len(p)-1])
			if !fontNameRe.MatchString(inner) {
				return ""
			}
			parts[i] = `"` + inner + `"`
		case strings.HasPrefix(p, "var("):
			if parts[i] = cssValue(p); parts[i] == "" {
				return ""
			}
		case fontNameRe.MatchString(p):
			parts[i] = p
		default:
			return ""
		}
	}
	return strings.Join(parts, ", ")
}

// balanced reports whether the braces and parentheses of a CSS text pair
// up, outside strings: a "css" string of theme.json that is nested under
// its selector cannot close that rule and write others.
func balanced(s string) bool {
	var stack []byte
	braces := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) || s[j] != c {
				return false
			}
			i = j
		case '{', '(', '[':
			stack = append(stack, c)
			if c == '{' {
				if braces++; braces > maxCSSNest {
					return false
				}
			}
			if len(stack) > 4*maxCSSNest {
				return false
			}
		case '}', ')', ']':
			if c == '}' {
				braces--
			}
			want := map[byte]byte{'}': '{', ')': '(', ']': '['}[c]
			if len(stack) == 0 || stack[len(stack)-1] != want {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}
	return len(stack) == 0
}

// cssDepth is how deep the blocks of a CSS text nest, outside strings.
func cssDepth(s string) int {
	depth, most := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '{':
			if depth++; depth > most {
				most = depth
			}
		case '}':
			depth--
		}
	}
	return most
}

// styleDecls converts a theme.json style object (or a block's "style"
// attribute) into declarations. blockGap and the layout are the caller's.
func (r *resolver) styleDecls(style map[string]any) []decl {
	if style == nil {
		return nil
	}
	var out []decl
	add := func(prop string, v any) {
		if s := r.value(v); s != "" {
			out = append(out, decl{prop, s})
		}
	}
	if c := attrMap(style, "color"); c != nil {
		add("color", c["text"])
		if g := r.value(c["gradient"]); g != "" {
			out = append(out, decl{"background", g})
		} else {
			add("background-color", c["background"])
		}
	}
	if t := attrMap(style, "typography"); t != nil {
		if s, ok := t["fontFamily"].(string); ok {
			if f := cssFontFamily(s); f != "" {
				out = append(out, decl{"font-family", f})
			}
		}
		if fs := r.value(t["fontSize"]); fs != "" {
			out = append(out, decl{"font-size", r.fluidSize(fs)})
		}
		add("font-style", t["fontStyle"])
		add("font-weight", t["fontWeight"])
		add("letter-spacing", t["letterSpacing"])
		add("line-height", t["lineHeight"])
		add("text-decoration", t["textDecoration"])
		add("text-transform", t["textTransform"])
		add("text-align", t["textAlign"])
		add("writing-mode", t["writingMode"])
		add("column-count", t["textColumns"])
	}
	if sp := attrMap(style, "spacing"); sp != nil {
		out = append(out, r.box("padding", sp["padding"])...)
		out = append(out, r.box("margin", sp["margin"])...)
	}
	if b := attrMap(style, "border"); b != nil {
		out = append(out, r.border(b)...)
	}
	if d := attrMap(style, "dimensions"); d != nil {
		add("min-height", d["minHeight"])
		add("aspect-ratio", d["aspectRatio"])
	}
	add("box-shadow", style["shadow"])
	if o := attrMap(style, "outline"); o != nil {
		add("outline-color", o["color"])
		add("outline-offset", o["offset"])
		add("outline-style", o["style"])
		add("outline-width", o["width"])
	}
	if bg := attrMap(style, "background"); bg != nil {
		add("background-size", bg["backgroundSize"])
		add("background-position", bg["backgroundPosition"])
		add("background-repeat", bg["backgroundRepeat"])
		add("background-attachment", bg["backgroundAttachment"])
	}
	return out
}

// box reads padding or margin: a string, or top/right/bottom/left.
func (r *resolver) box(prop string, v any) []decl {
	switch v := v.(type) {
	case string:
		if s := cssValue(v); s != "" {
			return []decl{{prop, s}}
		}
	case map[string]any:
		var out []decl
		for _, side := range []string{"top", "right", "bottom", "left"} {
			if s := r.value(v[side]); s != "" {
				out = append(out, decl{prop + "-" + side, s})
			}
		}
		return out
	}
	return nil
}

func (r *resolver) border(b map[string]any) []decl {
	var out []decl
	add := func(prop string, v any) bool {
		if s := r.value(v); s != "" {
			out = append(out, decl{prop, s})
			return true
		}
		return false
	}
	c := add("border-color", b["color"])
	w := add("border-width", b["width"])
	s := add("border-style", b["style"])
	if (c || w) && !s {
		out = append(out, decl{"border-style", "solid"})
	}
	switch rad := b["radius"].(type) {
	case string:
		add("border-radius", rad)
	case map[string]any:
		for k, prop := range map[string]string{"topLeft": "border-top-left-radius", "topRight": "border-top-right-radius",
			"bottomLeft": "border-bottom-left-radius", "bottomRight": "border-bottom-right-radius"} {
			add(prop, rad[k])
		}
	}
	for _, side := range []string{"top", "right", "bottom", "left"} {
		sb := attrMap(b, side)
		if sb == nil {
			continue
		}
		c := add("border-"+side+"-color", sb["color"])
		w := add("border-"+side+"-width", sb["width"])
		if !add("border-"+side+"-style", sb["style"]) && (c || w) {
			out = append(out, decl{"border-" + side + "-style", "solid"})
		}
	}
	slices.SortStableFunc(out, func(a, b decl) int { return strings.Compare(a.prop, b.prop) })
	return out
}

// Foreign CSS.

// cleanCSS cleans a stylesheet of the theme. resolve maps a url() of it
// to a bundled file ("fonts/x.woff2", relative to static/theme.css) or a
// data: image, or refuses it. It returns the CSS kept and how many
// statements were left out.
func cleanCSS(src string, resolve func(string) (string, bool)) (string, int) {
	src = stripCSSComments(stripSentinels(src))
	var out strings.Builder
	dropped := 0
	for _, st := range cssStatements(src) {
		st = strings.TrimSpace(st)
		if st == "" {
			continue
		}
		lower := strings.ToLower(st)
		// A statement is a rule or an at-rule: text that is neither (what
		// a parser would skip up to the next block, taking a rule with it)
		// is left out, and so is anything that reads as the end of an HTML
		// element, whatever the stylesheet ends up inside.
		complete := strings.HasSuffix(st, "}") || st[0] == '@' && strings.HasSuffix(st, ";")
		if !complete || cssDepth(st) > maxCSSNest || strings.Contains(st, "</") || strings.Contains(st, "<!") ||
			strings.HasPrefix(lower, "@import") || strings.HasPrefix(lower, "@charset") || strings.HasPrefix(lower, "@namespace") ||
			strings.Contains(lower, "footer-meta") {
			dropped++
			continue
		}
		st = rewriteURLs(st, resolve)
		if err := customize.CheckCSS(st, foreignCSS); err != nil {
			dropped++
			continue
		}
		out.WriteString(st)
		out.WriteByte('\n')
	}
	return out.String(), dropped
}

// stripCSSComments removes /* … */ outside strings.
func stripCSSComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(s))
			b.WriteString(s[i:j])
			i = j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return b.String()
			}
			i += j + 4
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// cssStatements splits a stylesheet into its top-level statements: rules
// and at-rules with their blocks, or at-rules ending in ";". Unbalanced
// text at the end is one last statement, which the checker refuses.
func cssStatements(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'':
			j := i + 1
			for j < len(s) && s[j] != c && s[j] != '\n' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				out = append(out, s[start:i+1])
				start = i + 1
			} else if depth < 0 {
				depth = 0
				start = i + 1
			}
		case ';':
			if depth == 0 {
				out = append(out, s[start:i+1])
				start = i + 1
			}
		}
	}
	if strings.TrimSpace(s[start:]) != "" {
		out = append(out, s[start:])
	}
	return out
}

// rewriteURLs replaces every url(…) of a statement: by the bundled file
// resolve gives, or by "none".
func rewriteURLs(st string, resolve func(string) (string, bool)) string {
	var b strings.Builder
	lower := strings.ToLower(st)
	i := 0
	for {
		j := strings.Index(lower[i:], "url(")
		if j < 0 {
			b.WriteString(st[i:])
			return b.String()
		}
		j += i
		// Only a url( that starts a token: not "myurl(".
		if j > 0 && (isNameByte(lower[j-1]) || lower[j-1] == '\\') {
			b.WriteString(st[i : j+4])
			i = j + 4
			continue
		}
		b.WriteString(st[i:j])
		end := strings.IndexByte(st[j:], ')')
		if end < 0 {
			b.WriteString("none")
			return b.String()
		}
		raw := strings.TrimSpace(st[j+4 : j+end])
		raw = strings.Trim(raw, `"'`)
		if u, ok := resolve(raw); ok && !strings.ContainsAny(u, `"\`+"\n") {
			b.WriteString(`url("` + u + `")`)
		} else {
			b.WriteString("none")
		}
		i = j + end + 1
	}
}

func isNameByte(c byte) bool {
	return c == '-' || c == '_' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c >= 0x80
}

// Dark variants.

// modeRules writes, for every rule of css whose declarations hold a
// literal colour, the same rule for the other mode with the colours
// mapped by f: dark mode (html.dark) when dark, else light mode (a theme
// whose own colours are dark). Rules inside @media and @supports are
// followed; others are left.
func modeRules(css string, f func(rgba) rgba, dark bool) string {
	return modeRulesIn(css, f, dark, 0)
}

// maxCSSNest is how deep blocks may nest in a stylesheet of the theme (a
// rule in @media in @supports in @layer, and a little more): cleanCSS
// drops a deeper statement, balanced refuses a deeper text, and modeRules
// stops there.
const maxCSSNest = 6

func modeRulesIn(css string, f func(rgba) rgba, dark bool, depth int) string {
	if depth >= maxCSSNest {
		return ""
	}
	var out strings.Builder
	for _, st := range cssStatements(css) {
		st = strings.TrimSpace(st)
		open := strings.IndexByte(st, '{')
		if open < 0 || !strings.HasSuffix(st, "}") {
			continue
		}
		head := strings.TrimSpace(st[:open])
		body := st[open+1 : len(st)-1]
		lower := strings.ToLower(head)
		switch {
		case strings.HasPrefix(lower, "@media"), strings.HasPrefix(lower, "@supports"), strings.HasPrefix(lower, "@layer"):
			if inner := modeRulesIn(body, f, dark, depth+1); inner != "" {
				out.WriteString(head + "{" + inner + "}\n")
			}
		case strings.HasPrefix(lower, "@"), strings.Contains(lower, ".dark"):
		default:
			var ds []string
			for _, d := range splitDecls(body) {
				prop, val, ok := strings.Cut(d, ":")
				if !ok || strings.Contains(val, "{") {
					continue
				}
				prop = strings.TrimSpace(prop)
				if strings.HasPrefix(prop, "--") && !colourProp(prop) {
					continue
				}
				if !colourProp(prop) && !strings.HasPrefix(prop, "--") {
					continue
				}
				nv := mapColours(val, f)
				if nv != val {
					ds = append(ds, prop+":"+strings.TrimSpace(nv))
				}
			}
			if len(ds) > 0 {
				out.WriteString(modeSelector(head, dark) + "{" + strings.Join(ds, ";") + "}\n")
			}
		}
	}
	return out.String()
}

// colourProp: properties whose colours dark mode changes.
func colourProp(p string) bool {
	p = strings.ToLower(p)
	return strings.Contains(p, "color") || p == "background" || p == "background-image" || p == "fill" || p == "stroke" ||
		p == "box-shadow" || p == "text-shadow" || p == "border" || strings.HasPrefix(p, "border-") && !strings.HasSuffix(p, "radius") &&
		!strings.HasSuffix(p, "width") && !strings.HasSuffix(p, "style") || p == "outline" || strings.HasPrefix(p, "--wp--preset--color") ||
		strings.HasPrefix(p, "--wp--preset--gradient")
}

// splitDecls splits a declaration block at the ";" outside strings and
// parentheses.
func splitDecls(body string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '"', '\'':
			j := i + 1
			for j < len(body) && body[j] != c {
				if body[j] == '\\' {
					j++
				}
				j++
			}
			i = j
		case '(':
			depth++
		case ')':
			depth--
		case ';':
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, body[start:])
	return out
}

// modeSelector scopes a selector list to dark mode (html.dark), or to
// light mode (html:not(.dark)).
func modeSelector(sel string, dark bool) string {
	mode := ".dark"
	if !dark {
		mode = ":not(.dark)"
	}
	parts := splitTop(sel, ',')
	for i, p := range parts {
		p = strings.TrimSpace(p)
		switch {
		case strings.HasPrefix(p, ":root"):
			parts[i] = ":root" + mode + p[len(":root"):]
		case p == "html" || strings.HasPrefix(p, "html ") || strings.HasPrefix(p, "html.") || strings.HasPrefix(p, "html:"):
			parts[i] = "html" + mode + p[len("html"):]
		default:
			parts[i] = ":root" + mode + " " + p
		}
	}
	return strings.Join(parts, ",")
}

// splitTop splits s at sep outside parentheses and brackets.
func splitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// scopeCSS nests the "css" string of theme.json under a selector, as
// WordPress does: "& a {…}" and plain declarations apply to it. Native
// CSS nesting does the rest. A string whose braces do not pair up is
// left out.
func scopeCSS(selector, css string) string {
	css = strings.TrimSpace(stripCSSComments(css))
	if css == "" || !balanced(css) {
		return ""
	}
	return selector + "{" + css + "}"
}
