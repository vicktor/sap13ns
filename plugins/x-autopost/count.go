package main

import (
	"regexp"

	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// X counts a post's length with weights (twitter-text, configuration v3):
// 280 units at most, where the code points of Latin, Greek, Cyrillic,
// Hebrew, Arabic, Indic scripts and common punctuation weigh 1 and every
// other code point (CJK, emoji…) weighs 2, and every link counts as 23,
// whatever its length. The text is normalized to NFC first.
//
// Weight counts per code point, so it is an upper bound: X counts an emoji
// sequence (👩‍💻, a flag, a skin tone) as 2 in all, and composes "e" +
// combining accent into one code point. The few characters that NFC
// expands (Devanagari ka with nukta, Hebrew presentation forms, musical
// symbols) are counted as three wide code points. Counting more than X
// does only shortens the text a little; counting less could make X refuse
// it.
const (
	xLimit   = 280
	xURLLen  = 23
	xNarrow  = 1
	xWide    = 2
	xExpands = 3 * xWide
)

// narrow are the ranges that weigh 1.
var narrow = [][2]rune{{0, 4351}, {8192, 8205}, {8208, 8223}, {8242, 8247}}

// expands are code points NFC replaces with two or three (the
// script-specific composition exclusions and the decompositions that are
// not starters).
var expands = [][2]rune{
	{0x0344, 0x0344}, {0x0958, 0x095F}, {0x09DC, 0x09DD}, {0x09DF, 0x09DF}, {0x0A33, 0x0A33}, {0x0A36, 0x0A36},
	{0x0A59, 0x0A5B}, {0x0A5E, 0x0A5E}, {0x0B5C, 0x0B5D}, {0x0F43, 0x0F43}, {0x0F4D, 0x0F4D}, {0x0F52, 0x0F52},
	{0x0F57, 0x0F57}, {0x0F5C, 0x0F5C}, {0x0F69, 0x0F69}, {0x0F73, 0x0F73}, {0x0F75, 0x0F76}, {0x0F78, 0x0F78},
	{0x0F81, 0x0F81}, {0x0F93, 0x0F93}, {0x0F9D, 0x0F9D}, {0x0FA2, 0x0FA2}, {0x0FA7, 0x0FA7}, {0x0FAC, 0x0FAC},
	{0x0FB9, 0x0FB9}, {0x2ADC, 0x2ADC}, {0xFB1D, 0xFB4E}, {0x1D15E, 0x1D164}, {0x1D1BB, 0x1D1C0},
}

func inRanges(r rune, ranges [][2]rune) bool {
	for _, rg := range ranges {
		if rg[0] <= r && r <= rg[1] {
			return true
		}
	}
	return false
}

func weight(r rune) int {
	switch {
	case inRanges(r, expands):
		return xExpands
	case inRanges(r, narrow):
		return xNarrow
	}
	return xWide
}

func weighted(s string) int {
	n := 0
	for _, r := range s {
		n += weight(r)
	}
	return n
}

// bareDomain finds what X may turn into a link without a scheme
// ("example.com/page"). Such a token counts as 23 or as itself, whichever
// is longer.
var bareDomain = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}-]*(?:\.[\p{L}\p{N}-]+)*\.\p{L}{2,}(?:/[^\s]*)?`)

// xLen is the length of a text as X counts it.
func xLen(s string) int {
	// Links with a scheme count 23; the text between them is weighed, and
	// bare domains in it count at least 23.
	n, last := 0, 0
	for _, loc := range autopost.URLs.FindAllStringIndex(s, -1) {
		n += plain(s[last:loc[0]]) + xURLLen
		last = loc[1]
	}
	return n + plain(s[last:])
}

func plain(s string) int {
	n := weighted(s)
	for _, d := range bareDomain.FindAllString(s, -1) {
		if w := weighted(d); w < xURLLen {
			n += xURLLen - w
		}
	}
	return n
}

// xText is the limit Compose fits the text into.
var xText = autopost.Limit{Max: xLimit, Len: xLen}
