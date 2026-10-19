package themeimport

import (
	"math"
	"slices"
	"strings"
)

// The colour scheme. sap13ns themes map the eleven colour tokens of the
// customiser (--background, --foreground, --primary, …) in :root and
// .dark, each reading --sap-light-<token> or --sap-dark-<token> with the
// theme's colour as the fallback (docs/THEMES.md, "Tokens"). A WordPress
// theme has a palette instead. The importer:
//
//   - finds the palette colours the theme uses as its background, its text
//     and its accent (styles.color, the link and button colours);
//   - wires those presets to the customiser: --wp--preset--color--base
//     becomes var(--sap-light-background, #ffffff), so changing the
//     background in the customiser changes every block that uses it;
//   - computes the other tokens (muted, border, card…) from them;
//   - derives the dark palette (color.go), unless the theme's own palette
//     is dark, in which case it is the dark one and the light palette is
//     derived from it.

// palette is a resolved palette: slug -> CSS value, in order.
type palette struct {
	slugs  []string
	values map[string]string
}

func newPalette(ps []preset) palette {
	p := palette{values: map[string]string{}}
	for _, x := range ps {
		if _, dup := p.values[x.slug]; !dup {
			p.slugs = append(p.slugs, x.slug)
		}
		p.values[x.slug] = cssValue(x.value)
	}
	return p
}

func (p palette) clone() palette {
	q := palette{slugs: slices.Clone(p.slugs), values: map[string]string{}}
	for k, v := range p.values {
		q.values[k] = v
	}
	return q
}

func (p palette) mapColours(f func(rgba) rgba) palette {
	q := p.clone()
	for k, v := range q.values {
		q.values[k] = mapColours(v, f)
	}
	return q
}

// roles names the palette colours a scheme is built on: a slug when the
// theme uses a preset, else a literal colour.
type roles struct {
	bg, fg, primary, primaryFg ref
}

type ref struct {
	slug    string // a palette preset
	literal string // or a colour
}

func (r ref) css() string {
	if r.slug != "" {
		return "var(--wp--preset--color--" + r.slug + ")"
	}
	return r.literal
}

func (r ref) colour(p palette) (rgba, bool) {
	v := r.literal
	if r.slug != "" {
		v = p.values[r.slug]
	}
	return parseColour(v)
}

// refOf reads a style colour: "var:preset|color|x", var(--wp--preset--color--x)
// or a literal colour.
func refOf(v string, p palette) (ref, bool) {
	v = strings.TrimSpace(v)
	slug := ""
	switch {
	case strings.HasPrefix(v, "var:preset|color|"):
		slug = cssIdent(strings.TrimPrefix(v, "var:preset|color|"))
	case strings.HasPrefix(v, "var(--wp--preset--color--") && strings.HasSuffix(v, ")"):
		slug = strings.TrimSuffix(strings.TrimPrefix(v, "var(--wp--preset--color--"), ")")
	}
	if slug != "" {
		if _, ok := p.values[slug]; ok {
			return ref{slug: slug}, true
		}
		return ref{}, false
	}
	if _, ok := parseColour(v); ok {
		return ref{literal: v}, true
	}
	return ref{}, false
}

// findRoles reads the roles from a theme's styles.
func findRoles(styles map[string]any, p palette) roles {
	get := func(keys ...string) (ref, bool) {
		v, _ := dig(styles, keys...).(string)
		return refOf(v, p)
	}
	var r roles
	var ok bool
	if r.bg, ok = get("color", "background"); !ok {
		r.bg = ref{literal: "#ffffff"}
		for _, s := range []string{"base", "background", "white"} {
			if _, has := p.values[s]; has {
				r.bg = ref{slug: s}
				break
			}
		}
	}
	if r.fg, ok = get("color", "text"); !ok {
		r.fg = ref{literal: "#111111"}
		for _, s := range []string{"contrast", "foreground", "black"} {
			if _, has := p.values[s]; has {
				r.fg = ref{slug: s}
				break
			}
		}
	}
	// The accent: the links' colour, else the buttons', else a preset
	// named for it, else the text.
	r.primary = r.fg
	link, linkOK := get("elements", "link", "color", "text")
	btn, btnOK := get("elements", "button", "color", "background")
	switch {
	case linkOK && link != r.fg && link != r.bg:
		r.primary = link
	case btnOK && btn != r.bg:
		r.primary = btn
	default:
		for _, s := range []string{"primary", "accent", "accent-1", "vivid-cyan-blue"} {
			if _, has := p.values[s]; has && s != r.bg.slug {
				r.primary = ref{slug: s}
				break
			}
		}
	}
	r.primaryFg = ref{}
	if btnOK && btn == r.primary {
		if t, ok := get("elements", "button", "color", "text"); ok {
			r.primaryFg = t
		}
	}
	return r
}

// coreSlug: a colour of WordPress's default palette, which every theme
// has unless it turns it off; the theme's own colours speak for it first.
func coreSlug(s string) bool {
	return slices.ContainsFunc(corePalette, func(p preset) bool { return p.slug == s })
}

// ownColours: the palette has colours of the theme's own.
func (p palette) ownColours() bool {
	return slices.ContainsFunc(p.slugs, func(s string) bool { return !coreSlug(s) })
}

// tokens computes the customiser's eleven colours from a palette.
func (r roles) tokens(p palette) map[string]string {
	bg, ok := r.bg.colour(p)
	if !ok || bg.a < 1 {
		bg = rgba{1, 1, 1, 1}
	}
	fg, ok := r.fg.colour(p)
	if !ok || fg.a < 1 {
		fg = readableOn(bg)
	}
	primary, ok := r.primary.colour(p)
	if !ok || primary.a < 1 {
		primary = fg
	}
	pfg, ok := r.primaryFg.colour(p)
	if !ok || pfg.a < 1 || contrast(pfg, primary) < 3 {
		pfg = readableOn(primary)
	}
	// muted: the palette's colour nearest the background, when it is a
	// tint of it; else a blend.
	muted := mix(bg, fg, 0.05)
	best := 0.09
	for _, s := range p.slugs {
		c, ok := parseColour(p.values[s])
		if !ok || c.a < 1 || s == r.bg.slug || p.ownColours() && coreSlug(s) {
			continue
		}
		ob, oc := bg.oklab(), c.oklab()
		d := math.Sqrt((ob.l-oc.l)*(ob.l-oc.l) + (ob.a-oc.a)*(ob.a-oc.a) + (ob.b-oc.b)*(ob.b-oc.b))
		if d > 0.005 && d < best {
			best, muted = d, c
		}
	}
	mutedFg := mix(fg, bg, 0.4)
	for t := 0.4; t > 0 && contrast(mutedFg, bg) < 4.5; t -= 0.05 {
		mutedFg = mix(fg, bg, t)
	}
	return map[string]string{
		"background": bg.hex(), "foreground": fg.hex(),
		"card": muted.hex(), "card-foreground": fg.hex(),
		"muted": muted.hex(), "muted-foreground": mutedFg.hex(),
		"border":  mix(bg, fg, 0.16).hex(),
		"primary": primary.hex(), "primary-foreground": pfg.hex(),
		"accent": muted.hex(), "accent-foreground": fg.hex(),
	}
}

// scheme is a light or dark colour scheme: the palette and the tokens.
type scheme struct {
	pal    palette
	roles  roles
	tokens map[string]string
}

// schemeCSS writes a scheme's custom properties. mode is "light" or
// "dark": the customiser's variables of that mode come first.
func schemeCSS(s scheme, mode string) []decl {
	var ds []decl
	wired := map[string]string{} // slug -> token it is wired to
	for _, x := range []struct {
		r     ref
		token string
	}{{s.roles.bg, "background"}, {s.roles.fg, "foreground"}, {s.roles.primary, "primary"}} {
		if x.r.slug != "" && wired[x.r.slug] == "" {
			wired[x.r.slug] = x.token
		}
	}
	for _, slug := range s.pal.slugs {
		v := s.pal.values[slug]
		if t := wired[slug]; t != "" {
			if _, ok := parseColour(v); ok {
				v = "var(--sap-" + mode + "-" + t + ", " + v + ")"
			}
		}
		ds = append(ds, decl{"--wp--preset--color--" + slug, v})
	}
	tok := func(name string, r ref) string {
		if r.slug != "" && wired[r.slug] == name {
			return r.css()
		}
		return "var(--sap-" + mode + "-" + name + ", " + s.tokens[name] + ")"
	}
	ds = append(ds,
		decl{"--background", tok("background", s.roles.bg)},
		decl{"--foreground", tok("foreground", s.roles.fg)},
		decl{"--primary", tok("primary", s.roles.primary)},
	)
	for _, name := range []string{"card", "card-foreground", "muted", "muted-foreground", "border", "primary-foreground", "accent", "accent-foreground"} {
		ds = append(ds, decl{"--" + name, "var(--sap-" + mode + "-" + name + ", " + s.tokens[name] + ")"})
	}
	ds = append(ds,
		decl{"--input", "var(--border)"},
		decl{"--ring", "color-mix(in oklab, var(--primary) 60%, transparent)"},
		decl{"--popover", "var(--background)"},
		decl{"--popover-foreground", "var(--foreground)"},
		decl{"--secondary", "var(--muted)"},
		decl{"--secondary-foreground", "var(--foreground)"},
	)
	return ds
}

// derive builds the other mode of a scheme: dark from light, or light
// from dark.
func derive(s scheme, toDark bool) scheme {
	f := darken
	if !toDark {
		f = lighten
	}
	d := scheme{pal: s.pal.mapColours(f), roles: s.roles}
	if d.roles.bg.literal != "" {
		d.roles.bg.literal = mapColours(d.roles.bg.literal, f)
	}
	if d.roles.fg.literal != "" {
		d.roles.fg.literal = mapColours(d.roles.fg.literal, f)
	}
	if d.roles.primary.literal != "" {
		d.roles.primary.literal = mapColours(d.roles.primary.literal, f)
	}
	if d.roles.primaryFg.literal != "" {
		d.roles.primaryFg.literal = mapColours(d.roles.primaryFg.literal, f)
	}
	d.tokens = d.roles.tokens(d.pal)
	return d
}

// schemes builds the light and dark schemes of a palette and its styles,
// and reports whether the theme's own colours were the dark ones.
func schemes(p palette, styles map[string]any) (light, dark scheme, ownDark bool) {
	own := scheme{pal: p, roles: findRoles(styles, p)}
	own.tokens = own.roles.tokens(p)
	bg, _ := parseColour(own.tokens["background"])
	if isDark(bg) {
		return derive(own, false), own, true
	}
	return own, derive(own, true), false
}
