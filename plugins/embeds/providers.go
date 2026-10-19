package main

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// A provider turns a page URL of a video site into a privacy-friendly player
// URL. Adding one means a new entry in providers, a bool setting with the
// same id in plugin.json and its player origin under "csp" → "frame-src".
type provider struct {
	id   string // data-category, setting key
	name string // shown to visitors
	// parse returns the video for u, or false when u is not a video page of
	// this provider.
	parse func(u *url.URL) (video, bool)
}

// video is one recognised link.
type video struct {
	provider *provider
	player   string // privacy-friendly player URL loaded after the click
	portrait bool   // 9:16 (YouTube Shorts, TikTok)
	author   string // "@name" when the URL names the author (TikTok)
}

var providers = []*provider{
	{id: "youtube", name: "YouTube", parse: parseYouTube},
	{id: "vimeo", name: "Vimeo", parse: parseVimeo},
	{id: "tiktok", name: "TikTok", parse: parseTikTok},
}

// match returns the video behind href for an enabled provider.
func match(href string, enabled map[string]bool) (video, bool) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return video{}, false
	}
	for _, p := range providers {
		if !enabled[p.id] {
			continue
		}
		if v, ok := p.parse(u); ok {
			v.provider = p
			return v, true
		}
	}
	return video{}, false
}

var (
	youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	vimeoID   = regexp.MustCompile(`^[0-9]{1,12}$`)
	vimeoHash = regexp.MustCompile(`^[0-9a-f]{6,20}$`)
	// TikTok post ids are 19 digits today; usernames are letters, digits,
	// "_" and "." (at most 24).
	tiktokID   = regexp.MustCompile(`^[0-9]{15,22}$`)
	tiktokUser = regexp.MustCompile(`^@[A-Za-z0-9_.]{1,24}$`)
	// 90, 90s, 1m30s, 1h2m3s
	timeRe = regexp.MustCompile(`^(?:(\d{1,2})h)?(?:(\d{1,4})m)?(?:(\d{1,6})s?)?$`)
)

// host returns the lowercased host name without the port, a trailing dot
// or the given prefixes.
func host(u *url.URL, prefixes ...string) string {
	h := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	for _, p := range prefixes {
		if rest, ok := strings.CutPrefix(h, p); ok {
			return rest
		}
	}
	return h
}

// segments splits the path into its non-empty parts.
func segments(u *url.URL) []string {
	var out []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// seconds reads a start time ("90", "90s", "1m30s", "1h2m3s"); 0 when it is
// missing or invalid.
func seconds(v string) int {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return 0
	}
	m := timeRe.FindStringSubmatch(v)
	if m == nil {
		return 0
	}
	n := 0
	for i, mult := range []int{3600, 60, 1} {
		if m[i+1] != "" {
			x, _ := strconv.Atoi(m[i+1])
			n += x * mult
		}
	}
	return n
}

func parseYouTube(u *url.URL) (video, bool) {
	var id string
	portrait := false
	seg := segments(u)
	switch host(u, "www.", "m.", "music.") {
	case "youtu.be":
		if len(seg) == 1 {
			id = seg[0]
		}
	case "youtube.com":
		switch {
		case len(seg) == 1 && seg[0] == "watch":
			id = u.Query().Get("v")
		case len(seg) == 2 && seg[0] == "shorts":
			id, portrait = seg[1], true
		case len(seg) == 2 && (seg[0] == "embed" || seg[0] == "live"):
			id = seg[1]
		}
	case "youtube-nocookie.com":
		if len(seg) == 2 && seg[0] == "embed" {
			id = seg[1]
		}
	}
	if !youtubeID.MatchString(id) {
		return video{}, false
	}
	q := url.Values{"autoplay": {"1"}}
	start := seconds(u.Query().Get("t"))
	if start == 0 {
		start = seconds(u.Query().Get("start"))
	}
	if start > 0 {
		q.Set("start", strconv.Itoa(start))
	}
	player := url.URL{Scheme: "https", Host: "www.youtube-nocookie.com", Path: "/embed/" + id, RawQuery: q.Encode()}
	return video{player: player.String(), portrait: portrait}, true
}

func parseVimeo(u *url.URL) (video, bool) {
	var id, hash string
	seg := segments(u)
	switch host(u, "www.") {
	case "vimeo.com":
		switch {
		case len(seg) == 1:
			id = seg[0]
		case len(seg) == 2 && vimeoID.MatchString(seg[0]):
			id, hash = seg[0], seg[1]
		case len(seg) == 3 && seg[0] == "channels":
			id = seg[2]
		}
	case "player.vimeo.com":
		if len(seg) == 2 && seg[0] == "video" {
			id = seg[1]
		}
	}
	if h := u.Query().Get("h"); hash == "" && h != "" {
		hash = h
	}
	if !vimeoID.MatchString(id) || hash != "" && !vimeoHash.MatchString(hash) {
		return video{}, false
	}
	q := url.Values{"dnt": {"1"}, "autoplay": {"1"}}
	if hash != "" {
		q.Set("h", hash)
	}
	player := url.URL{Scheme: "https", Host: "player.vimeo.com", Path: "/video/" + id, RawQuery: q.Encode()}
	// Vimeo takes the start time in the fragment: #t=90s.
	if t, ok := strings.CutPrefix(u.Fragment, "t="); ok {
		if n := seconds(t); n > 0 {
			player.Fragment = "t=" + strconv.Itoa(n) + "s"
		}
	}
	return video{player: player.String()}, true
}

// parseTikTok recognises the page and embed URLs of one post. Short links
// (vm.tiktok.com/…, vt.tiktok.com/…, tiktok.com/t/…) hide the post id behind
// a redirect, which a render hook cannot follow, so they stay links.
//
// The player is TikTok's embed player (developers.tiktok.com/doc/embed-player):
// www.tiktok.com/player/v1/<id>. TikTok has no privacy-enhanced mode, so
// the click-to-load placeholder is what keeps the page free of TikTok until
// the visitor asks for the video. rel=0 suggests the author's own videos at
// the end instead of TikTok's recommendations; description=1 shows the
// caption, which the page cannot know without asking TikTok.
func parseTikTok(u *url.URL) (video, bool) {
	var id, author string
	seg := segments(u)
	if host(u, "www.", "m.") == "tiktok.com" {
		switch {
		case len(seg) == 3 && (seg[1] == "video" || seg[1] == "photo") && tiktokUser.MatchString(seg[0]):
			id, author = seg[2], seg[0]
		case len(seg) == 2 && seg[0] == "v": // m.tiktok.com/v/<id>.html
			id = strings.TrimSuffix(seg[1], ".html")
		case len(seg) == 2 && seg[0] == "embed":
			id = seg[1]
		case len(seg) == 3 && (seg[0] == "embed" && seg[1] == "v2" || seg[0] == "player" && seg[1] == "v1"):
			id = seg[2]
		}
	}
	if !tiktokID.MatchString(id) {
		return video{}, false
	}
	q := url.Values{"autoplay": {"1"}, "description": {"1"}, "rel": {"0"}}
	player := url.URL{Scheme: "https", Host: "www.tiktok.com", Path: "/player/v1/" + id, RawQuery: q.Encode()}
	return video{player: player.String(), portrait: true, author: author}, true
}
