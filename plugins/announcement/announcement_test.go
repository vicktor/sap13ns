package main

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWindow(t *testing.T) {
	tests := []struct {
		name  string
		start string
		end   string
		tz    string
		now   string
		shown bool
		warn  string
	}{
		{"no bounds", "", "", "", "2026-01-01T00:00:00Z", true, ""},
		{"before start", "2026-11-01 09:00", "", "", "2026-11-01T08:59:59Z", false, ""},
		{"start is inclusive", "2026-11-01 09:00", "", "", "2026-11-01T09:00:00Z", true, ""},
		{"end is exclusive", "", "2026-11-02T10:00", "", "2026-11-02T10:00:00Z", false, ""},
		{"just before end", "", "2026-11-02T10:00", "", "2026-11-02T09:59:59Z", true, ""},
		{"inside window", "2026-11-01", "2026-11-30 23:00:00", "UTC", "2026-11-15T12:00:00Z", true, ""},
		{"date-only end covers the day", "", "2026-11-30", "", "2026-11-30T23:59:59Z", true, ""},
		{"date-only end, next day", "", "2026-11-30", "", "2026-12-01T00:00:00Z", false, ""},
		{"Madrid summer (UTC+2)", "2026-07-01 10:00", "", "Europe/Madrid", "2026-07-01T07:59:00Z", false, ""},
		{"Madrid summer start", "2026-07-01 10:00", "", "Europe/Madrid", "2026-07-01T08:00:00Z", true, ""},
		{"Madrid winter (UTC+1)", "2026-12-01 10:00", "", "Europe/Madrid", "2026-12-01T09:00:00Z", true, ""},
		{"Madrid date-only end across DST", "", "2026-10-25", "Europe/Madrid", "2026-10-25T22:59:00Z", true, ""},
		{"Madrid date-only end, after", "", "2026-10-25", "Europe/Madrid", "2026-10-25T23:00:00Z", false, ""},
		{"RFC3339 offset ignores timezone", "2026-07-01T10:00:00+00:00", "", "Europe/Madrid", "2026-07-01T10:00:00Z", true, ""},
		{"RFC3339 offset ignores timezone, before", "2026-07-01T10:00:00Z", "", "Europe/Madrid", "2026-07-01T09:59:00Z", false, ""},
		{"offset without seconds", "2026-07-01T10:00+02:00", "", "", "2026-07-01T08:00:00Z", true, ""},
		{"invalid start", "tomorrow", "", "", "2026-01-01T00:00:00Z", false, "starts_at"},
		{"invalid end", "", "2026-13-01", "", "2026-01-01T00:00:00Z", false, "ends_at"},
		{"end before start", "2026-11-02", "2026-11-01 10:00", "", "2026-11-01T12:00:00Z", false, "later than"},
		{"end equals start", "2026-11-01 10:00", "2026-11-01 10:00", "", "2026-11-01T10:00:00Z", false, "later than"},
		{"unknown timezone falls back to UTC", "2026-11-01 09:00", "", "Mars/Olympus", "2026-11-01T09:00:00Z", true, "unknown timezone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := map[string]string{"message": "Hi", "starts_at": tc.start, "ends_at": tc.end, "timezone": tc.tz}
			out, warnings := Render(s, "", "en", at(tc.now))
			if (out != "") != tc.shown {
				t.Fatalf("shown = %v, want %v (%q)", out != "", tc.shown, out)
			}
			w := strings.Join(warnings, "; ")
			if tc.warn == "" && w != "" || !strings.Contains(w, tc.warn) {
				t.Fatalf("warnings = %q, want %q", w, tc.warn)
			}
		})
	}
}

func TestLanguage(t *testing.T) {
	s := map[string]string{
		"message": "Default", "message_es": "Hola", "message_en": "Hello",
		"link_url": "/sale", "link_label": "Go", "link_label_es": "Ir",
	}
	tests := []struct {
		name, url, siteLang string
		want                []string
	}{
		{"english prefix", "https://example.com/en/blog/x", "es", []string{"Hello", ">Go<", `aria-label="Announcement"`}},
		{"english home", "https://example.com/en", "es", []string{"Hello"}},
		{"spanish prefix", "https://example.com/es/", "en", []string{"Hola", ">Ir<", `aria-label="Anuncio"`}},
		{"root uses site language", "https://example.com/", "es", []string{"Hola", ">Ir<"}},
		{"site language with region", "/blog/x", "es-MX", []string{"Hola"}},
		{"enigma is not english", "https://example.com/enigma", "es", []string{"Hola"}},
		{"other language uses the default message", "https://example.com/fr/x", "fr", []string{"Default", ">Go<", `aria-label="Announcement"`}},
		{"empty url", "", "en", []string{"Hello"}},
		{"unparsable url", "http://[::1", "es", []string{"Hola"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := Render(s, tc.url, tc.siteLang, time.Now())
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in %s", w, out)
				}
			}
		})
	}
}

func TestRender(t *testing.T) {
	now := at("2026-11-01T00:00:00Z")
	tests := []struct {
		name     string
		settings map[string]string
		lang     string
		want     string
	}{
		{"empty config", map[string]string{}, "en", ""},
		{"blank message", map[string]string{"message": "  \n "}, "en", ""},
		{"only another language", map[string]string{"message_es": "Hola"}, "en", ""},
		{"minimal", map[string]string{"message": "Hello **world**"}, "en",
			`<section class="alert announcement" data-category="info" aria-label="Announcement"><span data-icon="info"></span><section><p>Hello <strong>world</strong></p></section></section>`},
		{"success with default label", map[string]string{"message": "Done", "style": "success", "link_url": "https://example.com/a?b=1&c=2"}, "en",
			`<section class="alert announcement" data-category="success" aria-label="Announcement"><span data-icon="circle-check"></span><section><p>Done</p><p><a href="https://example.com/a?b=1&amp;c=2"><span class="btn">Learn more</span></a></p></section></section>`},
		{"warning in spanish", map[string]string{"message": "Ojo", "style": "warning", "link_url": "/x"}, "es",
			`<section class="alert announcement" data-category="warning" aria-label="Anuncio"><span data-icon="triangle-alert"></span><section><p>Ojo</p><p><a href="/x"><span class="btn">Más información</span></a></p></section></section>`},
		{"invalid style", map[string]string{"message": "x", "style": "danger"}, "en",
			`<section class="alert announcement" data-category="info" aria-label="Announcement"><span data-icon="info"></span><section><p>x</p></section></section>`},
		{"unsafe link url dropped", map[string]string{"message": "x", "link_url": "javascript:alert(1)"}, "en",
			`<section class="alert announcement" data-category="info" aria-label="Announcement"><span data-icon="info"></span><section><p>x</p></section></section>`},
		{"line breaks", map[string]string{"message": "One\r\n\r\nTwo\n  Three  "}, "en",
			`<section class="alert announcement" data-category="info" aria-label="Announcement"><span data-icon="info"></span><section><p>One<br>Two<br>Three</p></section></section>`},
		{"escaped label", map[string]string{"message": "x", "link_url": "/a", "link_label": `<b>"Go"</b>`}, "en",
			`<section class="alert announcement" data-category="info" aria-label="Announcement"><span data-icon="info"></span><section><p>x</p><p><a href="/a"><span class="btn">&lt;b&gt;&#34;Go&#34;&lt;/b&gt;</span></a></p></section></section>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := Render(tc.settings, "", tc.lang, now)
			if out != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", out, tc.want)
			}
		})
	}
}

func TestInline(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"plain text", "plain text"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"a & b \"c\" 'd'", "a &amp; b &#34;c&#34; &#39;d&#39;"},
		{"**bold**", "<strong>bold</strong>"},
		{"**unclosed", "**unclosed"},
		{"****", "****"},
		{"a **b** c **d**", "a <strong>b</strong> c <strong>d</strong>"},
		{"[docs](https://example.com/docs)", `<a href="https://example.com/docs"><u>docs</u></a>`},
		{"[home](/)", `<a href="/"><u>home</u></a>`},
		{"[top](#top) [mail](mailto:a@b.test)", `<a href="#top"><u>top</u></a> <a href="mailto:a@b.test"><u>mail</u></a>`},
		{"[x](javascript:alert(1))", "x"},
		{"[x](data:text/html,hi)", "x"},
		{"[x](ftp://a)", "x"},
		{"[bad link](https://a b)", "[bad link](https://a b)"},
		{"[no url]()", "[no url]()"},
		{"[](https://a.test)", "[](https://a.test)"},
		{"[unclosed](https://a.test", "[unclosed](https://a.test"},
		{"[no paren] (https://a.test)", "[no paren] (https://a.test)"},
		{"[wiki](https://en.wikipedia.org/wiki/Go_(lang)) end", `<a href="https://en.wikipedia.org/wiki/Go_(lang)"><u>wiki</u></a> end`},
		{`[q](https://a.test/?a="b"&c=<d>)`, `<a href="https://a.test/?a=&#34;b&#34;&amp;c=&lt;d&gt;"><u>q</u></a>`},
		{"**see [docs](/d)**", `<strong>see <a href="/d"><u>docs</u></a></strong>`},
		{"[**bold** link](/d)", `<a href="/d"><u><strong>bold</strong> link</u></a>`},
		{"[<b>](/d)", `<a href="/d"><u>&lt;b&gt;</u></a>`},
		{"Оферта **до 30 ноября** — [подробнее](/ru/x) 日本語", `Оферта <strong>до 30 ноября</strong> — <a href="/ru/x"><u>подробнее</u></a> 日本語`},
	}
	for _, tc := range tests {
		if got := Inline(tc.in); got != tc.want {
			t.Errorf("Inline(%q)\n got %s\nwant %s", tc.in, got, tc.want)
		}
	}
}
