package main

import (
	"fmt"
	"hash/fnv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// maxSlugLen keeps ids short enough for URLs and the host's id rules.
const maxSlugLen = 80

// translit maps letters that do not decompose into ASCII under NFKD.
// Greek and Cyrillic follow common romanizations (ELOT 743, BGN/PCGN-like).
var translit = map[rune]string{
	// Latin
	'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'đ': "d", 'ð': "d", 'þ': "th",
	'ł': "l", 'ı': "i", 'ŋ': "ng", 'ħ': "h", 'ĸ': "k", 'ŧ': "t", 'ſ': "s", 'ƒ': "f",
	// Greek
	'α': "a", 'β': "v", 'γ': "g", 'δ': "d", 'ε': "e", 'ζ': "z", 'η': "i", 'θ': "th",
	'ι': "i", 'κ': "k", 'λ': "l", 'μ': "m", 'ν': "n", 'ξ': "x", 'ο': "o", 'π': "p",
	'ρ': "r", 'σ': "s", 'ς': "s", 'τ': "t", 'υ': "y", 'φ': "f", 'χ': "ch", 'ψ': "ps",
	'ω': "o",
	// Cyrillic (Russian, Ukrainian, Belarusian, Serbian, Macedonian, Bulgarian)
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu",
	'я': "ya", 'є': "ye", 'і': "i", 'ї': "yi", 'ґ': "g", 'ў': "u", 'ђ': "dj", 'ј': "j",
	'љ': "lj", 'њ': "nj", 'ћ': "c", 'џ': "dz", 'ѓ': "gj", 'ќ': "kj", 'ѕ': "dz",
}

// slugify turns heading text into an ASCII id: letters are transliterated
// (á→a, ß→ss, ж→zh, λ→l), apostrophes vanish and every other run of
// characters becomes one hyphen. Text with nothing transliterable (Chinese,
// Arabic, emoji…) gets a stable id derived from a hash of the text.
func slugify(text string) string {
	var b strings.Builder
	pendingDash := false
	write := func(s string) {
		if s == "" {
			return
		}
		if pendingDash && b.Len() > 0 {
			b.WriteByte('-')
		}
		pendingDash = false
		b.WriteString(s)
	}
	one := func(r rune) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			write(string(r))
		case unicode.Is(unicode.Mn, r):
			// combining accents left over by the decomposition
		case r == '\'' || r == '’' || r == 'ʼ':
			// "don't" → "dont"
		default:
			if t, ok := translit[r]; ok {
				write(t)
			} else {
				pendingDash = true
			}
		}
	}
	for _, r := range norm.NFC.String(text) {
		if b.Len() >= maxSlugLen {
			break
		}
		r = unicode.ToLower(r)
		// Precomposed letters with their own romanization (й→y, ё→yo)
		// win over their decomposition (и + breve → i).
		if t, ok := translit[r]; ok {
			write(t)
			continue
		}
		for _, d := range norm.NFKD.String(string(r)) {
			one(unicode.ToLower(d))
		}
	}
	s := b.String()
	if len(s) > maxSlugLen {
		s = s[:maxSlugLen]
	}
	s = strings.Trim(s, "-")
	if s == "" {
		h := fnv.New32a()
		h.Write([]byte(strings.TrimSpace(text)))
		return fmt.Sprintf("section-%08x", h.Sum32())
	}
	return s
}

// uniqueID returns base, or base-2, base-3… if base is already used, and
// records the result.
func uniqueID(base string, used map[string]bool) string {
	id := base
	for n := 2; used[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	used[id] = true
	return id
}
