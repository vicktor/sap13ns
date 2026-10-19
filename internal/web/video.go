package web

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/content"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/videothumb"
)

// Video embeds: a YouTube or Vimeo link alone on a line (or the [video]
// shortcode) becomes a click-to-load player (content/video.go). The
// server caches each video's poster and title (videothumb), and only a
// page that shows a player lets the providers' players into frame-src.

// videoOptions says how renderBody renders video links in lang for where
// (nil: they stay links).
func (s *Server) videoOptions(lang, where string) *content.VideoOptions {
	mode := s.settings().VideoEmbeds
	if mode == "off" {
		return nil
	}
	o := &content.VideoOptions{Tr: func(key string, args ...any) string { return s.i18n.T(lang, key, args...) }}
	if where == plugin.FilterFeed {
		o.Mode = content.VideoFeed
	}
	if mode == "on" && s.videos != nil {
		o.Meta = s.videoMeta
	}
	return o
}

// videoMeta returns the cached poster and title of v and fetches them in
// the background when they are missing or old. It never waits.
func (s *Server) videoMeta(v content.Video) content.VideoMeta {
	m, due := s.videos.Lookup(v)
	if due {
		s.videos.Prefetch(v, s.goJob)
	}
	out := content.VideoMeta{Title: m.Title, Portrait: m.Portrait}
	if m.Poster {
		out.Poster, out.Width, out.Height = videothumb.PosterURL(v.Key()), m.Width, m.Height
	}
	return out
}

// prefetchVideos fetches the posters of the videos a post shows, when it
// is saved, so that its first visitors already see them.
func (s *Server) prefetchVideos(md string) {
	if s.videos == nil || s.settings().VideoEmbeds != "on" || !strings.Contains(md, "http") {
		return
	}
	content.RenderWith(md, content.Options{Video: &content.VideoOptions{
		Mode:  content.VideoWordPress, // the cheapest output; only Found matters
		Found: func(v content.Video) { s.videos.Prefetch(v, s.goJob) },
	}})
}

// awaitVideos fetches the missing posters of the videos in md, waiting at
// most d, so that the editor's preview shows a video just pasted with its
// picture.
func (s *Server) awaitVideos(ctx context.Context, md string, d time.Duration) {
	if s.videos == nil || s.settings().VideoEmbeds != "on" || !strings.Contains(md, "http") {
		return
	}
	var due []content.Video
	content.RenderWith(md, content.Options{Video: &content.VideoOptions{
		Mode: content.VideoWordPress,
		Found: func(v content.Video) {
			if _, stale := s.videos.Lookup(v); stale && len(due) < 8 {
				due = append(due, v)
			}
		},
	}})
	if len(due) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	var wg sync.WaitGroup
	for _, v := range due {
		wg.Go(func() { s.videos.Fetch(ctx, v) })
	}
	wg.Wait()
}

// basePolicy is the Content-Security-Policy of every response
// (securityHeaders), before plugin sources.
func (s *Server) basePolicy() string {
	if s.cfg.BaseURL != nil && s.cfg.BaseURL.Scheme == "https" {
		return csp + "; upgrade-insecure-requests"
	}
	return csp
}

// allowVideoFrames lets the YouTube and Vimeo players into frame-src of
// this response only. Public pages are rendered into the page cache's
// buffer, which has no policy yet: theirs is the public one.
func (s *Server) allowVideoFrames(w http.ResponseWriter) {
	h := w.Header()
	base := h.Get("Content-Security-Policy")
	if base == "" {
		base = s.pluginCSP(s.basePolicy())
	}
	h.Set("Content-Security-Policy", mergeCSP(base, map[string][]string{"frame-src": content.PlayerOrigins}))
}

var posterName = regexp.MustCompile(`^[a-z]{2,4}-[A-Za-z0-9_-]{1,64}\.jpg$`)

// handleVideoThumb serves a cached poster (/video-thumbs/yt-ID.jpg).
func (s *Server) handleVideoThumb(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if s.videos == nil || !posterName.MatchString(name) {
		s.notFound(w, r)
		return
	}
	path, ok := s.videos.File(name)
	if !ok {
		s.notFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		s.notFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// apiVideo checks a video URL for the editor and returns what the player
// shows: GET /admin/api/video?url=…. It waits briefly for the poster.
func (s *Server) apiVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := content.ParseVideoURL(r.URL.Query().Get("url"))
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": s.tu(r, "Enter a YouTube or Vimeo address.")})
		return
	}
	mode := s.settings().VideoEmbeds
	out := map[string]any{
		"provider": v.Provider, "name": v.ProviderName(), "key": v.Key(), "watch": v.WatchURL(), "player": v.PlayerURL(),
		"portrait": v.Portrait, "start": v.Start, "mode": mode,
	}
	if mode == "on" && s.videos != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		m := s.videos.Fetch(ctx, v)
		cancel()
		out["title"] = m.Title
		out["portrait"] = v.Portrait || m.Portrait
		if m.Poster {
			out["poster"], out["width"], out["height"] = videothumb.PosterURL(v.Key()), m.Width, m.Height
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// imageLookup describes the media-library images that the Markdown sizes
// ({width=…}), for their height and srcset. nil when there are none.
func (s *Server) imageLookup(ctx context.Context, md string) func(string) (content.ImageInfo, bool) {
	if !strings.Contains(md, "){") {
		return nil
	}
	var paths []string
	seen := map[string]bool{}
	for _, m := range sizedImage.FindAllStringSubmatch(md, 100) {
		p := strings.TrimPrefix(m[1], "/uploads/")
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	found, err := s.store.MediaByPaths(ctx, paths)
	if err != nil || len(found) == 0 {
		return nil
	}
	return func(src string) (content.ImageInfo, bool) {
		m := found[strings.TrimPrefix(src, "/uploads/")]
		if m == nil || !m.IsImage() || m.Width <= 0 || m.Height <= 0 {
			return content.ImageInfo{}, false
		}
		return mediaImageInfo(m), true
	}
}

var sizedImage = regexp.MustCompile(`!\[[^\]\n]*\]\((/uploads/[A-Za-z0-9/_.-]{1,200})(?:\s+"[^"\n]*")?\)\{`)

// mediaImageInfo lists a media item's files, narrowest first.
func mediaImageInfo(m *store.Media) content.ImageInfo {
	info := content.ImageInfo{Width: m.Width, Height: m.Height}
	for _, name := range []string{"thumb", "medium", "large"} {
		if v, ok := m.Variants[name]; ok && v.Width > 0 && v.Width < m.Width {
			info.Sources = append(info.Sources, content.ImageSource{URL: "/uploads/" + v.Path, Width: v.Width})
		}
	}
	info.Sources = append(info.Sources, content.ImageSource{URL: m.URL(), Width: m.Width})
	return info
}
