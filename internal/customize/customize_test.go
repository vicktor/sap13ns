package customize

import (
	"strings"
	"testing"
)

var testCtx = Context{
	Options: []OptionSpec{{Key: "accent", Type: "select", Choices: []string{"blue", "indigo"}}, {Key: "hero", Type: "bool"}, {Key: "hero_title", Type: "text"}},
	Langs:   []string{"es", "en"},
	Media:   func(id int64, kind string) bool { return (id == 7 && kind == "font") || (id == 9 && kind == "image") },
}

func TestParseValid(t *testing.T) {
	d, err := Parse([]byte(`{
		"light": {"background": "#FFF", "primary": "#1D4ED8", "accent": ""},
		"dark": {"background": "#0b0b0c"},
		"fonts": {"body": {"stack": "humanist"}, "heading": {"stack": "body"}, "mono": {"stack": "media", "media": 7}},
		"scale": {"text": "105", "radius": "none", "density": "compact", "shadow": "soft", "width": "wide"},
		"header": {"brand": "logo-title", "logo": 9, "align": "center", "sticky": "off", "hide_search": true},
		"footer": {"columns": 3, "text": "  Hola\nmundo ", "text_i18n": {"en": "Hello world"}},
		"home": {"hero": "off", "layout": "bento", "per_page": 12},
		"css": ".hero { color: red; }",
		"options": {"accent": "indigo", "hero": "false"}
	}`), testCtx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Light["background"] != "#ffffff" || d.Light["primary"] != "#1d4ed8" {
		t.Errorf("hex not normalised: %v", d.Light)
	}
	if _, ok := d.Light["accent"]; ok {
		t.Error("empty colour kept")
	}
	if d.Footer.Text != "Hola mundo" || d.Footer.TextIn("en") != "Hello world" || d.Footer.TextIn("es") != "Hola mundo" {
		t.Errorf("footer text: %+v", d.Footer)
	}
	if d.Version != Version {
		t.Errorf("version = %d", d.Version)
	}
	if got := d.HTMLClasses(); got != "sap-sticky-off sap-brand-center sap-has-logo" {
		t.Errorf("classes = %q", got)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field":   `{"colours": {}}`,
		"bad hex":         `{"light": {"background": "red"}}`,
		"hex with alpha":  `{"light": {"background": "#ffffff80"}}`,
		"unknown token":   `{"light": {"popover": "#ffffff"}}`,
		"css injection":   `{"light": {"background": "#fff;}body{display:none"}}`,
		"unknown font":    `{"fonts": {"body": {"stack": "comic"}}}`,
		"font no media":   `{"fonts": {"body": {"stack": "media"}}}`,
		"font wrong kind": `{"fonts": {"body": {"stack": "media", "media": 9}}}`,
		"mono stack":      `{"fonts": {"mono": {"stack": "serif"}}}`,
		"heading body":    `{"fonts": {"body": {"stack": "body"}}}`,
		"text scale":      `{"scale": {"text": "300"}}`,
		"radius":          `{"scale": {"radius": "9px"}}`,
		"width css":       `{"scale": {"width": "calc(100vw)"}}`,
		"brand":           `{"header": {"brand": "banner"}}`,
		"logo missing":    `{"header": {"brand": "logo"}}`,
		"logo not image":  `{"header": {"brand": "logo", "logo": 7}}`,
		"sticky":          `{"header": {"sticky": "yes"}}`,
		"columns":         `{"footer": {"columns": 9}}`,
		"footer lang":     `{"footer": {"text_i18n": {"fr": "Bonjour"}}}`,
		"footer long":     `{"footer": {"text": "` + strings.Repeat("x", MaxFooterText+1) + `"}}`,
		"layout":          `{"home": {"layout": "masonry"}}`,
		"per page":        `{"home": {"per_page": 500}}`,
		"import":          `{"css": "@import url(/x.css);"}`,
		"option unknown":  `{"options": {"palette": "x"}}`,
		"option choice":   `{"options": {"accent": "green"}}`,
		"option bool":     `{"options": {"hero": "yes"}}`,
		"version":         `{"version": 99}`,
		"not json":        `{"light":`,
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in), testCtx); err == nil {
			t.Errorf("%s: accepted %s", name, in)
		}
	}
}

func TestEmptyDesign(t *testing.T) {
	d, err := Parse([]byte(`{}`), testCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !d.IsZero() {
		t.Error("empty design not zero")
	}
	if css := d.Stylesheet(nil); css != "" {
		t.Errorf("empty design renders CSS: %q", css)
	}
	if d.HTMLClasses() != "" {
		t.Error("empty design has classes")
	}
}

func TestSanitizeCSS(t *testing.T) {
	ok := []string{
		`.hero { color: red; background: url(/uploads/2026/10/bg.webp) center / cover; }`,
		`.a { background-image: url("/uploads/a.png"), url( '/b.png' ); }`,
		`@media (max-width: 40rem) { .x { display: none } }`,
		`@supports (display: grid) { @container (min-width: 1px) { .y { gap: 1rem } } }`,
		`@font-face { font-family: X; src: url(/uploads/x.woff2) format("woff2"); }`,
		`@keyframes spin { to { transform: rotate(1turn) } }`,
		`.q::before { content: "a \"quoted\" ) } string"; }`,
		`/* comment with url(http://x) and @import */ .z { color: #fff }`,
		`.p { --x: 1px; width: calc(100% - var(--x)); }`,
		`a[href^="https://"]::after { content: "↗" }`,
	}
	for _, css := range ok {
		if _, err := SanitizeCSS(css); err != nil {
			t.Errorf("rejected %q: %v", css, err)
		}
	}
	bad := []string{
		`@import url(/x.css);`,
		`@IMPORT "x.css";`,
		`@\69mport "x.css";`,
		`@charset "utf-8";`,
		`@namespace svg url(http://www.w3.org/2000/svg);`,
		`.a { background: url(https://evil.example/x.png) }`,
		`.a { background: url("https://evil.example/x.png") }`,
		`.a { background: url(//evil.example/x.png) }`,
		`.a { background: url("//evil.example/x.png") }`,
		`.a { background: url(/\evil.example/x.png) }`,
		`.a { background: url("/\\evil.example/x.png") }`,
		`.a { background: url("/	/evil.example") }`,
		`.a { background: u\72l(https://evil.example/x.png) }`,
		`.a { background: U\52L("http://evil.example") }`,
		`.a { background: url(data:image/png;base64,AAAA) }`,
		`.a { background: url(javascript:alert(1)) }`,
		`.a { background: image-set("https://evil.example/x.png" 1x) }`,
		`.a { background: -webkit-image-set(url(/a.png) 1x) }`,
		`.a { background: image("https://evil.example/x.png") }`,
		`.a { background: cross-fade(url(/a.png), url(/b.png)) }`,
		`.a { background: element(#x) }`,
		`@font-face { src: src("https://evil.example/f.woff2") }`,
		`.a { width: expression(alert(1)) }`,
		`.a { behavior: url(/x.htc) }`,
		`.a { -moz-binding: url(/x.xml#y) }`,
		`.footer-meta { display: none }`,
		`.site-footer .footer\2d meta { display: none }`,
		`[class="footer-meta"] { opacity: 0 }`,
		`.a { color: red`,
		`.a { color: red; } }`,
		`.a { content: "unterminated }`,
		`/* unterminated`,
		`.a { background: url(/a.png }`,
		strings.Repeat("a", MaxCSS+1),
		"\x00",
	}
	for _, css := range bad {
		if _, err := SanitizeCSS(css); err == nil {
			t.Errorf("accepted %q", css)
		}
	}
}

func TestStylesheet(t *testing.T) {
	d := &Design{
		Light:  map[string]string{"background": "#ffffff", "primary": "#1d4ed8"},
		Dark:   map[string]string{"primary": "#93c5fd"},
		Fonts:  Fonts{Body: Font{Stack: "serif"}, Heading: Font{Stack: "body"}, Mono: Font{Stack: "media", Media: 7}},
		Scale:  Scale{Text: "110", Radius: "none", Density: "compact", Shadow: "strong", Width: "narrow"},
		Footer: Footer{Columns: 3},
		CSS:    ".hero { color: red; }",
	}
	css := d.Stylesheet(func(id int64) string {
		if id == 7 {
			return "/uploads/2026/10/code.woff2"
		}
		return ""
	})
	for _, want := range []string{
		"--sap-light-background: #ffffff;", "--sap-light-background-rgb: 255 255 255;",
		"--sap-light-background-hi:", "--sap-light-primary: #1d4ed8;", "--sap-light-primary-rgb: 29 78 216;",
		"--sap-dark-primary: #93c5fd;",
		"--sap-font-body: Charter,", "--sap-font-heading: var(--sap-font-body",
		`font-family: "sap-mono";`, `src: url("/uploads/2026/10/code.woff2") format("woff2");`, `--sap-font-mono: "sap-mono", ui-monospace`,
		"--sap-text-scale: 1.1;", "--sap-radius-scale: 0;", "--sap-space-scale: 0.75;", "--sap-shadow-scale: 1.6;", "--sap-content-width: 60rem;",
		":where(h1, h2, h3, h4, h5, h6) { font-family: var(--sap-font-heading); }",
		"(100% - 2 * 2.5rem) / 3)",
		"/* Custom CSS */\n.hero { color: red; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("stylesheet lacks %q:\n%s", want, css)
		}
	}
	if strings.Contains(css, "--sap-dark-background") {
		t.Error("unset dark background emitted")
	}
	// The custom CSS comes last so it can override everything else.
	if strings.Index(css, "Custom CSS") < strings.Index(css, ":root {") {
		t.Error("custom CSS before tokens")
	}
	// A missing font file drops the font instead of emitting a bad URL.
	d.Fonts.Mono.Media = 8
	if css := d.Stylesheet(func(int64) string { return "" }); strings.Contains(css, "sap-mono") {
		t.Error("missing font emitted")
	}
	if Hash("a") == Hash("b") || len(Hash("a")) != 16 {
		t.Error("hash")
	}
}

func TestContrast(t *testing.T) {
	white, black := RGB{255, 255, 255}, RGB{0, 0, 0}
	if r := Contrast(white, black); r < 20.9 || r > 21.1 {
		t.Errorf("white/black = %v", r)
	}
	if r := Contrast(white, white); r != 1 {
		t.Errorf("white/white = %v", r)
	}
	light := map[string]string{"background": "#ffffff", "foreground": "#111111", "primary": "#0071e3", "primary-foreground": "#ffffff"}
	// Untouched pairs are not reported even if the theme's own are weak.
	if w := CheckContrast(&Design{}, light, nil); len(w) != 0 {
		t.Errorf("untouched design warned: %v", w)
	}
	d := &Design{Light: map[string]string{"primary": "#ffff00"}}
	w := CheckContrast(d, light, nil)
	pairs := map[string]bool{}
	for _, x := range w {
		pairs[x.Pair] = true
		if x.Scheme != "light" || x.Ratio >= 4.5 {
			t.Errorf("bad warning %+v", x)
		}
	}
	if !pairs["primary/background"] || !pairs["primary-foreground/primary"] {
		t.Errorf("expected link and button warnings, got %v", w)
	}
	d = &Design{Light: map[string]string{"primary": "#1d4ed8"}}
	if w := CheckContrast(d, light, nil); len(w) != 0 {
		t.Errorf("good colours warned: %v", w)
	}
}

func TestDeriveDark(t *testing.T) {
	light := map[string]string{"background": "#fdfaf3", "foreground": "#1d1b16", "primary": "#1d4ed8", "accent": "#c5f23c"}
	dark := DeriveDark(light, map[string]string{"card": "#ffffff", "muted": "#f5f5f5", "border": "#e5e5e5", "muted-foreground": "#666666", "card-foreground": "#111111"})
	bg, _ := ParseHex(dark["background"])
	fg, _ := ParseHex(dark["foreground"])
	if bg.Luminance() > 0.05 || fg.Luminance() < 0.7 {
		t.Errorf("not a dark palette: %v", dark)
	}
	for _, k := range []string{"primary", "accent"} {
		c, _ := ParseHex(dark[k])
		if Contrast(c, bg) < 4.5 {
			t.Errorf("%s %s unreadable on %s", k, dark[k], dark["background"])
		}
		on, _ := ParseHex(dark[k+"-foreground"])
		if Contrast(on, c) < 4.5 {
			t.Errorf("%s-foreground unreadable", k)
		}
	}
	for _, k := range ColorTokens {
		if _, ok := NormalizeHex(dark[k]); !ok {
			t.Errorf("%s missing or invalid: %q", k, dark[k])
		}
	}
	// An accent that is a subtle surface (apple's hover grey) stays one.
	dark = DeriveDark(map[string]string{"background": "#ffffff", "accent": "#f5f5f5", "accent-foreground": "#171717"}, nil)
	acc, _ := ParseHex(dark["accent"])
	on, _ := ParseHex(dark["accent-foreground"])
	if acc.Luminance() > 0.05 || Contrast(on, acc) < 4.5 {
		t.Errorf("surface accent: %s on %s", dark["accent-foreground"], dark["accent"])
	}
}
