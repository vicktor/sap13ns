package ogcard

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"

	"golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

type palette struct {
	bg, fg, muted, accent color.NRGBA
	dark                  bool // the background is dark
}

var (
	black = color.NRGBA{0, 0, 0, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// resolve fills unset colours from the default palette and flattens
// translucent ones onto the background.
func resolve(s Style) palette {
	def := lightPalette
	if s.Dark {
		def = darkPalette
	}
	pick := func(c color.Color, fallback, over color.NRGBA) color.NRGBA {
		if c == nil {
			return fallback
		}
		if _, _, _, a := c.RGBA(); a == 0 {
			return fallback
		}
		return opaque(c, over)
	}
	p := palette{bg: pick(s.Background, def.bg, def.bg)}
	p.fg = pick(s.Foreground, def.fg, p.bg)
	p.muted = pick(s.Muted, def.muted, p.bg)
	p.accent = pick(s.Accent, def.accent, p.bg)
	p.dark = isDark(p.bg)
	return p
}

// isDark reports whether light text reads better than dark text on c.
func isDark(c color.NRGBA) bool { return contrast(c, white) > contrast(c, black) }

// opaque composites c over the opaque colour over.
func opaque(c color.Color, over color.NRGBA) color.NRGBA {
	r, g, b, a := c.RGBA()
	inv := 0xffff - a
	ch := func(v uint32, o uint8) uint8 {
		return uint8((v + uint32(o)*0x101*inv/0xffff) >> 8)
	}
	return color.NRGBA{ch(r, over.R), ch(g, over.G), ch(b, over.B), 0xff}
}

// mix interpolates between two opaque colours in sRGB space.
func mix(a, b color.NRGBA, t float64) color.NRGBA {
	ch := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return color.NRGBA{ch(a.R, b.R), ch(a.G, b.G), ch(a.B, b.B), 0xff}
}

// alpha returns c with opacity a in [0, 1].
func alpha(c color.NRGBA, a float64) *image.Uniform {
	c.A = uint8(math.Round(max(0, min(1, a)) * 255))
	return image.NewUniform(c)
}

// luminance is the WCAG relative luminance.
func luminance(c color.NRGBA) float64 {
	lin := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// contrast is the WCAG contrast ratio, from 1 to 21.
func contrast(a, b color.NRGBA) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// legible returns fg, or fg moved towards black or white just enough to reach
// the minimum contrast ratio against every background.
func legible(fg color.NRGBA, minRatio float64, bgs ...color.NRGBA) color.NRGBA {
	worst := func(c color.NRGBA) float64 {
		w := 21.0
		for _, bg := range bgs {
			w = min(w, contrast(c, bg))
		}
		return w
	}
	if worst(fg) >= minRatio {
		return fg
	}
	target := black
	if worst(white) > worst(black) {
		target = white
	}
	if worst(target) < minRatio {
		return target
	}
	lo, hi := 0.0, 1.0
	for range 16 {
		mid := (lo + hi) / 2
		if worst(mix(fg, target, mid)) >= minRatio {
			hi = mid
		} else {
			lo = mid
		}
	}
	return mix(fg, target, hi)
}

// onColor picks whichever of the candidates reads best on bg, adjusted to
// a 4.5:1 contrast.
func onColor(bg color.NRGBA, candidates ...color.NRGBA) color.NRGBA {
	best := white
	if len(candidates) > 0 {
		best = candidates[0]
		for _, c := range candidates[1:] {
			if contrast(c, bg) > contrast(best, bg) {
				best = c
			}
		}
	}
	return legible(best, 4.5, bg)
}

// canvas is the card being drawn.
type canvas struct {
	img *image.RGBA
	z   vector.Rasterizer
}

// pen builds a path in canvas coordinates on a rasterizer that only covers
// the shape's bounding box.
type pen struct {
	z      *vector.Rasterizer
	ox, oy float64
}

func (p pen) moveTo(x, y float64) { p.z.MoveTo(float32(x-p.ox), float32(y-p.oy)) }
func (p pen) lineTo(x, y float64) { p.z.LineTo(float32(x-p.ox), float32(y-p.oy)) }
func (p pen) cubeTo(x1, y1, x2, y2, x, y float64) {
	p.z.CubeTo(float32(x1-p.ox), float32(y1-p.oy), float32(x2-p.ox), float32(y2-p.oy), float32(x-p.ox), float32(y-p.oy))
}

// kappa places cubic Bézier control points to approximate a quarter circle.
const kappa = 0.5522847498

// circle adds a circle; reverse winding cuts a hole in an enclosing shape.
func (p pen) circle(cx, cy, r float64, reverse bool) {
	k := r * kappa
	p.moveTo(cx+r, cy)
	if reverse {
		p.cubeTo(cx+r, cy-k, cx+k, cy-r, cx, cy-r)
		p.cubeTo(cx-k, cy-r, cx-r, cy-k, cx-r, cy)
		p.cubeTo(cx-r, cy+k, cx-k, cy+r, cx, cy+r)
		p.cubeTo(cx+k, cy+r, cx+r, cy+k, cx+r, cy)
	} else {
		p.cubeTo(cx+r, cy+k, cx+k, cy+r, cx, cy+r)
		p.cubeTo(cx-k, cy+r, cx-r, cy+k, cx-r, cy)
		p.cubeTo(cx-r, cy-k, cx-k, cy-r, cx, cy-r)
		p.cubeTo(cx+k, cy-r, cx+r, cy-k, cx+r, cy)
	}
	p.z.ClosePath()
}

func (p pen) roundRect(x0, y0, x1, y1, r float64) {
	r = max(0, min(r, (x1-x0)/2, (y1-y0)/2))
	k := r * (1 - kappa)
	p.moveTo(x0+r, y0)
	p.lineTo(x1-r, y0)
	p.cubeTo(x1-k, y0, x1, y0+k, x1, y0+r)
	p.lineTo(x1, y1-r)
	p.cubeTo(x1, y1-k, x1-k, y1, x1-r, y1)
	p.lineTo(x0+r, y1)
	p.cubeTo(x0+k, y1, x0, y1-k, x0, y1-r)
	p.lineTo(x0, y0+r)
	p.cubeTo(x0, y0+k, x0+k, y0, x0+r, y0)
	p.z.ClosePath()
}

// fill rasterises the path drawn by build inside the box (x0, y0)-(x1, y1)
// and composites src through it. For image sources, origin is the canvas
// point where src's minimum point lands.
func (c *canvas) fill(x0, y0, x1, y1 float64, src image.Image, origin image.Point, build func(pen)) {
	box := image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1))).Intersect(c.img.Bounds())
	if box.Empty() {
		return
	}
	c.z.Reset(box.Dx(), box.Dy())
	build(pen{z: &c.z, ox: float64(box.Min.X), oy: float64(box.Min.Y)})
	c.z.Draw(c.img, box, src, src.Bounds().Min.Add(box.Min.Sub(origin)))
}

func (c *canvas) circle(cx, cy, r float64, src image.Image) {
	c.fill(cx-r, cy-r, cx+r, cy+r, src, image.Point{}, func(p pen) { p.circle(cx, cy, r, false) })
}

func (c *canvas) ring(cx, cy, r, width float64, src image.Image) {
	o := r + width/2
	c.fill(cx-o, cy-o, cx+o, cy+o, src, image.Point{}, func(p pen) {
		p.circle(cx, cy, o, false)
		p.circle(cx, cy, r-width/2, true)
	})
}

func (c *canvas) roundRect(x0, y0, x1, y1, r float64, src image.Image) {
	c.fill(x0, y0, x1, y1, src, image.Point{}, func(p pen) { p.roundRect(x0, y0, x1, y1, r) })
}

// bayer8 is an 8×8 ordered-dither matrix.
var bayer8 = [64]uint8{
	0, 32, 8, 40, 2, 34, 10, 42,
	48, 16, 56, 24, 50, 18, 58, 26,
	12, 44, 4, 36, 14, 46, 6, 38,
	60, 28, 52, 20, 62, 30, 54, 22,
	3, 35, 11, 43, 1, 33, 9, 41,
	51, 19, 59, 27, 49, 17, 57, 25,
	15, 47, 7, 39, 13, 45, 5, 37,
	63, 31, 55, 23, 61, 29, 53, 21,
}

// gradient paints the canvas with a vertical gradient from top to bottom,
// ordered-dithered so that it does not band. The gradient is kept vertical on
// purpose: every row is then a repetition of one 8-pixel dither pattern,
// which PNG compresses to almost nothing, whereas a dithered diagonal or
// radial gradient adds well over 100 KB to the file.
func (c *canvas) gradient(top, bottom color.NRGBA) {
	b := c.img.Bounds()
	w, h := b.Dx(), b.Dy()
	from := [3]float64{float64(top.R), float64(top.G), float64(top.B)}
	to := [3]float64{float64(bottom.R), float64(bottom.G), float64(bottom.B)}
	for y := range h {
		t := (float64(y) + 0.5) / float64(h)
		t = t * t * (3 - 2*t) // ease the ends
		var v [3]float64
		for i := range v {
			v[i] = from[i] + (to[i]-from[i])*t
		}
		var pattern [8 * 4]uint8
		for x := range 8 {
			n := (float64(bayer8[(y&7)*8+x])+0.5)/64 - 0.5
			pattern[x*4] = quant(v[0] + n)
			pattern[x*4+1] = quant(v[1] + n)
			pattern[x*4+2] = quant(v[2] + n)
			pattern[x*4+3] = 0xff
		}
		row := c.img.Pix[y*c.img.Stride : y*c.img.Stride+w*4]
		for x := 0; x < len(row); x += len(pattern) {
			copy(row[x:], pattern[:])
		}
	}
}

func quant(v float64) uint8 {
	return uint8(max(0, min(255, math.Round(v))))
}

// logo draws img scaled to fit a size×size square at (x, y), clipped to a
// rounded rectangle.
func (c *canvas) logo(img image.Image, x, y, size, radius float64) {
	b := img.Bounds()
	w, h := int(math.Round(size)), int(math.Round(size))
	if b.Dx() > b.Dy() {
		h = max(1, int(math.Round(size*float64(b.Dy())/float64(b.Dx()))))
	} else if b.Dy() > b.Dx() {
		w = max(1, int(math.Round(size*float64(b.Dx())/float64(b.Dy()))))
	}
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	var scaler draw.Scaler = draw.CatmullRom
	if b.Dx()*b.Dy() > 4_000_000 {
		scaler = draw.ApproxBiLinear
	}
	scaler.Scale(scaled, scaled.Bounds(), img, b, draw.Src, nil)
	ix := int(math.Round(x + (size-float64(w))/2))
	iy := int(math.Round(y + (size-float64(h))/2))
	r := min(radius, float64(min(w, h))/4)
	fx, fy := float64(ix), float64(iy)
	c.fill(fx, fy, fx+float64(w), fy+float64(h), scaled, image.Pt(ix, iy), func(p pen) {
		p.roundRect(fx, fy, fx+float64(w), fy+float64(h), r)
	})
}

// imageChecksum hashes the pixels of img inside its bounds.
func imageChecksum(img image.Image) []byte {
	h := sha256.New()
	b := img.Bounds()
	fmt.Fprintf(h, "%T", img)
	rows := func(pix []byte, stride, off, n int) {
		for y := 0; y < b.Dy(); y++ {
			i := off + y*stride
			h.Write(pix[i : i+n])
		}
	}
	switch m := img.(type) {
	case *image.RGBA:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx()*4)
	case *image.NRGBA:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx()*4)
	case *image.RGBA64:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx()*8)
	case *image.NRGBA64:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx()*8)
	case *image.Gray:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx())
	case *image.Alpha:
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx())
	case *image.Paletted:
		for _, c := range m.Palette {
			if c == nil {
				continue
			}
			r, g, bl, a := c.RGBA()
			binary.Write(h, binary.BigEndian, [4]uint16{uint16(r), uint16(g), uint16(bl), uint16(a)})
		}
		rows(m.Pix, m.Stride, m.PixOffset(b.Min.X, b.Min.Y), b.Dx())
	case *image.YCbCr:
		fmt.Fprint(h, m.SubsampleRatio)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			yi := m.YOffset(b.Min.X, y)
			h.Write(m.Y[yi : yi+b.Dx()])
			c0, c1 := m.COffset(b.Min.X, y), m.COffset(b.Max.X-1, y)+1
			h.Write(m.Cb[c0:c1])
			h.Write(m.Cr[c0:c1])
		}
	default:
		var px [8]byte
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := img.At(x, y).RGBA()
				binary.BigEndian.PutUint16(px[0:], uint16(r))
				binary.BigEndian.PutUint16(px[2:], uint16(g))
				binary.BigEndian.PutUint16(px[4:], uint16(bl))
				binary.BigEndian.PutUint16(px[6:], uint16(a))
				h.Write(px[:])
			}
		}
	}
	return h.Sum(nil)
}
