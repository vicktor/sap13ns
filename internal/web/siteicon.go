package web

// The site icon (Settings → General): an image of the media library that
// replaces the sap13ns icon in browser tabs, bookmarks, the home screen of
// phones and the web app manifest. Its sizes are drawn from the file on
// first use and kept in memory; an icon that cannot be drawn (a file gone,
// a type the server cannot decode) falls back to the sap13ns icon.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"sap13ns/internal/media"
	"sap13ns/internal/store"
)

// siteIconCache keeps the sizes of the current site icon.
type siteIconCache struct {
	mu    sync.Mutex
	id    int64
	files map[int][]byte // nil: the icon could not be drawn
}

// iconFallback is the sap13ns file of each size (ui/static/brand).
var iconFallback = map[int]string{32: "brand/favicon-32.png", 180: "brand/apple-touch-icon.png", 192: "brand/icon-192.png", 512: "brand/icon-512.png"}

// siteIconURL is the address of one size of the site icon, "" when the
// site has none. The id in the query string makes each icon its own URL.
func siteIconURL(id int64, size int) string {
	if id <= 0 {
		return ""
	}
	return "/site-icon/" + strconv.Itoa(size) + ".png?v=" + strconv.FormatInt(id, 10)
}

// iconMedia reports whether m can be a site icon: an image the server can
// draw (media.IconTypes).
func iconMedia(m *store.Media) bool {
	return m != nil && slices.Contains(media.IconTypes(), m.MIME)
}

// handleSiteIcon serves GET /site-icon/{file}, file being <size>.png.
func (s *Server) handleSiteIcon(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	size, err := strconv.Atoi(filepath.Base(file[:max(0, len(file)-len(".png"))]))
	if err != nil || file != strconv.Itoa(size)+".png" || iconFallback[size] == "" {
		http.NotFound(w, r)
		return
	}
	id := s.settings().SiteIcon
	var b []byte
	if id > 0 {
		b = s.siteIcon(r.Context(), id)[size]
	}
	if b == nil {
		w.Header().Set("Cache-Control", "no-cache")
		http.Redirect(w, r, s.assetURL(iconFallback[size]), http.StatusFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	if r.URL.Query().Get("v") == strconv.FormatInt(id, 10) {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(b))
}

// siteIcon returns the sizes of icon id, drawing them the first time.
// A failure is remembered (nil) until the setting changes or the server
// restarts, so a broken file is not decoded on every request.
func (s *Server) siteIcon(ctx context.Context, id int64) map[int][]byte {
	c := &s.icon
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.id == id {
		return c.files
	}
	files, err := s.drawSiteIcon(ctx, id)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil // the request went away: try again next time
		}
		s.log.Warn("site icon", "media", id, "err", err)
	}
	c.id, c.files = id, files
	return files
}

func (s *Server) drawSiteIcon(ctx context.Context, id int64) (map[int][]byte, error) {
	m, err := s.store.MediaByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !iconMedia(m) {
		return nil, media.ErrUnsupported
	}
	// The largest variant that still makes a sharp 512 px icon is the
	// cheapest file to decode; originals may be much larger.
	rel := m.Path
	if v, ok := m.Variants["medium"]; ok && v.Path != "" {
		rel = v.Path
	}
	f, err := os.Open(filepath.Join(s.cfg.UploadsDir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return media.MakeIcon(ctx, f, 20<<20)
}
