// Package videothumb fetches and caches the posters and titles of the
// YouTube and Vimeo videos that posts embed, so that the click-to-load
// player (content.Video) shows a real picture without the visitor's
// browser contacting the provider before they press play.
//
// The server makes the requests, through the SSRF guard (netguard) and to
// the providers' own hosts only:
//
//   - YouTube: the oEmbed endpoint (https://www.youtube.com/oembed) for the
//     title, and https://i.ytimg.com/vi/ID/maxresdefault.jpg, falling back to
//     hqdefault.jpg, for the picture. Playlists take the picture oEmbed names.
//   - Vimeo: the oEmbed endpoint (https://vimeo.com/api/oembed.json), which
//     gives the title and a thumbnail on i.vimeocdn.com.
//
// Sources: the oEmbed format (https://oembed.com/), YouTube's thumbnail
// sizes, maxres 1280x720 and high 480x360, of which maxres is not there for
// every video (https://developers.google.com/youtube/v3/docs/thumbnails),
// and Vimeo's oEmbed API (https://developer.vimeo.com/api/oembed/videos).
//
// Every picture is sniffed, decoded within size limits and re-encoded as a
// JPEG at most 1280 pixels wide, so nothing the provider sent is served
// as it came. The cache is bounded (entries and bytes, oldest out first),
// entries are refreshed after 30 days (the stale one is served meanwhile)
// and failures are retried after an hour. Rendering never waits for it:
// Lookup only reads what is there and Prefetch fetches in the background.
package videothumb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // decoders for the providers' pictures
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"sap13ns/internal/content"
	"sap13ns/internal/media"
	"sap13ns/internal/netguard"
)

// Meta is what the cache knows about one video.
type Meta struct {
	Title    string    `json:"title,omitempty"`
	Poster   bool      `json:"poster,omitempty"` // KEY.jpg exists
	Width    int       `json:"width,omitempty"`  // poster size
	Height   int       `json:"height,omitempty"`
	Portrait bool      `json:"portrait,omitempty"` // a vertical video (Vimeo says)
	Size     int64     `json:"size,omitempty"`     // bytes of KEY.jpg
	Fetched  time.Time `json:"fetched"`
	failed   bool      // nothing could be fetched (kept in memory only)
}

// Endpoints are the providers' URLs; tests point them at a fake server.
type Endpoints struct {
	YouTubeOEmbed string // https://www.youtube.com/oembed
	YouTubeImages string // https://i.ytimg.com/vi/
	VimeoOEmbed   string // https://vimeo.com/api/oembed.json
}

// DefaultEndpoints are the real providers.
var DefaultEndpoints = Endpoints{
	YouTubeOEmbed: "https://www.youtube.com/oembed",
	YouTubeImages: "https://i.ytimg.com/vi/",
	VimeoOEmbed:   "https://vimeo.com/api/oembed.json",
}

// DefaultOrigins are the only origins requests (and their redirects) may go
// to.
var DefaultOrigins = []string{"https://www.youtube.com", "https://i.ytimg.com", "https://vimeo.com", "https://i.vimeocdn.com"}

// Options configure a Cache. Zero values take the defaults.
type Options struct {
	Dir        string // where KEY.jpg and KEY.json live
	Endpoints  Endpoints
	Origins    []string              // allowed scheme://host of every request
	Allow      func(netip.Addr) bool // addresses that may be dialled (netguard.Public)
	MaxEntries int                   // 1000
	MaxBytes   int64                 // 64 MiB
	TTL        time.Duration         // 30 days
	RetryAfter time.Duration         // 1 hour
	Timeout    time.Duration         // per request, 10 s
	Now        func() time.Time      // time.Now
	OnChange   func(key string)      // a video's title or poster changed
	Log        *slog.Logger          // slog.Default()
	Client     *http.Client          // built from the above when nil
}

// Limits of one download and of a picture.
const (
	maxDownload = 4 << 20
	maxOEmbed   = 256 << 10
	maxSide     = 4096
	posterWidth = 1280
	maxPending  = 32
)

// Cache is the poster cache of one site.
type Cache struct {
	o       Options
	client  *http.Client
	origins map[string]bool

	mu      sync.Mutex
	loaded  bool
	entries map[string]*Meta
	pending map[string]chan struct{}
	sem     chan struct{}

	listeners []func(key string)
}

// OnChange adds fn to the functions called after a video's title or
// poster changed (on top of Options.OnChange).
func (c *Cache) OnChange(fn func(key string)) {
	c.mu.Lock()
	c.listeners = append(c.listeners, fn)
	c.mu.Unlock()
}

// New returns a cache that stores its files in o.Dir.
func New(o Options) *Cache {
	if o.Endpoints == (Endpoints{}) {
		o.Endpoints = DefaultEndpoints
	}
	if o.Origins == nil {
		o.Origins = DefaultOrigins
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = 1000
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = 64 << 20
	}
	if o.TTL <= 0 {
		o.TTL = 30 * 24 * time.Hour
	}
	if o.RetryAfter <= 0 {
		o.RetryAfter = time.Hour
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	c := &Cache{o: o, origins: map[string]bool{}, entries: map[string]*Meta{}, pending: map[string]chan struct{}{}, sem: make(chan struct{}, 2)}
	for _, s := range o.Origins {
		c.origins[strings.TrimSuffix(s, "/")] = true
	}
	c.client = o.Client
	if c.client == nil {
		c.client = &http.Client{
			Timeout: o.Timeout,
			Transport: &http.Transport{
				DialContext:           netguard.Dialer(o.Allow, nil, 5*time.Second),
				Proxy:                 nil,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: o.Timeout,
				MaxIdleConns:          4,
				IdleConnTimeout:       30 * time.Second,
			},
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return errors.New("videothumb: too many redirects")
				}
				if !c.allowed(r.URL) {
					return fmt.Errorf("videothumb: redirect to %s refused", r.URL.Host)
				}
				return nil
			},
		}
	}
	return c
}

// keyRe matches Video.Key(): it is also the file name, so it is checked
// again before anything touches the disk.
var keyRe = regexp.MustCompile(`^(?:yt-[A-Za-z0-9_-]{11}|ytpl-[A-Za-z0-9_-]{10,64}|vm-[1-9][0-9]{0,11})$`)

// PosterURL is where the site serves a cached poster.
func PosterURL(key string) string { return "/video-thumbs/" + key + ".jpg" }

// File returns the path of a cached poster by its file name (KEY.jpg).
func (c *Cache) File(name string) (string, bool) {
	key, ok := strings.CutSuffix(name, ".jpg")
	if !ok || !keyRe.MatchString(key) {
		return "", false
	}
	c.mu.Lock()
	c.load()
	m := c.entries[key]
	c.mu.Unlock()
	if m == nil || !m.Poster {
		return "", false
	}
	return filepath.Join(c.o.Dir, name), true
}

// Lookup returns what is cached for v and whether it should be fetched
// (missing, older than the TTL, or failed more than RetryAfter ago). It
// never blocks on the network.
func (c *Cache) Lookup(v content.Video) (Meta, bool) {
	key := v.Key()
	if !keyRe.MatchString(key) {
		return Meta{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	m := c.entries[key]
	if m == nil {
		return Meta{}, true
	}
	age := c.o.Now().Sub(m.Fetched)
	if m.failed {
		return Meta{}, age > c.o.RetryAfter
	}
	if !m.Poster {
		return *m, age > c.o.RetryAfter // a title without a picture: try again soon
	}
	return *m, age > c.o.TTL
}

// Prefetch fetches v in the background when Lookup says so and no fetch of
// it is running; run starts the goroutine (the server's job tracker).
func (c *Cache) Prefetch(v content.Video, run func(func())) {
	if _, due := c.Lookup(v); !due {
		return
	}
	key := v.Key()
	c.mu.Lock()
	if _, busy := c.pending[key]; busy || len(c.pending) >= maxPending {
		c.mu.Unlock()
		return
	}
	done := make(chan struct{})
	c.pending[key] = done
	c.mu.Unlock()
	run(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*c.o.Timeout)
		defer cancel()
		c.refresh(ctx, v, done)
	})
}

// Fetch returns v's entry, fetching it first when Lookup says so (waiting
// for a fetch already running). The admin editor uses it to show a poster
// right after a URL is pasted.
func (c *Cache) Fetch(ctx context.Context, v content.Video) Meta {
	if m, due := c.Lookup(v); !due {
		return m
	}
	key := v.Key()
	c.mu.Lock()
	if done, busy := c.pending[key]; busy {
		c.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		m, _ := c.Lookup(v)
		return m
	}
	done := make(chan struct{})
	c.pending[key] = done
	c.mu.Unlock()
	c.refresh(ctx, v, done)
	m, _ := c.Lookup(v)
	return m
}

func (c *Cache) refresh(ctx context.Context, v content.Video, done chan struct{}) {
	key := v.Key()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		close(done)
	}()
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return
	}
	m, jpg, err := c.fetch(ctx, v)
	if err != nil {
		c.o.Log.Info("video poster not fetched", "video", key, "err", err)
	}
	c.store(key, m, jpg)
}

// fetch asks the provider for the title and the picture.
func (c *Cache) fetch(ctx context.Context, v content.Video) (Meta, []byte, error) {
	var m Meta
	var thumb []string
	var errs []error
	switch {
	case v.Provider == "vimeo":
		o, err := c.oembed(ctx, c.o.Endpoints.VimeoOEmbed+"?url="+url.QueryEscape(v.WatchURL())+"&width="+fmt.Sprint(posterWidth))
		if err != nil {
			return Meta{}, nil, err
		}
		m.Title = o.Title
		m.Portrait = o.Height > o.Width && o.Width > 0
		thumb = []string{o.ThumbnailURL}
	case v.List != "":
		o, err := c.oembed(ctx, c.o.Endpoints.YouTubeOEmbed+"?format=json&url="+url.QueryEscape(v.WatchURL()))
		if err != nil {
			return Meta{}, nil, err
		}
		m.Title = o.Title
		thumb = []string{o.ThumbnailURL}
	default:
		if o, err := c.oembed(ctx, c.o.Endpoints.YouTubeOEmbed+"?format=json&url="+url.QueryEscape("https://www.youtube.com/watch?v="+v.ID)); err == nil {
			m.Title = o.Title
		} else {
			errs = append(errs, err)
		}
		base := c.o.Endpoints.YouTubeImages + v.ID + "/"
		thumb = []string{base + "maxresdefault.jpg", base + "hqdefault.jpg"}
	}
	for _, u := range thumb {
		jpg, w, h, err := c.picture(ctx, u)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		m.Poster, m.Width, m.Height = true, w, h
		return m, jpg, nil
	}
	return m, nil, errors.Join(errs...)
}

type oembed struct {
	Title        string `json:"title"`
	ThumbnailURL string `json:"thumbnail_url"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

func (c *Cache) oembed(ctx context.Context, u string) (oembed, error) {
	body, err := c.get(ctx, u, maxOEmbed)
	if err != nil {
		return oembed{}, err
	}
	var o oembed
	if err := json.Unmarshal(body, &o); err != nil {
		return oembed{}, fmt.Errorf("oembed: %w", err)
	}
	o.Title = strings.Join(strings.Fields(o.Title), " ")
	if len(o.Title) > 200 {
		o.Title = strings.ToValidUTF8(o.Title[:200], "")
	}
	return o, nil
}

// picture downloads an image and re-encodes it as a JPEG.
func (c *Cache) picture(ctx context.Context, u string) ([]byte, int, int, error) {
	if u == "" {
		return nil, 0, 0, errors.New("no thumbnail")
	}
	body, err := c.get(ctx, u, maxDownload)
	if err != nil {
		return nil, 0, 0, err
	}
	switch media.Sniff(body) {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, 0, 0, errors.New("thumbnail is not an image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxSide || cfg.Height > maxSide {
		return nil, 0, 0, errors.New("thumbnail cannot be decoded or is too large")
	}
	// YouTube answers a missing maxresdefault with a 120×90 placeholder.
	if cfg.Width <= 120 && cfg.Height <= 90 {
		return nil, 0, 0, errors.New("placeholder thumbnail")
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, errors.New("thumbnail cannot be decoded")
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > posterWidth {
		h = h * posterWidth / w
		w = posterWidth
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 82}); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), w, h, nil
}

func (c *Cache) allowed(u *url.URL) bool {
	return u != nil && u.User == nil && c.origins[u.Scheme+"://"+strings.ToLower(u.Host)]
}

func (c *Cache) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || !c.allowed(u) {
		return nil, fmt.Errorf("videothumb: %q is not a provider URL", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sap13ns (video poster)")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u.Host, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s: response too large", u.Host)
	}
	return body, nil
}

// load reads the sidecar files once (c.mu held).
func (c *Cache) load() {
	if c.loaded {
		return
	}
	c.loaded = true
	names, _ := filepath.Glob(filepath.Join(c.o.Dir, "*.json"))
	for _, p := range names {
		key := strings.TrimSuffix(filepath.Base(p), ".json")
		if !keyRe.MatchString(key) {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m Meta
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		if m.Poster {
			if fi, err := os.Stat(filepath.Join(c.o.Dir, key+".jpg")); err != nil || fi.Size() != m.Size {
				m.Poster, m.Width, m.Height, m.Size = false, 0, 0, 0
			}
		}
		c.entries[key] = &m
	}
}

// store records a fetch: files on disk for a success, a note in memory for
// a failure. It keeps the cache within its bounds and reports a change.
func (c *Cache) store(key string, m Meta, jpg []byte) {
	m.Fetched = c.o.Now()
	c.mu.Lock()
	c.load()
	old := c.entries[key]
	if m.Title == "" && !m.Poster {
		if old != nil && !old.failed {
			// Keep serving what we had; try again later.
			kept := *old
			kept.Fetched = m.Fetched.Add(c.o.RetryAfter - c.o.TTL)
			c.entries[key] = &kept
		} else {
			c.entries[key] = &Meta{Fetched: m.Fetched, failed: true}
		}
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	ok := c.write(key, &m, jpg)
	c.mu.Lock()
	if !ok {
		c.entries[key] = &Meta{Fetched: m.Fetched, failed: true}
		c.mu.Unlock()
		return
	}
	c.entries[key] = &m
	evicted := c.evict(key)
	changed := old == nil || old.failed || old.Title != m.Title || old.Poster != m.Poster || old.Width != m.Width || old.Height != m.Height || old.Portrait != m.Portrait
	c.mu.Unlock()
	for _, k := range evicted {
		os.Remove(filepath.Join(c.o.Dir, k+".jpg"))
		os.Remove(filepath.Join(c.o.Dir, k+".json"))
	}
	if !changed {
		return
	}
	c.mu.Lock()
	listeners := c.listeners
	c.mu.Unlock()
	for _, fn := range listeners {
		fn(key)
	}
	if c.o.OnChange != nil {
		c.o.OnChange(key)
	}
}

func (c *Cache) write(key string, m *Meta, jpg []byte) bool {
	if err := os.MkdirAll(c.o.Dir, 0o750); err != nil {
		c.o.Log.Warn("video poster cache", "err", err)
		return false
	}
	if m.Poster {
		if err := writeFile(filepath.Join(c.o.Dir, key+".jpg"), jpg); err != nil {
			c.o.Log.Warn("video poster cache", "err", err)
			m.Poster, m.Width, m.Height = false, 0, 0
		} else {
			m.Size = int64(len(jpg))
		}
	}
	if !m.Poster {
		os.Remove(filepath.Join(c.o.Dir, key+".jpg"))
	}
	b, _ := json.Marshal(m)
	if err := writeFile(filepath.Join(c.o.Dir, key+".json"), b); err != nil {
		c.o.Log.Warn("video poster cache", "err", err)
		return false
	}
	return true
}

func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".thumb-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// evict drops the oldest entries until the cache is within its bounds and
// returns their keys (c.mu held). keep is the entry just written.
func (c *Cache) evict(keep string) []string {
	var total int64
	var keys []string
	for k, m := range c.entries {
		if m.failed {
			continue
		}
		total += m.Size
		keys = append(keys, k)
	}
	if len(keys) <= c.o.MaxEntries && total <= c.o.MaxBytes {
		return nil
	}
	sort.Slice(keys, func(i, j int) bool { return c.entries[keys[i]].Fetched.Before(c.entries[keys[j]].Fetched) })
	var out []string
	count := len(keys)
	for _, k := range keys {
		if count <= c.o.MaxEntries && total <= c.o.MaxBytes {
			break
		}
		if k == keep {
			continue
		}
		total -= c.entries[k].Size
		count--
		delete(c.entries, k)
		out = append(out, k)
	}
	return out
}
