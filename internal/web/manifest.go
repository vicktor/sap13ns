package web

import (
	"encoding/json"
	"net/http"
	"unicode/utf8"
)

// handleManifest serves the web app manifest, so that "Add to home screen"
// uses the site's name and the brand icons.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	short := st.SiteTitle
	if utf8.RuneCountInString(short) > 12 {
		short = initials(short)
	}
	type icon struct {
		Src     string `json:"src"`
		Sizes   string `json:"sizes"`
		Type    string `json:"type"`
		Purpose string `json:"purpose,omitempty"`
	}
	m := struct {
		Name        string `json:"name"`
		ShortName   string `json:"short_name"`
		Description string `json:"description,omitempty"`
		Lang        string `json:"lang"`
		StartURL    string `json:"start_url"`
		Scope       string `json:"scope"`
		Display     string `json:"display"`
		Icons       []icon `json:"icons"`
	}{st.SiteTitle, short, st.Tagline, st.Language, "/", "/", "minimal-ui", []icon{
		{s.assetURL("brand/icon-192.png"), "192x192", "image/png", ""},
		{s.assetURL("brand/icon-512.png"), "512x512", "image/png", ""},
		{s.assetURL("brand/icon-maskable-512.png"), "512x512", "image/png", "maskable"},
	}}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	json.NewEncoder(w).Encode(m)
}
