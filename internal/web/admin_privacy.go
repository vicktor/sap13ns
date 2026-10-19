package web

import (
	"net/http"
	"strconv"

	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// Settings → Privacy: the cookie consent of the public site (consent.go).

type privacySettingsData struct {
	C     store.ConsentSettings
	Main  LangLink   // the main language
	Langs []LangLink // the extra ones, with their texts in Texts
	Texts map[string]store.ConsentText
	// Categories is the generated cookie list, in the administrator's
	// language; Optional whether anything in it needs consent.
	Categories  []ConsentCategory
	Optional    bool
	DefaultText string // the built-in banner text, as a placeholder
	Error       string
}

func (s *Server) adminPrivacySettings(w http.ResponseWriter, r *http.Request) {
	s.renderPrivacySettings(w, r, http.StatusOK, s.settings().Consent, "")
}

func (s *Server) renderPrivacySettings(w http.ResponseWriter, r *http.Request, status int, c store.ConsentSettings, msg string) {
	st := s.settings()
	lang := s.uiLang(r)
	d := privacySettingsData{C: c, Main: LangLink{Code: st.Language, Name: langName(st.Language)}, Langs: s.extraLangs(),
		Texts: c.Texts, Categories: s.consentCategories(lang), Error: msg,
	}
	if d.Texts == nil {
		d.Texts = map[string]store.ConsentText{}
	}
	d.Optional = consentOptional(d.Categories)
	d.DefaultText = s.i18n.T(st.Language, consentAskText)
	if !d.Optional {
		d.DefaultText = s.i18n.T(st.Language, consentNoticeText)
	}
	s.renderAdminStatus(w, r, status, "privacy_settings", s.tu(r, "Privacy and cookies"), "settings", d)
}

func (s *Server) adminPrivacySettingsSave(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	c := st.Consent
	c.Off = r.FormValue("enabled") != "1"
	c.Title, c.Text, c.PolicyURL = trimmed(r, "title"), trimmed(r, "text"), trimmed(r, "policy_url")
	c.Texts = map[string]store.ConsentText{}
	for _, l := range s.extraLangs() {
		t := store.ConsentText{Title: trimmed(r, "title_"+l.Code), Text: trimmed(r, "text_"+l.Code), PolicyURL: trimmed(r, "policy_url_"+l.Code)}
		if t != (store.ConsentText{}) {
			c.Texts[l.Code] = t
		}
	}
	again := r.FormValue("ask_again") == "1"
	if again && c.Revision < sitecheck.MaxConsentRevision {
		c.Revision++
	}
	st.Consent = c
	if !sitecheck.Consent(st) {
		msg := s.tu(r, "The text is too long.")
		bad := !sitecheck.ConsentLink(c.PolicyURL)
		for _, t := range c.Texts {
			bad = bad || !sitecheck.ConsentLink(t.PolicyURL)
		}
		if bad {
			msg = s.tu(r, "The policy link must be a path of this site, such as /cookies, or an address that starts with https://.")
		}
		s.renderPrivacySettings(w, r, http.StatusUnprocessableEntity, c, msg)
		return
	}
	if err := s.saveSettings(r, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "settings.privacy_saved", "", map[string]string{"enabled": strconv.FormatBool(!c.Off),
		"revision": strconv.Itoa(c.Revision)})
	if again {
		s.redirect(w, r, "/admin/settings/privacy", "success", s.tu(r, "Saved. Every visitor will be asked again."))
		return
	}
	s.redirect(w, r, "/admin/settings/privacy", "success", s.tu(r, "Settings saved."))
}
