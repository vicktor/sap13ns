package web

import (
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/auth"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// --------------------------------------------------------------- plugins --

type pluginPerm struct {
	Name    string
	Risk    string
	Granted bool
}

type pluginsData struct {
	Plugins []*plugin.Plugin
	Plugin  *plugin.Plugin
	Perms   []pluginPerm
	Values  map[string]string
	Logs    []plugin.LogLine
	Dir     string
}

func (s *Server) adminPlugins(w http.ResponseWriter, r *http.Request) {
	s.renderAdmin(w, r, "plugins", s.t("Plugins"), "plugins", pluginsData{Plugins: s.plugins.List(), Dir: s.cfg.PluginsDir})
}

func (s *Server) adminPluginsReload(w http.ResponseWriter, r *http.Request) {
	if err := s.plugins.Load(r.Context()); err != nil {
		s.redirect(w, r, "/admin/plugins", "error", err.Error())
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugins.reloaded", "", nil)
	s.redirect(w, r, "/admin/plugins", "success", s.t("Plugin directory scanned again."))
}

func (s *Server) pluginsChanged() {
	s.contentChanged()
	s.content.Invalidate()
}

func (s *Server) loadPlugin(w http.ResponseWriter, r *http.Request) (*plugin.Plugin, bool) {
	p := s.plugins.Get(r.PathValue("id"))
	if p == nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return p, true
}

func (s *Server) adminPlugin(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	var granted []string
	if p.Record != nil {
		granted = p.Record.GrantedPermissions
	}
	var perms []pluginPerm
	for _, perm := range p.Permissions {
		perms = append(perms, pluginPerm{Name: perm, Risk: plugin.PermissionRisk(perm), Granted: slices.Contains(granted, perm)})
	}
	values := p.SettingValues()
	for _, sd := range p.Settings {
		if sd.Type == "secret" && values[sd.Key] != "" {
			values[sd.Key] = "" // never echo secrets back to the browser
		}
	}
	title := p.Name
	if title == "" {
		title = p.ID
	}
	s.renderAdmin(w, r, "plugin", title, "plugins", pluginsData{Plugin: p, Perms: perms, Values: values, Logs: p.Logs()})
}

func (s *Server) adminPluginApprove(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	// The administrator confirms the exact build they reviewed.
	if r.FormValue("sha256") != p.SHA256 {
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin changed on disk while you were reviewing it. Review it again."))
		return
	}
	if err := s.plugins.Approve(r.Context(), p.ID, uid(r)); err != nil {
		s.redirect(w, r, "/admin/plugins/"+p.ID, "error", s.t("The plugin could not be enabled: %s", err.Error()))
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.approved", p.ID, map[string]string{"sha256": p.SHA256, "permissions": strings.Join(p.Permissions, " "), "version": p.Version})
	s.redirect(w, r, "/admin/plugins/"+p.ID, "success", s.t("Plugin enabled."))
}

func (s *Server) adminPluginDisable(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	if err := s.plugins.Disable(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.disabled", p.ID, nil)
	s.redirect(w, r, "/admin/plugins/"+p.ID, "success", s.t("Plugin disabled."))
}

func (s *Server) adminPluginSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return
	}
	values := map[string]string{}
	for _, sd := range p.Settings {
		v := strings.TrimSpace(r.FormValue("set_" + sd.Key))
		switch {
		case sd.Type == "bool":
			v = fmt.Sprint(v == "true")
		case sd.Type == "secret" && v == "":
			continue // keep the stored secret
		}
		values[sd.Key] = v
	}
	if err := s.plugins.SaveSettings(r.Context(), p.ID, values); err != nil {
		s.redirect(w, r, "/admin/plugins/"+p.ID+"#settings", "error", err.Error())
		return
	}
	s.pluginsChanged()
	s.audit(r, uid(r), "plugin.settings_saved", p.ID, nil)
	s.redirect(w, r, "/admin/plugins/"+p.ID+"#settings", "success", s.t("Saved."))
}

// -------------------------------------------------------------- settings --

type settingsData struct {
	S         store.Settings
	Pages     []store.Post
	Languages []LangLink // every language that can be chosen
	Extra     []LangLink // the extra content languages now enabled
	Zones     []string
	Roles     []string
	OGLayouts []LangLink // Code/Name pairs for the social image select
	Error     string
}

var commonZones = []string{"UTC", "Europe/Madrid", "Atlantic/Canary", "Europe/Lisbon", "Europe/London", "Europe/Paris", "Europe/Berlin",
	"Europe/Rome", "America/Mexico_City", "America/Bogota", "America/Lima", "America/Santiago", "America/Argentina/Buenos_Aires",
	"America/Caracas", "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "Asia/Tokyo", "Australia/Sydney"}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, st store.Settings, msg string) {
	pages, _, err := s.store.ListPosts(r.Context(), store.PostQuery{Type: store.TypePage, PublicOnly: true, Order: "title", Limit: 500})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	zones := commonZones
	if !slices.Contains(zones, st.Timezone) {
		zones = append([]string{st.Timezone}, zones...)
	}
	s.renderAdminStatus(w, r, status, "settings", s.t("Settings"), "settings", settingsData{S: st, Pages: pages, Languages: languageOptions(), Extra: s.extraLangsOf(st),
		OGLayouts: []LangLink{{Code: "classic", Name: s.t("Classic")}, {Code: "bold", Name: s.t("Bold")}, {Code: "minimal", Name: s.t("Minimal")}, {Code: "off", Name: s.t("Off")}},
		Zones:     zones, Roles: []string{auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor}, Error: msg})
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, http.StatusOK, s.settings(), "")
}

func (s *Server) adminSettingsSave(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	on := func(k string) bool { return r.FormValue(k) == "1" }
	st.SiteTitle, st.Tagline, st.FooterText = trimmed(r, "site_title"), trimmed(r, "tagline"), trimmed(r, "footer_text")
	oldMain := st.Language
	st.Language, st.Timezone = r.FormValue("language"), r.FormValue("timezone")
	st.Languages = nil
	for _, l := range r.Form["languages"] {
		if validLang(l) && l != st.Language && !slices.Contains(st.Languages, l) {
			st.Languages = append(st.Languages, l)
		}
	}
	// Content in the previous main language stays reachable under its prefix.
	if oldMain != st.Language && validLang(oldMain) && !slices.Contains(st.Languages, oldMain) {
		st.Languages = append(st.Languages, oldMain)
	}
	if len(st.Languages) > 10 {
		st.Languages = st.Languages[:10]
	}
	st.SiteText = map[string]store.SiteText{}
	for _, l := range st.Languages {
		t := store.SiteText{SiteTitle: trimmed(r, "text_"+l+"_title"), Tagline: trimmed(r, "text_"+l+"_tagline"),
			FooterText: trimmed(r, "text_"+l+"_footer")}
		if tooLong(t.SiteTitle, 120) || tooLong(t.Tagline, 250) || tooLong(t.FooterText, 1000) {
			s.renderSettings(w, r, http.StatusUnprocessableEntity, st, s.t("The text is too long."))
			return
		}
		if t != (store.SiteText{}) {
			st.SiteText[l] = t
		}
	}
	st.PostsPerPage = formInt(r, "posts_per_page", 10)
	st.FrontPage = r.FormValue("front_page")
	st.FrontPageID = 0
	if id := formInt64Ptr(r, "front_page_id"); id != nil {
		st.FrontPageID = *id
	}
	st.DiscourageSearch = on("discourage_search")
	st.CommentsEnabled, st.CommentsRequireLogin = on("comments_enabled"), on("comments_require_login")
	st.CommentModeration = r.FormValue("comment_moderation")
	st.CommentsCloseDays = formInt(r, "comments_close_days", 0)
	st.AllowRegistration, st.DefaultRole = on("allow_registration"), r.FormValue("default_role")
	st.FeedItems, st.FeedFullText, st.WebSubHub = formInt(r, "feed_items", 20), on("feed_full_text"), trimmed(r, "websub_hub")
	st.TwoFactor = r.FormValue("two_factor")
	st.OGCards, st.OGAccent = r.FormValue("og_cards"), strings.TrimSpace(r.FormValue("og_accent"))
	st.AIAccess, st.AnalyticsOff = r.FormValue("ai_access"), !on("analytics")
	st.SessionDays, st.IdleHours = formInt(r, "session_days", 14), formInt(r, "idle_hours", 48)

	fail := func(msg string) { s.renderSettings(w, r, http.StatusUnprocessableEntity, st, msg) }
	switch {
	case st.SiteTitle == "" || tooLong(st.SiteTitle, 120):
		fail(s.t("The site title is required (120 characters max)."))
	case tooLong(st.Tagline, 250) || tooLong(st.FooterText, 1000):
		fail(s.t("The text is too long."))
	case !validLang(st.Language):
		fail(s.t("Invalid request."))
	case !validZone(st.Timezone):
		fail(s.t("Unknown time zone."))
	case st.PostsPerPage < 1 || st.PostsPerPage > 100:
		fail(s.t("Posts per page must be between 1 and 100."))
	case st.FrontPage != "posts" && st.FrontPage != "page":
		fail(s.t("Invalid request."))
	case st.FrontPage == "page" && !s.publicPage(r, st.FrontPageID):
		fail(s.t("Choose a published page for the front page."))
	case !slices.Contains([]string{"all", "returning", "none"}, st.CommentModeration):
		fail(s.t("Invalid request."))
	case st.CommentsCloseDays < 0 || st.CommentsCloseDays > 3650:
		fail(s.t("Invalid number of days."))
	case !slices.Contains([]string{auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor}, st.DefaultRole):
		fail(s.t("Invalid request."))
	case st.FeedItems < 1 || st.FeedItems > 100:
		fail(s.t("Feed items must be between 1 and 100."))
	case st.WebSubHub != "" && (!strings.HasPrefix(st.WebSubHub, "https://") || !validHTTPURL(st.WebSubHub)):
		fail(s.t("The WebSub hub must be an https:// address."))
	case !slices.Contains([]string{"staff", "editors"}, st.TwoFactor):
		fail(s.t("Invalid request."))
	case !slices.Contains([]string{"classic", "bold", "minimal", "off"}, st.OGCards):
		fail(s.t("Invalid request."))
	case st.OGAccent != "" && !hexColorRe.MatchString(st.OGAccent):
		fail(s.t("Use a colour like #3f068b."))
	case !slices.Contains([]string{"open", "no-training", "block"}, st.AIAccess):
		fail(s.t("Invalid request."))
	case st.SessionDays < 1 || st.SessionDays > 90:
		fail(s.t("Session length must be between 1 and 90 days."))
	case st.IdleHours < 1 || st.IdleHours > 24*90:
		fail(s.t("Idle timeout must be between 1 and 2160 hours."))
	default:
		if err := s.saveSettings(r, st); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, uid(r), "settings.saved", "", nil)
		s.redirect(w, r, "/admin/settings", "success", s.t("Settings saved."))
	}
}

func validZone(z string) bool {
	if z == "" || len(z) > 64 {
		return false
	}
	_, err := time.LoadLocation(z)
	return err == nil
}

func (s *Server) publicPage(r *http.Request, id int64) bool {
	if id <= 0 {
		return false
	}
	p, err := s.store.PostByID(r.Context(), id)
	return err == nil && p.Type == store.TypePage && p.IsPublic(time.Now())
}

// ----------------------------------------------------------------- audit --

type auditData struct {
	Entries []store.AuditEntry
	Total   int
	Page    *Pagination
	Action  string
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.URL.Query().Get("action"))
	if len(action) > 60 {
		action = ""
	}
	page := listPage(r)
	const per = 50
	entries, total, err := s.store.ListAudit(r.Context(), action, per, (page-1)*per)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	vals := r.URL.Query()
	vals.Del("page")
	s.renderAdmin(w, r, "audit", s.t("Audit log"), "audit", auditData{Entries: entries, Total: total, Action: action,
		Page: newPagination("/admin/audit", vals, page, per, total)})
}

// ---------------------------------------------------------------- system --

type sysRow struct {
	Label string
	Value string
	OK    *bool
}

type systemData struct {
	Groups []sysGroup
}

type sysGroup struct {
	Title string
	Rows  []sysRow
}

var startedAt = time.Now()

func (s *Server) adminSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	yes, no := true, false
	flag := func(b bool) *bool {
		if b {
			return &yes
		}
		return &no
	}
	var pgVersion string
	s.store.DB.QueryRow(ctx, `SHOW server_version`).Scan(&pgVersion)
	var dbSize string
	s.store.DB.QueryRow(ctx, `SELECT pg_size_pretty(pg_database_size(current_database()))`).Scan(&dbSize)
	conns := "—"
	if sp, ok := s.store.DB.(interface{ Stat() *pgxpool.Stat }); ok {
		ps := sp.Stat()
		conns = fmt.Sprintf("%d / %d (%d idle)", ps.TotalConns(), ps.MaxConns(), ps.IdleConns())
	}
	pagesN, pagesB := s.pages.Stats()
	bodiesN, bodiesB := s.content.Stats()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	enabled := 0
	for _, p := range s.plugins.List() {
		if p.Status == plugin.StatusEnabled {
			enabled++
		}
	}
	https := s.cfg.BaseURL.Scheme == "https"
	d := systemData{Groups: []sysGroup{
		{s.t("Application"), []sysRow{
			{Label: s.t("Version"), Value: Version},
			{Label: "Go", Value: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH},
			{Label: s.t("Uptime"), Value: time.Since(startedAt).Round(time.Second).String()},
			{Label: s.t("Memory in use"), Value: filesize(int64(mem.HeapAlloc))},
			{Label: "Goroutines", Value: fmt.Sprint(runtime.NumGoroutine())},
			{Label: s.t("Development mode"), Value: fmt.Sprint(s.cfg.Dev), OK: flag(!s.cfg.Dev)},
		}},
		{s.t("Security"), []sysRow{
			{Label: s.t("Public URL"), Value: s.cfg.BaseURL.String(), OK: flag(https)},
			{Label: s.t("Secure cookies"), Value: fmt.Sprint(s.cfg.CookieSecure), OK: flag(s.cfg.CookieSecure)},
			{Label: "HSTS", Value: fmt.Sprint(s.cfg.HSTS), OK: flag(s.cfg.HSTS || !https)},
			{Label: s.t("Two-factor authentication"), Value: s.t("Required"), OK: flag(true)},
			{Label: s.t("E-mail delivery"), Value: s.mailDescription(), OK: flag(s.mailer.Enabled())},
			{Label: s.t("Trusted proxies"), Value: fmt.Sprint(s.cfg.TrustedProxies)},
		}},
		{s.t("Database"), []sysRow{
			{Label: "PostgreSQL", Value: pgVersion},
			{Label: s.t("Database size"), Value: dbSize},
			{Label: s.t("Connections"), Value: conns},
		}},
		{s.t("Caches"), []sysRow{
			{Label: s.t("Rendered pages"), Value: fmt.Sprintf("%d · %s", pagesN, filesize(int64(pagesB)))},
			{Label: s.t("Rendered posts"), Value: fmt.Sprintf("%d · %s", bodiesN, filesize(int64(bodiesB)))},
		}},
		{s.t("Storage"), []sysRow{
			{Label: s.t("Uploads"), Value: s.cfg.UploadsDir},
			{Label: s.t("Plugins"), Value: fmt.Sprintf("%s (%d enabled)", s.cfg.PluginsDir, enabled)},
			{Label: s.t("Extra themes"), Value: s.cfg.ThemesDir},
			{Label: s.t("Maximum upload"), Value: filesize(s.cfg.MaxUploadBytes)},
		}},
	}}
	s.renderAdmin(w, r, "system", s.t("System"), "system", d)
}

func (s *Server) mailDescription() string {
	if !s.mailer.Enabled() {
		return s.t("Not configured")
	}
	return fmt.Sprintf("%s:%d", s.cfg.SMTP.Host, s.cfg.SMTP.Port)
}
