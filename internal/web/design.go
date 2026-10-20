package web

// The design screen (Appearance → Design) is the customiser (style, texts
// and images) and the page builder (structure) as two tabs of one page,
// with one draft to publish or discard: the design's and the layout's
// together.

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// publishDesignAll publishes th's draft design (with its theme options and
// custom CSS) and its draft layout. It reports false when neither had
// unpublished changes.
func (s *Server) publishDesignAll(r *http.Request, th *theme.Theme) (bool, error) {
	ctx := r.Context()
	ms := s.mediaSet(ctx)
	d, row, err := s.draftDesign(ctx, th, ms)
	if err != nil {
		return false, err
	}
	l, lrow, err := s.draftLayout(ctx, th, ms)
	if err != nil {
		return false, err
	}
	if row.Draft == nil && lrow.Draft == nil {
		return false, nil
	}
	if row.Draft != nil {
		// Theme options live in the theme's stored options, which the
		// options page edits too: publishing writes the draft's overrides
		// there.
		opts := d.Options
		d.Options = nil
		err = s.store.PublishCustom(ctx, th.Slug, store.CustomDesign, d.JSON(), uid(r), func(tx pgx.Tx) error {
			if len(opts) == 0 {
				return nil
			}
			stored, err := s.store.ThemeOptions(ctx, th.Slug)
			if err != nil {
				return err
			}
			for k, v := range opts {
				stored[k] = v
			}
			return store.SaveThemeOptionsTx(ctx, tx, th.Slug, th.OptionValues(stored))
		})
		if err != nil {
			return false, err
		}
		s.audit(r, uid(r), "theme.design_published", th.Slug, nil)
	}
	if lrow.Draft != nil {
		if err := s.store.PublishCustom(ctx, th.Slug, store.CustomLayout, l.JSON(), uid(r), nil); err != nil {
			s.custom.invalidate()
			s.contentChanged()
			return false, err
		}
		s.audit(r, uid(r), "theme.layout_published", th.Slug, nil)
	}
	s.custom.invalidate()
	s.contentChanged()
	return true, nil
}

// discardDesignAll drops th's draft design and draft layout.
func (s *Server) discardDesignAll(r *http.Request, th *theme.Theme) error {
	ctx := r.Context()
	if err := s.store.DiscardCustomDraft(ctx, th.Slug, store.CustomDesign); err != nil {
		return err
	}
	if err := s.store.DiscardCustomDraft(ctx, th.Slug, store.CustomLayout); err != nil {
		return err
	}
	s.audit(r, uid(r), "theme.design_discarded", th.Slug, nil)
	return nil
}
