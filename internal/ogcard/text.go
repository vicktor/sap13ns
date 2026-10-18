package ogcard

import (
	_ "embed"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/text/unicode/norm"
)

// The fonts are Inter 4.1 by Rasmus Andersson (https://github.com/rsms/inter,
// SIL Open Font License 1.1, see fonts/OFL.txt): Inter Display Bold for titles
// and Inter Medium for everything else, taken from the release's extras/ttf.
// They were subset with HarfBuzz to Latin (Basic, Latin-1, Extended-A/B and
// Extended Additional), IPA and spacing modifiers, combining diacritics,
// Greek, Cyrillic, punctuation, currency and a few symbols, keeping only
// kerning and dropping hinting (likewise for InterDisplay-Bold.ttf):
//
//	hb-subset Inter-Medium.ttf --layout-features=kern --no-hinting \
//	  --unicodes=U+0020-007E,U+00A0-00FF,U+0100-024F,U+0250-02FF,U+0300-036F,U+0370-03FF,U+0400-04FF,U+1E00-1EFF,U+2000-206F,U+20A0-20CF,U+2100-214F,U+2190-2199,U+2212,U+2215,U+221E,U+2248,U+2260,U+2264,U+2265,U+2116,U+25CF,U+FB01,U+FB02 \
//	  -o fonts/Inter-Medium.ttf
var (
	//go:embed fonts/InterDisplay-Bold.ttf
	boldTTF []byte
	//go:embed fonts/Inter-Medium.ttf
	mediumTTF []byte
)

const (
	ellipsisRune = rune(0x2026)
	ellipsis     = string(ellipsisRune)
)

// fontFile is a parsed font. sfnt.Font is safe for concurrent use as long as
// every goroutine brings its own sfnt.Buffer.
type fontFile struct {
	f                           *sfnt.Font
	upem                        float64
	descent, capHeight, xHeight float64 // in em
}

type fontSet struct{ bold, medium *fontFile }

var (
	fontsOnce sync.Once
	fonts     *fontSet
	fontsErr  error
)

// loadFonts parses the embedded fonts on first use.
func loadFonts() (*fontSet, error) {
	fontsOnce.Do(func() {
		var fs fontSet
		if fs.bold, fontsErr = parseFont(boldTTF); fontsErr != nil {
			return
		}
		if fs.medium, fontsErr = parseFont(mediumTTF); fontsErr != nil {
			return
		}
		fonts = &fs
	})
	return fonts, fontsErr
}

func parseFont(b []byte) (*fontFile, error) {
	f, err := opentype.Parse(b)
	if err != nil {
		return nil, err
	}
	upem := int(f.UnitsPerEm())
	m, err := f.Metrics(nil, fixed.I(upem), font.HintingNone)
	if err != nil {
		return nil, err
	}
	em := func(v fixed.Int26_6) float64 { return float64(v) / 64 / float64(upem) }
	return &fontFile{
		f: f, upem: float64(upem),
		descent: em(m.Descent), capHeight: em(m.CapHeight), xHeight: em(m.XHeight),
	}, nil
}

// metrics caches glyph indices, advances, bounds and kerning of a font, in
// em, for one render.
type metrics struct {
	ff   *fontFile
	buf  sfnt.Buffer
	gid  map[rune]sfnt.GlyphIndex
	adv  map[rune]float64
	kern map[[2]rune]float64
	box  map[rune]box
}

// box is a glyph's ink bounds in em, y pointing down from the baseline.
type box struct{ x0, y0, x1, y1 float64 }

func newMetrics(ff *fontFile) *metrics {
	return &metrics{ff: ff, gid: map[rune]sfnt.GlyphIndex{}, adv: map[rune]float64{}, kern: map[[2]rune]float64{}, box: map[rune]box{}}
}

func (m *metrics) glyph(r rune) sfnt.GlyphIndex {
	g, ok := m.gid[r]
	if !ok {
		g, _ = m.ff.f.GlyphIndex(&m.buf, r)
		m.gid[r] = g
	}
	return g
}

// has reports whether the font can draw r.
func (m *metrics) has(r rune) bool { return m.glyph(r) != 0 }

func (m *metrics) advance(r rune) float64 {
	a, ok := m.adv[r]
	if !ok {
		v, err := m.ff.f.GlyphAdvance(&m.buf, m.glyph(r), fixed.I(int(m.ff.upem)), font.HintingNone)
		if err == nil {
			a = float64(v) / 64 / m.ff.upem
		}
		m.adv[r] = a
	}
	return a
}

func (m *metrics) bounds(r rune) box {
	b, ok := m.box[r]
	if !ok {
		v, _, err := m.ff.f.GlyphBounds(&m.buf, m.glyph(r), fixed.I(int(m.ff.upem)), font.HintingNone)
		if err == nil {
			em := func(v fixed.Int26_6) float64 { return float64(v) / 64 / m.ff.upem }
			b = box{em(v.Min.X), em(v.Min.Y), em(v.Max.X), em(v.Max.Y)}
		}
		m.box[r] = b
	}
	return b
}

func (m *metrics) kerning(a, b rune) float64 {
	k, ok := m.kern[[2]rune{a, b}]
	if !ok {
		v, err := m.ff.f.Kern(&m.buf, m.glyph(a), m.glyph(b), fixed.I(int(m.ff.upem)), font.HintingNone)
		if err == nil {
			k = float64(v) / 64 / m.ff.upem
		}
		m.kern[[2]rune{a, b}] = k
	}
	return k
}

// typeface is a font at one pixel size with letter-spacing.
type typeface struct {
	m     *metrics
	size  float64
	track float64   // extra space between characters, in em
	face  font.Face // rasterises glyphs; created on first draw
}

func (m *metrics) at(size, track float64) *typeface {
	return &typeface{m: m, size: size, track: track}
}

func (t *typeface) capHeight() float64 { return t.m.ff.capHeight * t.size }
func (t *typeface) descent() float64   { return t.m.ff.descent * t.size }

// layout calls fn with the byte offset of every rune of s and the pen
// position (in em) of the base character it belongs to, then returns the
// total advance. Combining marks take no space; draw places them over their
// base.
func (t *typeface) layout(s string, fn func(i int, r rune, pen float64)) float64 {
	pen, prev, base := 0.0, rune(-1), 0.0
	for i, r := range s {
		if isMark(r) {
			if fn != nil {
				fn(i, r, base)
			}
			continue
		}
		if prev >= 0 {
			pen += t.m.kerning(prev, r) + t.track
		}
		prev, base = r, pen
		if fn != nil {
			fn(i, r, pen)
		}
		pen += t.m.advance(r)
	}
	return pen
}

// width returns the advance width of s in pixels.
func (t *typeface) width(s string) float64 {
	return t.layout(s, nil) * t.size
}

// draw draws s with its baseline at y, starting at x.
func (t *typeface) draw(dst draw.Image, s string, x, y float64, col color.Color) {
	if t.face == nil {
		f, err := opentype.NewFace(t.m.ff.f, &opentype.FaceOptions{Size: t.size, DPI: 72, Hinting: font.HintingNone})
		if err != nil {
			return
		}
		t.face = f
	}
	src := image.NewUniform(col)
	// A whole-pixel baseline keeps flat glyph bottoms crisp.
	by := math.Round(y)
	var base rune
	var stack float64 // top of the base and the marks drawn on it so far, em above the baseline
	t.layout(s, func(_ int, r rune, pen float64) {
		dx, rise := 0.0, 0.0
		if isMark(r) {
			dx, rise = t.placeMark(base, r, &stack)
		} else {
			base, stack = r, -t.m.bounds(r).y0
		}
		if r == ' ' || r == 0xa0 || !t.m.has(r) {
			return
		}
		dot := fixed.Point26_6{
			X: fixed.Int26_6(math.Round((x + (pen+dx)*t.size) * 64)),
			Y: fixed.Int26_6(math.Round((by - rise*t.size) * 64)),
		}
		dr, mask, mp, _, ok := t.face.Glyph(dot, r)
		if ok && !dr.Empty() {
			draw.DrawMask(dst, dr, src, image.Point{}, mask, mp, draw.Over)
		}
	})
}

// placeMark positions a combining mark that NFC could not merge with its
// base (x/image does not apply the font's mark anchors): centred on the
// base, and raised above tall bases and earlier marks when it sits above
// the x-height. It returns the offset from the base's origin and the rise,
// in em, and updates stack, the height reached so far.
func (t *typeface) placeMark(base, mark rune, stack *float64) (dx, rise float64) {
	bb, mb := t.m.bounds(base), t.m.bounds(mark)
	dx = (bb.x0+bb.x1)/2 - (mb.x0+mb.x1)/2
	bottom := -mb.y1 // em above the baseline
	if bottom < t.m.ff.xHeight*0.8 {
		return dx, 0 // below or through the base
	}
	// The font draws the mark for a base reaching the x-height; keep that
	// gap above whatever is under it now.
	rise = max(0, *stack-t.m.ff.xHeight)
	*stack = -mb.y0 + rise
	return dx, rise
}

// wrap breaks s into lines no wider than maxW at spaces. Words that are wider
// than a line on their own are split, and broken counts how often.
func (t *typeface) wrap(s string, maxW float64) (lines []string, broken int) {
	line := ""
	for _, w := range strings.Split(s, " ") {
		if w == "" {
			continue
		}
		if line != "" {
			if c := line + " " + w; t.width(c) <= maxW {
				line = c
				continue
			}
			lines = append(lines, line)
			line = ""
		}
		for t.width(w) > maxW {
			head, tail := t.split(w, maxW)
			if tail == "" {
				break
			}
			lines = append(lines, head)
			broken++
			w = tail
		}
		line = w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines, broken
}

// split cuts an overlong word at the last character boundary that fits,
// adding a hyphen when the cut falls between two letters. At least one
// character is always kept on the first line.
func (t *typeface) split(w string, maxW float64) (head, tail string) {
	hy := t.m.advance('-') + t.track
	cut, first, stop := 0, 0, false
	t.layout(w, func(i int, r rune, pen float64) {
		if stop || i == 0 || isMark(r) {
			return
		}
		if first == 0 {
			first = i
		}
		extra := 0.0
		if hyphenate(w, i) {
			extra = hy
		}
		if (pen+extra)*t.size > maxW {
			stop = true
			return
		}
		cut = i
	})
	if cut == 0 {
		cut = first
	}
	if cut == 0 {
		return w, ""
	}
	cut = syllable(w, cut)
	head, tail = w[:cut], w[cut:]
	if hyphenate(w, cut) {
		head += "-"
	}
	return head, tail
}

// syllable moves a cut at byte offset i back by up to six characters to the
// nearest point before a consonant that starts a syllable, that is one
// followed by a vowel ("Schiff-fahrt", "elek-trizität"), without splitting
// the digraphs ch, sh, ph, th, ll and rr. It approximates hyphenation in most
// Latin-script languages; without such a point nearby the cut stays.
func syllable(w string, i int) int {
	for j, steps := i, 0; j > 0 && steps < 7; steps++ {
		if utf8.RuneCountInString(w[:j]) >= 2 && syllableStart(w, j) {
			return j
		}
		_, n := utf8.DecodeLastRuneInString(w[:j])
		j -= n
	}
	return i
}

func syllableStart(w string, j int) bool {
	p, _ := utf8.DecodeLastRuneInString(w[:j])
	c, n := utf8.DecodeRuneInString(w[j:])
	v, _ := utf8.DecodeRuneInString(w[j+n:])
	if !unicode.IsLetter(p) || !unicode.IsLetter(c) || isVowel(c) || !isVowel(v) {
		return false
	}
	p, c = unicode.ToLower(p), unicode.ToLower(c)
	return !(c == 'h' && strings.ContainsRune("cspt", p)) && !(c == p && (c == 'l' || c == 'r'))
}

func isVowel(r rune) bool {
	d, _ := utf8.DecodeRuneInString(norm.NFD.String(string(unicode.ToLower(r))))
	return strings.ContainsRune("aeiouyæøœ", d)
}

// hyphenate reports whether cutting w at byte offset i separates two letters.
func hyphenate(w string, i int) bool {
	before, after := w[:i], w[i:]
	b, n := utf8.DecodeLastRuneInString(before)
	for n > 0 && isMark(b) {
		before = before[:len(before)-n]
		b, n = utf8.DecodeLastRuneInString(before)
	}
	a, _ := utf8.DecodeRuneInString(after)
	return n > 0 && after != "" && unicode.IsLetter(b) && unicode.IsLetter(a)
}

// truncate shortens s to fit maxW with a trailing ellipsis. When more is set
// the ellipsis is added even if s fits, because text follows it. Cuts prefer
// word boundaries unless that would waste too much of the line.
func (t *typeface) truncate(s string, maxW float64, more bool) string {
	if !more && t.width(s) <= maxW {
		return s
	}
	ell := t.m.advance(ellipsisRune) + t.track
	cut, space := 0, -1
	done := false
	end := t.layout(s, func(i int, r rune, pen float64) {
		if done || i == 0 || isMark(r) {
			return
		}
		if (pen+ell)*t.size > maxW {
			done = true
			return
		}
		cut = i
		if r == ' ' {
			space = i
		}
	})
	if !done && (end+ell)*t.size <= maxW {
		cut = len(s)
	}
	if space > 0 && cut < len(s) && s[cut] != ' ' && t.width(s[:space]) >= 0.6*maxW {
		cut = space
	}
	head := strings.TrimRightFunc(s[:cut], func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",;:.-·–—", r)
	})
	return head + ellipsis
}

// fit returns s, ellipsised if it is wider than maxW.
func (t *typeface) fit(s string, maxW float64) string { return t.truncate(s, maxW, false) }
