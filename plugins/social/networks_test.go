package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

func TestMastodonLen(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"hola", 4},
		{"ñandú", 5},
		{"https://example.test/" + strings.Repeat("x", 100), 23},
		{"a http://x.y b https://example.test/long/path?q=1", 2 + 23 + 3 + 23},
	}
	for _, tt := range tests {
		if got := mastodonLimit.Len(tt.text); got != tt.want {
			t.Errorf("Mastodon length of %q = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestComposeFitsTheLimits(t *testing.T) {
	longURL := "https://example.test/" + strings.Repeat("a", 200)
	long := strings.Repeat("palabra ", 80) // 640 runes
	tags := []string{"#Uno", "#Dos", "#Tres"}
	tests := []struct {
		name  string
		tpl   string
		f     autopost.Fields
		net   autopost.Limit
		check func(t *testing.T, text string)
	}{
		{"short text is unchanged", "{title} {url}", autopost.Fields{Title: "Hola", URL: "https://example.test/h"}, blueskyLimit,
			func(t *testing.T, text string) {
				if text != "Hola https://example.test/h" {
					t.Errorf("%q", text)
				}
			}},
		{"excerpt is shortened first", "{title}\n\n{excerpt}\n\n{url}\n\n{hashtags}", autopost.Fields{Title: "Título", Excerpt: long, URL: "https://example.test/p", Hashtags: tags}, blueskyLimit,
			func(t *testing.T, text string) {
				if !strings.HasPrefix(text, "Título\n\npalabra") || !strings.Contains(text, "…\n\nhttps://example.test/p\n\n#Uno #Dos #Tres") {
					t.Errorf("%q", text)
				}
			}},
		{"then the title", "{title}\n\n{url}\n\n{hashtags}", autopost.Fields{Title: long, URL: "https://example.test/p", Hashtags: tags}, blueskyLimit,
			func(t *testing.T, text string) {
				if !strings.Contains(text, "…\n\nhttps://example.test/p\n\n#Uno #Dos #Tres") {
					t.Errorf("%q", text)
				}
			}},
		{"Mastodon counts a long URL as 23", "{title}\n{url}", autopost.Fields{Title: strings.Repeat("x", 470), URL: longURL}, mastodonLimit,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, longURL) || !strings.HasPrefix(text, strings.Repeat("x", 470)) {
					t.Errorf("the title was cut although it fits: %q", text)
				}
			}},
		{"Bluesky counts the whole URL", "{title}\n{url}", autopost.Fields{Title: strings.Repeat("x", 200), URL: longURL}, blueskyLimit,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "\n"+longURL) {
					t.Errorf("the URL was cut: %q", text)
				}
			}},
		{"hashtags are dropped from the end", "{url} {hashtags}", autopost.Fields{URL: "https://example.test/" + strings.Repeat("a", 260), Hashtags: []string{"#Uno", "#Dos", "#Tres", "#Cuatro", "#Cinco", "#Seis", "#Siete", "#Ocho"}}, blueskyLimit,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "a #Uno #Dos #Tres") { // 281 + 16 runes; #Cuatro would make 305
					t.Errorf("%q", text)
				}
			}},
		{"fixed text too long keeps whole words", strings.Repeat("texto ", 60) + "{url}", autopost.Fields{URL: "https://example.test/p"}, blueskyLimit,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "texto…") {
					t.Errorf("%q", text)
				}
			}},
		{"emoji count as one", "{title}", autopost.Fields{Title: strings.Repeat("🎉", 300)}, blueskyLimit,
			func(t *testing.T, text string) {
				if text != strings.Repeat("🎉", 300) {
					t.Errorf("%d runes", utf8.RuneCountInString(text))
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := autopost.Compose(tt.tpl, tt.f, tt.net)
			if n := tt.net.Len(text); n > tt.net.Max {
				t.Fatalf("%d > %d: %q", n, tt.net.Max, text)
			}
			if !utf8.ValidString(text) {
				t.Fatalf("invalid UTF-8: %q", text)
			}
			tt.check(t, text)
		})
	}
}

func TestFacetsUseByteOffsets(t *testing.T) {
	text := "¡Olé! 🎉 Mira https://example.test/ñandú?a=1. y (https://example.test/x_(y)) #Café #2026 #año_nuevo fin#no"
	facets := Facets(text)
	var got []string
	for _, f := range facets {
		got = append(got, f.Features[0].Type[len("app.bsky.richtext.facet"):]+" "+text[f.Index.ByteStart:f.Index.ByteEnd])
		if ft := f.Features[0]; ft.URI != "" && ft.URI != text[f.Index.ByteStart:f.Index.ByteEnd] ||
			ft.Tag != "" && "#"+ft.Tag != text[f.Index.ByteStart:f.Index.ByteEnd] {
			t.Errorf("feature %+v does not match its range", ft)
		}
	}
	want := []string{
		"#link https://example.test/ñandú?a=1",
		"#link https://example.test/x_(y)",
		"#tag #Café",
		"#tag #año_nuevo",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facets:\n%q\nwant\n%q", got, want)
	}
	if start := facets[0].Index.ByteStart; start != strings.Index(text, "https://") || start == utf8.RuneCountInString(text[:start]) {
		t.Errorf("offset %d is not a byte offset", start)
	}
}

func TestBlueskyLink(t *testing.T) {
	tests := map[string]string{
		"at://did:plc:abc/app.bsky.feed.post/3kxyz": "bsky.app/profile/did:plc:abc/post/3kxyz",
		"at://did:plc:abc/app.bsky.feed.like/3kxyz": "",
		"https://bsky.app/x":                        "",
		"":                                          "",
	}
	for uri, want := range tests {
		if got := blueskyLink(uri); got != want {
			t.Errorf("blueskyLink(%q) = %q, want %q", uri, got, want)
		}
	}
}

func TestFetchProblem(t *testing.T) {
	ok := &pluginsdk.HTTPResponse{Status: 201, Body: "{}"}
	tests := []struct {
		name   string
		resp   *pluginsdk.HTTPResponse
		err    error
		bad    bool
		failed bool
		detail string
	}{
		{"success", ok, nil, false, false, ""},
		{"denied host", nil, pluginsdk.ErrDenied, true, true, "the host mastodon.example is not in this plugin's permissions (http:mastodon.example)"},
		{"call limit", nil, pluginsdk.ErrLimit, true, true, ""},
		{"DNS failure", &pluginsdk.HTTPResponse{}, errors.New(`dial tcp: lookup mastodon.example: no such host`), true, true, "no such host"},
		{"private address", &pluginsdk.HTTPResponse{}, errors.New(`dial tcp 10.0.0.1:443: destination address is not allowed`), true, true, "not allowed"},
		{"timeout is unknown", &pluginsdk.HTTPResponse{}, errors.New(`context deadline exceeded`), true, false, "no answer (context deadline exceeded)"},
		{"unauthorized", &pluginsdk.HTTPResponse{Status: 401, Body: `{"error":"The access token is invalid"}`}, nil, true, true, "HTTP 401: The access token is invalid"},
		{"Bluesky error", &pluginsdk.HTTPResponse{Status: 400, Body: `{"error":"InvalidRequest","message":"Input/text must not be longer than 300 graphemes"}`}, nil, true, true,
			"HTTP 400: InvalidRequest: Input/text must not be longer than 300 graphemes"},
		{"HTML error page is not logged", &pluginsdk.HTTPResponse{Status: 502, Body: "<html>secret-ish page</html>"}, nil, true, true, "HTTP 502"},
		{"rate limited", &pluginsdk.HTTPResponse{Status: 429, Body: `{"error":"Too many requests"}`}, nil, true, true, "HTTP 429: Too many requests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, bad := fetchProblem(tt.resp, tt.err, "https://mastodon.example")
			if bad != tt.bad || r.Failed != tt.failed || r.Done || !strings.Contains(r.Detail, tt.detail) {
				t.Errorf("got %+v, %v", r, bad)
			}
			if strings.Contains(r.Detail, "<html>") {
				t.Errorf("the body leaked into the log: %q", r.Detail)
			}
		})
	}
}
