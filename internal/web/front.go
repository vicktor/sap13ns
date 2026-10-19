package web

// The front page (/ and /<lang>/). Settings → Reading picks what it shows:
//
//   - "posts": the latest posts, with the theme's home template;
//   - "page": a static page (public.go), with the posts at /blog;
//   - "theme": the theme's own front page, templates/front.html (a landing
//     page, a one-page site), or the page builder's "front" layout, which
//     any theme can have; the posts are at /blog. With neither, the latest
//     posts are shown, as in "posts".
//
// The front page is cached like any public page: publishing a layout,
// activating a theme and saving the settings all call contentChanged.

import (
	"net/http"
	"strings"

	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// Front page modes (store.Settings.FrontPage).
const (
	FrontPosts = "posts"
	FrontPage  = "page"
	FrontTheme = "theme"
)

// showsFront reports whether / renders th's front page for this request.
// A page builder preview of the front region (?front=1 with the draft) shows
// it whatever the setting, and so does a preview of another theme that has
// one while the site shows its posts. ?page=N keeps the posts list, as it
// does with a static front page.
func (s *Server) showsFront(r *http.Request, th *theme.Theme) bool {
	q := r.URL.Query()
	if q.Get("page") != "" {
		return false
	}
	mode := s.settings().FrontPage
	switch {
	case q.Get("front") == "1" && s.wantsDraft(r):
		mode = FrontTheme
	case mode == FrontPosts && th.Has("front") && s.previewing(r, th) != "" && th.Slug != s.settings().ActiveTheme:
		mode = FrontTheme
	}
	return mode == FrontTheme && (th.Has("front") || s.hasFrontLayout(r, th))
}

// hasFrontLayout reports whether th's layouts build the front page: the
// published ones, or the draft in the page builder's preview.
func (s *Server) hasFrontLayout(r *http.Request, th *theme.Theme) bool {
	ctx := r.Context()
	if s.wantsDraft(r) {
		l, _, err := s.draftLayout(ctx, th, s.mediaSet(ctx))
		return err == nil && l.Region("front") != nil
	}
	return s.publishedDesign(ctx, th).layout.Region("front") != nil
}

// frontShowsPosts reports whether / lists the posts for visitors with the
// active theme, so that the sitemap and the builder know where they are.
func (s *Server) frontShowsPosts(r *http.Request) bool {
	st := s.settings()
	switch st.FrontPage {
	case FrontPage:
		return st.FrontPageID == 0 || s.frontPage(r.Context(), st.Language) == nil
	case FrontTheme:
		th := s.themes.Active(st.ActiveTheme)
		return th == nil || !(th.Has("front") || s.publishedDesign(r.Context(), th).layout.Region("front") != nil)
	}
	return true
}

// renderFront renders the front page: the theme's front template, or the
// front layout of the page builder (applyLayout). The latest posts come
// along, without pagination, for a section that shows them.
func (s *Server) renderFront(w http.ResponseWriter, r *http.Request) {
	th, pd, err := s.basePage(r, "front")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	lang := pd.Site.Lang
	pd.IsFront = true
	pd.JSONLD = s.websiteLD(pd, lang)
	per := s.settings().PostsPerPage
	if n := pd.Site.Custom.PerPage; n > 0 {
		per = n
	}
	posts, total, err := s.store.ListPosts(r.Context(), store.PostQuery{Type: store.TypePost, Lang: lang, PublicOnly: true,
		Limit: per, WithTerms: true, WithMedia: true})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pd.Posts, pd.Total = s.postViews(posts, currentUser(r)), total
	s.renderPage(w, r, th, "front", pd, http.StatusOK)
}

// websiteLD is the WebSite JSON-LD of the front page.
func (s *Server) websiteLD(pd *PageData, lang string) map[string]any {
	return map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": pd.Site.Title,
		"url": pd.Site.URL, "description": pd.Site.Tagline, "inLanguage": lang,
		"potentialAction": map[string]any{"@type": "SearchAction", "target": s.cfg.AbsURL(pd.Site.Search + "?q={q}"), "query-input": "required name=q"}}
}

// Link returns a link of a menu or a header or footer block as this page
// needs it: a section anchor (#pricing) is on the front page, so elsewhere
// it becomes the front page's (/#pricing, or /en/#pricing). Themes can call
// it too: {{.Site.Link "#features"}}.
func (sv *SiteView) Link(u string) string {
	if strings.HasPrefix(u, "#") && len(u) > 1 && !sv.atHome {
		return sv.Home + u
	}
	return u
}

// OnePage reports whether the page has sections to scroll to, or menu
// links to them: the front page, a layout with anchored sections, a menu
// with #links. shared/head then marks the site bundle's script with
// data-sap-onepage, and its onepage.js marks the link of the section in view.
func (p *PageData) OnePage() bool {
	if p == nil || p.Site == nil {
		return false
	}
	if p.Kind == "front" {
		return true
	}
	if p.Blocks != nil && p.Blocks.Main != nil {
		for _, sec := range p.Blocks.Main.Sections {
			if sec.Anchor != "" {
				return true
			}
		}
	}
	if p.Site.atHome {
		for _, links := range p.Site.Menus {
			for _, l := range links {
				if strings.HasPrefix(l.URL, "#") {
					return true
				}
			}
		}
	}
	return false
}
