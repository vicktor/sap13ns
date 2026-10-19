package themeimport

import (
	"strings"
	"testing"

	"sap13ns/internal/customize"
)

func TestCSSValue(t *testing.T) {
	keep := map[string]string{
		"1rem":                               "1rem",
		"  #fff ":                            "#fff",
		"var:preset|color|accent-1":          "var(--wp--preset--color--accent-1)",
		"var:custom|spacing|gap":             "var(--wp--custom--spacing--gap)",
		"clamp(1rem, 2vw + 1rem, 3rem)":      "clamp(1rem, 2vw + 1rem, 3rem)",
		"calc(100% - var(--x))":              "calc(100% - var(--x))",
		"linear-gradient(90deg, #fff, #000)": "linear-gradient(90deg, #fff, #000)",
		"color-mix(in oklab, red 50%, blue)": "color-mix(in oklab, red 50%, blue)",
		"3/2":                                "3/2",
	}
	for in, want := range keep {
		if got := cssValue(in); got != want {
			t.Errorf("cssValue(%q) = %q, want %q", in, got, want)
		}
	}
	// Anything that could end the declaration or the rule, hold a string
	// or a comment, or load a file is refused.
	for _, in := range []string{
		"red;background:url(https://evil.example/x)",
		"red}body{color:red",
		"url(https://evil.example/x.png)",
		"URL(x.png)",
		"image-set(\"x.png\" 1x)",
		"image-set(x.png 1x)",
		"element(#x)",
		"expression(alert(1))",
		"-webkit-image-set(x.png 1x)",
		"red /* comment */",
		"\"quoted\"",
		"'quoted'",
		"red !important",
		"a\\62 c",
		"<script>",
		"red\x00blue",
		"@import",
		"calc(1px",
		"calc(1px))",
		"var:a|b|c|d|e|f|g",
		"var:preset||x",
		strings.Repeat("1", maxValue+1),
	} {
		if got := cssValue(in); got != "" {
			t.Errorf("cssValue(%q) = %q, want it refused", in, got)
		}
	}
}

func TestCSSFontFamily(t *testing.T) {
	for in, want := range map[string]string{
		`"Fixture Sans", system-ui, sans-serif`: `"Fixture Sans", system-ui, sans-serif`,
		`'Inter', sans-serif`:                   `"Inter", sans-serif`,
		`var:preset|font-family|body`:           `var(--wp--preset--font-family--body)`,
		`var(--x), serif`:                       `var(--x), serif`,
		// Twenty Twenty-Three's system stack.
		`-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif`: `-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif`,
	} {
		if got := cssFontFamily(in); got != want {
			t.Errorf("cssFontFamily(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{`"a"; background:url(x)`, `"a\"b"`, `x}`, `url(https://e.example/f.woff2)`, `"<b>"`, ``} {
		if got := cssFontFamily(in); got != "" {
			t.Errorf("cssFontFamily(%q) = %q, want it refused", in, got)
		}
	}
}

// A "css" string of theme.json nested under its block cannot close that
// rule and write its own.
func TestScopeCSS(t *testing.T) {
	if got := scopeCSS(".wp-block-x", "& a { color: red; }"); !strings.Contains(got, ".wp-block-x") || !strings.Contains(got, "color: red") {
		t.Errorf("scopeCSS = %q", got)
	}
	for _, in := range []string{"} body { color: red", "color: red; } x {", "a { color: red", "/* } */ }", `content: "}`} {
		if got := scopeCSS(".wp-block-x", in); strings.Contains(got, "body") || strings.Count(got, "{") != strings.Count(got, "}") {
			t.Errorf("scopeCSS(%q) = %q", in, got)
		}
	}
	for in, want := range map[string]bool{"a{b(c)}": true, `a{content:"}"}`: true, "a{": false, "}": false, "(]": false, `"x`: false} {
		if balanced(in) != want {
			t.Errorf("balanced(%q) = %v", in, !want)
		}
	}
}

func TestCleanCSS(t *testing.T) {
	resolve := func(u string) (string, bool) {
		if u == "images/ok.png" {
			return "images/ok.png", true
		}
		return "", false
	}
	src := `@import url(https://evil.example/a.css);
@font-face { font-family: X; src: url(https://evil.example/x.woff2); }
.a { background: url("images/ok.png"); }
.b { background: url(https://evil.example/t.png) }
@-moz-document url-prefix() { a { color: red } }
@charset "utf-8";
.c { color: red }`
	got, dropped := cleanCSS(src, resolve)
	for _, bad := range []string{"evil.example", "@import", "@-moz-document"} {
		if strings.Contains(got, bad) {
			t.Errorf("kept %s:\n%s", bad, got)
		}
	}
	for _, w := range []string{`url("images/ok.png")`, ".c { color: red }"} {
		if !strings.Contains(got, w) {
			t.Errorf("lost %s:\n%s", w, got)
		}
	}
	if dropped < 2 {
		t.Errorf("dropped = %d", dropped)
	}
	if err := customize.CheckCSS(got, themeCSS); err != nil {
		t.Errorf("the cleaned CSS fails CheckCSS: %v\n%s", err, got)
	}
}

// theme.json becomes custom properties, preset classes and rules.
func TestThemeJSONToCSS(t *testing.T) {
	res := mustConvert(t, fixture(t))
	css := string(res.Files["static/theme.css"])
	for _, w := range []string{
		"--wp--preset--color--base:var(--sap-light-background, #fbfaf7)",
		"--wp--preset--color--contrast:var(--sap-light-foreground, #1d1b19)",
		"--wp--preset--color--accent-2:#e8e1d4",
		"--wp--preset--font-size--large:clamp(1.3rem, 1.3rem + ((1vw - 0.2rem) * 0.577), 1.6rem)",
		"--wp--preset--spacing--50:clamp(1.5rem, 4vw, 3rem)",
		`--wp--preset--font-family--body:"Fixture Sans", system-ui, sans-serif`,
		"--wp--custom--radius--card:8px",
		"--wp--style--block-gap:1.25rem",
		".has-accent-1-background-color{background-color:var(--wp--preset--color--accent-1) !important}",
		".has-large-font-size{font-size:var(--wp--preset--font-size--large) !important}",
		":root :where(a:where(:not(.wp-element-button))){color:var(--wp--preset--color--accent-1)}",
		":root :where(a:where(:not(.wp-element-button)):hover){color:var(--wp--preset--color--contrast)}",
		":root :where(.wp-block-button .wp-block-button__link){color:var(--wp--preset--color--base);background-color:var(--wp--preset--color--accent-1)}",
		":root :where(.wp-block-site-title){font-weight:700}",
		":root.dark{",
		":root.wp-style-night{",
		`url("images/hero.png")`,
	} {
		if !strings.Contains(css, w) {
			t.Errorf("theme.css: missing %s", w)
		}
	}
	for _, bad := range []string{"evil.example", "fonts.example", "@import", "fonts/fake.woff2", "Remote"} {
		if strings.Contains(css, bad) {
			t.Errorf("theme.css has %s", bad)
		}
	}
	if err := customize.CheckCSS(css, themeCSS); err != nil {
		t.Errorf("theme.css fails CheckCSS: %v", err)
	}
}

func TestVariationSelector(t *testing.T) {
	for in, want := range map[string]string{
		".wp-block-button .wp-block-button__link": ".wp-block-button.is-style-outline .wp-block-button__link",
		"p":                       "p.is-style-outline",
		"ul:not(.a, .b) > li, ol": "ul:not(.a, .b).is-style-outline > li, ol.is-style-outline",
		".wp-block-image img":     ".wp-block-image.is-style-outline img",
		"h1:where(.x ~ .y)":       "h1:where(.x ~ .y).is-style-outline",
	} {
		if got := variationSelector(in, "outline"); got != want {
			t.Errorf("variationSelector(%q) = %q, want %q", in, got, want)
		}
	}
	// A theme's list padding stays off the lists of other blocks.
	tj := `{"version":3,"styles":{"blocks":{"core/list":{"spacing":{"padding":{"left":"2rem"}}}}}}`
	css := string(mustConvert(t, minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`, "theme.json", tj)).Files["static/theme.css"])
	if !strings.Contains(css, ":root :where(ol:not(.wp-block-post-template") || !strings.Contains(css, "padding-left:2rem") {
		t.Error("the list padding is missing or applies to every list")
	}
}

// An image block's border and shadow go to its img, as its block.json
// "selectors" say, in the block and in its style variations.
func TestFeatureSelectors(t *testing.T) {
	tj := `{"version":3,"styles":{"blocks":{"core/image":{"border":{"radius":"4px"},"spacing":{"margin":{"bottom":"1rem"}},
		"variations":{"rounded":{"border":{"radius":"12px"}}}}}}}`
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`, "theme.json", tj))
	css := string(res.Files["static/theme.css"])
	for _, w := range []string{
		":root :where(.wp-block-image img){border-radius:4px}",
		":root :where(.wp-block-image){margin-bottom:1rem}",
		":root :where(.wp-block-image.is-style-rounded img){border-radius:12px}",
	} {
		if !strings.Contains(css, w) {
			t.Errorf("theme.css: missing %s", w)
		}
	}
	if strings.Contains(css, ":root :where(.wp-block-image){border-radius") {
		t.Error("the border went to the figure")
	}
}

// The values of theme.json cannot break out of their declaration: every
// one goes through cssValue, the "css" strings through cleanCSS, and
// what fails is left out.
func TestThemeJSONInjection(t *testing.T) {
	tj := `{"version":3,"settings":{"color":{"palette":[
		{"slug":"base","color":"#fff"},
		{"slug":"evil","color":"red;}body{background:url(https://evil.example/1)"},
		{"slug":"x}body{color:red","color":"#000"}]},
		"custom":{"a":"1px;}*{background:url(https://evil.example/2)","b":"url(https://evil.example/3)"},
		"typography":{"fontFamilies":[{"slug":"f","fontFamily":"X;}body{background:url(https://evil.example/4)}",
			"fontFace":[{"fontFamily":"X\";src:url(https://evil.example/5)","src":["https://evil.example/6.woff2"]}]}]}},
	"styles":{"color":{"background":"url(https://evil.example/7)"},
		"css":"@import 'https://evil.example/8.css'; body{background-image:url(//evil.example/9)}",
		"spacing":{"padding":"1px;}body{x:url(https://evil.example/10)"},
		"elements":{"link":{"color":{"text":"red}a{background:url(https://evil.example/11)"}}},
		"blocks":{"core/group":{"css":"} body { background: url(https://evil.example/12) } x {","color":{"text":"image-set(\"https://evil.example/13\" 1x)"}},
			"core/paragraph":{"typography":{"fontFamily":"'a';background:url(https://evil.example/14)"}}}}}`
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`, "theme.json", tj))
	css := string(res.Files["static/theme.css"])
	if strings.Contains(css, "evil.example") {
		i := strings.Index(css, "evil.example")
		t.Fatalf("an external resource got through: …%s…", css[max(0, i-200):min(len(css), i+60)])
	}
	if err := customize.CheckCSS(css, themeCSS); err != nil {
		t.Fatalf("theme.css fails CheckCSS: %v", err)
	}
	if !strings.Contains(css, "--wp--preset--color--base:") {
		t.Error("the valid colour was dropped too")
	}
}

func TestModeSelector(t *testing.T) {
	for _, tc := range []struct {
		sel  string
		dark bool
		want string
	}{
		{":root", true, ":root.dark"},
		{".a .b", true, ":root.dark .a .b"},
		{".a", false, ":root:not(.dark) .a"},
	} {
		if got := modeSelector(tc.sel, tc.dark); got != tc.want {
			t.Errorf("modeSelector(%q, %v) = %q, want %q", tc.sel, tc.dark, got, tc.want)
		}
	}
}
