package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	"sap13ns/ui"
)

func TestSelectboxResolve(t *testing.T) {
	o := []any{opt("", "(none)"), opt(3, "Three"), opt(int64(7), "Seven")}
	cases := []struct {
		name         string
		kv           []any
		value, label string
		chosen       bool
		placeholder  string
	}{
		{"match", []any{"value", int64(7), "options", o}, "7", "Seven", true, ""},
		{"empty option chosen", []any{"value", "", "options", o}, "", "(none)", true, ""},
		{"unknown falls back to the first", []any{"value", 99, "options", o[1:]}, "3", "Three", true, ""},
		{"unknown with a placeholder is empty", []any{"value", 99, "options", o[1:], "placeholder", "Pick"}, "", "Pick", false, "Pick"},
		{"search: the empty option is the placeholder", []any{"value", 0, "options", o, "search", true}, "", "(none)", false, "(none)"},
		{"search: a match", []any{"value", 3, "options", o, "search", true}, "3", "Three", true, "(none)"},
		{"no options", []any{"value", "x"}, "", "", false, ""},
		{"pairs and strings", []any{"value", "b", "options", []any{[]any{"a", "A"}, []any{"b", "B"}}}, "b", "B", true, ""},
		{"string choices", []any{"value", "y", "options", []string{"x", "y"}}, "y", "y", true, ""},
	}
	for _, c := range cases {
		b, err := selectbox(append([]any{"id", "f"}, c.kv...)...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if b.Value != c.value || b.Label != c.label || b.Chosen != c.chosen || b.Placeholder != c.placeholder {
			t.Errorf("%s: got value %q label %q chosen %v placeholder %q", c.name, b.Value, b.Label, b.Chosen, b.Placeholder)
		}
	}
}

func TestSelectboxErrors(t *testing.T) {
	for name, kv := range map[string][]any{
		"no id":        {"name", "x"},
		"odd":          {"id"},
		"unknown key":  {"id", "x", "nmae", "y"},
		"bad bool":     {"id", "x", "disabled", "yes"},
		"bad options":  {"id", "x", "options", 3},
		"bad option":   {"id", "x", "options", []any{"a"}},
		"bad data key": {"id", "x", "data", map[string]any{"on click": "1"}},
		"data not map": {"id", "x", "data", "autosubmit"},
	} {
		if _, err := selectbox(kv...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// The partial renders a Basecoat select whose hidden input submits what the
// native select did, and whose trigger carries the id for <label for>.
func TestSelectPartial(t *testing.T) {
	s := &Server{}
	funcs := s.funcMap()
	funcs["T"] = func(k string, _ ...any) string { return k }
	src, err := ui.FS.ReadFile("admin/templates/partials.html")
	if err != nil {
		t.Fatal(err)
	}
	tpl := template.Must(template.New("p").Funcs(funcs).Parse(string(src)))
	render := func(kv ...any) string {
		b, err := selectbox(kv...)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := tpl.ExecuteTemplate(&out, "select", b); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	got := render("id", "role", "name", "role", "value", "editor", "disabled", true, "describedby", "role-help",
		"options", []any{opt("author", "Author"), opt("editor", `Editor <b>`)}, "data", map[string]any{"autosubmit": "", "type": `a"b`})
	for _, want := range []string{
		`<div class="select" id="role-select">`,
		`<button type="button" id="role" role="combobox" aria-haspopup="listbox" aria-expanded="false" aria-controls="role-listbox" aria-describedby="role-help" disabled>`,
		`<span class="truncate">Editor &lt;b&gt;</span>`,
		`<div role="option" id="role-opt-1" data-value="editor" aria-selected="true">Editor &lt;b&gt;</div>`,
		`<div role="option" id="role-opt-0" data-value="author">Author</div>`,
		`<input type="hidden" name="role" value="editor" data-autosubmit data-type="a&#34;b" disabled>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("select: missing %s in\n%s", want, got)
		}
	}

	got = render("id", "parent_id", "name", "parent_id", "value", 0, "search", true, "class", "w-full",
		"options", []any{opt("", "(none)"), opt(4, "About")})
	for _, want := range []string{
		`<div class="combobox w-full" id="parent_id-combobox" data-auto-highlight="true">`,
		`<span class="truncate text-muted-foreground" data-value data-placeholder="(none)">(none)</span>`,
		`<input type="text" role="combobox" aria-label="Search"`,
		`<div role="option" id="parent_id-opt-0" data-value="">(none)</div>`,
		`<input type="hidden" name="parent_id" value="">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("combobox: missing %s in\n%s", want, got)
		}
	}
}
