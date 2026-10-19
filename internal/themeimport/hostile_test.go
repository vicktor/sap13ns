package themeimport

import (
	"bytes"
	"crypto/rand"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	scriptRe  = regexp.MustCompile(`(?i)<\s*(script|style|iframe|object|embed|form|base|meta|link)\b`)
	handlerRe = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	styleRe   = regexp.MustCompile(`(?i)\sstyle\s*=`)
)

// checkTemplates fails on anything of the strict CSP or the template
// language a converted template must not have.
func checkTemplates(t *testing.T, res *Result) {
	t.Helper()
	for name, b := range res.Files {
		if !strings.HasPrefix(name, "templates/") {
			continue
		}
		s := string(b)
		for re, what := range map[*regexp.Regexp]string{scriptRe: "an element CSP or the theme rules forbid", handlerRe: "an event handler", styleRe: "a style attribute"} {
			if m := re.FindString(s); m != "" {
				t.Errorf("%s has %s: %q", name, what, m)
			}
		}
		for _, bad := range []string{"javascript:", "vbscript:", "data:text", "<?php", "<?=", "file_put_contents", "system("} {
			if strings.Contains(strings.ToLower(s), bad) {
				t.Errorf("%s has %q", name, bad)
			}
		}
		if !strings.HasPrefix(s, `{{define "`) {
			t.Errorf("%s does not start with a define", name)
		}
	}
	if !strings.Contains(string(res.Files["templates/layout.html"]), `{{template "shared/footer-meta" .}}`) {
		t.Error("the layout has no footer credit")
	}
}

func TestFixtureIsSafe(t *testing.T) {
	res := mustConvert(t, fixture(t))
	checkTemplates(t, res)
	all := generated(res)
	for _, bad := range []string{"evil.example", "pwned", "fonts.example", "alert(1)"} {
		if strings.Contains(all, bad) {
			t.Errorf("the output has %q", bad)
		}
	}
	for _, code := range []string{"theme.php", "pattern.php", "block.unknown", "html.dropped", "font.dropped", "css.dropped", "template.custom", "theme.variations", "theme.dark"} {
		if !hasNote(res.Report, code) {
			t.Errorf("the report has no %s note:\n%s", code, res.Report.Summary())
		}
	}
	if _, ok := res.Files["static/fonts/fake.woff2"]; ok {
		t.Error("a file that only starts like a font was kept")
	}
	if _, ok := res.Files["static/images/hero.png"]; !ok {
		t.Error("the theme's image was not kept")
	}
	for name := range res.Files {
		if strings.HasSuffix(name, ".php") || strings.HasPrefix(name, "styles/") || strings.HasPrefix(name, "patterns/") {
			t.Errorf("a source file was copied: %s", name)
		}
	}
	if string(res.Files["LICENSE.txt"]) != gpl2 {
		t.Error("the licence is not the GPL v2 the theme names")
	}
	if !strings.Contains(string(res.Files["README.md"]), "The sap13ns tests") {
		t.Error("the credits do not name the author")
	}
}

// The HTML of a theme goes through an allowlist: elements, attributes
// and URLs.
func TestHostileHTML(t *testing.T) {
	markup := `<!-- wp:group --><div class="wp-block-group" onclick="alert(1)" style="color:red;background:url(https://evil.example/s)">` +
		`<script>alert(1)</script><style>body{display:none}</style><iframe src="https://evil.example/f"></iframe>` +
		`<a href="javascript:alert(1)">j</a><a href="JaVaScRiPt&colon;alert(1)">k</a><a href=" data:text/html,x">d</a>` +
		`<a href="https://ok.example/" target="_blank">ok</a>` +
		`<img src="x" onerror="alert(1)"><svg onload="alert(1)"><circle/></svg>` +
		`<form action="https://evil.example/"><input name="p"></form><object data="x"></object><embed src="x">` +
		`<p id="sap-x" class="sap-wp-credit footer-meta ok" data-x="1" aria-label="A">{{.Site.Viewer}} {{template "x"}}</p>` +
		`<math><mi xlink:href="javascript:alert(1)">m</mi></math><noscript><p>n</p></noscript><template><p>t</p></template>` +
		`<base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example/"><link rel="stylesheet" href="https://evil.example/c.css">` +
		`</div><!-- /wp:group -->`
	res := mustConvert(t, minimal(markup))
	checkTemplates(t, res)
	home := content(t, res, "home")
	for _, bad := range []string{"evil.example", "alert", "{{.Site.Viewer}}", `{{template "x"}}`, "sap-x", "sap-wp-credit", "footer-meta", "<svg", "<math", "<input", "<template"} {
		if strings.Contains(home, bad) {
			t.Errorf("home has %q:\n%s", bad, home)
		}
	}
	for _, w := range []string{`href="https://ok.example/"`, `rel="noopener noreferrer"`, `aria-label="A"`, `class="ok"`, "&#123;&#123;.Site.Viewer}}"} {
		if !strings.Contains(home, w) {
			t.Errorf("home lost %s:\n%s", w, home)
		}
	}
	if !hasNote(res.Report, "html.dropped") {
		t.Error("no note for the dropped elements")
	}
}

// Images: only files of the theme, sniffed; SVG only when plain.
func TestHostileAssets(t *testing.T) {
	big := make([]byte, maxFile+1)
	copy(big, "\x89PNG\r\n\x1a\n")
	_, _ = rand.Read(big[8:])
	markup := `<!-- wp:image --><figure class="wp-block-image"><img src="` + `/wp-content/themes/mini/assets/fake.png" alt=""/></figure><!-- /wp:image -->` +
		`<!-- wp:image --><figure class="wp-block-image"><img src="/wp-content/themes/mini/assets/big.png" alt=""/></figure><!-- /wp:image -->` +
		`<!-- wp:image --><figure class="wp-block-image"><img src="/wp-content/themes/mini/assets/bad.svg" alt=""/></figure><!-- /wp:image -->` +
		`<!-- wp:image --><figure class="wp-block-image"><img src="/wp-content/themes/mini/assets/ok.svg" alt=""/></figure><!-- /wp:image -->` +
		`<!-- wp:image --><figure class="wp-block-image"><img src="/wp-content/themes/mini/../../wp-config.php" alt=""/></figure><!-- /wp:image -->`
	files := minimal(markup,
		"assets/fake.png", "<?php system('id'); ?>",
		"assets/big.png", string(big),
		"assets/bad.svg", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"assets/ok.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`)
	res := mustConvert(t, files)
	for name := range res.Files {
		if strings.HasPrefix(name, "static/") && !strings.HasSuffix(name, ".css") {
			t.Errorf("a refused file was bundled: %s", name)
		}
	}
	home := content(t, res, "home")
	if !strings.Contains(home, `src="data:image/svg+xml;base64,`) || strings.Count(home, "<img") != 1 {
		t.Errorf("home: %s", home)
	}
	if !hasNote(res.Report, "image.unsupported") {
		t.Error("no note for the refused images")
	}
}

// Fonts: only the theme's .woff2 and .woff files, checked; a font from
// another server is left out.
func TestHostileFonts(t *testing.T) {
	woff2 := make([]byte, 64)
	copy(woff2, "wOF2\x00\x01\x00\x00\x00\x00\x00\x40")
	tj := `{"version":3,"settings":{"typography":{"fontFamilies":[
		{"slug":"a","fontFamily":"A, sans-serif","fontFace":[{"fontFamily":"A","src":["file:./assets/a.woff2"]}]},
		{"slug":"b","fontFamily":"B, serif","fontFace":[{"fontFamily":"B","src":["https://fonts.example/b.woff2"]}]},
		{"slug":"c","fontFamily":"C, serif","fontFace":[{"fontFamily":"C","src":["file:./assets/c.ttf"]}]},
		{"slug":"d","fontFamily":"D, serif","fontFace":[{"fontFamily":"D","src":["file:./../../d.woff2"]}]}]}},
	"styles":{"typography":{"fontFamily":"var:preset|font-family|a"},"elements":{"h1":{"typography":{"fontFamily":"var:preset|font-family|b"}},
		"h2":{"typography":{"fontFamily":"var:preset|font-family|c"}},"h3":{"typography":{"fontFamily":"var:preset|font-family|d"}}}}}`
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`, "theme.json", tj,
		"assets/a.woff2", string(woff2), "assets/c.ttf", "ttf"))
	css := string(res.Files["static/theme.css"])
	if _, ok := res.Files["static/fonts/a.woff2"]; !ok || !strings.Contains(css, `url("fonts/a.woff2") format("woff2")`) {
		t.Errorf("the theme's font was not kept: %v", res.Report.Summary())
	}
	for _, bad := range []string{"fonts.example", "c.ttf", "d.woff2"} {
		if strings.Contains(css, bad) {
			t.Errorf("theme.css has %s", bad)
		}
	}
	if !hasNote(res.Report, "font.remote") || !hasNote(res.Report, "font.dropped") {
		t.Errorf("notes:\n%s", res.Report.Summary())
	}
}

// The archive: paths that leave it, too many files, too big, bombs.
func TestHostileArchive(t *testing.T) {
	base := minimal(`<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->`)
	with := func(extra map[string][]byte) map[string][]byte {
		m := map[string][]byte{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for name, files := range map[string]map[string][]byte{
		"zip slip":      with(map[string][]byte{"mini/../../evil.html": []byte("x")}),
		"absolute path": with(map[string][]byte{"/etc/evil": []byte("x")}),
		"backslash":     with(map[string][]byte{`mini\..\..\evil`: []byte("x")}),
		"bomb":          with(map[string][]byte{"mini/assets/zero.png": make([]byte, 20<<20)}),
	} {
		t.Run(name, func(t *testing.T) {
			if res, err := convertFiles(t, files); err == nil {
				t.Fatalf("converted: %v", res.Report.Summary())
			}
		})
	}
	t.Run("too many files", func(t *testing.T) {
		m := with(nil)
		for i := range maxEntries + 1 {
			m["mini/x/f"+strconv.Itoa(i)+".txt"] = nil
		}
		if _, err := convertFiles(t, m); err == nil {
			t.Fatal("converted an archive of too many files")
		}
	})
	t.Run("too big", func(t *testing.T) {
		src := bytes.Repeat([]byte{0}, MaxArchive+1)
		if _, err := Convert(bytes.NewReader(src), int64(len(src)), Options{}); err == nil {
			t.Fatal("converted an archive larger than the limit")
		}
	})
	t.Run("not an archive", func(t *testing.T) {
		src := []byte("<?php echo 1;")
		if _, err := Convert(bytes.NewReader(src), int64(len(src)), Options{}); err == nil {
			t.Fatal("converted a file that is not an archive")
		}
	})
	t.Run("a classic theme", func(t *testing.T) {
		_, err := convertFiles(t, map[string][]byte{"classic/style.css": []byte("/*\nTheme Name: Classic\n*/"), "classic/index.php": []byte("<?php get_header();")})
		if !errors.Is(err, ErrNotBlockTheme) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("hidden files are skipped", func(t *testing.T) {
		res := mustConvert(t, with(map[string][]byte{"__MACOSX/mini/._style.css": []byte("x"), "mini/.git/config": []byte("x")}))
		if len(res.Files) == 0 {
			t.Fatal("no files")
		}
	})
	t.Run("a template too big", func(t *testing.T) {
		res, err := convertFiles(t, with(map[string][]byte{"mini/templates/single.html": bytes.Repeat([]byte("<p>x</p>"), maxText/8+1)}))
		if err != nil {
			t.Fatal(err)
		}
		// single falls back to the index template, as in WordPress.
		if s := content(t, res, "single"); strings.Count(s, "<p>x</p>") != 1 || !hasNote(res.Report, "file.unreadable") {
			t.Errorf("the oversized template was converted (%d bytes)", len(s))
		}
	})
}

// Patterns that include themselves end.
func TestPatternCycle(t *testing.T) {
	res := mustConvert(t, minimal(`<!-- wp:pattern {"slug":"mini/a"} /-->`,
		"patterns/a.php", "<?php\n/**\n * Title: A\n * Slug: mini/a\n */\n?><!-- wp:paragraph --><p>A</p><!-- /wp:paragraph --><!-- wp:pattern {\"slug\":\"mini/b\"} /-->",
		"patterns/b.php", "<?php\n/**\n * Title: B\n * Slug: mini/b\n */\n?><!-- wp:pattern {\"slug\":\"mini/a\"} /-->"))
	if n := strings.Count(content(t, res, "home"), "<p>A</p>"); n == 0 || n > 8 {
		t.Errorf("the pattern was written %d times", n)
	}
}
