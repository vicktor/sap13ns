package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScanRepo(t *testing.T) {
	root := writeTree(t, map[string]string{
		"go.mod": "module example.test/site\n\ngo 1.24\n",
		"ui/admin/page.html": `{{define "page"}}
<h1>{{T "Say \"hi\""}}</h1>
<p>{{T "%d posts" .Count}} {{(T "%s by %s" .A .B)}} {{T ` + "`raw`" + `}}</p>
{{plural .N "%d comment" "%d comments"}} {{roleLabel .Role}}
{{if .X}}{{T "In an if"}}{{else}}{{T "In the else"}}{{end}}
{{template "row" (dict "Label" (T "Nested"))}}
{{end}}`,
		"internal/web/web.go": `package web

import (
	"fmt"

	"example.test/site/internal/schema"
)

type Server struct{}

func (s *Server) t(key string, args ...any) string { return fmt.Sprintf(key, args...) }

const greeting = "Hello, " + "world"

var labels = map[string]string{"a": "Label A", "b": "Label B"}

func (s *Server) label(v string) string { return s.t(v) } // a wrapper

func (s *Server) handler(lang string, k string, dyn string) {
	s.t("Plain")
	s.t("Escaped \"quotes\"\n")
	s.t(greeting)
	s.label("Through a wrapper")
	s.t(labels[k])
	for _, o := range []struct{ Name string }{{Name: "Option one"}, {Name: "Option two"}} {
		s.t(o.Name)
	}
	T := func(key string, args ...any) string { return s.t(key, args...) }
	T("Local translator %d", 1)
	for _, ty := range schema.Types {
		s.t(ty.Label)
		for _, f := range ty.Fields {
			s.t(f.Label)
			if f.I18n && f.Default != "" {
				s.t(f.Default)
			}
			for _, c := range f.Choices {
				s.t(c.Label)
			}
		}
	}
	if ty, ok := schema.TypeOf(k); ok {
		f, _ := ty.Field(k)
		s.t(f.Label)
	}
	s.t(dyn + "!")
}
`,
		"internal/schema/schema.go": `package schema

type Choice struct{ Value, Label string }

type Field struct {
	Key, Label, Default string
	I18n               bool
	Choices            []Choice
}

type Type struct {
	Label  string
	Fields []Field
}

func pairs(p ...string) []Choice {
	var out []Choice
	for i := 0; i+1 < len(p); i += 2 {
		out = append(out, Choice{Value: p[i], Label: p[i+1]})
	}
	return out
}

var (
	sizes  = pairs("s", "Small", "l", "Large")
	toggle = func(key, label string) Field { return Field{Key: key, Label: label, Default: "true"} }
)

var Types = []Type{
	{Label: "Hero", Fields: []Field{
		{Key: "title", Label: "Title", Default: "Welcome", I18n: true},
		{Key: "size", Label: "Size", Default: "s", Choices: sizes},
		toggle("tagline", "Show the tagline"),
	}},
}

func TypeOf(name string) (*Type, bool) { return &Types[0], true }

func (t *Type) Field(key string) (*Field, bool) { return &t.Fields[0], true }
`,
	})
	e := NewExtractor()
	if err := e.ScanRepo(root); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`Say "hi"`, "%d posts", "%s by %s", "raw", "%d comment", "%d comments", "In an if", "In the else", "Nested",
		"Plain", "Escaped \"quotes\"\n", "Hello, world", "Through a wrapper", "Label A", "Label B",
		"Option one", "Option two", "Local translator %d",
		"Hero", "Title", "Size", "Show the tagline", "Welcome", "Small", "Large",
	}
	for _, k := range want {
		if _, ok := e.Keys[k]; !ok {
			t.Errorf("key %q not found", k)
		}
	}
	for _, k := range []string{"s", "l", "true", "title", "size"} {
		if _, ok := e.Keys[k]; ok {
			t.Errorf("%q is a value, not a key", k)
		}
	}
	if loc := e.Keys["%d posts"][0]; loc.File != "ui/admin/page.html" || loc.Line != 3 {
		t.Errorf("location %v", loc)
	}
	var unresolved []string
	for _, d := range e.Dynamic {
		if d.Kind == DynUnresolved {
			unresolved = append(unresolved, d.Expr)
		}
	}
	if !slices.Equal(unresolved, []string{`s.t(dyn + "!")`}) {
		t.Errorf("unresolved sites %v", unresolved)
	}
	if len(e.Warnings) > 0 {
		t.Errorf("warnings %v", e.Warnings)
	}
}

func TestTemplateFallback(t *testing.T) {
	// A template that does not parse is still scanned with regexps.
	e := NewExtractor()
	e.ScanTemplate("broken.html", `{{if}} {{T "Still \"found\""}} {{plural .N "%d one" "%d many"}}`)
	for _, k := range []string{`Still "found"`, "%d one", "%d many"} {
		if _, ok := e.Keys[k]; !ok {
			t.Errorf("key %q not found", k)
		}
	}
	if len(e.Warnings) != 1 {
		t.Errorf("warnings %v", e.Warnings)
	}
}

func TestThemeManifests(t *testing.T) {
	// The admin shows manifest strings through themeT: they are keys, the
	// theme's name and the option values are not.
	root := writeTree(t, map[string]string{
		"go.mod": "module example.test/site\n\ngo 1.24\n",
		"ui/themes/paper/theme.json": `{
  "name": "Paper",
  "description": "A quiet theme.",
  "menus": [{"id": "primary", "label": "Main menu"}],
  "widget_areas": [{"id": "side", "label": "Sidebar", "description": "Beside posts."}],
  "options": [{"key": "tone", "label": "Tone", "type": "select", "default": "warm", "help": "Paper colour.",
    "choices": [{"value": "warm", "label": "Warm"}, {"value": "cold", "label": "Cold"}]}]
}`,
		"ui/admin/themes.html": `{{range .Themes}}<p>{{themeT . .Description}}</p>{{end}}`,
	})
	e := NewExtractor()
	if err := e.ScanRepo(root); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"A quiet theme.", "Main menu", "Sidebar", "Beside posts.", "Tone", "Paper colour.", "Warm", "Cold"} {
		if _, ok := e.Keys[k]; !ok {
			t.Errorf("key %q not found", k)
		}
	}
	for _, k := range []string{"Paper", "warm", "primary", "side", "tone"} {
		if _, ok := e.Keys[k]; ok {
			t.Errorf("%q is not a key", k)
		}
	}
	if loc := e.Keys["Warm"][0]; loc.File != "ui/themes/paper/theme.json" || loc.Line != 7 {
		t.Errorf("location %v", loc)
	}
	if len(e.Dynamic) != 1 || e.Dynamic[0].Kind != DynTheme {
		t.Errorf("dynamic sites %v", e.Dynamic)
	}
}
