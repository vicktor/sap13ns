package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/videothumb"
)

// fakeVideos points the server's poster cache at a YouTube and Vimeo
// stand-in on loopback and returns its request counter.
func (e *customEnv) fakeVideos() *atomic.Int32 {
	e.t.Helper()
	var hits atomic.Int32
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 640, 360)), nil)
	pic := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch {
		case r.URL.Path == "/oembed":
			json.NewEncoder(w).Encode(map[string]any{"title": "Never Gonna"})
		case r.URL.Path == "/api/oembed.json":
			json.NewEncoder(w).Encode(map[string]any{"title": "The Mountain", "width": 1280, "height": 720, "thumbnail_url": "http://" + r.Host + "/vimeo.jpg"})
		case strings.HasSuffix(r.URL.Path, ".jpg"):
			w.Write(pic)
		default:
			http.NotFound(w, r)
		}
	}))
	e.t.Cleanup(srv.Close)
	c := videothumb.New(videothumb.Options{
		Dir:       e.t.TempDir(),
		Endpoints: videothumb.Endpoints{YouTubeOEmbed: srv.URL + "/oembed", YouTubeImages: srv.URL + "/vi/", VimeoOEmbed: srv.URL + "/api/oembed.json"},
		Origins:   []string{srv.URL},
		Allow:     func(a netip.Addr) bool { return a.IsLoopback() },
	})
	c.OnChange(func(string) { e.s.pages.Invalidate(); e.s.content.Invalidate() })
	e.s.videos = c
	return &hits
}

func (e *customEnv) setVideoEmbeds(mode string) {
	e.t.Helper()
	st := e.s.settings()
	st.VideoEmbeds = mode
	if err := e.st.SaveSettings(context.Background(), st); err != nil {
		e.t.Fatal(err)
	}
	if err := e.s.ReloadSettings(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.s.contentChanged()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

const framePlayers = "frame-src https://www.youtube-nocookie.com https://player.vimeo.com"

func TestVideoEmbedPage(t *testing.T) {
	e := newCustomEnv(t)
	hits := e.fakeVideos()
	path := e.publishPost("Video post", "Intro.\n\nhttps://youtu.be/dQw4w9WgXcQ?t=90\n\nIn a sentence https://vimeo.com/22439234 stays a link.\n")
	plain := e.publishPost("Plain post", "No video here.\n")

	rec := e.get(path, nil)
	body, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	if rec.Code != http.StatusOK || rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Header().Get("X-Cache"))
	}
	for _, s := range []string{
		`<figure class="sap-video sap-video-youtube" data-video-provider="youtube">`,
		`data-video-src="https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ?autoplay=1&amp;start=90"`,
		`href="https://www.youtube.com/watch?v=dQw4w9WgXcQ&amp;t=90s"`,
		`/static/video.js`,
		`<a href="https://vimeo.com/22439234">https://vimeo.com/22439234</a>`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %s", s)
		}
	}
	// Nothing on the page loads from the provider before the click.
	if strings.Contains(body, "<iframe") || strings.Contains(body, ` src="https://`) {
		t.Error("the page loads something from the provider")
	}
	if !strings.Contains(csp, framePlayers) || strings.Contains(csp, "frame-src 'none'") || !strings.Contains(csp, "script-src 'self';") {
		t.Errorf("CSP of a page with a video: %s", csp)
	}
	// Signed in (no cache) too.
	if c := e.get(path, e.admin).Header().Get("Content-Security-Policy"); !strings.Contains(c, framePlayers) {
		t.Errorf("CSP signed in: %s", c)
	}

	// Pages without a video keep frame-src 'none' and load no player script.
	for _, p := range []string{plain, "/"} {
		rec = e.get(p, nil)
		if c := rec.Header().Get("Content-Security-Policy"); !strings.Contains(c, "frame-src 'none'") || strings.Contains(c, "youtube") {
			t.Errorf("CSP of %s: %s", p, c)
		}
		if strings.Contains(rec.Body.String(), "video.js") {
			t.Errorf("%s loads video.js", p)
		}
	}

	// The poster arrives in the background and the page shows it.
	waitFor(t, "the poster", func() bool {
		body = e.get(path, nil).Body.String()
		return strings.Contains(body, "sap-video-poster")
	})
	// The page cache keeps the page's own policy.
	rec = e.get(path, nil)
	if rec.Header().Get("X-Cache") != "HIT" || rec.Header().Get("Content-Security-Policy") != csp {
		t.Errorf("cached: %s %s", rec.Header().Get("X-Cache"), rec.Header().Get("Content-Security-Policy"))
	}
	for _, s := range []string{`<img class="sap-video-poster" src="/video-thumbs/yt-dQw4w9WgXcQ.jpg" alt="" width="640" height="360"`,
		`<span class="sap-video-title">Never Gonna</span>`, `data-video-label="Reproducir vídeo: Never Gonna (YouTube)"`, `Ver en YouTube`} { // the site is in Spanish
		if !strings.Contains(body, s) {
			t.Errorf("after the fetch: missing %s", s)
		}
	}
	rec = e.get("/video-thumbs/yt-dQw4w9WgXcQ.jpg", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("poster: %d %v", rec.Code, rec.Header())
	}
	if _, _, err := image.Decode(rec.Body); err != nil {
		t.Errorf("poster: %v", err)
	}
	for _, p := range []string{"/video-thumbs/yt-aaaaaaaaaaa.jpg", "/video-thumbs/..%2Fx.jpg", "/video-thumbs/yt-dQw4w9WgXcQ.json"} {
		if rec := e.get(p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if hits.Load() == 0 {
		t.Error("the fake provider was never asked")
	}
}

func TestVideoEmbedModes(t *testing.T) {
	e := newCustomEnv(t)
	hits := e.fakeVideos()
	path := e.publishPost("Modes", "[video url=\"https://vimeo.com/22439234\" title=\"Our talk\"]\n")

	e.setVideoEmbeds("plain")
	rec := e.get(path, nil)
	body := rec.Body.String()
	if !strings.Contains(body, `<figure class="sap-video sap-video-vimeo"`) || strings.Contains(body, "sap-video-poster") ||
		!strings.Contains(body, `<span class="sap-video-title">Our talk</span>`) || !strings.Contains(rec.Header().Get("Content-Security-Policy"), framePlayers) {
		t.Errorf("plain: %s", body)
	}
	time.Sleep(50 * time.Millisecond)
	if hits.Load() != 0 {
		t.Errorf("plain mode contacted the provider %d times", hits.Load())
	}

	e.setVideoEmbeds("off")
	rec = e.get(path, nil)
	body = rec.Body.String()
	if strings.Contains(body, "sap-video") || !strings.Contains(body, `<a href="https://vimeo.com/22439234">Our talk</a>`) ||
		!strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-src 'none'") {
		t.Errorf("off: %s", body)
	}
}

func TestVideoEmbedFeed(t *testing.T) {
	e := newCustomEnv(t)
	e.fakeVideos()
	e.publishPost("Feed video", "https://www.youtube.com/watch?v=dQw4w9WgXcQ\n")
	body := e.get("/feed.xml", nil).Body.String()
	if !strings.Contains(body, "https://www.youtube.com/watch?v=dQw4w9WgXcQ") || strings.Contains(body, "sap-video") ||
		strings.Contains(body, "youtube-nocookie") || strings.Contains(body, "data-video-src") {
		t.Errorf("feed: %s", body)
	}
	waitFor(t, "the poster in the feed", func() bool {
		body = e.get("/feed.json", nil).Body.String()
		return strings.Contains(body, `http://example.test/video-thumbs/yt-dQw4w9WgXcQ.jpg`)
	})
}

func TestVideoEditor(t *testing.T) {
	e := newCustomEnv(t)
	e.fakeVideos()
	// The editor may frame the players; other admin pages may not.
	if c := e.get("/admin/posts/new", e.author).Header().Get("Content-Security-Policy"); !strings.Contains(c, framePlayers) {
		t.Errorf("editor CSP: %s", c)
	}
	if c := e.get("/admin/", e.author).Header().Get("Content-Security-Policy"); !strings.Contains(c, "frame-src 'none'") {
		t.Errorf("dashboard CSP: %s", c)
	}

	rec := e.get("/admin/api/video?url="+strings.ReplaceAll("https://vimeo.com/22439234#t=1m", "#", "%23"), e.author)
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != http.StatusOK || got["provider"] != "vimeo" || got["title"] != "The Mountain" || got["poster"] != "/video-thumbs/vm-22439234.jpg" ||
		got["player"] != "https://player.vimeo.com/video/22439234?autoplay=1&dnt=1#t=60s" {
		t.Errorf("api: %d %v", rec.Code, got)
	}
	rec = e.get("/admin/api/video?url=https://youtube.com.evil.test/watch?v=dQw4w9WgXcQ", e.author)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "YouTube o Vimeo") {
		t.Errorf("bad URL: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.get("/admin/api/video?url=https://youtu.be/dQw4w9WgXcQ", nil); rec.Code == http.StatusOK {
		t.Errorf("anonymous: %d", rec.Code)
	}

	// The preview waits for a new video's poster.
	req := httptest.NewRequest("POST", "http://example.test/admin/api/preview", strings.NewReader(`{"content":"https://youtu.be/dQw4w9WgXcQ\n"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", e.acsrf)
	rec = e.do(req, e.author)
	var prev struct{ HTML string }
	json.Unmarshal(rec.Body.Bytes(), &prev)
	if !strings.Contains(prev.HTML, `<img class="sap-video-poster" src="/video-thumbs/yt-dQw4w9WgXcQ.jpg"`) || !strings.Contains(prev.HTML, "Never Gonna") {
		t.Errorf("preview: %d %s", rec.Code, rec.Body.String())
	}
}

func TestImageLayoutFromMedia(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	if _, err := e.st.CreateMedia(ctx, &store.Media{Path: "2026/10/abcdefabcdefabcdefab.jpg", OriginalName: "a.jpg", MIME: "image/jpeg", Size: 10,
		Width: 2000, Height: 1000, SHA256: strings.Repeat("a", 64), Variants: map[string]store.MediaVariant{
			"thumb":  {Path: "2026/10/abcdefabcdefabcdefab-thumb.jpg", Width: 480, Height: 240},
			"medium": {Path: "2026/10/abcdefabcdefabcdefab-medium.jpg", Width: 1024, Height: 512},
			"large":  {Path: "2026/10/abcdefabcdefabcdefab-large.jpg", Width: 1920, Height: 960},
		}}); err != nil {
		t.Fatal(err)
	}
	path := e.publishPost("Images", "![Left](/uploads/2026/10/abcdefabcdefabcdefab-medium.jpg){width=400 align=left} Text flows around it.\n\n"+
		"![Other](/uploads/2026/10/unknown.jpg){width=300 align=right}\n")
	body := e.get(path, nil).Body.String()
	for _, s := range []string{
		`<img src="/uploads/2026/10/abcdefabcdefabcdefab-thumb.jpg" alt="Left" loading="lazy" decoding="async" class="align-left" width="400" height="200" srcset="/uploads/2026/10/abcdefabcdefabcdefab-thumb.jpg 480w, /uploads/2026/10/abcdefabcdefabcdefab-medium.jpg 1024w, /uploads/2026/10/abcdefabcdefabcdefab-large.jpg 1920w, /uploads/2026/10/abcdefabcdefabcdefab.jpg 2000w" sizes="(max-width: 640px) 100vw, 400px"/>`,
		`<img src="/uploads/2026/10/unknown.jpg" alt="Other" loading="lazy" decoding="async" class="align-right" width="300"/>`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("missing %s", s)
		}
	}
	if strings.Contains(body, "style=") && strings.Contains(body, `style="`+"float") {
		t.Error("inline style in content")
	}
}
