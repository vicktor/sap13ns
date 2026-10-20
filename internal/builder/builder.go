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

	pipeline "sap13ns/internal/content"
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
	ID string `json:"id"`
	// Anchor is the section's id on the page (#pricing), for menu links
	// and buttons that scroll to it: a slug, unique on the page.
	Anchor string `json:"anchor,omitempty"`
	// Class is the owner's CSS classes for the section (ValidClasses), for
	// the theme's Custom CSS to target.
	Class   string            `json:"class,omitempty"`
	Layout  string            `json:"layout"` // a Presets key
	Style   map[string]string `json:"style,omitempty"`
	Columns [][]Block         `json:"columns"`
}

// Block is one block. Settings hold the main language's values; I18n the
// translations of translatable fields (lang -> field -> text), like terms.
// Items are the entries of a block that repeats some (Type.Items).
type Block struct {
	ID       string                       `json:"id"`
	Type     string                       `json:"type"`
	Settings map[string]string            `json:"settings,omitempty"`
	I18n     map[string]map[string]string `json:"i18n,omitempty"`
	Items    []Item                       `json:"items,omitempty"`
}

// Item is one entry of a block's list (a feature, a question, a plan), with
// its settings and translations as a block has them.
type Item struct {
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
	// Media reports whether an image exists. An error means the id could
	// not be checked (a timeout, a database problem): the value is kept and
	// Parse, Load and Normalize return the error, so a passing failure never
	// strips images from a layout. Nil accepts every positive id.
	Media func(id int64) (bool, error)
	// Types are block types of the theme's own (its sections), besides
	// the built-in ones.
	Types []Type
}

// TypeOf returns the block type named t: a built-in one or one of c.Types.
func (c Context) TypeOf(t string) (*Type, bool) {
	if ty, ok := TypeOf(t); ok {
		return ty, true
	}
	for i := range c.Types {
		if c.Types[i].Type == t {
			return &c.Types[i], true
		}
	}
	return nil, false
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
	errs, err := l.normalize(c, true)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return l, nil
}

// Load decodes stored layouts leniently: what no longer validates (a
// deleted image, a menu the theme dropped) is reset or removed so the rest
// keeps working, and a page without its required block falls back to the
// theme's template. An error is a failed media lookup (see Context.Media):
// the layouts are still fine for one response, but must be neither cached
// nor saved.
func Load(data []byte, c Context) (*Layouts, error) {
	l := &Layouts{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, l)
	}
	_, err := l.normalize(c, false)
	return l, err
}

// Normalize cleans the layouts in place (strict: reports every problem).
// The error is the first failed media lookup.
func (l *Layouts) Normalize(c Context) (Errors, error) { return l.normalize(c, true) }

// ImageIDs lists the image ids that stored layouts refer to, so they can be
// looked up in one query before Load; extra are the theme's own block
// types (Context.Types).
func ImageIDs(data []byte, extra ...Type) []int64 {
	c := Context{Types: extra}
	l := &Layouts{}
	if len(data) == 0 || json.Unmarshal(data, l) != nil {
		return nil
	}
	var ids []int64
	seen := map[int64]bool{}
	add := func(v string) {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	regions := []*Region{l.Header, l.Footer}
	for _, r := range l.Pages {
		regions = append(regions, r)
	}
	for _, r := range regions {
		if r == nil {
			continue
		}
		for _, sec := range r.Sections {
			for _, col := range sec.Columns {
				for _, b := range col {
					t, ok := c.TypeOf(b.Type)
					if !ok {
						continue
					}
					for _, f := range t.Fields {
						if f.Type != "image" {
							continue
						}
						add(b.Settings[f.Key])
						for _, tr := range b.I18n {
							add(tr[f.Key])
						}
					}
					if t.Items == nil {
						continue
					}
					for _, f := range t.Items.Fields {
						if f.Type != "image" {
							continue
						}
						for _, it := range b.Items {
							add(it.Settings[f.Key])
							for _, tr := range it.I18n {
								add(tr[f.Key])
							}
						}
					}
				}
			}
		}
	}
	return ids
}

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	anchorRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	classRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// MaxClasses is how many CSS classes a section or a block may have.
const MaxClasses = 5

// ValidClasses normalises a CSS class field: up to MaxClasses names of 1 to
// 40 lower-case letters, digits and hyphens starting with a letter,
// separated by spaces. Repeated names are dropped. ok is false when v
// cannot be used.
func ValidClasses(v string) (classes string, ok bool) {
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Fields(v) {
		if !classRe.MatchString(c) {
			return "", false
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	if len(out) > MaxClasses {
		return "", false
	}
	return strings.Join(out, " "), true
}

const classMessage = "use up to 5 class names of lower-case letters, digits and hyphens, each starting with a letter"

// ValidAnchor reports whether a can be a section's anchor: 1 to 40
// lower-case letters, digits and hyphens starting with a letter, neither an
// id the core's own markup uses (main, comments…) nor with the sap- prefix
// of the sections without an anchor.
func ValidAnchor(a string) bool {
	return anchorRe.MatchString(a) && !strings.HasPrefix(a, "sap-") && !reservedAnchors[a]
}

func (l *Layouts) normalize(c Context, strict bool) (Errors, error) {
	var errs Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	var lookupErr error
	if media := c.Media; media != nil {
		c.Media = func(id int64) (bool, error) {
			ok, err := media(id)
			if err != nil {
				if lookupErr == nil {
					lookupErr = fmt.Errorf("check image %d: %w", id, err)
				}
				return true, nil // unknown: keep the value
			}
			return ok, nil
		}
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

	// Anchors are unique on a page: those of the header and the footer,
	// which show on every page, and those of one main region.
	shared := map[string]bool{}
	checkAnchors := func(path string, r *Region, taken map[string]bool) {
		if r == nil {
			return
		}
		for si := range r.Sections {
			s := &r.Sections[si]
			if s.Anchor == "" {
				continue
			}
			field := fmt.Sprintf("%s.sections[%d].anchor", path, si)
			switch {
			case !ValidAnchor(s.Anchor):
				add(field, "use 1–40 lower-case letters, digits and hyphens, starting with a letter (not main, comments or sap-…)")
				s.Anchor = ""
			case taken[s.Anchor]:
				add(field, "%q is already the anchor of another section on this page", s.Anchor)
				s.Anchor = ""
			default:
				taken[s.Anchor] = true
			}
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
			if cl, ok := ValidClasses(s.Class); !ok {
				add(sp+".class", classMessage)
				s.Class = ""
			} else {
				s.Class = cl
			}
			for ci := range s.Columns {
				col := s.Columns[ci]
				if len(col) > MaxBlocks {
					add(fmt.Sprintf("%s.columns[%d]", sp, ci), "at most %d blocks", MaxBlocks)
					col = col[:MaxBlocks]
				}
				out := col[:0]
				for bi, b := range col {
					bp := fmt.Sprintf("%s.columns[%d][%d]", sp, ci, bi)
					t, ok := c.TypeOf(b.Type)
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
		for si := range r.Sections {
			// The licence credit stays visible: its section takes no
			// classes that custom CSS could hide it with.
			if s := &r.Sections[si]; s.Class != "" && sectionHas(s, MetaType) {
				add(fmt.Sprintf("%s.sections[%d].class", path, si), "the section with the footer credits cannot have a CSS class")
				s.Class = ""
			}
		}
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
	checkAnchors("header", l.Header, shared)
	checkAnchors("footer", l.Footer, shared)
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
			taken := map[string]bool{}
			for a := range shared {
				taken[a] = true
			}
			checkAnchors("pages."+k, r, taken)
			pages[k] = r
		}
	}
	if len(pages) == 0 {
		pages = nil
	}
	l.Pages = pages
	sort.SliceStable(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs, lookupErr
}

func (r *Region) has(typ string) bool {
	for i := range r.Sections {
		if sectionHas(&r.Sections[i], typ) {
			return true
		}
	}
	return false
}

func sectionHas(s *Section, typ string) bool {
	for _, col := range s.Columns {
		for _, b := range col {
			if b.Type == typ {
				return true
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

// cleanBlock validates a block's settings and items against its type.
func cleanBlock(path string, b *Block, t *Type, c Context) Errors {
	var errs Errors
	b.Settings, b.I18n, errs = cleanValues(path, b.Settings, b.I18n, t.Field, t.Label, c)
	if t.Items == nil {
		if len(b.Items) > 0 {
			errs = append(errs, FieldError{Field: path + ".items", Message: fmt.Sprintf("%s has no items", t.Label)})
		}
		b.Items = nil
		return errs
	}
	if len(b.Items) > t.Items.Max {
		errs = append(errs, FieldError{Field: path + ".items", Message: fmt.Sprintf("at most %d items", t.Items.Max)})
		b.Items = b.Items[:t.Items.Max]
	}
	one := map[string]bool{} // the bools only one item may have on
	for i := range b.Items {
		it := &b.Items[i]
		ip := fmt.Sprintf("%s.items[%d]", path, i)
		var ierrs Errors
		it.Settings, it.I18n, ierrs = cleanValues(ip, it.Settings, it.I18n, t.Items.Field, t.Items.Label, c)
		errs = append(errs, ierrs...)
		for _, f := range t.Items.Fields {
			if !f.One || it.Settings[f.Key] != "true" {
				continue
			}
			if one[f.Key] {
				errs = append(errs, FieldError{Field: ip + ".settings." + f.Key, Message: "only one item can have this on"})
				delete(it.Settings, f.Key)
				if len(it.Settings) == 0 {
					it.Settings = nil
				}
			}
			one[f.Key] = true
		}
	}
	if len(b.Items) == 0 {
		b.Items = nil
	}
	return errs
}

// cleanValues validates the settings and the translations of a block or
// of an item against its fields; path is its place in the layouts, label
// its type's name for the messages.
func cleanValues(path string, settings map[string]string, i18n map[string]map[string]string, field func(string) (*Field, bool),
	label string, c Context) (map[string]string, map[string]map[string]string, Errors) {
	var errs Errors
	add := func(f, format string, args ...any) {
		errs = append(errs, FieldError{Field: path + "." + f, Message: fmt.Sprintf(format, args...)})
	}
	out := map[string]string{}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f, ok := field(k)
		if !ok {
			add("settings."+k, "%s has no setting %q", label, k)
			continue
		}
		v, msg := cleanValue(f, settings[k], c)
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

	tr := map[string]map[string]string{}
	langs := make([]string, 0, len(i18n))
	for lang := range i18n {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		if !langRe.MatchString(lang) || !contains(c.Langs, lang) {
			add("i18n."+lang, "this site has no such language")
			continue
		}
		m := map[string]string{}
		ks := make([]string, 0, len(i18n[lang]))
		for k := range i18n[lang] {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			f, ok := field(k)
			if !ok || !f.I18n {
				add("i18n."+lang+"."+k, "this setting is not translatable")
				continue
			}
			v, msg := cleanValue(f, i18n[lang][k], c)
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
	return out, tr, errs
}

// imageOK reports whether image id exists; one that could not be checked
// counts as existing (normalize records the error).
func imageOK(c Context, id int64) bool {
	ok, err := c.Media(id)
	return ok || err != nil
}

// cleanValue normalises one value and returns a message when it is refused.
func cleanValue(f *Field, v string, c Context) (string, string) {
	if f.Type != "markdown" && f.Type != "textarea" && f.Type != "html" {
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
	case "html":
		// Sanitised on save (and again on render): what is stored is what
		// a page can show.
		if f.Max > 0 && utf8.RuneCountInString(v) > f.Max {
			return "", fmt.Sprintf("at most %d characters", f.Max)
		}
		v = pipeline.SanitizeHTMLBlock(v)
	case "class":
		cl, ok := ValidClasses(v)
		if !ok {
			return "", classMessage
		}
		v = cl
	case "select", "icon":
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
		if err != nil || id <= 0 || (c.Media != nil && !imageOK(c, id)) {
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

// Value returns an item's setting in lang, as Block.Value does a block's.
func (it *Item) Value(f *Field, lang string) string {
	if v := it.I18n[lang][f.Key]; v != "" {
		return v
	}
	if v := it.Settings[f.Key]; v != "" {
		return v
	}
	return f.Default
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
