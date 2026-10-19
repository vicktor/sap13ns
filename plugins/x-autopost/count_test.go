package main

import (
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

func TestXLen(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{"Latin", "hola", 4},
		{"accents", "ñandú", 5},
		{"the en dash is narrow, the ellipsis is not", "a – b … c", 10},
		{"CJK weighs 2", "日本語", 6},
		{"emoji weigh at least 2", "🎉", 2},
		{"a long link counts 23", "https://example.test/" + strings.Repeat("x", 100), 23},
		{"a short link counts 23", "http://x.y", 23},
		{"text around links", "a https://example.test/long b", 1 + 1 + 23 + 1 + 1},
		{"a bare domain counts at least 23", "see example.com", 4 + 23},
		{"a long bare domain counts itself", "see " + strings.Repeat("a", 30) + ".com", 4 + 34},
		{"version numbers are no domain", "Go 1.27", 7},
		{"NFC expansions count wide", "क़", xExpands},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := xLen(tt.text); got != tt.want {
				t.Errorf("xLen(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestComposeFitsX(t *testing.T) {
	f := autopost.Fields{Title: strings.Repeat("palabra ", 40), URL: "https://example.test/" + strings.Repeat("a", 150),
		Hashtags: []string{"#Uno", "#Dos"}}
	text := autopost.Compose("{title}\n\n{url}\n\n{hashtags}", f, xText)
	if n := xLen(text); n > xLimit {
		t.Fatalf("%d > %d: %q", n, xLimit, text)
	}
	if !strings.Contains(text, "…\n\n"+f.URL+"\n\n#Uno #Dos") {
		t.Errorf("the link or the hashtags were cut: %q", text)
	}
	// The whole long URL fits: X counts it as 23.
	if n := len([]rune(text)); n <= xLimit {
		t.Errorf("the text could be longer: %d runes", n)
	}

	wide := autopost.Compose("{title}", autopost.Fields{Title: strings.Repeat("日本", 100)}, xText)
	if n := xLen(wide); n > xLimit || n < xLimit-4 {
		t.Errorf("CJK text: %d units: %q", n, wide)
	}
}
