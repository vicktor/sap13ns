// Package customize holds the visual customiser's model: a per-theme
// design (colours, fonts, scales, header, footer, home layout, custom CSS),
// its validation, the stylesheet generated from it and the WCAG contrast
// checks shown while editing.
//
// The generated stylesheet only sets --sap-* input tokens on :root. Each
// theme maps those inputs onto its own variables with its original values
// as fallbacks (var(--sap-light-background, #fff)), so an empty design
// changes nothing and themes keep working without it.
package customize

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Version of the design JSON format.
const Version = 1

// ColorTokens are the colours a design can set, separately for the light
// and the dark scheme. Every theme maps each one onto its own variables.
var ColorTokens = []string{
	"background", "foreground",
	"card", "card-foreground",
	"muted", "muted-foreground",
	"border",
	"primary", "primary-foreground",
	"accent", "accent-foreground",
}

// Scale choices: the keys are what the JSON stores, the values what the
// stylesheet emits. An empty key means "the theme's own value".
var (
	TextScales    = map[string]string{"90": "0.9", "95": "0.95", "105": "1.05", "110": "1.1", "120": "1.2"}
	RadiusScales  = map[string]string{"none": "0", "small": "0.5", "large": "1.5", "xlarge": "2"}
	DensityScales = map[string]string{"compact": "0.75", "snug": "0.875", "relaxed": "1.25", "spacious": "1.5"}
	ShadowScales  = map[string]string{"none": "0", "soft": "0.5", "strong": "1.6"}
	Widths        = map[string]string{"narrow": "60rem", "medium": "68rem", "wide": "84rem", "xwide": "96rem"}
)

// Choice lists for the enumerated settings ("" is always allowed).
var (
	BrandModes   = []string{"logo", "logo-title"}
	BrandAligns  = []string{"center"}
	StickyModes  = []string{"on", "off"}
	HeroModes    = []string{"on", "off"}
	HomeLayouts  = []string{"list", "grid", "bento"}
	FontStacks   = []string{"system", "humanist", "geometric", "serif", "mono", "media"}
	MonoStacks   = []string{"mono", "media"}
	HeadingExtra = []string{"body"} // heading only: same as the body font
)

// Limits.
const (
	MaxCSS        = 64 << 10 // custom CSS bytes
	MaxFooterText = 500      // characters, per language
	MaxPerPage    = 50
	MaxFooterCols = 4
	MaxJSON       = 256 << 10 // whole design JSON accepted by the API
)

// Design is one theme's customisation. The zero value changes nothing.
type Design struct {
	Version int               `json:"version"`
	Light   map[string]string `json:"light,omitempty"` // token -> #rrggbb
	Dark    map[string]string `json:"dark,omitempty"`
	Fonts   Fonts             `json:"fonts"`
	Scale   Scale             `json:"scale"`
	Header  Header            `json:"header"`
	Footer  Footer            `json:"footer"`
	Home    Home              `json:"home"`
	CSS     string            `json:"css,omitempty"`
	// Options overrides theme.json options while drafting; publishing
	// writes them to the theme's stored options.
	Options map[string]string `json:"options,omitempty"`
}

// Font picks a curated stack or a WOFF2 file from the media library.
type Font struct {
	Stack string `json:"stack,omitempty"`
	Media int64  `json:"media,omitempty"` // with Stack "media"
}

type Fonts struct {
	Body    Font `json:"body"`
	Heading Font `json:"heading"`
	Mono    Font `json:"mono"`
}

type Scale struct {
	Text    string `json:"text,omitempty"`
	Radius  string `json:"radius,omitempty"`
	Density string `json:"density,omitempty"`
	Shadow  string `json:"shadow,omitempty"`
	Width   string `json:"width,omitempty"`
}

type Header struct {
	Brand  string `json:"brand,omitempty"` // "" (title) | logo | logo-title
	Logo   int64  `json:"logo,omitempty"`  // media id of an image
	Align  string `json:"align,omitempty"` // "" | center
	Sticky string `json:"sticky,omitempty"`
	// HideSearch and HideLang remove the search link and language switch.
	HideSearch bool `json:"hide_search,omitempty"`
	HideLang   bool `json:"hide_lang,omitempty"`
}

type Footer struct {
	Columns int `json:"columns,omitempty"` // widget columns, 0 = theme
	// Text replaces the footer line; TextI18n holds per-language texts like
	// terms do (lang -> text). Plain text, escaped on output.
	Text     string            `json:"text,omitempty"`
	TextI18n map[string]string `json:"text_i18n,omitempty"`
}

// TextIn returns the footer text for lang ("" when not customised).
func (f Footer) TextIn(lang string) string {
	if t := f.TextI18n[lang]; t != "" {
		return t
	}
	return f.Text
}

type Home struct {
	Hero    string `json:"hero,omitempty"`   // "" | on | off
	Layout  string `json:"layout,omitempty"` // "" | list | grid | bento
	PerPage int    `json:"per_page,omitempty"`
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

// OptionSpec describes one theme.json option for validation.
type OptionSpec struct {
	Key     string
	Type    string // text | textarea | select | bool
	Choices []string
}

// Context is what validation needs to know about the site and theme.
type Context struct {
	Options []OptionSpec
	Langs   []string // languages that may have their own footer text
	// Media reports whether a media id exists and is of the wanted kind
	// ("image" or "font"). An error means the id could not be checked (a
	// timeout, a database problem): the value is kept and Parse, Load and
	// Normalize return the error, so a passing failure never strips a font
	// or the logo from a design. Nil accepts every positive id.
	Media func(id int64, kind string) (bool, error)
}

// Parse decodes and validates a design. Unknown fields are rejected so a
// typo in an imported file does not silently do nothing.
func Parse(data []byte, c Context) (*Design, error) {
	if len(data) > MaxJSON {
		return nil, Errors{{Field: "", Message: "the design is too large"}}
	}
	d := &Design{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return d, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(d); err != nil {
		return nil, Errors{{Field: "", Message: "invalid design JSON: " + err.Error()}}
	}
	errs, err := d.Normalize(c)
	if err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return d, nil
}

// Load decodes a stored design leniently: unknown fields are ignored and
// values that no longer validate (a deleted logo, an option the theme
// dropped) are reset, so the rest of the design keeps working. An error is
// a failed media lookup (see Context.Media): the design is still fine for
// one response, but must be neither cached nor saved.
func Load(data []byte, c Context) (*Design, error) {
	d := &Design{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, d)
	}
	_, err := d.Normalize(c)
	return d, err
}

// Normalize cleans every value in place (lower-case hex, trimmed text,
// empty maps dropped), resets what cannot be accepted and reports it. The
// error is the first failed media lookup.
func (d *Design) Normalize(c Context) (Errors, error) {
	var errs Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
	}
	var lookupErr error
	exists := func(id int64, kind string) bool {
		if c.Media == nil {
			return true
		}
		ok, err := c.Media(id, kind)
		if err != nil {
			if lookupErr == nil {
				lookupErr = fmt.Errorf("check media %d: %w", id, err)
			}
			return true // unknown: keep the value
		}
		return ok
	}
	if d.Version != 0 && d.Version != Version {
		add("version", "unsupported version %d", d.Version)
	}
	d.Version = Version

	for _, scheme := range []struct {
		name string
		m    *map[string]string
	}{{"light", &d.Light}, {"dark", &d.Dark}} {
		out := map[string]string{}
		for k, v := range *scheme.m {
			if !contains(ColorTokens, k) {
				add(scheme.name+"."+k, "unknown colour")
				continue
			}
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			hex, ok := NormalizeHex(v)
			if !ok {
				add(scheme.name+"."+k, "use a hex colour such as #1d4ed8")
				continue
			}
			out[k] = hex
		}
		if len(out) == 0 {
			out = nil
		}
		*scheme.m = out
	}

	checkFont := func(field string, f *Font, stacks []string) {
		f.Stack = strings.TrimSpace(f.Stack)
		if f.Stack == "" {
			f.Media = 0
			return
		}
		if !contains(stacks, f.Stack) {
			add(field, "unknown font %q", f.Stack)
			*f = Font{}
			return
		}
		if f.Stack != "media" {
			f.Media = 0
			return
		}
		if f.Media <= 0 || !exists(f.Media, "font") {
			add(field, "choose a WOFF2 font from the media library")
			*f = Font{}
		}
	}
	checkFont("fonts.body", &d.Fonts.Body, FontStacks)
	checkFont("fonts.heading", &d.Fonts.Heading, append(append([]string{}, FontStacks...), HeadingExtra...))
	checkFont("fonts.mono", &d.Fonts.Mono, MonoStacks)

	checkKey := func(field string, v *string, m map[string]string) {
		*v = strings.TrimSpace(*v)
		if *v != "" {
			if _, ok := m[*v]; !ok {
				add(field, "unknown value %q", *v)
				*v = ""
			}
		}
	}
	checkKey("scale.text", &d.Scale.Text, TextScales)
	checkKey("scale.radius", &d.Scale.Radius, RadiusScales)
	checkKey("scale.density", &d.Scale.Density, DensityScales)
	checkKey("scale.shadow", &d.Scale.Shadow, ShadowScales)
	checkKey("scale.width", &d.Scale.Width, Widths)

	checkChoice := func(field string, v *string, choices []string) {
		*v = strings.TrimSpace(*v)
		if *v != "" && !contains(choices, *v) {
			add(field, "unknown value %q", *v)
			*v = ""
		}
	}
	checkChoice("header.brand", &d.Header.Brand, BrandModes)
	checkChoice("header.align", &d.Header.Align, BrandAligns)
	checkChoice("header.sticky", &d.Header.Sticky, StickyModes)
	if d.Header.Brand == "" {
		d.Header.Logo = 0
	} else if d.Header.Logo <= 0 || !exists(d.Header.Logo, "image") {
		add("header.logo", "choose an image from the media library")
		d.Header.Brand, d.Header.Logo = "", 0
	}

	if d.Footer.Columns < 0 || d.Footer.Columns > MaxFooterCols {
		add("footer.columns", "between 1 and %d, or 0 for the theme's layout", MaxFooterCols)
		d.Footer.Columns = 0
	}
	d.Footer.Text = cleanText(d.Footer.Text)
	if utf8.RuneCountInString(d.Footer.Text) > MaxFooterText {
		add("footer.text", "at most %d characters", MaxFooterText)
		d.Footer.Text = clip(d.Footer.Text, MaxFooterText)
	}
	texts := map[string]string{}
	for lang, t := range d.Footer.TextI18n {
		t = cleanText(t)
		if t == "" {
			continue
		}
		if !contains(c.Langs, lang) {
			add("footer.text_i18n."+lang, "unknown language")
			continue
		}
		if utf8.RuneCountInString(t) > MaxFooterText {
			add("footer.text_i18n."+lang, "at most %d characters", MaxFooterText)
			t = clip(t, MaxFooterText)
		}
		texts[lang] = t
	}
	if len(texts) == 0 {
		texts = nil
	}
	d.Footer.TextI18n = texts

	checkChoice("home.hero", &d.Home.Hero, HeroModes)
	checkChoice("home.layout", &d.Home.Layout, HomeLayouts)
	if d.Home.PerPage < 0 || d.Home.PerPage > MaxPerPage {
		add("home.per_page", "between 1 and %d, or 0 for the reading settings", MaxPerPage)
		d.Home.PerPage = 0
	}

	if css, err := SanitizeCSS(d.CSS); err != nil {
		add("css", "%s", err.Error())
		d.CSS = ""
	} else {
		d.CSS = css
	}

	opts := map[string]string{}
	for k, v := range d.Options {
		var spec *OptionSpec
		for i := range c.Options {
			if c.Options[i].Key == k {
				spec = &c.Options[i]
			}
		}
		if spec == nil {
			add("options."+k, "this theme has no such option")
			continue
		}
		v = strings.TrimSpace(v)
		switch spec.Type {
		case "bool":
			if v != "true" && v != "false" {
				add("options."+k, "use true or false")
				continue
			}
		case "select":
			if !contains(spec.Choices, v) {
				add("options."+k, "unknown value %q", v)
				continue
			}
		default:
			if utf8.RuneCountInString(v) > 2000 {
				add("options."+k, "at most 2000 characters")
				continue
			}
		}
		opts[k] = v
	}
	if len(opts) == 0 {
		opts = nil
	}
	d.Options = opts
	sort.Sort(errs)
	return errs, lookupErr
}

func (es Errors) Len() int           { return len(es) }
func (es Errors) Less(i, j int) bool { return es[i].Field < es[j].Field }
func (es Errors) Swap(i, j int)      { es[i], es[j] = es[j], es[i] }

// IsZero reports whether the design changes nothing.
func (d *Design) IsZero() bool {
	z := Design{Version: d.Version}
	a, _ := json.Marshal(d)
	b, _ := json.Marshal(z)
	return string(a) == string(b)
}

// JSON encodes the design (normalised designs encode deterministically:
// encoding/json sorts map keys).
func (d *Design) JSON() []byte {
	b, _ := json.Marshal(d)
	return b
}

// HTMLClasses returns the classes the layout puts on <html> for the
// structural header options; themes style them in their hooks section.
func (d *Design) HTMLClasses() string {
	var cl []string
	if d.Header.Sticky != "" {
		cl = append(cl, "sap-sticky-"+d.Header.Sticky)
	}
	if d.Header.Align == "center" {
		cl = append(cl, "sap-brand-center")
	}
	if d.Header.Brand != "" {
		cl = append(cl, "sap-has-logo")
	}
	return strings.Join(cl, " ")
}

func cleanText(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// clip cuts s to n characters.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return strings.TrimSpace(string(r))
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
