package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Signal is one reason to hold or reject a comment.
type Signal struct {
	Verdict string // pending | spam
	Text    string
}

// Comment is what the local checks look at.
type Comment struct {
	Author, URL, Content, Email string
	Registered                  bool
	Known                       bool // the e-mail address had a comment approved before (host-checked)
}

var linkRe = regexp.MustCompile(`(?i)\bhttps?://\S+|\bwww\.\S+`)

// Check runs the local checks and returns every signal, strictest first.
func Check(c Comment, cfg Config) []Signal {
	var out []Signal
	add := func(v, format string, args ...any) { out = append(out, Signal{v, fmt.Sprintf(format, args...)}) }

	links := len(linkRe.FindAllStringIndex(c.Content, -1))
	site := ""
	if strings.TrimSpace(c.URL) != "" {
		site = ", plus a website address"
	}
	switch {
	case cfg.SpamLinks > 0 && links >= cfg.SpamLinks:
		add(verdictSpam, "%s%s (spam from %d)", plural(links, "link"), site, cfg.SpamLinks)
	case links > cfg.MaxLinks:
		add(verdictHold, "%s%s (more than %d)", plural(links, "link"), site, cfg.MaxLinks)
	case links > 0 && cfg.FirstTimeLinks && !c.Registered && !c.Known:
		add(verdictHold, "first comment with a link")
	}

	for _, field := range []struct{ name, text string }{
		{"content", c.Content}, {"name", c.Author}, {"website", c.URL}, {"e-mail address", c.Email},
	} {
		if e, ok := matchBlocklist(cfg.Blocklist, field.text); ok {
			add(cfg.BlockAction, "blocklist entry “%s” in the %s", e.Text, field.name)
		}
	}

	text := linkRe.ReplaceAllString(c.Content, " ")
	if cfg.Script != scriptOff {
		if w := mixedWord(text + " " + c.Author); w != "" {
			add(verdictHold, "a word mixes Latin with Cyrillic or Greek letters (“%s”)", w)
		}
	}
	if cfg.Script == scriptLatin {
		if pct, ok := mostlyNonLatin(text); ok {
			add(verdictHold, "%d%% of the letters are not Latin", pct)
		}
	}
	if cfg.Caps {
		if pct, ok := shouting(text); ok {
			add(verdictHold, "%d%% capital letters", pct)
		}
	}
	if cfg.Repeat {
		if s := repeated(text); s != "" {
			add(verdictHold, "%s", s)
		}
	}

	// Strictest first, keeping the order of the checks.
	var spam, hold []Signal
	for _, s := range out {
		if s.Verdict == verdictSpam {
			spam = append(spam, s)
		} else {
			hold = append(hold, s)
		}
	}
	return append(spam, hold...)
}

// Decide combines the signals: spam beats pending beats ok, and the reason
// lists every signal.
func Decide(signals []Signal) (verdict, reason string) {
	verdict = verdictOK
	var parts []string
	for _, s := range signals {
		if s.Verdict == verdictSpam || verdict == verdictOK {
			verdict = s.Verdict
		}
		parts = append(parts, s.Text)
	}
	return verdict, strings.Join(parts, "; ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Fold lowercases s and removes accents and compatibility forms, so that
// "VÍAGRA", "viagra" and "ｖｉａｇｒａ" compare equal. Whitespace is collapsed.
func Fold(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// matchBlocklist finds the first entry in text. A phrase matches anywhere;
// a single word, domain or address must not touch other letters or digits
// ("sex" does not match "Essex").
func matchBlocklist(entries []Entry, text string) (Entry, bool) {
	if len(entries) == 0 || strings.TrimSpace(text) == "" {
		return Entry{}, false
	}
	f := Fold(text)
	for _, e := range entries {
		if e.Phrase {
			if strings.Contains(f, e.Folded) {
				return e, true
			}
			continue
		}
		for from := 0; from <= len(f); {
			i := strings.Index(f[from:], e.Folded)
			if i < 0 {
				break
			}
			i += from
			j := i + len(e.Folded)
			first, _ := utf8.DecodeRuneInString(e.Folded)
			last, _ := utf8.DecodeLastRuneInString(e.Folded)
			before, _ := utf8.DecodeLastRuneInString(f[:i])
			after, _ := utf8.DecodeRuneInString(f[j:])
			if (i == 0 || !wordRune(first) || !wordRune(before)) && (j == len(f) || !wordRune(last) || !wordRune(after)) {
				return e, true
			}
			_, size := utf8.DecodeRuneInString(f[i:])
			from = i + size
		}
	}
	return Entry{}, false
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsMark(r) })
}

// mixedWord returns a word written with Latin and Cyrillic or Greek
// letters ("vіagra" with a Cyrillic і), a usual trick to dodge filters.
func mixedWord(s string) string {
	for _, w := range words(s) {
		latin, other := false, false
		for _, r := range w {
			switch {
			case unicode.Is(unicode.Latin, r):
				latin = true
			case unicode.Is(unicode.Cyrillic, r), unicode.Is(unicode.Greek, r):
				other = true
			}
		}
		if latin && other {
			if r := []rune(w); len(r) > 30 {
				w = string(r[:30]) + "…"
			}
			return w
		}
	}
	return ""
}

// mostlyNonLatin reports texts of at least 10 letters with more than half
// of them in other scripts.
func mostlyNonLatin(s string) (int, bool) {
	letters, other := 0, 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if !unicode.Is(unicode.Latin, r) {
			other++
		}
	}
	if letters < 10 || other*2 <= letters {
		return 0, false
	}
	return other * 100 / letters, true
}

// shouting reports texts of at least 20 cased letters with 70% or more in
// capitals.
func shouting(s string) (int, bool) {
	cased, upper := 0, 0
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			upper++
			cased++
		case unicode.IsLower(r):
			cased++
		}
	}
	if cased < 20 || upper*10 < cased*7 {
		return 0, false
	}
	return upper * 100 / cased, true
}

// repeated finds a character repeated 8 times or more ("!!!!!!!!") or a
// word repeated 6 times or more in a row.
func repeated(s string) string {
	var prev rune
	run := 0
	for _, r := range s {
		if r == prev && !unicode.IsSpace(r) {
			run++
		} else {
			prev, run = r, 1
		}
		if run == 8 {
			return fmt.Sprintf("a character repeated 8 times or more (“%s”)", strings.Repeat(string(r), 3)+"…")
		}
	}
	ws := strings.Fields(Fold(s))
	count := 0
	for i := range ws {
		ws[i] = strings.TrimFunc(ws[i], func(r rune) bool { return !wordRune(r) })
		if i > 0 && ws[i] != "" && ws[i] == ws[i-1] {
			count++
		} else {
			count = 1
		}
		if count == 6 {
			w := ws[i]
			if r := []rune(w); len(r) > 30 {
				w = string(r[:30]) + "…"
			}
			return fmt.Sprintf("the word “%s” 6 times or more in a row", w)
		}
	}
	return ""
}
