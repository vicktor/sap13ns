package main

import (
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

func cron(t *testing.T, env *sdktest.Env) {
	t.Helper()
	if _, err := env.Event(pluginsdk.EventCronHourly, map[string]string{"time": now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
}

func shortcode(t *testing.T, env *sdktest.Env, attrs map[string]string) string {
	t.Helper()
	html, err := env.Shortcode("youtube_videos", attrs, "")
	if err != nil {
		t.Fatal(err)
	}
	return html
}

func stored(t *testing.T, env *sdktest.Env, source string) record {
	t.Helper()
	var r record
	if b, ok := env.KV[recordKey(source)]; ok {
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func logged_(env *sdktest.Env, sub string) bool {
	return slices.ContainsFunc(env.Logs, func(l sdktest.LogLine) bool { return strings.Contains(l.Text, sub) })
}

// The whole path without an API key: a shortcode shows a link and asks for
// its source, the hourly run fetches the feed, and the next render shows
// the gallery.
func TestShortcodeThenCron(t *testing.T) {
	env, yt := setup(t)
	attrs := map[string]string{"channel": "https://www.youtube.com/channel/" + gfdChannel, "count": "4", "layout": "carousel", "title": "Our videos"}

	html := shortcode(t, env, attrs)
	if !strings.Contains(html, `class="card ytg ytg-empty"`) || !strings.Contains(html, "Watch the videos on YouTube") {
		t.Fatalf("before the first fetch: %s", html)
	}
	if len(yt.requests()) != 0 {
		t.Fatal("a render made a request")
	}
	// The host renders a post's gallery again within five minutes, so the
	// list fetched below reaches the post without saving it.
	if r, err := env.Call(pluginsdk.HookShortcode, pluginsdk.Shortcode{Name: "youtube_videos", Attrs: attrs}); err != nil || r.MaxAge != 300 {
		t.Fatalf("max age %+v, %v", r, err)
	}
	if r, err := env.Call(pluginsdk.HookWidget, pluginsdk.Widget{Widget: "videos", Config: map[string]string{}}); err != nil || r.MaxAge != 0 {
		t.Fatalf("widget max age %+v, %v", r, err)
	}
	if n := countSlots(env); n != 1 {
		t.Fatalf("%d slots after the render, want 1", n)
	}

	cron(t, env)
	reqs := yt.requests()
	if len(reqs) != 1 || reqs[0] != feedHost+" /feeds/videos.xml?channel_id="+gfdChannel {
		t.Fatalf("requests: %q", reqs)
	}
	rec := stored(t, env, "c:"+gfdChannel)
	if len(rec.Videos) != 15 || rec.Title != "Google for Developers" || rec.Via != "feed" || rec.Fetched != t0.Unix() {
		t.Fatalf("record: %+v", rec)
	}

	html = shortcode(t, env, attrs)
	for _, w := range []string{`<section class="card ytg ytg-carousel" aria-label="Latest videos from Google for Developers on YouTube">`,
		`<h2 class="ytg-heading">Our videos</h2>`, `src="https://i.ytimg.com/vi/LJ2v4-FvPcA/hqdefault.jpg"`,
		`<time datetime="2026-10-16T21:00:00Z">Oct 16, 2026</time>`, "Thumbnails are loaded from YouTube."} {
		if !strings.Contains(html, w) {
			t.Errorf("missing %q in\n%s", w, html)
		}
	}
	if n := strings.Count(html, `<li class="ytg-item">`); n != 4 {
		t.Errorf("%d items, want 4", n)
	}

	// Within the hour nothing is fetched again.
	setClock(t, t0.Add(30*time.Minute))
	cron(t, env)
	if len(yt.requests()) != 1 {
		t.Fatalf("refetched too soon: %q", yt.requests())
	}
	setClock(t, t0.Add(time.Hour))
	cron(t, env)
	if len(yt.requests()) != 2 {
		t.Fatalf("not refreshed after an hour: %q", yt.requests())
	}
}

func countSlots(env *sdktest.Env) int {
	n := 0
	for k := range env.KV {
		if strings.HasPrefix(k, "want-") {
			n++
		}
	}
	return n
}

func TestDefaultsFromSettings(t *testing.T) {
	env, yt := setup(t)
	env.Plugin.Settings = map[string]string{"channel": "@GoogleDevelopers", "playlist": gfdUploads, "count": "3", "layout": "carousel",
		"thumbnails": "false", "notice_es": "Nota.", "privacy_url": "/privacidad"}
	env.Site.Language = "es"

	// The default source needs no slot: every run fetches it.
	shortcode(t, env, nil)
	if countSlots(env) != 0 {
		t.Fatal("the default source took a slot")
	}
	cron(t, env)
	if r := yt.requests(); len(r) != 1 || !strings.HasSuffix(r[0], "playlist_id="+gfdUploads) {
		t.Fatalf("requests: %q (the playlist wins over the channel)", r)
	}
	html := shortcode(t, env, nil)
	for _, w := range []string{`class="card ytg ytg-carousel"`, `aria-label="Vídeos de la lista Uploads from Google for Developers en YouTube"`,
		`<p class="ytg-notice">Nota. <a href="/privacidad">Política de privacidad</a></p>`, `>Lista completa en YouTube</a>`} {
		if !strings.Contains(html, w) {
			t.Errorf("missing %q in\n%s", w, html)
		}
	}
	if strings.Contains(html, "<img") || strings.Count(html, `<li class="ytg-item">`) != 3 {
		t.Errorf("thumbnails or count wrong:\n%s", html)
	}

	// The widget defaults to a carousel; its config wins over the settings.
	html, err := env.Widget("videos", map[string]string{"layout": "grid", "count": "2", "playlist": gfdUploads})
	if err != nil || !strings.Contains(html, `<div class="ytg ytg-grid ytg-widget">`) || strings.Count(html, `<li class="ytg-item">`) != 2 {
		t.Fatalf("widget: %s, %v", html, err)
	}
	html, _ = env.Widget("videos", map[string]string{"layout": "carousel"})
	if !strings.Contains(html, `ytg-carousel ytg-widget`) {
		t.Fatalf("widget layout: %s", html)
	}
}

func TestHandleNeedsAKey(t *testing.T) {
	env, yt := setup(t)
	html := shortcode(t, env, map[string]string{"channel": "@GoogleDevelopers"})
	if !strings.Contains(html, `href="https://www.youtube.com/@GoogleDevelopers"`) || !logged_(env, "can only be looked up with a YouTube Data API key") {
		t.Fatalf("html %s\nlogs %+v", html, env.Logs)
	}
	cron(t, env)
	if len(yt.requests()) != 0 {
		t.Fatalf("requests without a way to resolve the handle: %q", yt.requests())
	}
	if rec := stored(t, env, "h:googledevelopers"); rec.Fails != 1 || !strings.Contains(rec.Err, "API key") {
		t.Fatalf("record: %+v", rec)
	}
}

// With a key: the handle is looked up once, the uploads playlist listed
// and the durations read, in three requests; the key is only ever in the
// header.
func TestDataAPI(t *testing.T) {
	env, yt := setup(t)
	env.Plugin.Settings = map[string]string{"api_key": testKey}
	attrs := map[string]string{"channel": "@GoogleDevelopers", "count": "24"}
	shortcode(t, env, attrs)
	cron(t, env)

	reqs := yt.requests()
	if len(reqs) != 3 || !strings.HasPrefix(reqs[0], apiHost+" /youtube/v3/channels?") || !strings.Contains(reqs[0], "forHandle=%40googledevelopers") ||
		!strings.Contains(reqs[1], "/youtube/v3/playlistItems?") || !strings.Contains(reqs[1], "playlistId="+gfdUploads) || !strings.Contains(reqs[1], "maxResults=24") ||
		!strings.Contains(reqs[2], "/youtube/v3/videos?") || !strings.Contains(reqs[2], "id=LJ2v4-FvPcA%2Ci3X7Fv0Z3Uc%2CnSwmcs37Jd4") {
		t.Fatalf("requests: %q", reqs)
	}
	for _, r := range reqs {
		if strings.Contains(r, testKey) || strings.Contains(r, "key=") {
			t.Fatalf("the key is in a URL: %s", r)
		}
	}
	if slices.ContainsFunc(yt.keys, func(k string) bool { return k != testKey }) {
		t.Fatalf("keys sent: %q", yt.keys)
	}
	for _, l := range env.Logs {
		if strings.Contains(l.Text, testKey) {
			t.Fatalf("the key was logged: %s", l.Text)
		}
	}
	rec := stored(t, env, "h:googledevelopers")
	if rec.Channel != gfdChannel || rec.Uploads != gfdUploads || rec.Via != "api" || len(rec.Videos) != 3 {
		t.Fatalf("record: %+v", rec)
	}
	if v := rec.Videos; v[0].Duration != 58 || v[1].Live != "live" || v[1].Duration != 0 || v[2].Duration != 3723 {
		t.Fatalf("details: %+v", v)
	}
	html := shortcode(t, env, attrs)
	for _, w := range []string{`>0:58</span>`, `>Live</span>`, `>1:02:03</span>`, `(1 h 2 min)`} {
		if !strings.Contains(html, w) {
			t.Errorf("missing %q in\n%s", w, html)
		}
	}
	if strings.Contains(html, "Private video") || strings.Contains(html, "Deleted video") {
		t.Errorf("private or deleted videos shown:\n%s", html)
	}

	// The next refresh skips the lookup: two requests.
	setClock(t, t0.Add(time.Hour))
	cron(t, env)
	if n := len(yt.requests()); n != 5 {
		t.Fatalf("%d requests after the second run, want 5: %q", n, yt.requests())
	}
}

// An exhausted quota falls back to the feed; a failure keeps the last good
// list, and a failing source waits longer and longer.
func TestFailuresKeepTheLastList(t *testing.T) {
	env, yt := setup(t)
	env.Plugin.Settings = map[string]string{"channel": gfdChannel, "api_key": testKey}
	yt.status["/youtube/v3/channels"] = 403
	yt.body["/youtube/v3/channels"] = `{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your <a href=\"/youtube/v3/getting-started#quota\">quota</a>.","errors":[{"reason":"quotaExceeded"}]}}`
	cron(t, env)
	rec := stored(t, env, "c:"+gfdChannel)
	if rec.Via != "feed" || len(rec.Videos) != 15 || !logged_(env, "channels: HTTP 403 quotaExceeded") {
		t.Fatalf("no fallback to the feed: %+v\n%+v", rec, env.Logs)
	}

	// Now everything fails.
	yt.status["/feeds/videos.xml"] = 500
	setClock(t, t0.Add(time.Hour))
	cron(t, env)
	rec = stored(t, env, "c:"+gfdChannel)
	if len(rec.Videos) != 15 || rec.Fails != 1 || rec.Fetched != t0.Unix() || !strings.Contains(rec.Err, "HTTP 500") {
		t.Fatalf("record after a failure: %+v", rec)
	}
	if html := shortcode(t, env, nil); !strings.Contains(html, `ytg-grid`) {
		t.Fatalf("the last good list is not shown: %s", html)
	}
	n := len(yt.requests())
	setClock(t, t0.Add(2*time.Hour)) // 1 h after the failure: retried
	cron(t, env)
	setClock(t, t0.Add(3*time.Hour)) // 1 h after the second failure: waits 2 h
	cron(t, env)
	if got := len(yt.requests()) - n; got != 2 { // one retry: the API call and the feed
		t.Fatalf("%d requests during the backoff, want 2: %q", got, yt.requests()[n:])
	}

	// After 30 days without a good fetch the list is no longer shown.
	setClock(t, t0.Add(31*24*time.Hour))
	if html := shortcode(t, env, nil); !strings.Contains(html, "ytg-empty") {
		t.Fatalf("a month-old list is still shown: %s", html)
	}
}

func TestNotFound(t *testing.T) {
	env, _ := setup(t)
	missing := "UC" + strings.Repeat("x", 22)
	shortcode(t, env, map[string]string{"channel": missing})
	cron(t, env)
	if rec := stored(t, env, "c:"+missing); rec.Fails != 1 || !strings.Contains(rec.Err, "not found") {
		t.Fatalf("record: %+v", rec)
	}
}

// After a post is published, sources that never had a good fetch are
// fetched at once; the others wait for the hourly run.
func TestPostEventsFetchOnlyNewSources(t *testing.T) {
	env, yt := setup(t)
	env.Plugin.Settings = map[string]string{"channel": gfdChannel}
	cron(t, env)
	shortcode(t, env, map[string]string{"playlist": gfdUploads})
	setClock(t, t0.Add(2*time.Hour))
	if _, err := env.Event(pluginsdk.EventPostPublished, pluginsdk.Post{ID: 1, Title: "x"}); err != nil {
		t.Fatal(err)
	}
	reqs := yt.requests()
	if len(reqs) != 2 || !strings.HasSuffix(reqs[1], "playlist_id="+gfdUploads) {
		t.Fatalf("requests: %q", reqs)
	}
	if _, err := env.Event(pluginsdk.EventPostUpdated, pluginsdk.Post{ID: 1, Title: "x"}); err != nil || len(yt.requests()) != 2 {
		t.Fatalf("an update refetched known sources: %q, %v", yt.requests(), err)
	}
}

// A run makes at most the host's four requests, stalest first; the rest
// waits for the next run.
func TestBudgetAndSlots(t *testing.T) {
	env, yt := setup(t)
	var ids []string
	for i := range slots + 1 {
		id := "UC" + strings.Repeat("a", 20) + strconv.Itoa(10+i)
		ids = append(ids, id)
		yt.feeds[id] = readTestdata(t, "channel-feed.xml")
		shortcode(t, env, map[string]string{"channel": id})
	}
	if countSlots(env) != slots || !logged_(env, "not fetched, because galleries already show 16 other channels and playlists") {
		t.Fatalf("%d slots; logs %+v", countSlots(env), env.Logs)
	}
	cron(t, env)
	if n := len(yt.requests()); n != httpBudget {
		t.Fatalf("%d requests in one run, want %d", n, httpBudget)
	}
	for range 3 {
		setClock(t, now().Add(10*time.Minute))
		cron(t, env)
	}
	if n := len(yt.requests()); n != slots {
		t.Fatalf("%d requests after four runs, want %d (each source once)", n, slots)
	}

	// A source no gallery showed for 30 days is forgotten with its list.
	setClock(t, t0.Add(31*24*time.Hour))
	shortcode(t, env, map[string]string{"channel": ids[0]}) // still in use
	cron(t, env)
	if countSlots(env) != 1 || stored(t, env, "c:"+ids[1]).S != "" || stored(t, env, "c:"+ids[0]).S == "" {
		t.Fatalf("pruning: %d slots", countSlots(env))
	}
}

// The scanner's dry run denies storage and network: nothing fails.
func TestDeniedStorage(t *testing.T) {
	env, _ := setup(t)
	env.Denied["kv"] = true
	env.Plugin.Settings = map[string]string{"channel": gfdChannel}
	if html := shortcode(t, env, nil); !strings.Contains(html, "ytg-empty") {
		t.Fatalf("render without storage: %s", html)
	}
	cron(t, env)
	if !logged_(env, "storage is not available") {
		t.Fatalf("logs: %+v", env.Logs)
	}
}

func TestBadInput(t *testing.T) {
	env, _ := setup(t)
	if html := shortcode(t, env, nil); html != "" || !logged_(env, "no channel or playlist") {
		t.Fatalf("no source: %q %+v", html, env.Logs)
	}
	if html := shortcode(t, env, map[string]string{"channel": "https://www.youtube.com/c/Old"}); html != "" || !logged_(env, "old custom URL") {
		t.Fatalf("custom URL: %q", html)
	}
	env.Plugin.Settings = map[string]string{"channel": gfdChannel, "privacy_url": "javascript:alert(1)"}
	html := shortcode(t, env, map[string]string{"count": "lots", "layout": "masonry"})
	if strings.Contains(html, "javascript") || !logged_(env, `count “lots” is not a number`) || !logged_(env, `layout “masonry” is neither`) || !logged_(env, "privacy URL") {
		t.Fatalf("html %s\nlogs %+v", html, env.Logs)
	}
}

// TestManifest keeps plugin.json in step with the code: the hosts the
// plugin fetches are its http: permissions, and the hosts the page loads
// (thumbnails, player) are its CSP sources.
func TestManifest(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Permissions []string            `json:"permissions"`
		CSP         map[string][]string `json:"csp"`
		Shortcodes  []string            `json:"shortcodes"`
		Events      []string            `json:"events"`
		Widgets     []struct {
			ID     string `json:"id"`
			Config []struct {
				Key, Type, Default string
				Choices            []string
			} `json:"config"`
		} `json:"widgets"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"kv", "http:" + feedHost, "http:" + apiHost, "assets:js"} {
		if !slices.Contains(m.Permissions, p) {
			t.Errorf("permission %s missing", p)
		}
	}
	if len(m.Permissions) != 4 {
		t.Errorf("unexpected permissions: %v", m.Permissions)
	}
	html := Gallery(gfd, sample, view("en"))
	if !slices.Equal(m.CSP["img-src"], []string{"https://i.ytimg.com"}) || !strings.Contains(html, `src="https://i.ytimg.com/`) {
		t.Errorf("img-src %v", m.CSP["img-src"])
	}
	if !slices.Equal(m.CSP["frame-src"], []string{"https://www.youtube-nocookie.com"}) || !strings.Contains(html, `href="https://www.youtube-nocookie.com/embed/`) {
		t.Errorf("frame-src %v", m.CSP["frame-src"])
	}
	if !slices.Equal(m.Shortcodes, []string{"youtube_videos"}) || len(m.Widgets) != 1 || m.Widgets[0].ID != "videos" {
		t.Errorf("shortcodes %v widgets %+v", m.Shortcodes, m.Widgets)
	}
	for _, e := range []string{pluginsdk.EventCronHourly, pluginsdk.EventPostPublished, pluginsdk.EventPostUpdated} {
		if !slices.Contains(m.Events, e) {
			t.Errorf("event %s missing", e)
		}
	}
}
