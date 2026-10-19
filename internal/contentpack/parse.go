// Package contentpack loads a directory of Markdown files into a site: pages
// and posts in several languages, linked as translations, with their
// categories, tags and menu entries, plus the site settings.
//
// Layout of a pack:
//
//	<dir>/site.json        site settings and extra menu links (see Site)
//	<dir>/<lang>/*.md      one page or post per file, e.g. en/features.md
//
// Each Markdown file starts with front matter between two "---" lines:
//
//	---
//	type: page            # page | post
//	key: features         # same key in every language = translations
//	title: Features
//	slug: features
//	excerpt: One-line summary.
//	order: 2              # menu_order, and the order of menu entries
//	menu: [primary, landing]
//	menu_label: Features
//	menu_label_landing: All features
//	date: 2026-10-18
//	categories: [Releases]
//	tags: [go, security]
//	comments: closed
//	---
//	Markdown body.
//
// The front matter is a deliberately small "key: value" format, not YAML:
// one key per line, values optionally quoted, lists written as [a, b, "c, d"]
// and "#" starting a comment when it follows whitespace outside quotes.
package contentpack

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Item is one page or post of a pack, in one language.
type Item struct {
	File string // path of the source file
	Lang string // from the language directory, or the "lang" key

	Type            string // "page" | "post"
	Key             string // shared by the translations of the same item
	Title           string
	Slug            string
	Excerpt         string
	MetaDescription string
	Order           int
	Menus           []string          // menu locations, e.g. primary, footer
	MenuLabel       string            // default label in every menu (empty = title)
	MenuLabels      map[string]string // per-location overrides (menu_label_<location>)
	Date            string            // raw "date" value; see ParseDate
	DateLine        int               // line of the "date" key, for error messages
	Categories      []string          // display names
	Tags            []string          // display names
	Comments        string            // "open" | "closed"
	Status          string            // "published" | "draft"
	Body            string            // Markdown

	// Extra holds keys this package does not know, with their raw values.
	Extra map[string]string
}

// Label returns the text of the item's entry in a menu location.
func (it *Item) Label(location string) string {
	if l := it.MenuLabels[location]; l != "" {
		return l
	}
	if it.MenuLabel != "" {
		return it.MenuLabel
	}
	return it.Title
}

// Path returns the public path of the item; extra languages are prefixed
// with /<lang>. Pages live at /<slug>, posts at /blog/<slug>.
func (it *Item) Path(mainLang string) string {
	p := "/" + it.Slug
	if it.Type == "post" {
		p = "/blog/" + it.Slug
	}
	if it.Lang != mainLang {
		p = "/" + it.Lang + p
	}
	return p
}

var (
	langRe     = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)
	slugRe     = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	keyNameRe  = regexp.MustCompile(`^[a-z0-9_]+$`)
	itemKeyRe  = regexp.MustCompile(`^[a-z0-9]+([_-][a-z0-9]+)*$`)
	locationRe = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
)

// Parse reads every <dir>/<lang>/*.md file, in a deterministic order (language
// directories and file names sorted). Hidden files (".name", such as the
// "._name" metadata macOS adds to archives) are skipped. Every problem found
// is reported, each prefixed with the file and line.
func Parse(dir string) ([]Item, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var items []Item
	var errs []error
	for _, e := range entries { // os.ReadDir sorts by name
		if !e.IsDir() || !langRe.MatchString(e.Name()) {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, e.Name(), "*.md"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			if strings.HasPrefix(filepath.Base(f), ".") {
				continue
			}
			it, err := ParseFile(f, e.Name())
			if err != nil {
				errs = append(errs, err)
				continue
			}
			items = append(items, it)
		}
	}
	errs = append(errs, checkDuplicates(items)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: no <lang>/*.md files found", dir)
	}
	return items, nil
}

func checkDuplicates(items []Item) []error {
	var errs []error
	slugs, keys := map[string]string{}, map[string]string{}
	for _, it := range items {
		s := it.Type + "\x00" + it.Lang + "\x00" + it.Slug
		if prev, ok := slugs[s]; ok {
			errs = append(errs, fmt.Errorf("%s: %s slug %q in %q is already used by %s", it.File, it.Type, it.Slug, it.Lang, prev))
		} else {
			slugs[s] = it.File
		}
		k := it.Type + "\x00" + it.Lang + "\x00" + it.Key
		if prev, ok := keys[k]; ok {
			errs = append(errs, fmt.Errorf("%s: %s key %q in %q is already used by %s", it.File, it.Type, it.Key, it.Lang, prev))
		} else {
			keys[k] = it.File
		}
	}
	return errs
}

// ParseFile parses one Markdown file; lang is the default language (the name
// of its directory).
func ParseFile(path, lang string) (Item, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Item{}, err
	}
	return ParseBytes(path, data, lang)
}

// value is one front matter entry.
type value struct {
	line   int
	raw    string   // after comment removal, before unquoting
	scalar string   // unquoted scalar (when !isList)
	list   []string // items (when isList)
	isList bool
}

// ParseBytes parses a file's contents; name is used in error messages.
func ParseBytes(name string, data []byte, lang string) (Item, error) {
	text := strings.ReplaceAll(string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return Item{}, fmt.Errorf("%s:1: the file must start with a --- line opening the front matter", name)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Item{}, fmt.Errorf("%s:1: the front matter is not closed with a --- line", name)
	}

	vals := map[string]*value{}
	var errs []error
	fail := func(line int, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s:%d: %s", name, line, fmt.Sprintf(format, args...)))
	}
	for i := 1; i < end; i++ {
		n := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, rest, ok := strings.Cut(line, ":")
		k = strings.TrimSpace(k)
		if !ok || !keyNameRe.MatchString(k) {
			fail(n, "expected \"key: value\" with a lowercase key, got %q", line)
			continue
		}
		if _, dup := vals[k]; dup {
			fail(n, "duplicate key %q", k)
			continue
		}
		v, err := parseValue(rest)
		if err != nil {
			fail(n, "%s: %v", k, err)
			continue
		}
		v.line = n
		vals[k] = v
	}

	it := Item{File: name, Lang: lang, Comments: "closed", Status: "published", MenuLabels: map[string]string{}, Extra: map[string]string{}}
	scalar := func(k string, dst *string) {
		v, ok := vals[k]
		if !ok {
			return
		}
		if v.isList {
			fail(v.line, "%s must be a single value, not a list", k)
			return
		}
		*dst = v.scalar
	}
	list := func(k string) []string {
		v, ok := vals[k]
		if !ok {
			return nil
		}
		if !v.isList {
			if v.scalar == "" {
				return nil
			}
			return []string{v.scalar}
		}
		return v.list
	}
	line := func(k string) int {
		if v, ok := vals[k]; ok {
			return v.line
		}
		return 1
	}

	scalar("type", &it.Type)
	scalar("key", &it.Key)
	scalar("title", &it.Title)
	scalar("slug", &it.Slug)
	scalar("lang", &it.Lang)
	scalar("excerpt", &it.Excerpt)
	scalar("meta_description", &it.MetaDescription)
	scalar("menu_label", &it.MenuLabel)
	scalar("date", &it.Date)
	scalar("comments", &it.Comments)
	scalar("status", &it.Status)
	it.DateLine = line("date")
	var order string
	scalar("order", &order)
	if order != "" {
		n, err := strconv.Atoi(order)
		if err != nil {
			fail(line("order"), "order must be an integer, got %q", order)
		}
		it.Order = n
	}
	it.Menus = list("menu")
	it.Categories = list("categories")
	it.Tags = list("tags")

	known := map[string]bool{"type": true, "key": true, "title": true, "slug": true, "lang": true, "excerpt": true,
		"meta_description": true, "menu_label": true, "date": true, "comments": true, "status": true, "order": true,
		"menu": true, "categories": true, "tags": true}
	for k, v := range vals {
		if known[k] {
			continue
		}
		if loc, ok := strings.CutPrefix(k, "menu_label_"); ok && loc != "" {
			var s string
			scalar(k, &s)
			it.MenuLabels[strings.ReplaceAll(loc, "_", "-")] = s
			if loc != strings.ReplaceAll(loc, "_", "-") {
				// menu_label_<loc> keys cannot contain "-"; accept "_" for both.
				it.MenuLabels[loc] = s
			}
			continue
		}
		it.Extra[k] = v.raw
	}

	// Validation.
	if it.Type == "" {
		fail(1, "type is required (page or post)")
	} else if it.Type != "page" && it.Type != "post" {
		fail(line("type"), "type must be page or post, got %q", it.Type)
	}
	if it.Key == "" {
		fail(1, "key is required")
	} else if !itemKeyRe.MatchString(it.Key) {
		fail(line("key"), "key must be lowercase letters, digits, - or _, got %q", it.Key)
	}
	if strings.TrimSpace(it.Title) == "" {
		fail(1, "title is required")
	} else if len([]rune(it.Title)) > 300 {
		fail(line("title"), "title is longer than 300 characters")
	}
	if it.Slug == "" {
		fail(1, "slug is required")
	} else if !slugRe.MatchString(it.Slug) || len(it.Slug) > 200 {
		fail(line("slug"), "slug must be lowercase words joined by single hyphens (a-z, 0-9), got %q", it.Slug)
	}
	if !langRe.MatchString(it.Lang) {
		fail(line("lang"), "invalid language code %q", it.Lang)
	}
	if len([]rune(it.Excerpt)) > 2000 {
		fail(line("excerpt"), "excerpt is longer than 2000 characters")
	}
	if len([]rune(it.MetaDescription)) > 300 {
		fail(line("meta_description"), "meta_description is longer than 300 characters")
	}
	if it.Comments != "open" && it.Comments != "closed" {
		fail(line("comments"), "comments must be open or closed, got %q", it.Comments)
	}
	if it.Status != "published" && it.Status != "draft" {
		fail(line("status"), "status must be published or draft, got %q", it.Status)
	}
	if it.Date != "" {
		if _, err := ParseDate(it.Date, time.UTC); err != nil {
			fail(it.DateLine, "%v", err)
		}
	}
	for _, m := range it.Menus {
		if !locationRe.MatchString(m) {
			fail(line("menu"), "invalid menu location %q", m)
		}
	}
	for _, m := range it.Menus {
		if l := it.Label(m); len([]rune(l)) > 100 {
			fail(line("menu"), "menu label %q is longer than 100 characters", l)
		}
	}
	for _, c := range append(append([]string{}, it.Categories...), it.Tags...) {
		if c == "" || len([]rune(c)) > 100 {
			fail(1, "category and tag names must be 1 to 100 characters, got %q", c)
		} else if Slugify(c) == "" {
			fail(1, "the term %q has no letters or digits to build a slug from", c)
		}
	}

	if len(errs) > 0 {
		return Item{}, errors.Join(errs...)
	}

	body := strings.Join(lines[end+1:], "\n")
	body = strings.TrimPrefix(body, "\n")
	it.Body = strings.TrimRight(body, " \t\n")
	return it, nil
}

// parseValue parses the part after "key:".
func parseValue(s string) (*value, error) {
	s = stripComment(strings.TrimSpace(s))
	v := &value{raw: s}
	if strings.HasPrefix(s, "[") {
		if !strings.HasSuffix(s, "]") {
			return nil, errors.New("a list must end with ]")
		}
		v.isList = true
		parts, err := splitList(s[1 : len(s)-1])
		if err != nil {
			return nil, err
		}
		v.list = parts
		return v, nil
	}
	u, err := unquote(s)
	if err != nil {
		return nil, err
	}
	v.scalar = u
	return v, nil
}

// stripComment removes a trailing "# comment" that follows whitespace and is
// not inside quotes, then trims the result.
func stripComment(s string) string {
	var quote rune
	escaped := false
	prevSpace := true // a value that starts with # is a comment
	for i, r := range s {
		switch {
		case quote != 0:
			switch {
			case escaped:
				escaped = false
			case r == '\\' && quote == '"':
				escaped = true
			case r == quote:
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && prevSpace:
			return strings.TrimSpace(s[:i])
		}
		prevSpace = r == ' ' || r == '\t'
	}
	return strings.TrimSpace(s)
}

// splitList splits the inside of [...] on commas outside quotes.
func splitList(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	var quote rune
	escaped := false
	flush := func() error {
		item := strings.TrimSpace(cur.String())
		cur.Reset()
		if item == "" {
			return nil
		}
		u, err := unquote(item)
		if err != nil {
			return err
		}
		if u != "" {
			out = append(out, u)
		}
		return nil
	}
	for _, r := range s {
		if quote != 0 {
			cur.WriteRune(r)
			switch {
			case escaped:
				escaped = false
			case r == '\\' && quote == '"':
				escaped = true
			case r == quote:
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
			cur.WriteRune(r)
		case ',':
			if err := flush(); err != nil {
				return nil, err
			}
		case '[', ']':
			return nil, errors.New("nested lists are not supported")
		default:
			cur.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in list")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// unquote strips matching surrounding quotes. Double-quoted strings accept
// \" and \\ escapes; single-quoted ones accept a doubled single quote.
func unquote(s string) (string, error) {
	if len(s) == 0 {
		return s, nil
	}
	q := s[0]
	if q != '"' && q != '\'' {
		return s, nil
	}
	if len(s) < 2 || s[len(s)-1] != q {
		return "", fmt.Errorf("unterminated quote in %s", s)
	}
	inner := s[1 : len(s)-1]
	if q == '\'' {
		return strings.ReplaceAll(inner, "''", "'"), nil
	}
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c == '\\' && i+1 < len(inner) && (inner[i+1] == '"' || inner[i+1] == '\\') {
			b.WriteByte(inner[i+1])
			i++
			continue
		}
		if c == '"' {
			return "", fmt.Errorf("unescaped quote inside %s", s)
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

// ParseDate reads "2006-01-02" (09:00 local time), "2006-01-02 15:04" or an
// RFC 3339 timestamp, in loc unless the value carries its own offset.
func ParseDate(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02T15:04", s, loc); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, loc); err == nil {
		return t.Add(9 * time.Hour), nil
	}
	return time.Time{}, fmt.Errorf("date must be YYYY-MM-DD, YYYY-MM-DD HH:MM or RFC 3339, got %q", s)
}

var translit = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ß", "ss", "æ", "ae", "œ", "oe", "·", "-",
)

// Slugify builds a term slug from a display name: lowercase ASCII letters and
// digits joined by single hyphens, accents transliterated (á → a, ñ → n).
func Slugify(name string) string {
	s := translit.Replace(strings.ToLower(name))
	var b strings.Builder
	dash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
			continue
		}
		dash = true
	}
	out := b.String()
	if len(out) > 120 {
		out = strings.TrimRight(out[:120], "-")
	}
	return out
}
