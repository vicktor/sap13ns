package web

// The sign-in pages live under the site's sign-in address: /login, or the
// one an administrator chose in Settings → Accounts & security
// (store.Settings.LoginPath, checked by sitecheck and SignInPathFree).
//
//	{base}                  sign in (GET, POST)
//	{base}/2fa              the second step (GET, POST)
//	{base}/passkey/begin    a passkey sign-in (POST, JSON)
//	{base}/passkey/finish
//	{base}/forgot           ask for a password reset link (GET, POST)
//	{base}/reset            choose a new password with that link (GET, POST)
//	{base}/setup            the web installer of a site without users
//
// With the default /login, the addresses the account pages had before the
// sign-in address could change (/forgot-password, /reset-password,
// /setup) answer too, so that the links of e-mails sent before still
// work. Any other address of the site never answers them: once the
// address is changed, /login is an address like any other the site does
// not serve. The routes are matched before the site's own (withSignIn),
// so neither a page nor a redirect can take them.

import (
	"context"
	"net/http"
	"strings"

	"sap13ns/internal/sitecheck"
	"sap13ns/internal/store"
)

// AuthPaths are the addresses of the sign-in pages of a site.
type AuthPaths struct {
	Login, TwoFactor, PasskeyBegin, PasskeyFinish, Forgot, Reset, Setup string
}

func authPaths(base string) AuthPaths {
	return AuthPaths{Login: base, TwoFactor: base + "/2fa", PasskeyBegin: base + "/passkey/begin",
		PasskeyFinish: base + "/passkey/finish", Forgot: base + "/forgot", Reset: base + "/reset", Setup: base + "/setup"}
}

// authSuffixes are the addresses of the sign-in pages under the base.
var authSuffixes = []string{"", "/2fa", "/passkey/begin", "/passkey/finish", "/forgot", "/reset", "/setup"}

// legacyAuth are the addresses of the account pages before the sign-in
// address could change, served with the default one only.
var legacyAuth = []string{"/forgot-password", "/reset-password", "/setup"}

// signInBase is the site's sign-in address.
func (s *Server) signInBase() string { return SignInPath(s.settings()) }

// SignInPath is the sign-in address of a site with the settings st: what
// they hold when that has the form of one, the default otherwise.
func SignInPath(st store.Settings) string {
	if p := st.LoginPath; p == sitecheck.DefaultLoginPath || sitecheck.LoginPath.MatchString(p) {
		return p
	}
	return sitecheck.DefaultLoginPath
}

// auth returns the addresses of the site's sign-in pages.
func (s *Server) auth() AuthPaths { return authPaths(s.signInBase()) }

// visibleSignIn is the sign-in page that public pages may link to: none
// when the administration is hidden from visitors.
func (s *Server) visibleSignIn() string {
	if s.settings().HideAdmin {
		return ""
	}
	return s.signInBase()
}

// SignInURL is the absolute address of the site's sign-in page.
func (s *Server) SignInURL() string { return s.cfg.AbsURL(s.signInBase()) }

// signInRouter serves the sign-in pages at one base.
type signInRouter struct {
	base  string
	paths map[string]bool // every address it answers
	mux   *http.ServeMux
}

// signInRoutes returns the router of the current sign-in address, built
// again when the address changes.
func (s *Server) signInRoutes() *signInRouter {
	base := s.signInBase()
	if rt := s.signIn.Load(); rt != nil && rt.base == base {
		return rt
	}
	p := authPaths(base)
	rt := &signInRouter{base: base, paths: map[string]bool{}, mux: http.NewServeMux()}
	handle := func(pattern string, h http.HandlerFunc) {
		rt.mux.HandleFunc(pattern, h)
		_, path, _ := strings.Cut(pattern, " ")
		rt.paths[path] = true
	}
	handle("GET "+p.Login, s.handleLoginForm)
	handle("POST "+p.Login, s.handleLogin)
	handle("GET "+p.TwoFactor, s.handleMFAForm)
	handle("POST "+p.TwoFactor, s.handleMFA)
	handle("POST "+p.PasskeyBegin, s.loginPasskeyBegin)
	handle("POST "+p.PasskeyFinish, s.loginPasskeyFinish)
	forgot, reset, setup := []string{p.Forgot}, []string{p.Reset}, []string{p.Setup}
	if base == sitecheck.DefaultLoginPath {
		forgot, reset, setup = append(forgot, legacyAuth[0]), append(reset, legacyAuth[1]), append(setup, legacyAuth[2])
	}
	for _, path := range forgot {
		handle("GET "+path, s.handleForgotForm)
		handle("POST "+path, s.handleForgot)
	}
	for _, path := range reset {
		handle("GET "+path, s.handleResetForm)
		handle("POST "+path, s.handleReset)
	}
	for _, path := range setup {
		handle("GET "+path, s.handleSetupForm)
		handle("POST "+path, s.handleSetup)
	}
	s.signIn.Store(rt)
	return rt
}

// isSignIn reports whether path is the address of a sign-in page of the
// site, without a language prefix.
func (s *Server) isSignIn(path string) bool { return s.signInRoutes().paths[path] }

// underSignIn reports whether a redirect source is the site's sign-in
// address or under it, where the sign-in pages answer first: the
// redirects page and imports refuse it, as SignInPathFree refuses an
// address with a redirect.
func (s *Server) underSignIn(source string) bool {
	return sitecheck.UnderSignIn(s.signInBase(), sourceKey(source))
}

// withSignIn serves the sign-in pages at the site's sign-in address, ahead
// of the site's routes. A path under a language prefix is not one of them
// (sign-in is not translated by address): it goes on to the routes, which
// answer it as any address they do not know. With the administration
// hidden from visitors (hideAdmin), every address under /admin answers
// them as an unknown one too, before the routes can tell it apart by a
// 405 or a redirect to the trailing slash.
func (s *Server) withSignIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rctx(r).Lang == "" {
			if rt := s.signInRoutes(); rt.paths[r.URL.Path] {
				rt.mux.ServeHTTP(w, r)
				return
			}
			if p := r.URL.Path; (p == "/admin" || strings.HasPrefix(p, "/admin/")) && s.hideAdmin(r) {
				s.unknownAddress(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hideAdmin reports whether a request for the administration gets the
// answer of an address the site does not serve instead of the way to the
// sign-in page: "Hide the administration from visitors" is on, and
// nobody is signed in, nor halfway (the second step: they have just used
// the sign-in address).
func (s *Server) hideAdmin(r *http.Request) bool {
	if currentUser(r) != nil || !s.settings().HideAdmin {
		return false
	}
	sess := rctx(r).Session
	return sess == nil || !sess.MFAPending
}

// unknownAddress answers as the site's routes answer an address they do
// not serve, headers included: a single segment is the page that does not
// exist (GET /{slug}, through the page cache), or a 405 to another method;
// a deeper address the plain 404. On a site of a network that is not
// open, the network's status page (SignInOnly).
func (s *Server) unknownAddress(w http.ResponseWriter, r *http.Request) {
	if deny, ok := r.Context().Value(signInOnlyKey{}).(http.Handler); ok {
		deny.ServeHTTP(w, r)
		return
	}
	switch {
	case strings.Contains(strings.TrimPrefix(r.URL.Path, "/"), "/"):
		http.NotFound(w, r)
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
	default:
		s.cached(s.notFound)(w, r)
	}
}

// signInReserved reports whether p cannot be the sign-in address because
// the site, or one of its routes (routes.go), answers there or under it:
// an address no redirect may take (reservedRedirectSource), a name no
// page may have (reservedSlugs), as the address or its first segment; a
// language prefix; the families of generated files (feed…, sitemap…,
// llms…); and WordPress's paged archives (/page/2). The default /login
// is the sign-in page's own.
func signInReserved(p string) bool {
	if p == sitecheck.DefaultLoginPath {
		return false
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	if validLang(first) || reservedSlugs[first] || first == "page" || reservedRedirectSource(p) || reservedRedirectSource("/"+first) {
		return true
	}
	for _, family := range []string{"feed", "sitemap", "llms"} {
		if strings.HasPrefix(first, family) {
			return true
		}
	}
	return false
}

// SignInPathFree reports whether p can be the sign-in address of the site
// whose store is st: it has the form of one (sitecheck.LoginPath), the
// site does not serve it itself (signInReserved), no page has it as its
// address, in any language or status (a draft could be published, a
// language added), and no redirect starts at it or under it. The default
// /login always can. The settings page, restores and the command line
// apply it.
func SignInPathFree(ctx context.Context, st *store.Store, p string) (bool, error) {
	if p == sitecheck.DefaultLoginPath {
		return true, nil
	}
	if !sitecheck.LoginPath.MatchString(p) || signInReserved(p) {
		return false, nil
	}
	if !strings.Contains(p[1:], "/") { // a page's address has one segment; posts are under /blog
		if taken, err := st.SlugInUse(ctx, store.TypePage, p[1:]); err != nil || taken {
			return false, err
		}
	}
	list, err := st.Redirects(ctx)
	if err != nil {
		return false, err
	}
	for _, rd := range list {
		if sitecheck.UnderSignIn(p, sourceKey(rd.Source)) {
			return false, nil
		}
	}
	return true, nil
}

// CheckRestoredSignIn is SignInPathFree for a restore (st is its
// transaction, with the restored content): false on an error.
func CheckRestoredSignIn(ctx context.Context, st *store.Store, p string) bool {
	ok, err := SignInPathFree(ctx, st, p)
	return err == nil && ok
}

// signInCheck is the sitecheck.Site.SignIn of the settings page: the
// sign-in address is checked against this site's routes, pages and
// redirects.
func (s *Server) signInCheck(ctx context.Context) func(string) bool {
	return func(p string) bool {
		ok, err := SignInPathFree(ctx, s.store, p)
		if err != nil {
			s.log.Error("check the sign-in address", "err", err)
		}
		return err == nil && ok
	}
}

// ------------------------------------------------- a site that is not open --

type signInOnlyKey struct{}

// SignInOnly marks a request to a site of a network that is not open:
// waiting for the network's approval, or held for its billing. The site
// answers its sign-in pages, its single sign-on, its administration and
// the assets of their pages, and deny the rest. Only the site knows its
// sign-in address, so the network loads it for any address that could be
// one (NotOpenServes).
func SignInOnly(ctx context.Context, deny http.Handler) context.Context {
	return context.WithValue(ctx, signInOnlyKey{}, deny)
}

// notOpenFixed are the addresses a site that is not open answers whatever
// its sign-in address.
func notOpenFixed(path string) bool {
	for _, p := range []string{"/logout", "/sso", "/admin"} {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "/static/")
}

// NotOpenServes reports whether a site that is not open may answer path
// (SignInOnly): one of its fixed addresses, or an address that is a
// sign-in page under some sign-in address a site can have. The network
// answers the others with its status page without loading the site.
func NotOpenServes(path string) bool {
	if notOpenFixed(path) {
		return true
	}
	for _, legacy := range legacyAuth {
		if path == legacy {
			return true
		}
	}
	for _, suffix := range authSuffixes {
		if base, ok := strings.CutSuffix(path, suffix); ok &&
			(base == sitecheck.DefaultLoginPath || sitecheck.LoginPath.MatchString(base) && !signInReserved(base)) {
			return true
		}
	}
	return false
}

// signInGate applies SignInOnly.
func (s *Server) signInGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deny, ok := r.Context().Value(signInOnlyKey{}).(http.Handler)
		if !ok || notOpenFixed(r.URL.Path) || s.isSignIn(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		deny.ServeHTTP(w, r)
	})
}
