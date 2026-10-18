package web

// Block builder (Appearance → Page builder): per-theme layouts of sections,
// columns and blocks for the global header and footer and each page type.
// A published layout replaces the theme's template for its region; every
// other region keeps the theme's. Drafts are only rendered in the
// customiser's preview (?customize=draft, see customize.go), so they share
// its rules: never cached, never shown to visitors.

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/builder"
	"sap13ns/internal/content"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// BlocksView is what theme templates see of the layouts (PageData.Blocks).
type BlocksView struct {
	Header *RegionView
	Footer *RegionView
	Main   *RegionView
}

// RegionView is a region ready to render.
type RegionView struct {
	Name     string
	Sections []*SectionView
	TopBar   bool // header: show the Top bar widget area above it
	HasMeta  bool // footer: the region renders shared/footer-meta itself
	Page     *PageData

	// NeedsH1: no block of this main region renders the page's h1, so
	// shared/blocks-main adds a visually hidden one.
	NeedsH1 bool
}

type SectionView struct {
	ID      string
	Class   string // enumerated appearance classes
	Layout  string // column preset, e.g. "2-1"
	Columns [][]*BlockView
}

// BlockView is one block with its settings resolved for the page language
// (defaults filled in) and its media and Markdown prepared.
type BlockView struct {
	ID    string
	Type  string
	S     map[string]string
	HTML  template.HTML // rich text
	Image *ImageView
	Links []SocialLink
	Page  *PageData
}

// On reports whether a bool setting is on.
func (b *BlockView) On(key string) bool { return b.S[key] == "true" }

type SocialLink struct {
	Key, Label, URL, Icon string
}

// BlocksHeader and BlocksFooter are nil-safe accessors for theme layouts:
// {{if .BlocksHeader}}{{template "shared/blocks-header" .}}{{else}}…{{end}}.
func (p *PageData) BlocksHeader() *RegionView {
	if p == nil || p.Blocks == nil {
		return nil
	}
	return p.Blocks.Header
}

func (p *PageData) BlocksFooter() *RegionView {
	if p == nil || p.Blocks == nil {
		return nil
	}
	return p.Blocks.Footer
}

func (s *Server) builderRoutes(mux *http.ServeMux, admin func(string, auth.Cap, http.HandlerFunc)) {
	base := "/admin/appearance/themes/{slug}/builder"
	admin("GET "+base, auth.CapAppearance, s.adminBuilder)
	admin("POST "+base+"/draft", auth.CapAppearance, s.adminBuilderDraft)
	admin("POST "+base+"/publish", auth.CapAppearance, s.adminBuilderPublish)
	admin("POST "+base+"/discard", auth.CapAppearance, s.adminBuilderDiscard)
	admin("POST "+base+"/revisions/{id}/restore", auth.CapAppearance, s.adminBuilderRestore)
	admin("GET /admin/appearance/builder", auth.CapAppearance, func(w http.ResponseWriter, r *http.Request) {
		th := s.themes.Active(s.settings().ActiveTheme)
		if th == nil {
			s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
			return
		}
		http.Redirect(w, r, "/admin/appearance/themes/"+th.Slug+"/builder", http.StatusSeeOther)
	})
}

// ------------------------------------------------------------ resolving --

func (s *Server) layoutContext(th *theme.Theme, ms *mediaSet) builder.Context {
	c := builder.Context{Media: func(id int64) (bool, error) { return ms.exists(id, "image") }}
	for _, m := range th.Menus {
		c.Menus = append(c.Menus, m.ID)
	}
	for _, a := range th.WidgetAreas {
		c.Areas = append(c.Areas, a.ID)
	}
	for _, l := range s.extraLangs() {
		c.Langs = append(c.Langs, l.Code)
	}
	return c
}

// loadLayout is builder.Load with the images of raw looked up in one query.
func (s *Server) loadLayout(raw []byte, th *theme.Theme, ms *mediaSet) (*builder.Layouts, error) {
	ms.prefetch(builder.ImageIDs(raw))
	return builder.Load(raw, s.layoutContext(th, ms))
}

// draftLayout loads the draft layouts of th (the published ones when there
// is no draft), looking their images up in ms. An error, a failed image
// lookup included, means the layouts must not be saved or published.
func (s *Server) draftLayout(ctx context.Context, th *theme.Theme, ms *mediaSet) (*builder.Layouts, *store.ThemeCustom, error) {
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomLayout)
	if err != nil {
		return nil, nil, err
	}
	raw := row.Published
	if row.Draft != nil {
		raw = row.Draft
	}
	l, err := s.loadLayout(raw, th, ms)
	if err != nil {
		return nil, nil, err
	}
	return l, row, nil
}

// loadPublishedLayout loads th's published layouts for the cache entry of
// its published design (customize.go), which they are invalidated with. A
// failed image lookup is left in ms.err.
func (s *Server) loadPublishedLayout(ctx context.Context, th *theme.Theme, ms *mediaSet) (*builder.Layouts, error) {
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomLayout)
	if err != nil {
		return nil, err
	}
	l, _ := s.loadLayout(row.Published, th, ms)
	return l, nil
}

// applyLayout fills pd.Blocks and returns the page template to render:
// "blocks" when a layout replaces the theme's template for this page.
func (s *Server) applyLayout(r *http.Request, th *theme.Theme, page string, pd *PageData) string {
	ctx := r.Context()
	var l *builder.Layouts
	var media func(int64) *store.Media
	if s.wantsDraft(r) {
		var err error
		ms := s.mediaSet(ctx)
		if l, _, err = s.draftLayout(ctx, th, ms); err != nil {
			s.log.Error("load theme layout draft", "theme", th.Slug, "err", err)
			return page
		}
		media = ms.lookup
	} else {
		e := s.publishedDesign(ctx, th)
		l, media = e.layout, e.mediaByID
		if e.partial && pd.Site != nil && pd.Site.Custom != nil {
			pd.Site.Custom.Partial = true // keep the page out of the page cache
		}
	}
	if l.IsZero() {
		return page
	}
	bv := &BlocksView{}
	bv.Header = s.regionView(l.Header, "header", pd, media)
	bv.Footer = s.regionView(l.Footer, "footer", pd, media)
	if page != "ask" {
		if main := s.regionView(l.Region(page), page, pd, media); main != nil {
			bv.Main = main
			page = "blocks"
		}
	}
	pd.Blocks = bv
	return page
}

// regionView builds a region for rendering; media resolves image ids
// without a query (the images were looked up with the layouts).
func (s *Server) regionView(r *builder.Region, name string, pd *PageData, media func(int64) *store.Media) *RegionView {
	if r == nil {
		return nil
	}
	lang := pd.Site.Lang
	rv := &RegionView{Name: name, Page: pd, TopBar: name == "header" && r.TopBar != "off"}
	for _, sec := range r.Sections {
		sv := &SectionView{ID: sec.ID, Layout: sec.Layout, Class: sectionClass(sec)}
		for _, col := range sec.Columns {
			views := make([]*BlockView, 0, len(col))
			for i := range col {
				b := &col[i]
				if b.Type == builder.MetaType {
					rv.HasMeta = true
				}
				views = append(views, s.blockView(b, lang, pd, media))
			}
			sv.Columns = append(sv.Columns, views)
		}
		rv.Sections = append(rv.Sections, sv)
	}
	return rv
}

// rendersH1 reports whether a main region has a block that renders the
// page's h1 (see the templates in ui/shared/templates/blocks).
func rendersH1(rv *RegionView) bool {
	for _, sec := range rv.Sections {
		for _, col := range sec.Columns {
			for _, b := range col {
				switch {
				case b.Type == "hero" && (rv.Name == "home" || rv.Name == "404"),
					b.Type == "post-list" && rv.Name != "home",
					b.Type == "single-content" && b.On("title"):
					return true
				}
			}
		}
	}
	return false
}

func sectionClass(sec builder.Section) string {
	cl := []string{"sap-section"}
	for _, k := range builder.StyleOrder {
		v := sec.Style[k]
		if v == "" {
			v = builder.SectionStyles[k][0]
		}
		cl = append(cl, "sap-"+k+"-"+v)
	}
	return strings.Join(cl, " ")
}

func (s *Server) blockView(b *builder.Block, lang string, pd *PageData, media func(int64) *store.Media) *BlockView {
	v := &BlockView{ID: b.ID, Type: b.Type, S: map[string]string{}, Page: pd}
	t, _ := builder.TypeOf(b.Type)
	for _, f := range t.Fields {
		v.S[f.Key] = b.Value(f.Key, lang)
		// A translatable default ("Learn more") is shown in the page language.
		if f.I18n && f.Default != "" && b.Settings[f.Key] == "" && b.I18n[lang][f.Key] == "" {
			v.S[f.Key] = s.tr2(lang, f.Default)
		}
	}
	image := func(key string) *ImageView {
		id, err := strconv.ParseInt(v.S[key], 10, 64)
		if err != nil || id <= 0 {
			return nil
		}
		if m := media(id); m != nil && m.IsImage() {
			return imageView(m)
		}
		return nil
	}
	switch b.Type {
	case "rich-text":
		v.HTML = content.RenderPlain(v.S["text"])
	case "hero", "image":
		v.Image = image("image")
		if v.Image != nil && v.S["alt"] != "" {
			v.Image.Alt = v.S["alt"]
		}
	case "social-links":
		t, _ := builder.TypeOf(b.Type)
		icons := map[string]string{"email": "mail", "website": "globe", "mastodon": "at-sign", "bluesky": "at-sign"}
		for _, k := range builder.SocialKeys {
			u := v.S[k]
			if u == "" {
				continue
			}
			f, _ := t.Field(k)
			if k == "email" {
				u = "mailto:" + u
			}
			icon := icons[k]
			if icon == "" {
				icon = "link"
			}
			v.Links = append(v.Links, SocialLink{Key: k, Label: s.tr2(lang, f.Label), URL: u, Icon: icon})
		}
		if v.On("rss") {
			v.Links = append(v.Links, SocialLink{Key: "rss", Label: "RSS", URL: pd.Site.FeedRSS, Icon: "rss"})
		}
	}
	return v
}

// tr2 translates an English label into lang.
func (s *Server) tr2(lang, key string) string { return s.i18n.T(lang, key) }

// ---------------------------------------------------------------- admin --

type builderData struct {
	Theme       *theme.Theme
	State       string // JSON for builder.js
	HasDraft    bool
	DraftAt     *time.Time
	PublishedAt *time.Time
	Revisions   []store.CustomRevision
	Active      bool
	Icons       []string // icons builder.js needs, rendered into a <template>
}

// builderIcons are the editor's own icons; the block types add theirs.
var builderIcons = []string{"grip-vertical", "copy", "trash-2", "plus", "chevron-up", "chevron-down", "sliders-horizontal",
	"columns-2", "panel-top", "panel-bottom", "layout-template", "lock", "circle-alert", "languages"}

// builderState is the JSON the editor starts from.
type builderState struct {
	Theme     string               `json:"theme"`
	Layout    *builder.Layouts     `json:"layout"`
	Types     []builder.Type       `json:"types"`
	Presets   []string             `json:"presets"`
	Styles    []styleField         `json:"styles"`
	Regions   []regionInfo         `json:"regions"`
	Menus     []theme.MenuLocation `json:"menus"`
	Areas     []theme.WidgetArea   `json:"areas"`
	Langs     []langOption         `json:"langs"`
	MainLang  string               `json:"main_lang"`
	Images    []mediaOption        `json:"images"`
	DesignURL string               `json:"design_url"`
}

type styleField struct {
	Key     string           `json:"key"`
	Label   string           `json:"label"`
	Choices []builder.Choice `json:"choices"`
}

type regionInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Preview string `json:"preview"` // a page that shows the region
}

type langOption struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type mediaOption struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Thumb string `json:"thumb"`
}

func (s *Server) adminBuilder(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	l, row, err := s.draftLayout(ctx, th, s.mediaSet(ctx))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	revs, err := s.store.CustomRevisions(ctx, th.Slug, store.CustomLayout)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := builderState{Theme: th.Slug, Layout: l, Presets: builder.PresetOrder,
		Langs: []langOption{}, MainLang: s.mainLang(), Regions: s.builderRegions(ctx, th),
		DesignURL: "/admin/appearance/themes/" + th.Slug + "/customize"}
	st.Menus, st.Areas = []theme.MenuLocation{}, []theme.WidgetArea{}
	for _, m := range th.Menus {
		st.Menus = append(st.Menus, m)
	}
	for _, a := range th.WidgetAreas {
		st.Areas = append(st.Areas, a)
	}
	for _, l := range s.extraLangs() {
		st.Langs = append(st.Langs, langOption{Code: l.Code, Name: l.Name})
	}
	// The schema with its labels in the admin's language.
	for _, t := range builder.Types {
		t.Label, t.Help = s.t(t.Label), s.tOpt(t.Help)
		fields := make([]builder.Field, len(t.Fields))
		for i, f := range t.Fields {
			f.Label, f.Help = s.t(f.Label), s.tOpt(f.Help)
			if f.Default != "" && f.I18n {
				f.Default = s.t(f.Default)
			}
			chs := make([]builder.Choice, len(f.Choices))
			for j, c := range f.Choices {
				chs[j] = builder.Choice{Value: c.Value, Label: s.t(c.Label)}
			}
			f.Choices = chs
			fields[i] = f
		}
		t.Fields = fields
		st.Types = append(st.Types, t)
	}
	styleLabels := map[string]string{"background": s.t("Background"), "padding": s.t("Spacing"), "width": s.t("Width"), "align": s.t("Alignment")}
	valueLabels := map[string]string{"none": s.t("None"), "muted": s.t("Muted"), "card": s.t("Card"), "primary": s.t("Primary colour"),
		"accent": s.t("Accent colour"), "inverse": s.t("Inverted"), "small": s.t("Small"), "medium": s.t("Medium"), "large": s.t("Large"),
		"content": s.t("Content width"), "narrow": s.t("Narrow"), "wide": s.t("Wide"), "full": s.t("Full width"), "start": s.t("Left"), "center": s.t("Centre")}
	for _, k := range builder.StyleOrder {
		sf := styleField{Key: k, Label: styleLabels[k]}
		for _, v := range builder.SectionStyles[k] {
			sf.Choices = append(sf.Choices, builder.Choice{Value: v, Label: valueLabels[v]})
		}
		st.Styles = append(st.Styles, sf)
	}
	if imgs, _, err := s.store.ListMedia(ctx, store.MediaQuery{ImagesOnly: true, Limit: 200}); err == nil {
		for _, m := range imgs {
			st.Images = append(st.Images, mediaOption{ID: m.ID, Name: firstNonEmpty(m.Alt, m.OriginalName), Thumb: m.VariantURL("thumb")})
		}
	}
	if st.Images == nil {
		st.Images = []mediaOption{}
	}
	b, _ := json.Marshal(st)
	data := builderData{Theme: th, State: string(b), HasDraft: row.Draft != nil, DraftAt: row.DraftAt, PublishedAt: row.PublishedAt,
		Revisions: revs, Active: s.themes.Active(s.settings().ActiveTheme) == th, Icons: append([]string{}, builderIcons...)}
	for _, t := range builder.Types {
		data.Icons = append(data.Icons, t.Icon)
	}
	w.Header().Set("Content-Security-Policy", setCSPDirective(w.Header().Get("Content-Security-Policy"), "frame-src", "'self'"))
	s.renderAdmin(w, r, "builder", s.t("Page builder · %s", th.Name), "builder", data)
}

// tOpt translates s unless it is empty.
func (s *Server) tOpt(v string) string {
	if v == "" {
		return ""
	}
	return s.t(v)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// builderRegions lists the regions with a page of this site that shows each.
func (s *Server) builderRegions(ctx context.Context, th *theme.Theme) []regionInfo {
	q := "&preview_theme=" + url.QueryEscape(th.Slug) + "&customize=draft"
	home := "/?" + strings.TrimPrefix(q, "&")
	out := []regionInfo{
		{Name: "header", Label: s.t("Header (all pages)"), Preview: home},
		{Name: "home", Label: s.t("Home page"), Preview: home},
	}
	single, page, archive := "", "", ""
	if posts, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Lang: s.mainLang(), PublicOnly: true, Limit: 1}); err == nil && len(posts) > 0 {
		single = s.postURL(&posts[0])
	}
	if pages, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePage, Lang: s.mainLang(), PublicOnly: true, Limit: 1}); err == nil && len(pages) > 0 {
		page = s.postURL(&pages[0])
	}
	if terms, err := s.store.ListTerms(ctx, "category", s.mainLang()); err == nil {
		for i := range terms {
			if terms[i].Count > 0 {
				archive = termURL(&terms[i])
				break
			}
		}
	}
	with := func(p string) string {
		if p == "" {
			return ""
		}
		if strings.Contains(p, "?") {
			return p + q
		}
		return p + "?" + strings.TrimPrefix(q, "&")
	}
	out = append(out,
		regionInfo{Name: "single", Label: s.t("Posts"), Preview: with(single)},
		regionInfo{Name: "page", Label: s.t("Pages"), Preview: with(page)},
		regionInfo{Name: "archive", Label: s.t("Archives"), Preview: with(archive)},
		regionInfo{Name: "search", Label: s.t("Search results"), Preview: "/search?q=a" + q},
		regionInfo{Name: "404", Label: s.t("Page not found"), Preview: with("/this-page-does-not-exist")},
		regionInfo{Name: "footer", Label: s.t("Footer (all pages)"), Preview: home},
	)
	return out
}

type layoutResult struct {
	OK      bool                 `json:"ok"`
	Errors  []builder.FieldError `json:"errors,omitempty"`
	Message string               `json:"message,omitempty"`
	SavedAt string               `json:"saved_at,omitempty"`
	Layout  *builder.Layouts     `json:"layout,omitempty"`
}

// parseLayout reads and validates layouts from a JSON body ({"layout": …}
// or the layouts themselves).
func (s *Server) parseLayout(r *http.Request, th *theme.Theme, ms *mediaSet) (*builder.Layouts, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, builder.MaxJSON+1))
	if err != nil {
		return nil, err
	}
	var env struct {
		Layout json.RawMessage `json:"layout"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Layout) > 0 {
		raw = env.Layout
	}
	return s.parseLayoutJSON(raw, th, ms)
}

// parseLayoutJSON is builder.Parse with the images looked up in one query.
func (s *Server) parseLayoutJSON(raw []byte, th *theme.Theme, ms *mediaSet) (*builder.Layouts, error) {
	if len(raw) <= builder.MaxJSON {
		ms.prefetch(builder.ImageIDs(raw))
	}
	return builder.Parse(raw, s.layoutContext(th, ms))
}

func (s *Server) adminBuilderDraft(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, layoutResult{Message: s.t("Not found.")})
		return
	}
	ms := s.mediaSet(r.Context())
	l, err := s.parseLayout(r, th, ms)
	if err != nil {
		var errs builder.Errors
		switch {
		case ms.err != nil:
			s.log.Error("check theme layout images", "theme", th.Slug, "err", err)
			writeJSON(w, http.StatusInternalServerError, layoutResult{Message: s.t("Something went wrong. The error has been logged.")})
		case errors.As(err, &errs):
			writeJSON(w, http.StatusUnprocessableEntity, layoutResult{Errors: errs, Message: s.t("Some values were not accepted.")})
		default:
			writeJSON(w, http.StatusBadRequest, layoutResult{Message: s.t("The request could not be read.")})
		}
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomLayout, l.JSON(), uid(r)); err != nil {
		s.log.Error("save theme layout draft", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, layoutResult{Message: s.t("Something went wrong. The error has been logged.")})
		return
	}
	// The normalised layout (ids fixed, the footer credit put back) goes
	// back to the editor.
	writeJSON(w, http.StatusOK, layoutResult{OK: true, SavedAt: time.Now().In(s.loc()).Format("15:04:05"), Layout: l})
}

func (s *Server) builderDone(w http.ResponseWriter, r *http.Request, th *theme.Theme, kind, msg string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.redirect(w, r, "/admin/appearance/themes/"+th.Slug+"/builder", kind, msg)
}

func (s *Server) adminBuilderPublish(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	l, row, err := s.draftLayout(ctx, th, s.mediaSet(ctx))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if row.Draft == nil {
		s.builderDone(w, r, th, "info", s.t("There are no unpublished changes."))
		return
	}
	if err := s.store.PublishCustom(ctx, th.Slug, store.CustomLayout, l.JSON(), uid(r), nil); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.custom.invalidate()
	s.contentChanged()
	s.audit(r, uid(r), "theme.layout_published", th.Slug, nil)
	s.builderDone(w, r, th, "success", s.t("Published. Visitors now see the new layout."))
}

func (s *Server) adminBuilderDiscard(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	if err := s.store.DiscardCustomDraft(r.Context(), th.Slug, store.CustomLayout); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.layout_discarded", th.Slug, nil)
	s.builderDone(w, r, th, "success", s.t("Draft discarded."))
}

func (s *Server) adminBuilderRestore(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	rev, err := s.store.CustomRevision(r.Context(), th.Slug, store.CustomLayout, id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	l, err := s.loadLayout(rev.Data, th, s.mediaSet(r.Context()))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomLayout, l.JSON(), uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.layout_restored", th.Slug, map[string]string{"revision": strconv.FormatInt(id, 10)})
	s.builderDone(w, r, th, "success", s.t("Version restored into the draft. Publish to apply it."))
}

// -------------------------------------------------------- export/import --

// layoutExport returns the theme's layouts (the draft, else the published
// ones) for an export file, or nil when there are none.
func (s *Server) layoutExport(ctx context.Context, th *theme.Theme, ms *mediaSet) (json.RawMessage, error) {
	l, _, err := s.draftLayout(ctx, th, ms)
	if err != nil {
		return nil, err
	}
	if l.IsZero() {
		return nil, nil
	}
	return json.Marshal(l)
}

// parseImportLayout validates the layouts of an export file (nil when it
// has none).
func (s *Server) parseImportLayout(r *http.Request, th *theme.Theme, raw json.RawMessage, ms *mediaSet) (*builder.Layouts, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return s.parseLayoutJSON(raw, th, ms)
}
