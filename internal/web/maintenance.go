package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"sap13ns/internal/auth"
	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// Maintenance mode (Settings → Maintenance): while it is on, visitors get
// the maintenance page, with a 503 that tells search engines to come back
// later, instead of any page of the site, its feeds and its APIs. Whoever
// writes on the site (contributors and up) browses it as usual once
// signed in, and sign-in, the administration and the assets of their pages
// answer everyone, so that the site can be opened again.

// The built-in texts of the maintenance page.
const (
	maintenanceTitle = "We'll be back soon"
	maintenanceText  = "We are doing some maintenance on this site. It will be back in a little while: thank you for your patience."
)

// maintenanceRetry is the Retry-After of the page, in seconds.
const maintenanceRetry = 3600

type maintenancePage struct {
	Title string
	Text  []string // paragraphs
}

// maintenanceGate answers visitors with the maintenance page while the
// mode is on.
func (s *Server) maintenanceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.settings().Maintenance.On || maintenanceBypass(r) || s.maintenanceServes(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		s.serveMaintenance(w, r)
	})
}

// maintenanceBypass reports whether the request's user browses the site
// during maintenance: anyone who can write on it.
func maintenanceBypass(r *http.Request) bool {
	u := currentUser(r)
	return u != nil && auth.Can(u.Role, auth.CapEditOwnPosts)
}

// maintenanceServes reports whether path answers everyone during
// maintenance: sign-in and the administration, the assets their pages
// and the maintenance page use, and what keeps the server and the
// installed app working.
func (s *Server) maintenanceServes(path string) bool {
	if notOpenFixed(path) || s.isSignIn(path) {
		return true
	}
	for _, p := range []string{"/themes/", "/plugin-assets/", "/site-icon/", "/uploads/", "/.well-known/"} {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return slices.Contains([]string{"/healthz", "/sw.js", "/manifest.webmanifest", "/favicon.ico"}, path)
}

// serveMaintenance writes the maintenance page, in the language of the
// address (its prefix) or else the site's main language.
func (s *Server) serveMaintenance(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Retry-After", strconv.Itoa(maintenanceRetry))
	h.Set("X-Robots-Tag", "noindex")
	h.Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	st := s.settings()
	lang := s.lang()
	if first, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/"); slices.Contains(st.Languages, first) && s.i18n.Has(first) {
		lang = first
	}
	if c := rctx(r); c.UILang == "" {
		c.UILang = lang
	}
	s.renderMaintenance(w, r, http.StatusServiceUnavailable, s.maintenanceContent(st.Maintenance, s.uiLang(r)))
}

// renderMaintenance renders the maintenance page, with the site icon when
// the site has one.
func (s *Server) renderMaintenance(w http.ResponseWriter, r *http.Request, status int, content maintenancePage) {
	page := s.newAdminPage(w, r, content.Title, "", content)
	page.Icon = siteIconURL(page.Site.SiteIcon, 192)
	s.renderAdminPage(w, r, status, "maintenance", page)
}

// maintenanceContent is what the page shows in lang: the site's texts, or
// the built-in ones where it has none.
func (s *Server) maintenanceContent(m store.MaintenanceSettings, lang string) maintenancePage {
	p := maintenancePage{Title: m.Title}
	if p.Title == "" {
		p.Title = s.i18n.T(lang, maintenanceTitle)
	}
	text := m.Text
	if text == "" {
		text = s.i18n.T(lang, maintenanceText)
	}
	for para := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n") {
		if para = strings.TrimSpace(para); para != "" {
			p.Text = append(p.Text, para)
		}
	}
	return p
}

// ------------------------------------------------------- the settings page --

type maintenanceData struct {
	M            store.MaintenanceSettings
	DefaultTitle string
	DefaultText  string
	Error        string
}

func (s *Server) adminMaintenance(w http.ResponseWriter, r *http.Request) {
	s.renderMaintenanceSettings(w, r, http.StatusOK, s.settings().Maintenance, "")
}

func (s *Server) renderMaintenanceSettings(w http.ResponseWriter, r *http.Request, status int, m store.MaintenanceSettings, msg string) {
	lang := s.lang()
	d := maintenanceData{M: m, DefaultTitle: s.i18n.T(lang, maintenanceTitle), DefaultText: s.i18n.T(lang, maintenanceText), Error: msg}
	s.renderAdminStatus(w, r, status, "maintenance_settings", s.tu(r, "Maintenance mode"), "settings", d)
}

// adminMaintenanceSave turns the mode on or off (action "on" or "off"), or
// saves the texts of the page (action "save").
func (s *Server) adminMaintenanceSave(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	m := st.Maintenance
	if r.FormValue("action") == "save" {
		m.Title, m.Text = trimmed(r, "title"), strings.TrimSpace(r.FormValue("text"))
	}
	was := m.On
	switch r.FormValue("action") {
	case "on":
		m.On = true
	case "off":
		m.On = false
	}
	if !sitecheck.Maintenance(m) {
		s.renderMaintenanceSettings(w, r, http.StatusUnprocessableEntity, m, s.tu(r, "The text is too long."))
		return
	}
	st.Maintenance = m
	if err := s.saveSettings(r, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	msg := s.tu(r, "Settings saved.")
	switch {
	case m.On && !was:
		s.audit(r, uid(r), "settings.maintenance_on", "", nil)
		msg = s.tu(r, "Maintenance mode is on: visitors see the maintenance page.")
	case !m.On && was:
		s.audit(r, uid(r), "settings.maintenance_off", "", nil)
		msg = s.tu(r, "Maintenance mode is off: the site is open again.")
	}
	s.redirect(w, r, "/admin/settings/maintenance", "success", msg)
}

// adminMaintenancePreview shows the maintenance page as visitors see it.
func (s *Server) adminMaintenancePreview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Robots-Tag", "noindex")
	s.renderMaintenance(w, r, http.StatusOK, s.maintenanceContent(s.settings().Maintenance, s.lang()))
}
