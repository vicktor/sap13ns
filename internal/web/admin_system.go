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
	// SignInURL is the saved sign-in address, in full; Recovery the
	// command that prints and resets it on the server, "" on a site of a
	// network, whose members sign in through the portal anyway (Tenant).
	SignInURL string
	Recovery  string
	Tenant    bool
	Moved     *signInMove // the sign-in address just changed
}

// signInMove is the notice of a new sign-in address.
type signInMove struct {
	URL string // the new address, in full
	Old string // the old one, a path
}

var commonZones = []string{"UTC", "Europe/Madrid", "Atlantic/Canary", "Europe/Lisbon", "Europe/London", "Europe/Paris", "Europe/Berlin",
	"Europe/Rome", "America/Mexico_City", "America/Bogota", "America/Lima", "America/Santiago", "America/Argentina/Buenos_Aires",
	"America/Caracas", "America/New_York", "America/Chicago", "America/Denver", "America/Los_Angeles", "Asia/Tokyo", "Australia/Sydney"}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, status int, st store.Settings, msg string) {
	s.renderSettingsPage(w, r, status, st, msg, nil)
}

func (s *Server) renderSettingsPage(w http.ResponseWriter, r *http.Request, status int, st store.Settings, msg string, moved *signInMove) {
	pages, _, err := s.store.ListPosts(r.Context(), store.PostQuery{Type: store.TypePage, PublicOnly: true, Order: "title", Limit: 500})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	zones := commonZones
	if !slices.Contains(zones, st.Timezone) {
		zones = append([]string{st.Timezone}, zones...)
	}
	s.renderAdminStatus(w, r, status, "settings", s.tu(r, "Settings"), "settings", settingsData{S: st, Pages: pages, Languages: languageOptions(), Extra: s.extraLangsOf(st),
		OGLayouts: []LangLink{{Code: "classic", Name: s.tu(r, "Classic")}, {Code: "bold", Name: s.tu(r, "Bold")}, {Code: "minimal", Name: s.tu(r, "Minimal")}, {Code: "off", Name: s.tu(r, "Off")}},
		Zones:     zones, Roles: []string{auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor}, Error: msg, Portal: s.portal != nil,
		SignInURL: s.SignInURL(), Recovery: s.signInRecovery(), Tenant: s.sso != nil, Moved: moved})
}

// signInRecovery is the command that prints and resets the sign-in
// address on the server: with -site on a network (network for the
// portal), "" on a tenant, whose administrators have no shell there.
func (s *Server) signInRecovery() string {
	switch {
	case s.sso != nil:
		return ""
	case s.backupHooks != nil && s.portal != nil:
		return "sap13ns settings login-path -site network"
	case s.backupHooks != nil:
		return "sap13ns settings login-path -site " + s.backupHooks.BackupIdentity().Label
	}
	return "sap13ns settings login-path"
}

// formLoginPath reads the sign-in address of the settings form, forgiving
// a missing or a trailing slash and capitals: empty is the default.
func formLoginPath(r *http.Request) string {
	p := strings.Trim(strings.ToLower(trimmed(r, "login_path")), "/")
	if p == "" {
		return sitecheck.DefaultLoginPath
	}
	return "/" + p
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
	st.VideoEmbeds = r.FormValue("video_embeds")
	st.SessionDays, st.IdleHours = formInt(r, "session_days", 14), formInt(r, "idle_hours", 48)
	// Commenters' Gravatars need the users' setting, which sends anything
	// to Gravatar at all (sitecheck).
	st.GravatarUsers = on("gravatar_users")
	st.GravatarCommenters = st.GravatarUsers && on("gravatar_commenters")
	before := s.settings()
	st.LoginPath, st.HideAdmin = formLoginPath(r), on("hide_admin")

	// The rules are shared with restores (sitecheck). The active theme is
	// not on this page: an uninstalled one must not block it.
	if bad := sitecheck.Settings(st, sitecheck.Site{PublicPage: func(id int64) bool { return s.publicPage(r, id) },
		SignIn: s.signInCheck(r.Context())}); len(bad) > 0 {
		s.renderSettings(w, r, http.StatusUnprocessableEntity, st, s.settingsError(r, bad[0], st))
		return
	}
	old := s.signInBase()
	if err := s.saveSettings(r, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.gravatarSettingsChanged(r.Context(), before, st) // avatars.go
	s.audit(r, uid(r), "settings.saved", "", nil)
	if st.LoginPath != old {
		// Shown on the page, not in a toast that fades: the old address
		// stops working now.
		s.audit(r, uid(r), "settings.login_path", st.LoginPath, map[string]string{"from": old})
		s.renderSettingsPage(w, r, http.StatusOK, s.settings(), "", &signInMove{URL: s.SignInURL(), Old: old})
		return
	}
	s.redirect(w, r, "/admin/settings", "success", s.tu(r, "Settings saved."))
}

// settingsError explains why the setting field (sitecheck.Settings) was
// refused.
func (s *Server) settingsError(r *http.Request, field string, st store.Settings) string {
	switch field {
	case "site_title":
		return s.tu(r, "The site title is required (120 characters max).")
	case "tagline", "footer_text", "site_text":
		return s.tu(r, "The text is too long.")
	case "timezone":
		return s.tu(r, "Unknown time zone.")
	case "posts_per_page":
		return s.tu(r, "Posts per page must be between 1 and 100.")
	case "front_page":
		if st.FrontPage == "page" {
			return s.tu(r, "Choose a published page for the front page.")
		}
	case "comments_close_days":
		return s.tu(r, "Invalid number of days.")
	case "feed_items":
		return s.tu(r, "Feed items must be between 1 and 100.")
	case "websub_hub":
		return s.tu(r, "The WebSub hub must be an https:// address.")
	case "og_accent":
		return s.tu(r, "Use a colour like #3f068b.")
	case "session_days":
		return s.tu(r, "Session length must be between 1 and 90 days.")
	case "idle_hours":
		return s.tu(r, "Idle timeout must be between 1 and 2160 hours.")
	case "login_path":
		if !sitecheck.LoginPath.MatchString(st.LoginPath) {
			return s.tu(r, "The sign-in address must be one or two words of lowercase letters, numbers and dashes, of 3 to 41 characters each, such as /entrar.")
		}
		return s.tu(r, "%s is taken: the site uses it for one of its own addresses, a page or a redirect. Choose another sign-in address.", st.LoginPath)
	case "video_embeds":
		return s.tu(r, "Choose how video links are shown.")
	}
	return s.tu(r, "Invalid request.")
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
	s.renderAdmin(w, r, "audit", s.tu(r, "Audit log"), "audit", auditData{Entries: entries, Total: total, Action: action,
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
	// A single site: how a network of sites is started instead.
	SingleSite bool
	Guide      string
}

// multisiteGuide documents networks of sites (docs/MULTISITE.md).
const multisiteGuide = "https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md"

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
		{s.tu(r, "Application"), []sysRow{
			{Label: s.tu(r, "Version"), Value: Version},
			{Label: "Go", Value: runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH},
			{Label: s.tu(r, "Uptime"), Value: time.Since(startedAt).Round(time.Second).String()},
			{Label: s.tu(r, "Memory in use"), Value: filesize(int64(mem.HeapAlloc))},
			{Label: "Goroutines", Value: fmt.Sprint(runtime.NumGoroutine())},
			{Label: s.tu(r, "Development mode"), Value: fmt.Sprint(s.cfg.Dev), OK: flag(!s.cfg.Dev)},
		}},
		{s.tu(r, "Security"), []sysRow{
			{Label: s.tu(r, "Public URL"), Value: s.cfg.BaseURL.String(), OK: flag(https)},
			{Label: s.tu(r, "Secure cookies"), Value: fmt.Sprint(s.cfg.CookieSecure), OK: flag(s.cfg.CookieSecure)},
			{Label: "HSTS", Value: fmt.Sprint(s.cfg.HSTS), OK: flag(s.cfg.HSTS || !https)},
			{Label: s.tu(r, "Two-factor authentication"), Value: s.twoFactorDescription(r), OK: flag(!(s.cfg.Dev && s.cfg.DevSkip2FA))},
			{Label: s.tu(r, "E-mail delivery"), Value: s.mailDescription(r), OK: flag(s.mailer.Enabled())},
			{Label: s.tu(r, "Trusted proxies"), Value: fmt.Sprint(s.cfg.TrustedProxies)},
		}},
		{s.tu(r, "Database"), []sysRow{
			{Label: "PostgreSQL", Value: pgVersion},
			{Label: s.tu(r, "Database size"), Value: dbSize},
			{Label: s.tu(r, "Connections"), Value: conns},
		}},
		{s.tu(r, "Caches"), []sysRow{
			{Label: s.tu(r, "Rendered pages"), Value: fmt.Sprintf("%d · %s", pagesN, filesize(int64(pagesB)))},
			{Label: s.tu(r, "Rendered posts"), Value: fmt.Sprintf("%d · %s", bodiesN, filesize(int64(bodiesB)))},
		}},
		{s.tu(r, "Storage"), []sysRow{
			{Label: s.tu(r, "Uploads"), Value: s.cfg.UploadsDir},
			{Label: s.tu(r, "Plugins"), Value: s.tu(r, "%s (%d enabled)", s.cfg.PluginsDir, enabled)},
			{Label: s.tu(r, "Extra themes"), Value: s.cfg.ThemesDir},
			{Label: s.tu(r, "Maximum upload"), Value: filesize(s.cfg.MaxUploadBytes)},
		}},
	}}
	d.Updates, d.Checks = s.updateRows(r), s.updates != nil && !s.updates.Disabled
	d.SingleSite, d.Guide = s.network == nil, multisiteGuide
	s.renderAdmin(w, r, "system", s.tu(r, "System"), "system", d)
}

func (s *Server) twoFactorDescription(r *http.Request) string {
	switch {
	case s.cfg.Dev && s.cfg.DevSkip2FA:
		return s.tu(r, "Skipped (development mode)")
	case s.portal != nil:
		return s.tu(r, "Required for every account (network portal)")
	case s.settings().TwoFactor == "editors":
		return s.tu(r, "Required for administrators and editors")
	}
	return s.tu(r, "Required for all staff")
}

func (s *Server) mailDescription(r *http.Request) string {
	if !s.mailer.Enabled() {
		return s.tu(r, "Not configured")
	}
	return fmt.Sprintf("%s:%d", s.cfg.SMTP.Host, s.cfg.SMTP.Port)
}
