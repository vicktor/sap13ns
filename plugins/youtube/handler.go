package main

import (
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // the sandbox has no zoneinfo files

	"github.com/vicktor/sap13ns/pluginsdk"
)

const defaultCount = 6

// logged keeps each warning to one line per plugin instance; instances are
// recreated whenever the settings change.
var logged = map[string]bool{}

func logOnce(level pluginsdk.Level, msg string) {
	if !logged[msg] {
		logged[msg] = true
		pluginsdk.Log(level, msg)
	}
}

// handle answers the hooks of plugin.json. Expected trouble (nothing
// configured, storage denied in the scanner's dry run) is logged, never
// returned as an error, so a page always renders.
func handle(req *pluginsdk.Request) (any, error) {
	s := req.Plugin.Settings
	switch req.Hook {
	case pluginsdk.HookShortcode:
		var sc pluginsdk.Shortcode
		if err := req.Decode(&sc); err != nil {
			return nil, nil
		}
		lang := req.Site.Language
		if sc.Post != nil && sc.Post.Lang != "" {
			lang = sc.Post.Lang
		}
		a := sc.Attrs
		return pluginsdk.HTML(render(req, a["channel"], a["playlist"], a["count"], a["layout"], a["title"], lang, false, "[youtube_videos]")), nil
	case pluginsdk.HookWidget:
		var w pluginsdk.Widget
		if err := req.Decode(&w); err != nil {
			return nil, nil
		}
		lang := req.Site.Language
		if w.Page != nil && w.Page.Lang != "" {
			lang = w.Page.Lang
		}
		c := w.Config
		return pluginsdk.HTML(render(req, c["channel"], c["playlist"], c["count"], c["layout"], "", lang, true, "the widget")), nil
	case pluginsdk.HookEvent:
		var defaults []Source
		if src, err := Pick("", "", s["channel"], s["playlist"]); err == nil {
			defaults = append(defaults, src)
		} else if err != errNoSource {
			logOnce(pluginsdk.Warn, "settings: "+err.Error())
		}
		switch req.Event {
		case pluginsdk.EventCronHourly:
			Refresh(defaults, strings.TrimSpace(s["api_key"]), false)
		case pluginsdk.EventPostPublished, pluginsdk.EventPostUpdated:
			Refresh(defaults, strings.TrimSpace(s["api_key"]), true)
		}
	}
	return nil, nil
}

// render builds one gallery from the attributes of a shortcode or the
// configuration of a widget; empty values use the plugin settings.
func render(req *pluginsdk.Request, channel, playlist, count, layout, heading, lang string, widget bool, where string) string {
	s := req.Plugin.Settings
	src, err := Pick(channel, playlist, s["channel"], s["playlist"])
	if err != nil {
		if err == errNoSource {
			logOnce(pluginsdk.Warn, where+": no channel or playlist; set one in the plugin settings or in "+where)
		} else {
			logOnce(pluginsdk.Warn, where+": "+err.Error())
		}
		return ""
	}
	v := viewFor(req, count, layout, lang, widget, where)
	v.Heading = heading
	key := src.Key()
	if def, err := Pick("", "", s["channel"], s["playlist"]); err != nil || def.Key() != key {
		ok, err := register(key)
		switch {
		case err != nil:
			logOnce(pluginsdk.Warn, "the plugin's storage is not available: "+err.Error())
		case !ok:
			logOnce(pluginsdk.Warn, describe(src)+": not fetched, because galleries already show "+strconv.Itoa(slots)+" other channels and playlists; unused ones are forgotten after 30 days")
		}
	}
	rec, ok, err := loadRecord(key)
	if err != nil {
		logOnce(pluginsdk.Warn, "the plugin's storage is not available: "+err.Error())
	}
	if !ok || len(rec.Videos) == 0 || now().Sub(time.Unix(rec.Fetched, 0)) > keepFor {
		// Nothing yet (the first fetch runs within the hour, or when a post
		// is published or updated), or a list YouTube's rules say is too old
		// to show.
		if src.Kind == kindHandle && strings.TrimSpace(s["api_key"]) == "" && rec.Channel == "" {
			logOnce(pluginsdk.Warn, describe(src)+": "+errNeedsKey.Error())
		}
		return Empty(src, rec.Title, v)
	}
	return Gallery(src, rec, v)
}

func viewFor(req *pluginsdk.Request, count, layout, lang string, widget bool, where string) View {
	s := req.Plugin.Settings
	t := textsFor(lang)
	v := View{Widget: widget, Thumbnails: s["thumbnails"] != "false", Text: t, Loc: time.UTC}
	if loc, err := time.LoadLocation(req.Site.Timezone); err == nil && req.Site.Timezone != "" {
		v.Loc = loc
	}
	v.Count = number(s["count"], defaultCount, "settings: count", maxVideos)
	if strings.TrimSpace(count) != "" {
		v.Count = number(count, v.Count, where+": count", maxVideos)
	}
	v.Layout = layoutGrid
	if l := strings.TrimSpace(s["layout"]); l == layoutCarousel {
		v.Layout = l
	}
	if widget {
		v.Layout = layoutCarousel // the widget's own default
	}
	switch l := strings.ToLower(strings.TrimSpace(layout)); l {
	case "":
	case layoutGrid, layoutCarousel:
		v.Layout = l
	default:
		logOnce(pluginsdk.Warn, where+": layout "+quote(layout)+" is neither grid nor carousel; using "+v.Layout)
	}
	if s["show_notice"] != "false" {
		v.Notice = localized(s, "notice", t.es)
		if v.Notice == "" {
			v.Notice = t.Notice(v.Thumbnails)
		}
		if u := localized(s, "privacy_url", t.es); safeURL(u) {
			v.PrivacyURL = u
		} else if u != "" {
			logOnce(pluginsdk.Warn, "settings: privacy URL "+quote(u)+" is not an http(s) URL or a site path; ignored")
		}
	}
	return v
}

// number reads a count from 1 to max; anything else gives def.
func number(raw string, def int, what string, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		logOnce(pluginsdk.Warn, what+" "+quote(raw)+" is not a number from 1 to "+strconv.Itoa(max)+"; using "+strconv.Itoa(def))
		return def
	}
	return min(n, max)
}

// localized reads key_es / key_en, then key.
func localized(s map[string]string, key string, spanish bool) string {
	suffix := "_en"
	if spanish {
		suffix = "_es"
	}
	if v := strings.TrimSpace(s[key+suffix]); v != "" {
		return v
	}
	return strings.TrimSpace(s[key])
}

// safeURL accepts absolute http(s) URLs and site paths ("/privacy").
func safeURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\"<>\\") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
