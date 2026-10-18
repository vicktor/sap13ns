package ogcard

import (
	"image"
	"image/color"
	"math"
	"strings"

	"golang.org/x/image/draw"
)

const (
	titleMin   = 44.0
	titleLines = 4
	titleLead  = 1.12   // line height, in em
	titleTrack = -0.014 // letter-spacing, in em
	capsTrack  = 0.09   // letter-spacing for uppercase labels
)

// faces holds the font metrics of one render.
type faces struct{ bold, medium *metrics }

func newFaces(fs *fontSet) faces {
	return faces{bold: newMetrics(fs.bold), medium: newMetrics(fs.medium)}
}

func compose(img *image.RGBA, p card, f faces) {
	c := &canvas{img: img}
	switch p.layout {
	case "bold":
		c.bold(p, f)
	case "minimal":
		c.minimal(p, f)
	default:
		c.classic(p, f)
	}
}

// block is a wrapped title.
type block struct {
	tf    *typeface
	lines []string
	lead  float64 // distance between baselines
}

// height spans from the cap height of the first line to just below the
// descenders of the last one.
func (b block) height() float64 {
	if len(b.lines) == 0 {
		return 0
	}
	return b.tf.capHeight() + float64(len(b.lines)-1)*b.lead + b.tf.descent()*0.75
}

// draw draws the block with the top of its first line's capitals at top.
func (b block) draw(c *canvas, x, top float64, col color.NRGBA) {
	if len(b.lines) == 0 {
		return
	}
	y := top + b.tf.capHeight()
	for _, l := range b.lines {
		b.tf.draw(c.img, l, x, y, col)
		y += b.lead
	}
}

// fitTitle picks the largest size from maxSize down to titleMin at which s
// wraps into at most titleLines lines within maxW×maxH, preferring sizes that
// split fewer words. Text that does not fit even at the minimum size is
// ellipsised.
func fitTitle(m *metrics, s string, maxW, maxH, maxSize float64) block {
	if s == "" || maxW <= 0 {
		return block{}
	}
	try := func(size float64) (block, int) {
		tf := m.at(size, titleTrack)
		lines, broken := tf.wrap(s, maxW)
		return block{tf: tf, lines: lines, lead: math.Round(size * titleLead)}, broken
	}
	// The largest size that fits without splitting words wins. When every
	// size has to split (a single huge word), allow one split more than the
	// minimum so the title is not shrunk to the smallest size for nothing.
	type option struct {
		b      block
		broken int
	}
	var opts []option
	minBroken := -1
	for size := maxSize; size >= titleMin; size -= 2 {
		b, broken := try(size)
		if len(b.lines) > titleLines || b.height() > maxH {
			continue
		}
		opts = append(opts, option{b, broken})
		if minBroken < 0 || broken < minBroken {
			minBroken = broken
		}
		if broken == 0 {
			break
		}
	}
	for _, o := range opts {
		if o.broken == minBroken || (minBroken > 0 && o.broken == minBroken+1) {
			o.b.balance(s, maxW, o.broken)
			return o.b
		}
	}
	b, _ := try(titleMin)
	n := titleLines
	for n > 1 && b.tf.capHeight()+float64(n-1)*b.lead+b.tf.descent()*0.75 > maxH {
		n--
	}
	if len(b.lines) > n {
		b.lines = append(b.lines[:n-1:n-1], b.tf.truncate(b.lines[n-1], maxW, true))
	}
	return b
}

// balance narrows the measure as far as possible without adding a line or a
// word split, so lines come out with similar lengths instead of a lone word
// at the end.
func (b *block) balance(s string, maxW float64, broken int) {
	n := len(b.lines)
	if n < 2 {
		return
	}
	lo, hi := maxW/2, maxW
	for range 10 {
		mid := (lo + hi) / 2
		if lines, br := b.tf.wrap(s, mid); len(lines) == n && br <= broken {
			hi = mid
		} else {
			lo = mid
		}
	}
	b.lines, _ = b.tf.wrap(s, hi)
}

// line is one line of a text stack.
type line struct {
	tf   *typeface
	text string
	col  color.NRGBA
}

// stack draws lines under each other, ellipsised to maxW, vertically centred
// on cy and separated by gap pixels between a baseline and the next line's
// capitals.
func (c *canvas) stack(x, cy, maxW, gap float64, lines ...line) {
	var h float64
	var drawn []line
	for _, l := range lines {
		if l.text == "" {
			continue
		}
		if len(drawn) > 0 {
			h += gap
		}
		h += l.tf.capHeight()
		drawn = append(drawn, l)
	}
	y := cy - h/2
	for _, l := range drawn {
		y += l.tf.capHeight()
		l.tf.draw(c.img, l.tf.fit(l.text, maxW), x, y, l.col)
		y += gap
	}
}

// avatar draws the logo, or a monogram of the site name when there is no
// logo, in a size×size square. It returns false when it drew nothing.
func (c *canvas) avatar(p card, f faces, x, y, size float64, fill, ink color.NRGBA) bool {
	if p.logo != nil {
		c.logo(p.logo, x, y, size, size*0.22)
		return true
	}
	initial := firstCluster(p.site)
	if initial == "" {
		return false
	}
	initial = strings.ToUpper(initial)
	c.circle(x+size/2, y+size/2, size/2, alpha(fill, 1))
	tf := f.bold.at(size*0.46, 0)
	tf.draw(c.img, initial, x+size/2-tf.width(initial)/2, y+size/2+tf.capHeight()/2, ink)
	return true
}

func firstCluster(s string) string {
	for i, r := range s {
		if i > 0 && !isMark(r) {
			return s[:i]
		}
	}
	return s
}

// classic: a faint vertical gradient with a ripple of accent discs and rings
// in the top-right corner, an eyebrow pill (or an accent bar), the title, and
// a byline row at the bottom with the logo, site name and footer.
func (c *canvas) classic(p card, f faces) {
	const (
		padX, padTop, padBottom = 80.0, 72.0, 64.0
		row                     = 64.0 // byline height
	)
	pal := p.pal
	tint, discA, ringA, pillA := 0.05, 0.035, 0.22, 0.11
	if pal.dark {
		tint, discA, ringA, pillA = 0.06, 0.04, 0.32, 0.18
	}
	low := mix(pal.bg, pal.accent, tint)
	c.gradient(pal.bg, low)
	// A ripple of translucent discs and rings in the top-right corner.
	gx, gy := float64(Width)-150, 70.0
	radii := []float64{150, 250, 360, 480}
	for i := len(radii) - 1; i >= 0; i-- {
		c.circle(gx, gy, radii[i], alpha(pal.accent, discA))
	}
	for i, r := range radii {
		c.ring(gx, gy, r, 1.5, alpha(pal.accent, ringA*(1-0.22*float64(i))))
	}
	under := mix(low, pal.accent, 3*discA) // darkest spot the title may cross
	fg := legible(pal.fg, 4.5, pal.bg, low, under)
	muted := legible(pal.muted, 3, pal.bg, low)
	contentW := float64(Width) - 2*padX

	bottom := float64(Height) - padBottom
	if p.logo != nil || p.site != "" || p.footer != "" {
		top := bottom - row
		x := padX
		if c.avatar(p, f, x, top, row, pal.accent, onColor(pal.accent, pal.bg, pal.fg)) {
			x += row + 20
		}
		c.stack(x, top+row/2, contentW-(x-padX), 11,
			line{f.medium.at(26, -0.005), p.site, fg},
			line{f.medium.at(22, 0), p.footer, muted})
		bottom = top - 44
	}

	// Eyebrow pill, or a short accent bar when there is none.
	head := 44.0
	var drawHead func(y float64)
	if p.eyebrow != "" {
		lab := f.medium.at(19, capsTrack)
		text := lab.fit(strings.ToUpper(p.eyebrow), contentW-60)
		w := 18 + 8 + 12 + lab.width(text) + 20
		pill := mix(pal.bg, pal.accent, pillA)
		ink := legible(pal.accent, 4.5, pill)
		drawHead = func(y float64) {
			c.roundRect(padX, y, padX+w, y+head, head/2, alpha(pal.accent, pillA))
			c.circle(padX+22, y+head/2, 4.5, alpha(ink, 1))
			lab.draw(c.img, text, padX+38, y+head/2+lab.capHeight()/2, ink)
		}
	} else {
		drawHead = func(y float64) {
			c.roundRect(padX, y+head/2-4, padX+64, y+head/2+4, 4, alpha(pal.accent, 1))
		}
	}
	const gap = 40.0
	title := fitTitle(f.bold, p.title, contentW-60, bottom-padTop-head-gap, 76)
	// Centre eyebrow and title in the free space, slightly above the middle.
	group := head + gap + title.height()
	y := padTop + max(0, (bottom-padTop-group)*0.42)
	drawHead(y)
	title.draw(c, padX, y+head+gap, fg)
}

// bold: the accent fills the card, with a gradient towards a deeper (or
// lighter) shade, large rings and a dot grid, and the title in a contrasting
// colour anchored above the footer.
func (c *canvas) bold(p card, f faces) {
	const padX, padTop, padBottom = 80.0, 68.0, 68.0
	pal := p.pal
	base := pal.accent
	ink := onColor(base, pal.bg, pal.fg, white, black)
	// Shade away from the text colour so contrast only improves.
	away := black
	if luminance(ink) < luminance(base) {
		away = white
	}
	to := mix(base, away, 0.24)
	c.gradient(base, to)

	// Two soft discs in the top-right corner, rings growing from the
	// bottom-right one and a dot grid.
	c.circle(float64(Width)-20, -60, 420, alpha(ink, 0.05))
	c.circle(float64(Width)-20, -60, 280, alpha(ink, 0.05))
	cx, cy := float64(Width)-90, float64(Height)+30
	for i, r := range []float64{210, 320, 430} {
		c.ring(cx, cy, r, 2, alpha(ink, 0.16-0.04*float64(i)))
	}
	c.circle(cx, cy, 120, alpha(ink, 0.08))
	for i := range 5 {
		for j := range 4 {
			c.circle(float64(Width)-padX-float64(i)*24-3, padTop+8+float64(j)*24, 3, alpha(ink, 0.32))
		}
	}
	ink = legible(ink, 4.5, base, to, mix(base, ink, 0.1))
	soft := legible(mix(to, ink, 0.82), 4.5, base, to)

	contentW := float64(Width) - 2*padX
	top := padTop
	if p.logo != nil || p.site != "" {
		const size = 72.0
		x := padX
		if c.avatar(p, f, x, top, size, ink, base) {
			x += size + 22
		}
		c.stack(x, top+size/2, contentW-(x-padX)-140, 0, line{f.medium.at(28, -0.005), p.site, ink})
		top += size + 44
	}
	bottom := float64(Height) - padBottom
	if p.footer != "" {
		ft := f.medium.at(24, 0)
		ft.draw(c.img, ft.fit(p.footer, contentW), padX, bottom, soft)
		bottom -= ft.capHeight() + 52
	}
	var lab *typeface
	labH := 0.0
	if p.eyebrow != "" {
		lab = f.medium.at(21, capsTrack)
		labH = lab.capHeight() + 30
	}
	title := fitTitle(f.bold, p.title, contentW-40, bottom-top-labH, 80)
	y := bottom - title.height()
	if lab != nil {
		bar := 34.0
		ly := y - 30
		c.roundRect(padX, ly-lab.capHeight()/2-2, padX+bar, ly-lab.capHeight()/2+2, 2, alpha(soft, 1))
		lab.draw(c.img, lab.fit(strings.ToUpper(p.eyebrow), contentW-bar-16), padX+bar+16, ly, soft)
	}
	title.draw(c, padX, y, ink)
}

// minimal: a flat background, generous margins, the title in the middle and a
// thin rule above the footer.
func (c *canvas) minimal(p card, f faces) {
	const (
		padX, padTop, padBottom = 96.0, 80.0, 76.0
		size                    = 56.0 // logo
	)
	pal := p.pal
	draw.Draw(c.img, c.img.Bounds(), image.NewUniform(pal.bg), image.Point{}, draw.Src)
	fg := legible(pal.fg, 4.5, pal.bg)
	muted := legible(pal.muted, 3, pal.bg)
	contentW := float64(Width) - 2*padX

	lab := f.medium.at(18, capsTrack+0.03)
	eyebrow := strings.ToUpper(p.eyebrow)
	top := padTop
	header := p.logo != nil || p.site != ""
	if header {
		x := padX
		if c.avatar(p, f, x, top, size, pal.accent, onColor(pal.accent, pal.bg, pal.fg)) {
			x += size + 18
		}
		siteW := contentW - (x - padX)
		if eyebrow != "" {
			eyebrow = lab.fit(eyebrow, contentW*0.4)
			ew := lab.width(eyebrow)
			lab.draw(c.img, eyebrow, float64(Width)-padX-ew, top+size/2+lab.capHeight()/2, muted)
			siteW -= ew + 40
		}
		c.stack(x, top+size/2, siteW, 0, line{f.medium.at(24, -0.005), p.site, fg})
		top += size
	}

	bottom := float64(Height) - padBottom
	ruleY := bottom
	if p.footer != "" {
		ft := f.medium.at(22, 0)
		ft.draw(c.img, ft.fit(p.footer, contentW), padX, bottom, muted)
		ruleY = bottom - ft.capHeight() - 30
	}
	// The thin rule: a hairline with a short accent segment.
	c.roundRect(padX, ruleY-1, float64(Width)-padX, ruleY, 0, alpha(fg, 0.12))
	c.roundRect(padX, ruleY-1.5, padX+56, ruleY+0.5, 1, alpha(legible(pal.accent, 3, pal.bg), 1))

	space := ruleY - 56 - (top + 56)
	labH := 0.0
	if !header && eyebrow != "" {
		labH = lab.capHeight() + 28
	}
	title := fitTitle(f.bold, p.title, min(contentW, 920), space-labH, 66)
	y := top + 56 + max(0, (space-labH-title.height())*0.5) + labH
	if labH > 0 {
		lab.draw(c.img, lab.fit(eyebrow, contentW), padX, y-28, muted)
	}
	title.draw(c, padX, y, fg)
}
