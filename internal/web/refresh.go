package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"

	"sap13ns/internal/theme"
)

// Open pages can follow the site's changes (Settings → Site, the setting
// Refresh). Every public page carries the site's version, a hash of what
// its look and behaviour are made of: the core's static files and shared
// templates, the active theme's files and the published design and
// layouts. Content is not part of it: a new post is no new version.
// refresh.js asks /api/site-version when the visitor comes back to the
// page, and tells them, or reloads, when the answer is another one.
//
// The version is derived from content only, never from a counter or the
// time, so a restart or a site loaded again on a network gives the same
// one, and no open page takes it for a change.

// RefreshView is what shared/head gives refresh.js.
type RefreshView struct {
	Version string
	Mode    string // "ask" or "reload"
}

// coreStamp hashes the core's static files and shared templates.
func (s *Server) coreStamp() string {
	s.coreStampOnce.Do(func() {
		h := sha256.New()
		for _, m := range []map[string]string{s.static, hashFiles(s.ui, "shared")} {
			names := make([]string, 0, len(m))
			for name := range m {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				h.Write([]byte(name + "\x00" + m[name] + "\x00"))
			}
		}
		s.coreStampV = hex.EncodeToString(h.Sum(nil)[:8])
	})
	return s.coreStampV
}

// siteVersion is the version of the site as th shows it, "" when the
// published design could not be loaded whole (the next request tries
// again).
func (s *Server) siteVersion(ctx context.Context, th *theme.Theme) string {
	if th == nil {
		return ""
	}
	e := s.publishedDesign(ctx, th)
	if e.partial {
		return ""
	}
	sum := sha256.Sum256([]byte(s.coreStamp() + "\x00" + th.Slug + "\x00" + th.Stamp() + "\x00" + e.stamp))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) refreshView(th *theme.Theme) *RefreshView {
	mode := s.settings().Refresh
	if mode != "ask" && mode != "reload" {
		return nil
	}
	v := s.siteVersion(context.Background(), th)
	if v == "" {
		return nil
	}
	return &RefreshView{Version: v, Mode: mode}
}

// handleSiteVersion answers GET /api/site-version: {"version": "…"}, the
// same for every visitor. 404 while the setting is off.
func (s *Server) handleSiteVersion(w http.ResponseWriter, r *http.Request) {
	rv := s.refreshView(s.themes.Active(s.settings().ActiveTheme))
	if rv == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]string{"version": rv.Version})
}
