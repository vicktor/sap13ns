package web

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

type reqCtx struct {
	IP      netip.Addr
	User    *store.User
	Session *store.Session
	Lang    string // content language from the URL prefix; "" = main language
}

type ctxKey struct{}

func rctx(r *http.Request) *reqCtx {
	if c, ok := r.Context().Value(ctxKey{}).(*reqCtx); ok {
		return c
	}
	return &reqCtx{}
}

func clientIP(r *http.Request) netip.Addr { return rctx(r).IP }

// ipKey is the rate-limit key of an address: the address itself for IPv4,
// its /64 for IPv6, where one host usually holds the whole prefix.
func ipKey(ip netip.Addr) string {
	ip = ip.Unmap()
	if ip.Is4() || !ip.IsValid() {
		return ip.String()
	}
	p, _ := ip.Prefix(64)
	return p.String()
}

func currentUser(r *http.Request) *store.User { return rctx(r).User }

// statusWriter records the status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

// Handler assembles the middleware chain around the router.
func (s *Server) Handler() http.Handler {
	mux := s.routes()
	var h http.Handler = mux
	h = s.withLang(h)
	h = s.withRedirects(h) // sees the path with its language prefix
	h = s.emergencyGate(h) // a suspended site served for an emergency access only
	h = s.loadSession(h)
	h = s.cop.Handler(h)
	h = s.securityHeaders(h)
	h = s.logRequests(h)
	h = s.withClientIP(h) // outermost so that logs and panics see the visitor IP
	h = s.recoverPanics(h)
	return h
}

func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "err", v, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		level := slog.LevelInfo
		if strings.HasPrefix(r.URL.Path, "/static/") || strings.HasPrefix(r.URL.Path, "/themes/") || strings.HasPrefix(r.URL.Path, "/uploads/") {
			level = slog.LevelDebug
		}
		if sw.status >= 500 {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.bytes, "ms", time.Since(start).Milliseconds(), "ip", clientIP(r).String())
	})
}

// withClientIP resolves the visitor address. Forwarded headers are only
// trusted when the connection comes from a configured proxy (nginx).
func (s *Server) withClientIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &reqCtx{IP: s.resolveIP(r)}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Server) resolveIP(r *http.Request) netip.Addr {
	remote, unixSocket := netip.Addr{}, false
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		remote = ap.Addr().Unmap()
	} else {
		unixSocket = true // unix socket connections only come from the local proxy
	}
	if !unixSocket && !s.trusted(remote) {
		return remote
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		// Walk from the right, skipping our own proxies.
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			if i == 0 || !s.trusted(a) {
				return a
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(xr)); err == nil {
			return a.Unmap()
		}
	}
	return remote
}

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: https:; " +
	"media-src 'self' https:; font-src 'self'; connect-src 'self'; frame-src 'none'; frame-ancestors 'none'; " +
	"base-uri 'self'; form-action 'self'; object-src 'none'; manifest-src 'self'; worker-src 'none'"

// sameHost reports whether a Host header names host, whatever its port,
// case or trailing dot.
func sameHost(hostport, host string) bool {
	h := hostport
	if hh, _, err := net.SplitHostPort(hostport); err == nil {
		h = hh
	}
	return host != "" && strings.EqualFold(strings.TrimSuffix(h, "."), host)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	policy := csp
	if s.cfg.BaseURL.Scheme == "https" {
		policy += "; upgrade-insecure-requests"
	}
	hsts := "max-age=63072000; includeSubDomains"
	if s.cfg.HSTSNoSubdomains {
		// A site on a customer's domain must not force HTTPS on the
		// customer's other hosts.
		hsts = "max-age=63072000"
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hsts := hsts
		if s.cfg.Multisite && !sameHost(r.Host, s.cfg.BaseURL.Hostname()) {
			// A site of a network answers a few paths on its other hosts
			// too (ACME challenges, the WebAuthn related origins file),
			// which can be a customer's domain: never with subdomains.
			hsts = "max-age=63072000"
		}
		h := w.Header()
		if strings.HasPrefix(r.URL.Path, "/admin") {
			p := policy
			if s.billing != nil && r.URL.Path == "/admin/sites" {
				// Its upgrade and billing forms redirect to Stripe.
				p = strings.Replace(p, "form-action 'self'", "form-action 'self' "+stripeForms, 1)
			}
			h.Set("Content-Security-Policy", p)
		} else {
			h.Set("Content-Security-Policy", s.pluginCSP(policy)) // plus frame and image sources of enabled plugins
		}
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), browsing-topics=(), interest-cohort=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if s.cfg.HSTS {
			h.Set("Strict-Transport-Security", hsts)
		}
		next.ServeHTTP(w, r)
	})
}

// --------------------------------------------------------------- sessions --

func (s *Server) cookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-sap13ns"
	}
	return "sap13ns_session"
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ck, err := r.Cookie(s.cookieName())
		if err != nil || len(ck.Value) < 20 || len(ck.Value) > 100 {
			next.ServeHTTP(w, r)
			return
		}
		c := rctx(r)
		hash := auth.HashToken(ck.Value)
		sess, err := s.store.SessionByHash(r.Context(), hash)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("load session", "err", err)
			}
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		st := s.settings()
		if time.Since(sess.LastSeenAt) > time.Duration(st.IdleHours)*time.Hour {
			s.store.DeleteSession(r.Context(), hash)
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		u, err := s.store.UserByID(r.Context(), sess.UserID)
		if err != nil || u.Status != "active" {
			s.store.DeleteSession(r.Context(), hash)
			s.clearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		}
		// A network admin's operator session lasts only as long as the
		// site's support access, or their emergency access (sso.go).
		if (sess.AMR == store.AMRSSOOperator || sess.AMR == store.AMRSSOEmergency) && !s.operatorAllowed(w, r, u, sess) {
			next.ServeHTTP(w, r)
			return
		}
		if time.Since(sess.LastSeenAt) > 5*time.Minute {
			s.store.TouchSession(r.Context(), hash)
		}
		c.Session = sess
		if !sess.MFAPending {
			c.User = u
		}
		next.ServeHTTP(w, r)
	})
}

// startSession issues a fresh session (never reusing an old identifier),
// authenticated on this site.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, mfaPending bool) error {
	return s.newSession(w, r, u, mfaPending, store.AMRLocal)
}

// newSession issues a fresh session authenticated as amr says
// (store.AMRLocal, AMRSSO, AMRSSOOperator).
func (s *Server) newSession(w http.ResponseWriter, r *http.Request, u *store.User, mfaPending bool, amr string) error {
	if old, err := r.Cookie(s.cookieName()); err == nil {
		s.store.DeleteSession(r.Context(), auth.HashToken(old.Value))
	}
	token := auth.NewToken(32)
	st := s.settings()
	expires := time.Now().Add(time.Duration(st.SessionDays) * 24 * time.Hour)
	if mfaPending {
		expires = time.Now().Add(10 * time.Minute)
	}
	ua := r.UserAgent()
	if len(ua) > 300 {
		ua = ua[:300]
	}
	err := s.store.CreateSession(r.Context(), store.Session{
		TokenHash: auth.HashToken(token), UserID: u.ID, CSRFToken: auth.NewToken(24),
		MFAPending: mfaPending, ExpiresAt: expires, IP: clientIP(r), UserAgent: ua, AMR: amr,
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: s.cookieName(), Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
}

// validCSRF checks the synchronizer token sent in a form field or header.
func validCSRF(r *http.Request) bool {
	sess := rctx(r).Session
	if sess == nil {
		return false
	}
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok = r.PostFormValue("csrf")
	}
	return tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(sess.CSRFToken)) == 1
}

// requireCap protects admin handlers: authentication, two-factor policy,
// capability and (for unsafe methods) the CSRF token.
func (s *Server) requireCap(c auth.Cap, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			if rctx(r).Session != nil && rctx(r).Session.MFAPending {
				http.Redirect(w, r, "/login/2fa", http.StatusSeeOther)
				return
			}
			http.Redirect(w, r, "/login?next="+url.QueryEscape(safeNext(r.URL.RequestURI())), http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, s.maxBody(r))
			if !validCSRF(r) {
				s.log.Warn("invalid CSRF token", "path", r.URL.Path, "user", u.Username, "ip", clientIP(r))
				s.adminError(w, r, http.StatusForbidden, s.t("Your session form expired. Go back, reload the page and try again."))
				return
			}
		}
		// The profile is where the second factor is set up.
		if s.mustEnroll2FA(r, u) && !strings.HasPrefix(r.URL.Path, "/admin/profile") {
			if s.portal != nil {
				s.flash(w, "warning", s.t("Every sap13ns account needs two-factor authentication. Set it up to continue."))
			} else {
				s.flash(w, "warning", s.t("Two-factor authentication is required for your role. Set it up to continue."))
			}
			http.Redirect(w, r, "/admin/profile/2fa", http.StatusSeeOther)
			return
		}
		if !auth.Can(u.Role, c) {
			s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
			return
		}
		h(w, r)
	}
}

// mustEnroll2FA reports whether u, signed in by r, has to set up a second
// factor before using the admin. Two-factor authentication is mandatory
// for staff; a passkey counts as one, and so does a single sign-on through
// the network portal, which required the second factor before letting
// anyone in. On the portal itself every account needs one, whatever its
// role and the two-factor policy: whoever signs in there opens the
// account's sites.
func (s *Server) mustEnroll2FA(r *http.Request, u *store.User) bool {
	if hasSecondFactor(u) || !auth.Can(u.Role, auth.CapAdminPanel) {
		return false
	}
	if sess := rctx(r).Session; sess != nil && sess.UserID == u.ID && strings.HasPrefix(sess.AMR, store.AMRSSO) {
		return false
	}
	if s.cfg.Dev && s.cfg.DevSkip2FA {
		return false
	}
	if s.portal != nil {
		return true
	}
	if s.settings().TwoFactor == "editors" {
		return u.Role == auth.RoleAdmin || u.Role == auth.RoleEditor
	}
	return true
}

func (s *Server) maxBody(r *http.Request) int64 {
	if r.URL.Path == "/admin/plugins/upload" {
		return pluginUploadLimit
	}
	if r.URL.Path == "/admin/appearance/themes/upload" {
		return themeUploadLimit
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return s.cfg.MaxUploadBytes + 1<<20
	}
	return 4 << 20
}

// safeNext only allows local redirect targets: an absolute path on this
// host. Control characters and backslashes are refused anywhere, since
// browsers strip the former and read "\\" as "/" ("/\\evil.example" would
// leave the site), and the value is re-serialized from its parsed form.
func safeNext(next string) string {
	const fallback = "/admin/"
	if next == "" || len(next) > 2048 || strings.ContainsRune(next, '\\') {
		return fallback
	}
	for i := 0; i < len(next); i++ {
		if c := next[i]; c < 0x20 || c == 0x7f {
			return fallback
		}
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" ||
		!strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return fallback
	}
	return u.RequestURI()
}

// dirFS returns the directory as an fs.FS, or nil when it does not exist.
func dirFS(dir string) fs.FS {
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return os.DirFS(dir)
	}
	return nil
}
