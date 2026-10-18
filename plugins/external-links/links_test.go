package main

import (
	"strings"
	"testing"
)

const site = "https://example.com"

func TestFilter(t *testing.T) {
	base := Config{SiteURL: site, Lang: "en", UTMMedium: "referral"}
	withTab := base
	withTab.NewTab = true
	spanish := withTab
	spanish.Lang = "es-ES"
	sponsored := base
	sponsored.SponsoredDomains = ParseDomains("https://www.Amazon.com/ref, shop.test")
	utm := base
	utm.UTMDomains = ParseDomains("partner.test\npartner2.test")
	utm.UTMCampaign = "fall sale"
	idnSite := base
	idnSite.SiteURL = "https://bücher.example"
	noSite := base
	noSite.SiteURL = ""

	tests := []struct {
		name string
		cfg  Config
		in   string
		want string // "" means unchanged
	}{
		{"empty input", base, "", ""},
		{"no links", base, "<p>Hola</p>", ""},
		{"same host", base, `<a href="https://example.com/a">a</a>`, ""},
		{"same host uppercase", base, `<a href="HTTPS://EXAMPLE.COM/a">a</a>`, ""},
		{"www variant", base, `<a href="https://www.example.com/a">a</a>`, ""},
		{"same host other port", base, `<a href="http://example.com:8080/a">a</a>`, ""},
		{"trailing dot", base, `<a href="https://example.com./a">a</a>`, ""},
		{"subdomain is external", base, `<a href="https://blog.example.com/">b</a>`,
			`<a href="https://blog.example.com/" rel="noopener noreferrer">b</a>`},
		{"external", base, `<p>See <a href="https://other.test/x">this</a>.</p>`,
			`<p>See <a href="https://other.test/x" rel="noopener noreferrer">this</a>.</p>`},
		{"protocol-relative", base, `<a href="//other.test/x">x</a>`,
			`<a href="//other.test/x" rel="noopener noreferrer">x</a>`},
		{"protocol-relative same host", base, `<a href="//example.com/x">x</a>`, ""},
		{"fragment", base, `<a href="#top">top</a>`, ""},
		{"relative path", base, `<a href="/blog/x">x</a> <a href="../y">y</a> <a href="?page=2">2</a>`, ""},
		{"mailto and tel", base, `<a href="mailto:a@other.test">m</a> <a href="tel:+34600000000">t</a>`, ""},
		{"javascript", base, `<a href="javascript:alert(1)">j</a>`, ""},
		{"ftp", base, `<a href="ftp://other.test/f">f</a>`, ""},
		{"no href", base, `<a name="x">x</a>`, ""},
		{"unparsable", base, `<a href="http://[::1">x</a>`, ""},
		{"existing rel merged", base, `<a href="https://other.test" rel="NOFOLLOW external">x</a>`,
			`<a href="https://other.test" rel="nofollow external noopener noreferrer">x</a>`},
		{"already marked", base, `<a href="https://other.test" rel="noopener noreferrer">x</a>`, ""},
		{"sponsored", sponsored, `<a href="https://amazon.com/dp/1">x</a>`,
			`<a href="https://amazon.com/dp/1" rel="noopener noreferrer sponsored nofollow">x</a>`},
		{"sponsored subdomain", sponsored, `<a href="https://smile.amazon.com/">x</a>`,
			`<a href="https://smile.amazon.com/" rel="noopener noreferrer sponsored nofollow">x</a>`},
		{"sponsored lookalike", sponsored, `<a href="https://notamazon.com/">x</a>`,
			`<a href="https://notamazon.com/" rel="noopener noreferrer">x</a>`},
		{"new tab", withTab, `<a href="https://other.test">x</a>`,
			`<a href="https://other.test" rel="noopener noreferrer" target="_blank">x<span class="sr-only"> (opens in a new tab)</span></a>`},
		{"new tab spanish", spanish, `<a href="https://other.test">x</a>`,
			`<a href="https://other.test" rel="noopener noreferrer" target="_blank">x<span class="sr-only"> (se abre en una pestaña nueva)</span></a>`},
		{"existing target kept", withTab, `<a href="https://other.test" target="_self">x</a>`,
			`<a href="https://other.test" target="_self" rel="noopener noreferrer">x</a>`},
		{"existing blank target gets text", base, `<a href="https://other.test" target="_BLANK">x</a>`,
			`<a href="https://other.test" target="_BLANK" rel="noopener noreferrer">x<span class="sr-only"> (opens in a new tab)</span></a>`},
		{"internal link keeps no target", withTab, `<a href="https://example.com/a">a</a>`, ""},
		{"nested strong", withTab, `<a href="https://other.test"><strong>Bold</strong> link</a>`,
			`<a href="https://other.test" rel="noopener noreferrer" target="_blank"><strong>Bold</strong> link<span class="sr-only"> (opens in a new tab)</span></a>`},
		{"utm", utm, `<a href="https://partner.test/p">x</a>`,
			`<a href="https://partner.test/p?utm_source=example.com&amp;utm_medium=referral&amp;utm_campaign=fall+sale" rel="noopener noreferrer">x</a>`},
		{"utm existing query and fragment", utm, `<a href="https://shop.partner.test/p?id=7&amp;b=2#buy">x</a>`,
			`<a href="https://shop.partner.test/p?id=7&amp;b=2&amp;utm_source=example.com&amp;utm_medium=referral&amp;utm_campaign=fall+sale#buy" rel="noopener noreferrer">x</a>`},
		{"utm trailing question mark", utm, `<a href="https://partner2.test/?">x</a>`,
			`<a href="https://partner2.test/?utm_source=example.com&amp;utm_medium=referral&amp;utm_campaign=fall+sale" rel="noopener noreferrer">x</a>`},
		{"utm trailing ampersand", utm, `<a href="https://partner.test/?a=1&amp;">x</a>`,
			`<a href="https://partner.test/?a=1&amp;utm_source=example.com&amp;utm_medium=referral&amp;utm_campaign=fall+sale" rel="noopener noreferrer">x</a>`},
		{"utm existing source kept", utm, `<a href="https://partner.test/?utm_source=news&amp;x=%zz">x</a>`,
			`<a href="https://partner.test/?utm_source=news&amp;x=%zz&amp;utm_medium=referral&amp;utm_campaign=fall+sale" rel="noopener noreferrer">x</a>`},
		{"utm only listed domains", utm, `<a href="https://other.test/">x</a>`,
			`<a href="https://other.test/" rel="noopener noreferrer">x</a>`},
		{"idn site, punycode link", idnSite, `<a href="https://xn--bcher-kva.example/a">a</a>`, ""},
		{"idn site, unicode link", idnSite, `<a href="https://BÜCHER.example/a">a</a>`, ""},
		{"punycode site, unicode link", Config{SiteURL: "https://xn--bcher-kva.example"}, `<a href="https://bücher.example/">a</a>`, ""},
		{"idn external", base, `<a href="https://bücher.example/">a</a>`,
			`<a href="https://bücher.example/" rel="noopener noreferrer">a</a>`},
		{"unknown site makes every absolute link external", noSite, `<a href="https://example.com/">a</a> <a href="/x">x</a>`,
			`<a href="https://example.com/" rel="noopener noreferrer">a</a> <a href="/x">x</a>`},
		{"inside code and pre", base, `<pre><a href="https://other.test">x</a></pre><code><a href="https://other.test">y</a></code>`, ""},
		{"non-Latin text", base, `<p>Читайте <a href="https://другой.рф/статья">здесь</a> 日本語</p>`,
			`<p>Читайте <a href="https://другой.рф/статья" rel="noopener noreferrer">здесь</a> 日本語</p>`},
		{"malformed html", base, `<p>Unclosed <a href="https://other.test/x">link <em>text</p><div>`,
			`<p>Unclosed <a href="https://other.test/x" rel="noopener noreferrer">link <em>text</em></a></p><div></div>`},
		{"several links", base, `<ul><li><a href="https://a.test">a</a></li><li><a href="/b">b</a></li></ul>`,
			`<ul><li><a href="https://a.test" rel="noopener noreferrer">a</a></li><li><a href="/b">b</a></li></ul>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Filter(tc.in, tc.cfg)
			if tc.want == "" {
				if changed || got != tc.in {
					t.Fatalf("expected no change, got changed=%v\n%s", changed, got)
				}
				return
			}
			if !changed {
				t.Fatalf("expected a change, got none")
			}
			if got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
			// Running the filter on its own output changes nothing.
			if again, changed := Filter(got, tc.cfg); changed {
				t.Fatalf("not idempotent:\n%s", again)
			}
		})
	}
}

func TestHiddenTextAddedOnce(t *testing.T) {
	cfg := Config{SiteURL: site, NewTab: true}
	out, _ := Filter(`<a href="https://other.test">x</a>`, cfg)
	out, _ = Filter(out, cfg)
	if n := strings.Count(out, "sr-only"); n != 1 {
		t.Fatalf("hidden text added %d times: %s", n, out)
	}
	// An author-provided hidden text is respected.
	in := `<a href="https://other.test" target="_blank">x<span class="visually sr-only">(new window)</span></a>`
	out, _ = Filter(in, cfg)
	if strings.Count(out, "sr-only") != 1 || strings.Contains(out, "opens in") {
		t.Fatalf("existing hidden text not respected: %s", out)
	}
}

func TestParseDomains(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  \n , ", nil},
		{"Example.com", []string{"example.com"}},
		{"https://www.example.com/path?q=1, *.cdn.test:8443\nuser@mail.test", []string{"example.com", "cdn.test", "mail.test"}},
		{"bücher.example münchen.de", []string{"xn--bcher-kva.example", "xn--mnchen-3ya.de"}},
		{"a.test a.test A.TEST.", []string{"a.test"}},
		{"bad*domain [::1] <x>", nil},
	}
	for _, tc := range tests {
		got := ParseDomains(tc.in)
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("ParseDomains(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAddUTM(t *testing.T) {
	p := [][2]string{{"utm_source", "my site"}, {"utm_medium", ""}, {"utm_campaign", "ñ"}}
	tests := []struct{ in, want string }{
		{"https://a.test", "https://a.test?utm_source=my+site&utm_campaign=%C3%B1"},
		{"https://a.test/#", "https://a.test/?utm_source=my+site&utm_campaign=%C3%B1#"},
		{"https://a.test/?utm_source=x&utm_campaign=y", "https://a.test/?utm_source=x&utm_campaign=y"},
		{"https://a.test/?utm%5Fsource=x", "https://a.test/?utm%5Fsource=x&utm_campaign=%C3%B1"},
		{"https://a.test/p?q=a#frag?x", "https://a.test/p?q=a&utm_source=my+site&utm_campaign=%C3%B1#frag?x"},
	}
	for _, tc := range tests {
		if got := addUTM(tc.in, p); got != tc.want {
			t.Errorf("addUTM(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
