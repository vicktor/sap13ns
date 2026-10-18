package main

import (
	"strings"
	"testing"
)

func TestCallout(t *testing.T) {
	for _, tc := range []struct {
		name  string
		attrs map[string]string
		inner string
		want  string
	}{
		{"warning with title", map[string]string{"type": "warning", "title": "Ojo"}, "Texto <b>importante</b>",
			`<div class="alert sc-callout" role="note" data-category="warning"><span data-icon="triangle-alert"></span><strong>Ojo</strong><section><p>Texto &lt;b&gt;importante&lt;/b&gt;</p></section></div>`},
		{"error is destructive", map[string]string{"type": "ERROR "}, "x",
			`<div class="alert sc-callout" role="note" data-category="error" data-variant="destructive"><span data-icon="circle-alert"></span><section><p>x</p></section></div>`},
		{"unknown type falls back to info", map[string]string{"type": `"><script>`}, "",
			`<div class="alert sc-callout" role="note" data-category="info"><span data-icon="info"></span></div>`},
		{"no attributes", nil, "a\r\n\r\nb\n\n\n\nc",
			`<div class="alert sc-callout" role="note" data-category="info"><span data-icon="info"></span><section><p>a</p><p>b</p><p>c</p></section></div>`},
		{"title is escaped", map[string]string{"type": "success", "title": `"Hecho" & <listo>`}, "Привет, 世界",
			`<div class="alert sc-callout" role="note" data-category="success"><span data-icon="circle-check"></span><strong>&#34;Hecho&#34; &amp; &lt;listo&gt;</strong><section><p>Привет, 世界</p></section></div>`},
		{"blank title and inner", map[string]string{"title": "  "}, " \n ",
			`<div class="alert sc-callout" role="note" data-category="info"><span data-icon="info"></span></div>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Callout(tc.attrs, tc.inner); got != tc.want {
				t.Errorf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestKbd(t *testing.T) {
	for in, want := range map[string]string{
		"Ctrl+K":       `<span class="kbd"><kbd>Ctrl+K</kbd></span>`,
		"  Ctrl \n K ": `<span class="kbd"><kbd>Ctrl K</kbd></span>`,
		"<Esc>":        `<span class="kbd"><kbd>&lt;Esc&gt;</kbd></span>`,
		"⌘ + Ü":        `<span class="kbd"><kbd>⌘ + Ü</kbd></span>`,
		"":             "",
		" \t ":         "",
	} {
		if got := Kbd(in); got != want {
			t.Errorf("Kbd(%q) = %q, want %q", in, got, want)
		}
	}
}

const noteOpen = `<div class="alert markdown-alert markdown-alert-note" role="note" data-category="info"><span data-icon="info"></span><strong>Note</strong><section>`

func TestFilterAlerts(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		lang string
		want string // "" means unchanged
	}{
		{
			name: "goldmark output, marker on its own line",
			in:   "<blockquote>\n<p>[!NOTE]\nUseful information.</p>\n</blockquote>\n",
			want: noteOpen + "\n<p>Useful information.</p>\n</section></div>\n",
		},
		{
			name: "marker paragraph alone, several paragraphs",
			in:   "<blockquote>\n<p>[!NOTE]</p>\n<p>One.</p>\n<ul><li>Two</li></ul>\n</blockquote>",
			want: noteOpen + "\n\n<p>One.</p>\n<ul><li>Two</li></ul>\n</section></div>",
		},
		{
			name: "hard line break after the marker",
			in:   "<blockquote><p>[!NOTE]<br/>\nText <strong>bold</strong></p></blockquote>",
			want: noteOpen + "<p>Text <strong>bold</strong></p></section></div>",
		},
		{
			name: "text on the marker line is kept as body",
			in:   "<blockquote><p>  [!note] Same line.</p></blockquote>",
			want: noteOpen + "<p>Same line.</p></section></div>",
		},
		{
			name: "Spanish titles and every kind",
			in: "<blockquote><p>[!TIP]\na</p></blockquote><blockquote><p>[!IMPORTANT]\nb</p></blockquote>" +
				"<blockquote><p>[!WARNING]\nc</p></blockquote><blockquote><p>[!CAUTION]\nd</p></blockquote>",
			lang: "es",
			want: `<div class="alert markdown-alert markdown-alert-tip" role="note" data-category="success"><span data-icon="lightbulb"></span><strong>Consejo</strong><section><p>a</p></section></div>` +
				`<div class="alert markdown-alert markdown-alert-important" role="note" data-category="important"><span data-icon="message-square-warning"></span><strong>Importante</strong><section><p>b</p></section></div>` +
				`<div class="alert markdown-alert markdown-alert-warning" role="note" data-category="warning"><span data-icon="triangle-alert"></span><strong>Advertencia</strong><section><p>c</p></section></div>` +
				`<div class="alert markdown-alert markdown-alert-caution" role="note" data-category="error" data-variant="destructive"><span data-icon="octagon-alert"></span><strong>Precaución</strong><section><p>d</p></section></div>`,
		},
		{
			name: "regional Spanish",
			in:   "<blockquote><p>[!NOTE]\nx</p></blockquote>",
			lang: "es-mx",
			want: `<div class="alert markdown-alert markdown-alert-note" role="note" data-category="info"><span data-icon="info"></span><strong>Nota</strong><section><p>x</p></section></div>`,
		},
		{
			name: "nested quote inside an alert is converted too",
			in:   "<blockquote><p>[!NOTE]\nouter</p><blockquote><p>[!WARNING]\ninner</p></blockquote></blockquote>",
			want: noteOpen + `<p>outer</p><div class="alert markdown-alert markdown-alert-warning" role="note" data-category="warning"><span data-icon="triangle-alert"></span><strong>Warning</strong><section><p>inner</p></section></div></section></div>`,
		},
		{
			name: "non-Latin body",
			in:   "<blockquote><p>[!NOTE]\nПривет, 世界 — مرحبا</p></blockquote>",
			want: noteOpen + "<p>Привет, 世界 — مرحبا</p></section></div>",
		},
		{name: "plain quote", in: "<blockquote><p>Just a quote [!NOTE]</p></blockquote>"},
		{name: "unknown kind", in: "<blockquote><p>[!DANGER]\nx</p></blockquote>"},
		{name: "unclosed marker", in: "<blockquote><p>[!NOTE\nx</p></blockquote>"},
		{name: "marker not first", in: "<blockquote><p><em>[!NOTE]</em> x</p></blockquote>"},
		{name: "first child is not a paragraph", in: "<blockquote><ul><li>[!NOTE] x</li></ul></blockquote>"},
		{name: "empty alert stays a quote", in: "<blockquote>\n<p>[!NOTE]</p>\n</blockquote>"},
		{name: "marker in code", in: "<pre><code>&gt; [!NOTE]\n</code></pre><p><code>[!NOTE]</code></p>"},
		{name: "no blockquote", in: "<p>[!NOTE] text</p>"},
		{name: "empty input", in: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := FilterAlerts(tc.in, tc.lang)
			want := tc.want
			if want == "" {
				want = tc.in
			}
			if changed != (tc.want != "") {
				t.Fatalf("changed = %v\n%s", changed, got)
			}
			if got != want {
				t.Errorf("got\n%q\nwant\n%q", got, want)
			}
		})
	}
}

func TestFilterAlertsMalformedHTML(t *testing.T) {
	for _, in := range []string{
		"<blockquote><p>[!NOTE]\nunclosed <b>bold",
		"<blockquote><p>[!WARNING]</p><div><p>stray</div></blockquote></blockquote>",
		"</p><blockquote><p>[!TIP]\n<a href=x>link</blockquote>",
		"<blockquote><p>[!NOTE]\x00\nnul</p></blockquote>",
	} {
		got, changed := FilterAlerts(in, "en")
		if !changed || !strings.Contains(got, `class="alert markdown-alert`) || strings.Contains(got, "[!") {
			t.Errorf("FilterAlerts(%q) = %q, %v", in, got, changed)
		}
	}
}
