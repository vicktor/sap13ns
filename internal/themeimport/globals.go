package themeimport

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// theme.json. WordPress turns a theme's settings into CSS custom
// properties and classes (https://developer.wordpress.org/themes/global-settings-and-styles/):
// a palette colour "base" becomes --wp--preset--color--base and the classes
// .has-base-color and .has-base-background-color, a font size becomes
// --wp--preset--font-size--{slug} (fluid ones a clamp()), the layout's
// sizes --wp--style--global--content-size and --wide-size. Its styles
// become rules for the body, the elements (links, headings, buttons) and
// each block. This file writes the same properties and rules, so that
// the theme's block markup, which names them, looks as the author made it.

type preset struct {
	slug, name, value string
	fluid             any // font sizes: false, true or {min, max}
}

type fontFace struct {
	family, weight, style, stretch, display, unicodeRange string
	src                                                   []string
}

type fontFamily struct {
	slug, name, family string
	faces              []fontFace
}

// wpTheme is what the converter knows of a theme's theme.json.
type wpTheme struct {
	json     map[string]any
	settings map[string]any
	styles   map[string]any

	palette, gradients, fontSizes, spacing, shadows []preset
	families                                        []fontFamily
	contentSize, wideSize                           string
	rootPaddingAware                                bool
	fluid                                           *fluidSettings
	custom                                          []decl

	variations []styleVariation // colour variations (styles/*.json)
	sections   []sectionStyle   // block style variations (styles/**.json with blockTypes)
}

// styleVariation is a colour variation of the theme: its palette over the
// theme's, and its own body colours.
type styleVariation struct {
	slug, title string
	palette     []preset
	bg, text    string // styles.color, "" to keep the theme's
}

// sectionStyle is a block style variation defined in its own file
// (WordPress 6.6 "section styles"): is-style-<slug> on the listed blocks.
type sectionStyle struct {
	slug       string
	blockTypes []string
	styles     map[string]any
}

// WordPress's own defaults (wp-includes/theme.json), used unless the
// theme turns them off: the default palette, font sizes and spacing scale,
// the block gap and the default link and button styles.
var (
	corePalette = []preset{
		{slug: "black", value: "#000000"}, {slug: "cyan-bluish-gray", value: "#abb8c3"}, {slug: "white", value: "#ffffff"},
		{slug: "pale-pink", value: "#f78da7"}, {slug: "vivid-red", value: "#cf2e2e"}, {slug: "luminous-vivid-orange", value: "#ff6900"},
		{slug: "luminous-vivid-amber", value: "#fcb900"}, {slug: "light-green-cyan", value: "#7bdcb5"}, {slug: "vivid-green-cyan", value: "#00d084"},
		{slug: "pale-cyan-blue", value: "#8ed1fc"}, {slug: "vivid-cyan-blue", value: "#0693e3"}, {slug: "vivid-purple", value: "#9b51e0"},
	}
	coreFontSizes = []preset{
		{slug: "small", value: "13px", fluid: false}, {slug: "medium", value: "20px", fluid: false},
		{slug: "large", value: "36px", fluid: false}, {slug: "x-large", value: "42px", fluid: false},
	}
	coreScale = map[string]any{"operator": "*", "increment": 1.5, "steps": 7.0, "mediumStep": 1.5, "unit": "rem"}
)

func (t *wpTheme) setting(keys ...string) any { return dig(t.settings, keys...) }

// enabled reads a boolean setting that is on unless false.
func (t *wpTheme) enabled(keys ...string) bool {
	b, ok := t.setting(keys...).(bool)
	return !ok || b
}

// loadThemeJSON reads theme.json (an empty one when the theme has none).
func loadThemeJSON(m map[string]any) *wpTheme {
	if m == nil {
		m = map[string]any{}
	}
	t := &wpTheme{json: m, settings: attrMap(m, "settings"), styles: attrMap(m, "styles")}
	if t.settings == nil {
		t.settings = map[string]any{}
	}
	if t.styles == nil {
		t.styles = map[string]any{}
	}
	t.palette = presets(t.setting("color", "palette"), "color")
	if t.enabled("color", "defaultPalette") {
		t.palette = mergePresets(corePalette, t.palette)
	}
	t.gradients = presets(t.setting("color", "gradients"), "gradient")
	t.fontSizes = presets(t.setting("typography", "fontSizes"), "size")
	if t.enabled("typography", "defaultFontSizes") {
		t.fontSizes = mergePresets(coreFontSizes, t.fontSizes)
	}
	t.spacing = presets(t.setting("spacing", "spacingSizes"), "size")
	if t.enabled("spacing", "defaultSpacingSizes") {
		scale, _ := t.setting("spacing", "spacingScale").(map[string]any)
		if scale == nil {
			scale = coreScale
		}
		t.spacing = mergePresets(spacingScale(scale), t.spacing)
	}
	t.shadows = presets(t.setting("shadow", "presets"), "shadow")
	for _, f := range listOf(t.setting("typography", "fontFamilies")) {
		ff := fontFamily{slug: attrString(f, "slug"), name: attrString(f, "name"), family: attrString(f, "fontFamily")}
		if ff.slug == "" || ff.family == "" {
			continue
		}
		for _, face := range listOf(f["fontFace"]) {
			fc := fontFace{family: attrString(face, "fontFamily"), weight: scalar(face["fontWeight"]),
				style: attrString(face, "fontStyle"), stretch: attrString(face, "fontStretch"),
				display: attrString(face, "fontDisplay"), unicodeRange: attrString(face, "unicodeRange")}
			switch src := face["src"].(type) {
			case string:
				fc.src = []string{src}
			case []any:
				for _, s := range src {
					if s, ok := s.(string); ok {
						fc.src = append(fc.src, s)
					}
				}
			}
			ff.faces = append(ff.faces, fc)
		}
		t.families = append(t.families, ff)
	}
	t.contentSize, _ = t.setting("layout", "contentSize").(string)
	t.wideSize, _ = t.setting("layout", "wideSize").(string)
	t.rootPaddingAware, _ = t.setting("useRootPaddingAwareAlignments").(bool)
	t.fluid = fluidFrom(t.setting("typography", "fluid"), t.wideSize)
	t.custom = customProps("--wp--custom", attrMap(t.settings, "custom"), 0)
	return t
}

// scalar writes a JSON string or number; anything else is "".
func scalar(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	}
	return ""
}

// fluidOf is a preset's fluid setting: true unless the theme says.
func fluidOf(v any) any {
	if v == nil {
		return true
	}
	return v
}

func listOf(v any) []map[string]any {
	l, _ := v.([]any)
	var out []map[string]any
	for _, e := range l {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// presets reads a list of presets: slug, name and the value under key.
func presets(v any, key string) []preset {
	var out []preset
	for _, m := range listOf(v) {
		p := preset{slug: cssIdent(attrString(m, "slug")), name: attrString(m, "name"), fluid: m["fluid"]}
		switch val := m[key].(type) {
		case string:
			p.value = val
		case fmt.Stringer:
			p.value = val.String()
		}
		if p.slug == "" || cssValue(p.value) == "" {
			continue
		}
		out = append(out, p)
		if len(out) >= 100 {
			break
		}
	}
	return out
}

// mergePresets puts over's presets over base's, by slug, in order.
func mergePresets(base, over []preset) []preset {
	out := slices.Clone(base)
	for _, p := range over {
		if i := slices.IndexFunc(out, func(q preset) bool { return q.slug == p.slug }); i >= 0 {
			out[i] = p
		} else {
			out = append(out, p)
		}
	}
	return out
}

// spacingScale generates spacing presets as WordPress does
// (WP_Theme_JSON::set_spacing_sizes): the medium step is "50", smaller
// steps count down by 10, larger ones up.
func spacingScale(s map[string]any) []preset {
	num := func(k string) float64 {
		switch v := s[k].(type) {
		case float64:
			return v
		case fmt.Stringer:
			f, _ := strconv.ParseFloat(v.String(), 64)
			return f
		}
		return 0
	}
	steps, medium, inc := int(num("steps")), num("mediumStep"), num("increment")
	op, _ := s["operator"].(string)
	unit, _ := s["unit"].(string)
	if steps <= 0 || steps > 10 || medium <= 0 || inc <= 0 || (op != "*" && op != "+") || !slices.Contains([]string{"px", "em", "rem", "%", "vw", "vh"}, unit) {
		return nil
	}
	size := func(v float64) string {
		return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) + unit
	}
	mid := int(math.Round(float64(steps) / 2))
	var below []preset
	cur, slug, remainder := medium, 40, 0
	for n := mid - 1; steps > 1 && slug > 0 && n > 0; n-- {
		switch {
		case op == "+":
			cur -= inc
		case inc > 1:
			cur /= inc
		default:
			cur *= inc
		}
		if cur <= 0 {
			remainder = n
			break
		}
		below = append(below, preset{slug: strconv.Itoa(slug), value: size(cur)})
		slug -= 10
	}
	slices.Reverse(below)
	out := append(below, preset{slug: "50", value: size(medium)})
	cur, slug = medium, 60
	for range steps - mid + remainder {
		switch {
		case op == "+":
			cur += inc
		case inc >= 1:
			cur *= inc
		default:
			cur /= inc
		}
		out = append(out, preset{slug: strconv.Itoa(slug), value: size(cur)})
		slug += 10
	}
	return out
}

// customProps flattens settings.custom into --wp--custom--a--b.
func customProps(prefix string, m map[string]any, depth int) []decl {
	if m == nil || depth > 5 {
		return nil
	}
	var out []decl
	for _, k := range sortedKeys(m) {
		name := prefix + "--" + cssIdent(k)
		switch v := m[k].(type) {
		case map[string]any:
			out = append(out, customProps(name, v, depth+1)...)
		case string:
			if s := cssValue(v); s != "" {
				out = append(out, decl{name, s})
			}
		case fmt.Stringer:
			out = append(out, decl{name, v.String()})
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Fluid typography (wp_get_typography_font_size_value): a size scales
// between a minimum and a maximum across the viewports, with clamp().

type fluidSettings struct {
	minViewport, maxViewport, minFont float64 // px
}

func fluidFrom(v any, wide string) *fluidSettings {
	f := &fluidSettings{minViewport: 320, maxViewport: 1600, minFont: 14}
	if px, ok := toPx(wide); ok {
		f.maxViewport = px
	}
	switch v := v.(type) {
	case bool:
		if !v {
			return nil
		}
	case map[string]any:
		if px, ok := toPx(attrString(v, "minViewportWidth")); ok {
			f.minViewport = px
		}
		if px, ok := toPx(attrString(v, "maxViewportWidth")); ok {
			f.maxViewport = px
		}
		if px, ok := toPx(attrString(v, "minFontSize")); ok {
			f.minFont = px
		}
	default:
		return nil
	}
	if f.maxViewport <= f.minViewport {
		return nil
	}
	return f
}

// toPx reads a size in px, rem or em (16px).
func toPx(s string) (float64, bool) {
	v, unit, ok := splitUnit(s)
	if !ok {
		return 0, false
	}
	if unit == "px" {
		return v, true
	}
	return v * 16, true
}

func splitUnit(s string) (float64, string, bool) {
	s = strings.TrimSpace(s)
	for _, unit := range []string{"px", "rem", "em"} {
		if strings.HasSuffix(s, unit) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, unit), 64)
			return v, unit, err == nil
		}
	}
	return 0, "", false
}

func fmtNum(v float64) string { return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64) }

// fluidValue computes a size's clamp(); fluid is the preset's own
// setting (true, false or {min, max}).
func (f *fluidSettings) fluidValue(size string, fluid any) string {
	if f == nil {
		return size
	}
	var minRaw, maxRaw string
	switch v := fluid.(type) {
	case bool:
		if !v {
			return size
		}
	case map[string]any:
		minRaw, maxRaw = attrString(v, "min"), attrString(v, "max")
	}
	pref, unit, ok := splitUnit(size)
	if !ok {
		return size
	}
	conv := func(px float64, to string) float64 {
		if to == "px" {
			return px
		}
		return px / 16
	}
	minLimit := conv(f.minFont, unit)
	if minRaw == "" && maxRaw == "" && pref <= minLimit {
		return size
	}
	if maxRaw == "" {
		maxRaw = fmtNum(pref) + unit
	}
	if minRaw == "" {
		prefPx := pref
		if unit != "px" {
			prefPx = pref * 16
		}
		factor := math.Min(math.Max(1-0.075*math.Log2(prefPx), 0.25), 0.75)
		calc := math.Round(pref*factor*1000) / 1000
		if calc <= minLimit {
			minRaw = fmtNum(minLimit) + unit
		} else {
			minRaw = fmtNum(calc) + unit
		}
	}
	_, minUnit, ok1 := splitUnit(minRaw)
	maxPx, ok2 := toPx(maxRaw)
	if !ok1 || !ok2 {
		return size
	}
	minPx, _ := toPx(minRaw)
	toUnit := func(px float64) float64 {
		if minUnit == "px" {
			return px
		}
		return px / 16
	}
	offset := fmtNum(toUnit(f.minViewport)/100) + minUnit
	factor := 100 * (toUnit(maxPx) - toUnit(minPx)) / (toUnit(f.maxViewport) - toUnit(f.minViewport))
	factor = math.Round(factor*1000) / 1000
	if factor == 0 {
		factor = 1
	}
	minRem := fmtNum(minPx/16) + "rem"
	return fmt.Sprintf("clamp(%s, %s + ((1vw - %s) * %s), %s)", minRaw, minRem, offset, fmtNum(factor), maxRaw)
}

// fluidSize makes a custom font size fluid, as WordPress does for the
// sizes of styles when fluid typography is on.
func (r *resolver) fluidSize(v string) string {
	if r.fluid == nil || strings.Contains(v, "(") {
		return v
	}
	return r.fluid.fluidValue(v, true)
}

// Selectors.

// blockSelectors are the root selectors of the core blocks whose styles
// do not apply to .wp-block-<name> (their block.json "selectors").
var blockSelectors = map[string]string{
	"core/paragraph": "p",
	"core/heading":   "h1,h2,h3,h4,h5,h6",
	"core/button":    ".wp-block-button .wp-block-button__link",
	"core/table":     ".wp-block-table > table",
	// The lists of other blocks (the posts of a query, the social links,
	// the menu) are not list blocks: a theme's list padding stays off them.
	"core/list": "ol" + notOtherLists + ", ul" + notOtherLists,
}

const notOtherLists = ":not(.wp-block-post-template, .wp-block-social-links, .wp-block-navigation__container, " +
	".wp-block-navigation__submenu-container, .wp-block-page-list, .wp-block-latest-posts, .wp-block-categories, .wp-block-comment-template)"

// featureSelectors are the block.json "selectors" of features that apply
// to an inner element rather than the block's root: an image block's
// border and shadow are on its img.
var featureSelectors = map[string]map[string]string{
	"core/image": {"border": ".wp-block-image img", "shadow": ".wp-block-image img"},
}

// featuresIn places a block's feature selectors inside outer ("body" for
// none), as descend does for the block's own selector.
func featuresIn(outer, name string) map[string]string {
	fs := featureSelectors[name]
	if fs == nil {
		return nil
	}
	out := make(map[string]string, len(fs))
	for k, s := range fs {
		if outer != "body" {
			s = descend(outer, s)
		}
		out[k] = s
	}
	return out
}

// featuresVariation is featuresIn for a style variation of the block.
func featuresVariation(fs map[string]string, name string) map[string]string {
	if fs == nil {
		return nil
	}
	out := make(map[string]string, len(fs))
	for k, s := range fs {
		out[k] = variationSelector(s, name)
	}
	return out
}

func blockSelector(name string) string {
	if s, ok := blockSelectors[name]; ok {
		return s
	}
	ns, n, ok := strings.Cut(name, "/")
	if !ok {
		return ""
	}
	if ns == "core" {
		return ".wp-block-" + cssIdent(n)
	}
	return ".wp-block-" + cssIdent(ns) + "-" + cssIdent(n)
}

var elementSelectors = map[string]string{
	"link":    "a:where(:not(.wp-element-button))",
	"heading": "h1, h2, h3, h4, h5, h6",
	"h1":      "h1", "h2": "h2", "h3": "h3", "h4": "h4", "h5": "h5", "h6": "h6",
	"button":  ".wp-element-button, .wp-block-button__link",
	"caption": ".wp-element-caption, .wp-block-audio figcaption, .wp-block-embed figcaption, .wp-block-gallery figcaption, .wp-block-image figcaption, .wp-block-table figcaption, .wp-block-video figcaption",
	"cite":    "cite",
}

var pseudoStates = []string{":hover", ":focus", ":focus-visible", ":active", ":visited"}

// descend combines every selector of outer with every one of inner.
func descend(outer, inner string) string {
	var out []string
	for _, o := range splitTop(outer, ',') {
		for _, i := range splitTop(inner, ',') {
			out = append(out, strings.TrimSpace(o)+" "+strings.TrimSpace(i))
		}
	}
	return strings.Join(out, ", ")
}

// withSuffix adds a pseudo-class or a class to every selector of a list.
func withSuffix(sel, suffix string) string {
	parts := splitTop(sel, ',')
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p) + suffix
	}
	return strings.Join(parts, ", ")
}

// variationSelector adds .is-style-<name> to the first compound selector
// of each selector of a list: ".wp-block-button .wp-block-button__link"
// becomes ".wp-block-button.is-style-outline .wp-block-button__link".
func variationSelector(sel, name string) string {
	parts := splitTop(sel, ',')
	for i, p := range parts {
		p = strings.TrimSpace(p)
		cut, depth := -1, 0
		for j := 0; j < len(p) && cut < 0; j++ {
			switch c := p[j]; {
			case c == '(':
				depth++
			case c == ')':
				depth--
			case depth == 0 && strings.IndexByte(" >+~", c) >= 0:
				cut = j
			}
		}
		if cut < 0 {
			parts[i] = p + ".is-style-" + name
		} else {
			parts[i] = p[:cut] + ".is-style-" + name + p[cut:]
		}
	}
	return strings.Join(parts, ", ")
}

func where(sel string) string { return ":root :where(" + sel + ")" }

// cssWriter collects rules, in order, without duplicates.
type cssWriter struct {
	b    strings.Builder
	seen map[string]bool
}

func (w *cssWriter) rule(sel string, ds []decl) {
	if len(ds) == 0 || sel == "" {
		return
	}
	r := sel + "{" + declString(ds) + "}"
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	if w.seen[r] {
		return
	}
	w.seen[r] = true
	w.b.WriteString(r + "\n")
}

func (w *cssWriter) raw(s string) {
	if s != "" {
		w.b.WriteString(s + "\n")
	}
}

// styles writes a style object for a selector: its declarations, its
// states, its elements, its nested blocks, its variations and its css.
// feat are the block's feature selectors (featureSelectors), or nil.
func (r *resolver) styles(w *cssWriter, sel string, style map[string]any, block bool, depth int, feat map[string]string) {
	if style == nil || depth > 4 {
		return
	}
	if len(feat) > 0 {
		own := make(map[string]any, len(style))
		for k, v := range style {
			own[k] = v
		}
		for _, k := range sortedKeys(feat) {
			if v, ok := style[k]; ok {
				delete(own, k)
				w.rule(where(feat[k]), r.styleDecls(map[string]any{k: v}))
			}
		}
		style = own
	}
	w.rule(where(sel), r.styleDecls(style))
	if block {
		r.blockGap(w, sel, dig(style, "spacing", "blockGap"))
	}
	for _, st := range pseudoStates {
		if s, ok := style[st].(map[string]any); ok {
			w.rule(where(withSuffix(sel, st)), r.styleDecls(s))
		}
	}
	if els := attrMap(style, "elements"); els != nil {
		for _, name := range sortedKeys(els) {
			es, ok := els[name].(map[string]any)
			elSel, known := elementSelectors[name]
			if !ok || !known {
				continue
			}
			full := elSel
			if sel != "body" {
				full = descend(sel, elSel)
			}
			r.styles(w, full, es, false, depth+1, nil)
		}
	}
	if blocks := attrMap(style, "blocks"); blocks != nil {
		for _, name := range sortedKeys(blocks) {
			bs, ok := blocks[name].(map[string]any)
			bsel := blockSelector(name)
			if !ok || bsel == "" {
				continue
			}
			full := bsel
			if sel != "body" {
				full = descend(sel, bsel)
			}
			r.styles(w, full, bs, true, depth+1, featuresIn(sel, name))
		}
	}
	if vars := attrMap(style, "variations"); vars != nil {
		for _, name := range sortedKeys(vars) {
			vs, ok := vars[name].(map[string]any)
			if !ok || cssIdent(name) == "" {
				continue
			}
			r.styles(w, variationSelector(sel, cssIdent(name)), vs, block, depth+1, featuresVariation(feat, cssIdent(name)))
		}
	}
	if css, ok := style["css"].(string); ok {
		scope := where(sel)
		if sel == "body" {
			scope = "body"
		}
		w.raw(r.foreign(scopeCSS(scope, css)))
	}
}

// blockGap writes a block's gap: between flex and grid items, or as the
// margin between the children of a flow layout.
func (r *resolver) blockGap(w *cssWriter, sel string, v any) {
	var row, col string
	switch g := v.(type) {
	case string:
		row = cssValue(g)
		col = row
	case map[string]any:
		row, col = r.value(g["top"]), r.value(g["left"])
	}
	if row == "" && col == "" {
		return
	}
	gap := []decl{}
	if row != "" {
		gap = append(gap, decl{"row-gap", row})
	}
	if col != "" {
		gap = append(gap, decl{"column-gap", col})
	}
	w.rule(where(withSuffix(sel, ".is-layout-flex")+", "+withSuffix(sel, ".is-layout-grid")), gap)
	if row != "" {
		w.rule(where(withSuffix(sel, ".is-layout-flow > *")+", "+withSuffix(sel, ".is-layout-constrained > *")), []decl{{"margin-block-start", row}})
		w.rule(where(withSuffix(sel, ".is-layout-flow > :first-child")+", "+withSuffix(sel, ".is-layout-constrained > :first-child")), []decl{{"margin-block-start", "0"}})
	}
}

// globalCSS writes the presets, the font faces and the styles of the
// theme. fontURL maps a font file of theme.json ("file:./assets/…") to
// its bundled place, or refuses it.
//
// Every "css" string of theme.json goes through cleanCSS (r.foreign),
// with its url()s mapped by urls; values go through cssValue. dropped
// counts the statements left out.
func (t *wpTheme) globalCSS(urls, fontURL func(string) (string, bool)) (css, faces string, dropped int) {
	r := &resolver{root: t.json, fluid: t.fluid, urls: urls}
	w := &cssWriter{}

	// The presets, on :root; the palette's colours are written by the
	// colour scheme (colours.go).
	var vars []decl
	for _, p := range t.gradients {
		vars = append(vars, decl{"--wp--preset--gradient--" + p.slug, cssValue(p.value)})
	}
	for _, p := range t.fontSizes {
		vars = append(vars, decl{"--wp--preset--font-size--" + p.slug, t.fluid.fluidValue(cssValue(p.value), fluidOf(p.fluid))})
	}
	for _, f := range t.families {
		if v := cssFontFamily(f.family); v != "" {
			vars = append(vars, decl{"--wp--preset--font-family--" + cssIdent(f.slug), v})
		}
	}
	for _, p := range t.spacing {
		vars = append(vars, decl{"--wp--preset--spacing--" + p.slug, cssValue(p.value)})
	}
	for _, p := range t.shadows {
		vars = append(vars, decl{"--wp--preset--shadow--" + p.slug, cssValue(p.value)})
	}
	if t.contentSize != "" {
		vars = append(vars, decl{"--wp--style--global--content-size", cssValue(t.contentSize)})
	} else {
		vars = append(vars, decl{"--wp--style--global--content-size", "100%"})
	}
	if t.wideSize != "" {
		vars = append(vars, decl{"--wp--style--global--wide-size", "var(--sap-content-width, " + cssValue(t.wideSize) + ")"})
	} else {
		vars = append(vars, decl{"--wp--style--global--wide-size", "var(--sap-content-width, 100%)"})
	}
	gap := r.value(dig(t.styles, "spacing", "blockGap"))
	if gap == "" {
		gap = "24px"
	}
	vars = append(vars, decl{"--wp--style--block-gap", gap})
	pad := r.box("padding", dig(t.styles, "spacing", "padding"))
	for _, side := range []string{"top", "right", "bottom", "left"} {
		v := "0px"
		for _, d := range pad {
			if d.prop == "padding-"+side {
				v = d.value
			}
		}
		vars = append(vars, decl{"--wp--style--root--padding-" + side, v})
	}
	vars = append(vars, t.custom...)
	w.rule(":root", vars)

	// The preset classes.
	for _, p := range t.palette {
		v := "var(--wp--preset--color--" + p.slug + ")"
		w.rule(".has-"+p.slug+"-color", []decl{{"color", v + " !important"}})
		w.rule(".has-"+p.slug+"-background-color", []decl{{"background-color", v + " !important"}})
		w.rule(".has-"+p.slug+"-border-color", []decl{{"border-color", v + " !important"}})
	}
	for _, p := range t.gradients {
		w.rule(".has-"+p.slug+"-gradient-background", []decl{{"background", "var(--wp--preset--gradient--" + p.slug + ") !important"}})
	}
	for _, p := range t.fontSizes {
		w.rule(".has-"+p.slug+"-font-size", []decl{{"font-size", "var(--wp--preset--font-size--" + p.slug + ") !important"}})
	}
	for _, f := range t.families {
		if s := cssIdent(f.slug); s != "" {
			w.rule(".has-"+s+"-font-family", []decl{{"font-family", "var(--wp--preset--font-family--" + s + ") !important"}})
		}
	}

	// The body: the customiser's fonts first, the theme's as fallbacks.
	body := r.styleDecls(map[string]any{"color": t.styles["color"], "typography": t.styles["typography"]})
	for i, d := range body {
		switch d.prop {
		case "font-family":
			body[i].value = "var(--sap-font-body, " + d.value + ")"
		case "background-color":
			body[i].value = "var(--background)"
		case "color":
			body[i].value = "var(--foreground)"
		}
	}
	if !t.rootPaddingAware {
		body = append(body, pad...)
	}
	w.rule("body", body)
	if t.rootPaddingAware {
		w.rule(".wp-site-blocks", []decl{{"padding-top", "var(--wp--style--root--padding-top)"}, {"padding-bottom", "var(--wp--style--root--padding-bottom)"}})
		w.rule(".has-global-padding", []decl{{"padding-right", "var(--wp--style--root--padding-right)"}, {"padding-left", "var(--wp--style--root--padding-left)"}})
		w.rule(".has-global-padding > .alignfull", []decl{{"margin-right", "calc(var(--wp--style--root--padding-right) * -1)"}, {"margin-left", "calc(var(--wp--style--root--padding-left) * -1)"}})
		w.rule(".has-global-padding :where(:not(.alignfull.is-layout-flow) > .has-global-padding:not(.wp-block-block, .alignfull))", []decl{{"padding-right", "0"}, {"padding-left", "0"}})
		w.rule(".has-global-padding :where(:not(.alignfull.is-layout-flow) > .has-global-padding:not(.wp-block-block, .alignfull)) > .alignfull", []decl{{"margin-left", "0"}, {"margin-right", "0"}})
		w.rule(".has-global-padding > .alignfull:where(:not(.has-global-padding):not(.is-layout-flex):not(.is-layout-grid)) > :where(.wp-block-columns, .wp-block-group, .wp-block-cover, .wp-block-image, .wp-block-heading, .wp-block-paragraph, p, h1, h2, h3, h4, h5, h6, ul, ol)",
			[]decl{{"padding-right", "var(--wp--style--root--padding-right)"}, {"padding-left", "var(--wp--style--root--padding-left)"}})
	}

	// WordPress's default element styles, then the theme's.
	w.rule(where(elementSelectors["link"]), []decl{{"text-decoration", "underline"}})
	w.rule(where(elementSelectors["button"]), []decl{{"background-color", "#32373c"}, {"color", "#fff"}, {"border-width", "0"},
		{"padding", "calc(0.667em + 2px) calc(1.333em + 2px)"}, {"font-size", "inherit"}, {"font-family", "inherit"}, {"line-height", "inherit"}, {"text-decoration", "none"}})
	w.rule(where(elementSelectors["caption"]), []decl{{"font-size", "13px"}, {"margin-top", "0.5em"}, {"margin-bottom", "1em"}})
	root := map[string]any{"elements": t.styles["elements"]}
	r.styles(w, "body", root, false, 0, nil)
	// The customiser's heading font wins over the theme's.
	w.rule("h1, h2, h3, h4, h5, h6", []decl{{"font-family", "var(--sap-font-heading, inherit)"}})

	// The blocks, then the section styles.
	r.styles(w, "body", map[string]any{"blocks": t.styles["blocks"]}, false, 0, nil)
	for _, s := range t.sections {
		for _, bt := range s.blockTypes {
			if sel := blockSelector(bt); sel != "" {
				r.styles(w, variationSelector(sel, s.slug), s.styles, true, 1, featuresVariation(featuresIn("body", bt), s.slug))
			}
		}
	}
	if css, ok := t.styles["css"].(string); ok {
		w.raw(r.foreign(css))
	}

	// The font faces.
	var f strings.Builder
	for _, fam := range t.families {
		for _, face := range fam.faces {
			var srcs []string
			for _, s := range face.src {
				u, ok := fontURL(s)
				if !ok {
					continue
				}
				format := "woff2"
				if strings.HasSuffix(u, ".woff") {
					format = "woff"
				}
				srcs = append(srcs, `url("`+u+`") format("`+format+`")`)
			}
			if len(srcs) == 0 {
				continue
			}
			name := cssFontName(cmp.Or(face.family, fam.name, fam.slug))
			if name == "" {
				continue
			}
			display := "fallback"
			if d := strings.ToLower(strings.TrimSpace(face.display)); slices.Contains([]string{"auto", "block", "swap", "fallback", "optional"}, d) {
				display = d
			}
			ds := []decl{{"font-family", name}, {"src", strings.Join(srcs, ", ")}, {"font-display", display}}
			for _, d := range []decl{{"font-weight", face.weight}, {"font-style", face.style}, {"font-stretch", face.stretch}, {"unicode-range", face.unicodeRange}} {
				if v := cssValue(d.value); v != "" {
					ds = append(ds, decl{d.prop, v})
				}
			}
			f.WriteString("@font-face{" + declString(ds) + "}\n")
		}
	}
	return w.b.String(), f.String(), r.dropped
}

// cssFontName quotes a font family name for @font-face, or returns ""
// when it is not a plain name.
func cssFontName(s string) string {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), `"'`))
	if !fontNameRe.MatchString(s) {
		return ""
	}
	return `"` + s + `"`
}

// usedFamilies lists the font families the theme's styles and markup
// name, so that only their files are bundled.
func (t *wpTheme) usedFamilies(markup string) map[string]bool {
	used := map[string]bool{}
	var walk func(v any, depth int)
	walk = func(v any, depth int) {
		if depth > 12 {
			return
		}
		switch v := v.(type) {
		case string:
			for _, f := range t.families {
				s := cssIdent(f.slug)
				if strings.Contains(v, "font-family|"+f.slug) || strings.Contains(v, "--wp--preset--font-family--"+s) {
					used[f.slug] = true
				}
			}
		case map[string]any:
			for _, k := range sortedKeys(v) {
				walk(v[k], depth+1)
			}
		case []any:
			for _, e := range v {
				walk(e, depth+1)
			}
		}
	}
	walk(t.styles, 0)
	for _, s := range t.sections {
		walk(s.styles, 0)
	}
	for _, f := range t.families {
		s := cssIdent(f.slug)
		if strings.Contains(markup, `"fontFamily":"`+f.slug+`"`) || strings.Contains(markup, "has-"+s+"-font-family") ||
			strings.Contains(markup, "--wp--preset--font-family--"+s) {
			used[f.slug] = true
		}
	}
	return used
}
