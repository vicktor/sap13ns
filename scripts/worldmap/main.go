// Command worldmap writes the world map and the country names of the
// analytics page (internal/worldmap). It reads the Natural Earth 1:110m
// countries of the world-atlas package (npm ci installs it), projects them
// with the Equal Earth projection and writes SVG path data keyed by ISO
// 3166-1 alpha-2 code. The names come from CLDR (golang.org/x/text), in
// English and in every language of ui/i18n.
//
//	go run ./scripts/worldmap
//
// Its outputs are committed, like the other generated assets: the binary
// embeds them and needs neither Node nor the CLDR display tables.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// width is the map's width in SVG units; the height follows from the data.
const width = 1000

// byName gives a code to the shapes world-atlas leaves without one: the IP
// databases place their addresses in these countries.
var byName = map[string]string{"Kosovo": "XK", "Somaliland": "SO", "N. Cyprus": "CY"}

type topology struct {
	Transform struct {
		Scale     [2]float64 `json:"scale"`
		Translate [2]float64 `json:"translate"`
	} `json:"transform"`
	Arcs    [][][2]float64 `json:"arcs"`
	Objects struct {
		Countries struct {
			Geometries []struct {
				Type       string          `json:"type"`
				Arcs       json.RawMessage `json:"arcs"`
				ID         string          `json:"id"`
				Properties struct {
					Name string `json:"name"`
				} `json:"properties"`
			} `json:"geometries"`
		} `json:"countries"`
	} `json:"objects"`
}

type shape struct {
	ID string `json:"id"`
	D  string `json:"d"`
}

type world struct {
	W      int     `json:"w"`
	H      int     `json:"h"`
	Shapes []shape `json:"shapes"`
}

func main() {
	src := flag.String("src", "node_modules/world-atlas/countries-110m.json", "world-atlas TopoJSON")
	out := flag.String("out", "internal/worldmap", "output directory")
	i18n := flag.String("i18n", "ui/i18n", "translations, for the list of languages")
	flag.Parse()
	if err := run(*src, *out, *i18n); err != nil {
		log.Fatal(err)
	}
}

func run(src, out, i18nDir string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("%w (run npm ci first)", err)
	}
	var topo topology
	if err := json.Unmarshal(raw, &topo); err != nil {
		return err
	}
	arcs := decodeArcs(&topo)

	type ring = [][2]float64
	var polys []struct {
		id    string
		rings []ring
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, g := range topo.Objects.Countries.Geometries {
		id := g.ID
		if id == "010" { // Antarctica: no visitors, and most of the map's height
			continue
		}
		code := byName[g.Properties.Name]
		if code == "" {
			r, err := language.ParseRegion(id)
			if err != nil {
				return fmt.Errorf("%s (%q): %w", g.Properties.Name, id, err)
			}
			code = r.String()
		}
		var rings [][]int
		switch g.Type {
		case "Polygon":
			if err := json.Unmarshal(g.Arcs, &rings); err != nil {
				return err
			}
		case "MultiPolygon":
			var mp [][][]int
			if err := json.Unmarshal(g.Arcs, &mp); err != nil {
				return err
			}
			for _, p := range mp {
				rings = append(rings, p...)
			}
		default:
			return fmt.Errorf("%s: geometry %s", g.Properties.Name, g.Type)
		}
		p := struct {
			id    string
			rings []ring
		}{id: code}
		for _, idx := range rings {
			for _, r := range splitAntimeridian(stitch(arcs, idx)) {
				for i, pt := range r {
					x, y := equalEarth(pt[0], pt[1])
					r[i] = [2]float64{x, -y}
					minX, maxX = min(minX, x), max(maxX, x)
					minY, maxY = min(minY, -y), max(maxY, -y)
				}
				p.rings = append(p.rings, r)
			}
		}
		polys = append(polys, p)
	}

	k := width / (maxX - minX)
	w := world{W: width, H: int(math.Ceil((maxY - minY) * k))}
	paths := map[string]*strings.Builder{}
	for _, p := range polys {
		b := paths[p.id]
		if b == nil {
			b = &strings.Builder{}
			paths[p.id] = b
		}
		for _, r := range p.rings {
			writeRing(b, r, func(pt [2]float64) (float64, float64) { return (pt[0] - minX) * k, (pt[1] - minY) * k })
		}
	}
	for _, id := range slices.Sorted(func(yield func(string) bool) {
		for id := range paths {
			if !yield(id) {
				return
			}
		}
	}) {
		w.Shapes = append(w.Shapes, shape{ID: id, D: paths[id].String()})
	}
	if err := writeJSON(filepath.Join(out, "world.json"), w); err != nil {
		return err
	}

	langs, err := languages(i18nDir)
	if err != nil {
		return err
	}
	names := map[string]map[string]string{}
	for _, l := range langs {
		namer := display.Regions(language.Make(l))
		if namer == nil {
			return fmt.Errorf("no CLDR region names for %q", l)
		}
		m := map[string]string{}
		for a := 'A'; a <= 'Z'; a++ {
			for b := 'A'; b <= 'Z'; b++ {
				code := string([]rune{a, b})
				r, err := language.ParseRegion(code)
				if err != nil || r.String() != code || !r.IsCountry() {
					continue
				}
				if n := namer.Name(r); n != "" {
					m[code] = n
				}
			}
		}
		names[l] = m
	}
	return writeJSON(filepath.Join(out, "names.json"), names)
}

// decodeArcs undoes the quantisation and delta encoding of the arcs.
func decodeArcs(t *topology) [][][2]float64 {
	s, tr := t.Transform.Scale, t.Transform.Translate
	out := make([][][2]float64, len(t.Arcs))
	for i, arc := range t.Arcs {
		var x, y float64
		for _, d := range arc {
			x, y = x+d[0], y+d[1]
			out[i] = append(out[i], [2]float64{x*s[0] + tr[0], y*s[1] + tr[1]})
		}
	}
	return out
}

// stitch joins the arcs of a ring; ^i is arc i reversed.
func stitch(arcs [][][2]float64, idx []int) [][2]float64 {
	var r [][2]float64
	for _, i := range idx {
		a := slices.Clone(arcs[max(i, ^i)])
		if i < 0 {
			slices.Reverse(a)
		}
		if len(r) > 0 {
			a = a[1:] // the first point repeats the last one
		}
		r = append(r, a...)
	}
	return r
}

// splitAntimeridian cuts a ring that crosses the 180th meridian (Russia,
// Fiji) into the pieces on either side, which would otherwise be joined by
// a band across the whole map. It makes the longitudes continuous, then
// clips the ring, and its copies a turn east and west, to [-180, 180].
func splitAntimeridian(r [][2]float64) [][][2]float64 {
	u := make([][2]float64, len(r))
	for i, p := range r {
		if i > 0 {
			for d := p[0] - u[i-1][0]; d > 180; d -= 360 {
				p[0] -= 360
			}
			for d := p[0] - u[i-1][0]; d < -180; d += 360 {
				p[0] += 360
			}
		}
		u[i] = p
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range u {
		lo, hi = min(lo, p[0]), max(hi, p[0])
	}
	if lo >= -180 && hi <= 180 {
		return [][][2]float64{r}
	}
	var out [][][2]float64
	for _, shift := range []float64{0, 360, -360} {
		if lo+shift >= 180 || hi+shift <= -180 {
			continue
		}
		piece := make([][2]float64, len(u))
		for i, p := range u {
			piece[i] = [2]float64{p[0] + shift, p[1]}
		}
		piece = clip(piece, -180, false)
		piece = clip(piece, 180, true)
		if len(piece) >= 3 {
			out = append(out, piece)
		}
	}
	return out
}

// clip keeps the part of a ring west (west true) or east of longitude x
// (Sutherland-Hodgman, one edge).
func clip(r [][2]float64, x float64, west bool) [][2]float64 {
	in := func(p [2]float64) bool {
		if west {
			return p[0] <= x
		}
		return p[0] >= x
	}
	var out [][2]float64
	for i, cur := range r {
		prev := r[(i+len(r)-1)%len(r)]
		if in(cur) != in(prev) {
			t := (x - prev[0]) / (cur[0] - prev[0])
			out = append(out, [2]float64{x, prev[1] + t*(cur[1]-prev[1])})
		}
		if in(cur) {
			out = append(out, cur)
		}
	}
	return out
}

// equalEarth projects longitude and latitude (degrees) with the Equal Earth
// projection (Šavrič, Patterson and Jenny, 2018).
func equalEarth(lon, lat float64) (float64, float64) {
	const a1, a2, a3, a4 = 1.340264, -0.081106, 0.000893, 0.003796
	m := math.Sqrt(3) / 2
	l, p := lon*math.Pi/180, lat*math.Pi/180
	t := math.Asin(m * math.Sin(p))
	t2 := t * t
	t6 := t2 * t2 * t2
	x := l * math.Cos(t) / (m * (a1 + 3*a2*t2 + t6*(7*a3+9*a4*t2)))
	y := t * (a1 + a2*t2 + t6*(a3+a4*t2))
	return x, y
}

// writeRing appends the ring as a closed subpath, in whole units relative
// to the previous point, dropping points that would not move it.
func writeRing(b *strings.Builder, r [][2]float64, at func([2]float64) (float64, float64)) {
	var px, py int
	n := 0
	start := b.Len()
	for i, pt := range r {
		fx, fy := at(pt)
		x, y := int(math.Round(fx*2)), int(math.Round(fy*2)) // half units
		switch {
		case i == 0:
			fmt.Fprintf(b, "M%s %s", half(x), half(y))
		case x == px && y == py:
			continue
		default:
			fmt.Fprintf(b, "l%s %s", half(x-px), half(y-py))
		}
		px, py = x, y
		n++
	}
	if n < 3 { // too small to see at this size
		s := b.String()[:start]
		b.Reset()
		b.WriteString(s)
		return
	}
	b.WriteString("z")
}

// half formats n/2 with at most one decimal.
func half(n int) string {
	if n%2 == 0 {
		return strconv.Itoa(n / 2)
	}
	s := strconv.FormatFloat(float64(n)/2, 'f', 1, 64)
	if strings.HasPrefix(s, "0.") {
		return s[1:]
	}
	if strings.HasPrefix(s, "-0.") {
		return "-" + s[2:]
	}
	return s
}

// languages lists "en" and every ui/i18n/<lang>.json.
func languages(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("no translations in " + dir)
	}
	langs := []string{"en"}
	for _, f := range files {
		langs = append(langs, strings.TrimSuffix(filepath.Base(f), ".json"))
	}
	return langs, nil
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
