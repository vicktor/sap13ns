package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The YouTube Data API v3 (https://developers.google.com/youtube/v3/docs),
// used only when an API key is set. Every call below is a list request,
// which costs 1 quota unit (the default quota is 10,000 units a day). The
// key travels in the X-Goog-Api-Key header, never in the URL, so it cannot
// end up in a log line.

const (
	apiHost    = "www.googleapis.com"
	feedHost   = "www.youtube.com"
	timeoutMS  = 6000
	httpBudget = 4 // the host allows 4 requests per call
)

// errNotFound means the channel or playlist does not exist (or is private):
// retrying soon is pointless.
var errNotFound = errors.New("not found on YouTube")

// client makes the requests of one event call within the host's budget.
type client struct {
	left int
	key  string // API key, "" without one
}

var errBudget = errors.New("no requests left in this run")

func (n *client) get(host, path string, q url.Values, accept string) (int, string, error) {
	if n.left <= 0 {
		return 0, "", errBudget
	}
	n.left--
	u := url.URL{Scheme: "https", Host: host, Path: path, RawQuery: q.Encode()}
	h := map[string]string{"Accept": accept}
	if host == apiHost {
		h["X-Goog-Api-Key"] = n.key
	}
	resp, err := pluginsdk.Fetch(pluginsdk.HTTPRequest{Method: "GET", URL: u.String(), Headers: h, TimeoutMS: timeoutMS})
	if err != nil {
		return 0, "", errors.New(host + ": " + err.Error())
	}
	return resp.Status, resp.Body, nil
}

// Feed fetches the Atom feed of a channel or a playlist: the newest 15
// videos, no key needed.
func (n *client) Feed(kind, id string) (List, error) {
	q := url.Values{"channel_id": {id}}
	if kind == kindPlaylist {
		q = url.Values{"playlist_id": {id}}
	}
	status, body, err := n.get(feedHost, "/feeds/videos.xml", q, "application/atom+xml, application/xml;q=0.9")
	switch {
	case err != nil:
		return List{}, err
	case status == 404:
		return List{}, errNotFound
	case status != 200:
		return List{}, errors.New("the feed answered HTTP " + strconv.Itoa(status))
	}
	return ParseFeed([]byte(body), maxVideos)
}

// apiGet calls one list method and decodes its JSON.
func (n *client) apiGet(method string, q url.Values, out any) error {
	status, body, err := n.get(apiHost, "/youtube/v3/"+method, q, "application/json")
	if err != nil {
		return err
	}
	if status != 200 {
		return apiError(method, status, body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		return errors.New(method + ": unreadable answer: " + err.Error())
	}
	return nil
}

// apiError explains a failed call with Google's error reason
// (keyInvalid, quotaExceeded, accessNotConfigured…).
func apiError(method string, status int, body string) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(body), &e)
	msg := method + ": HTTP " + strconv.Itoa(status)
	if len(e.Error.Errors) > 0 && e.Error.Errors[0].Reason != "" {
		msg += " " + cleanText(e.Error.Errors[0].Reason, 60)
	}
	if e.Error.Message != "" {
		msg += ": " + cleanText(e.Error.Message, 200)
	}
	if status == 404 {
		return errors.Join(errNotFound, errors.New(msg))
	}
	return errors.New(msg)
}

// Channel looks a channel up by id or by handle: its id, name and uploads
// playlist (channels.list, 1 unit).
func (n *client) Channel(s Source) (id, title, uploads string, err error) {
	q := url.Values{"part": {"snippet,contentDetails"}, "fields": {"items(id,snippet/title,contentDetails/relatedPlaylists/uploads)"}}
	if s.Kind == kindHandle {
		q.Set("forHandle", "@"+s.ID)
	} else {
		q.Set("id", s.ID)
	}
	var r struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	if err := n.apiGet("channels", q, &r); err != nil {
		return "", "", "", err
	}
	if len(r.Items) == 0 || !channelID.MatchString(r.Items[0].ID) {
		return "", "", "", errNotFound
	}
	it := r.Items[0]
	uploads = it.ContentDetails.RelatedPlaylists.Uploads
	if !playlistID.MatchString(uploads) {
		return "", "", "", errors.New("channels: the channel has no uploads playlist")
	}
	return it.ID, cleanText(it.Snippet.Title, maxNameRunes), uploads, nil
}

// Playlist reads a playlist's title and its channel's name
// (playlists.list, 1 unit).
func (n *client) Playlist(id string) (title, author string, err error) {
	q := url.Values{"part": {"snippet"}, "id": {id}, "fields": {"items(snippet/title,snippet/channelTitle)"}}
	var r struct {
		Items []struct {
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := n.apiGet("playlists", q, &r); err != nil {
		return "", "", err
	}
	if len(r.Items) == 0 {
		return "", "", errNotFound
	}
	return cleanText(r.Items[0].Snippet.Title, maxNameRunes), cleanText(r.Items[0].Snippet.ChannelTitle, maxNameRunes), nil
}

// Items lists the newest public videos of a playlist (playlistItems.list,
// 1 unit for up to 50). Private and deleted videos stay in a playlist
// without a publication date; they are left out.
func (n *client) Items(playlist string, max int) ([]Video, error) {
	q := url.Values{"part": {"snippet,contentDetails,status"}, "playlistId": {playlist}, "maxResults": {strconv.Itoa(max)},
		"fields": {"items(snippet/title,contentDetails/videoId,contentDetails/videoPublishedAt,status/privacyStatus)"}}
	var r struct {
		Items []struct {
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
			ContentDetails struct {
				VideoID     string `json:"videoId"`
				PublishedAt string `json:"videoPublishedAt"`
			} `json:"contentDetails"`
			Status struct {
				Privacy string `json:"privacyStatus"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := n.apiGet("playlistItems", q, &r); err != nil {
		return nil, err
	}
	var out []Video
	for _, it := range r.Items {
		if len(out) >= max {
			break
		}
		id := it.ContentDetails.VideoID
		t, err := time.Parse(time.RFC3339, it.ContentDetails.PublishedAt)
		if !videoID.MatchString(id) || err != nil || (it.Status.Privacy != "public" && it.Status.Privacy != "") {
			continue
		}
		out = append(out, Video{ID: id, Title: cleanText(it.Snippet.Title, maxTitleRunes), Published: t.Unix()})
	}
	return out, nil
}

// Details adds durations and live states (videos.list, 1 unit for up to 50
// ids).
func (n *client) Details(videos []Video) error {
	if len(videos) == 0 {
		return nil
	}
	ids := make([]string, len(videos))
	for i, v := range videos {
		ids[i] = v.ID
	}
	q := url.Values{"part": {"contentDetails,snippet"}, "id": {strings.Join(ids, ",")},
		"fields": {"items(id,contentDetails/duration,snippet/liveBroadcastContent)"}}
	var r struct {
		Items []struct {
			ID             string `json:"id"`
			ContentDetails struct {
				Duration string `json:"duration"`
			} `json:"contentDetails"`
			Snippet struct {
				Live string `json:"liveBroadcastContent"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := n.apiGet("videos", q, &r); err != nil {
		return err
	}
	byID := map[string]int{}
	for i, v := range videos {
		byID[v.ID] = i
	}
	for _, it := range r.Items {
		i, ok := byID[it.ID]
		if !ok {
			continue
		}
		videos[i].Duration = ParseDuration(it.ContentDetails.Duration)
		if it.Snippet.Live == "live" || it.Snippet.Live == "upcoming" {
			videos[i].Live = it.Snippet.Live
		}
	}
	return nil
}

// ISO 8601 durations as the API writes them: PT15M33S, PT1H2M3S, P1DT2H.
var durationRe = regexp.MustCompile(`^P(?:(\d{1,3})D)?(?:T(?:(\d{1,3})H)?(?:(\d{1,4})M)?(?:(\d{1,6})S)?)?$`)

// ParseDuration returns seconds, or 0 for live streams (P0D) and anything
// unreadable.
func ParseDuration(s string) int {
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n := 0
	for i, mult := range []int{86400, 3600, 60, 1} {
		if m[i+1] != "" {
			x, _ := strconv.Atoi(m[i+1])
			n += x * mult
		}
	}
	return n
}
