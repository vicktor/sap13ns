package videothumb

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/content"
)

func encode(t *testing.T, w, h int, asPNG bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, h/2, color.RGBA{200, 30, 30, 255})
	}
	var buf bytes.Buffer
	var err error
	if asPNG {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fake is a YouTube and Vimeo stand-in on a loopback address.
type fake struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newFake(t *testing.T) *fake {
	f := &fake{hits: map[string]int{}}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	count := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.hits[r.URL.Path]++
			f.mu.Unlock()
			h(w, r)
		}
	}
	placeholder, hq, big := encode(t, 120, 90, false), encode(t, 480, 360, false), encode(t, 2000, 1000, true)
	mux.HandleFunc("/oembed", count(func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		switch {
		case strings.Contains(u, "playlist?list="):
			json.NewEncoder(w).Encode(map[string]any{"title": "A  playlist", "thumbnail_url": f.URL + "/vi/dQw4w9WgXcQ/hqdefault.jpg"})
		case strings.Contains(u, "v=dQw4w9WgXcQ"):
			json.NewEncoder(w).Encode(map[string]any{"title": "Never\nGonna", "thumbnail_url": "ignored"})
		default:
			http.NotFound(w, r)
		}
	}))
	mux.HandleFunc("/vi/", count(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vi/dQw4w9WgXcQ/maxresdefault.jpg", "/vi/aqz-KE-bpKQ/maxresdefault.jpg":
			w.WriteHeader(http.StatusNotFound) // YouTube sends a placeholder with the 404
			w.Write(placeholder)
		case "/vi/dQw4w9WgXcQ/hqdefault.jpg":
			w.Write(hq)
		case "/vi/aqz-KE-bpKQ/hqdefault.jpg":
			w.Write([]byte("<html>not a picture</html>"))
		case "/vi/xxxxxxxxxx1/maxresdefault.jpg", "/vi/xxxxxxxxxx2/maxresdefault.jpg", "/vi/xxxxxxxxxx3/maxresdefault.jpg":
			w.Write(hq)
		default:
			http.NotFound(w, r)
		}
	}))
	mux.HandleFunc("/api/oembed.json", count(func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		switch {
		case u == "https://vimeo.com/22439234" && r.URL.Query().Get("width") == "1280":
			json.NewEncoder(w).Encode(map[string]any{"title": "The Mountain", "width": 1280, "height": 720, "thumbnail_url": f.URL + "/vimeo/145027281-d_1280"})
		case u == "https://vimeo.com/76979871":
			json.NewEncoder(w).Encode(map[string]any{"title": "Elsewhere", "width": 720, "height": 1280, "thumbnail_url": "https://evil.test/a.jpg"})
		case u == "https://vimeo.com/111":
			json.NewEncoder(w).Encode(map[string]any{"title": "Redirected", "thumbnail_url": f.URL + "/redirect"})
		default:
			http.NotFound(w, r)
		}
	}))
	mux.HandleFunc("/vimeo/", count(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	mux.HandleFunc("/redirect", count(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data", http.StatusFound)
	}))
	return f
}

func (f *fake) hit(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newCache(t *testing.T, f *fake, dir string, clk *clock, mod func(*Options)) (*Cache, *[]string) {
	var changed []string
	var mu sync.Mutex
	o := Options{
		Dir:       dir,
		Endpoints: Endpoints{YouTubeOEmbed: f.URL + "/oembed", YouTubeImages: f.URL + "/vi/", VimeoOEmbed: f.URL + "/api/oembed.json"},
		Origins:   []string{f.URL},
		// The fake listens on loopback, which netguard refuses unless told.
		Allow:    func(a netip.Addr) bool { return a.IsLoopback() },
		Now:      clk.now,
		OnChange: func(k string) { mu.Lock(); changed = append(changed, k); mu.Unlock() },
	}
	if mod != nil {
		mod(&o)
	}
	return New(o), &changed
}

func sync1(fn func()) { fn() }

func mustVideo(t *testing.T, raw string) content.Video {
	v, ok := content.ParseVideoURL(raw)
	if !ok {
		t.Fatalf("not a video: %s", raw)
	}
	return v
}

func TestYouTubePoster(t *testing.T) {
	f := newFake(t)
	clk := &clock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	c, changed := newCache(t, f, dir, clk, nil)
	v := mustVideo(t, "https://youtu.be/dQw4w9WgXcQ?t=90")

	if _, due := c.Lookup(v); !due {
		t.Fatal("empty cache: Lookup should ask for a fetch")
	}
	c.Prefetch(v, sync1)
	m, due := c.Lookup(v)
	if due || m.Title != "Never Gonna" || !m.Poster || m.Width != 480 || m.Height != 360 {
		t.Fatalf("after fetch: %+v due=%v", m, due)
	}
	if len(*changed) != 1 || (*changed)[0] != "yt-dQw4w9WgXcQ" {
		t.Errorf("OnChange: %v", *changed)
	}
	// The placeholder that comes with maxresdefault's 404 was not used.
	if f.hit("/vi/dQw4w9WgXcQ/maxresdefault.jpg") != 1 || f.hit("/vi/dQw4w9WgXcQ/hqdefault.jpg") != 1 {
		t.Errorf("hits: %v", f.hits)
	}
	path, ok := c.File("yt-dQw4w9WgXcQ.jpg")
	if !ok {
		t.Fatal("File: poster missing")
	}
	b, _ := os.ReadFile(path)
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(b)); err != nil || format != "jpeg" || cfg.Width != 480 {
		t.Errorf("poster: %v %s %+v", err, format, cfg)
	}

	// Fresh: nothing more is fetched.
	c.Prefetch(v, sync1)
	if f.hit("/oembed") != 1 {
		t.Errorf("fresh entry fetched again: %v", f.hits)
	}
	// Stale after the TTL: the old entry is served and refreshed.
	clk.add(31 * 24 * time.Hour)
	if m, due := c.Lookup(v); !due || !m.Poster {
		t.Errorf("stale: %+v due=%v", m, due)
	}
	c.Prefetch(v, sync1)
	if f.hit("/oembed") != 2 || len(*changed) != 1 {
		t.Errorf("refresh: hits %v, changed %v (same data: no change)", f.hits, *changed)
	}

	// Another cache on the same directory reads the files back.
	c2, _ := newCache(t, f, dir, clk, nil)
	if m, due := c2.Lookup(v); due || m.Title != "Never Gonna" || !m.Poster {
		t.Errorf("reloaded: %+v due=%v", m, due)
	}
}

func TestVimeoPosterAndPlaylist(t *testing.T) {
	f := newFake(t)
	clk := &clock{t: time.Now()}
	c, _ := newCache(t, f, t.TempDir(), clk, nil)

	m := c.Fetch(t.Context(), mustVideo(t, "https://vimeo.com/22439234"))
	if m.Title != "The Mountain" || !m.Poster || m.Width != 1280 || m.Height != 640 || m.Portrait {
		t.Errorf("vimeo: %+v", m)
	}
	m = c.Fetch(t.Context(), mustVideo(t, "https://www.youtube.com/playlist?list=PLFgquLnL59alCl_2TQvOiD5Vgm1hCaGSI"))
	if m.Title != "A playlist" || !m.Poster {
		t.Errorf("playlist: %+v", m)
	}
	// A thumbnail on another host is never requested; the title is kept.
	m = c.Fetch(t.Context(), mustVideo(t, "https://vimeo.com/76979871"))
	if m.Title != "Elsewhere" || m.Poster || !m.Portrait {
		t.Errorf("foreign thumbnail: %+v", m)
	}
	// Nor is a redirect to another host followed.
	m = c.Fetch(t.Context(), mustVideo(t, "https://vimeo.com/111"))
	if m.Title != "Redirected" || m.Poster {
		t.Errorf("redirect: %+v", m)
	}
	// Something that is not a picture is refused.
	m = c.Fetch(t.Context(), mustVideo(t, "https://www.youtube.com/shorts/aqz-KE-bpKQ"))
	if m.Poster {
		t.Errorf("html as a picture: %+v", m)
	}
	if _, ok := c.File("yt-aqz-KE-bpKQ.jpg"); ok {
		t.Error("File serves a poster that was refused")
	}
}

func TestNetguardRefusesLoopback(t *testing.T) {
	f := newFake(t)
	clk := &clock{t: time.Now()}
	c, changed := newCache(t, f, t.TempDir(), clk, func(o *Options) { o.Allow = nil })
	v := mustVideo(t, "https://youtu.be/dQw4w9WgXcQ")
	if m := c.Fetch(t.Context(), v); m.Poster || m.Title != "" {
		t.Errorf("fetched through the guard: %+v", m)
	}
	if f.hit("/oembed") != 0 || len(*changed) != 0 {
		t.Errorf("the fake was reached: %v", f.hits)
	}
	// The failure is remembered for an hour.
	if _, due := c.Lookup(v); due {
		t.Error("failure not remembered")
	}
	clk.add(61 * time.Minute)
	if _, due := c.Lookup(v); !due {
		t.Error("failure never retried")
	}
}

func TestBounds(t *testing.T) {
	f := newFake(t)
	clk := &clock{t: time.Now()}
	dir := t.TempDir()
	c, _ := newCache(t, f, dir, clk, func(o *Options) { o.MaxEntries = 2 })
	for _, id := range []string{"xxxxxxxxxx1", "xxxxxxxxxx2", "xxxxxxxxxx3"} {
		clk.add(time.Minute)
		if m := c.Fetch(t.Context(), mustVideo(t, "https://youtu.be/"+id)); !m.Poster {
			t.Fatalf("%s: %+v", id, m)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "yt-xxxxxxxxxx1.jpg")); !os.IsNotExist(err) {
		t.Errorf("oldest poster kept: %v", err)
	}
	for _, k := range []string{"yt-xxxxxxxxxx2", "yt-xxxxxxxxxx3"} {
		if _, ok := c.File(k + ".jpg"); !ok {
			t.Errorf("%s evicted", k)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(files) != 4 {
		t.Errorf("files: %v", files)
	}

	// The byte bound works the same way.
	dir2 := t.TempDir()
	c2, _ := newCache(t, f, dir2, clk, func(o *Options) { o.MaxBytes = 1 })
	c2.Fetch(t.Context(), mustVideo(t, "https://youtu.be/xxxxxxxxxx1"))
	clk.add(time.Minute)
	c2.Fetch(t.Context(), mustVideo(t, "https://youtu.be/xxxxxxxxxx2"))
	if _, ok := c2.File("yt-xxxxxxxxxx1.jpg"); ok {
		t.Error("byte bound not kept")
	}
	if _, ok := c2.File("yt-xxxxxxxxxx2.jpg"); !ok {
		t.Error("the newest entry must stay")
	}
}

func TestFileNames(t *testing.T) {
	c := New(Options{Dir: t.TempDir()})
	for _, name := range []string{"../etc/passwd", "yt-dQw4w9WgXcQ.json", "yt-short.jpg", "yt-dQw4w9WgXcQ.jpg/..", "vm-0.jpg", "x.jpg", ""} {
		if _, ok := c.File(name); ok {
			t.Errorf("File(%q) ok", name)
		}
	}
	if PosterURL("vm-22439234") != "/video-thumbs/vm-22439234.jpg" {
		t.Error(PosterURL("vm-22439234"))
	}
}
