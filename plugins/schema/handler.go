package main

import (
	"errors"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// logged keeps each warning to one line per plugin instance; the host
// creates new instances when the settings change.
var logged = map[string]bool{}

func logOnce(level pluginsdk.Level, msg string) {
	if !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(level, msg)
	}
}

// handle answers head.meta with one JSON-LD object.
func handle(req *pluginsdk.Request) (any, error) {
	if req.Hook != pluginsdk.HookHeadMeta {
		return nil, nil
	}
	var page pluginsdk.Page
	if err := req.Decode(&page); err != nil {
		logOnce(pluginsdk.Warn, "invalid head.meta payload: "+err.Error())
		return nil, nil
	}
	cfg, warnings := ConfigFrom(req.Plugin.Settings, req.Site)
	for _, w := range warnings {
		logOnce(pluginsdk.Warn, w)
	}
	if page.Post != nil {
		post := *page.Post
		complete(&post)
		page.Post = &post
	}
	ld := Build(page, cfg)
	if ld == nil {
		if page.Kind != "404" && page.URL != "" {
			logOnce(pluginsdk.Warn, "the structured data of "+page.URL+" is too large and was left out")
		}
		return nil, nil
	}
	return pluginsdk.Head(nil, ld), nil
}

// complete adds the publication date, image and terms that the host leaves
// out of head.meta payloads, with one posts_query search. Failures only
// mean less detail: nothing is invented.
func complete(p *pluginsdk.Post) {
	if !NeedsLookup(p) {
		return
	}
	q, ok := LookupQuery(p)
	if !ok {
		return
	}
	results, err := pluginsdk.QueryPosts(q)
	switch {
	case errors.Is(err, pluginsdk.ErrDenied):
		logOnce(pluginsdk.Warn, "posts:read is not granted: dates, images and categories are left out")
		return
	case err != nil:
		logOnce(pluginsdk.Warn, "posts_query failed: "+err.Error())
		return
	}
	if !Merge(p, results) {
		pluginsdk.Log(pluginsdk.Debug, "post "+p.URL+" not found by its title; dates and images left out")
	}
}
