package web

import (
	"bytes"
	"cmp"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/content"
	"sap13ns/internal/i18n"
	"sap13ns/internal/ogcard"
	"sap13ns/internal/store"
)

// Automatic Open Graph images. Posts and pages without a featured image get
// a generated 1200×630 card. Its URL carries the card's content hash, so the
// file can be cached forever and changes whenever the title, category or
// colours do; rendered cards are kept on disk under DataDir/cache/og.

const ogDefaultAccent = "#3F068B" // sap13ns violet

var ogLayouts = []string{"classic", "bold", "minimal"}

// ogRender serialises rendering: a card costs ~20 ms of CPU, and a burst of
// crawler requests for new posts must not pile up.
var ogRender sync.Mutex

func (s *Server) ogEnabled() bool { return s.settings().OGCards != "off" }

// ogCard builds the card for a post. It is cheap (no rendering).
func (s *Server) ogCard(p *store.Post) ogcard.Card {
	st := s.settings()
	txt := st.Text(p.Lang)
	style, err := ogcard.StyleFromHex("", "", "", cmp.Or(st.OGAccent, ogDefaultAccent))
	if err != nil {
		style, _ = ogcard.StyleFromHex("", "", "", ogDefaultAccent)
	}
	c := ogcard.Card{Title: p.Title, SiteName: txt.SiteTitle, Style: style, Layout: st.OGCards}
	if p.Type == store.TypePost {
		if cats := p.Categories(); len(cats) > 0 {
			c.Eyebrow = cats[0].In(p.Lang).Name
		}
		var parts []string
		if p.PublishedAt != nil {
			parts = append(parts, i18n.FormatDate(p.Lang, p.PublishedAt.In(s.loc())))
		}
		if m := content.ReadingMinutes(p.Content); m > 0 {
			parts = append(parts, s.i18n.T(p.Lang, "%d min read", m))
		}
		c.Footer = strings.Join(parts, " · ")
	}
	return c
}

// ogImageURL is the absolute URL of a post's generated card.
func (s *Server) ogImageURL(p *store.Post) string {
	return s.cfg.AbsURL("/og/" + strconv.FormatInt(p.ID, 10) + "-" + ogcard.Key(s.ogCard(p))[:16] + ".png")
}

func (s *Server) handleOGImage(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutSuffix(r.PathValue("file"), ".png")
	idStr, key, ok2 := strings.Cut(name, "-")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || !ok2 || err != nil || len(key) != 16 || !s.ogEnabled() {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.PostByID(r.Context(), id)
	if err != nil || !p.IsPublic(time.Now()) {
		http.NotFound(w, r)
		return
	}
	card := s.ogCard(p)
	full := ogcard.Key(card)
	if full[:16] != key {
		// Stale link (the title changed): send crawlers to the current card.
		http.Redirect(w, r, s.ogImageURL(p), http.StatusMovedPermanently)
		return
	}
	data, err := s.ogFile(full, card)
	if err != nil {
		s.log.Error("og card", "post", id, "err", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+full+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// ogFile returns the cached PNG for key, rendering it on a miss.
func (s *Server) ogFile(key string, card ogcard.Card) ([]byte, error) {
	dir := filepath.Join(s.cfg.DataDir, "cache", "og")
	path := filepath.Join(dir, key+".png")
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	ogRender.Lock()
	defer ogRender.Unlock()
	if b, err := os.ReadFile(path); err == nil {
		return b, nil
	}
	b, err := ogcard.Render(card)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return b, nil // serve it anyway; caching is best effort
	}
	tmp, err := os.CreateTemp(dir, ".og-*")
	if err != nil {
		return b, nil
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return b, nil
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
	}
	return b, nil
}
