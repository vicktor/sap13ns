package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

func TestLittle(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Hola, ¿qué tal?", "Hola, ¿qué tal?"},
		{"C++ (y Go) <3 *nuevo* ~ya~ a_b @ana [x] {y} | \\", `C++ \(y Go\) \<3 \*nuevo\* \~ya\~ a\_b \@ana \[x\] \{y\} \| \\`},
		{"#OpenSource #Go", `{hashtag|\#|OpenSource} {hashtag|\#|Go}`},
		{"Fin.\n\n#año_2026 #Málaga", "Fin.\n\n" + `{hashtag|\#|año\_2026} {hashtag|\#|Málaga}`},
		{"no#tag, # alone, #2026", `no\#tag, \# alone, \#2026`},
		{"https://example.test/a_b(c)", `https://example.test/a\_b\(c\)`},
	}
	for _, tt := range tests {
		if got := Little(tt.in); got != tt.want {
			t.Errorf("Little(%q)\n= %q\nwant %q", tt.in, got, tt.want)
		}
	}
}

func TestCommentaryFits(t *testing.T) {
	f := autopost.Fields{Title: "Título (con paréntesis)", Excerpt: strings.Repeat("palabra_clave * ", 400), Hashtags: []string{"#Uno", "#Dos"}}
	text := Little(autopost.Compose(defaultTemplate("es", "post"), f, commentaryLimit))
	if n := utf8.RuneCountInString(text); n > 3000 {
		t.Fatalf("%d characters", n)
	}
	if !strings.HasPrefix(text, `Nueva entrada: Título \(con paréntesis\)`) || !strings.HasSuffix(text, `…`+"\n\n"+`{hashtag|\#|Uno} {hashtag|\#|Dos}`) {
		t.Errorf("%q", text[:80]+" … "+text[len(text)-80:])
	}
}
