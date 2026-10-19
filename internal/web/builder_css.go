package web

// Custom CSS in the page builder. The CSS is the customiser's (the "css" of
// the theme's design, internal/customize): one stylesheet per theme, served
// with the generated tokens at /themes/{theme}/custom/…, never inlined, so
// style-src 'self' holds. The builder edits it in the design's draft, which
// the preview shows; its Publish and Discard take the CSS along with the
// layout and leave the design's other unpublished changes alone. Its
// published versions are the design's (Customise → Published versions),
// and it travels in the shared export file.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/customize"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// themeCSS is the custom CSS of th's design: the draft's (the published
// one's without a draft) and the published one's.
type themeCSS struct {
	Draft     string
	Published string
	draft     *customize.Design // nil without a draft
	published *customize.Design
}

// Pending reports whether the draft changes the custom CSS.
func (c *themeCSS) Pending() bool { return c.draft != nil && c.Draft != c.Published }

func (s *Server) loadCustomCSS(ctx context.Context, th *theme.Theme) (*themeCSS, error) {
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomDesign)
	if err != nil {
		return nil, err
	}
	ms := s.mediaSet(ctx)
	dc := s.designContext(th, ms)
	pub, err := customize.Load(row.Published, dc)
	if err != nil {
		return nil, err
	}
	c := &themeCSS{Published: pub.CSS, Draft: pub.CSS, published: pub}
	if row.Draft != nil {
		if c.draft, err = customize.Load(row.Draft, dc); err != nil {
			return nil, err
		}
		c.Draft = c.draft.CSS
	}
	return c, nil
}

type cssResult struct {
	OK      bool                   `json:"ok"`
	Errors  []customize.FieldError `json:"errors,omitempty"`
	Message string                 `json:"message,omitempty"`
	SavedAt string                 `json:"saved_at,omitempty"`
	CSS     string                 `json:"css"`
	Pending bool                   `json:"pending"`
}

// adminBuilderCSS saves the custom CSS into the design's draft: JSON
// {"css": "…"}, checked by customize.SanitizeCSS.
func (s *Server) adminBuilderCSS(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, cssResult{Message: s.tu(r, "Not found.")})
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, cssResult{Message: s.tu(r, "The request could not be read.")})
		return
	}
	var body struct {
		CSS *string `json:"css"`
	}
	// A JSON string can take up to six bytes per byte of CSS (\u0000).
	dec := json.NewDecoder(io.LimitReader(r.Body, 6*customize.MaxCSS+1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.CSS == nil {
		writeJSON(w, http.StatusBadRequest, cssResult{Message: s.tu(r, "The request could not be read.")})
		return
	}
	css, err := customize.SanitizeCSS(*body.CSS)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, cssResult{Errors: []customize.FieldError{{Field: "css", Message: err.Error()}},
			Message: s.tu(r, "Some values were not accepted.")})
		return
	}
	ctx := r.Context()
	d, _, err := s.draftDesign(ctx, th, s.mediaSet(ctx))
	if err != nil {
		s.log.Error("load theme draft", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, cssResult{Message: s.tu(r, "Something went wrong. The error has been logged.")})
		return
	}
	if css != d.CSS {
		d.CSS = css
		if err := s.store.SaveCustomDraft(ctx, th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
			s.log.Error("save theme draft", "theme", th.Slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, cssResult{Message: s.tu(r, "Something went wrong. The error has been logged.")})
			return
		}
	}
	c, err := s.loadCustomCSS(ctx, th)
	if err != nil {
		s.log.Error("load theme custom CSS", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, cssResult{Message: s.tu(r, "Something went wrong. The error has been logged.")})
		return
	}
	writeJSON(w, http.StatusOK, cssResult{OK: true, CSS: css, Pending: c.Pending(), SavedAt: time.Now().In(s.loc()).Format("15:04:05")})
}

// publishCustomCSS publishes the draft's custom CSS on its own: the
// published design with the draft's CSS becomes the published version (a
// revision of the design), and the draft keeps its other changes.
func (s *Server) publishCustomCSS(ctx context.Context, r *http.Request, th *theme.Theme, c *themeCSS) error {
	pub := *c.published
	pub.CSS = c.Draft
	rest := *c.draft
	keep := string(rest.JSON()) != string(pub.JSON())
	return s.store.PublishCustom(ctx, th.Slug, store.CustomDesign, pub.JSON(), uid(r), func(tx pgx.Tx) error {
		if !keep {
			return nil
		}
		return store.SaveCustomDraftTx(ctx, tx, th.Slug, store.CustomDesign, rest.JSON(), uid(r))
	})
}

// discardCustomCSS puts the published CSS back into the draft, which is
// dropped when nothing else is left in it.
func (s *Server) discardCustomCSS(ctx context.Context, r *http.Request, th *theme.Theme, c *themeCSS) error {
	rest := *c.draft
	rest.CSS = c.Published
	if string(rest.JSON()) == string(c.published.JSON()) {
		return s.store.DiscardCustomDraft(ctx, th.Slug, store.CustomDesign)
	}
	return s.store.SaveCustomDraft(ctx, th.Slug, store.CustomDesign, rest.JSON(), uid(r))
}
