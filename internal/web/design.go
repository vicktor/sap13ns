package web

// The design screen (Appearance → Design) is the customiser (style, texts
// and images) and the page builder (structure) as two tabs of one page,
// with one draft to publish or discard: the design's and the layout's
// together.

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// publishDesign is the design screen's Publish: the whole draft and, with
// their boxes checked, the pages the design assistant created
// (publish_pages) and the design's front page as the site's (use_front,
// frontOffer). It returns the flash message and its kind.
func (s *Server) publishDesign(r *http.Request, th *theme.Theme) (kind, msg string, err error) {
	front := r.FormValue("use_front") == "1" && s.frontOffer(r, th)
	done, err := s.publishDesignAll(r, th)
	if err != nil {
		return "", "", err
	}
	pages := 0
	if r.FormValue("publish_pages") == "1" {
		if pages, err = s.publishAIPages(r, th); err != nil {
			return "", "", err
		}
	}
	if front {
		if err := s.useThemeFront(r, th); err != nil {
			return "", "", err
		}
	}
	switch {
	case pages > 0 && front:
		return "success", s.tu(r, "Published, with the pages the assistant created. The site's front page is now the design's, and the latest posts are at /blog."), nil
	case pages > 0:
		return "success", s.tu(r, "Published, with the pages the assistant created."), nil
	case front:
		return "success", s.tu(r, "Published. The site's front page is now the design's, and the latest posts are at /blog."), nil
	case !done:
		return "info", s.tu(r, "There are no unpublished changes."), nil
	}
	return "success", s.tu(r, "Published. Visitors now see the new design."), nil
}

// useThemeFront makes the theme's front page the site's (Settings → Reading).
func (s *Server) useThemeFront(r *http.Request, th *theme.Theme) error {
	st := s.settings()
	st.FrontPage = FrontTheme
	if err := s.saveSettings(r, st); err != nil {
		return err
	}
	s.audit(r, uid(r), "settings.front_page", FrontTheme, map[string]string{"theme": th.Slug})
	return nil
}

// aiPagesPending counts the pages the design assistant created for th that
// are still drafts, for a user who may publish them.
func (s *Server) aiPagesPending(r *http.Request, th *theme.Theme) int {
	if u := currentUser(r); u == nil || !auth.Can(u.Role, auth.CapManagePages) {
		return 0
	}
	ids, err := s.store.DesignAIPages(r.Context(), th.Slug)
	if err != nil {
		return 0
	}
	n := 0
	for _, id := range ids {
		if p, err := s.store.PostByID(r.Context(), id); err == nil && p.Status == store.StatusDraft {
			n++
		}
	}
	return n
}

// publishAIPages publishes the pages the design assistant created for th.
func (s *Server) publishAIPages(r *http.Request, th *theme.Theme) (int, error) {
	if u := currentUser(r); u == nil || !auth.Can(u.Role, auth.CapManagePages) {
		return 0, nil
	}
	ctx := r.Context()
	ids, err := s.store.DesignAIPages(ctx, th.Slug)
	if err != nil {
		return 0, err
	}
	done, err := s.store.PublishDraftPages(ctx, ids)
	if err != nil {
		return 0, err
	}
	for _, id := range done {
		s.audit(r, uid(r), "page.published", fmt.Sprintf("#%d", id), map[string]string{"by": "design"})
		s.afterPostSave(r, id, store.StatusDraft, store.StatusPublished)
	}
	if err := s.store.SetDesignAIPages(ctx, th.Slug, nil); err != nil {
		return 0, err
	}
	return len(done), nil
}

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
	// The assistant's pages stay drafts in Pages; they are no longer
	// offered for publishing with the design.
	if err := s.store.SetDesignAIPages(ctx, th.Slug, nil); err != nil {
		return err
	}
	s.audit(r, uid(r), "theme.design_discarded", th.Slug, nil)
	return nil
}
