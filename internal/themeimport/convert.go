// Package themeimport converts a WordPress block theme into a sap13ns
// theme (docs/THEME-IMPORT.md). Nothing of the source theme runs: its
// templates, parts and patterns are read as block markup, its PHP is
// read by a small evaluator that only prints literal text (php.go), its
// theme.json becomes CSS, and its fonts and images are copied only after
// their bytes are sniffed. The result is an ordinary uploaded theme,
// which internal/theme checks like any other before it is installed.
package themeimport

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"sap13ns/internal/archive"
	"sap13ns/internal/customize"
	"sap13ns/internal/media"
	"sap13ns/internal/theme"
)

// baseCSS is the shared part of every converted theme's stylesheet:
// Basecoat's tokens and the components plugins use, and the styles of
// the shared templates (comments, search form, admin bar…), built from
// src/base.css by npm run build. blocksCSS is the block library: the
// layout and block styles WordPress ships, rewritten for sap13ns.
var (
	//go:embed base.css
	baseCSS string
	//go:embed blocks.css
	blocksCSS string
	//go:embed licenses/gpl-2.0.txt
	gpl2 string
	//go:embed licenses/gpl-3.0.txt
	gpl3 string
)

// Bounds of a conversion. The archive may be larger than an uploaded
// sap13ns theme (a WordPress theme carries PHP, screenshots and source
// files the conversion leaves behind); what it writes stays within the
// limits of uploaded themes.
const (
	MaxArchive  = 32 << 20
	maxEntries  = 3000
	maxUnpacked = 128 << 20
	maxText     = 512 << 10 // a template, part or pattern
	maxJSON     = 1 << 20
	maxStyle    = 1 << 20
	maxFile     = 4 << 20 // an image or font
	maxSVG      = 48 << 10
	maxImages   = 160
	maxBytesOut = 18 << 20
	maxFonts    = 48
	maxCSSOut   = 1 << 20
	maxTmplOut  = 250 << 10
)

// ErrNotBlockTheme: the archive is not a WordPress block theme.
var ErrNotBlockTheme = errors.New("not a WordPress block theme: it needs style.css and block templates (templates/index.html)")

// Options of a conversion.
type Options struct {
	// Slug names the sap13ns theme; "" makes one from the theme's
	// folder ("wp-twentytwentyfive").
	Slug string
}

// Result is a converted theme: its files, by path.
type Result struct {
	Slug   string
	Files  map[string][]byte
	Report *Report
}

// Zip writes the theme as an archive, its files at the root.
func (r *Result) Zip() ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	names := make([]string, 0, len(r.Files))
	for n := range r.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		method := zip.Deflate
		if strings.HasSuffix(n, ".woff2") || strings.HasSuffix(n, ".woff") || strings.HasSuffix(n, ".webp") ||
			strings.HasSuffix(n, ".jpg") || strings.HasSuffix(n, ".png") || strings.HasSuffix(n, ".avif") {
			method = zip.Store
		}
		f, err := w.CreateHeader(&zip.FileHeader{Name: n, Method: method, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(r.Files[n]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// converter holds one conversion.
type converter struct {
	z       *archive.Zip
	files   map[string]bool
	folder  string // the theme's folder, as in /wp-content/themes/<folder>/
	slug    string // the sap13ns slug
	header  map[string]string
	theme   *wpTheme
	res     *resolver
	marks   marks
	markRe  *regexp.Regexp
	report  *Report
	woo     bool
	tmplDir string
	partDir string

	patterns  map[string]*pattern
	parts     map[string]string
	partAreas map[string]string
	cache     map[string][]*Block

	gen     *cssWriter
	classes map[string]bool

	out      map[string][]byte
	assets   map[string]string // source file -> bundled path, or a data: URI; "" refused
	names    map[string]bool   // bundled names taken
	outBytes int64
	images   int
	fonts    int
	menus    []theme.MenuLocation

	lightTokens, darkTokens map[string]string
	lightPalette            palette
	variationTokens         map[string][2]map[string]string // slug -> light, dark
}

// Convert converts a WordPress block theme archive.
func Convert(r io.ReaderAt, size int64, opt Options) (*Result, error) {
	z, err := archive.Open(r, size, "style.css", archive.Limits{MaxBytes: MaxArchive, MaxEntries: maxEntries,
		MaxUnpacked: maxUnpacked, MaxRatio: 100, SkipHidden: true})
	if err != nil {
		var ae *archive.Error
		if errors.As(err, &ae) && ae.Rule == "package.no-manifest" {
			return nil, ErrNotBlockTheme
		}
		return nil, err
	}
	// Sizes, ratios and duplicate names, before anything is read.
	if err := z.Check(func(string) (int64, error) { return 16 << 20, nil }); err != nil {
		return nil, err
	}
	c := &converter{z: z, files: map[string]bool{}, report: &Report{Source: "wordpress-block", Blocks: map[string]int{}},
		marks: newMarks(), patterns: map[string]*pattern{}, parts: map[string]string{}, partAreas: map[string]string{},
		cache: map[string][]*Block{}, gen: &cssWriter{}, classes: map[string]bool{}, out: map[string][]byte{},
		assets: map[string]string{}, names: map[string]bool{}}
	c.markRe = c.marks.re()
	for _, f := range z.Files() {
		c.files[f] = true
	}
	c.folder = strings.TrimSuffix(z.Prefix, "/")
	for _, d := range []string{"templates", "block-templates"} {
		if c.files[d+"/index.html"] {
			c.tmplDir = d
		}
	}
	if c.tmplDir == "" {
		return nil, ErrNotBlockTheme
	}
	c.partDir = "parts"
	if !c.hasDir("parts") && c.hasDir("block-template-parts") {
		c.partDir = "block-template-parts"
	}
	if err := c.readHeader(); err != nil {
		return nil, err
	}
	c.slug = opt.Slug
	if c.slug == "" {
		c.slug = slugFor(cmpOr(c.folder, c.header["Text Domain"]), c.header["Theme Name"])
	}
	if !theme.ValidSlug(c.slug) {
		return nil, fmt.Errorf("%q cannot name a theme (lowercase letters, digits and dashes, at most 40)", c.slug)
	}
	c.report.Slug = c.slug
	c.readThemeJSON()
	c.res = &resolver{root: c.theme.json, fluid: c.theme.fluid}
	c.readParts()
	c.readPatterns()
	php := 0
	for f := range c.files {
		if strings.HasSuffix(strings.ToLower(f), ".php") && !strings.HasPrefix(f, "patterns/") {
			php++
		}
	}
	if php > 0 {
		c.report.add(Note{Code: "theme.php", Args: []any{php}})
	}
	if p := c.header["Template"]; p != "" {
		c.report.add(Note{Code: "theme.parent", Args: []any{p}})
	}

	if err := c.writeTemplates(); err != nil {
		return nil, err
	}
	if err := c.writeCSS(); err != nil {
		return nil, err
	}
	if err := c.writeManifest(); err != nil {
		return nil, err
	}
	c.writeDocs()
	if c.woo {
		c.report.add(Note{Code: "theme.woocommerce"})
	}
	var total int64
	for _, b := range c.out {
		total += int64(len(b))
	}
	if total > maxBytesOut || len(c.out) > 280 {
		return nil, fmt.Errorf("the converted theme is too large (%d files, %d MiB)", len(c.out), total>>20)
	}
	pages := []string{}
	for _, p := range []string{"front", "home", "single", "page", "archive", "search", "404"} {
		if _, ok := c.out["templates/"+p+".html"]; ok {
			pages = append(pages, p)
		}
	}
	c.report.Pages = pages
	c.report.Fonts = c.fonts
	return &Result{Slug: c.slug, Files: c.out, Report: c.report}, nil
}

func (c *converter) hasDir(d string) bool {
	for f := range c.files {
		if strings.HasPrefix(f, d+"/") {
			return true
		}
	}
	return false
}

// read reads a file of the theme, at most limit bytes.
func (c *converter) read(name string, limit int64) ([]byte, bool) {
	if !c.files[name] {
		return nil, false
	}
	b, err := c.z.ReadFile(name, limit)
	if err != nil {
		c.report.add(Note{Code: "file.unreadable", File: name})
		return nil, false
	}
	return b, true
}

// text reads a text file of the theme: UTF-8, without the importer's
// mark runes.
func (c *converter) text(name string, limit int64) (string, bool) {
	b, ok := c.read(name, limit)
	if !ok {
		return "", false
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	s := string(b)
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return stripMarks(s), true
}

var headerRe = regexp.MustCompile(`(?m)^[ \t*#@]*([A-Za-z][A-Za-z ]{1,30}):[ \t]*(.*?)\s*$`)

// readHeader reads the header of style.css.
func (c *converter) readHeader() error {
	src, ok := c.text("style.css", maxStyle)
	if !ok {
		return ErrNotBlockTheme
	}
	c.header = map[string]string{}
	head := src
	if i := strings.Index(src, "*/"); i >= 0 {
		head = src[:i]
	}
	if len(head) > 16<<10 {
		head = head[:16<<10]
	}
	for _, m := range headerRe.FindAllStringSubmatch(head, -1) {
		k := strings.TrimSpace(m[1])
		if _, seen := c.header[k]; !seen {
			c.header[k] = cleanLine(m[2])
		}
	}
	if c.header["Theme Name"] == "" {
		return ErrNotBlockTheme
	}
	c.report.Name = truncateText(c.header["Theme Name"], 80)
	c.report.Version = truncateText(c.header["Version"], 40)
	c.report.Author = truncateText(c.header["Author"], 100)
	c.report.License = truncateText(c.header["License"], 100)
	return nil
}

// cleanLine keeps a header value as plain text on one line.
func cleanLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, stripMarks(s))
	return strings.TrimSpace(s)
}

var slugCleanRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugFor(folder, name string) string {
	s := strings.Trim(slugCleanRe.ReplaceAllString(strings.ToLower(cmpOr(folder, name)), "-"), "-")
	if s == "" {
		s = "theme"
	}
	s = "wp-" + s
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// readThemeJSON reads theme.json and the style variations.
func (c *converter) readThemeJSON() {
	var m map[string]any
	if b, ok := c.read("theme.json", maxJSON); ok {
		var err error
		if m, err = decodeJSON(b); err != nil {
			m = nil
		}
	}
	c.theme = loadThemeJSON(m)
	for _, tp := range listOf(m["templateParts"]) {
		if n := cssIdent(attrString(tp, "name")); n != "" {
			c.partAreas[n] = attrString(tp, "area")
		}
	}
	// Variations: styles/*.json and styles/**/*.json. A file with
	// blockTypes styles blocks (is-style-<slug>); one with a palette is a
	// colour variation.
	var files []string
	for f := range c.files {
		if strings.HasPrefix(f, "styles/") && strings.HasSuffix(f, ".json") {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		b, ok := c.read(f, 256<<10)
		if !ok {
			continue
		}
		v, err := decodeJSON(b)
		if err != nil {
			continue
		}
		if bt, ok := v["blockTypes"].([]any); ok {
			s := sectionStyle{slug: cssIdent(cmpOr(attrString(v, "slug"), strings.TrimSuffix(path.Base(f), ".json"))), styles: attrMap(v, "styles")}
			for _, t := range bt {
				if t, ok := t.(string); ok && len(s.blockTypes) < 20 {
					s.blockTypes = append(s.blockTypes, t)
				}
			}
			if s.slug != "" && s.styles != nil && len(c.theme.sections) < 60 {
				c.theme.sections = append(c.theme.sections, s)
			}
			continue
		}
		pal := presets(dig(v, "settings", "color", "palette"), "color")
		if len(pal) == 0 {
			continue
		}
		title := cleanLine(attrString(v, "title"))
		slug := cssIdent(cmpOr(title, strings.TrimSuffix(path.Base(f), ".json")))
		if len(slug) > 36 {
			slug = slug[:36]
		}
		if slug == "" || slug == "default" || seen[slug] || len(c.theme.variations) >= 24 {
			continue
		}
		seen[slug] = true
		sv := styleVariation{slug: slug, title: truncateText(cmpOr(title, slug), 60), palette: pal}
		sv.bg, _ = dig(v, "styles", "color", "background").(string)
		sv.text, _ = dig(v, "styles", "color", "text").(string)
		c.theme.variations = append(c.theme.variations, sv)
	}
}

// readParts reads the template parts.
func (c *converter) readParts() {
	for f := range c.files {
		dir, name := path.Split(f)
		if dir != c.partDir+"/" || !strings.HasSuffix(name, ".html") {
			continue
		}
		if src, ok := c.text(f, maxText); ok {
			c.parts[cssIdent(strings.TrimSuffix(name, ".html"))] = src
		}
	}
}

// readPatterns reads the patterns and evaluates their PHP.
func (c *converter) readPatterns() {
	var files []string
	for f := range c.files {
		if strings.HasPrefix(f, "patterns/") && (strings.HasSuffix(f, ".php") || strings.HasSuffix(f, ".html")) && strings.Count(f, "/") == 1 {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, ok := c.read(f, maxText)
		if !ok {
			continue
		}
		b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
		p := parsePattern(f, string(b))
		if p.Invalid {
			c.report.add(Note{Code: "pattern.invalid", File: f})
			continue
		}
		if len(p.Skipped) > 0 {
			c.report.add(Note{Code: "pattern.php", File: f, Args: []any{len(p.Skipped)}})
		}
		p.Markup = c.marks.bind(p.Markup)
		if p.Slug == "" {
			p.Slug = c.folder + "/" + strings.TrimSuffix(path.Base(f), path.Ext(f))
		}
		if _, dup := c.patterns[p.Slug]; !dup {
			c.patterns[p.Slug] = p
		}
	}
}

// parsed parses markup once.
func (c *converter) parsed(key, src string) ([]*Block, error) {
	if bs, ok := c.cache[key]; ok {
		return bs, nil
	}
	bs, err := ParseBlocks(src)
	if err != nil {
		return nil, err
	}
	c.cache[key] = bs
	return bs, nil
}

// partArea is "header", "footer" or "": theme.json's templateParts, the
// block's tag, or the part's name.
func (c *converter) partArea(slug, tag string) string {
	if a := c.partAreas[slug]; a == "header" || a == "footer" {
		return a
	}
	switch {
	case tag == "header" || tag == "footer":
		return tag
	case strings.Contains(slug, "header"):
		return "header"
	case strings.Contains(slug, "footer"):
		return "footer"
	}
	return ""
}

// menuLocation names the menu location of a navigation block: the first
// of the header is the main menu, the footer's are footer menus.
func (c *converter) menuLocation(region string, n int) (id, label string) {
	switch {
	case region == "header" && n <= 1:
		id, label = "primary", "Main menu"
	case region == "header" && n == 2:
		id, label = "secondary", "Secondary menu"
	case region == "footer" && n <= 1:
		id, label = "footer", "Footer menu"
	case region == "footer":
		id, label = fmt.Sprintf("footer-%d", n), "Footer menu"
	default:
		id, label = "content", "Menu"
		if n > 1 {
			id = fmt.Sprintf("content-%d", n)
		}
		if region == "header" {
			id = fmt.Sprintf("header-%d", n)
		}
	}
	if !slices.ContainsFunc(c.menus, func(m theme.MenuLocation) bool { return m.ID == id }) && len(c.menus) < 10 {
		ml := theme.MenuLocation{ID: id, Label: label}
		if n > 1 {
			ml.Label = fmt.Sprintf("%s %d", label, n)
		}
		c.menus = append(c.menus, ml)
		c.report.add(Note{Code: "menu.location", Args: []any{id}})
	}
	return id, label
}

// Assets.

var nameCleanRe = regexp.MustCompile(`[^a-z0-9._-]+`)

// bundleName gives a file of the theme its name under static/: a safe
// name, unique.
func (c *converter) bundleName(dir, file string) string {
	base := strings.ToLower(path.Base(file))
	ext := path.Ext(base)
	stem := strings.Trim(nameCleanRe.ReplaceAllString(strings.TrimSuffix(base, ext), "-"), "-._")
	if stem == "" {
		stem = "file"
	}
	if len(stem) > 60 {
		stem = stem[:60]
	}
	name := dir + "/" + stem + ext
	for i := 2; c.names[name]; i++ {
		name = fmt.Sprintf("%s/%s-%d%s", dir, stem, i, ext)
	}
	c.names[name] = true
	return name
}

var imageTypes = map[string][]string{
	".png": {"image/png"}, ".jpg": {"image/jpeg"}, ".jpeg": {"image/jpeg"}, ".webp": {"image/webp"},
	".avif": {"image/avif"}, ".gif": {"image/gif"},
}

// cleanPath resolves a path into the theme; "" when it leaves it.
func cleanPath(p string) string {
	p, _, _ = strings.Cut(p, "?")
	p, _, _ = strings.Cut(p, "#")
	p = path.Clean("/" + strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." || strings.HasPrefix(p, "..") {
		return ""
	}
	return p
}

// asset bundles an image of the theme: its path under static/, or a
// data: URI for a small SVG. ok is false when it is not the theme's or
// not an allowed picture.
func (c *converter) asset(file string) (string, bool) {
	file = cleanPath(file)
	if file == "" {
		return "", false
	}
	if v, done := c.assets[file]; done {
		return v, v != ""
	}
	c.assets[file] = ""
	ext := strings.ToLower(path.Ext(file))
	if ext == ".svg" {
		b, ok := c.read(file, maxSVG)
		if !ok || len(b) > maxSVG || !safeSVG(b) {
			c.report.add(Note{Code: "image.unsupported", Args: []any{file}})
			return "", false
		}
		v := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(b)
		c.assets[file] = v
		return v, true
	}
	want, ok := imageTypes[ext]
	if !ok {
		return "", false
	}
	if c.images >= maxImages {
		c.report.add(Note{Code: "image.unsupported", Args: []any{file}})
		return "", false
	}
	b, ok := c.read(file, maxFile)
	if !ok || len(b) > maxFile || !slices.Contains(want, media.Sniff(b)) || c.outBytes+int64(len(b)) > maxBytesOut-(2<<20) {
		if ok {
			c.report.add(Note{Code: "image.unsupported", Args: []any{file}})
		}
		return "", false
	}
	out := c.bundleName("images", file)
	c.out["static/"+out] = b
	c.outBytes += int64(len(b))
	c.images++
	c.assets[file] = out
	return out, true
}

// font bundles a font file: .woff2 or .woff, sniffed.
func (c *converter) font(file string) (string, bool) {
	file = cleanPath(file)
	if file == "" {
		return "", false
	}
	if v, done := c.assets[file]; done {
		return v, v != ""
	}
	c.assets[file] = ""
	ext := strings.ToLower(path.Ext(file))
	want := map[string]string{".woff2": "font/woff2", ".woff": "font/woff"}[ext]
	if want == "" || c.fonts >= maxFonts {
		c.report.add(Note{Code: "font.dropped", Args: []any{file}})
		return "", false
	}
	b, ok := c.read(file, maxFile)
	if !ok || len(b) > maxFile || media.Sniff(b) != want || !fontHeader(b) || c.outBytes+int64(len(b)) > maxBytesOut-(2<<20) {
		c.report.add(Note{Code: "font.dropped", Args: []any{file}})
		return "", false
	}
	out := c.bundleName("fonts", file)
	c.out["static/"+out] = b
	c.outBytes += int64(len(b))
	c.fonts++
	c.assets[file] = out
	return out, true
}

// fontHeader: the header of a WOFF or WOFF2 file says how long the file
// is (https://www.w3.org/TR/WOFF2/#woff20Header); the signature alone,
// which sniffing reads, is four bytes anyone can write.
func fontHeader(b []byte) bool {
	if len(b) < 48 {
		return false
	}
	n := int(b[8])<<24 | int(b[9])<<16 | int(b[10])<<8 | int(b[11])
	return n == len(b)
}

// safeSVG: an SVG picture without scripts, foreign content, event
// handlers, entities or links to other files. As an image it cannot run
// anything anyway; this keeps the data: URI plain.
func safeSVG(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	l := strings.ToLower(string(b))
	if !strings.Contains(l, "<svg") {
		return false
	}
	for _, bad := range []string{"<script", "foreignobject", "<!entity", "<!doctype", "javascript:", "<iframe", "<use", "href=\"http", "href='http", "@import", "url(http", "url(//"} {
		if strings.Contains(l, bad) {
			return false
		}
	}
	return !onAttrRe.MatchString(l)
}

var onAttrRe = regexp.MustCompile(`\son[a-z]+\s*=`)

// cssURL resolves the url()s of a stylesheet of the theme in folder
// base: a file of the theme becomes its bundled path (relative to
// static/theme.css), a small data: image stays, anything else is refused.
func (c *converter) cssURL(base string) func(string) (string, bool) {
	return func(u string) (string, bool) {
		u = strings.TrimSpace(stripMarks(u))
		l := strings.ToLower(u)
		switch {
		case u == "":
			return "", false
		case strings.HasPrefix(l, "data:image/"):
			if len(u) <= 64<<10 && !strings.ContainsAny(u, `"'()\`+"\n ") {
				return u, true
			}
			return "", false
		case strings.HasPrefix(u, "#"):
			return u, true
		}
		file := ""
		if i := strings.Index(u, "/wp-content/themes/"); i >= 0 {
			file = themeFileOf(u[i:], c.folder)
		} else if !strings.Contains(u, ":") && !strings.HasPrefix(u, "/") {
			file = cleanPath(path.Join(base, u))
		}
		if file == "" {
			c.report.add(Note{Code: "css.url-dropped", Args: []any{truncateText(u, 80)}})
			return "", false
		}
		switch strings.ToLower(path.Ext(cleanPath(file))) {
		case ".woff2", ".woff":
			return c.font(file)
		case ".ttf", ".otf", ".eot":
			c.report.add(Note{Code: "font.dropped", Args: []any{file}})
			return "", false
		}
		return c.asset(file)
	}
}

// bundledURL accepts only what the conversion bundled: the last check of
// the generated CSS.
func (c *converter) bundledURL(u string) (string, bool) {
	if strings.HasPrefix(u, "data:image/") || strings.HasPrefix(u, "#") {
		return u, !strings.ContainsAny(u, `"'()\`+"\n ")
	}
	if _, ok := c.out["static/"+u]; ok {
		return u, true
	}
	return "", false
}

// cleanStatement returns a CSS statement when it passes the checks of
// the theme's stylesheets, else "".
func cleanStatement(st string) string {
	if customize.CheckCSS(st, foreignCSS) != nil {
		return ""
	}
	return st
}

// Templates.

// pageSources are the WordPress templates each sap13ns page is made
// from, in order of preference (the template hierarchy, reduced).
var pageSources = []struct {
	page  string
	files []string
}{
	{"front", []string{"front-page"}},
	{"home", []string{"home", "index"}},
	{"single", []string{"single", "single-post", "singular", "index"}},
	{"page", []string{"page", "singular"}},
	{"archive", []string{"archive", "category", "tag", "index"}},
	{"search", []string{"search", "index"}},
	{"404", []string{"404"}},
}

type pageTemplate struct {
	page, file           string
	header, body, footer []*Block
	headerKey, footerKey string
}

func (c *converter) writeTemplates() error {
	used := map[string]bool{}
	var pages []*pageTemplate
	for _, ps := range pageSources {
		for _, f := range ps.files {
			file := c.tmplDir + "/" + f + ".html"
			src, ok := c.text(file, maxText)
			if !ok {
				continue
			}
			bs, err := c.parsed("template:"+f, src)
			if err != nil {
				c.report.add(Note{Code: "block.too-many", File: file})
				break
			}
			used[file] = true
			bs = c.flatten(bs, 0, nil)
			pt := &pageTemplate{page: ps.page, file: file}
			pt.header, pt.body, pt.footer = c.split(bs)
			pt.headerKey, pt.footerKey = partsKey(pt.header), partsKey(pt.footer)
			pages = append(pages, pt)
			break
		}
	}
	for f := range c.files {
		if strings.HasPrefix(f, c.tmplDir+"/") && strings.HasSuffix(f, ".html") && !used[f] && strings.Count(f, "/") == 1 {
			name := strings.TrimSuffix(path.Base(f), ".html")
			if hierarchy[name] {
				continue // shown by the page it falls back to
			}
			if strings.Contains(name, "product") || strings.Contains(name, "shop") || strings.HasPrefix(name, "page-cart") || strings.HasPrefix(name, "page-checkout") {
				c.woo = true
				continue
			}
			c.report.add(Note{Code: "template.custom", File: f, Args: []any{name}})
		}
	}

	// The layout: the header and footer most pages use, and the others
	// for the pages that differ.
	headers, footers := map[string]*pageTemplate{}, map[string]*pageTemplate{}
	count := map[string]int{}
	for _, p := range pages {
		if _, ok := headers[p.headerKey]; !ok {
			headers[p.headerKey] = p
		}
		if _, ok := footers[p.footerKey]; !ok {
			footers[p.footerKey] = p
		}
		count["h"+p.headerKey]++
		count["f"+p.footerKey]++
	}
	defH, defF := mostUsed(pages, count, "h"), mostUsed(pages, count, "f")
	if len(headers) > 1 || len(footers) > 1 {
		c.report.add(Note{Code: "layout.parts"})
	}
	layout := &emitter{c: c, file: "layout", kind: "layout"}
	chain := func(key func(*pageTemplate) string, def string, parts func(*pageTemplate) []*Block, region string) string {
		var b strings.Builder
		var defBlocks []*Block
		first := true
		done := map[string]bool{}
		for _, p := range pages {
			k := key(p)
			if k == def {
				defBlocks = parts(p)
				continue
			}
			if done[p.page] {
				continue
			}
			var kinds []string
			for _, q := range pages {
				if key(q) == k {
					kinds = append(kinds, `"`+q.page+`"`)
					done[q.page] = true
				}
			}
			if first {
				b.WriteString(`{{if eq .Kind ` + strings.Join(kinds, " ") + `}}`)
				first = false
			} else {
				b.WriteString(`{{else if eq .Kind ` + strings.Join(kinds, " ") + `}}`)
			}
			b.WriteString(layout.writeRegion(parts(p), region))
		}
		if !first {
			b.WriteString(`{{else}}`)
		}
		b.WriteString(layout.writeRegion(defBlocks, region))
		if !first {
			b.WriteString(`{{end}}`)
		}
		return b.String()
	}
	header := chain(func(p *pageTemplate) string { return p.headerKey }, defH, func(p *pageTemplate) []*Block { return p.header }, "header")
	footer := chain(func(p *pageTemplate) string { return p.footerKey }, defF, func(p *pageTemplate) []*Block { return p.footer }, "footer")
	styleClass := ""
	if len(c.theme.variations) > 0 {
		styleClass = ` wp-style-{{index .Site.Options "style"}}`
	}
	lay := `{{define "layout"}}<!doctype html>
<html lang="{{.Site.Lang}}" class="sap-wp` + styleClass + `{{with .Site.Custom.Class}} {{.}}{{end}}">
<head>{{template "shared/head" .}}</head>
<body class="wp-theme-` + c.slug + ` kind-{{.Kind}}{{if .IsFront}} home{{end}}{{if eq .Kind "single"}} single single-post{{else if eq .Kind "page"}} page{{else if eq .Kind "archive"}} archive{{else if eq .Kind "search"}} search{{else if eq .Kind "404"}} error404{{else}} blog{{end}}">
{{template "shared/skip-link"}}
{{template "shared/admin-bar" .}}
<div class="wp-site-blocks">
{{if .BlocksHeader}}{{template "shared/blocks-header" .}}{{else}}{{template "shared/widget-area" (dict "Page" . "Area" "top")}}
` + header + `
{{end}}
<main id="main" tabindex="-1" class="sap-wp-main">
{{template "content" .}}
</main>
{{if .BlocksFooter}}{{template "shared/blocks-footer" .}}{{else}}
` + footer + `
<div class="sap-wp-credit">{{template "shared/widget-area" (dict "Page" . "Area" "footer")}}{{template "shared/footer-meta" .}}</div>
{{end}}
</div>
{{template "shared/assistant" .}}
</body>
</html>{{end}}
`
	if len(lay) > maxTmplOut {
		return errors.New("the converted layout is too large")
	}
	c.out["templates/layout.html"] = []byte(lay)

	for _, p := range pages {
		e := &emitter{c: c, file: p.file, kind: p.page}
		if p.page == "single" || p.page == "page" {
			e.post = "page"
		}
		body := e.blocks(p.body)
		if p.page == "front" && blank(body) {
			// Its blocks were all left out (patterns registered from PHP):
			// the home page's template shows the front page instead.
			c.report.add(Note{Code: "template.empty", File: p.file})
			continue
		}
		if p.page == "404" && strings.TrimSpace(body) == "" {
			body = `{{template "shared/not-found" .}}`
		}
		out := `{{define "content"}}` + body + "{{end}}\n"
		if len(out) > maxTmplOut {
			c.report.add(Note{Code: "block.too-many", File: p.file})
			continue
		}
		c.out["templates/"+p.page+".html"] = []byte(out)
	}
	if _, ok := c.out["templates/404.html"]; !ok {
		c.report.add(Note{Code: "template.missing", Args: []any{"404"}})
		c.out["templates/404.html"] = []byte(`{{define "content"}}{{template "shared/not-found" .}}{{end}}` + "\n")
	}
	if _, ok := c.out["templates/home.html"]; !ok {
		return errors.New("the theme's index template could not be converted")
	}
	return nil
}

// blank: converted content that shows nothing, only empty elements.
func blank(body string) bool {
	return !strings.Contains(body, "{{") && !strings.Contains(body, "<img") &&
		strings.TrimSpace(tagRe.ReplaceAllString(body, "")) == ""
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

// hierarchy are the templates of WordPress's template hierarchy whose
// pages sap13ns shows with one of its own.
var hierarchy = map[string]bool{
	"index": true, "home": true, "front-page": true, "single": true, "single-post": true, "singular": true,
	"page": true, "archive": true, "category": true, "tag": true, "author": true, "date": true,
	"taxonomy": true, "attachment": true, "search": true, "404": true, "privacy-policy": true, "embed": true,
}

// flatten writes the top-level patterns of a template in place, so that a
// template made of one pattern (header, content and footer) splits too.
func (c *converter) flatten(bs []*Block, depth int, seen []string) []*Block {
	var out []*Block
	for _, b := range bs {
		if b.Name == "core/pattern" && depth < 4 {
			slug := attrString(b.Attrs, "slug")
			if p := c.patterns[slug]; p != nil && !slices.Contains(seen, slug) {
				if inner, err := c.parsed("pattern:"+slug, p.Markup); err == nil {
					out = append(out, c.flatten(inner, depth+1, append(slices.Clone(seen), slug))...)
					continue
				}
			}
		}
		out = append(out, b)
	}
	return out
}

func mostUsed(pages []*pageTemplate, count map[string]int, prefix string) string {
	best, bestN := "", -1
	for _, p := range pages {
		k := p.headerKey
		if prefix == "f" {
			k = p.footerKey
		}
		if n := count[prefix+k]; n > bestN {
			best, bestN = k, n
		}
	}
	return best
}

// region writes the header or footer parts of the layout.
func (e *emitter) writeRegion(bs []*Block, region string) string {
	var b strings.Builder
	n := 0
	placed := false
	s := e.sub()
	s.region, s.navs, s.tools = region, &n, &placed
	for _, x := range bs {
		e.c.report.Blocks[x.Name]++
		b.WriteString(s.part(x, region))
	}
	if region == "header" && !placed {
		b.WriteString(`<div class="sap-wp-tools sap-wp-tools-alone">{{template "shared/lang-switch" $}}{{template "shared/theme-toggle"}}</div>`)
	}
	return b.String()
}

// split takes a template's leading header parts and trailing footer
// parts off its content.
func (c *converter) split(bs []*Block) (header, body, footer []*Block) {
	isPart := func(b *Block, area string) bool {
		return b.Name == "core/template-part" && c.partArea(cssIdent(attrString(b.Attrs, "slug")), attrString(b.Attrs, "tagName")) == area
	}
	i := 0
	for i < len(bs) && isPart(bs[i], "header") {
		i++
	}
	j := len(bs)
	for j > i && isPart(bs[j-1], "footer") {
		j--
	}
	return bs[:i], bs[i:j], bs[j:]
}

func partsKey(bs []*Block) string {
	var ks []string
	for _, b := range bs {
		ks = append(ks, attrString(b.Attrs, "slug")+"|"+attrString(b.Attrs, "tagName")+"|"+attrString(b.Attrs, "className"))
	}
	return strings.Join(ks, ",")
}

// CSS.

func (c *converter) writeCSS() error {
	t := c.theme
	var css strings.Builder
	css.WriteString(baseCSS)
	css.WriteString("\n")
	css.WriteString(blocksCSS)
	css.WriteString("\n")

	// Colours: the light and dark schemes, then the variations.
	light, dark, ownDark := schemes(newPalette(t.palette), t.styles)
	c.report.OwnDark = ownDark
	if ownDark {
		c.report.add(Note{Code: "theme.dark-own"})
	} else {
		c.report.add(Note{Code: "theme.dark"})
	}
	scheme := &cssWriter{}
	scheme.rule(":root", schemeCSS(light, "light"))
	scheme.rule(":root.dark", schemeCSS(dark, "dark"))
	c.variationTokens = map[string][2]map[string]string{}
	for _, v := range t.variations {
		pal := newPalette(mergePresets(t.palette, v.palette))
		styles := t.styles
		if v.bg != "" || v.text != "" {
			styles = map[string]any{"color": map[string]any{"background": cmpOr(v.bg, scalar(dig(t.styles, "color", "background"))),
				"text": cmpOr(v.text, scalar(dig(t.styles, "color", "text")))}, "elements": t.styles["elements"]}
		}
		vl, vd, vOwnDark := schemes(pal, styles)
		if vOwnDark {
			// A dark variation is dark in both modes.
			vl = vd
		}
		scheme.rule(":root.wp-style-"+v.slug, schemeCSS(vl, "light"))
		scheme.rule(":root.dark.wp-style-"+v.slug, schemeCSS(vd, "dark"))
		c.variationTokens[v.slug] = [2]map[string]string{vl.tokens, vd.tokens}
	}
	if len(t.variations) > 0 {
		c.report.Variations = len(t.variations)
		c.report.add(Note{Code: "theme.variations", Args: []any{len(t.variations)}})
	}
	c.lightTokens, c.darkTokens, c.lightPalette = light.tokens, dark.tokens, light.pal
	css.WriteString(scheme.b.String())

	// The theme's fonts: only the families its styles and markup use.
	markup := c.allMarkup()
	used := t.usedFamilies(markup)
	unused := 0
	kept := t.families[:0:0]
	for _, f := range t.families {
		if used[f.slug] {
			kept = append(kept, f)
		} else if len(f.faces) > 0 {
			unused++
		}
	}
	if unused > 0 {
		c.report.add(Note{Code: "font.unused", Args: []any{unused}})
	}
	all := t.families
	t.families = kept
	fontURL := func(src string) (string, bool) {
		if strings.HasPrefix(src, "file:./") {
			return c.font(strings.TrimPrefix(src, "file:./"))
		}
		c.report.add(Note{Code: "font.remote", Args: []any{truncateText(src, 80)}})
		return "", false
	}
	global, faces, dropped := t.globalCSS(c.cssURL(""), fontURL)
	t.families = all
	// The generated CSS is checked as the theme's own: every statement
	// passes CheckCSS, every url() names a bundled file.
	faces, n1 := cleanCSS(faces, c.bundledURL)
	global, n2 := cleanCSS(global, c.bundledURL)
	css.WriteString(faces)
	css.WriteString(global)

	// The theme's style.css.
	if src, ok := c.text("style.css", maxStyle); ok {
		own, n := cleanCSS(src, c.cssURL(""))
		dropped += n
		css.WriteString(own)
	}
	// The block styles functions.php registers, read without running it.
	if src, ok := c.text("functions.php", maxStyle); ok {
		styles := readBlockStyles(src)
		for _, s := range styles {
			own, n := cleanCSS(s.CSS, c.cssURL(""))
			dropped += n
			css.WriteString(own + "\n")
		}
		if len(styles) > 0 {
			c.report.add(Note{Code: "theme.block-styles", File: "functions.php", Args: []any{len(styles)}})
		}
	}
	gen, n3 := cleanCSS(c.gen.b.String(), c.bundledURL)
	css.WriteString(gen)
	if n := dropped + n1 + n2 + n3; n > 0 {
		c.report.add(Note{Code: "css.dropped", Args: []any{n}})
	}

	// The other mode of the literal colours.
	start := len(baseCSS) + len(blocksCSS) + 2 + scheme.b.Len()
	literal := css.String()[start:]
	if ownDark {
		css.WriteString(modeRules(literal, lighten, false))
	} else {
		css.WriteString(modeRules(literal, darken, true))
	}
	out := css.String()
	if err := customize.CheckCSS(out, themeCSS); err != nil {
		return fmt.Errorf("the converted stylesheet does not pass the theme checks: %v", err)
	}
	if len(out) > maxCSSOut {
		return fmt.Errorf("the converted stylesheet is too large (%d KiB)", len(out)>>10)
	}
	c.out["static/theme.css"] = []byte(out)
	return nil
}

// allMarkup is every template, part and pattern, for the fonts they name.
func (c *converter) allMarkup() string {
	var b strings.Builder
	for _, s := range c.parts {
		b.WriteString(s)
	}
	for _, p := range c.patterns {
		b.WriteString(p.Markup)
	}
	for f := range c.files {
		if strings.HasPrefix(f, c.tmplDir+"/") {
			if s, ok := c.text(f, maxText); ok {
				b.WriteString(s)
			}
		}
	}
	return b.String()
}

// The manifest, the translations and the credits.

func (c *converter) writeManifest() error {
	m := theme.Manifest{
		Slug:        c.slug,
		Name:        truncateText(c.header["Theme Name"], 80),
		Version:     truncateText(c.header["Version"], 100),
		Description: truncateText(c.header["Description"], 480),
		Author:      truncateText(c.header["Author"], 100),
		License:     truncateText(c.header["License"], 100),
		Menus:       c.menus,
		WidgetAreas: []theme.WidgetArea{
			{ID: "top", Label: "Top bar", Description: "A full-width strip above the header, for announcements."},
			{ID: "footer", Label: "Footer", Description: "Above the footer line."},
		},
		Tokens: &theme.Tokens{Light: c.lightTokens, Dark: c.darkTokens},
	}
	if len(m.Menus) == 0 {
		m.Menus = []theme.MenuLocation{{ID: "primary", Label: "Main menu"}}
	}
	if len(c.theme.variations) > 0 {
		o := theme.Option{Key: "style", Label: "Colour style", Type: "select", Default: "default",
			Help:    "The colour variations of the original theme.",
			Choices: []theme.Choice{{Value: "default", Label: "Default"}}}
		opts := map[string]theme.TokenOverrides{}
		for _, v := range c.theme.variations {
			o.Choices = append(o.Choices, theme.Choice{Value: v.slug, Label: v.title})
			tk := c.variationTokens[v.slug]
			opts[v.slug] = theme.TokenOverrides{Light: tk[0], Dark: tk[1]}
		}
		m.Options = append(m.Options, o)
		m.Tokens.Options = map[string]map[string]theme.TokenOverrides{"style": opts}
	}
	// Swatches: the scheme's colours, then the palette's.
	var sw []string
	addSwatch := func(v string) {
		if h, ok := customize.NormalizeHex(v); ok && !slices.Contains(sw, h) && len(sw) < 12 {
			sw = append(sw, h)
		}
	}
	addSwatch(c.lightTokens["background"])
	addSwatch(c.lightTokens["foreground"])
	addSwatch(c.lightTokens["primary"])
	own := c.lightPalette.ownColours()
	for _, s := range c.lightPalette.slugs {
		if own && coreSlug(s) {
			continue
		}
		if col, ok := parseColour(c.lightPalette.values[s]); ok && col.a == 1 {
			addSwatch(col.hex())
		}
	}
	m.Swatches = sw
	if m.Description == "" {
		m.Description = "Converted from the WordPress theme " + m.Name + "."
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	c.out["theme.json"] = append(b, '\n')

	// The labels in Spanish, the other language of the admin.
	es := map[string]string{
		"Main menu": "Menú principal", "Secondary menu": "Menú secundario", "Footer menu": "Menú del pie", "Menu": "Menú",
		"Top bar": "Barra superior", "Footer": "Pie", "A full-width strip above the header, for announcements.": "Una franja a todo el ancho sobre la cabecera, para avisos.",
		"Above the footer line.": "Sobre la línea del pie.", "Colour style": "Estilo de color", "Default": "Predeterminado",
		"The colour variations of the original theme.": "Las variaciones de color del tema original.",
	}
	for _, ml := range m.Menus {
		var n int
		for _, base := range []string{"Footer menu", "Menu", "Main menu", "Secondary menu"} {
			if _, err := fmt.Sscanf(strings.TrimPrefix(ml.Label, base+" "), "%d", &n); err == nil && strings.HasPrefix(ml.Label, base+" ") {
				es[ml.Label] = fmt.Sprintf("%s %d", es[base], n)
			}
		}
	}
	eb, err := json.MarshalIndent(es, "", "  ")
	if err != nil {
		return err
	}
	c.out["i18n/es.json"] = append(eb, '\n')
	return nil
}

// writeDocs keeps the theme's licence and credits: README.md says where
// the theme comes from, who made it and under which licence, with the
// original readme.txt (its credits for bundled fonts and images); the
// licence text is the theme's own, or the GPL its header names.
func (c *converter) writeDocs() {
	h := c.header
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", mdText(h["Theme Name"]))
	fmt.Fprintf(&b, "Converted for sap13ns from the WordPress block theme **%s**", mdText(h["Theme Name"]))
	if v := h["Version"]; v != "" {
		fmt.Fprintf(&b, " %s", mdText(v))
	}
	b.WriteString(". The design, its templates, patterns, styles, fonts and images are the work of its authors.\n\n")
	for _, k := range []string{"Theme URI", "Author", "Author URI", "License", "License URI", "Copyright"} {
		if v := h[k]; v != "" {
			fmt.Fprintf(&b, "- %s: %s\n", k, mdText(v))
		}
	}
	b.WriteString("\nThe converted theme keeps the original's licence. sap13ns itself is licensed separately (CPAL-1.0); the credit line the sap13ns layout shows is rendered by the server, not by this theme.\n")
	readme := ""
	for _, f := range []string{"readme.txt", "README.txt", "readme.md", "README.md"} {
		if s, ok := c.text(f, 256<<10); ok {
			readme = s
			break
		}
	}
	if readme != "" {
		b.WriteString("\n## Original readme\n\n```text\n" + strings.ReplaceAll(readme, "```", "'''") + "\n```\n")
	}
	c.out["README.md"] = []byte(b.String())

	for _, f := range []string{"LICENSE", "LICENSE.txt", "LICENSE.md", "license.txt", "COPYING", "COPYING.txt"} {
		if s, ok := c.text(f, 256<<10); ok {
			c.out["LICENSE.txt"] = []byte(s)
			return
		}
	}
	lic := strings.ToLower(h["License"] + " " + h["License URI"])
	switch {
	case strings.Contains(lic, "gpl-3") || strings.Contains(lic, "gplv3") || strings.Contains(lic, "gpl v3") || strings.Contains(lic, "gpl-3.0") || strings.Contains(lic, "version 3"):
		c.out["LICENSE.txt"] = []byte(gpl3)
	case strings.Contains(lic, "gpl"):
		c.out["LICENSE.txt"] = []byte(gpl2)
	}
}

// mdText keeps a header value as plain Markdown text.
func mdText(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "[", "\\[", "]", "\\]", "`", "'").Replace(s)
}
