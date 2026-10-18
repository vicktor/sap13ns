package main

import (
	"github.com/vicktor/sap13ns/pluginsdk"
)

// head.meta adds tags to the <head> of every public page. Names are
// validated by the host (no http-equiv), and JSON-LD objects are checked,
// size-capped and escaped before they reach the page.
func init() {
	headTags = append(headTags, func(req *pluginsdk.Request, page pluginsdk.Page) ([]pluginsdk.Meta, []any) {
		return HeadFor(page)
	})
}

// HeadFor describes single posts as schema.org BlogPosting objects.
func HeadFor(page pluginsdk.Page) ([]pluginsdk.Meta, []any) {
	if page.Kind != "single" || page.Post == nil {
		return nil, nil
	}
	p := page.Post
	ld := map[string]any{
		"@context":   "https://schema.org",
		"@type":      "BlogPosting",
		"headline":   p.Title,
		"url":        p.URL,
		"inLanguage": p.Lang,
		"wordCount":  p.WordCount,
	}
	if p.PublishedAt != "" {
		ld["datePublished"] = p.PublishedAt
	}
	if p.Image != "" {
		ld["image"] = p.Image
	}
	return []pluginsdk.Meta{{Name: "author", Content: p.Author}}, []any{ld}
}
