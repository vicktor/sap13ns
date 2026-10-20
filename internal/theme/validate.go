package theme

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"sap13ns/internal/archive"
	"sap13ns/internal/customize"
	"sap13ns/internal/media"
)

// The validation of an uploaded theme. Nothing of the archive is trusted:
// it is unpacked under the archive limits into a staging folder, every
// file must be one a theme needs, of a type the server serves as passive
// content, and the templates go through the checks of check.go before the
// theme can be installed. Every problem found is reported, up to
// MaxProblems, so that an author can fix them in one go.

// archiveRules are the limits of a theme archive.
type archiveRules struct{ limits archive.Limits }

var themeArchive = archiveRules{archive.Limits{
	MaxBytes:    20 << 20,
	MaxEntries:  300,
	MaxUnpacked: 40 << 20,
	MaxRatio:    100,
}}

// MaxArchiveBytes is the largest theme archive accepted.
const MaxArchiveBytes = 20 << 20

const maxTemplates = 60

// The kinds of files a theme holds.
const (
	kindManifest = "manifest"
	kindTemplate = "template"
	kindI18n     = "i18n"
	kindStatic   = "static"
	kindSrc      = "src"
	kindDoc      = "doc"
)

// limit is the size limit of a file of the theme.
func (l archiveRules) limit(name string) int64 {
	switch kind, _ := classify(name); kind {
	case kindManifest:
		return 64 << 10
	case kindTemplate, kindI18n, kindDoc:
		return 256 << 10
	case kindSrc:
		return 1 << 20
	case kindStatic:
		if strings.EqualFold(path.Ext(name), ".css") {
			return 1 << 20
		}
		return 4 << 20
	}
	return 0
}

var (
	segRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	docRe     = regexp.MustCompile(`(?i)^(readme|license|licence|copying|changelog)(\.(md|txt))?$`)
	langRe    = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	idRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	pageFiles = func() map[string]bool {
		m := map[string]bool{"layout": true}
		for _, p := range pageNames {
			m[p] = true
		}
		return m
	}()
)

// staticTypes maps the extensions static/ may hold to what media.Sniff
// must see in them. They are all passive: the browser never runs them.
var staticTypes = map[string][]string{
	".css":   {"text/plain"},
	".woff2": {"font/woff2"},
	".woff":  {"font/woff"},
	".png":   {"image/png"},
	".jpg":   {"image/jpeg"},
	".jpeg":  {"image/jpeg"},
	".webp":  {"image/webp"},
	".avif":  {"image/avif"},
	".gif":   {"image/gif"},
	".ico":   {"image/x-icon", "image/png"}, // an .ico may hold a PNG image
}

// StaticExtensions lists what static/ may hold, for messages and docs.
func StaticExtensions() string {
	exts := make([]string, 0, len(staticTypes))
	for e := range staticTypes {
		exts = append(exts, e)
	}
	slices.Sort(exts)
	return strings.Join(exts, ", ")
}

// activeExt are extensions of files browsers may run or render as pages.
var activeExt = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".jsx": true, ".ts": true, ".html": true, ".htm": true,
	".xhtml": true, ".shtml": true, ".xml": true, ".xsl": true, ".xslt": true, ".wasm": true, ".swf": true, ".php": true,
}

// classify sorts a file of the archive into the theme's layout, or
// returns the problem with its place.
func classify(name string) (string, *Problem) {
	segs := strings.Split(name, "/")
	for _, s := range segs {
		if !segRe.MatchString(s) {
			return "", &Problem{Code: "archive.bad-name", File: name}
		}
	}
	switch {
	case name == "theme.json":
		return kindManifest, nil
	case len(segs) == 1 && docRe.MatchString(name):
		return kindDoc, nil
	case segs[0] == "templates":
		switch {
		case len(segs) == 2 && strings.HasSuffix(segs[1], ".html") && pageFiles[strings.TrimSuffix(segs[1], ".html")]:
			return kindTemplate, nil
		case len(segs) == 3 && (segs[1] == "partials" || segs[1] == "sections") && strings.HasSuffix(segs[2], ".html"):
			return kindTemplate, nil
		}
		return "", &Problem{Code: "layout.template-name", File: name}
	case segs[0] == "i18n" && len(segs) == 2 && strings.HasSuffix(segs[1], ".json"):
		return kindI18n, nil
	case segs[0] == "static" && len(segs) > 1:
		ext := strings.ToLower(path.Ext(name))
		switch {
		case ext == ".svg" || ext == ".svgz":
			return "", &Problem{Code: "static.svg", File: name}
		case activeExt[ext]:
			return "", &Problem{Code: "static.script", File: name}
		case staticTypes[ext] == nil:
			return "", &Problem{Code: "static.type", File: name, Args: []any{StaticExtensions()}}
		}
		return kindStatic, nil
	case segs[0] == "src" && len(segs) > 1:
		if strings.ToLower(path.Ext(name)) != ".css" {
			return "", &Problem{Code: "src.type", File: name}
		}
		return kindSrc, nil
	}
	if activeExt[strings.ToLower(path.Ext(name))] {
		return "", &Problem{Code: "static.script", File: name}
	}
	return "", &Problem{Code: "layout.not-allowed", File: name}
}

// validation is the outcome of validate.
type validation struct {
	slug     string
	theme    *Theme // loaded from the staging folder, when nothing else failed
	problems []Problem
}

func (v *validation) add(p Problem) {
	if len(v.problems) < MaxProblems {
		v.problems = append(v.problems, p)
	}
}

func (v *validation) full() bool { return len(v.problems) >= MaxProblems }

// validate unpacks a theme archive into root, an empty folder, and checks
// it. Problems are the theme's fault; an error is the server's.
func (m *Manager) validate(r io.ReaderAt, size int64, root string) (*validation, error) {
	v := &validation{}
	z, err := archive.Open(r, size, "theme.json", themeArchive.limits)
	if err != nil {
		p, ok := fromArchive(err, themeArchive)
		if !ok {
			return nil, err
		}
		v.add(p)
		return v, nil
	}

	// The layout, before anything is unpacked.
	templates := 0
	for _, name := range z.Files() {
		kind, p := classify(name)
		if p != nil {
			v.add(*p)
			continue
		}
		if kind == kindTemplate {
			templates++
		}
	}
	if templates > maxTemplates {
		v.add(Problem{Code: "layout.too-many", Args: []any{maxTemplates}})
	}

	// The slug: the archive's folder, or theme.json's "slug".
	folder := strings.TrimSuffix(z.Prefix, "/")
	raw, err := z.ReadFile("theme.json", themeArchive.limit("theme.json"))
	if err != nil {
		v.add(Problem{Code: "archive.file-too-large", File: "theme.json", Args: []any{themeArchive.limit("theme.json") >> 10}})
		return v, nil
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		v.add(Problem{Code: "manifest.json", File: "theme.json", Args: []any{jsonError(raw, err)}})
		return v, nil
	}
	switch {
	case man.Slug != "" && !slugRe.MatchString(man.Slug):
		v.add(Problem{Code: "manifest.slug", File: "theme.json", Args: []any{man.Slug}})
	case folder != "" && !slugRe.MatchString(folder):
		v.add(Problem{Code: "manifest.slug", Args: []any{folder}})
	case man.Slug != "" && folder != "" && man.Slug != folder:
		v.add(Problem{Code: "manifest.slug-mismatch", File: "theme.json", Args: []any{man.Slug, folder}})
	case man.Slug == "" && folder == "":
		v.add(Problem{Code: "manifest.no-slug"})
	default:
		v.slug = cmp.Or(man.Slug, folder)
		if builtin, network := m.taken(v.slug); builtin {
			v.add(Problem{Code: "manifest.builtin", Args: []any{v.slug}})
		} else if network {
			v.add(Problem{Code: "manifest.network", Args: []any{v.slug}})
		}
	}
	checkManifest(v, &man)
	// The layout, the not-found page and a front page (front.html, a
	// landing, or home.html, the latest posts); every other page falls back
	// to the shared templates.
	for _, f := range []string{"templates/layout.html", "templates/404.html", "static/theme.css"} {
		if !z.Has(f) {
			v.add(Problem{Code: "layout.missing", File: f})
		}
	}
	if !z.Has("templates/front.html") && !z.Has("templates/home.html") {
		v.add(Problem{Code: "layout.missing-front", File: "templates/front.html"})
	}
	if len(v.problems) > 0 {
		return v, nil
	}

	allow := func(name string) (int64, error) {
		if _, p := classify(name); p != nil {
			return 0, archive.Errf("package.bad-name", name, "not part of a theme")
		}
		return themeArchive.limit(name), nil
	}
	if _, err := z.Extract(root, allow); err != nil {
		p, ok := fromArchive(err, themeArchive)
		if !ok {
			return nil, err
		}
		v.add(p)
		return v, nil
	}

	// The content of every file.
	fsys := os.DirFS(root)
	for _, name := range z.Files() {
		if v.full() {
			break
		}
		kind, _ := classify(name)
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		switch kind {
		case kindStatic:
			checkStatic(v, name, b)
		case kindSrc:
			// The stylesheet's source, never served nor read by the
			// server (only static/ is): it only has to be text.
			if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
				v.add(Problem{Code: "src.type", File: name})
			}
		case kindI18n:
			checkI18n(v, name, b)
		case kindTemplate:
			if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
				v.add(Problem{Code: "template.encoding", File: name})
			}
		case kindDoc:
			// Never served nor parsed.
		}
	}
	if len(v.problems) > 0 {
		return v, nil
	}

	// The templates: they must parse with the server's functions and the
	// shared templates, then pass the checks of check.go.
	t, err := m.loadTheme(v.slug, fsys, false, true)
	if err != nil {
		v.add(parseProblem(fsys, err))
		return v, nil
	}
	for _, p := range m.checkTemplates(fsys, t) {
		v.add(p)
	}
	if len(v.problems) == 0 {
		v.theme = t
	}
	return v, nil
}

// themeCSS are the rules of a theme's stylesheets: those of the
// customiser's custom CSS, with the relative paths and data: images a
// compiled stylesheet uses, and the credit line it styles.
var themeCSS = customize.CSSRules{RelativeURLs: true, DataImages: true, FooterMeta: true}

func checkStatic(v *validation, name string, b []byte) {
	ext := strings.ToLower(path.Ext(name))
	got := media.Sniff(b)
	if !slices.Contains(staticTypes[ext], got) {
		if strings.HasPrefix(got, "text/html") || strings.Contains(got, "javascript") || strings.Contains(got, "xml") {
			v.add(Problem{Code: "static.script", File: name})
			return
		}
		v.add(Problem{Code: "static.mismatch", File: name, Args: []any{got}})
		return
	}
	if ext == ".css" {
		if err := customize.CheckCSS(string(b), themeCSS); err != nil {
			v.add(Problem{Code: "static.css", File: name, Args: []any{err.Error()}})
		}
	}
}

func checkI18n(v *validation, name string, b []byte) {
	lang := strings.TrimSuffix(path.Base(name), ".json")
	if !langRe.MatchString(lang) {
		v.add(Problem{Code: "i18n.lang", File: name, Args: []any{lang}})
		return
	}
	cat := map[string]string{}
	if err := json.Unmarshal(b, &cat); err != nil {
		v.add(Problem{Code: "i18n.invalid", File: name, Args: []any{jsonError(b, err)}})
		return
	}
	keys := make([]string, 0, len(cat))
	for k := range cat {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		for _, s := range []string{k, cat[k]} {
			if !boundedFormat(s) {
				v.add(Problem{Code: "format.width", File: name, Args: []any{s}})
			}
			for _, slot := range TokenSlots {
				if strings.Contains(s, slot) {
					v.add(Problem{Code: "template.token", File: name, Args: []any{slot}})
				}
			}
		}
	}
}

// boundedFormat reports whether a fmt format asks only for widths and
// precisions up to 99, written in the format: "%*d" takes its width from
// an argument, and "%999999d" writes a megabyte.
func boundedFormat(f string) bool {
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			continue
		}
		i++
		digits := 0
		for ; i < len(f); i++ {
			c := f[i]
			switch {
			case c == '%' && digits == 0:
			case c == '*':
				return false
			case c >= '0' && c <= '9':
				if digits++; digits > 2 {
					return false
				}
				continue
			case c == '.' || c == '[' || c == ']' || c == '+' || c == '-' || c == '#' || c == ' ':
				digits = 0
				continue
			}
			break
		}
	}
	return true
}

// checkContent checks the manifest's editable texts and images.
func checkContent(v *validation, m *Manifest, text func(field, s string, limit int, required bool), id func(kind, s string) bool) {
	if len(m.Content) > MaxContentGroups {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"content", MaxContentGroups}})
	}
	groups, keys, n := map[string]bool{}, map[string]bool{}, 0
	for _, g := range m.Content {
		if id("content[].id", g.ID) && groups[g.ID] {
			v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"content[].id", g.ID}})
		}
		groups[g.ID] = true
		text("content[].label", g.Label, maxLabel, true)
		for _, f := range g.Fields {
			n++
			if id("content[].fields[].key", f.Key) && keys[f.Key] {
				v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"content[].fields[].key", f.Key}})
			}
			keys[f.Key] = true
			text("content[].fields[].label", f.Label, maxLabel, true)
			text("content[].fields[].help", f.Help, maxHelp, false)
			text("content[].fields[].default", f.Default, maxDefault, false)
			switch f.Type {
			case ContentText, ContentTextarea, ContentLines, ContentImage:
			default:
				v.add(Problem{Code: "manifest.content-type", File: "theme.json", Args: []any{f.Key, f.Type}})
			}
		}
	}
	if n > MaxContentFields {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"content[].fields", MaxContentFields}})
	}
}

// checkSections checks the manifest's sections for the page builder.
func checkSections(v *validation, m *Manifest, text func(field, s string, limit int, required bool), id func(kind, s string) bool) {
	if len(m.Sections) > MaxSections {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"sections", MaxSections}})
	}
	content := map[string]string{}
	for _, g := range m.Content {
		for _, f := range g.Fields {
			content[f.Key] = f.Type
		}
	}
	regions := map[string]bool{"front": true, "home": true, "single": true, "page": true, "archive": true, "search": true, "404": true}
	ids := map[string]bool{}
	for _, sec := range m.Sections {
		if id("sections[].id", sec.ID) && ids[sec.ID] {
			v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"sections[].id", sec.ID}})
		}
		ids[sec.ID] = true
		text("sections[].label", sec.Label, maxLabel, true)
		text("sections[].help", sec.Help, maxHelp, false)
		if sec.Anchor != "" {
			id("sections[].anchor", sec.Anchor)
		}
		if sec.Icon != "" {
			id("sections[].icon", sec.Icon)
		}
		for _, r := range sec.Where {
			if !regions[r] {
				v.add(Problem{Code: "manifest.section-where", File: "theme.json", Args: []any{sec.ID, r}})
			}
		}
		if len(sec.Fields) > MaxSectionFields {
			v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"sections[].fields", MaxSectionFields}})
		}
		keys := map[string]bool{}
		for _, f := range sec.Fields {
			if id("sections[].fields[].key", f.Key) && keys[f.Key] {
				v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"sections[].fields[].key", f.Key}})
			}
			keys[f.Key] = true
			text("sections[].fields[].label", f.Label, maxLabel, true)
			text("sections[].fields[].help", f.Help, maxHelp, false)
			text("sections[].fields[].default", f.Default, maxDefault, false)
			switch f.Type {
			case ContentText, ContentTextarea, ContentLines, ContentImage, SectionURL:
			default:
				v.add(Problem{Code: "manifest.section-type", File: "theme.json", Args: []any{sec.ID, f.Key, f.Type}})
			}
			if f.Content != "" {
				ct, ok := content[f.Content]
				if !ok || (ct == ContentImage) != (f.Type == ContentImage) || f.Type == SectionURL {
					v.add(Problem{Code: "manifest.section-content", File: "theme.json", Args: []any{sec.ID, f.Key, f.Content}})
				}
			}
		}
	}
	for _, sid := range m.FrontSections {
		if !ids[sid] {
			v.add(Problem{Code: "manifest.front-section", File: "theme.json", Args: []any{sid}})
		}
	}
}

// Manifest rules.
const (
	maxName        = 80
	maxShortText   = 100
	maxDescription = 500
	maxLabel       = 120
	maxHelp        = 300
	maxDefault     = 1000

	MaxContentGroups = 40
	MaxContentFields = 400
	MaxSections      = 40
	MaxSectionFields = 60
)

func checkManifest(v *validation, m *Manifest) {
	text := func(field, s string, limit int, required bool) {
		switch {
		case s == "" && required:
			v.add(Problem{Code: "manifest.required", File: "theme.json", Args: []any{field}})
		case utf8.RuneCountInString(s) > limit || strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }):
			v.add(Problem{Code: "manifest.text", File: "theme.json", Args: []any{field, limit}})
		}
	}
	id := func(kind, s string) bool {
		if !idRe.MatchString(s) {
			v.add(Problem{Code: "manifest.id", File: "theme.json", Args: []any{kind, s}})
			return false
		}
		return true
	}
	text("name", m.Name, maxName, true)
	text("version", m.Version, maxShortText, false)
	text("description", m.Description, maxDescription, false)
	text("author", m.Author, maxShortText, false)
	text("license", m.License, maxShortText, false)

	seen := map[string]bool{}
	if len(m.Menus) > 10 {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"menus", 10}})
	}
	for _, ml := range m.Menus {
		if id("menus[].id", ml.ID) && seen["menu:"+ml.ID] {
			v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"menus[].id", ml.ID}})
		}
		seen["menu:"+ml.ID] = true
		text("menus[].label", ml.Label, maxLabel, true)
	}
	if len(m.WidgetAreas) > 20 {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"widget_areas", 20}})
	}
	for _, a := range m.WidgetAreas {
		if id("widget_areas[].id", a.ID) && seen["area:"+a.ID] {
			v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"widget_areas[].id", a.ID}})
		}
		seen["area:"+a.ID] = true
		text("widget_areas[].label", a.Label, maxLabel, true)
		text("widget_areas[].description", a.Description, maxHelp, false)
	}
	if len(m.Options) > 40 {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"options", 40}})
	}
	options := map[string]*Option{}
	for i := range m.Options {
		o := &m.Options[i]
		if id("options[].key", o.Key) && options[o.Key] != nil {
			v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"options[].key", o.Key}})
		}
		options[o.Key] = o
		text("options[].label", o.Label, maxLabel, true)
		text("options[].help", o.Help, maxHelp, false)
		text("options[].default", o.Default, maxDefault, false)
		switch o.Type {
		case "text", "textarea":
		case "bool":
			if o.Default != "" && o.Default != "true" && o.Default != "false" {
				v.add(Problem{Code: "manifest.option-default", File: "theme.json", Args: []any{o.Key}})
			}
		case "select":
			if len(o.Choices) == 0 || len(o.Choices) > 50 {
				v.add(Problem{Code: "manifest.option-choices", File: "theme.json", Args: []any{o.Key, 50}})
				continue
			}
			values := map[string]bool{}
			for _, c := range o.Choices {
				text("options[].choices[].value", c.Value, maxShortText, true)
				text("options[].choices[].label", c.Label, maxLabel, true)
				if values[c.Value] {
					v.add(Problem{Code: "manifest.duplicate", File: "theme.json", Args: []any{"options[].choices[].value", c.Value}})
				}
				values[c.Value] = true
			}
			if !values[o.Default] {
				v.add(Problem{Code: "manifest.option-default", File: "theme.json", Args: []any{o.Key}})
			}
		default:
			v.add(Problem{Code: "manifest.option-type", File: "theme.json", Args: []any{o.Key, o.Type}})
		}
	}
	checkContent(v, m, text, id)
	checkSections(v, m, text, id)
	if len(m.Swatches) > 12 {
		v.add(Problem{Code: "manifest.too-many", File: "theme.json", Args: []any{"swatches", 12}})
	}
	for _, s := range m.Swatches {
		if _, ok := customize.NormalizeHex(s); !ok {
			v.add(Problem{Code: "manifest.colour", File: "theme.json", Args: []any{fmt.Sprintf("swatches %q", s)}})
		}
	}
	if m.Tokens == nil {
		return
	}
	colours := func(where string, c map[string]string) {
		keys := make([]string, 0, len(c))
		for k := range c {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if !slices.Contains(customize.ColorTokens, k) {
				v.add(Problem{Code: "manifest.token", File: "theme.json", Args: []any{k, strings.Join(customize.ColorTokens, ", ")}})
			} else if _, ok := customize.NormalizeHex(c[k]); !ok {
				v.add(Problem{Code: "manifest.colour", File: "theme.json", Args: []any{fmt.Sprintf("tokens.%s.%s", where, k)}})
			}
		}
	}
	colours("light", m.Tokens.Light)
	colours("dark", m.Tokens.Dark)
	keys := make([]string, 0, len(m.Tokens.Options))
	for k := range m.Tokens.Options {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		o := options[key]
		for value, ov := range m.Tokens.Options[key] {
			known := o != nil && (o.Type == "bool" && (value == "true" || value == "false") ||
				slices.ContainsFunc(o.Choices, func(c Choice) bool { return c.Value == value }))
			if !known {
				v.add(Problem{Code: "manifest.token-option", File: "theme.json", Args: []any{key + "=" + value}})
				continue
			}
			colours("options."+key+"."+value+".light", ov.Light)
			colours("options."+key+"."+value+".dark", ov.Dark)
		}
	}
}

// jsonError describes a JSON error with its line.
func jsonError(b []byte, err error) string {
	var se *json.SyntaxError
	var te *json.UnmarshalTypeError
	off := int64(-1)
	switch {
	case errors.As(err, &se):
		off = se.Offset
	case errors.As(err, &te):
		off = te.Offset
	}
	msg := strings.TrimPrefix(err.Error(), "json: ")
	if off >= 0 && off <= int64(len(b)) {
		return fmt.Sprintf("line %d: %s", bytes.Count(b[:off], []byte("\n"))+1, msg)
	}
	return msg
}

var (
	undefinedFunc = regexp.MustCompile(`function "([^"]+)" not defined`)
	parseWhere    = regexp.MustCompile(`template: ([A-Za-z0-9._-]+\.html):(\d+):`)
)

// parseProblem turns an error of html/template's parse into a problem,
// with the file and line when it names them.
func parseProblem(fsys fs.FS, err error) Problem {
	msg := err.Error()
	p := Problem{Code: "template.parse", Args: []any{cleanParseError(err)}}
	if m := undefinedFunc.FindStringSubmatch(msg); m != nil {
		p = Problem{Code: "template.func", Args: []any{m[1]}}
	}
	if m := parseWhere.FindStringSubmatch(msg); m != nil {
		for _, f := range []string{"templates/" + m[1], "templates/partials/" + m[1], "templates/sections/" + m[1]} {
			if _, err := fs.Stat(fsys, f); err == nil {
				p.File = f
				p.Line, _ = strconv.Atoi(m[2])
				break
			}
		}
	}
	return p
}

// cleanParseError strips the server's paths from a template error.
func cleanParseError(err error) string {
	msg := strings.TrimPrefix(err.Error(), "template: ")
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
