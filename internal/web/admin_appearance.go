package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// ---------------------------------------------------------------- themes --

type themesData struct {
	Themes   []*theme.Theme
	Active   string
	Uploads  bool   // the site can upload themes (admin_theme_upload.go)
	Plan     string // the plan's name, on a site of a network
	Tenant   bool   // a site of a network
	MaxMB    int
	Refused  *themeRefusal
	Failures []theme.Failure // themes of the site's directory that did not load
}

func (s *Server) adminThemes(w http.ResponseWriter, r *http.Request) {
	// Themes copied into the themes directory appear without a restart.
	if err := s.themes.Load(); err != nil {
		s.log.Error("reload themes", "err", err)
	}
	d, err := s.themesPage(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "themes", s.t("Themes"), "themes", d)
}

func (s *Server) adminThemeActivate(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	st := s.settings()
	st.ActiveTheme = th.Slug
	// A theme with its own front page (a landing, a one-page site) shows it
	// at / instead of the latest posts, which move to /blog. A static front
	// page the owner chose stays.
	front := st.FrontPage == FrontPosts && th.Has("front")
	if front {
		st.FrontPage = FrontTheme
	}
	if err := s.saveSettings(r, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.activated", th.Slug, nil)
	msg := s.t("“%s” is now the active theme.", th.Name)
	if front {
		msg += " " + s.t("Its front page now shows at / and the posts at /blog; Settings → Reading changes that.")
	}
	s.redirect(w, r, "/admin/appearance/themes", "success", msg)
}

// saveSettings stores settings and refreshes the in-memory snapshot.
func (s *Server) saveSettings(r *http.Request, st store.Settings) error {
	if err := s.store.SaveSettings(r.Context(), st); err != nil {
		return err
	}
	if err := s.ReloadSettings(r.Context()); err != nil {
		return err
	}
	s.contentChanged()
	s.content.Invalidate()
	return nil
}

type themeOptionsData struct {
	Theme  *theme.Theme
	Values map[string]string
}

func (s *Server) loadTheme(w http.ResponseWriter, r *http.Request) (*theme.Theme, bool) {
	th, ok := s.themes.Get(r.PathValue("slug"))
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
	}
	return th, ok
}

func (s *Server) adminThemeOptions(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	stored, err := s.store.ThemeOptions(r.Context(), th.Slug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "theme_options", th.Name, "themes", themeOptionsData{Theme: th, Values: th.OptionValues(stored)})
}

func (s *Server) adminThemeOptionsSave(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok {
		return
	}
	opts := map[string]string{}
	for _, o := range th.Options {
		v := strings.TrimSpace(r.FormValue("opt_" + o.Key))
		if o.Type == "bool" {
			v = strconv.FormatBool(v == "true")
		}
		if tooLong(v, 2000) {
			s.redirect(w, r, r.URL.Path, "error", s.t("“%s” is too long.", s.themeText(th, o.Label)))
			return
		}
		opts[o.Key] = v
	}
	if err := s.store.SaveThemeOptions(r.Context(), th.Slug, th.OptionValues(opts)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.options_saved", th.Slug, nil)
	s.contentChanged()
	s.redirect(w, r, r.URL.Path, "success", s.t("Saved."))
}

// ----------------------------------------------------------------- menus --

type menusData struct {
	Theme      *theme.Theme
	Menus      map[string][]store.MenuItem // of the selected language scope
	Pages      []store.Post
	Categories []store.Term
	Lang       string     // "" = shared menus, else a language's own menus
	Langs      []LangLink // extra languages, when multilingual
}

// menuScope reads the ?lang= (or form) scope of the menu editor: "" for the
// shared menus or an extra language that has its own.
func (s *Server) menuScope(r *http.Request) string {
	if l := r.FormValue("lang"); s.isExtraLang(l) {
		return l
	}
	return ""
}

func menusPath(lang string) string { return withQuery("/admin/appearance/menus", "lang", lang) }

func (s *Server) adminMenus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	scope := s.menuScope(r)
	all, err := s.store.AllMenus(ctx)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	th := s.themes.Active(s.settings().ActiveTheme)
	menus := map[string][]store.MenuItem{}
	for _, loc := range []string{"primary", "footer"} {
		menus[loc] = all[store.MenuKey(loc, scope)]
	}
	for _, m := range th.Menus {
		menus[m.ID] = all[store.MenuKey(m.ID, scope)]
	}
	content := scope
	if content == "" {
		content = s.mainLang()
	}
	pages, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePage, Lang: content, PublicOnly: true, Order: "menu", Limit: 200})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	cats, err := s.store.ListTerms(ctx, "category", content)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for i := range cats {
		cats[i] = cats[i].In(content)
	}
	var extra []LangLink
	for _, l := range s.adminLangs() {
		if l.Code != s.mainLang() {
			extra = append(extra, l)
		}
	}
	s.renderAdmin(w, r, "menus", s.t("Menus"), "menus", menusData{Theme: th, Menus: menus, Pages: pages, Categories: cats,
		Lang: scope, Langs: extra})
}

// validMenuURL accepts site-relative paths, fragments and http(s) links.
func validMenuURL(u string) bool {
	if u == "" || len(u) > 1000 || strings.ContainsAny(u, "\r\n\t ") {
		return false
	}
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") || strings.HasPrefix(u, "#") {
		return true
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}

func (s *Server) adminMenuAdd(w http.ResponseWriter, r *http.Request) {
	th := s.themes.Active(s.settings().ActiveTheme)
	loc, scope := r.FormValue("location"), s.menuScope(r)
	back := menusPath(scope)
	if !th.HasMenu(loc) && loc != "primary" && loc != "footer" {
		s.redirect(w, r, back, "error", s.t("Unknown menu location."))
		return
	}
	content := scope
	if content == "" {
		content = s.mainLang()
	}
	ctx := r.Context()
	type item struct{ label, url string }
	var items []item
	switch r.FormValue("source") {
	case "pages":
		for _, id := range formIDs(r, "page_ids") {
			if p, err := s.store.PostByID(ctx, id); err == nil && p.Type == store.TypePage {
				items = append(items, item{p.Title, s.postURL(p)})
			}
		}
	case "categories":
		for _, id := range formIDs(r, "term_ids") {
			if t, err := s.store.TermByID(ctx, id); err == nil {
				lt := t.In(content)
				items = append(items, item{lt.Name, s.langPath(content, termURL(&lt))})
			}
		}
	default:
		label, link := trimmed(r, "label"), trimmed(r, "url")
		if label == "" || tooLong(label, 100) || !validMenuURL(link) {
			s.redirect(w, r, back, "error", s.t("Enter a label and a valid link (/path or https://…)."))
			return
		}
		items = append(items, item{label, link})
	}
	for _, it := range items {
		if err := s.store.AddMenuItem(ctx, loc, scope, it.label, it.url); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.audit(r, uid(r), "menu.items_added", store.MenuKey(loc, scope), map[string]string{"count": strconv.Itoa(len(items))})
	s.contentChanged()
	s.redirect(w, r, back+"#menu-"+loc, "success", s.t("%d items updated.", len(items)))
}

func (s *Server) adminMenuUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	label, link := trimmed(r, "label"), trimmed(r, "url")
	if label == "" || tooLong(label, 100) || !validMenuURL(link) {
		s.redirect(w, r, menusPath(s.menuScope(r)), "error", s.t("Enter a label and a valid link (/path or https://…)."))
		return
	}
	if err := s.store.UpdateMenuItem(r.Context(), id, label, link); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.redirect(w, r, menusPath(s.menuScope(r)), "success", s.t("Saved."))
}

func (s *Server) adminMenuAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	var err error
	switch r.PathValue("action") {
	case "up":
		err = s.store.MoveMenuItem(r.Context(), id, -1)
	case "down":
		err = s.store.MoveMenuItem(r.Context(), id, 1)
	case "delete":
		err = s.store.DeleteMenuItem(r.Context(), id)
	default:
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.redirect(w, r, menusPath(s.menuScope(r)), "", "")
}

// --------------------------------------------------------------- widgets --

type widgetKind struct {
	Kind  string
	Label string
}

type widgetsData struct {
	Theme   *theme.Theme
	Widgets map[string][]store.Widget
	Kinds   []widgetKind
	Labels  map[string]string
	Langs   []LangLink // every site language, when there is more than one

	Fields map[string][]plugin.SettingDecl // config fields of plugin widget kinds
}

func (s *Server) widgetKinds() []widgetKind {
	labels := map[string]string{
		"recent_posts": s.t("Recent posts"), "categories": s.t("Categories"), "tags": s.t("Tags"),
		"archives": s.t("Archives"), "search": s.t("Search"), "feeds": s.t("Subscribe (feeds)"), "text": s.t("Text"),
	}
	var out []widgetKind
	for _, k := range builtinWidgets {
		out = append(out, widgetKind{k, labels[k]})
	}
	var plugged []widgetKind
	for k, label := range s.plugins.WidgetKinds() {
		plugged = append(plugged, widgetKind{k, label})
	}
	sort.Slice(plugged, func(i, j int) bool { return plugged[i].Label < plugged[j].Label })
	return append(out, plugged...)
}

func (s *Server) adminWidgets(w http.ResponseWriter, r *http.Request) {
	widgets, err := s.store.AllWidgets(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	kinds := s.widgetKinds()
	labels := map[string]string{}
	for _, k := range kinds {
		labels[k.Kind] = k.Label
	}
	s.renderAdmin(w, r, "widgets", s.t("Widgets"), "widgets", widgetsData{Theme: s.themes.Active(s.settings().ActiveTheme), Widgets: widgets, Kinds: kinds, Labels: labels, Langs: s.adminLangs(), Fields: s.pluginWidgetFields(kinds)})
}

func (s *Server) validWidgetKind(kind string) bool {
	for _, k := range s.widgetKinds() {
		if k.Kind == kind {
			return true
		}
	}
	return false
}

// widgetConfig reads the per-kind settings from the form.
func widgetConfig(r *http.Request, kind string) map[string]string {
	cfg := map[string]string{}
	switch kind {
	case "recent_posts":
		n := formInt(r, "count", 5)
		if n < 1 || n > 20 {
			n = 5
		}
		cfg["count"] = strconv.Itoa(n)
	case "text":
		t := r.FormValue("text")
		if len(t) > 10000 {
			t = t[:10000]
		}
		cfg["text"] = t
	}
	return cfg
}

// widgetLang is the language a widget is limited to ("" = every language).
func (s *Server) widgetLang(r *http.Request) string {
	if l := r.FormValue("lang"); slices.Contains(s.siteLangs(), l) {
		return l
	}
	return ""
}

func (s *Server) adminWidgetAdd(w http.ResponseWriter, r *http.Request) {
	th := s.themes.Active(s.settings().ActiveTheme)
	area, kind := r.FormValue("area"), r.FormValue("kind")
	if !th.HasWidgetArea(area) || !s.validWidgetKind(kind) {
		s.redirect(w, r, "/admin/appearance/widgets", "error", s.t("Invalid request."))
		return
	}
	title := trimmed(r, "title")
	if tooLong(title, 100) {
		title = truncateStr(title, 100)
	}
	cfg, err := s.widgetConfigFor(r, kind)
	if err != nil {
		s.redirect(w, r, "/admin/appearance/widgets#area-"+area, "error", err.Error())
		return
	}
	if err := s.store.AddWidget(r.Context(), area, s.widgetLang(r), kind, title, cfg); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "widget.added", area+"/"+kind, nil)
	s.contentChanged()
	s.redirect(w, r, "/admin/appearance/widgets#area-"+area, "success", s.t("Widget added."))
}

func (s *Server) adminWidgetUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	title := trimmed(r, "title")
	if tooLong(title, 100) {
		title = truncateStr(title, 100)
	}
	kind := r.FormValue("kind")
	cfg, err := s.widgetConfigFor(r, kind)
	if err != nil {
		s.redirect(w, r, fmt.Sprintf("/admin/appearance/widgets#widget-%d", id), "error", err.Error())
		return
	}
	if err := s.store.UpdateWidget(r.Context(), id, s.widgetLang(r), title, cfg); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.redirect(w, r, fmt.Sprintf("/admin/appearance/widgets#widget-%d", id), "success", s.t("Saved."))
}

func (s *Server) adminWidgetAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	var err error
	switch r.PathValue("action") {
	case "up":
		err = s.store.MoveWidget(r.Context(), id, -1)
	case "down":
		err = s.store.MoveWidget(r.Context(), id, 1)
	case "delete":
		err = s.store.DeleteWidget(r.Context(), id)
	default:
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.redirect(w, r, "/admin/appearance/widgets", "", "")
}
