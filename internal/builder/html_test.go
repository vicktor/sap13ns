package builder

import (
	"errors"
	"strings"
	"testing"
)

// TestHTMLBlock: an HTML block goes in any region, the header and the
// footer included, and is sanitised when it is saved, per language.
func TestHTMLBlock(t *testing.T) {
	src := `{"header": {"sections": [{"id": "h", "layout": "1", "class": "top  promo", "columns": [[
	    {"id": "hdr", "type": "site-header"},
	    {"id": "x", "type": "html", "settings": {"class": "notice", "html": "<p class=\"a\" onclick=\"alert(1)\">Hola <script>alert(1)</script><a href=\"javascript:alert(1)\">x</a></p><iframe src=\"https://evil.example\"></iframe>"},
	     "i18n": {"en": {"html": "<p style=\"color:red\">Hello</p><style>p{}</style>"}}}]]}]},
	  "footer": {"sections": [{"id": "f", "layout": "1", "columns": [[{"id": "y", "type": "html", "settings": {"html": "<details><summary>More</summary>x</details>"}}, {"id": "m", "type": "footer-meta"}]]}]}}`
	l, err := Parse([]byte(src), ctx)
	if err != nil {
		t.Fatal(err)
	}
	sec := l.Header.Sections[0]
	if sec.Class != "top promo" {
		t.Errorf("section class %q", sec.Class)
	}
	b := sec.Columns[0][1]
	if got := b.Settings["html"]; got != `<p class="a">Hola x</p>` {
		t.Errorf("sanitised on save: %q", got)
	}
	if got := b.I18n["en"]["html"]; got != `<p>Hello</p>` {
		t.Errorf("translation sanitised on save: %q", got)
	}
	if b.Settings["class"] != "notice" {
		t.Errorf("block class %q", b.Settings["class"])
	}
	if got := l.Footer.Sections[0].Columns[0][0].Settings["html"]; !strings.Contains(got, "<details>") {
		t.Errorf("footer HTML: %q", got)
	}
}

func TestHTMLBlockLimits(t *testing.T) {
	big := strings.Repeat("a", MaxHTML+1)
	for name, src := range map[string]string{
		"too long":       `{"pages": {"front": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "x", "type": "html", "settings": {"html": "` + big + `"}}]]}]}}}`,
		"bad class":      `{"pages": {"front": {"sections": [{"id": "s", "layout": "1", "columns": [[{"id": "x", "type": "html", "settings": {"class": "Bad\"><script>"}}]]}]}}}`,
		"section class":  `{"pages": {"front": {"sections": [{"id": "s", "layout": "1", "class": "1abc", "columns": [[]]}]}}}`,
		"too many":       `{"pages": {"front": {"sections": [{"id": "s", "layout": "1", "class": "a b c d e f", "columns": [[]]}]}}}`,
		"credits hidden": `{"footer": {"sections": [{"id": "f", "layout": "1", "class": "hidden", "columns": [[{"id": "m", "type": "footer-meta"}]]}]}}`,
	} {
		_, err := Parse([]byte(src), ctx)
		var errs Errors
		if !errors.As(err, &errs) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	// Stored layouts that no longer validate keep working without the value.
	l := load(t, `{"footer": {"sections": [{"id": "f", "layout": "1", "class": "hidden", "columns": [[{"id": "m", "type": "footer-meta"}]]}]}}`)
	if l.Footer.Sections[0].Class != "" {
		t.Error("the credits section kept its class")
	}
}

func TestValidClasses(t *testing.T) {
	for in, want := range map[string]string{"": "", " a  b-2 a ": "a b-2", "promo": "promo", "a b c d e": "a b c d e"} {
		if got, ok := ValidClasses(in); !ok || got != want {
			t.Errorf("ValidClasses(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"A", "1a", "a_b", "a:b", "a.b", "-a", "a b c d e f", `a"b`, strings.Repeat("a", 41)} {
		if _, ok := ValidClasses(in); ok {
			t.Errorf("ValidClasses(%q) accepted", in)
		}
	}
}
