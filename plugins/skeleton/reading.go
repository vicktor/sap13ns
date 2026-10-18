package main

import (
	"strconv"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// A content.filter receives the rendered HTML of each post and may return a
// changed version. This one adds a reading-time note to blog posts.
func init() {
	filters = append(filters, func(req *pluginsdk.Request, c pluginsdk.ContentFilter) (string, bool) {
		if c.Post == nil || c.Post.Type != "post" {
			return "", false
		}
		wpm, _ := strconv.Atoi(req.Setting("words_per_minute"))
		return c.HTML + ReadingNote(c.Post.WordCount, wpm, c.Post.Lang), true
	})
}

// ReadingNote is "<p class="reading-time">3 min read</p>" in the post's
// language (English or Spanish here).
func ReadingNote(words, wordsPerMinute int, lang string) string {
	if wordsPerMinute < 50 {
		wordsPerMinute = 220
	}
	minutes := max(1, (words+wordsPerMinute-1)/wordsPerMinute)
	text := strconv.Itoa(minutes) + " min read"
	if lang == "es" {
		text = strconv.Itoa(minutes) + " min de lectura"
	}
	return `<p class="reading-time">` + text + "</p>"
}
