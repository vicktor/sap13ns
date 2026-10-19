package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// The feed is the connected account's latest posts. It is fetched in the
// background and kept under one key of the plugin's storage, next to the
// publishing records of pluginsdk/autopost ("posted", "queue", "lock").
const (
	feedKey      = "feed"
	maxItems     = 24
	maxFeedBytes = 200 << 10
	refreshEvery = 55 * time.Minute // a little under the hourly run
	maxBackoff   = 24 * time.Hour
	// showFor is how long a list that could not be refreshed is shown:
	// Instagram's image addresses are signed and stop working after a few
	// days.
	showFor = 72 * time.Hour
)

// The fields of GET /<IG_ID>/media. Meta's reference marks caption as
// "Facebook Login only"; if the API refuses it, the plugin falls back to
// the fields every login has, and remembers it.
const (
	fullFields  = "id,media_type,media_url,thumbnail_url,permalink,timestamp,caption,alt_text"
	basicFields = "id,media_type,media_url,thumbnail_url,permalink,timestamp"
)

// Feed is what the plugin stores.
type Feed struct {
	Account string  `json:"account,omitempty"` // the IG user id the list belongs to
	User    string  `json:"user,omitempty"`    // its username
	Items   []Media `json:"items,omitempty"`
	Basic   bool    `json:"basic,omitempty"`   // the API refused the caption fields
	Fetched int64   `json:"fetched,omitempty"` // last good fetch
	Tried   int64   `json:"tried,omitempty"`   // last attempt
	Fails   int     `json:"fails,omitempty"`   // failed attempts in a row
	Err     string  `json:"err,omitempty"`     // the last error
}

// Media is one post of the feed.
type Media struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`          // image | video | album
	Image string `json:"img,omitempty"` // "" shows a placeholder
	Link  string `json:"link"`          // the post on instagram.com
	Alt   string `json:"alt,omitempty"` // the alt text, or the start of the caption
	Time  int64  `json:"time,omitempty"`
}

func loadFeed() (Feed, bool, error) {
	var f Feed
	raw, err := pluginsdk.KVGet(feedKey)
	if errors.Is(err, pluginsdk.ErrNotFound) {
		return f, false, nil
	}
	if err != nil {
		return f, false, err
	}
	if json.Unmarshal(raw, &f) != nil {
		return Feed{}, false, nil // unreadable: treated as absent and overwritten
	}
	return f, true, nil
}

// saveFeed stores f, dropping its oldest posts if it is too large.
func saveFeed(f Feed) error {
	for {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if len(b) <= maxFeedBytes || len(f.Items) == 0 {
			return pluginsdk.KVSet(feedKey, b)
		}
		f.Items = f.Items[:len(f.Items)-1]
	}
}

// due reports whether the feed should be fetched now. After a failure it
// waits 1, 2, 4… hours (at most a day).
func (f Feed) due(t time.Time) bool {
	if f.Fails > 0 {
		wait := min(time.Hour<<min(f.Fails-1, 5), maxBackoff)
		return t.Sub(time.Unix(f.Tried, 0)) >= wait-5*time.Minute
	}
	return f.Fetched == 0 || t.Sub(time.Unix(f.Fetched, 0)) >= refreshEvery
}

// Showable reports whether the stored list may be shown at t.
func (f Feed) Showable(t time.Time) bool {
	return len(f.Items) > 0 && t.Sub(time.Unix(f.Fetched, 0)) <= showFor
}

// RefreshFeed fetches the feed when it is due (always with force, when
// the account was just connected) and returns the requests it used. A
// failure keeps the last good list. Without a connection nothing is
// fetched, and a feed whose account was disconnected is deleted.
func RefreshFeed(conn connection, force bool) int {
	f, ok, err := loadFeed()
	if err != nil {
		pluginsdk.Log(pluginsdk.Warn, "feed: the plugin's storage is not available: "+err.Error())
		return 0
	}
	if conn.token == "" {
		if conn.gone && ok {
			if err := pluginsdk.KVDelete(feedKey); err == nil {
				pluginsdk.Log(pluginsdk.Info, "feed: Instagram is not connected, so the stored feed was removed")
			}
		}
		return 0
	}
	if f.Account != conn.account {
		f = Feed{Account: conn.account} // another account: start over
	}
	if conn.name != "" {
		f.User = conn.name
	}
	t := now()
	if !force && !f.due(t) {
		return 0
	}
	f.Tried = t.Unix()
	used := 1
	items, err := fetchMedia(conn, f.Basic)
	if !f.Basic && fieldRefused(err) {
		f.Basic = true
		pluginsdk.Log(pluginsdk.Info, "feed: Instagram refused the caption fields ("+err.Error()+"); the feed uses the basic fields from now on, and the images get a generic alt text")
		used++
		items, err = fetchMedia(conn, true)
	}
	if err != nil {
		f.Fails++
		f.Err = autopost.Shorten(err.Error(), 300)
		level := pluginsdk.Warn
		if f.Fails > 1 {
			level = pluginsdk.Debug // already reported
		}
		kept := "nothing to show yet"
		if len(f.Items) > 0 {
			kept = "the last good list (" + itoa(len(f.Items)) + " posts) stays"
		}
		pluginsdk.Log(level, "feed: fetching the latest posts failed ("+f.Err+"); "+kept)
	} else {
		if f.Fails > 0 || f.Fetched == 0 {
			pluginsdk.Log(pluginsdk.Info, "feed: "+itoa(len(items))+" posts from "+who(f))
		}
		f.Items, f.Fetched, f.Fails, f.Err = items, t.Unix(), 0, ""
	}
	if err := saveFeed(f); err != nil {
		pluginsdk.Log(pluginsdk.Warn, "feed: could not store the list: "+err.Error())
	}
	return used
}

func who(f Feed) string {
	if f.User != "" {
		return "@" + f.User
	}
	return "account " + f.Account
}

// graphError is a refused Graph API request.
type graphError struct {
	msg  string
	meta meta
}

func (e *graphError) Error() string { return e.msg }

// fieldRefused recognizes Graph API error #100 about a field.
func fieldRefused(err error) bool {
	var ge *graphError
	return errors.As(err, &ge) && ge.meta.Code == 100 && strings.Contains(strings.ToLower(ge.msg), "field")
}

// fetchMedia reads the newest posts of the connected account.
func fetchMedia(conn connection, basic bool) ([]Media, error) {
	fields := fullFields
	if basic {
		fields = basicFields
	}
	q := url.Values{"fields": {fields}, "limit": {itoa(maxItems)}}
	resp, err := pluginsdk.Fetch(graphRequest("GET", graphBase+url.PathEscape(conn.account)+"/media?"+q.Encode(), conn.token, nil, 5000))
	if r, bad := autopost.FetchProblem(resp, err, graphBase, ""); bad {
		m := metaCodes(resp)
		return nil, &graphError{msg: r.Detail + hint(m), meta: m}
	}
	var page struct {
		Data []struct {
			ID           string `json:"id"`
			MediaType    string `json:"media_type"`
			MediaURL     string `json:"media_url"`
			ThumbnailURL string `json:"thumbnail_url"`
			Permalink    string `json:"permalink"`
			Timestamp    string `json:"timestamp"`
			Caption      string `json:"caption"`
			AltText      string `json:"alt_text"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp.Body), &page); err != nil {
		return nil, errors.New("the answer is not the expected JSON")
	}
	var out []Media
	var foreign []string
	for _, d := range page.Data {
		if len(out) == maxItems {
			break
		}
		m := Media{ID: d.ID, Link: permalink(d.Permalink)}
		switch d.MediaType {
		case "IMAGE":
			m.Kind, m.Image = "image", d.MediaURL
		case "VIDEO":
			m.Kind, m.Image = "video", d.ThumbnailURL
		case "CAROUSEL_ALBUM":
			m.Kind, m.Image = "album", d.MediaURL
		default:
			continue
		}
		if m.ID == "" || m.Link == "" {
			continue
		}
		if isVideoFile(m.Image) { // an album whose cover is a video
			m.Image = ""
		}
		if m.Image != "" && !allowedImage(m.Image) {
			if u, err := url.Parse(m.Image); err == nil && len(foreign) < 3 {
				foreign = append(foreign, u.Hostname())
			}
			m.Image = ""
		}
		m.Alt = autopost.Truncate(autopost.OneLine(cmpOr(d.AltText, d.Caption)), 150)
		if t, ok := parseTime(d.Timestamp); ok {
			m.Time = t.Unix()
		}
		out = append(out, m)
	}
	if len(foreign) > 0 {
		pluginsdk.Log(pluginsdk.Warn, "feed: some images are on hosts outside the plugin's img-src ("+strings.Join(foreign, ", ")+"); they show as placeholders")
	}
	return out, nil
}

// permalink accepts the post's address on instagram.com only.
func permalink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || len(raw) > 500 ||
		(u.Host != "www.instagram.com" && u.Host != "instagram.com") {
		return ""
	}
	return u.String()
}

// imageHosts are the plugin's csp img-src sources (plugin.json).
var imageHosts = []string{".cdninstagram.com", ".fbcdn.net"}

// allowedImage accepts https images on Instagram's CDN, which the page's
// Content-Security-Policy lets the browser load.
func allowedImage(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(raw) > 2000 {
		return false
	}
	for _, h := range imageHosts {
		if strings.HasSuffix(u.Hostname(), h) {
			return true
		}
	}
	return false
}

func isVideoFile(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(path.Ext(u.Path)) {
	case ".mp4", ".mov", ".m4v", ".webm":
		return true
	}
	return false
}

// parseTime reads Meta's timestamps ("2026-10-19T09:30:00+0000").
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02T15:04:05-0700", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
