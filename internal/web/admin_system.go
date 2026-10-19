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
	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

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
	Portal    bool // the network portal: every account needs a second factor
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
		Zones:     zones, Roles: []string{auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor}, Error: msg, Portal: s.portal != nil})
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

	// The rules are shared with restores (sitecheck). The active theme is
	// not on this page: an uninstalled one must not block it.
	if bad := sitecheck.Settings(st, sitecheck.Site{PublicPage: func(id int64) bool { return s.publicPage(r, id) }}); len(bad) > 0 {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, st, s.settingsError(bad[0], st))
		return
	}
	if err := s.saveSettings(r, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "settings.saved", "", nil)
	s.redirect(w, r, "/admin/settings", "success", s.t("Settings saved."))
}

// settingsError explains why the setting field (sitecheck.Settings) was
// refused.
func (s *Server) settingsError(field string, st store.Settings) string {
	switch field {
	case "site_title":
		return s.t("The site title is required (120 characters max).")
	case "tagline", "footer_text", "site_text":
		return s.t("The text is too long.")
	case "timezone":
		return s.t("Unknown time zone.")
	case "posts_per_page":
		return s.t("Posts per page must be between 1 and 100.")
	case "front_page":
		if st.FrontPage == "page" {
			return s.t("Choose a published page for the front page.")
		}
	case "comments_close_days":
		return s.t("Invalid number of days.")
	case "feed_items":
		return s.t("Feed items must be between 1 and 100.")
	case "websub_hub":
		return s.t("The WebSub hub must be an https:// address.")
	case "og_accent":
		return s.t("Use a colour like #3f068b.")
	case "session_days":
		return s.t("Session length must be between 1 and 90 days.")
	case "idle_hours":
		return s.t("Idle timeout must be between 1 and 2160 hours.")
	}
	return s.t("Invalid request.")
}

func validZone(z string) bool { return sitecheck.ValidZone(z) }

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
	Groups  []sysGroup
	Updates []sysRow
	Checks  bool // release checks are on: offer "Check now"
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
			{Label: s.t("Two-factor authentication"), Value: s.twoFactorDescription(), OK: flag(!(s.cfg.Dev && s.cfg.DevSkip2FA))},
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
			{Label: s.t("Plugins"), Value: s.t("%s (%d enabled)", s.cfg.PluginsDir, enabled)},
			{Label: s.t("Extra themes"), Value: s.cfg.ThemesDir},
			{Label: s.t("Maximum upload"), Value: filesize(s.cfg.MaxUploadBytes)},
		}},
	}}
	d.Updates, d.Checks = s.updateRows(), s.updates != nil && !s.updates.Disabled
	s.renderAdmin(w, r, "system", s.t("System"), "system", d)
}

func (s *Server) twoFactorDescription() string {
	switch {
	case s.cfg.Dev && s.cfg.DevSkip2FA:
		return s.t("Skipped (development mode)")
	case s.portal != nil:
		return s.t("Required for every account (network portal)")
	case s.settings().TwoFactor == "editors":
		return s.t("Required for administrators and editors")
	}
	return s.t("Required for all staff")
}

func (s *Server) mailDescription() string {
	if !s.mailer.Enabled() {
		return s.t("Not configured")
	}
	return fmt.Sprintf("%s:%d", s.cfg.SMTP.Host, s.cfg.SMTP.Port)
}
