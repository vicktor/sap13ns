package main

import (
	"strings"
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The host passes head.meta a shortened post (id, type, title, slug, lang,
// url, excerpt and author), without the publication date, the featured
// image or the terms. The plugin finds the full post with one posts_query
// search on its title and merges what is missing.

// NeedsLookup reports whether the post in a head.meta payload lacks the
// fields a full post object carries. A host that sends the complete post
// (as docs/PLUGINS.md describes) skips the query.
func NeedsLookup(p *pluginsdk.Post) bool {
	return p != nil && p.ID > 0 && p.PublishedAt == "" && p.Image == "" && len(p.Terms) == 0
}

// SearchQuery turns a title into a posts_query search that the post itself
// always matches: words made of letters and digits (the host's full-text
// index splits on everything else), at most eight of them, so that search
// operators ("or", quotes, "-") in titles change nothing.
func SearchQuery(title string) string {
	words := strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []string
	for _, w := range words {
		if strings.EqualFold(w, "or") {
			continue // websearch syntax: OR
		}
		out = append(out, w)
		if len(out) == 8 {
			break
		}
	}
	return strings.Join(out, " ")
}

// LookupQuery is the posts_query that finds the full post, and false when
// the title has nothing searchable.
func LookupQuery(p *pluginsdk.Post) (pluginsdk.PostsQuery, bool) {
	q := SearchQuery(p.Title)
	if q == "" {
		return pluginsdk.PostsQuery{}, false
	}
	typ := p.Type
	if typ != "page" {
		typ = "post"
	}
	return pluginsdk.PostsQuery{Type: typ, Lang: p.Lang, Search: q, Limit: 20}, true
}

// Merge fills the fields of p that the head.meta payload leaves out from
// the matching result of the lookup. It reports whether a match was found.
func Merge(p *pluginsdk.Post, results []pluginsdk.Post) bool {
	for _, r := range results {
		if r.ID != p.ID {
			continue
		}
		if p.PublishedAt == "" {
			p.PublishedAt = r.PublishedAt
		}
		if p.Image == "" {
			p.Image = r.Image
		}
		if len(p.Terms) == 0 {
			p.Terms = r.Terms
		}
		if len(p.Categories) == 0 {
			p.Categories = r.Categories
		}
		if len(p.Tags) == 0 {
			p.Tags = r.Tags
		}
		if p.WordCount == 0 {
			p.WordCount = r.WordCount
		}
		if p.Author == "" {
			p.Author = r.Author
		}
		if p.Excerpt == "" {
			p.Excerpt = r.Excerpt
		}
		return true
	}
	return false
}
