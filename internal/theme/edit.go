package theme

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Editing a theme's code in the admin (docs/THEMES.md, "Editing a theme's
// code"). A save never writes a file in place: the theme is packed again
// with the changed file and goes through Install, so it passes every check
// of an upload, counts against the storage and replaces the old version
// only once it loads. A built-in or network theme cannot change, so its
// first save installs a copy under another slug.

// MaxEditBytes caps one file in the code editor.
const MaxEditBytes = 512 << 10

// ErrNotEditable: the file is not one of the theme's text files.
var ErrNotEditable = errors.New("this file cannot be edited here")

// editable reports whether a file of a theme is text the code editor opens.
func editable(name string) bool {
	kind, p := classify(name)
	if p != nil {
		return false
	}
	switch kind {
	case kindManifest, kindTemplate, kindI18n, kindSrc:
		return true
	case kindStatic:
		return strings.EqualFold(path.Ext(name), ".css")
	}
	return false
}

// EditableFiles are the theme's files the code editor opens: theme.json,
// then the templates, stylesheets and translations, sorted.
func (t *Theme) EditableFiles() []string {
	var out []string
	_ = fs.WalkDir(t.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != "." && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if editable(p) {
			out = append(out, p)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i] == "theme.json") != (out[j] == "theme.json") {
			return out[i] == "theme.json"
		}
		return out[i] < out[j]
	})
	return out
}

// ReadEditable returns one of EditableFiles.
func (t *Theme) ReadEditable(name string) ([]byte, error) {
	if !editable(name) {
		return nil, ErrNotEditable
	}
	b, err := fs.ReadFile(t.fsys, name)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxEditBytes {
		return nil, ErrNotEditable
	}
	return b, nil
}

// SaveOptions describe a save from the code editor.
type SaveOptions struct {
	InstallOptions
	// Slug is the theme it installs as: t.Slug for a theme of the site's
	// own directory, a free slug for a copy (with Name, its new name).
	Slug, Name string
}

// SaveEdit packs t again with file name holding content and installs it
// as opts.Slug (Install): a refusal changes nothing.
func (m *Manager) SaveEdit(ctx context.Context, t *Theme, name string, content []byte, opts SaveOptions) (*InstallResult, error) {
	if !editable(name) || len(content) > MaxEditBytes {
		return nil, ErrNotEditable
	}
	if _, err := fs.Stat(t.fsys, name); err != nil {
		return nil, ErrNotEditable
	}
	if opts.Slug != t.Slug && !ValidSlug(opts.Slug) {
		return nil, fmt.Errorf("bad slug %q", opts.Slug)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	err := fs.WalkDir(t.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != "." {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil // the upload record, a stray dotfile
		}
		if d.IsDir() {
			return nil
		}
		b := content
		if p != name {
			if b, err = fs.ReadFile(t.fsys, p); err != nil {
				return err
			}
		}
		if p == "theme.json" && opts.Slug != t.Slug {
			if b, err = renameManifest(b, opts.Slug, opts.Name); err != nil {
				// An unreadable theme.json of the copy: Install says why.
				b = content
			}
		}
		w, err := zw.Create(opts.Slug + "/" + p)
		if err != nil {
			return err
		}
		_, err = w.Write(b)
		return err
	})
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		return nil, err
	}
	return m.Install(ctx, bytes.NewReader(buf.Bytes()), int64(buf.Len()), opts.InstallOptions)
}

// FreeSlug is the first of base-custom, base-custom-2 … that no theme
// (built in, the network's or the site's) has, or "" when none is.
func (m *Manager) FreeSlug(base string) string {
	for i := 1; i < 100; i++ {
		s := base + "-custom"
		if i > 1 {
			s = fmt.Sprintf("%s-custom-%d", base, i)
		}
		if !ValidSlug(s) {
			return ""
		}
		if _, ok := m.Get(s); ok {
			continue
		}
		if builtin, network := m.taken(s); builtin || network || m.failure(s) != nil {
			continue
		}
		return s
	}
	return ""
}

// renameManifest sets theme.json's "name" (when name is not "") and, when
// it has one, its "slug", keeping the order and layout of the rest.
func renameManifest(raw []byte, slug, name string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("theme.json is not an object")
	}
	type member struct {
		key string
		val json.RawMessage
	}
	var members []member
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		members = append(members, member{key, val})
	}
	set := func(key, v string) {
		q, _ := json.Marshal(v)
		for i := range members {
			if members[i].key == key {
				members[i].val = q
				return
			}
		}
		if key == "name" {
			members = append([]member{{key, q}}, members...)
		}
	}
	if name != "" {
		set("name", name)
	}
	for _, mb := range members {
		if mb.key == "slug" {
			set("slug", slug)
		}
	}
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, mb := range members {
		k, _ := json.Marshal(mb.key)
		fmt.Fprintf(&out, "  %s: %s", k, mb.val)
		if i < len(members)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("}\n")
	return out.Bytes(), nil
}

// FormatCSS breaks a minified stylesheet (a compiled theme.css is one long
// line) into one rule and one declaration a line, indented by nesting. It
// only changes whitespace between tokens, never inside strings, comments
// or parentheses, so the stylesheet means the same. A stylesheet that
// already has lines comes back as it is.
func FormatCSS(css string) string {
	if strings.Count(css, "\n") > len(css)/400 {
		return css
	}
	var b strings.Builder
	b.Grow(len(css) + len(css)/4)
	depth, parens := 0, 0
	breakLine := false // a line break is owed before the next token
	emit := func(s string) {
		if breakLine {
			b.WriteByte('\n')
			for range depth {
				b.WriteString("  ")
			}
			breakLine = false
		}
		b.WriteString(s)
	}
	for i := 0; i < len(css); i++ {
		c := css[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(css) && css[j] != c {
				if css[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(css))
			emit(css[i:j])
			i = j - 1
		case c == '/' && i+1 < len(css) && css[i+1] == '*':
			end := len(css)
			if j := strings.Index(css[i+2:], "*/"); j >= 0 {
				end = i + 2 + j + 2
			}
			emit(css[i:end])
			i = end - 1
			if depth == 0 && parens == 0 {
				breakLine = true
			}
		case parens > 0:
			if c == '(' {
				parens++
			} else if c == ')' {
				parens--
			}
			b.WriteByte(c)
		case c == '(':
			parens++
			emit("(")
		case c == '{':
			b.WriteString(" {")
			depth++
			breakLine = true
		case c == ';':
			b.WriteByte(';')
			breakLine = true
		case c == '}':
			depth = max(depth-1, 0)
			breakLine = true
			emit("}")
			breakLine = true
		case breakLine && (c == ' ' || c == '\n' || c == '\t' || c == '\r'):
			// Whitespace where a line break goes already.
		default:
			emit(string(c))
		}
	}
	return b.String() + "\n"
}
