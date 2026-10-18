// Package builder holds the block builder's model: per-theme layouts made
// of sections, columns and blocks for the global header and footer and for
// each page type, their schema and their validation.
//
// A layout only changes the regions it defines: a theme renders its own
// template for every other region. Whatever a layout says, the footer
// always shows shared/footer-meta, the copyright line and the sap13ns
// credit that the CPAL licence requires (see Normalize).
package builder

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Version of the layout JSON format.
const Version = 1

// Limits.
const (
	MaxSections = 40  // per region
	MaxBlocks   = 40  // per column
	MaxTotal    = 600 // blocks in all regions
	MaxJSON     = 512 << 10
	MaxURL      = 2000
	MetaType    = "footer-meta"
	ContentType = "single-content"
)

// Layouts is a theme's block layouts. A nil region uses the theme's template.
type Layouts struct {
	Version int                `json:"version"`
	Header  *Region            `json:"header,omitempty"`
	Footer  *Region            `json:"footer,omitempty"`
	Pages   map[string]*Region `json:"pages,omitempty"`
}

// Region is a stack of sections.
type Region struct {
	Sections []Section `json:"sections"`
	// TopBar (header only): "off" hides the theme's Top bar widget area,
	// which is shown above a block header by default.
	TopBar string `json:"top_bar,omitempty"`
}

// Section is a row of 1–3 columns.
type Section struct {
	ID      string            `json:"id"`
	Layout  string            `json:"layout"` // a Presets key
	Style   map[string]string `json:"style,omitempty"`
	Columns [][]Block         `json:"columns"`
}

// Block is one block. Settings hold the main language's values; I18n the
// translations of translatable fields (lang -> field -> text), like terms.
type Block struct {
	ID       string                       `json:"id"`
	Type     string                       `json:"type"`
	Settings map[string]string            `json:"settings,omitempty"`
	I18n     map[string]map[string]string `json:"i18n,omitempty"`
}

// FieldError is a validation problem with the JSON path of the field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

// Errors is a list of field errors.
type Errors []FieldError

func (es Errors) Error() string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// Context is what validation needs to know about the site and the theme.
type Context struct {
	Menus []string // the theme's menu locations
	Areas []string // the theme's widget areas
	Langs []string // languages besides the main one (for I18n)
	// Media reports whether an image exists. Nil accepts every positive id.
	Media func(id int64) bool
}

// Region returns the region named name (nil when the theme's template is used).
func (l *Layouts) Region(name string) *Region {
	if l == nil {
		return nil
	}
	switch name {
	case "header":
		return l.Header
	case "footer":
		return l.Footer
	}
	return l.Pages[name]
}

// IsZero reports whether the layouts change nothing.
func (l *Layouts) IsZero() bool {
	return l == nil || (l.Header == nil && l.Footer == nil && len(l.Pages) == 0)
}

// JSON encodes the layouts (normalised layouts encode deterministically).
func (l *Layouts) JSON() []byte {
	b, _ := json.Marshal(l)
	return b
}

// Parse decodes and validates layouts strictly: unknown fields, types,
// settings and values are errors, so a typo never silently does nothing.
func Parse(data []byte, c Context) (*Layouts, error) {
	if len(data) > MaxJSON {
		return nil, Errors{{Message: "the layout is too large"}}
	}
	l := &Layouts{}
	if strings.TrimSpace(string(data)) == "" {
		l.Version = Version
		return l, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(l); err != nil {
		return nil, Errors{{Message: "invalid layout JSON: " + err.Error()}}
	}
	if errs := l.normalize(c, true); len(errs) > 0 {
		return nil, errs
	}
	return l, nil
}

// Load decodes stored layouts leniently: what no longer validates (a
// deleted image, a menu the theme dropped) is reset or removed so the rest
// keeps working, and a page without its required block falls back to the
// theme's template.
func Load(data []byte, c Context) *Layouts {
	l := &Layouts{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, l)
	}
	l.normalize(c, false)
	return l
}

// Normalize cleans the layouts in place (strict: reports every problem).
func (l *Layouts) Normalize(c Context) Errors { return l.normalize(c, true) }

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (l *Layouts) normalize(c Context, strict bool) Errors {
	var errs Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	if l.Version != 0 && l.Version != Version {
		add("version", "unsupported version %d", l.Version)
	}
	l.Version = Version
	seen := map[string]bool{}
	total := 0
	newID := func(prefix string) string {
		for i := 1; ; i++ {
			id := prefix + "-" + strconv.Itoa(i)
			if !seen[id] {
				seen[id] = true
				return id
			}
		}
	}
	checkID := func(field string, id *string, prefix string) {
		switch {
		case !idRe.MatchString(*id):
			if strict {
				add(field+".id", "use 1–32 lower-case letters, digits and hyphens")
			}
			*id = newID(prefix)
		case seen[*id]:
			if strict {
				add(field+".id", "%q is used twice", *id)
			}
			*id = newID(prefix)
		default:
			seen[*id] = true
		}
	}

	region := func(name string, r *Region) *Region {
		if r == nil {
			return nil
		}
		path := name
		if name != "header" && name != "footer" {
			path = "pages." + name
		}
		if name == "header" {
			if r.TopBar != "" && r.TopBar != "off" {
				add(path+".top_bar", "use \"off\" or leave it empty")
				r.TopBar = ""
			}
		} else if r.TopBar != "" {
			add(path+".top_bar", "only the header has a top bar")
			r.TopBar = ""
		}
		if len(r.Sections) > MaxSections {
			add(path+".sections", "at most %d sections", MaxSections)
			r.Sections = r.Sections[:MaxSections]
		}
		once := map[string]bool{}
		hasContent := false
		secs := r.Sections[:0]
		for si, s := range r.Sections {
			sp := fmt.Sprintf("%s.sections[%d]", path, si)
			checkID(sp, &s.ID, "s")
			n, ok := Presets[s.Layout]
			if !ok {
				add(sp+".layout", "unknown column layout %q", s.Layout)
				s.Layout, n = "1", 1
			}
			if len(s.Columns) != n {
				if strict {
					add(sp+".columns", "layout %s has %d columns, got %d", s.Layout, n, len(s.Columns))
				}
				s.Columns = fitColumns(s.Columns, n)
			}
			style := map[string]string{}
			for k, v := range s.Style {
				opts, ok := SectionStyles[k]
				if !ok {
					add(sp+".style."+k, "unknown style")
					continue
				}
				if !contains(opts, v) {
					add(sp+".style."+k, "unknown value %q", v)
					continue
				}
				if v != opts[0] { // the first value is the default
					style[k] = v
				}
			}
			if len(style) == 0 {
				style = nil
			}
			s.Style = style
			for ci := range s.Columns {
				col := s.Columns[ci]
				if len(col) > MaxBlocks {
					add(fmt.Sprintf("%s.columns[%d]", sp, ci), "at most %d blocks", MaxBlocks)
					col = col[:MaxBlocks]
				}
				out := col[:0]
				for bi, b := range col {
					bp := fmt.Sprintf("%s.columns[%d][%d]", sp, ci, bi)
					t, ok := TypeOf(b.Type)
					if !ok {
						add(bp+".type", "unknown block %q", b.Type)
						continue
					}
					if !t.Allowed(name) {
						add(bp+".type", "%s cannot be placed in %s", t.Label, name)
						continue
					}
					if t.Once {
						if once[t.Type] {
							add(bp+".type", "only one %s per page", t.Label)
							continue
						}
						once[t.Type] = true
					}
					if total++; total > MaxTotal {
						add(bp, "at most %d blocks in all", MaxTotal)
						continue
					}
					checkID(bp, &b.ID, "b")
					errs = append(errs, cleanBlock(bp, &b, t, c)...)
					if b.Type == ContentType {
						hasContent = true
					}
					out = append(out, b)
				}
				s.Columns[ci] = out
			}
			secs = append(secs, s)
		}
		r.Sections = secs
		if (name == "single" || name == "page") && !hasContent {
			if strict {
				add(path, "add the Post content block: without it the %s would not show its text", map[string]string{"single": "post", "page": "page"}[name])
			} else {
				return nil
			}
		}
		if name == "footer" && !r.has(MetaType) {
			// The licence credit is not optional: put it back.
			id := MetaType
			if seen[id] {
				id = newID(MetaType)
			}
			seen[id] = true
			r.Sections = append(r.Sections, Section{ID: newID("credits"), Layout: "1",
				Columns: [][]Block{{{ID: id, Type: MetaType}}}})
		}
		return r
	}

	l.Header = region("header", l.Header)
	l.Footer = region("footer", l.Footer)
	pages := map[string]*Region{}
	keys := make([]string, 0, len(l.Pages))
	for k := range l.Pages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !contains(PageKinds, k) {
			add("pages."+k, "unknown page type")
			continue
		}
		if r := region(k, l.Pages[k]); r != nil {
			pages[k] = r
		}
	}
	if len(pages) == 0 {
		pages = nil
	}
	l.Pages = pages
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs
}

func (r *Region) has(typ string) bool {
	for _, s := range r.Sections {
		for _, col := range s.Columns {
			for _, b := range col {
				if b.Type == typ {
					return true
				}
			}
		}
	}
	return false
}

// HasBlock reports whether the region has a block of type typ.
func (r *Region) HasBlock(typ string) bool { return r != nil && r.has(typ) }

// fitColumns makes cols have n columns: extra columns' blocks move to the
// last one, missing columns are added empty.
func fitColumns(cols [][]Block, n int) [][]Block {
	out := make([][]Block, n)
	for i, c := range cols {
		j := i
		if j >= n {
			j = n - 1
		}
		out[j] = append(out[j], c...)
	}
	for i := range out {
		if out[i] == nil {
			out[i] = []Block{}
		}
	}
	return out
}

var langRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

// cleanBlock validates a block's settings against its type.
func cleanBlock(path string, b *Block, t *Type, c Context) Errors {
	var errs Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: path + "." + field, Message: fmt.Sprintf(format, args...)})
	}
	out := map[string]string{}
	keys := make([]string, 0, len(b.Settings))
	for k := range b.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, ok := t.Field(k)
		if !ok {
			add("settings."+k, "%s has no setting %q", t.Label, k)
			continue
		}
		v, msg := cleanValue(f, b.Settings[k], c)
		if msg != "" {
			add("settings."+k, "%s", msg)
			continue
		}
		if v != "" && v != f.Default {
			out[k] = v
		}
	}
	if len(out) == 0 {
		out = nil
	}
	b.Settings = out

	tr := map[string]map[string]string{}
	langs := make([]string, 0, len(b.I18n))
	for lang := range b.I18n {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		if !langRe.MatchString(lang) || !contains(c.Langs, lang) {
			add("i18n."+lang, "this site has no such language")
			continue
		}
		m := map[string]string{}
		for k, v := range b.I18n[lang] {
			f, ok := t.Field(k)
			if !ok || !f.I18n {
				add("i18n."+lang+"."+k, "this setting is not translatable")
				continue
			}
			v, msg := cleanValue(f, v, c)
			if msg != "" {
				add("i18n."+lang+"."+k, "%s", msg)
				continue
			}
			if v != "" {
				m[k] = v
			}
		}
		if len(m) > 0 {
			tr[lang] = m
		}
	}
	if len(tr) == 0 {
		tr = nil
	}
	b.I18n = tr
	return errs
}

// cleanValue normalises one value and returns a message when it is refused.
func cleanValue(f *Field, v string, c Context) (string, string) {
	if f.Type != "markdown" && f.Type != "textarea" {
		v = strings.TrimSpace(v)
	} else {
		v = strings.TrimSpace(strings.ReplaceAll(v, "\r\n", "\n"))
	}
	if strings.ContainsRune(v, 0) {
		return "", "contains a NUL character"
	}
	if v == "" {
		return "", ""
	}
	switch f.Type {
	case "text", "textarea", "markdown":
		if f.Type == "text" {
			v = strings.Join(strings.Fields(v), " ")
		}
		if f.Max > 0 && utf8.RuneCountInString(v) > f.Max {
			return "", fmt.Sprintf("at most %d characters", f.Max)
		}
	case "select":
		ok := false
		for _, ch := range f.Choices {
			ok = ok || ch.Value == v
		}
		if !ok {
			return "", fmt.Sprintf("unknown value %q", v)
		}
	case "bool":
		if v != "true" && v != "false" {
			return "", "use true or false"
		}
	case "int":
		n, err := strconv.Atoi(v)
		if err != nil || n < f.Min || (f.Max > 0 && n > f.Max) {
			return "", fmt.Sprintf("a whole number from %d to %d", f.Min, f.Max)
		}
		v = strconv.Itoa(n)
	case "url":
		if !SafeURL(v) {
			return "", "use https://…, a path on this site such as /about, #section or mailto:"
		}
	case "email":
		a, err := mail.ParseAddress(v)
		if err != nil || a.Address != v || len(v) > 254 {
			return "", "not an e-mail address"
		}
	case "image":
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 || (c.Media != nil && !c.Media(id)) {
			return "", "choose an image from the media library"
		}
		v = strconv.FormatInt(id, 10)
	case "menu":
		if !contains(c.Menus, v) {
			return "", fmt.Sprintf("this theme has no menu %q", v)
		}
	case "area":
		if !contains(c.Areas, v) {
			return "", fmt.Sprintf("this theme has no widget area %q", v)
		}
	default:
		return "", "unknown field type"
	}
	return v, ""
}

// SafeURL reports whether u may be used as a link: http(s), mailto: and
// tel: links, site paths and fragments. Never javascript: or data:.
func SafeURL(u string) bool {
	if u == "" || len(u) > MaxURL || strings.ContainsAny(u, " \t\r\n\\<>\"'`") {
		return false
	}
	switch {
	case strings.HasPrefix(u, "#"):
		return true
	case strings.HasPrefix(u, "/"):
		return !strings.HasPrefix(u, "//")
	}
	lower := strings.ToLower(u)
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(lower, p) {
			return len(u) > len(p) && !strings.HasPrefix(u[len(p):], "/")
		}
	}
	for _, p := range []string{"mailto:", "tel:"} {
		if strings.HasPrefix(lower, p) {
			return len(u) > len(p)
		}
	}
	return false
}

// Value returns a block setting in lang: the translation when there is
// one, else the main value, else the field's default.
func (b *Block) Value(key, lang string) string {
	if v := b.I18n[lang][key]; v != "" {
		return v
	}
	if v := b.Settings[key]; v != "" {
		return v
	}
	if t, ok := TypeOf(b.Type); ok {
		if f, ok := t.Field(key); ok {
			return f.Default
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
