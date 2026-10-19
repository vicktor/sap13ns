package autopost

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHashtags(t *testing.T) {
	tests := []struct {
		name  string
		tags  []string
		limit int
		want  []string
	}{
		{"single words keep their case", []string{"Go", "iOS", "café"}, 5, []string{"#Go", "#iOS", "#café"}},
		{"multi-word tags are CamelCased", []string{"open source", "inteligencia artificial", "ciencia-ficción"}, 5,
			[]string{"#OpenSource", "#InteligenciaArtificial", "#CienciaFicción"}},
		{"punctuation is removed", []string{"C++", "node.js", "¿Qué?", "año_2026"}, 5, []string{"#C", "#NodeJs", "#Qué", "#año_2026"}},
		{"non-Latin scripts", []string{"日本語", "русский язык", "ελληνικά"}, 5, []string{"#日本語", "#РусскийЯзык", "#ελληνικά"}},
		{"digits only are skipped", []string{"2026", "1_000", "web3"}, 5, []string{"#web3"}},
		{"duplicates ignoring case", []string{"Go", "go", "GO", "Open Source", "opensource"}, 5, []string{"#Go", "#OpenSource"}},
		{"empty and symbols", []string{"", "  ", "!!!", "♥"}, 5, nil},
		{"limit", []string{"a", "b", "c", "d"}, 2, []string{"#a", "#b"}},
		{"zero limit", []string{"a"}, 0, nil},
		{"overlong", []string{strings.Repeat("a", 65), strings.Repeat("ñ", 64)}, 5, []string{"#" + strings.Repeat("ñ", 64)}},
		{"combining marks stay", []string{"café"}, 5, []string{"#café"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Hashtags(tt.tags, tt.limit); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Hashtags(%q) = %q, want %q", tt.tags, got, tt.want)
			}
		})
	}
}

func TestURLsCountAs(t *testing.T) {
	l := URLsCountAs(23, Runes)
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
		if got := l(tt.text); got != tt.want {
			t.Errorf("len(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestComposeFitsTheLimit(t *testing.T) {
	short := Limit{Max: 300, Len: Runes}
	links := Limit{Max: 500, Len: URLsCountAs(23, Runes)}
	longURL := "https://example.test/" + strings.Repeat("a", 200)
	long := strings.Repeat("palabra ", 80) // 640 runes
	tags := []string{"#Uno", "#Dos", "#Tres"}
	tests := []struct {
		name  string
		tpl   string
		f     Fields
		limit Limit
		check func(t *testing.T, text string)
	}{
		{"short text is unchanged", "{title} {url}", Fields{Title: "Hola", URL: "https://example.test/h"}, short,
			func(t *testing.T, text string) {
				if text != "Hola https://example.test/h" {
					t.Errorf("%q", text)
				}
			}},
		{"excerpt is shortened first", "{title}\n\n{excerpt}\n\n{url}\n\n{hashtags}", Fields{Title: "Título", Excerpt: long, URL: "https://example.test/p", Hashtags: tags}, short,
			func(t *testing.T, text string) {
				if !strings.HasPrefix(text, "Título\n\npalabra") || !strings.Contains(text, "…\n\nhttps://example.test/p\n\n#Uno #Dos #Tres") {
					t.Errorf("%q", text)
				}
			}},
		{"then the title", "{title}\n\n{url}\n\n{hashtags}", Fields{Title: long, URL: "https://example.test/p", Hashtags: tags}, short,
			func(t *testing.T, text string) {
				if !strings.Contains(text, "…\n\nhttps://example.test/p\n\n#Uno #Dos #Tres") {
					t.Errorf("%q", text)
				}
			}},
		{"a long URL counted as 23", "{title}\n{url}", Fields{Title: strings.Repeat("x", 470), URL: longURL}, links,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, longURL) || !strings.HasPrefix(text, strings.Repeat("x", 470)) {
					t.Errorf("the title was cut although it fits: %q", text)
				}
			}},
		{"the whole URL counted", "{title}\n{url}", Fields{Title: strings.Repeat("x", 200), URL: longURL}, short,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "\n"+longURL) {
					t.Errorf("the URL was cut: %q", text)
				}
			}},
		{"hashtags are dropped from the end", "{url} {hashtags}", Fields{URL: "https://example.test/" + strings.Repeat("a", 260), Hashtags: []string{"#Uno", "#Dos", "#Tres", "#Cuatro", "#Cinco", "#Seis", "#Siete", "#Ocho"}}, short,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "a #Uno #Dos #Tres") { // 281 + 16 runes; #Cuatro would make 305
					t.Errorf("%q", text)
				}
			}},
		{"fixed text too long keeps whole words", strings.Repeat("texto ", 60) + "{url}", Fields{URL: "https://example.test/p"}, short,
			func(t *testing.T, text string) {
				if !strings.HasSuffix(text, "texto…") {
					t.Errorf("%q", text)
				}
			}},
		{"emoji count as one rune", "{title}", Fields{Title: strings.Repeat("🎉", 300)}, short,
			func(t *testing.T, text string) {
				if text != strings.Repeat("🎉", 300) {
					t.Errorf("%d runes", utf8.RuneCountInString(text))
				}
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := Compose(tt.tpl, tt.f, tt.limit)
			if n := tt.limit.Len(text); n > tt.limit.Max {
				t.Fatalf("%d > %d: %q", n, tt.limit.Max, text)
			}
			if !utf8.ValidString(text) {
				t.Fatalf("invalid UTF-8: %q", text)
			}
			tt.check(t, text)
		})
	}
}

func TestRenderTemplate(t *testing.T) {
	tests := []struct {
		tpl  string
		f    Fields
		want string
	}{
		{"{title}\n\n{excerpt}\n\n{url}", Fields{Title: "T", URL: "U"}, "T\n\nU"},
		{"{title}\r\n{url}   \n\n\n\n{hashtags}", Fields{Title: "T", URL: "U", Hashtags: []string{"#a", "#b"}}, "T\nU\n\n#a #b"},
		{"  {site}: {title}  ", Fields{Title: "T", Site: "S"}, "S: T"},
		{"{title} {unknown}", Fields{Title: "T"}, "T {unknown}"},
	}
	for _, tt := range tests {
		if got := RenderTemplate(tt.tpl, tt.f); got != tt.want {
			t.Errorf("RenderTemplate(%q) = %q, want %q", tt.tpl, got, tt.want)
		}
	}
}

func TestComposeKeepsTitleOnOneLine(t *testing.T) {
	got := Compose("{title}\n{url}", Fields{Title: "Uno\n\ndos\tTres", URL: "https://example.test/"}, Limit{Max: 300, Len: Runes})
	if got != "Uno dos Tres\nhttps://example.test/" {
		t.Errorf("%q", got)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s    string
		n    int
		want string
	}{
		{"corto", 10, "corto"},
		{"una frase bastante larga", 12, "una frase…"},
		{"ñññññññññ", 5, "ññññ…"},
	}
	for _, tt := range tests {
		got := Truncate(tt.s, tt.n)
		if got != tt.want || utf8.RuneCountInString(got) > tt.n {
			t.Errorf("Truncate(%q, %d) = %q, want %q", tt.s, tt.n, got, tt.want)
		}
	}
}

func TestValidTag(t *testing.T) {
	for tag, want := range map[string]bool{"Go": true, "2026": false, "1_000": false, "": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false} {
		if ValidTag(tag) != want {
			t.Errorf("ValidTag(%q) = %v", tag, !want)
		}
	}
}
