package themeimport

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Colours. A WordPress theme has one palette, almost always a light one;
// a sap13ns theme works in light and dark mode. The dark palette is
// derived in OKLCH, where lightness is perceptual: every colour keeps its
// hue and most of its chroma, and its lightness is mirrored and
// compressed into [darkFloor, darkCeil], so that text that contrasts with
// its background in the light palette still does in the dark one (the
// mirror is monotonic: what was darker becomes lighter).

// rgba is a colour in sRGB, components in [0, 1].
type rgba struct{ r, g, b, a float64 }

const (
	darkFloor = 0.15 // the lightness white becomes
	darkCeil  = 0.96 // the lightness black becomes
	// vividFloor is the least lightness of a vivid colour (OKLCH chroma of
	// vividChroma or more) in a derived dark palette: about 5:1 against a
	// background of darkFloor.
	vividFloor  = 0.62
	vividChroma = 0.1
)

var namedColours = map[string]rgba{
	"white": {1, 1, 1, 1}, "black": {0, 0, 0, 1}, "transparent": {0, 0, 0, 0},
	"red": {1, 0, 0, 1}, "green": {0, 128.0 / 255, 0, 1}, "blue": {0, 0, 1, 1},
	"gray": {128.0 / 255, 128.0 / 255, 128.0 / 255, 1}, "grey": {128.0 / 255, 128.0 / 255, 128.0 / 255, 1},
}

// parseColour reads a hexadecimal colour, rgb()/rgba() with numbers, or a
// few named colours. Anything else (color-mix(), currentColor, var())
// is not a colour the converter can compute with.
func parseColour(s string) (rgba, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if c, ok := namedColours[s]; ok {
		return c, true
	}
	if strings.HasPrefix(s, "#") {
		h := s[1:]
		switch len(h) {
		case 3, 4:
			var b strings.Builder
			for _, c := range h {
				b.WriteRune(c)
				b.WriteRune(c)
			}
			h = b.String()
		case 6, 8:
		default:
			return rgba{}, false
		}
		n, err := strconv.ParseUint(h, 16, 64)
		if err != nil {
			return rgba{}, false
		}
		c := rgba{a: 1}
		if len(h) == 8 {
			c.a = float64(n&0xff) / 255
			n >>= 8
		}
		c.r, c.g, c.b = float64(n>>16&0xff)/255, float64(n>>8&0xff)/255, float64(n&0xff)/255
		return c, true
	}
	if m := rgbRe.FindStringSubmatch(s); m != nil {
		parts := strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) != 3 && len(parts) != 4 {
			return rgba{}, false
		}
		c := rgba{a: 1}
		vals := []*float64{&c.r, &c.g, &c.b, &c.a}
		for i, p := range parts {
			pct := strings.HasSuffix(p, "%")
			v, err := strconv.ParseFloat(strings.TrimSuffix(p, "%"), 64)
			if err != nil {
				return rgba{}, false
			}
			switch {
			case pct:
				v /= 100
			case i < 3:
				v /= 255
			}
			*vals[i] = clamp01(v)
		}
		return c, true
	}
	return rgba{}, false
}

var rgbRe = regexp.MustCompile(`^rgba?\(\s*([0-9.%\s,/]+)\)$`)

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// hex writes the colour as #rrggbb, or #rrggbbaa when it is translucent.
func (c rgba) hex() string {
	b := func(v float64) int { return int(math.Round(clamp01(v) * 255)) }
	if c.a < 1 {
		return fmt.Sprintf("#%02x%02x%02x%02x", b(c.r), b(c.g), b(c.b), b(c.a))
	}
	return fmt.Sprintf("#%02x%02x%02x", b(c.r), b(c.g), b(c.b))
}

func toLinear(v float64) float64 {
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func fromLinear(v float64) float64 {
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// luminance is the WCAG relative luminance.
func (c rgba) luminance() float64 {
	return 0.2126*toLinear(c.r) + 0.7152*toLinear(c.g) + 0.0722*toLinear(c.b)
}

// contrast is the WCAG contrast ratio of two opaque colours.
func contrast(a, b rgba) float64 {
	la, lb := a.luminance(), b.luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// oklab: L in [0, 1], a and b around 0.
type oklab struct{ l, a, b float64 }

func (c rgba) oklab() oklab {
	r, g, b := toLinear(c.r), toLinear(c.g), toLinear(c.b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return oklab{
		0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s,
	}
}

// rgb converts back, reporting whether the colour is inside sRGB.
func (o oklab) rgb() (rgba, bool) {
	l := o.l + 0.3963377774*o.a + 0.2158037573*o.b
	m := o.l - 0.1055613458*o.a - 0.0638541728*o.b
	s := o.l - 0.0894841775*o.a - 1.2914855480*o.b
	l, m, s = l*l*l, m*m*m, s*s*s
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	b := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	const eps = 1e-4
	in := r >= -eps && r <= 1+eps && g >= -eps && g <= 1+eps && b >= -eps && b <= 1+eps
	return rgba{fromLinear(clamp01(r)), fromLinear(clamp01(g)), fromLinear(clamp01(b)), 1}, in
}

// fromOKLCH builds a colour from lightness, chroma and hue (radians),
// reducing the chroma until it fits in sRGB.
func fromOKLCH(l, c, h float64) rgba {
	for range 40 {
		out, ok := oklab{l, c * math.Cos(h), c * math.Sin(h)}.rgb()
		if ok || c < 1e-4 {
			return out
		}
		c *= 0.92
	}
	out, _ := oklab{l, 0, 0}.rgb()
	return out
}

// darken is the dark-mode counterpart of a colour of the light palette.
func darken(c rgba) rgba {
	o := c.oklab()
	ch, h := math.Hypot(o.a, o.b), math.Atan2(o.b, o.a)
	l := darkFloor + (1-o.l)*(darkCeil-darkFloor)
	if l < o.l {
		ch *= 0.85 // a light tint turned dark: a little less vivid
	}
	// A vivid mid-tone (a link or accent colour) keeps its lightness under
	// the inversion, and with it too little contrast on the dark
	// background: it is lifted to where it reads (about 4.5:1 on it).
	if ch >= vividChroma && l < vividFloor {
		l = vividFloor
	}
	out := fromOKLCH(l, ch, h)
	out.a = c.a
	return out
}

// lighten is the light-mode counterpart of a dark colour: the inverse of
// darken, for themes whose own palette is dark.
func lighten(c rgba) rgba {
	o := c.oklab()
	ch, h := math.Hypot(o.a, o.b), math.Atan2(o.b, o.a)
	l := 1 - (o.l-darkFloor)/(darkCeil-darkFloor)
	out := fromOKLCH(math.Max(0, math.Min(1, l)), ch, h)
	out.a = c.a
	return out
}

// mix blends a and b in OKLab, t of b.
func mix(a, b rgba, t float64) rgba {
	oa, ob := a.oklab(), b.oklab()
	out, _ := oklab{oa.l + (ob.l-oa.l)*t, oa.a + (ob.a-oa.a)*t, oa.b + (ob.b-oa.b)*t}.rgb()
	out.a = a.a + (b.a-a.a)*t
	return out
}

// readableOn picks black or white text for a background.
func readableOn(bg rgba) rgba {
	white, black := rgba{1, 1, 1, 1}, rgba{0.07, 0.07, 0.07, 1}
	if contrast(bg, white) >= contrast(bg, black) {
		return white
	}
	return black
}

// isDark reports whether a background is dark.
func isDark(c rgba) bool { return c.oklab().l < 0.5 }

// hexColourRe finds hexadecimal colours in a CSS value (a gradient).
var hexColourRe = regexp.MustCompile(`#(?:[0-9a-fA-F]{8}|[0-9a-fA-F]{6}|[0-9a-fA-F]{3,4})\b`)

// mapColours rewrites every hexadecimal and rgb() colour in a CSS value
// with f, leaving the rest (var(), keywords, color-mix()) as it is.
func mapColours(v string, f func(rgba) rgba) string {
	v = hexColourRe.ReplaceAllStringFunc(v, func(h string) string {
		if c, ok := parseColour(h); ok {
			return f(c).hex()
		}
		return h
	})
	return rgbInRe.ReplaceAllStringFunc(v, func(s string) string {
		if c, ok := parseColour(s); ok {
			return f(c).hex()
		}
		return s
	})
}

var rgbInRe = regexp.MustCompile(`rgba?\([0-9.%\s,/]+\)`)
