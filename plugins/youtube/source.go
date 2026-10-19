package main

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A Source is what a gallery shows: the uploads of a channel (given by its
// id or by its handle) or a playlist.
type Source struct {
	Kind string // kindChannel, kindHandle or kindPlaylist
	ID   string // UC… id, handle without "@", or playlist id
}

const (
	kindChannel  = "channel"
	kindHandle   = "handle"
	kindPlaylist = "playlist"
)

var (
	channelID  = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)
	playlistID = regexp.MustCompile(`^[A-Za-z0-9_-]{12,64}$`)

	errNoSource = errors.New("no channel or playlist is set")
)

// Key identifies the source in the plugin's storage. Handles are not case
// sensitive on YouTube, so they are compared in lower case.
func (s Source) Key() string {
	switch s.Kind {
	case kindChannel:
		return "c:" + s.ID
	case kindHandle:
		return "h:" + strings.ToLower(s.ID)
	case kindPlaylist:
		return "p:" + s.ID
	}
	return ""
}

// SourceFromKey reverses Key.
func SourceFromKey(key string) (Source, bool) {
	kind, id, ok := strings.Cut(key, ":")
	if !ok {
		return Source{}, false
	}
	switch kind {
	case "c":
		return Source{kindChannel, id}, channelID.MatchString(id)
	case "h":
		return Source{kindHandle, id}, validHandle(id)
	case "p":
		return Source{kindPlaylist, id}, playlistID.MatchString(id)
	}
	return Source{}, false
}

// Page is the source's page on YouTube, linked from the gallery.
func (s Source) Page() string {
	u := url.URL{Scheme: "https", Host: "www.youtube.com"}
	switch s.Kind {
	case kindChannel:
		u.Path = "/channel/" + s.ID
	case kindHandle:
		u.Path = "/@" + s.ID
	case kindPlaylist:
		u.Path = "/playlist"
		u.RawQuery = url.Values{"list": {s.ID}}.Encode()
	}
	return u.String()
}

// Pick returns the source of a gallery: the playlist when one is given,
// else the channel. Empty values fall back to the plugin settings, but only
// as a pair, so a shortcode that names a channel never shows the default
// playlist.
func Pick(channel, playlist, defChannel, defPlaylist string) (Source, error) {
	channel, playlist = strings.TrimSpace(channel), strings.TrimSpace(playlist)
	if channel == "" && playlist == "" {
		channel, playlist = strings.TrimSpace(defChannel), strings.TrimSpace(defPlaylist)
	}
	switch {
	case playlist != "":
		return ParsePlaylist(playlist)
	case channel != "":
		return ParseChannel(channel)
	}
	return Source{}, errNoSource
}

// ParseChannel reads a channel id (UC…), a handle (@name, or name), or the
// URL of a channel or a playlist on YouTube.
func ParseChannel(raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if u, ok := youtubeURL(raw); ok {
		return fromURL(u, raw)
	}
	switch {
	case channelID.MatchString(raw):
		return Source{kindChannel, raw}, nil
	case validHandle(strings.TrimPrefix(raw, "@")):
		return Source{kindHandle, strings.TrimPrefix(raw, "@")}, nil
	}
	return Source{}, errors.New(quote(raw) + " is not a channel id (UC…), a handle (@name) or a channel URL")
}

// ParsePlaylist reads a playlist id or a URL with ?list=.
func ParsePlaylist(raw string) (Source, error) {
	raw = strings.TrimSpace(raw)
	if u, ok := youtubeURL(raw); ok {
		if id := u.Query().Get("list"); playlistID.MatchString(id) && !channelID.MatchString(id) {
			return Source{kindPlaylist, id}, nil
		}
		return Source{}, errors.New(quote(raw) + " is not a playlist URL (youtube.com/playlist?list=…)")
	}
	if playlistID.MatchString(raw) && !channelID.MatchString(raw) {
		return Source{kindPlaylist, raw}, nil
	}
	return Source{}, errors.New(quote(raw) + " is not a playlist id or URL")
}

// youtubeURL parses raw when it is an http(s) URL on youtube.com.
func youtubeURL(raw string) (*url.URL, bool) {
	if !strings.Contains(raw, "/") {
		return nil, false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw // "youtube.com/@name"
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return nil, false
	}
	switch strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		return u, true
	}
	return nil, false
}

func fromURL(u *url.URL, raw string) (Source, error) {
	var seg []string
	for _, s := range strings.Split(u.Path, "/") {
		if s != "" {
			seg = append(seg, s)
		}
	}
	switch {
	case len(seg) >= 2 && seg[0] == "channel" && channelID.MatchString(seg[1]):
		return Source{kindChannel, seg[1]}, nil
	case len(seg) >= 1 && strings.HasPrefix(seg[0], "@") && validHandle(seg[0][1:]):
		return Source{kindHandle, seg[0][1:]}, nil
	case len(seg) == 1 && (seg[0] == "playlist" || seg[0] == "watch") && u.Query().Get("list") != "":
		return ParsePlaylist(raw)
	case len(seg) >= 2 && (seg[0] == "c" || seg[0] == "user"):
		return Source{}, errors.New(quote(raw) + " is an old custom URL, which the feeds cannot read: use the channel id (UC…) or the handle (@name)")
	}
	return Source{}, errors.New(quote(raw) + " is not a channel URL (youtube.com/channel/UC… or youtube.com/@name)")
}

// validHandle follows YouTube's rules: 3 to 30 letters (of any script),
// digits, underscores, hyphens and periods.
func validHandle(h string) bool {
	if n := utf8.RuneCountInString(h); n < 3 || n > 30 {
		return false
	}
	for _, r := range h {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' && !unicode.Is(unicode.Mn, r) {
			return false
		}
	}
	return true
}

// quote shows a value from the settings in a log line, shortened.
func quote(s string) string {
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return "“" + s + "”"
}
