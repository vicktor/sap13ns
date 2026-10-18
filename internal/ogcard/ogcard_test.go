package ogcard

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// go test ./internal/ogcard -run Samples -ogcard.out=/some/dir writes the
// sample cards as PNG files for visual review.
var outDir = flag.String("ogcard.out", "", "directory to write sample cards to")

func testLogo() image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := range 256 {
		for x := range 256 {
			m.Set(x, y, color.NRGBA{uint8(40 + x/4), uint8(90 + y/5), 200, 255})
		}
	}
	for y := 80; y < 176; y++ {
		for x := 80; x < 176; x++ {
			m.Set(x, y, color.NRGBA{255, 255, 255, 255})
		}
	}
	return m
}

func mustStyle(t testing.TB, bg, fg, muted, accent string) Style {
	t.Helper()
	s, err := StyleFromHex(bg, fg, muted, accent)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type sample struct {
	name string
	card Card
}

func samples(t testing.TB) []sample {
	light := mustStyle(t, "#ffffff", "#0f172a", "#64748b", "#4f46e5")
	warm := mustStyle(t, "#fbf7f0", "#2b2118", "#7a6a58", "#c2410c")
	dark := mustStyle(t, "#0b1020", "#f8fafc", "#94a3b8", "#818cf8")
	green := mustStyle(t, "#f6fbf7", "#0f2a1d", "#5b7465", "#15803d")
	es := Card{
		Title:    "Cómo preparamos la paella valenciana perfecta en una cocina pequeña",
		Eyebrow:  "Gastronomía",
		Footer:   "12 de marzo de 2026 · 5 min de lectura",
		SiteName: "La Cocina de Núria",
	}
	long := Card{
		Title:    "Ein überraschend langer Titel über die Geschichte der Straßenbahn in Zürich, Genève und Köln, mit vielen Umlauten, Akzenten und noch viel mehr Wörtern, die nicht mehr auf die Karte passen sollten",
		Eyebrow:  "Geschichte",
		Footer:   "sap13ns.dev · 3. Februar 2026 · 14 min",
		SiteName: "Straßen & Bahnen",
	}
	word := Card{
		Title:    "Donaudampfschifffahrtselektrizitätenhauptbetriebswerkbauunterbeamtengesellschaft",
		Eyebrow:  "Wörter",
		Footer:   "1 min read",
		SiteName: "Lexikon",
	}
	var out []sample
	for _, layout := range []string{"classic", "bold", "minimal"} {
		add := func(name string, c Card, s Style, logo bool) {
			c.Layout, c.Style = layout, s
			if logo {
				c.Logo = testLogo()
			}
			out = append(out, sample{layout + "-" + name, c})
		}
		add("es", es, light, true)
		add("es-nologo", es, warm, false)
		add("long", long, green, true)
		add("word", word, light, false)
		add("dark", es, dark, true)
		add("bare", Card{Title: "Página sin categoría: ¿qué opinas?"}, Style{}, false)
		add("short", Card{Title: "Hola, món!", Eyebrow: "Pàgina", SiteName: "Catalunya Oberta", Footer: "Publicat l'1 de gener"}, mustStyle(t, "#111827", "", "", "#f59e0b"), false)
	}
	return out
}

func TestRenderLayouts(t *testing.T) {
	for _, s := range samples(t) {
		t.Run(s.name, func(t *testing.T) {
			b, err := Render(s.card)
			if err != nil {
				t.Fatal(err)
			}
			img, err := png.Decode(bytes.NewReader(b))
			if err != nil {
				t.Fatal(err)
			}
			if got := img.Bounds().Size(); got != image.Pt(Width, Height) {
				t.Fatalf("size = %v", got)
			}
			t.Logf("%d KB", len(b)>>10)
			if len(b) > 200<<10 {
				t.Errorf("PNG is %d KB", len(b)>>10)
			}
			if *outDir != "" {
				if err := os.MkdirAll(*outDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(*outDir, s.name+".png"), b, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRenderOddInput(t *testing.T) {
	cases := []Card{
		{},
		{Title: "   \n\t  "},
		{Title: "🚀🔥", Eyebrow: "😀", Footer: "\x00\x01"},
		{Title: "\xff\xfe invalid utf-8 \xc3"},
		{Title: strings.Repeat("ñ", 5000), Eyebrow: strings.Repeat("Ü", 500), Footer: strings.Repeat("a b ", 500)},
		{Title: "n\u0303o\u0308 q\u0303 a\u0301\u0301\u0301", Layout: "BOLD"},
		{Title: "\u0301\u0301 marks first", Layout: "minimal"},
		{Title: "中文标题 with Latin", Layout: "nope"},
		{Title: "x", Logo: image.NewRGBA(image.Rect(0, 0, 0, 0))},
		{Title: "x", Logo: image.NewRGBA(image.Rect(10, 10, 400, 40)), Layout: "bold"},
		{Title: "x", Logo: image.NewGray(image.Rect(0, 0, 30, 300)), Layout: "minimal"},
		{Title: "x", Style: Style{Background: color.Transparent, Foreground: color.Alpha{0x80}}},
	}
	for i, c := range cases {
		b, err := Render(c)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width != Width || cfg.Height != Height {
			t.Fatalf("case %d: %v %+v", i, err, cfg)
		}
		if *outDir != "" {
			os.MkdirAll(*outDir, 0o755)
			os.WriteFile(filepath.Join(*outDir, fmt.Sprintf("odd-%02d.png", i)), b, 0o644)
		}
	}
}

func TestClean(t *testing.T) {
	fs, err := loadFonts()
	if err != nil {
		t.Fatal(err)
	}
	has := newMetrics(fs.bold).has
	tests := []struct{ in, want string }{
		{"  Hola \n\t mundo  ", "Hola mundo"},
		{"Cafe\u0301", "Café"},                                       // NFC
		{"🚀 Launch day", "Launch day"},                               // no glyph
		{"a\u200bb\u00adc", "abc"},                                   // format characters
		{"Qu'est-ce que c'est\u00a0?", "Qu'est-ce que c'est\u00a0?"}, // NBSP kept
		{"😀\u0301x", "x"},                                            // mark on a dropped base
	}
	for _, tt := range tests {
		if got := clean(tt.in, 100, has); got != tt.want {
			t.Errorf("clean(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := clean(strings.Repeat("é", 400), maxTitleRunes, nil); len([]rune(got)) != maxTitleRunes {
		t.Errorf("title not capped: %d runes", len([]rune(got)))
	}
}

func TestKey(t *testing.T) {
	logo := testLogo()
	base := Card{Title: "Hola", Eyebrow: "Blog", Footer: "1 min", SiteName: "S", Logo: logo, Style: mustStyle(t, "#fff", "#000", "#666", "#f00")}
	k := Key(base)
	if len(k) != 32 || k != Key(base) {
		t.Fatalf("unstable key %q", k)
	}
	same := base
	same.Title = "  Hola  "
	same.Layout = "unknown"
	same.Logo = testLogo()
	if Key(same) != k {
		t.Error("equivalent cards have different keys")
	}
	if Key(Card{Title: "Cafe\u0301"}) != Key(Card{Title: "Café"}) {
		t.Error("key is not normalised")
	}
	if Key(Card{Title: "x"}) == Key(Card{Title: "x", Style: Style{Dark: true}}) {
		t.Error("dark default palette does not alter the key")
	}
	changes := []func(*Card){
		func(c *Card) { c.Title = "Adiós" },
		func(c *Card) { c.Eyebrow = "News" },
		func(c *Card) { c.Footer = "2 min" },
		func(c *Card) { c.SiteName = "T" },
		func(c *Card) { c.Layout = "bold" },
		func(c *Card) { c.Style.Accent = color.NRGBA{0, 0, 255, 255} },
		func(c *Card) { c.Logo = nil },
		func(c *Card) {
			m := testLogo().(*image.NRGBA)
			m.Pix[1000]++
			c.Logo = m
		},
		func(c *Card) { c.Logo = testLogo().(*image.NRGBA).SubImage(image.Rect(0, 0, 128, 128)) },
	}
	for i, change := range changes {
		c := base
		change(&c)
		if Key(c) == k {
			t.Errorf("change %d does not alter the key", i)
		}
	}
}

func TestStyleFromHex(t *testing.T) {
	s, err := StyleFromHex("#123", "fafafa", "", "#ABCDEF")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Dark || s.Muted != nil {
		t.Errorf("style = %+v", s)
	}
	if got := s.Background.(color.NRGBA); got != (color.NRGBA{0x11, 0x22, 0x33, 0xff}) {
		t.Errorf("bg = %v", got)
	}
	if got := s.Accent.(color.NRGBA); got != (color.NRGBA{0xab, 0xcd, 0xef, 0xff}) {
		t.Errorf("accent = %v", got)
	}
	for _, bad := range []string{"#12", "#ggg", "1234567", "red"} {
		if _, err := StyleFromHex(bad, "", "", ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if s, _ := StyleFromHex("", "", "", ""); s.Dark || s.Background != nil {
		t.Errorf("empty style = %+v", s)
	}
}

func TestContrast(t *testing.T) {
	if r := contrast(white, black); r < 20.9 || r > 21.1 {
		t.Fatalf("white/black = %v", r)
	}
	bgs := []color.NRGBA{white, {0xfb, 0xf7, 0xf0, 0xff}, {0x0b, 0x10, 0x20, 0xff}, {0x77, 0x77, 0x77, 0xff}, {0xfa, 0xcc, 0x15, 0xff}}
	fgs := []color.NRGBA{{0xcc, 0xcc, 0xcc, 0xff}, {0x81, 0x8c, 0xf8, 0xff}, {0x70, 0x70, 0x70, 0xff}, white, black}
	for _, bg := range bgs {
		for _, fg := range fgs {
			got := legible(fg, 4.5, bg)
			if contrast(got, bg) < 4.5 {
				t.Errorf("legible(%v on %v) = %v, contrast %.2f", fg, bg, got, contrast(got, bg))
			}
			if contrast(fg, bg) >= 4.5 && got != fg {
				t.Errorf("legible changed %v on %v although it was readable", fg, bg)
			}
		}
	}
	// A near-invisible title colour must come out readable in every layout.
	for _, layout := range []string{"classic", "bold", "minimal"} {
		pale := Style{Background: white, Foreground: color.NRGBA{0xf2, 0xf2, 0xf2, 0xff}, Accent: color.NRGBA{0xf5, 0xf5, 0xf5, 0xff}}
		b, err := Render(Card{Title: "Hola", Style: pale, Layout: layout})
		if err != nil {
			t.Fatal(err)
		}
		img, _ := png.Decode(bytes.NewReader(b))
		best := 1.0
		for y := 0; y < Height; y += 2 {
			for x := 0; x < Width; x += 2 {
				r, g, bl, _ := img.At(x, y).RGBA()
				px := color.NRGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 0xff}
				best = max(best, contrast(px, white), contrast(px, opaque(img.At(0, 0), white)))
			}
		}
		if best < 4.5 {
			t.Errorf("%s: best text contrast %.2f", layout, best)
		}
	}
}

func TestWrap(t *testing.T) {
	fs, _ := loadFonts()
	m := newMetrics(fs.bold)
	tf := m.at(60, titleTrack)
	lines, broken := tf.wrap("Donaudampfschifffahrtsgesellschaftskapitän", 500)
	if broken == 0 || len(lines) < 2 {
		t.Fatalf("lines = %q", lines)
	}
	for _, l := range lines {
		if tf.width(l) > 500 {
			t.Errorf("%q is %.0f px wide", l, tf.width(l))
		}
	}
	if !strings.HasSuffix(lines[0], "-") {
		t.Errorf("no hyphen in %q", lines[0])
	}
	b := fitTitle(m, strings.Repeat("palabra ", 80), 1000, 400, 76)
	if len(b.lines) != titleLines || !strings.HasSuffix(b.lines[len(b.lines)-1], ellipsis) || b.tf.size != titleMin {
		t.Errorf("long title: %v lines at %v px: %q", len(b.lines), b.tf.size, b.lines)
	}
	if b := fitTitle(m, "Hola", 1000, 400, 76); len(b.lines) != 1 || b.tf.size != 76 {
		t.Errorf("short title: %q at %v", b.lines, b.tf.size)
	}
}

func BenchmarkRender(b *testing.B) {
	for _, s := range samples(b) {
		if !strings.HasSuffix(s.name, "-es") {
			continue
		}
		b.Run(s.name, func(b *testing.B) {
			for b.Loop() {
				if _, err := Render(s.card); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Renders share the parsed fonts; run with -race.
func TestRenderConcurrent(t *testing.T) {
	ss := samples(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			if _, err := Render(ss[i%len(ss)].card); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
