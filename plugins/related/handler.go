package main

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// logged keeps each warning to one log line per plugin instance; instances
// are recreated whenever the settings change.
var logged = map[string]bool{}

func warn(msg string) {
	if !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(pluginsdk.Warn, msg)
	}
}

// handle answers the host: content.filter appends the block to posts, the
// "related" widget lists the same posts in a widget area.
func handle(req *pluginsdk.Request) (any, error) {
	switch req.Hook {
	case pluginsdk.HookContentFilter:
		return filter(req), nil
	case pluginsdk.HookWidget:
		return widget(req), nil
	}
	return nil, nil
}

func filter(req *pluginsdk.Request) any {
	var c pluginsdk.ContentFilter
	if err := req.Decode(&c); err != nil {
		warn("invalid content.filter payload: " + err.Error())
		return nil
	}
	if c.Post == nil || c.Post.Type != "post" {
		return nil
	}
	lang := language(c.Post.Lang, req.Site.Language)
	o := options(req, lang)
	if !o.AfterContent {
		return nil
	}
	posts, err := Related(c.Post, lang, o.Count, maxQueries, pluginsdk.QueryPosts)
	if err != nil {
		warn("cannot list related posts: " + err.Error())
		return nil
	}
	block := Block(posts, o)
	if block == "" {
		return nil // the host keeps the HTML as it was
	}
	return pluginsdk.HTML(c.HTML + "\n" + block)
}

func widget(req *pluginsdk.Request) any {
	var w pluginsdk.Widget
	if err := req.Decode(&w); err != nil {
		warn("invalid widget payload: " + err.Error())
		return pluginsdk.HTML("")
	}
	if w.Widget != "related" || w.Page == nil || w.Page.Kind != "single" {
		return pluginsdk.HTML("")
	}
	lang := language(w.Page.Lang, req.Site.Language)
	o := options(req, lang)
	o.Count = count(w.Config["count"], o.Count)
	post, used, err := Current(w.Page, lang, pluginsdk.QueryPosts)
	if err == nil && post != nil {
		var posts []pluginsdk.Post
		if posts, err = Related(post, lang, o.Count, maxQueries-used, pluginsdk.QueryPosts); err == nil {
			return pluginsdk.HTML(Widget(posts, o))
		}
	}
	if err != nil {
		warn("cannot list related posts: " + err.Error())
	}
	return pluginsdk.HTML("")
}

// options reads the plugin settings for content in lang.
func options(req *pluginsdk.Request, lang string) Options {
	s := req.Plugin.Settings
	es := isSpanish(lang)
	o := Options{
		Count: count(s["count"], 4),
		// Settings missing from the request (an older manifest) keep their
		// defaults.
		Images:       strings.TrimSpace(s["show_images"]) == "true",
		AfterContent: strings.TrimSpace(s["after_content"]) != "false",
		Spanish:      es,
		Title:        localized(s, "title", es),
		Location:     time.UTC,
	}
	if o.Title == "" {
		o.Title = "Related posts"
		if es {
			o.Title = "Artículos relacionados"
		}
	}
	if tz := req.Site.Timezone; tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			o.Location = loc
		} else {
			warn("unknown site time zone " + strconv.Quote(tz) + "; dates use UTC")
		}
	}
	return o
}

// count reads a number of posts, clamped to 1-10; def when empty or not a
// number.
func count(v string, def int) int {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) {
		return def
	}
	return int(math.Max(1, math.Min(maxCount, f)))
}

// language is the content language as the host's query expects it
// (lower-case BCP 47), falling back to the site language.
func language(lang, site string) string {
	if strings.TrimSpace(lang) == "" {
		lang = site
	}
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(lang), "_", "-"))
}

// localized reads key_es / key_en, then key (the announcement pattern).
func localized(settings map[string]string, key string, spanish bool) string {
	suffix := "_en"
	if spanish {
		suffix = "_es"
	}
	if v := strings.TrimSpace(settings[key+suffix]); v != "" {
		return v
	}
	return strings.TrimSpace(settings[key])
}

func isSpanish(lang string) bool {
	lang = strings.ToLower(strings.TrimSpace(lang))
	return lang == "es" || strings.HasPrefix(lang, "es-") || strings.HasPrefix(lang, "es_")
}
