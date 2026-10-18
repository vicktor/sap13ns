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

	"github.com/jackc/pgx/v5"

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
}

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
	s.builderRoutes(mux, admin) // the block builder (builder.go)
	// Appearance → Customise opens the active theme.
	admin("GET /admin/appearance/customize", auth.CapAppearance, func(w http.ResponseWriter, r *http.Request) {
		th := s.themes.Active(s.settings().ActiveTheme)
		if th == nil {
			s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
			return
		}
		http.Redirect(w, r, "/admin/appearance/themes/"+th.Slug+"/customize", http.StatusSeeOther)
	})
}

// ------------------------------------------------------------ resolving --

// designContext is what validating a design of th needs.
func (s *Server) designContext(ctx context.Context, th *theme.Theme) customize.Context {
	c := customize.Context{Langs: s.siteLangs()}
	for _, o := range th.Options {
		spec := customize.OptionSpec{Key: o.Key, Type: o.Type}
		for _, ch := range o.Choices {
			spec.Choices = append(spec.Choices, ch.Value)
		}
		c.Options = append(c.Options, spec)
	}
	c.Media = func(id int64, kind string) bool {
		m, err := s.store.MediaByID(ctx, id)
		if err != nil {
			return false
		}
		if kind == "font" {
			return m.MIME == "font/woff2"
		}
		return m.IsImage()
	}
	return c
}

func (s *Server) fontResolver(ctx context.Context) customize.Resolver {
	return func(id int64) string {
		m, err := s.store.MediaByID(ctx, id)
		if err != nil || m.MIME != "font/woff2" {
			return ""
		}
		return m.URL()
	}
}

func (s *Server) logoView(ctx context.Context, d *customize.Design) *ImageView {
	if d.Header.Brand == "" || d.Header.Logo == 0 {
		return nil
	}
	m, err := s.store.MediaByID(ctx, d.Header.Logo)
	if err != nil {
		return nil
	}
	return imageView(m)
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
	e := &customEntry{gen: gen, design: &customize.Design{}, layout: &builder.Layouts{}}
	if row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomDesign); err != nil {
		s.log.Error("load theme design", "theme", th.Slug, "err", err)
		return e // not cached: try again next time
	} else {
		e.design = customize.Load(row.Published, s.designContext(ctx, th))
	}
	if l, err := s.loadPublishedLayout(ctx, th); err != nil {
		s.log.Error("load theme layout", "theme", th.Slug, "err", err)
		return e
	} else {
		e.layout = l
	}
	e.css = e.design.Stylesheet(s.fontResolver(ctx))
	if e.css != "" {
		e.hash = customize.Hash(e.css)
		e.url = customCSSURL(th.Slug, "", e.css)
	}
	e.logo = s.logoView(ctx, e.design)
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

// canCustomize reports whether the request's user may see drafts.
func canCustomize(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && auth.Can(u.Role, auth.CapAppearance)
}

// wantsDraft reports whether this request should render the draft design.
func wantsDraft(r *http.Request) bool {
	return r.URL.Query().Get("customize") == "draft" && canCustomize(r)
}

// draftDesign loads the draft design of th (the published one when there
// is no draft).
func (s *Server) draftDesign(ctx context.Context, th *theme.Theme) (*customize.Design, *store.ThemeCustom, error) {
	row, err := s.store.ThemeCustom(ctx, th.Slug, store.CustomDesign)
	if err != nil {
		return nil, nil, err
	}
	raw := row.Published
	if row.Draft != nil {
		raw = row.Draft
	}
	return customize.Load(raw, s.designContext(ctx, th)), row, nil
}

// applyCustom fills sv.Custom and applies the design's overrides.
func (s *Server) applyCustom(r *http.Request, th *theme.Theme, sv *SiteView) {
	sv.Custom = &CustomView{}
	ctx := r.Context()
	var d *customize.Design
	if wantsDraft(r) {
		var err error
		if d, _, err = s.draftDesign(ctx, th); err != nil {
			s.log.Error("load theme draft", "theme", th.Slug, "err", err)
			d = &customize.Design{}
		}
		css := d.Stylesheet(s.fontResolver(ctx))
		// Always link a stylesheet in previews, so the customiser can
		// swap its URL after every change.
		sv.Custom.CSS = customCSSURL(th.Slug, "draft-", css)
		sv.Custom.Logo = s.logoView(ctx, d)
		sv.Custom.Preview = true
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
		sv.Custom.CSS, sv.Custom.Logo = e.url, e.logo
	}
	cv := sv.Custom
	cv.Class = d.HTMLClasses()
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
// from this origin) and keeps it out of every cache.
func (s *Server) customPreviewHeaders(w http.ResponseWriter, pd *PageData) {
	if pd.Site == nil || pd.Site.Custom == nil || !pd.Site.Custom.Preview {
		return
	}
	h := w.Header()
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
		if !canCustomize(r) {
			http.NotFound(w, r)
			return
		}
		d, _, err := s.draftDesign(r.Context(), th)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "private, no-store")
		io.WriteString(w, d.Stylesheet(s.fontResolver(r.Context())))
		return
	}
	e := s.publishedDesign(r.Context(), th)
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
	Theme       *theme.Theme
	Design      *customize.Design // the draft (or published) design being edited
	Colors      []colorRow
	State       string // JSON for customizer.js: design, defaults, options
	HasDraft    bool
	DraftAt     *time.Time
	PublishedAt *time.Time
	PreviewURL  string
	Revisions   []store.CustomRevision
	Fonts       []store.Media     // WOFF2 files in the media library
	Images      []store.Media     // images for the logo
	Langs       []LangLink        // extra languages for the footer text
	Options     map[string]string // option values in effect in the draft
	Active      bool              // the theme is the site's active theme
}

// colorRow is one colour token in the customiser form.
type colorRow struct {
	Key, Label        string
	Light, Dark       string // the design's values, "" for the theme's
	DefLight, DefDark string // the theme's values for the current options
}

func (s *Server) colorLabels() map[string]string {
	return map[string]string{
		"background": s.t("Background"), "foreground": s.t("Text"),
		"card": s.t("Cards"), "card-foreground": s.t("Text on cards"),
		"muted": s.t("Muted surfaces"), "muted-foreground": s.t("Secondary text"),
		"border":  s.t("Borders"),
		"primary": s.t("Primary"), "primary-foreground": s.t("Text on primary"),
		"accent": s.t("Accent"), "accent-foreground": s.t("Text on accent"),
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
	d, row, err := s.draftDesign(ctx, th)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	stored, err := s.store.ThemeOptions(ctx, th.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pub := customize.Load(row.Published, s.designContext(ctx, th))
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
	if fonts, _, err := s.store.ListMedia(ctx, store.MediaQuery{MIME: "font/woff2", Limit: 100}); err == nil {
		data.Fonts = fonts
	}
	if imgs, _, err := s.store.ListMedia(ctx, store.MediaQuery{ImagesOnly: true, Limit: 100}); err == nil {
		data.Images = imgs
	}
	st := s.customizerState(th, d, pub, stored)
	b, _ := json.Marshal(st)
	data.State = string(b)
	labels := s.colorLabels()
	for _, k := range customize.ColorTokens {
		data.Colors = append(data.Colors, colorRow{Key: k, Label: labels[k], Light: d.Light[k], Dark: d.Dark[k],
			DefLight: st.Light[k], DefDark: st.Dark[k]})
	}
	// The live preview is an iframe of this site.
	w.Header().Set("Content-Security-Policy", setCSPDirective(w.Header().Get("Content-Security-Policy"), "frame-src", "'self'"))
	s.renderAdmin(w, r, "customize", s.t("Customise %s", th.Name), "customize", data)
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
func (s *Server) parseDesign(r *http.Request, th *theme.Theme) (*customize.Design, error) {
	d, _, err := s.parseDesignFile(r, th)
	return d, err
}

// parseDesignFile is parseDesign that also returns the "layout" of an
// export file, unvalidated (see parseImportLayout).
func (s *Server) parseDesignFile(r *http.Request, th *theme.Theme) (*customize.Design, json.RawMessage, error) {
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
			return nil, nil, customize.Errors{{Message: s.t("Choose a JSON file to import.")}}
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
	}
	d, err := customize.Parse(raw, s.designContext(r.Context(), th))
	return d, layout, err
}

func (s *Server) adminCustomizeDraft(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, draftResult{Message: s.t("Not found.")})
		return
	}
	d, err := s.parseDesign(r, th)
	if err != nil {
		var errs customize.Errors
		if errors.As(err, &errs) {
			writeJSON(w, http.StatusUnprocessableEntity, draftResult{Errors: errs, Message: s.t("Some values were not accepted."), Warnings: []customize.Warning{}})
			return
		}
		writeJSON(w, http.StatusBadRequest, draftResult{Message: s.t("The request could not be read.")})
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.log.Error("save theme draft", "theme", th.Slug, "err", err)
		writeJSON(w, http.StatusInternalServerError, draftResult{Message: s.t("Something went wrong. The error has been logged.")})
		return
	}
	stored, _ := s.store.ThemeOptions(r.Context(), th.Slug)
	st := s.customizerState(th, d, d, stored)
	css := d.Stylesheet(s.fontResolver(r.Context()))
	writeJSON(w, http.StatusOK, draftResult{OK: true, CSSURL: customCSSURL(th.Slug, "draft-", css), Classes: d.HTMLClasses(),
		Warnings: st.Warnings, SavedAt: time.Now().In(s.loc()).Format("15:04:05"), Light: st.Light, Dark: st.Dark})
}

// adminCustomizeDerive suggests dark colours from the design's light ones
// (and the theme's light defaults for whatever the design leaves unset).
func (s *Server) adminCustomizeDerive(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		writeJSON(w, http.StatusNotFound, draftResult{Message: s.t("Not found.")})
		return
	}
	d, err := s.parseDesign(r, th)
	if err != nil {
		var errs customize.Errors
		if errors.As(err, &errs) {
			writeJSON(w, http.StatusUnprocessableEntity, draftResult{Errors: errs, Message: s.t("Some values were not accepted."), Warnings: []customize.Warning{}})
			return
		}
		writeJSON(w, http.StatusBadRequest, draftResult{Message: s.t("The request could not be read.")})
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
	ctx := r.Context()
	d, row, err := s.draftDesign(ctx, th)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if row.Draft == nil {
		s.customizeDone(w, r, th, "info", s.t("There are no unpublished changes."))
		return
	}
	// Theme options live in the theme's stored options, which the options
	// page edits too: publishing writes the draft's overrides there.
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
		s.serverError(w, r, err)
		return
	}
	s.custom.invalidate()
	s.contentChanged()
	s.audit(r, uid(r), "theme.design_published", th.Slug, nil)
	s.customizeDone(w, r, th, "success", s.t("Published. Visitors now see the new design."))
}

func (s *Server) adminCustomizeDiscard(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	if err := s.store.DiscardCustomDraft(r.Context(), th.Slug, store.CustomDesign); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.design_discarded", th.Slug, nil)
	s.customizeDone(w, r, th, "success", s.t("Draft discarded."))
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
	s.customizeDone(w, r, th, "success", s.t("The draft now uses the theme's defaults. Publish to apply them."))
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
	d, _, err := s.draftDesign(ctx, th)
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
	exp.Layout = s.layoutExport(ctx, th)
	b, err := json.MarshalIndent(exp, "", "  ")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="sap13ns-%s-design.json"`, th.Slug))
	w.Write(b)
}

// adminCustomizeImport validates an exported file and makes it the draft.
func (s *Server) adminCustomizeImport(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	d, rawLayout, err := s.parseDesignFile(r, th)
	if err != nil {
		var errs customize.Errors
		msg := s.t("The file could not be read.")
		if errors.As(err, &errs) {
			msg = s.t("The file was not imported: %s", errs.Error())
		}
		s.customizeFail(w, r, th, http.StatusUnprocessableEntity, msg)
		return
	}
	// Validate the layouts too before changing anything.
	l, err := s.parseImportLayout(r, th, rawLayout)
	if err != nil {
		s.customizeFail(w, r, th, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	if l != nil {
		if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomLayout, l.JSON(), uid(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.audit(r, uid(r), "theme.design_imported", th.Slug, nil)
	s.customizeDone(w, r, th, "success", s.t("Imported into the draft. Check the preview, then publish."))
}

func (s *Server) adminCustomizeRestore(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	rev, err := s.store.CustomRevision(r.Context(), th.Slug, store.CustomDesign, id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := customize.Load(rev.Data, s.designContext(r.Context(), th))
	if err := s.store.SaveCustomDraft(r.Context(), th.Slug, store.CustomDesign, d.JSON(), uid(r)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.design_restored", th.Slug, map[string]string{"revision": strconv.FormatInt(id, 10)})
	s.customizeDone(w, r, th, "success", s.t("Version restored into the draft. Publish to apply it."))
}
