package customize

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// RGB is an sRGB colour with 8-bit channels.
type RGB struct{ R, G, B uint8 }

// NormalizeHex accepts #rgb or #rrggbb (any case) and returns #rrggbb.
func NormalizeHex(s string) (string, bool) {
	c, ok := ParseHex(s)
	if !ok {
		return "", false
	}
	return c.Hex(), true
}

// ParseHex parses #rgb or #rrggbb.
func ParseHex(s string) (RGB, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return RGB{}, false
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return RGB{}, false
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return RGB{}, false
	}
	return RGB{uint8(n >> 16), uint8(n >> 8), uint8(n)}, true
}

func (c RGB) Hex() string     { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
func (c RGB) Triplet() string { return fmt.Sprintf("%d %d %d", c.R, c.G, c.B) }

// Luminance is the WCAG 2 relative luminance.
func (c RGB) Luminance() float64 {
	ch := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
}

// Contrast is the WCAG 2 contrast ratio between two colours (1 to 21).
func Contrast(a, b RGB) float64 {
	la, lb := a.Luminance(), b.Luminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// Mix returns a weighted mix of a and b in sRGB (t = share of b).
func Mix(a, b RGB, t float64) RGB {
	m := func(x, y uint8) uint8 { return uint8(math.Round(float64(x)*(1-t) + float64(y)*t)) }
	return RGB{m(a.R, b.R), m(a.G, b.G), m(a.B, b.B)}
}

// hsl conversions, used to derive a dark palette that keeps the hues.
func (c RGB) hsl() (h, s, l float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func fromHSL(h, s, l float64) RGB {
	if s == 0 {
		v := uint8(math.Round(l * 255))
		return RGB{v, v, v}
	}
	hue := func(p, q, t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		switch {
		case t < 1.0/6:
			return p + (q-p)*6*t
		case t < 0.5:
			return q
		case t < 2.0/3:
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	q := l * (1 + s)
	if l >= 0.5 {
		q = l + s - l*s
	}
	p := 2*l - q
	to := func(x float64) uint8 { return uint8(math.Round(math.Max(0, math.Min(1, x)) * 255)) }
	return RGB{to(hue(p, q, h+1.0/3)), to(hue(p, q, h)), to(hue(p, q, h-1.0/3))}
}

// DeriveDark suggests a dark palette from a light one: surfaces become dark
// with the same hue, text becomes light, and the primary and accent
// colours are lightened until they read on the dark background (or stay
// surfaces, when the theme uses them as one). Tokens
// missing from light fall back to the theme's light defaults.
func DeriveDark(light, defaults map[string]string) map[string]string {
	get := func(k string) (RGB, bool) {
		if v, ok := ParseHex(light[k]); ok {
			return v, true
		}
		return ParseHex(defaults[k])
	}
	out := map[string]string{}
	surface := func(k string, l float64) {
		if c, ok := get(k); ok {
			h, s, _ := c.hsl()
			out[k] = fromHSL(h, math.Min(s, 0.35), l).Hex()
		}
	}
	text := func(k string, l float64) {
		if c, ok := get(k); ok {
			h, s, _ := c.hsl()
			out[k] = fromHSL(h, math.Min(s, 0.2), l).Hex()
		}
	}
	surface("background", 0.07)
	surface("card", 0.11)
	surface("muted", 0.14)
	surface("border", 0.22)
	text("foreground", 0.93)
	text("card-foreground", 0.93)
	text("muted-foreground", 0.70)
	bg, _ := ParseHex(out["background"])
	lightBg, _ := get("background")
	for _, k := range []string{"primary", "accent"} {
		c, ok := get(k)
		if !ok {
			continue
		}
		h, s, l := c.hsl()
		// Some themes use accent (or even primary) as a subtle surface,
		// such as a hover background: it stays a surface in the dark.
		if s < 0.25 && Contrast(c, lightBg) < 1.6 {
			out[k] = fromHSL(h, s, 0.17).Hex()
			out[k+"-foreground"] = "#ededee"
			text(k+"-foreground", 0.93)
			continue
		}
		for i := 0; i < 20 && Contrast(fromHSL(h, s, l), bg) < 4.5; i++ {
			l = math.Min(0.95, l+0.04)
		}
		col := fromHSL(h, s, l)
		out[k] = col.Hex()
		// Text on the lightened colour: whichever end of the scale reads.
		fg := RGB{0x10, 0x10, 0x14}
		if Contrast(col, fg) < Contrast(col, RGB{255, 255, 255}) {
			fg = RGB{255, 255, 255}
		}
		out[k+"-foreground"] = fg.Hex()
	}
	return out
}

// Warning is a failed contrast check.
type Warning struct {
	Scheme  string  `json:"scheme"` // light | dark
	Pair    string  `json:"pair"`   // e.g. "foreground/background"
	Ratio   float64 `json:"ratio"`
	Minimum float64 `json:"minimum"`
	Message string  `json:"message"`
}

// contrastPairs are the text-on-surface combinations every theme uses,
// with the WCAG AA minimum for normal text.
var contrastPairs = []struct{ fg, bg, label string }{
	{"foreground", "background", "Text on the page background"},
	{"muted-foreground", "background", "Secondary text on the page background"},
	{"primary", "background", "Links on the page background"},
	{"card-foreground", "card", "Text on cards"},
	{"primary-foreground", "primary", "Text on primary buttons"},
	{"accent-foreground", "accent", "Text on accent surfaces"},
}

// CheckContrast compares the effective colours (the design's, else the
// theme defaults for the current options) and returns the AA failures.
// Pairs where neither colour was customised are skipped: the theme's own
// choices are not the editor's problem.
func CheckContrast(d *Design, lightDefaults, darkDefaults map[string]string) []Warning {
	var out []Warning
	for _, sc := range []struct {
		name     string
		set, def map[string]string
	}{{"light", d.Light, lightDefaults}, {"dark", d.Dark, darkDefaults}} {
		for _, p := range contrastPairs {
			_, a := sc.set[p.fg]
			_, b := sc.set[p.bg]
			if !a && !b {
				continue
			}
			pick := func(k string) (RGB, bool) {
				if v, ok := sc.set[k]; ok {
					return ParseHex(v)
				}
				return ParseHex(sc.def[k])
			}
			fg, ok1 := pick(p.fg)
			bg, ok2 := pick(p.bg)
			if !ok1 || !ok2 {
				continue
			}
			ratio := Contrast(fg, bg)
			if ratio < 4.5 {
				out = append(out, Warning{Scheme: sc.name, Pair: p.fg + "/" + p.bg, Ratio: math.Round(ratio*100) / 100,
					Minimum: 4.5, Message: p.label})
			}
		}
	}
	return out
}
