package content

import (
	"strings"
	"testing"
)

func TestRenderLinks(t *testing.T) {
	out := string(RenderPlain("[home](/blog/hola) and [out](https://example.org/x) and [bad](javascript:alert(1))\n\n<script>alert(1)</script>"))
	for _, want := range []string{`<a href="/blog/hola">home</a>`, `<a href="https://example.org/x">out</a>`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	for _, bad := range []string{"nofollow", "javascript:", "<script"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q in %s", bad, out)
		}
	}
}

func TestHeadingIDs(t *testing.T) {
	out := string(RenderPlain("## Detalle técnico\n\n## Detalle técnico\n\n## 你好\n\n### Ελληνικά"))
	for _, want := range []string{`<h2 id="detalle-tecnico">`, `<h2 id="detalle-tecnico-2">`, `<h2 id="section-`, `<h3 id="`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	if again := string(RenderPlain("## 你好")); !strings.Contains(out, again[:strings.Index(again, ">")]) {
		t.Errorf("hash id not stable: %s vs %s", again, out)
	}
}

func TestPluginPolicy(t *testing.T) {
	in := `<nav aria-label="Contents" role="navigation"><a href="/x">in</a> <a href="https://e.org" rel="noopener external" target="_blank">out</a> <a href="tel:+34600000000">call</a> <a href="https://e.org" target="_top" rel="evil">bad</a> <kbd>K</kbd></nav><span data-icon="info"></span>`
	kept := string(SanitizePluginHTML(in, nil))
	for _, want := range []string{`<nav aria-label="Contents" role="navigation">`, `<a href="/x">in</a>`,
		`rel="noopener external" target="_blank"`, `href="tel:+34600000000"`, `<kbd>K</kbd>`, `<span data-icon="info"></span>`} {
		if !strings.Contains(kept, want) {
			t.Errorf("missing %s in %s", want, kept)
		}
	}
	for _, bad := range []string{"nofollow", "_top", "evil"} {
		if strings.Contains(kept, bad) {
			t.Errorf("unexpected %q in %s", bad, kept)
		}
	}
	icon := func(n string) string { return "<svg>" + n + "</svg>" }
	if got := ExpandIcons(kept, icon); !strings.Contains(got, "<svg>info</svg>") || strings.Contains(got, "data-icon") {
		t.Errorf("icons not expanded: %s", got)
	}
	if got := string(SanitizePluginHTML(`<span data-icon="nope"></span>x`, func(string) string { return "" })); got != "x" {
		t.Errorf("unknown icon kept: %q", got)
	}
}

func TestPluginFormPlaceholder(t *testing.T) {
	got := string(SanitizePluginHTML(`<div data-form="contact.main"></div><div data-form="bad form"></div>`, nil))
	if !strings.Contains(got, `<div data-form="contact.main"></div>`) || strings.Contains(got, "bad form") {
		t.Errorf("form placeholders: %s", got)
	}
}
