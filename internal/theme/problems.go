package theme

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sap13ns/internal/archive"
)

// A Problem is one reason an uploaded theme is refused. Its message is
// Messages[Code] with Args, in English; the admin translates it.
type Problem struct {
	Code string
	File string // the file of the theme it is about; "" for the whole theme
	Line int    // 0 when not known
	Args []any
}

// Where is "file:line", "file" or "".
func (p Problem) Where() string {
	if p.File != "" && p.Line > 0 {
		return p.File + ":" + strconv.Itoa(p.Line)
	}
	return p.File
}

func (p Problem) Error() string {
	msg := fmt.Sprintf(Messages[p.Code], p.Args...)
	if w := p.Where(); w != "" {
		return w + ": " + msg
	}
	return msg
}

// A Refusal lists what is wrong with an uploaded theme. Nothing was
// installed.
type Refusal struct {
	Slug     string // "" when the archive did not name one
	Problems []Problem
}

func (r *Refusal) Error() string {
	var b strings.Builder
	b.WriteString("the theme was refused")
	for i, p := range r.Problems {
		if i == 5 {
			fmt.Fprintf(&b, "; and %d more", len(r.Problems)-i)
			break
		}
		b.WriteString("; " + p.Error())
	}
	return b.String()
}

// IsRefusal returns the refusal in err, if any.
func IsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	return r, errors.As(err, &r)
}

// MaxProblems bounds the problems one validation collects.
const MaxProblems = 50

// Messages are the English texts of the problem codes (fmt formats).
var Messages = map[string]string{
	// The archive.
	"archive.too-large":      "The archive is larger than %d MB.",
	"archive.invalid":        "This is not a valid .zip archive.",
	"archive.too-many":       "The archive holds more than %d files and folders.",
	"archive.unsafe-path":    "This path is absolute or climbs out of the theme's folder.",
	"archive.bad-name":       "This file name is not allowed: use letters, digits, dots, hyphens and underscores.",
	"archive.hidden":         "Hidden files are not allowed.",
	"archive.link":           "Symbolic links are not allowed.",
	"archive.special":        "Special files are not allowed.",
	"archive.duplicate":      "The archive holds this file twice.",
	"archive.file-too-large": "This file is larger than %d KB.",
	"archive.bomb":           "The archive unpacks to too much data: at most %d MB in all, and no file compressed more than %d to 1.",
	"archive.compression":    "This file is compressed with a method other than deflate or store.",
	"archive.no-manifest":    "theme.json is missing. Put the theme's files in a folder named after its slug, with theme.json inside, and zip that folder.",
	// The layout of the folder.
	"layout.not-allowed":   "This file does not belong in a theme. A theme holds theme.json, templates/, i18n/, static/, src/ and its README and LICENSE.",
	"layout.template-name": "Only the page templates (layout, front, home, single, page, archive, search, 404 and ask) go in templates/. Put other templates in templates/partials/.",
	"layout.missing":       "This file is required.",
	"layout.missing-front": "A theme needs a front page: templates/front.html (a landing or a one-page site) or templates/home.html (the latest posts).",
	"layout.too-many":      "A theme has at most %d templates.",
	"static.type":          "This type of file cannot be in static/. Allowed: %s.",
	"static.script":        "Scripts, pages and WebAssembly cannot be in a theme: the site's own JavaScript runs the toggles and menus.",
	"static.svg":           "SVG files are refused, because they can carry scripts. Use PNG, WebP or AVIF, an icon, or a data: image in the stylesheet.",
	"static.mismatch":      "The content of this file does not match its extension: it looks like %s.",
	"static.css":           "The stylesheet is refused: %s.",
	"src.type":             "Only .css files go in src/.",
	// theme.json.
	"manifest.json":           "theme.json is not valid: %s.",
	"manifest.slug":           "%q cannot name a theme: use 1 to 40 lower-case letters, digits and hyphens, starting with a letter or a digit.",
	"manifest.slug-mismatch":  "theme.json names the theme %q, but its folder is %q.",
	"manifest.no-slug":        "The theme has no name. Put its files in a folder named after its slug, or set \"slug\" in theme.json.",
	"manifest.builtin":        "%q is the name of a built-in theme. Choose another slug.",
	"manifest.network":        "%q is the name of a theme the network offers. Choose another slug.",
	"manifest.text":           "The field %s is longer than %d characters or holds control characters.",
	"manifest.required":       "The field %s is required.",
	"manifest.id":             "In %s, %q is not a valid id: use up to 40 lower-case letters, digits, hyphens and underscores.",
	"manifest.duplicate":      "In %s, %q is declared twice.",
	"manifest.too-many":       "The list %s has more than %d entries.",
	"manifest.option-type":    "The option %q has the type %q; use text, textarea, select or bool.",
	"manifest.option-choices": "The option %q needs between 1 and %d choices.",
	"manifest.option-default": "The default of the option %q is not one of its values.",
	"manifest.colour":         "%s is not a hexadecimal colour such as #1d4ed8.",
	"manifest.token":          "%q is not a colour token. Use the tokens of the customiser: %s.",
	"manifest.token-option":   "The colours of %q are for an option value the theme does not have.",
	// Translations.
	"i18n.lang":    "%q is not a language code such as es or pt-BR.",
	"i18n.invalid": "A translation file maps English strings to their translations, as a JSON object of strings: %s.",
	"format.width": "The format %q asks for a width or a precision over 99, or for one computed at run time.",
	// Templates.
	"template.encoding":       "Templates must be UTF-8 text.",
	"template.parse":          "The template does not parse: %s",
	"template.func":           "There is no template function %q.",
	"template.func-denied":    "Uploaded themes cannot use the function %q.",
	"template.format-literal": "%s needs its format as a string in quotes here.",
	"template.protected":      "A theme cannot redefine %q: the core renders it.",
	"template.missing":        "There is no template named %q.",
	"template.range":          "range can only go over a list of the page data, such as .Posts or .Site.Categories, not over %s.",
	"template.range-shared":   "%s, called here, would loop over %s: pass it the page data it expects.",
	"template.recursion":      "The template %q calls itself (%s). Templates cannot recurse.",
	"template.silent-loops":   "This loop (%s) can make passes that write nothing, and it holds another such loop. Nested loops must write something on every pass.",
	"template.silent-cost":    "This loop (%s) can make passes that write nothing, so one pass must stay cheap: at most %d steps, and no function that reads a whole text, such as contains or printf. Write something on every pass, or move the work out of the loop.",
	"template.complex":        "The templates pass their data through too many templates and dicts for the server to check them. Simplify them.",
	"template.too-big":        "The templates of the %s page expand to more than %d steps.",
	"template.field":          "%s has no field or method %q.",
	"template.name":           "%q cannot name a template: \"$\" is reserved for the copies html/template makes.",
	"template.secret":         "Uploaded themes cannot read %s: it holds a form token, such as the signed-in user's CSRF token.",
	"template.secret-print":   "%s would write out a whole value that holds a form token, such as the signed-in user's CSRF token. Write the fields you need instead, such as .Name.",
	"template.token":          "Templates and translations cannot write %s: the server replaces it with a form token or a plugin's form.",
	"template.member-denied":  "Uploaded themes cannot use %s: the shared templates render it.",
	"template.script":         "Templates cannot hold <script> elements: the site's own JavaScript is the only script on its pages.",
	"template.element":        "Templates cannot hold <%s> elements.",
	"template.form-attr":      "Templates cannot hold the %s attribute: it sends a form somewhere else.",
	"template.form-post":      "A theme's own forms can only use method=\"get\". The forms that change something are the core's shared templates.",
	"template.form-action":    "A theme's form can only send to a path of the site, written in the template: action=\"/search\" or action=\"{{link \"/search\"}}\", not to another site nor to an address an action makes.",
	"template.form-nested":    "{{template %q}} writes a form or form fields, and it is called here inside the theme's own <form> or <select>: the browser would add its fields, tokens included, to the theme's form. Close the theme's form or select before it.",
	"template.referrer":       "Templates cannot change the page's Referrer-Policy: no referrerpolicy attribute and no <meta name=\"referrer\">.",
	"template.meta-name":      "An action cannot write the name of a <meta> element: write it in the template.",
	"template.rel":            "A rel attribute must be written in the template, and without opener.",
	"template.svg":            "Inside an <svg>, templates can only hold drawing elements (svg, g, path, circle, ellipse, line, polyline, polygon, rect, defs, symbol, use, linearGradient, radialGradient, stop, clipPath, mask, pattern, text, tspan), not %s.",
	"template.render-limit":   "Even without page data, the %s page goes over the limits of uploaded themes: 8 MB of output, 1 MB for one text and 16 MB for all the text its functions make, in 5 seconds.",
	"template.tag-name":       "An action cannot write the name of an element: write it in the template.",
	"template.attr-name":      "An action cannot write the name of an attribute: write it in the template.",
	"template.context":        "{{template %q}} must be in the page's text, not inside an attribute, a comment, a <title> or a <textarea>: it holds the signed-in user's CSRF token.",
	"template.style":          "Templates cannot hold <style> elements or style attributes. Put the styles in static/theme.css.",
	"template.handler":        "Templates cannot hold event handler attributes such as %s.",
	"template.http-equiv":     "Templates cannot hold <meta http-equiv> elements.",
	"template.escape":         "The HTML around this action is ambiguous: %s",
	"template.credit":         "The layout does not always render the footer credit, {{template \"shared/footer-meta\" .}}, on these pages: %s. The CPAL-1.0 licence requires the \"Powered by sap13ns\" attribution on every page.",
	"template.credit-context": "{{template \"shared/footer-meta\" .}} must be in the page's text, not inside an attribute, a comment, a <title> or a <textarea>.",
	"template.credit-data":    "shared/footer-meta must be called with the page data: {{template \"shared/footer-meta\" .}} or $.",
}

// fromArchive turns a refused archive into a problem.
func fromArchive(err error, l archiveRules) (Problem, bool) {
	var ae *archive.Error
	if !errors.As(err, &ae) {
		return Problem{}, false
	}
	p := Problem{File: ae.Entry}
	switch ae.Rule {
	case "package.too-large":
		p.Code, p.Args = "archive.too-large", []any{l.limits.MaxBytes >> 20}
	case "package.too-many-entries":
		p.Code, p.Args = "archive.too-many", []any{l.limits.MaxEntries}
	case "package.zip-slip":
		p.Code = "archive.unsafe-path"
	case "package.hidden-file":
		p.Code = "archive.hidden"
	case "package.bad-name":
		p.Code = "archive.bad-name"
	case "package.symlink":
		p.Code = "archive.link"
	case "package.special-file":
		p.Code = "archive.special"
	case "package.duplicate":
		p.Code = "archive.duplicate"
	case "package.too-large-file":
		p.Code, p.Args = "archive.file-too-large", []any{l.limit(ae.Entry) >> 10}
	case "package.zip-bomb":
		p.Code, p.Args = "archive.bomb", []any{l.limits.MaxUnpacked >> 20, l.limits.MaxRatio}
	case "package.no-manifest":
		p.Code = "archive.no-manifest"
	case "package.invalid":
		p.Code = "archive.invalid"
		if strings.Contains(ae.Msg, "compression method") {
			p.Code = "archive.compression"
		}
	default:
		p.Code = "archive.invalid"
	}
	return p, true
}
