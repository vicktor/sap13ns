package web

import (
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"sap13ns/internal/store"
)

// Content languages. The main language (Settings.Language) is served at the
// root; every extra language lives under /<code>/, and withLang strips that
// prefix so the router only ever sees unprefixed paths.

// languageNames are endonyms for the languages offered in the settings.
var languageNames = map[string]string{
	"ar": "العربية", "ca": "Català", "cs": "Čeština", "da": "Dansk", "de": "Deutsch", "el": "Ελληνικά",
	"en": "English", "es": "Español", "eu": "Euskara", "fi": "Suomi", "fr": "Français", "gl": "Galego",
	"he": "עברית", "hi": "हिन्दी", "it": "Italiano", "ja": "日本語", "ko": "한국어", "nb": "Norsk bokmål",
	"nl": "Nederlands", "pl": "Polski", "pt": "Português", "pt-br": "Português (Brasil)", "ro": "Română",
	"ru": "Русский", "sv": "Svenska", "tr": "Türkçe", "uk": "Українська", "zh": "中文",
}

var langCodeRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})?$`)

func validLang(code string) bool { _, ok := languageNames[code]; return ok }

func langName(code string) string {
	if n, ok := languageNames[code]; ok {
		return n
	}
	return code
}

// languageOptions lists every offered language, sorted by code.
func languageOptions() []LangLink {
	out := make([]LangLink, 0, len(languageNames))
	for code, name := range languageNames {
		out = append(out, LangLink{Code: code, Name: name})
	}
	slices.SortFunc(out, func(a, b LangLink) int { return strings.Compare(a.Code, b.Code) })
	return out
}

func (s *Server) mainLang() string { return s.settings().Language }

// siteLangs returns the main language followed by the extra ones.
func (s *Server) siteLangs() []string {
	st := s.settings()
	out := []string{st.Language}
	for _, l := range st.Languages {
		if l != st.Language && validLang(l) && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out
}

func (s *Server) multilingual() bool { return len(s.siteLangs()) > 1 }

// extraLangs lists the languages other than the main one, for admin forms.
func (s *Server) extraLangs() []LangLink { return s.extraLangsOf(s.settings()) }

func (s *Server) extraLangsOf(st store.Settings) []LangLink {
	var out []LangLink
	for _, l := range st.Languages {
		if l != st.Language && validLang(l) {
			out = append(out, LangLink{Code: l, Name: langName(l)})
		}
	}
	return out
}

func (s *Server) isExtraLang(code string) bool {
	return code != s.mainLang() && slices.Contains(s.siteLangs(), code)
}

// reqLang is the content language of a public request.
func (s *Server) reqLang(r *http.Request) string {
	if l := rctx(r).Lang; l != "" {
		return l
	}
	return s.mainLang()
}

// langPath prefixes a site path with the language segment when needed.
func (s *Server) langPath(lang, p string) string {
	if lang == "" || lang == s.mainLang() {
		return p
	}
	if p == "/" || p == "" {
		return "/" + lang + "/"
	}
	return "/" + lang + p
}

// here is the path of the current request as the visitor sees it.
func (s *Server) here(r *http.Request) string { return s.langPath(s.reqLang(r), r.URL.Path) }

// tr translates an interface string into the request's language.
func (s *Server) tr(r *http.Request, key string, args ...any) string {
	return s.i18n.T(s.reqLang(r), key, args...)
}

// postURL is the public path of a post or page, language prefix included.
func (s *Server) postURL(p *store.Post) string {
	return s.langPath(p.Lang, postPath(p.Type, p.Slug))
}

func postPath(typ, slug string) string {
	if typ == store.TypePage {
		return "/" + slug
	}
	return "/blog/" + slug
}

// prefixOnly lists routes that never take a language prefix.
var prefixOnly = []string{"/admin", "/login", "/logout", "/register", "/forgot-password", "/reset-password",
	"/setup", "/static/", "/themes/", "/uploads/", "/healthz", "/robots.txt", "/sitemap.xml", "/og/",
	"/api/", "/mcp"}

// withLang detects a /<lang>/ prefix for an extra language, records it and
// strips it from the path before routing.
func (s *Server) withLang(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if !langCodeRe.MatchString(seg) || !s.isExtraLang(seg) {
			next.ServeHTTP(w, r)
			return
		}
		p := "/" + rest
		for _, pre := range prefixOnly {
			if p == pre || strings.HasPrefix(p, strings.TrimSuffix(pre, "/")+"/") {
				http.NotFound(w, r)
				return
			}
		}
		rctx(r).Lang = seg
		r2 := r.Clone(r.Context())
		r2.URL.Path, r2.URL.RawPath = p, ""
		next.ServeHTTP(w, r2)
	})
}

// translationLinks builds the language switch for a page. alts maps each
// language to the URL of this page's version in it; languages without one
// link to their home page and are not advertised as hreflang alternates.
func (s *Server) translationLinks(r *http.Request, alts map[string]string) []LangLink {
	langs := s.siteLangs()
	if len(langs) < 2 {
		return nil
	}
	cur := s.reqLang(r)
	out := make([]LangLink, 0, len(langs))
	for _, l := range langs {
		u, ok := alts[l]
		if !ok {
			u = s.langPath(l, "/")
		}
		out = append(out, LangLink{Code: l, Name: langName(l), URL: u, AbsURL: s.cfg.AbsURL(u), Current: l == cur, Alt: ok})
	}
	return out
}

// sameAlternates maps every language to the same path (listings, archives).
func (s *Server) sameAlternates(r *http.Request) map[string]string {
	out := map[string]string{}
	for _, l := range s.siteLangs() {
		u := s.langPath(l, r.URL.Path)
		if q := r.URL.Query().Get("q"); q != "" {
			u += "?q=" + url.QueryEscape(q)
		}
		out[l] = u
	}
	return out
}
