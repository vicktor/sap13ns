package themeimport

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"
)

// Block supports. WordPress adds classes and styles to a block when it
// renders it, from its attributes: the layout of its children (flow,
// constrained, flex, grid) and their gap, the colours and font size of
// dynamic blocks, and the colours of the links inside it. The saved HTML
// of a static block has the colour classes and inline styles already, but
// not these; this file computes them, as wp-includes/block-supports does,
// with generated classes (wpl-…, wps-…, wpe-…) in theme.css.

// classFor returns the generated class of a set of declarations, writing
// its rule the first time: .wpi-1a2b3c4d.wpi-1a2b3c4d{…}. The doubled
// class outweighs the block library's rules, as the inline style it
// replaces did.
func (c *converter) classFor(prefix string, ds []decl) string {
	if len(ds) == 0 {
		return ""
	}
	return c.instance(prefix, []rule{{"", ds}}, true)
}

// rule is a rule of a generated class: its selector after the class.
type rule struct {
	suffix string
	ds     []decl
}

// instance returns the generated class of a set of rules.
func (c *converter) instance(prefix string, rules []rule, double bool) string {
	var key strings.Builder
	for _, r := range rules {
		key.WriteString(r.suffix + "{" + declString(r.ds) + "}")
	}
	sum := sha256.Sum256([]byte(prefix + key.String()))
	name := prefix + "-" + hex.EncodeToString(sum[:4])
	if c.classes[name] {
		return name
	}
	c.classes[name] = true
	sel := "." + name
	if double {
		sel += "." + name
	}
	for _, r := range rules {
		if len(r.ds) == 0 {
			continue
		}
		c.gen.rule(withSuffix(sel, r.suffix), r.ds)
	}
	return name
}

// layoutInfo is a block's layout.
type layoutInfo struct {
	kind string // flow, constrained, flex, grid
	m    map[string]any
}

// defaultLayouts are the layouts blocks have without a layout attribute.
var defaultLayouts = map[string]string{
	"core/buttons": "flex", "core/social-links": "flex", "core/navigation": "flex", "core/columns": "flex",
	"core/query-pagination": "flex", "core/post-template": "flow", "core/group": "flow", "core/column": "flow",
	"core/cover": "flow", "core/post-content": "flow", "core/query": "flow", "core/details": "flow",
	"core/comments": "flow", "core/quote": "flow", "core/pullquote": "flow",
}

func blockLayout(b *Block) (layoutInfo, bool) {
	def, has := defaultLayouts[b.Name]
	m := attrMap(b.Attrs, "layout")
	if !has && m == nil {
		return layoutInfo{}, false
	}
	l := layoutInfo{kind: def, m: m}
	if l.kind == "" {
		l.kind = "flow"
	}
	if m != nil {
		switch t := attrString(m, "type"); t {
		case "constrained", "flex", "grid":
			l.kind = t
		case "default":
			l.kind = "flow"
		case "":
			if attrBool(m, "inherit") || attrString(m, "contentSize") != "" {
				l.kind = "constrained"
			}
		}
	}
	return l, true
}

// layoutClasses are the classes WordPress adds for a block's layout, and
// a generated class for what the layout sets on this block alone.
func (e *emitter) layoutClasses(b *Block) []string {
	l, ok := blockLayout(b)
	if !ok {
		return nil
	}
	short := strings.TrimPrefix(b.Name, "core/")
	short = strings.ReplaceAll(short, "/", "-")
	classes := []string{"is-layout-" + l.kind, "wp-block-" + short + "-is-layout-" + l.kind}
	var rules []rule
	r := e.c.res
	gap := ""
	var row, col string
	switch g := dig(b.Attrs, "style", "spacing", "blockGap").(type) {
	case string:
		row = cssValue(g)
		col = row
	case map[string]any:
		row, col = r.value(g["top"]), r.value(g["left"])
	}
	if row != "" || col != "" {
		gap = strings.TrimSpace(cmpOr(row, "var(--wp--style--block-gap)") + " " + cmpOr(col, "var(--wp--style--block-gap)"))
	}
	m := l.m
	switch l.kind {
	case "constrained":
		if e.c.theme.rootPaddingAware {
			classes = append(classes, "has-global-padding")
		}
		content := cssValue(attrString(m, "contentSize"))
		wide := cssValue(attrString(m, "wideSize"))
		just := attrString(m, "justifyContent")
		inner := "> :where(:not(.alignleft):not(.alignright):not(.alignfull))"
		var ds []decl
		if content != "" {
			ds = append(ds, decl{"max-width", content})
		}
		switch just {
		case "left":
			ds = append(ds, decl{"margin-left", "0 !important"}, decl{"margin-right", "auto !important"})
		case "right":
			ds = append(ds, decl{"margin-left", "auto !important"}, decl{"margin-right", "0 !important"})
		default:
			if content != "" {
				ds = append(ds, decl{"margin-left", "auto !important"}, decl{"margin-right", "auto !important"})
			}
		}
		if len(ds) > 0 {
			rules = append(rules, rule{" " + inner, ds})
		}
		if wide != "" || content != "" {
			rules = append(rules, rule{" > .alignwide", []decl{{"max-width", cmpOr(wide, content)}}})
		}
		if row != "" {
			rules = append(rules, rule{" > *", []decl{{"margin-block-start", row}, {"margin-block-end", "0"}}},
				rule{" > :first-child", []decl{{"margin-block-start", "0"}}})
		}
	case "flow":
		if row != "" {
			rules = append(rules, rule{" > *", []decl{{"margin-block-start", row}, {"margin-block-end", "0"}}},
				rule{" > :first-child", []decl{{"margin-block-start", "0"}}})
		}
	case "flex":
		vertical := attrString(m, "orientation") == "vertical"
		if vertical {
			classes = append(classes, "is-vertical")
		}
		if just := attrString(m, "justifyContent"); slices.Contains([]string{"left", "center", "right", "space-between", "stretch"}, just) {
			classes = append(classes, "is-content-justification-"+just)
		}
		if attrString(m, "flexWrap") == "nowrap" {
			classes = append(classes, "is-nowrap")
		}
		var ds []decl
		if gap != "" {
			ds = append(ds, decl{"gap", gap})
		}
		if va := attrString(m, "verticalAlignment"); va != "" {
			v := map[string]string{"top": "flex-start", "center": "center", "bottom": "flex-end", "stretch": "stretch", "space-between": "space-between"}[va]
			if v != "" {
				if vertical {
					ds = append(ds, decl{"justify-content", v})
				} else {
					ds = append(ds, decl{"align-items", v})
				}
			}
		}
		if len(ds) > 0 {
			rules = append(rules, rule{"", ds})
		}
	case "grid":
		var ds []decl
		n := attrInt(m, "columnCount", 0)
		minW := cssValue(attrString(m, "minimumColumnWidth"))
		switch {
		case n > 0 && n <= 16 && minW == "":
			ds = append(ds, decl{"grid-template-columns", "repeat(" + strconv.Itoa(n) + ", minmax(0, 1fr))"})
		case n > 0 && n <= 16:
			ds = append(ds, decl{"grid-template-columns", "repeat(auto-fill, minmax(max(" + minW + ", (100% - (var(--wp--style--block-gap) * " +
				strconv.Itoa(n-1) + ")) / " + strconv.Itoa(n) + "), 1fr))"})
		default:
			ds = append(ds, decl{"grid-template-columns", "repeat(auto-fill, minmax(min(" + cmpOr(minW, "12rem") + ", 100%), 1fr))"})
		}
		if gap != "" {
			ds = append(ds, decl{"gap", gap})
		}
		rules = append(rules, rule{"", ds})
		if n > 1 && minW == "" {
			classes = append(classes, "sap-wp-grid-columns")
		}
	}
	if len(rules) > 0 {
		classes = append(classes, e.c.instance("wpl", rules, false))
	}
	return classes
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// supportClasses are the classes WordPress writes for a dynamic block's
// colour, typography, alignment and style attributes (a static block has
// them in its saved HTML already).
func (e *emitter) supportClasses(b *Block, withStyle bool) []string {
	a := b.Attrs
	var out []string
	add := func(cs ...string) {
		for _, c := range cs {
			if c != "" && classRe.MatchString(c) && !reservedClass(c) && !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	if s := cssIdent(attrString(a, "textColor")); s != "" {
		add("has-"+s+"-color", "has-text-color")
	}
	if s := cssIdent(attrString(a, "backgroundColor")); s != "" {
		add("has-"+s+"-background-color", "has-background")
	}
	if s := cssIdent(attrString(a, "gradient")); s != "" {
		add("has-"+s+"-gradient-background", "has-background")
	}
	if s := cssIdent(attrString(a, "borderColor")); s != "" {
		add("has-"+s+"-border-color", "has-border-color")
	}
	if s := cssIdent(attrString(a, "fontSize")); s != "" {
		add("has-" + s + "-font-size")
	}
	if s := cssIdent(attrString(a, "fontFamily")); s != "" {
		add("has-" + s + "-font-family")
	}
	if s := attrString(a, "align"); slices.Contains([]string{"wide", "full", "left", "right", "center"}, s) {
		add("align" + s)
	}
	for _, k := range []string{"textAlign", "justification"} {
		if s := attrString(a, k); slices.Contains([]string{"left", "center", "right", "justify"}, s) {
			add("has-text-align-" + s)
		}
	}
	if dig(a, "style", "color", "text") != nil {
		add("has-text-color")
	}
	if dig(a, "style", "color", "background") != nil || dig(a, "style", "color", "gradient") != nil {
		add("has-background")
	}
	add(strings.Fields(attrString(a, "className"))...)
	if withStyle {
		if style := attrMap(a, "style"); style != nil {
			add(e.c.classFor("wps", e.c.res.styleDecls(style)))
		}
	}
	add(e.elementClass(b))
	return out
}

// elementClass is the generated class of a block's style.elements: the
// colours of its links, headings and buttons.
func (e *emitter) elementClass(b *Block) string {
	els := attrMap(attrMap(b.Attrs, "style"), "elements")
	if els == nil {
		return ""
	}
	var rules []rule
	r := e.c.res
	for _, name := range sortedKeys(els) {
		es, ok := els[name].(map[string]any)
		sel, known := elementSelectors[name]
		if !ok || !known {
			continue
		}
		for _, part := range splitTop(sel, ',') {
			part = strings.TrimSpace(part)
			if ds := r.styleDecls(es); len(ds) > 0 {
				rules = append(rules, rule{" " + part, ds})
			}
			for _, st := range pseudoStates {
				if s, ok := es[st].(map[string]any); ok {
					if ds := r.styleDecls(s); len(ds) > 0 {
						rules = append(rules, rule{" " + part + st, ds})
					}
				}
			}
		}
	}
	if len(rules) == 0 {
		return ""
	}
	return e.c.instance("wpe", rules, true)
}

// classAttr writes a class attribute, or nothing.
func classAttr(cs ...[]string) string {
	var all []string
	for _, c := range cs {
		for _, x := range c {
			if x != "" && !slices.Contains(all, x) {
				all = append(all, x)
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	return ` class="` + strings.Join(all, " ") + `"`
}
