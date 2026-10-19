package main

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// LinkedIn's commentary is "little" text: the characters | { } @ [ ] ( ) <
// > # \ * _ ~ are markup and must be escaped with a backslash to show as
// themselves, and a hashtag is the template {hashtag|\#|tag}.
const reserved = `|{}@[]()<>#\*_~`

var hashtagRe = regexp.MustCompile(`(^|\s)#([\p{L}\p{M}\p{N}_]+)`)

// Little turns plain text into little text: the hashtags become hashtag
// templates and every other reserved character is escaped.
func Little(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range hashtagRe.FindAllStringSubmatchIndex(s, -1) {
		tag := s[m[4]:m[5]]
		if !autopost.ValidTag(tag) {
			continue
		}
		b.WriteString(escape(s[last : m[4]-1])) // up to the "#"
		b.WriteString(`{hashtag|\#|` + escape(tag) + `}`)
		last = m[5]
	}
	b.WriteString(escape(s[last:]))
	return b.String()
}

func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(reserved, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// commentaryLimit is LinkedIn's limit for a post's text, 3000 characters,
// counted on the little text that is sent (an upper bound of what the
// reader sees).
var commentaryLimit = autopost.Limit{Max: 3000, Len: func(s string) int { return utf8.RuneCountInString(Little(s)) }}
