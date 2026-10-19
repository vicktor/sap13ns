package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

const (
	gfdChannel = "UC_x5XG1OV2P6uZZ5FSM9Ttw" // Google for Developers
	gfdUploads = "UU_x5XG1OV2P6uZZ5FSM9Ttw"
	testKey    = "AIza-TEST-KEY-SECRET"
)

// t0 is the fake clock: three days after the newest video of the feeds.
var t0 = time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)

// fakeYouTube is an httptest server that answers like www.youtube.com (the
// Atom feeds) and www.googleapis.com (the Data API v3), with the response
// shapes of the documentation and the captured feeds in testdata/.
type fakeYouTube struct {
	srv *httptest.Server
	mu  sync.Mutex
	// reqs records every request as "host path?query".
	reqs []string
	keys []string // X-Goog-Api-Key of each API request
	// status forces an answer for a path ("/youtube/v3/playlistItems").
	status map[string]int
	body   map[string]string
	feeds  map[string]string // channel or playlist id → feed XML
}

func newFakeYouTube(t *testing.T) *fakeYouTube {
	t.Helper()
	f := &fakeYouTube{status: map[string]int{}, body: map[string]string{}, feeds: map[string]string{
		gfdChannel: readTestdata(t, "channel-feed.xml"),
		gfdUploads: readTestdata(t, "playlist-feed.xml"),
	}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (f *fakeYouTube) serve(w http.ResponseWriter, r *http.Request) {
	host := r.Header.Get("X-Test-Host")
	f.mu.Lock()
	f.reqs = append(f.reqs, host+" "+r.URL.Path+"?"+r.URL.RawQuery)
	if host == apiHost {
		f.keys = append(f.keys, r.Header.Get("X-Goog-Api-Key"))
	}
	st, forced := f.status[r.URL.Path]
	body := f.body[r.URL.Path]
	f.mu.Unlock()
	if forced {
		w.WriteHeader(st)
		io.WriteString(w, body)
		return
	}
	q := r.URL.Query()
	switch {
	case host == feedHost && r.URL.Path == "/feeds/videos.xml":
		id := q.Get("channel_id") + q.Get("playlist_id")
		feed, ok := f.feeds[id]
		if !ok {
			http.NotFound(w, r) // what YouTube answers for an unknown id
			return
		}
		w.Header().Set("Content-Type", "text/xml; charset=UTF-8")
		io.WriteString(w, feed)
	case host == apiHost && r.Header.Get("X-Goog-Api-Key") != testKey:
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":400,"message":"API key not valid. Please pass a valid API key.","errors":[{"message":"API key not valid. Please pass a valid API key.","domain":"global","reason":"badRequest"}]}}`)
	case host == apiHost && r.URL.Path == "/youtube/v3/channels":
		if q.Get("id") != gfdChannel && !strings.EqualFold(q.Get("forHandle"), "@googledevelopers") {
			io.WriteString(w, `{"kind":"youtube#channelListResponse","etag":"x","pageInfo":{"totalResults":0,"resultsPerPage":5}}`)
			return
		}
		io.WriteString(w, `{"items":[{"id":"`+gfdChannel+`","snippet":{"title":"Google for Developers"},"contentDetails":{"relatedPlaylists":{"likes":"","uploads":"`+gfdUploads+`"}}}]}`)
	case host == apiHost && r.URL.Path == "/youtube/v3/playlists":
		io.WriteString(w, `{"items":[{"snippet":{"title":"Gemini at I/O","channelTitle":"Google for Developers"}}]}`)
	case host == apiHost && r.URL.Path == "/youtube/v3/playlistItems":
		io.WriteString(w, `{"items":[`+
			`{"snippet":{"title":"Builders building with Gemini Audio"},"contentDetails":{"videoId":"LJ2v4-FvPcA","videoPublishedAt":"2026-10-16T21:00:00Z"},"status":{"privacyStatus":"public"}},`+
			`{"snippet":{"title":"Private video"},"contentDetails":{"videoId":"AAAAAAAAAAA"},"status":{"privacyStatus":"private"}},`+
			`{"snippet":{"title":"Deleted video"},"contentDetails":{"videoId":"BBBBBBBBBBB"},"status":{"privacyStatus":"privacyStatusUnspecified"}},`+
			`{"snippet":{"title":"Live: Gemini Q&A"},"contentDetails":{"videoId":"i3X7Fv0Z3Uc","videoPublishedAt":"2026-10-15T16:00:00Z"},"status":{"privacyStatus":"public"}},`+
			`{"snippet":{"title":"A long talk"},"contentDetails":{"videoId":"nSwmcs37Jd4","videoPublishedAt":"2026-10-14T09:30:00Z"},"status":{"privacyStatus":"public"}}`+
			`],"nextPageToken":"CAUQAA"}`)
	case host == apiHost && r.URL.Path == "/youtube/v3/videos":
		io.WriteString(w, `{"items":[`+
			`{"id":"LJ2v4-FvPcA","snippet":{"liveBroadcastContent":"none"},"contentDetails":{"duration":"PT58S"}},`+
			`{"id":"i3X7Fv0Z3Uc","snippet":{"liveBroadcastContent":"live"},"contentDetails":{"duration":"P0D"}},`+
			`{"id":"nSwmcs37Jd4","snippet":{"liveBroadcastContent":"none"},"contentDetails":{"duration":"PT1H2M3S"}}]}`)
	default:
		http.NotFound(w, r)
	}
}

// fetch is the sdktest HTTP hook: it sends the plugin's request to the
// httptest server, keeping the host it was meant for in a header.
func (f *fakeYouTube) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	u, err := url.Parse(req.URL)
	if err != nil {
		return nil, err
	}
	hreq, err := http.NewRequest(req.Method, f.srv.URL+u.RequestURI(), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range req.Headers {
		hreq.Header.Set(k, v)
	}
	hreq.Header.Set("X-Test-Host", u.Host)
	resp, err := f.srv.Client().Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return &pluginsdk.HTTPResponse{Status: resp.StatusCode, Body: string(b)}, nil
}

func (f *fakeYouTube) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reqs...)
}

// setup returns a fake host wired to a fake YouTube, with the plugin's
// http: permissions, a fixed clock and a fresh log-once memory.
func setup(t *testing.T) (*sdktest.Env, *fakeYouTube) {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "youtube"
	env.Site.Timezone = "Europe/Madrid"
	yt := newFakeYouTube(t)
	env.HTTP = yt.fetch
	env.HTTPHosts = []string{feedHost, apiHost}
	setClock(t, t0)
	logged = map[string]bool{}
	return env, yt
}

func setClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}
