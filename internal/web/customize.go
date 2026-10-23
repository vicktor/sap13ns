package web

// Theme customiser (Appearance → Customise): a per-theme design of colours,
// fonts, scales, header, footer and home options, edited as a draft with a
// live preview and published for visitors. See internal/customize for the
// model, validation and generated stylesheet.
//
// Drafts are only ever rendered for signed-in users with the appearance
// capability who ask for them with ?customize=draft. Such requests carry a
// session, which the page cache (cached in public.go) never stores, and the
// response is no-store as a second line of defence.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/builder"
	"sap13ns/internal/customize"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// CustomView is what theme templates see of the customisation (.Site.Custom).
type CustomView struct {
	CSS        string     // customiser stylesheet URL ("" for none)
	Class      string     // extra classes for <html>
	Logo       *ImageView // header logo, when the brand shows one
	LogoOnly   bool       // the logo replaces the site title
	HideSearch bool       // no search link in the header
	HideLang   bool       // no language switch
	HomeLayout string     // "" (theme) | list | grid | bento
	Hero       string     // "" (theme) | on | off
	PerPage    int        // home posts per page, 0 = reading settings
	Preview    bool       // a draft rendered for the customiser preview
	Partial    bool       // built from a failed load: kept out of every cache
	Motion     bool       // the design animates texts or sets how much things move
}

// HeroOn reports whether the home page shows its introduction, given the
// theme's own default for themes without a hero option.
func (c *CustomView) HeroOn(themeDefault bool) bool {
	switch c.Hero {
	case "on":
		return true
	case "off":
		return false
	}
	return themeDefault
}

// customCache keeps each theme's published design and stylesheet.
type customCache struct {
	mu     sync.Mutex
	gen    uint64
	themes map[string]*customEntry
}

type customEntry struct {
	gen    uint64
	layout *builder.Layouts // published block layouts (builder.go)
	design *customize.Design
	css    string
	url    string // "" when the design renders no CSS
	hash   string
	logo   *ImageView
	// The media the design and layouts use, looked up once per cache
	// generation (nil values: no such file). Read-only once cached.
	media map[int64]*store.Media
	// partial marks an entry built from a failed load: served once, never
	// cached, and the page it renders is kept out of the page cache.
	partial bool
}

// mediaByID returns a file the published design or layouts use.
func (e *customEntry) mediaByID(id int64) *store.Media { return e.media[id] }

// customFillTimeout bounds loading a theme's published design and layouts.
var customFillTimeout = 10 * time.Second

func (c *customCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
}

func (s *Server) customRoutes(mux *http.ServeMux, admin func(string, auth.Cap, http.HandlerFunc)) {
	mux.HandleFunc("GET /themes/{theme}/custom/{file}", s.handleCustomCSS)
	base := "/admin/appearance/themes/{slug}/customize"
	admin("GET "+base, auth.CapAppearance, s.adminCustomize)
	admin("POST "+base+"/draft", auth.CapAppearance, s.adminCustomizeDraft)
	admin("POST "+base+"/publish", auth.CapAppearance, s.adminCustomizePublish)
	admin("POST "+base+"/discard", auth.CapAppearance, s.adminCustomizeDiscard)
	admin("POST "+base+"/reset", auth.CapAppearance, s.adminCustomizeReset)
	admin("GET "+base+"/export", auth.CapAppearance, s.adminCustomizeExport)
	admin("POST "+base+"/import", auth.CapAppearance, s.adminCustomizeImport)
	admin("POST "+base+"/revisions/{id}/restore", auth.CapAppearance, s.adminCustomizeRestore)
	admin("POST "+base+"/derive", auth.CapAppearance, s.adminCustomizeDerive)
	admin("POST /admin/appearance/themes/{slug}/design/ai", auth.CapAppearance, s.apiDesignAI)
	s.builderRoutes(mux, admin) // the block builder (builder.go)
	// Appearance → Customise opens the active theme.
	admin("GET /admin/appearance/customize", auth.CapAppearance, func(w http.ResponseWriter, r *http.Request) {
		th := s.themes.Active(s.settings().ActiveTheme)
		if th == nil {
			s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
			return
		}
		http.Redirect(w, r, "/admin/appearance/themes/"+th.Slug+"/customize", http.StatusSeeOther)
	})
}

// ------------------------------------------------------------ resolving --

// mediaSet looks up the media of one run (loading, validating or rendering
// a design or layouts), each id once. Only "not found" means a file is
// missing: any other failure (a cancelled request, a timeout, an exhausted
// pool) is kept in err and returned by the validators, and the run must
// then be neither cached nor saved, or a passing problem would strip the
// fonts, the logo and the images until the next change.
type mediaSet struct {
	ctx context.Context
	st  *store.Store
	m   map[int64]*store.Media // nil value: no such file
	err error                  // the first failed lookup
}

func (s *Server) mediaSet(ctx context.Context) *mediaSet {
	return &mediaSet{ctx: ctx, st: s.store, m: map[int64]*store.Media{}}
}

// get returns the file with id (nil when there is none).
func (ms *mediaSet) get(id int64) (*store.Media, error) {
	if id <= 0 {
		return nil, nil
	}
	if m, ok := ms.m[id]; ok {
		return m, nil
	}
	if ms.err != nil {
		return nil, ms.err // the database is failing: do not insist
	}
	m, err := ms.st.MediaByID(ms.ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		m, err = nil, nil
	}
	if err != nil {
		ms.err = err
		return nil, err
	}
	ms.m[id] = m
	return m, nil
}

// prefetch looks up ids in one query.
func (ms *mediaSet) prefetch(ids []int64) {
	var need []int64
	for _, id := range ids {
		if _, ok := ms.m[id]; !ok && id > 0 {
			need = append(need, id)
		}
	}
	if len(need) == 0 || ms.err != nil {
		return
	}
	found, err := ms.st.MediaByIDs(ms.ctx, need)
	if err != nil {
		ms.err = err
		return
	}
	for _, id := range need {
		ms.m[id] = found[id] // nil when missing
	}
}

// lookup is get for rendering: nil for missing and failed lookups alike
// (a failure is in ms.err).
func (ms *mediaSet) lookup(id int64) *store.Media {
	m, _ := ms.get(id)
	return m
}

func (ms *mediaSet) exists(id int64, kind string) (bool, error) {
	m, err := ms.get(id)
	if err != nil || m == nil {
		return false, err
	}
	if kind == "font" {
		return m.MIME == "font/woff2", nil
	}
	return m.IsImage(), nil
}

// fontURL resolves the design's uploaded fonts for its stylesheet.
func (ms *mediaSet) fontURL(id int64) string {
	m := ms.lookup(id)
	if m == nil || m.MIME != "font/woff2" {
		return ""
	}
	return m.URL()
}

// designContext is what validating a design of th needs.
func (s *Server) designContext(th *theme.Theme, ms *mediaSet) customize.Context {
	c := customize.Context{Langs: s.siteLangs(), Media: ms.exists, Content: contentSpecs(th)}
	for _, o := range th.Options {
		spec := customize.OptionSpec{Key: o.Key, Type: o.Type}
		for _, ch := range o.Choices {
			spec.Choices = append(spec.Choices, ch.Value)
		}
		c.Options = append(c.Options, spec)
	}
	return c
}

func logoView(ms *mediaSet, d *customize.Design) *ImageView {
	if d.Header.Brand == "" || d.Header.Logo == 0 {
		return nil
	}
	if m := ms.lookup(d.Header.Logo); m != nil {
		return imageView(m)
	}
	return nil
}

func customCSSURL(slug, prefix, css string) string {
	return "/themes/" + slug + "/custom/" + prefix + customize.Hash(css) + ".css"
}

// publishedDesign returns the cached published design of a theme.
func (s *Server) publishedDesign(ctx context.Context, th *theme.Theme) *customEntry {
	c := &s.custom
	c.mu.Lock()
	gen := c.gen
	if e := c.themes[th.Slug]; e != nil && e.gen == gen {
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()
	// The entry serves every visitor until the next change, so it is not
	// tied to this request: a visitor who goes away mid-load must not
	// leave a design without its fonts behind.
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), customFillTimeout)
	defer cancel()
	e, err := s.loadPublished(fctx, th, gen)
	if err != nil {
		s.log.Error("load published theme design", "theme", th.Slug, "err", err)
		e.partial = true
		return e // served this once, not cached: try again next time
	}
	c.mu.Lock()
	if c.themes == nil {
		c.themes = map[string]*customEntry{}
	}
	if c.gen == gen {
		c.themes[th.Slug] = e
	}
	c.mu.Unlock()
	return e
}

// loadPublished builds the cache entry of th's published design and
// layouts. The entry is usable even with an error, for one response.
func (s *Server) loadPublished(ctx context.Context, th *theme.Theme, gen uint64) (*customEntry, error) {
	e := &customEntry{gen: gen, design: &customize.Design{}, layout: &builder.Layouts{}}
	ms := s.mediaSet(ctx)
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomDesign)
	if err != nil {
		return e, err
	}
	prefetchContent(ms, th, row.Published)
	e.design, _ = customize.Load(row.Published, s.designContext(th, ms)) // a lookup failure is in ms.err
	l, err := s.loadPublishedLayout(ctx, th, ms)
	if err != nil {
		return e, fmt.Errorf("layout: %w", err)
	}
	e.layout = l
	e.css = e.design.Stylesheet(ms.fontURL)
	if e.css != "" {
		e.hash = customize.Hash(e.css)
		e.url = customCSSURL(th.Slug, "", e.css)
	}
	e.logo = logoView(ms, e.design)
	e.media = ms.m
	if ms.err != nil {
		return e, fmt.Errorf("media: %w", ms.err)
	}
	return e, nil
}

// canCustomize reports whether the request's user may see drafts: the
// appearance capability, with the second factor the admin would demand.
func (s *Server) canCustomize(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && auth.Can(u.Role, auth.CapAppearance) && !s.mustEnroll2FA(r, u)
}

// wantsDraft reports whether this request should render the draft design.
func (s *Server) wantsDraft(r *http.Request) bool {
	return r.URL.Query().Get("customize") == "draft" && s.canCustomize(r)
}

// draftDesign loads the draft design of th (the published one when there
// is no draft), looking its media up in ms. An error, a failed media
// lookup included, means the design must not be saved or published.
func (s *Server) draftDesign(ctx context.Context, th *theme.Theme, ms *mediaSet) (*customize.Design, *store.ThemeCustom, error) {
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomDesign)
	if err != nil {
		return nil, nil, err
	}
	raw := row.Published
	if row.Draft != nil {
		raw = row.Draft
	}
	prefetchContent(ms, th, raw)
	d, err := customize.Load(raw, s.designContext(th, ms))
	if err != nil {
		return nil, nil, err
	}
	return d, row, nil
}

// applyCustom fills sv.Custom and applies the design's overrides.
func (s *Server) applyCustom(r *http.Request, th *theme.Theme, sv *SiteView) {
	sv.Custom = &CustomView{}
	ctx := r.Context()
	var d *customize.Design
	if s.wantsDraft(r) {
		var err error
		ms := s.mediaSet(ctx)
		if d, _, err = s.draftDesign(ctx, th, ms); err != nil {
			s.log.Error("load theme draft", "theme", th.Slug, "err", err)
			d = &customize.Design{}
		}
		css := d.Stylesheet(ms.fontURL)
		// Always link a stylesheet in previews, so the customiser can
		// swap its URL after every change.
		sv.Custom.CSS = customCSSURL(th.Slug, "draft-", css)
		sv.Custom.Logo = logoView(ms, d)
		sv.Custom.Preview = true
		sv.content = &siteContent{design: d, media: ms.lookup, main: s.mainLang()}
		if len(d.Options) > 0 {
			stored, err := s.store.ThemeOptions(ctx, th.Slug)
			if err != nil {
				stored = map[string]string{}
			}
			for k, v := range d.Options {
				stored[k] = v
			}
			sv.Options = th.OptionValues(stored)
		}
	} else {
		e := s.publishedDesign(ctx, th)
		d = e.design
		sv.Custom.CSS, sv.Custom.Logo, sv.Custom.Partial = e.url, e.logo, e.partial
		sv.content = &siteContent{design: d, media: e.mediaByID, main: s.mainLang()}
	}
	sv.texts = &theme.Texts{Over: d.Texts[sv.Lang], Mark: sv.Custom.Preview}
	cv := sv.Custom
	cv.Class = d.HTMLClasses()
	cv.Motion = d.Motion.Used()
	cv.LogoOnly = d.Header.Brand == "logo" && cv.Logo != nil
	if cv.Logo == nil {
		cv.Class = strings.TrimSpace(strings.ReplaceAll(cv.Class, "sap-has-logo", ""))
	}
	cv.HideSearch, cv.HideLang = d.Header.HideSearch, d.Header.HideLang
	cv.HomeLayout, cv.Hero, cv.PerPage = d.Home.Layout, d.Home.Hero, d.Home.PerPage
	if d.Home.Hero != "" {
		if _, ok := sv.Options["hero"]; ok {
			sv.Options["hero"] = strconv.FormatBool(d.Home.Hero == "on")
		}
	}
	if t := d.Footer.TextIn(sv.Lang); t != "" {
		sv.FooterText = t
	}
}

// customPreviewHeaders lets the customiser frame a draft preview (only
// from this origin) and keeps it out of every cache, as it does a page
// built while the published design could not be loaded whole.
func (s *Server) customPreviewHeaders(w http.ResponseWriter, pd *PageData) {
	if pd.Site == nil || pd.Site.Custom == nil {
		return
	}
	h := w.Header()
	if pd.Site.Custom.Partial {
		h.Set("Cache-Control", "no-store")
	}
	if !pd.Site.Custom.Preview {
		return
	}
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Content-Security-Policy", setCSPDirective(h.Get("Content-Security-Policy"), "frame-ancestors", "'self'"))
}

// setCSPDirective replaces (or adds) one directive of a policy.
func setCSPDirective(policy, directive, value string) string {
	parts := strings.Split(policy, ";")
	found := false
	for i, p := range parts {
		f := strings.Fields(p)
		if len(f) > 0 && strings.EqualFold(f[0], directive) {
			parts[i] = " " + directive + " " + value
			found = true
		}
	}
	if !found {
		parts = append(parts, " "+directive+" "+value)
	}
	return strings.TrimSpace(strings.Join(parts, ";"))
}

// handleCustomCSS serves the generated stylesheet. Published versions are
// content-addressed and immutable; an outdated hash redirects to the
// current one. Drafts are private to customisers and never cached.
func (s *Server) handleCustomCSS(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("theme"))
	name, isCSS := strings.CutSuffix(r.PathValue("file"), ".css")
	if !ok || !isCSS {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	if strings.HasPrefix(name, "draft-") {
		if !s.canCustomize(r) {
			http.NotFound(w, r)
			return
		}
		ms := s.mediaSet(r.Context())
		d, _, err := s.draftDesign(r.Context(), th, ms)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "private, no-store")
		io.WriteString(w, d.Stylesheet(ms.fontURL))
		return
	}
	e := s.publishedDesign(r.Context(), th)
	if e.partial {
		// Never hand out (or redirect to) a stylesheet missing its fonts.
		h.Set("Cache-Control", "no-store")
		h.Set("Retry-After", "5")
		http.Error(w, "stylesheet temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if e.url == "" {
		http.NotFound(w, r)
		return
	}
	if name != e.hash {
		h.Set("Cache-Control", "no-cache")
		http.Redirect(w, r, e.url, http.StatusFound)
		return
	}
	h.Set("Content-Type", "text/css; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", `"`+e.hash+`"`)
	if r.Header.Get("If-None-Match") == `"`+e.hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	io.WriteString(w, e.css)
}

// ---------------------------------------------------------------- admin --

type customizeData struct {
	Theme        *theme.Theme
	AI           *designAIView     // nil: no design assistant
	Design       *customize.Design // the draft (or published) design being edited
	Colors       []colorRow
	State        string // JSON for customizer.js: design, defaults, options
	HasDraft     bool
	FrontOffer   bool           // publishing can make the front layout the site's front page
	AIPages      int            // the design assistant's draft pages, published with the design
	Animations   []selectOption // the motion library's, for the texts and images
	MotionLevels []selectOption
	DraftAt      *time.Time
	PublishedAt  *time.Time
	PreviewURL   string
	Revisions    []store.CustomRevision
	Fonts        []store.Media      // WOFF2 files in the media library
	Images       []store.Media      // images for the logo
	Langs        []LangLink         // extra languages for the footer text
	Options      map[string]string  // option values in effect in the draft
	Active       bool               // the theme is the site's active theme
	Content      []contentGroupView // the theme's texts and images (theme_content.go)
	ContentLang  []LangLink         // the languages of the texts, the main one first
}

// colorRow is one colour token in the customiser form.
type colorRow struct {
	Key, Label        string
	Light, Dark       string // the design's values, "" for the theme's
	DefLight, DefDark string // the theme's values for the current options
}

func (s *Server) colorLabels(r *http.Request) map[string]string {
	return map[string]string{
		"background": s.tu(r, "Background"), "foreground": s.tu(r, "Text"),
		"card": s.tu(r, "Cards"), "card-foreground": s.tu(r, "Text on cards"),
		"muted": s.tu(r, "Muted surfaces"), "muted-foreground": s.tu(r, "Secondary text"),
		"border":  s.tu(r, "Borders"),
		"primary": s.tu(r, "Primary"), "primary-foreground": s.tu(r, "Text on primary"),
		"accent": s.tu(r, "Accent"), "accent-foreground": s.tu(r, "Text on accent"),
	}
}

// customizerState is the JSON the customiser script starts from.
type customizerState struct {
	Theme     string                                     `json:"theme"`
	Design    *customize.Design                          `json:"design"`
	Published *customize.Design                          `json:"published"`
	Light     map[string]string                          `json:"light"` // theme defaults for the current options
	Dark      map[string]string                          `json:"dark"`
	Base      map[string]map[string]string               `json:"base"` // defaults without option overrides
	Overrides map[string]map[string]theme.TokenOverrides `json:"overrides,omitempty"`
	Options   map[string]string                          `json:"options"` // stored option values
	Warnings  []customize.Warning                        `json:"warnings"`
	Tokens    []string                                   `json:"tokens"`
	Fonts     map[string]string                          `json:"fonts"` // curated stacks, for the font previews
}

func (s *Server) adminCustomize(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	ms := s.mediaSet(ctx)
	d, row, err := s.draftDesign(ctx, th, ms)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stored, err := s.store.ThemeOptions(ctx, th.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pub, err := customize.Load(row.Published, s.designContext(th, ms))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	revs, err := s.store.CustomRevisions(ctx, th.Slug, store.CustomDesign)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	effective := map[string]string{}
	for k, v := range stored {
		effective[k] = v
	}
	for k, v := range d.Options {
		effective[k] = v
	}
	data := customizeData{Theme: th, Design: d, HasDraft: row.Draft != nil, DraftAt: row.DraftAt, PublishedAt: row.PublishedAt,
		PreviewURL: "/?preview_theme=" + th.Slug + "&customize=draft", Revisions: revs, Langs: s.extraLangs(),
		Options: th.OptionValues(effective), Active: s.themes.Active(s.settings().ActiveTheme) == th}
	data.Animations = s.animationOptions(r)
	data.AI = s.designAIView(r)
	data.AIPages = s.aiPagesPending(r, th)
	data.HasDraft = data.HasDraft || data.AIPages > 0
	// The draft's new front page shows in the preview, though the site's
	// front page is something else for now.
	if data.FrontOffer = s.frontOffer(r, th); data.FrontOffer {
		data.PreviewURL += "&front=1"
	}
	data.MotionLevels = []selectOption{{Value: "", Label: s.tu(r, "Normal")}, {Value: "subtle", Label: s.tu(r, "Subtle")},
		{Value: "expressive", Label: s.tu(r, "Expressive")}, {Value: "off", Label: s.tu(r, "Off: nothing moves")}}
	if lr, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomLayout); err == nil {
		data.HasDraft = data.HasDraft || lr.Draft != nil
	}
	if fonts, _, err := s.store.ListMedia(ctx, store.MediaQuery{MIME: "font/woff2", Limit: 100}); err == nil {
		data.Fonts = fonts
	}
	if imgs, _, err := s.store.ListMedia(ctx, store.MediaQuery{ImagesOnly: true, Limit: 100}); err == nil {
		data.Images = imgs
	}
	if len(th.Content) > 0 {
		data.Content = s.contentForm(r, th, d, ms)
		data.ContentLang = append([]LangLink{{Code: s.mainLang(), Name: langName(s.mainLang())}}, s.extraLangs()...)
	}
	st := s.customizerState(th, d, pub, stored)
	b, _ := json.Marshal(st)
	data.State = string(b)
	labels := s.colorLabels(r)
	for _, k := range customize.ColorTokens {
		data.Colors = append(data.Colors, colorRow{Key: k, Label: labels[k], Light: d.Light[k], Dark: d.Dark[k],
			DefLight: st.Light[k], DefDark: st.Dark[k]})
	}
	// The live preview is an iframe of this site.
	w.Header().Set("Content-Security-Policy", setCSPDirective(w.Header().Get("Content-Security-Policy"), "frame-src", "'self'"))
	s.renderAdmin(w, r, "customize", s.tu(r, "Design · %s", th.Name), "customize", data)
}

func (s *Server) customizerState(th *theme.Theme, d, pub *customize.Design, stored map[string]string) customizerState {
	opts := map[string]string{}
	for k, v := range stored {
		opts[k] = v
	}
	for k, v := range d.Options {
		opts[k] = v
	}
	light, dark := th.DefaultColors(th.OptionValues(opts))
	st := customizerState{Theme: th.Slug, Design: d, Published: pub, Light: light, Dark: dark,
		Options: th.OptionValues(stored), Tokens: customize.ColorTokens, Fonts: map[string]string{}}
	if th.Tokens != nil {
		st.Base = map[string]map[string]string{"light": th.Tokens.Light, "dark": th.Tokens.Dark}
		st.Overrides = th.Tokens.Options
	}
	st.Warnings = customize.CheckContrast(d, light, dark)
	if st.Warnings == nil {
		st.Warnings = []customize.Warning{}
	}
	for _, f := range customize.FontStacks {
		st.Fonts[f] = customize.FontStack(f)
	}
	return st
}

type draftResult struct {
	OK       bool                   `json:"ok"`
	CSSURL   string                 `json:"css_url,omitempty"`
	Classes  string                 `json:"classes"`
	Warnings []customize.Warning    `json:"warnings"`
	Errors   []customize.FieldError `json:"errors,omitempty"`
	Message  string                 `json:"message,omitempty"`
	SavedAt  string                 `json:"saved_at,omitempty"`
	Light    map[string]string      `json:"light,omitempty"` // defaults for the draft's options
	Dark     map[string]string      `json:"dark,omitempty"`
}

// parseDesign reads and validates a design from a request body: JSON
// ({"design": {...}} or the design itself), or a form field "design".
func (s *Server) parseDesign(r *http.Request, th *theme.Theme, ms *mediaSet) (*customize.Design, error) {
	d, _, err := s.parseDesignFile(r, th, ms)
	if err == nil && d == nil {
		d = &customize.Design{} // an envelope without a design
	}
	return d, err
}

// designFail answers a design that parseDesign refused: 422 with the
// field errors, 500 when its media could not be checked, 400 otherwise.
func (s *Server) designFail(w http.ResponseWriter, r *http.Request, th *theme.Theme, ms *mediaSet, err error) {
	var errs customize.Errors
	switch {
	case ms.err != nil:
		s.log.Error("check theme design media", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, draftResult{Message: s.tu(r, "Something went wrong. The error has been logged."), Warnings: []customize.Warning{}})
	case errors.As(err, &errs):
		writeJSON(w, http.StatusUnprocessableEntity, draftResult{Errors: errs, Message: s.tu(r, "Some values were not accepted."), Warnings: []customize.Warning{}})
	default:
		writeJSON(w, http.StatusBadRequest, draftResult{Message: s.tu(r, "The request could not be read.")})
	}
}

// parseDesignFile is parseDesign that also returns the "layout" of an
// export file, unvalidated (see parseImportLayout). The design is nil when
// an export envelope has none (a file with layouts only).
func (s *Server) parseDesignFile(r *http.Request, th *theme.Theme, ms *mediaSet) (*customize.Design, json.RawMessage, error) {
	var raw []byte
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		b, err := io.ReadAll(io.LimitReader(r.Body, customize.MaxJSON+builder.MaxJSON+1))
		if err != nil {
			return nil, nil, err
		}
		raw = b
	case strings.HasPrefix(ct, "multipart/form-data"):
		if err := r.ParseMultipartForm(customize.MaxJSON + builder.MaxJSON); err != nil {
			return nil, nil, err
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, nil, customize.Errors{{Message: s.tu(r, "Choose a JSON file to import.")}}
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, customize.MaxJSON+builder.MaxJSON+1))
		if err != nil {
			return nil, nil, err
		}
		raw = b
	default:
		raw = []byte(r.FormValue("design"))
	}
	// Accept the export envelope, {"design": ...} and a bare design.
	var env struct {
		Format string          `json:"format"`
		Theme  string          `json:"theme"`
		Design json.RawMessage `json:"design"`
		Layout json.RawMessage `json:"layout"`
	}
	var layout json.RawMessage
	if json.Unmarshal(raw, &env) == nil && (len(env.Design) > 0 || len(env.Layout) > 0) {
		raw, layout = env.Design, env.Layout
		if len(raw) == 0 || string(raw) == "null" {
			return nil, layout, nil
		}
	}
	d, err := customize.Parse(raw, s.designContext(th, ms))
	return d, layout, err
}

func (s *Server) adminCustomizeDraft(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, draftResult{Message: s.tu(r, "Not found.")})
		return
	}
	ms := s.mediaSet(r.Context())
	d, err := s.parseDesign(r, th, ms)
	if err != nil {
		s.designFail(w, r, th, ms, err)
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.log.Error("save theme draft", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, draftResult{Message: s.tu(r, "Something went wrong. The error has been logged.")})
		return
	}
	stored, _ := s.store.ThemeOptions(r.Context(), th.Slug)
	st := s.customizerState(th, d, d, stored)
	css := d.Stylesheet(ms.fontURL)
	writeJSON(w, http.StatusOK, draftResult{OK: true, CSSURL: customCSSURL(th.Slug, "draft-", css), Classes: d.HTMLClasses(),
		Warnings: st.Warnings, SavedAt: time.Now().In(s.loc()).Format("15:04:05"), Light: st.Light, Dark: st.Dark})
}

// adminCustomizeDerive suggests dark colours from the design's light ones
// (and the theme's light defaults for whatever the design leaves unset).
func (s *Server) adminCustomizeDerive(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, draftResult{Message: s.tu(r, "Not found.")})
		return
	}
	ms := s.mediaSet(r.Context())
	d, err := s.parseDesign(r, th, ms)
	if err != nil {
		s.designFail(w, r, th, ms, err)
		return
	}
	stored, _ := s.store.ThemeOptions(r.Context(), th.Slug)
	st := s.customizerState(th, d, d, stored)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dark": customize.DeriveDark(d.Light, st.Light)})
}

// customizeDone answers a customiser action: JSON for scripts, otherwise
// a redirect back to the customiser with a flash.
func (s *Server) customizeDone(w http.ResponseWriter, r *http.Request, th *theme.Theme, kind, msg string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": msg})
		return
	}
	s.redirect(w, r, customizeBack(r, th), kind, msg)
}

func (s *Server) customizeFail(w http.ResponseWriter, r *http.Request, th *theme.Theme, status int, msg string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"ok": false, "message": msg})
		return
	}
	s.redirect(w, r, customizeBack(r, th), "error", msg)
}

// customizeBack is where a customiser form returns to: the page builder
// shares export and import.
func customizeBack(r *http.Request, th *theme.Theme) string {
	if r.FormValue("return") == "builder" {
		return "/admin/appearance/themes/" + th.Slug + "/builder"
	}
	return "/admin/appearance/themes/" + th.Slug + "/customize"
}

func (s *Server) adminCustomizePublish(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	kind, msg, err := s.publishDesign(r, th)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.customizeDone(w, r, th, kind, msg)
}

func (s *Server) adminCustomizeDiscard(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	if err := s.discardDesignAll(r, th); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.customizeDone(w, r, th, "success", s.tu(r, "Draft discarded."))
}

// adminCustomizeReset starts a draft with nothing customised and the
// theme's default options; publishing it restores the theme's own look.
func (s *Server) adminCustomizeReset(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	d := &customize.Design{Version: customize.Version, Options: th.OptionValues(nil)}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.design_reset", th.Slug, nil)
	s.customizeDone(w, r, th, "success", s.tu(r, "The draft now uses the theme's defaults. Publish to apply them."))
}

// designExport is the file format of exports (layouts join it in the
// block builder).
type designExport struct {
	Format   string            `json:"format"`
	Version  int               `json:"version"`
	Theme    string            `json:"theme"`
	Exported string            `json:"exported"`
	Design   *customize.Design `json:"design"`
	Layout   json.RawMessage   `json:"layout,omitempty"`
}

const exportFormat = "sap13ns-theme-custom"

func (s *Server) adminCustomizeExport(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	ms := s.mediaSet(ctx)
	d, _, err := s.draftDesign(ctx, th, ms)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stored, err := s.store.ThemeOptions(ctx, th.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Export the options in effect too, so an import reproduces the look.
	opts := th.OptionValues(stored)
	for k, v := range d.Options {
		opts[k] = v
	}
	d.Options = opts
	exp := designExport{Format: exportFormat, Version: customize.Version, Theme: th.Slug,
		Exported: time.Now().UTC().Format(time.RFC3339), Design: d}
	if exp.Layout, err = s.layoutExport(ctx, th, ms); err != nil {
		s.serverError(w, r, err)
		return
	}
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sap13ns-%s-design.json"`, th.Slug))
	w.Write(b)
}

// adminCustomizeImport validates an exported file and makes what it
// carries (a design, layouts or both) the draft; the rest is left alone.
func (s *Server) adminCustomizeImport(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	ms := s.mediaSet(r.Context())
	d, rawLayout, err := s.parseDesignFile(r, th, ms)
	if err != nil {
		if ms.err != nil {
			s.serverError(w, r, err)
			return
		}
		var errs customize.Errors
		msg := s.tu(r, "The file could not be read.")
		if errors.As(err, &errs) {
			msg = s.tu(r, "The file was not imported: %s", s.errorSummary(r, errorStrings(errs)))
		}
		s.customizeFail(w, r, th, http.StatusUnprocessableEntity, msg)
		return
	}
	// Validate the layouts too before changing anything.
	l, err := s.parseImportLayout(r, th, rawLayout, ms)
	if err != nil {
		if ms.err != nil {
			s.serverError(w, r, err)
			return
		}
		var errs builder.Errors
		msg := s.tu(r, "The file could not be read.")
		if errors.As(err, &errs) {
			msg = s.tu(r, "The file was not imported: %s", s.errorSummary(r, errorStrings(errs)))
		}
		s.customizeFail(w, r, th, http.StatusUnprocessableEntity, msg)
		return
	}
	if d == nil && l == nil {
		s.customizeFail(w, r, th, http.StatusUnprocessableEntity, s.tu(r, "The file has neither a design nor layouts."))
		return
	}
	if d != nil {
		if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if l != nil {
		if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomLayout, l.JSON(), uid(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.audit(r, uid(r), "theme.design_imported", th.Slug, nil)
	s.customizeDone(w, r, th, "success", s.tu(r, "Imported into the draft. Check the preview, then publish."))
}

func (s *Server) adminCustomizeRestore(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	rev, err := s.store.CustomRevision(r.Context(), th.Slug, store.CustomDesign, id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	d, err := customize.Load(rev.Data, s.designContext(th, s.mediaSet(r.Context())))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.design_restored", th.Slug, map[string]string{"revision": strconv.FormatInt(id, 10)})
	s.customizeDone(w, r, th, "success", s.tu(r, "Version restored into the draft. Publish to apply it."))
}

// errorSummary joins at most five problems for a flash message, which
// travels in a cookie.
func (s *Server) errorSummary(r *http.Request, msgs []string) string {
	const max = 5
	if len(msgs) > max {
		msgs = append(msgs[:max:max], s.tu(r, "and %d more", len(msgs)-max))
	}
	return strings.Join(msgs, "; ")
}

func errorStrings[E error](es []E) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Error()
	}
	return out
}
