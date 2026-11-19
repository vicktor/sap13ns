package theme

import (
	"bytes"
	"strings"
	"testing"
)

// A theme's templates are parsed when it first renders, not when it is
// loaded; one whose templates do not parse is still found at Load, also
// by a second manager of the same process, which does not parse it again.
func TestTemplatesParseOnFirstUse(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "fine", `{{define "layout"}}<p>{{template "content" .}}</p>{{template "shared/footer-meta" .}}{{end}}`, "home of fine")
	writeTheme(t, dir, "broken", `{{define "layout"}}{{template "content" .}}{{end}}`, `{{if}}`)
	for round := range 2 {
		m := testManager(t, dir)
		if err := m.Load(); err != nil {
			t.Fatal(err)
		}
		if f := m.Failures(); len(f) != 1 || f[0].Slug != "broken" || f[0].Err == "" {
			t.Fatalf("round %d: failures %v", round, f)
		}
		if _, ok := m.Get("broken"); ok {
			t.Fatalf("round %d: the broken theme was loaded", round)
		}
		th, ok := m.Get("fine")
		if !ok {
			t.Fatalf("round %d: the theme was not loaded", round)
		}
		if th.pages != nil {
			t.Fatalf("round %d: the templates were parsed at load", round)
		}
		if !th.Has("home") || !th.Has("single") || th.Has("front") {
			t.Errorf("round %d: the theme's own pages are not known before it renders", round)
		}
		var buf bytes.Buffer
		if err := th.Render(&buf, "home", "en", nil); err != nil || !strings.Contains(buf.String(), "home of fine") {
			t.Fatalf("round %d: render: %v %q", round, err, buf.String())
		}
		if th.pages == nil {
			t.Fatalf("round %d: rendering did not keep the templates", round)
		}
	}
}
