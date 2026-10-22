package theme

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFormatCSS(t *testing.T) {
	in := `/*! tw */@layer base{a{color:red;background:url(a;b.png)}b:hover{content:"x;{}"}}@media (min-width:40rem){.c{margin:0}}`
	want := `/*! tw */
@layer base {
  a {
    color:red;
    background:url(a;b.png)
  }
  b:hover {
    content:"x;{}"
  }
}
@media (min-width:40rem) {
  .c {
    margin:0
  }
}
`
	if got := FormatCSS(in); got != want {
		t.Errorf("FormatCSS:\n%s\nwant:\n%s", got, want)
	}
	// Only whitespace changes.
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if squash(FormatCSS(in)) != squash(in) {
		t.Error("FormatCSS changed more than whitespace")
	}
	lined := "a {\n  color: red;\n}\n"
	if FormatCSS(lined) != lined {
		t.Error("a stylesheet with lines changed")
	}
}

func TestRenameManifest(t *testing.T) {
	raw := []byte(`{
  "name": "Onepage",
  "slug": "onepage",
  "options": [
    {"key": "a"}
  ]
}`)
	out, err := renameManifest(raw, "onepage-custom", "Onepage (custom)")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if m["name"] != "Onepage (custom)" || m["slug"] != "onepage-custom" || len(m["options"].([]any)) != 1 {
		t.Errorf("%s", out)
	}
	if !strings.HasPrefix(string(out), "{\n  \"name\"") {
		t.Errorf("order lost: %s", out)
	}
	// No slug: none is added (the archive's folder names the copy).
	out, _ = renameManifest([]byte(`{"version": "1"}`), "x-custom", "X")
	if strings.Contains(string(out), "slug") || !strings.HasPrefix(string(out), "{\n  \"name\": \"X\"") {
		t.Errorf("%s", out)
	}
	if _, err := renameManifest([]byte(`[1]`), "x", "X"); err == nil {
		t.Error("an array was accepted")
	}
}
