// Package ogcard draws Open Graph / social card images (1200×630 PNG) for
// posts and pages that have no featured image. Cards are composed from the
// post's own text with embedded Inter fonts and anti-aliased vector shapes:
// no network access and no generative models are involved, so the same input
// always produces the same image.
package ogcard

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Card dimensions in pixels, the size recommended by Facebook, LinkedIn and X.
const (
	Width  = 1200
	Height = 630
)

// version is mixed into Key so cached cards are regenerated when the design
// changes. Bump it whenever the rendering output changes.
const version = "ogcard/1"

const (
	maxTitleRunes = 300
	maxFieldRunes = 120
	maxLogoPixels = 40_000_000 // decompression-bomb sized logos are ignored
)

var ErrBadColor = errors.New("invalid hex colour")

// Card is the content of one social card. Text is normalised to NFC, capped
// at 300 runes for the title and 120 for the other fields, and characters the
// embedded fonts lack (emoji, CJK…) are left out.
type Card struct {
	Title    string      // post title, required; the site name stands in when empty
	Eyebrow  string      // e.g. category name or "Page"
	Footer   string      // e.g. site name · date · "5 min read"
	SiteName string      // shown next to the logo
	Logo     image.Image // optional, square works best: top-left (bold, minimal) or in the byline (classic), ~56–72px
	Style    Style       // colours
	Layout   string      // "classic" | "bold" | "minimal"; unknown → classic
}

// Style holds the card colours. Nil colours are replaced by a light or dark
// default palette depending on Dark; decorations adapt to the actual
// luminance of Background. Text colours are adjusted when their contrast
// against the background is too low to read.
type Style struct {
	Background, Foreground, Muted, Accent color.Color
	Dark                                  bool
}

var (
	lightPalette = palette{
		bg:     color.NRGBA{0xff, 0xff, 0xff, 0xff},
		fg:     color.NRGBA{0x0f, 0x17, 0x2a, 0xff},
		muted:  color.NRGBA{0x64, 0x74, 0x8b, 0xff},
		accent: color.NRGBA{0x4f, 0x46, 0xe5, 0xff},
	}
	darkPalette = palette{
		bg:     color.NRGBA{0x0b, 0x10, 0x20, 0xff},
		fg:     color.NRGBA{0xf8, 0xfa, 0xfc, 0xff},
		muted:  color.NRGBA{0x94, 0xa3, 0xb8, 0xff},
		accent: color.NRGBA{0x81, 0x8c, 0xf8, 0xff},
		dark:   true,
	}
)

// StyleFromHex builds a Style from CSS hex colours ("#rgb" or "#rrggbb", the
// "#" is optional). Empty values take the default for the background's
// brightness, so StyleFromHex("#111", "", "", "") yields a dark style.
func StyleFromHex(bg, fg, muted, accent string) (Style, error) {
	var s Style
	cols := []*color.Color{&s.Background, &s.Foreground, &s.Muted, &s.Accent}
	for i, v := range []string{bg, fg, muted, accent} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		c, err := parseHex(v)
		if err != nil {
			return Style{}, err
		}
		*cols[i] = c
	}
	if s.Background != nil {
		s.Dark = isDark(opaque(s.Background, lightPalette.bg))
	}
	return s, nil
}

func parseHex(s string) (color.NRGBA, error) {
	h := strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return color.NRGBA{}, fmt.Errorf("%w: %q", ErrBadColor, s)
	}
	var b [3]byte
	if _, err := hex.Decode(b[:], []byte(h)); err != nil {
		return color.NRGBA{}, fmt.Errorf("%w: %q", ErrBadColor, s)
	}
	return color.NRGBA{b[0], b[1], b[2], 0xff}, nil
}

// Render draws the card and returns it PNG encoded. It never panics: a
// failure inside the renderer (for example a misbehaving Logo
// implementation) is returned as an error.
func Render(c Card) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("ogcard: render: %v", r)
		}
	}()
	fs, err := loadFonts()
	if err != nil {
		return nil, err
	}
	f := newFaces(fs)
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	compose(img, prepare(c, &f), f)
	var buf bytes.Buffer
	buf.Grow(128 << 10)
	// Default compression is ~12% smaller than BestSpeed for ~2 ms more.
	enc := png.Encoder{CompressionLevel: png.DefaultCompression, BufferPool: encoderBuffers}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encoderBuffers recycles the PNG encoder's compression state between renders.
var encoderBuffers = &bufferPool{}

type bufferPool struct{ p sync.Pool }

func (b *bufferPool) Get() *png.EncoderBuffer {
	e, _ := b.p.Get().(*png.EncoderBuffer)
	return e
}

func (b *bufferPool) Put(e *png.EncoderBuffer) { b.p.Put(e) }

// Key returns a stable hash of everything that affects the rendered image,
// suitable as a cache key or file name.
func Key(c Card) string {
	p := prepare(c, nil)
	h := sha256.New()
	field := func(s string) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	field(version)
	field(p.layout)
	field(p.title)
	field(p.eyebrow)
	field(p.footer)
	field(p.site)
	// The resolved colours already reflect Style.Dark.
	for _, c := range []color.NRGBA{p.pal.bg, p.pal.fg, p.pal.muted, p.pal.accent} {
		h.Write([]byte{c.R, c.G, c.B})
	}
	if p.logo != nil {
		b := p.logo.Bounds()
		field(fmt.Sprintf("logo %d,%d,%d,%d", b.Min.X, b.Min.Y, b.Max.X, b.Max.Y))
		h.Write(imageChecksum(p.logo))
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// card is a Card with cleaned text and resolved colours.
type card struct {
	title, eyebrow, footer, site string
	logo                         image.Image
	pal                          palette
	layout                       string
}

// prepare cleans the text fields and resolves the palette. When f is not
// nil, characters the fonts cannot draw (emoji, CJK…) are removed.
func prepare(c Card, f *faces) card {
	var titleHas, textHas func(rune) bool
	if f != nil {
		titleHas, textHas = f.bold.has, f.medium.has
	}
	p := card{
		title:   clean(c.Title, maxTitleRunes, titleHas),
		eyebrow: clean(c.Eyebrow, maxFieldRunes, textHas),
		footer:  clean(c.Footer, maxFieldRunes, textHas),
		site:    clean(c.SiteName, maxFieldRunes, textHas),
		pal:     resolve(c.Style),
	}
	if p.title == "" {
		p.title = clean(c.SiteName, maxFieldRunes, titleHas)
	}
	switch l := strings.ToLower(strings.TrimSpace(c.Layout)); l {
	case "bold", "minimal":
		p.layout = l
	default:
		p.layout = "classic"
	}
	if c.Logo != nil {
		if b := c.Logo.Bounds(); !b.Empty() && b.Dx()*b.Dy() <= maxLogoPixels && b.Dx() < 1<<15 && b.Dy() < 1<<15 {
			p.logo = c.Logo
		}
	}
	return p
}

// clean normalises s to NFC, removes control and format characters, collapses
// white space (keeping no-break spaces) and caps the length. When has is not
// nil, runes it rejects are dropped together with any marks attached to them.
func clean(s string, limit int, has func(rune) bool) string {
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) > limit*4 {
		s = string([]rune(s)[:limit*4])
	}
	s = norm.NFC.String(s)
	var b strings.Builder
	space, dropped := false, false
	for _, r := range s {
		switch {
		case r == '\u00a0' || r == '\u202f' || r == '\u2007':
			r = '\u00a0'
		case unicode.IsSpace(r):
			space = true
			continue
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r) || r == utf8.RuneError:
			continue
		}
		if isMark(r) {
			if dropped || b.Len() == 0 || space || (has != nil && !has(r)) {
				continue
			}
		} else {
			dropped = has != nil && !has(r)
			if dropped {
				continue
			}
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return capClusters(strings.Trim(b.String(), "\u00a0"), limit)
}

// capClusters truncates s to at most limit runes without separating a base
// character from its combining marks.
func capClusters(s string, limit int) string {
	n := 0
	for i, r := range s {
		if !isMark(r) && n >= limit {
			return strings.TrimRight(s[:i], " \u00a0")
		}
		n++
	}
	return s
}

func isMark(r rune) bool {
	return r >= 0x300 && unicode.In(r, unicode.Mn, unicode.Me)
}
