package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// The language of the administration panel. Each user can choose one among
// the languages the binary has a catalog for (ui/i18n), with the language
// menu in the top bar or on their profile; "" follows the site's main
// language. requireCap puts the user's choice in the request context, so
// that the admin templates ({{T}}) and the strings built in Go (s.tu) come
// out in it. The public site keeps the site's languages (lang.go) and
// never reads it. On a network every site, and the portal, has its own
// users, so a user signed in through single sign-on chooses per site.

// uiLang is the admin language of r: the signed-in user's choice on an
// admin route, else the site's main language. r may be nil.
func (s *Server) uiLang(r *http.Request) string {
	if r != nil {
		if l := rctx(r).UILang; l != "" {
			return l
		}
	}
	return s.lang()
}

// tu translates an admin interface string into the language of r (see
// uiLang). Handlers use it where s.t would give the site's main language.
func (s *Server) tu(r *http.Request, key string, args ...any) string {
	return s.i18n.T(s.uiLang(r), key, args...)
}

// uiDateTime renders a short date and a time in the language of r.
func (s *Server) uiDateTime(r *http.Request, t time.Time) string { return s.dateTimeIn(s.uiLang(r), t) }

// dateTime renders a short date and a time in the site's main language,
// for what goes to other people (e-mails).
func (s *Server) dateTime(t time.Time) string { return s.dateTimeIn(s.lang(), t) }

// userLang is the admin language u chose, when the binary has a catalog
// for it; "" otherwise.
func (s *Server) userLang(u *store.User) string {
	if u != nil && s.uiLanguage(u.Language) {
		return u.Language
	}
	return ""
}

// uiLanguage reports whether the admin panel can be shown in lang.
func (s *Server) uiLanguage(lang string) bool {
	return lang != "" && validLang(lang) && s.i18n.Has(lang)
}

// uiLanguages lists the admin languages by their own names, sorted by
// code, with cur marked.
func (s *Server) uiLanguages(cur string) []LangLink {
	var out []LangLink
	for _, l := range s.i18n.Languages() {
		if validLang(l) {
			out = append(out, LangLink{Code: l, Name: langName(l), Current: l == cur})
		}
	}
	slices.SortFunc(out, func(a, b LangLink) int { return strings.Compare(a.Code, b.Code) })
	return out
}

// rtlLangs are the languages offered (languageNames) written right to left.
var rtlLangs = []string{"ar", "he"}

// langDir is the direction of lang's script, for <html dir>.
func langDir(lang string) string {
	base, _, _ := strings.Cut(lang, "-")
	if slices.Contains(rtlLangs, base) {
		return "rtl"
	}
	return "ltr"
}

// langNext is where choosing a language from the page of r comes back to:
// the page itself when it was a GET, else the dashboard (a form's POST
// address may not answer a GET).
func langNext(r *http.Request) string {
	if r.Method == http.MethodGet {
		return safeNext(r.URL.RequestURI())
	}
	return "/admin/"
}

// signInLangPage reports whether path is a sign-in page that takes ?lang=
// (signInLang): sign-in, the second step and the forgotten password, at
// the site's sign-in address (signin.go), and registration.
func (s *Server) signInLangPage(path string) bool {
	if path == "/register" {
		return true
	}
	a := s.auth()
	return path == a.Login || path == a.TwoFactor || path == a.Forgot ||
		path == legacyAuth[0] && a.Login == sitecheck.DefaultLoginPath
}

// signInLang shows a sign-in page in the admin language of its ?lang=
// instead of the site's. Nothing is stored, no cookie and no session: the
// page carries the parameter in its forms and links, and the user's own
// choice applies once they are signed in. These pages are never cached
// (only public GET routes are), and next stays apart (safeNext).
func (s *Server) signInLang(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if l := r.URL.Query().Get("lang"); s.uiLanguage(l) {
			rctx(r).UILang = l
		}
		h(w, r)
	}
}

// signInLangs is the language menu of a sign-in page: the same page in
// each admin language, keeping where to go after signing in.
func signInLangs(r *http.Request, langs []LangLink) []LangLink {
	next := r.URL.Query().Get("next")
	if r.Method == http.MethodPost {
		next = r.PostFormValue("next")
	}
	out := make([]LangLink, len(langs))
	for i, l := range langs {
		q := url.Values{"lang": {l.Code}}
		if next != "" {
			q.Set("next", safeNext(next))
		}
		l.URL = r.URL.Path + "?" + q.Encode()
		out[i] = l
	}
	return out
}

// adminLanguage saves the admin language the user chose ("" for the site's
// language) and goes back to the page they were on.
func (s *Server) adminLanguage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	lang := strings.TrimSpace(r.PostFormValue("lang"))
	if lang != "" && !s.uiLanguage(lang) {
		s.adminError(w, r, http.StatusBadRequest, s.tu(r, "That language is not available."))
		return
	}
	if lang != u.Language {
		if err := s.store.SetUserLanguage(r.Context(), u.ID, lang); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}
