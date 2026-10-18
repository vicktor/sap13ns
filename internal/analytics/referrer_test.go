package analytics

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReferrerSource(t *testing.T) {
	internal := map[string]bool{"alias.example.org": true}
	cases := []struct {
		url, host, referer string
		want               string
		ok                 bool
	}{
		{"/", "example.com", "", direct, true},
		{"/", "example.com", "https://www.google.com/", "google.com", true},
		{"/", "example.com", "https://WWW.DuckDuckGo.com./?q=x", "duckduckgo.com", true},
		{"/", "example.com", "https://news.ycombinator.com/item?id=1", "news.ycombinator.com", true},
		{"/", "example.com", "http://t.co:80/abc", "t.co", true},
		{"/", "example.com", "android-app://com.google.android.gm/", "com.google.android.gm", true},
		// Same site, including www and port variants and configured aliases.
		{"/", "example.com", "https://example.com/blog", "", false},
		{"/", "example.com:8137", "http://example.com:8137/blog", "", false},
		{"/", "www.example.com", "https://example.com/", "", false},
		{"/", "example.com", "https://www.example.com/", "", false},
		{"/", "example.com", "https://alias.example.org/", "", false},
		// Unusable referrers count as direct.
		{"/", "example.com", "about:blank", direct, true},
		{"/", "example.com", "not a url %%", direct, true},
		{"/", "example.com", "https:///path-only", direct, true},
		// Campaign parameters win over the Referer.
		{"/?utm_source=Newsletter", "example.com", "https://mail.google.com/", "newsletter", true},
		{"/?ref=HN&x=1", "example.com", "", "hn", true},
		{"/?utm_source=Mastodon", "example.com", "https://example.com/", "mastodon", true},
		{"/?utm_source=%20", "example.com", "https://bing.com/", "bing.com", true},
		{"/?utm_source=a%00b%0Ac", "example.com", "", "abc", true},
		{"/?utm_source=" + strings.Repeat("x", 100), "example.com", "", strings.Repeat("x", maxSourceLen), true},
		{"/?utm_source=" + strings.Repeat("é", 70), "example.com", "", strings.Repeat("é", maxSourceLen), true},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "http://"+tc.host+tc.url, nil)
		r.Host = tc.host
		if tc.referer != "" {
			r.Header.Set("Referer", tc.referer)
		}
		got, ok := referrerSource(r, internal)
		if got != tc.want || ok != tc.ok {
			t.Errorf("referrerSource(%s%s, Referer %q) = %q, %v; want %q, %v", tc.host, tc.url, tc.referer, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCleanPath(t *testing.T) {
	cases := map[string]string{
		"":                             "/",
		"/":                            "/",
		"/blog/hello":                  "/blog/hello",
		"/search?q=secret":             "/search",
		"/page#top":                    "/page",
		"/a\x00b":                      "/ab",
		"/caf\xe9":                     "/caf",
		"/es/" + "ñ":                   "/es/ñ",
		"/" + strings.Repeat("ñ", 200): "/" + strings.Repeat("ñ", 127),
	}
	for in, want := range cases {
		if got := cleanPath(in); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", in, got, want)
		}
		if got := cleanPath(in); len(got) > maxPathLen {
			t.Errorf("cleanPath(%q) has %d bytes", in, len(got))
		}
	}
}

func TestCleanLang(t *testing.T) {
	cases := map[string]string{"": "", "es": "es", " EN ": "en", "pt-BR": "pt-br", "x<y": "", "abcdefghijklmnopq": ""}
	for in, want := range cases {
		if got := cleanLang(in); got != want {
			t.Errorf("cleanLang(%q) = %q, want %q", in, got, want)
		}
	}
}
