package main

import (
	"strings"
	"testing"
)

func cfgWith(s map[string]string) Config { return ConfigFrom(s) }

func TestCheck(t *testing.T) {
	block := "viagra\ncasino.example\nbuy cheap\n# a note, not an entry\n@spam.example\nsex"
	tests := []struct {
		name     string
		settings map[string]string
		c        Comment
		verdict  string
		reason   string
	}{
		{"clean", nil, Comment{Author: "Ana", Content: "Muy buen artículo, gracias."}, "ok", ""},
		{"one link from a known commenter", nil, Comment{Author: "Ana", Known: true, Content: "Ver https://example.org/a"}, "ok", ""},
		{"one link from a signed-in user", nil, Comment{Author: "Ana", Registered: true, Content: "Ver www.example.org"}, "ok", ""},
		{"first comment with a link", nil, Comment{Author: "Ana", Content: "Ver https://example.org/a"}, "pending", "first comment with a link"},
		{"first comment check off", map[string]string{"first_time_links": "false"}, Comment{Author: "Ana", Content: "Ver https://example.org/a"}, "ok", ""},
		{"three links", nil, Comment{Known: true, Content: "https://a.example http://b.example www.c.example"}, "pending", "3 links (more than 2)"},
		{"links with a website", nil, Comment{Known: true, URL: "https://me.example", Content: "https://a.example https://b.example https://c.example"},
			"pending", "3 links, plus a website address (more than 2)"},
		{"six links are spam", nil, Comment{Known: true, Content: strings.Repeat("https://a.example/x ", 6)}, "spam", "6 links (spam from 6)"},
		{"spam links off", map[string]string{"spam_links": "0"}, Comment{Known: true, Content: strings.Repeat("https://a.example/x ", 9)}, "pending", "9 links (more than 2)"},
		{"max links 0", map[string]string{"max_links": "0"}, Comment{Registered: true, Content: "www.example.org"}, "pending", "1 link (more than 0)"},
		{"the website alone is not a link", nil, Comment{URL: "https://me.example", Content: "Hola"}, "ok", ""},

		{"blocklist word", map[string]string{"blocklist": block}, Comment{Content: "Compra VÍAGRA hoy"}, "spam", "blocklist entry “viagra” in the content"},
		{"blocklist full-width", map[string]string{"blocklist": block}, Comment{Content: "ｖｉａｇｒａ"}, "spam", "blocklist entry “viagra” in the content"},
		{"blocklist whole words only", map[string]string{"blocklist": block}, Comment{Content: "Vivo en Essex y Sussex"}, "ok", ""},
		{"blocklist domain in the website", map[string]string{"blocklist": block}, Comment{URL: "https://www.casino.example/promo", Content: "Hola"},
			"spam", "blocklist entry “casino.example” in the website"},
		{"blocklist phrase across spaces and case", map[string]string{"blocklist": block}, Comment{Content: "You can BUY   cheap\nwatches"},
			"spam", "blocklist entry “buy cheap” in the content"},
		{"blocklist phrase is a substring", map[string]string{"blocklist": block}, Comment{Content: "ebuy cheaper"}, "spam", "blocklist entry “buy cheap”"},
		{"blocklist e-mail domain", map[string]string{"blocklist": block}, Comment{Email: "bob@spam.example", Content: "Hola"},
			"spam", "blocklist entry “@spam.example” in the e-mail address"},
		{"blocklist name", map[string]string{"blocklist": block}, Comment{Author: "Viagra Shop", Content: "Hola"}, "spam", "blocklist entry “viagra” in the name"},
		{"blocklist as pending", map[string]string{"blocklist": block, "blocklist_action": "pending"}, Comment{Content: "viagra"}, "pending", "blocklist entry “viagra”"},
		{"a note is not an entry", map[string]string{"blocklist": block}, Comment{Content: "a note, not an entry"}, "ok", ""},

		{"mixed script off by default", nil, Comment{Content: "Buy vіagra now"}, "ok", ""},
		{"mixed script word", map[string]string{"script_check": "mixed"}, Comment{Content: "Buy vіagra now"}, "pending",
			"a word mixes Latin with Cyrillic or Greek letters (“vіagra”)"},
		{"mixed script in the name", map[string]string{"script_check": "mixed"}, Comment{Author: "Аnna", Content: "Hola"}, "pending", "a word mixes Latin"},
		{"Cyrillic alone is fine in mixed mode", map[string]string{"script_check": "mixed"}, Comment{Content: "Очень хорошая статья, спасибо"}, "ok", ""},
		{"mostly non-Latin in latin mode", map[string]string{"script_check": "latin"}, Comment{Content: "Очень хорошая статья, спасибо"}, "pending",
			"100% of the letters are not Latin"},
		{"short non-Latin text passes", map[string]string{"script_check": "latin"}, Comment{Content: "спасибо!"}, "ok", ""},
		{"Spanish passes latin mode", map[string]string{"script_check": "latin"}, Comment{Content: "¡Qué artículo tan útil! Señalo un error en el título."}, "ok", ""},

		{"shouting", nil, Comment{Content: "THIS IS THE BEST ARTICLE EVER WRITTEN"}, "pending", "100% capital letters"},
		{"short shouting passes", nil, Comment{Content: "GRACIAS!!"}, "ok", ""},
		{"acronyms pass", nil, Comment{Content: "La API de la UE y la OTAN usan JSON y HTTP según el texto."}, "ok", ""},
		{"URLs do not count as capitals", nil, Comment{Known: true, Content: "ok https://EXAMPLE.ORG/ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, "ok", ""},
		{"caps check off", map[string]string{"caps_check": "false"}, Comment{Content: "THIS IS THE BEST ARTICLE EVER WRITTEN"}, "ok", ""},

		{"repeated character", nil, Comment{Content: "Great!!!!!!!!"}, "pending", "a character repeated 8 times or more (“!!!…”)"},
		{"seven is fine", nil, Comment{Content: "Great!!!!!!!"}, "ok", ""},
		{"repeated word", nil, Comment{Content: "buy buy buy, BUY buy buy now"}, "pending", "the word “buy” 6 times or more in a row"},
		{"repeats inside URLs are ignored", nil, Comment{Known: true, Content: "https://example.org/aaaaaaaaaaaa"}, "ok", ""},
		{"repeat check off", map[string]string{"repeat_check": "false"}, Comment{Content: "Great!!!!!!!!"}, "ok", ""},

		{"every signal is listed, spam first", map[string]string{"blocklist": block}, Comment{Content: "VIAGRA VIAGRA VIAGRA VIAGRA VIAGRA VIAGRA https://x.example"},
			"spam", "blocklist entry “viagra” in the content; first comment with a link; 100% capital letters; the word “viagra” 6 times or more in a row"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, reason := Decide(Check(tt.c, cfgWith(tt.settings)))
			if v != tt.verdict || !strings.Contains(reason, tt.reason) || tt.verdict == "ok" && reason != "" {
				t.Errorf("got %s %q, want %s %q", v, reason, tt.verdict, tt.reason)
			}
		})
	}
}

func TestFold(t *testing.T) {
	tests := map[string]string{
		"VÍAGRA":            "viagra",
		"Ñandú  Çava\tbien": "nandu cava bien",
		"ｖｉａｇｒａ":            "viagra",
		"ﬁne":               "fine",
		"  ":                "",
	}
	for in, want := range tests {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseBlocklist(t *testing.T) {
	entries, dropped := ParseBlocklist("  Viagra \n\n# note\nviagra\nbuy  cheap\n" + strings.Repeat("x", 150))
	if dropped != 0 || len(entries) != 3 {
		t.Fatalf("%+v, %d", entries, dropped)
	}
	if entries[0].Folded != "viagra" || entries[0].Text != "Viagra" || entries[0].Phrase ||
		entries[1].Folded != "buy cheap" || !entries[1].Phrase || len(entries[2].Folded) != maxEntryLen {
		t.Errorf("%+v", entries)
	}
	var b strings.Builder
	for i := range maxEntries + 5 {
		b.WriteString("w" + strings.Repeat("a", i%50) + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + "\n")
	}
	entries, dropped = ParseBlocklist(b.String())
	if len(entries) != maxEntries || dropped == 0 {
		t.Errorf("%d entries, %d dropped", len(entries), dropped)
	}
}

func TestConfigWarnings(t *testing.T) {
	c := ConfigFrom(map[string]string{"max_links": "-1", "spam_links": "x", "sfs_threshold": "150", "script_check": "bogus", "blocklist_action": "delete"})
	if c.MaxLinks != 2 || c.SpamLinks != 6 || c.SFSThreshold != 60 || c.Script != "off" || c.BlockAction != "spam" || len(c.Warnings) != 3 {
		t.Errorf("%+v", c)
	}
	d := ConfigFrom(map[string]string{})
	if !d.Caps || !d.Repeat || !d.FirstTimeLinks || !d.LogOK || d.SFS || !d.SFSIP || !d.SFSEmail || len(d.Warnings) != 0 {
		t.Errorf("defaults: %+v", d)
	}
}
