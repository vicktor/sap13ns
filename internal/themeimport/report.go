package themeimport

import (
	"fmt"
	"sort"
	"strings"
)

// A Note is one thing the report says about a conversion: what was left
// out, approximated or changed. Its message is Messages[Code] with Args,
// in English; the admin translates it.
type Note struct {
	Code  string
	File  string // the file of the source theme it is about; "" for the whole theme
	Args  []any
	Count int // how many times it happened (the same code, file and arguments)
}

func (n Note) String() string {
	msg := fmt.Sprintf(Messages[n.Code], n.Args...)
	if n.Count > 1 {
		msg += fmt.Sprintf(" (×%d)", n.Count)
	}
	if n.File != "" {
		return n.File + ": " + msg
	}
	return msg
}

// Messages are the English texts of the notes. internal/web translates
// them with s.t(themeimport.Messages[code], args...); scripts/i18ncheck
// reads this table.
var Messages = map[string]string{
	"block.unknown":         "The block %s is not supported; its saved HTML was kept.",
	"block.dropped":         "The block %s is not supported and has no saved HTML; it was left out.",
	"block.bad-json":        "The attributes of a %s block are not valid JSON; the block was converted without them.",
	"block.too-many":        "The file has too many blocks or nests them too deeply; it was left out.",
	"block.query-custom":    "A query block shows posts the theme picks (%s); it shows the page's posts instead.",
	"block.dynamic-content": "The block %s shows content of the site that sap13ns does not have; it was left out.",
	"block.form":            "The block %s needs a form; the site's own form is used instead.",
	"block.comments-inner":  "The comment blocks were replaced by the site's own comments.",
	"css.dropped":           "%d rules of the theme's CSS were left out (they load other files, or are not allowed in a theme).",
	"css.url-dropped":       "A file the CSS names is not in the theme or not allowed: %s.",
	"file.unreadable":       "The file is too large or cannot be read; it was left out.",
	"font.dropped":          "The font file %s was left out (only .woff2 and .woff fonts in the theme are kept).",
	"font.unused":           "%d font families of the theme are not used by its styles and were left out.",
	"font.remote":           "The font %s is loaded from another server; it was left out (fonts must be in the theme).",
	"html.dropped":          "The element <%s> was left out.",
	"html.image-dropped":    "The image %s is not a file of the theme and was left out.",
	"image.unsupported":     "The image %s is not a PNG, JPEG, WebP, AVIF or GIF picture, or is too big; it was left out.",
	"image.svg":             "The SVG image %s was kept as an inline data image.",
	"layout.parts":          "The templates use different headers or footers; the layout picks one for each kind of page.",
	"menu.location":         "The navigation block becomes the menu location “%s”.",
	"pattern.missing":       "The pattern %s is not in the theme; it was left out.",
	"pattern.php":           "%d pieces of PHP in the pattern could not be read without running them; they were left out.",
	"pattern.invalid":       "The pattern is not UTF-8 text; it was left out.",
	"pattern.cycle":         "The pattern %s includes itself; the inner copy was left out.",
	"part.missing":          "The template part %s is not in the theme; it was left out.",
	"template.custom":       "The custom template %s has no counterpart in sap13ns; it was left out.",
	"template.empty":        "Nothing of the front page template could be converted; the front page uses the home template.",
	"template.missing":      "The theme has no %s template; the site's default is used.",
	"template.file":         "The file %s is not a template, style, font, image or text file; it was left out.",
	"theme.php":             "The theme's PHP files (%d) were not run; only the block templates, patterns and styles were converted.",
	"theme.block-styles":    "The CSS of %d block styles the theme registers in PHP was read as text and kept.",
	"theme.variations":      "%d style variations become choices of the “Colour style” option.",
	"theme.dark":            "The dark colours were derived from the light ones.",
	"theme.dark-own":        "The theme's own colours are dark; the light colours were derived from them.",
	"theme.parent":          "The theme is a child of %s, which is not included; only the child's own files were converted.",
	"theme.woocommerce":     "The templates and blocks of WooCommerce were left out.",
}

// A Report says what a conversion made and what it left out.
type Report struct {
	Source      string // "wordpress-block"
	Name        string
	Slug        string
	Version     string
	Author      string
	License     string
	Pages       []string       // the sap13ns pages written (home, single, …)
	Blocks      map[string]int // blocks converted, by name
	Images      int
	Fonts       int
	Variations  int
	OwnDark     bool
	Notes       []Note
	notesByCode map[string]int
}

const maxNotes = 200

func (r *Report) add(n Note) {
	key := n.Code + "\x00" + n.File + "\x00" + fmt.Sprint(n.Args...)
	if r.notesByCode == nil {
		r.notesByCode = map[string]int{}
	}
	if i, ok := r.notesByCode[key]; ok {
		r.Notes[i].Count++
		return
	}
	if len(r.Notes) >= maxNotes {
		return
	}
	n.Count = 1
	r.notesByCode[key] = len(r.Notes)
	r.Notes = append(r.Notes, n)
}

// Summary is the report as text, for the command line.
func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", r.Name, r.Version)
	if r.Author != "" {
		fmt.Fprintf(&b, " by %s", r.Author)
	}
	if r.License != "" {
		fmt.Fprintf(&b, " (%s)", r.License)
	}
	fmt.Fprintf(&b, "\nslug: %s\npages: %s\n", r.Slug, strings.Join(r.Pages, ", "))
	names := make([]string, 0, len(r.Blocks))
	total := 0
	for n, c := range r.Blocks {
		names = append(names, n)
		total += c
	}
	sort.Slice(names, func(i, j int) bool {
		if r.Blocks[names[i]] != r.Blocks[names[j]] {
			return r.Blocks[names[i]] > r.Blocks[names[j]]
		}
		return names[i] < names[j]
	})
	fmt.Fprintf(&b, "blocks: %d (%d kinds), images: %d, fonts: %d, colour styles: %d\n", total, len(names), r.Images, r.Fonts, r.Variations)
	for _, n := range r.Notes {
		b.WriteString("- " + n.String() + "\n")
	}
	return b.String()
}
