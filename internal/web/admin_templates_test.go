package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
)

// The admin pages are parsed when they are first shown, each in the
// language it is shown in; a page that does not parse is found at start.
func TestAdminTemplatesParseOnFirstUse(t *testing.T) {
	files := fstest.MapFS{}
	for _, name := range adminBase {
		files["admin/templates/"+name] = &fstest.MapFile{Data: []byte(`{{define "` + strings.TrimSuffix(name, ".html") + `-part"}}{{end}}`)}
	}
	files["admin/templates/one.html"] = &fstest.MapFile{Data: []byte(`one in {{lang}}`)}
	files["admin/templates/two.html"] = &fstest.MapFile{Data: []byte(`two in {{lang}}`)}
	funcs := func(lang string) template.FuncMap {
		return template.FuncMap{"lang": func() string { return "<" + lang + ">" }}
	}
	a, err := newAdminTemplates(files, funcs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.sets) != 0 {
		t.Fatalf("%d languages parsed at start", len(a.sets))
	}
	for _, c := range []struct{ lang, page, want string }{{"", "one", "one in &lt;&gt;"}, {"es", "one", "one in &lt;es&gt;"}, {"", "one", "one in &lt;&gt;"}} {
		var buf bytes.Buffer
		if err := a.render(&buf, c.lang, c.page, nil); err != nil || buf.String() != c.want {
			t.Fatalf("%s in %q: %v %q", c.page, c.lang, err, buf.String())
		}
	}
	if len(a.sets[""].pages) != 1 || len(a.sets["es"].pages) != 1 {
		t.Errorf("pages kept: %d and %d", len(a.sets[""].pages), len(a.sets["es"].pages))
	}
	var buf bytes.Buffer
	for _, name := range []string{"missing", "layout", "../one"} {
		if err := a.render(&buf, "", name, nil); err == nil {
			t.Errorf("%q rendered", name)
		}
	}

	files["admin/templates/three.html"] = &fstest.MapFile{Data: []byte(`{{if}}`)}
	if _, err := newAdminTemplates(files, funcs, false); err == nil {
		t.Error("a page that does not parse went unnoticed at start")
	}
}
