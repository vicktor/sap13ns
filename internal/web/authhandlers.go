package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

var usernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type authForm struct {
	Error             string
	Notice            string
	Next              string
	Values            map[string]string
	AllowRegistration bool
	CanReset          bool
	Token             string
	Passkeys          bool // this build supports passkeys (sign-in button, autofill)
	TOTP              bool // second step: the account has an authenticator app
	HasPasskey        bool // second step: the account has passkeys
}

func (s *Server) renderAuth(w http.ResponseWriter, r *http.Request, status int, name, title string, f *authForm) {
	if f.Values == nil {
		f.Values = map[string]string{}
	}
	f.AllowRegistration = s.settings().AllowRegistration
	f.CanReset = s.mailer.Enabled() || s.cfg.Dev
	f.Passkeys = passkeysAvailable
	w.Header().Set("X-Robots-Tag", "noindex")
	s.renderAdminStatus(w, r, status, name, title, "", f)
}

func (s *Server) audit(r *http.Request, userID int64, action, target string, details map[string]string) {
	if err := s.store.Audit(r.Context(), userID, action, target, clientIP(r), details); err != nil {
		s.log.Error("audit", "err", err)
	}
}

func uid(r *http.Request) int64 {
	if u := currentUser(r); u != nil {
		return u.ID
	}
	return 0
}

// ------------------------------------------------------------------ login --

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if currentUser(r) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.renderAuth(w, r, http.StatusOK, "login", s.t("Log in"), &authForm{Next: next})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	login := strings.ToLower(strings.TrimSpace(r.PostFormValue("login")))
	password := r.PostFormValue("password")
	next := safeNext(r.PostFormValue("next"))
	ip := ipKey(clientIP(r))
	form := &authForm{Next: next, Values: map[string]string{"login": login}}
	fail := func(status int, msg string) {
		form.Error = msg
		s.renderAuth(w, r, status, "login", s.t("Log in"), form)
	}
	if len(login) > 254 || len(password) > auth.MaxPasswordLen {
		fail(http.StatusBadRequest, s.t("Invalid username or password."))
		return
	}
	if !s.loginIP.Allow(ip) || s.loginUser.Blocked(login) {
		s.audit(r, 0, "login.throttled", login, nil)
		fail(http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	ctx := r.Context()
	u, err := s.store.UserByLogin(ctx, login)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.serverError(w, r, err)
			return
		}
		auth.VerifyDummy(password) // equalize timing
		s.loginUser.Allow(login)
		s.audit(r, 0, "login.failed", login, map[string]string{"reason": "unknown user"})
		fail(http.StatusUnauthorized, s.t("Invalid username or password."))
		return
	}
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		auth.VerifyDummy(password)
		s.audit(r, u.ID, "login.locked", u.Username, nil)
		fail(http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, password) || u.Status != "active" {
		s.loginUser.Allow(login)
		s.store.RecordLoginFailure(ctx, u.ID, 10, 15*time.Minute)
		s.audit(r, u.ID, "login.failed", u.Username, map[string]string{"reason": "bad password or disabled"})
		fail(http.StatusUnauthorized, s.t("Invalid username or password."))
		return
	}
	if auth.NeedsRehash(u.PasswordHash) {
		if h, err := auth.HashPassword(password); err == nil {
			s.store.SetPassword(ctx, u.ID, h)
		}
	}
	s.loginUser.Reset(login)
	if hasSecondFactor(u) {
		if err := s.startSession(w, r, u, true); err != nil {
			s.serverError(w, r, err)
			return
		}
		http.Redirect(w, r, "/login/2fa?next="+url.QueryEscape(next), http.StatusSeeOther)
		return
	}
	s.completeLogin(w, r, u, next, "password")
}

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, u *store.User, next, method string) {
	next, err := s.establishLogin(w, r, u, next, method)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// establishLogin opens a fully authenticated session and returns where to go.
func (s *Server) establishLogin(w http.ResponseWriter, r *http.Request, u *store.User, next, method string) (string, error) {
	if err := s.startSession(w, r, u, false); err != nil {
		return "", err
	}
	s.store.RecordLoginSuccess(r.Context(), u.ID)
	s.audit(r, u.ID, "login.success", u.Username, map[string]string{"method": method})
	if u.Role == auth.RoleSubscriber && next == "/admin/" {
		next = "/admin/profile"
	}
	return next, nil
}

// hasSecondFactor reports whether a password login must be confirmed with an
// authenticator code or a passkey.
func hasSecondFactor(u *store.User) bool {
	return u.TOTPEnabled || (passkeysAvailable && u.PasskeyCount > 0)
}

func (s *Server) mfaForm(u *store.User, next string) *authForm {
	return &authForm{Next: next, TOTP: u.TOTPEnabled, HasPasskey: passkeysAvailable && u.PasskeyCount > 0}
}

func (s *Server) pendingMFA(r *http.Request) (*store.User, bool) {
	sess := rctx(r).Session
	if sess == nil || !sess.MFAPending {
		return nil, false
	}
	u, err := s.store.UserByID(r.Context(), sess.UserID)
	if err != nil || !hasSecondFactor(u) {
		return nil, false
	}
	return u, true
}

func (s *Server) handleMFAForm(w http.ResponseWriter, r *http.Request) {
	u, ok := s.pendingMFA(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	s.renderAuth(w, r, http.StatusOK, "login_2fa", s.t("Two-factor authentication"), s.mfaForm(u, safeNext(r.URL.Query().Get("next"))))
}

func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	u, ok := s.pendingMFA(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	next := safeNext(r.PostFormValue("next"))
	form := s.mfaForm(u, next)
	fail := func(status int, msg string) {
		form.Error = msg
		s.renderAuth(w, r, status, "login_2fa", s.t("Two-factor authentication"), form)
	}
	if !validCSRF(r) {
		fail(http.StatusForbidden, s.t("Your session form expired. Go back, reload the page and try again."))
		return
	}
	if !s.mfaUser.Allow(u.Username) {
		s.audit(r, u.ID, "2fa.throttled", u.Username, nil)
		fail(http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	ctx := r.Context()
	code := strings.TrimSpace(r.PostFormValue("code"))
	method := ""
	if len(strings.ReplaceAll(code, " ", "")) == 6 {
		secret, err := s.keys.Decrypt(u.TOTPSecret, []byte(u.Username))
		if err == nil {
			if step := auth.ValidateTOTP(secret, code, time.Now()); step > 0 {
				if fresh, _ := s.store.UseTOTPStep(ctx, u.ID, step); fresh {
					method = "totp"
				}
			}
		}
	} else if code != "" {
		if used, _ := s.store.ConsumeRecoveryCode(ctx, u.ID, auth.HashTokenHex(auth.NormalizeRecoveryCode(code))); used {
			method = "recovery"
		}
	}
	if method == "" {
		s.audit(r, u.ID, "2fa.failed", u.Username, nil)
		fail(http.StatusUnauthorized, s.t("The code is not valid."))
		return
	}
	s.mfaUser.Reset(u.Username)
	if method == "recovery" {
		s.flash(w, "warning", s.t("You signed in with a recovery code. Generate new codes if you are running out."))
	}
	s.completeLogin(w, r, u, next, method)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sess := rctx(r).Session
	if sess != nil {
		if !validCSRF(r) {
			http.Error(w, "invalid request", http.StatusForbidden)
			return
		}
		s.store.DeleteSession(r.Context(), sess.TokenHash)
		s.audit(r, sess.UserID, "logout", "", nil)
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// --------------------------------------------------------------- register --

func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	if !s.settings().AllowRegistration {
		s.notFound(w, r)
		return
	}
	s.renderAuth(w, r, http.StatusOK, "register", s.t("Create an account"), &authForm{Token: s.keys.Sign("register", "", time.Now())})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	if !st.AllowRegistration {
		s.notFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	r.ParseForm()
	vals := map[string]string{
		"username": strings.ToLower(strings.TrimSpace(r.PostFormValue("username"))),
		"email":    strings.TrimSpace(r.PostFormValue("email")),
	}
	form := &authForm{Values: vals, Token: s.keys.Sign("register", "", time.Now())}
	fail := func(status int, msg string) {
		form.Error = msg
		s.renderAuth(w, r, status, "register", s.t("Create an account"), form)
	}
	age, valid := s.keys.Verify("register", "", r.PostFormValue("token"), 6*time.Hour)
	if r.PostFormValue("website") != "" || !valid || age < 2*time.Second {
		fail(http.StatusBadRequest, s.t("The form could not be verified. Please try again."))
		return
	}
	if !s.registerIP.Allow(ipKey(clientIP(r))) {
		fail(http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	password := r.PostFormValue("password")
	if msg := s.validateNewAccount(vals["username"], vals["email"], password, r.PostFormValue("password2")); msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	role := st.DefaultRole
	if auth.RoleRank(role) > auth.RoleRank(auth.RoleAuthor) || !auth.ValidRole(role) {
		role = auth.RoleSubscriber // public sign-ups never get editorial power
	}
	id, err := s.store.CreateUser(r.Context(), store.NewUser{Username: vals["username"], Email: vals["email"], Role: role, PasswordHash: hash})
	if errors.Is(err, store.ErrConflict) {
		fail(http.StatusConflict, s.t("That username or e-mail address is already registered."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, id, "user.registered", u.Username, nil)
	s.plugins.Emit("user.registered", map[string]any{"username": u.Username, "role": u.Role})
	s.flash(w, "success", s.t("Welcome! Your account is ready."))
	s.completeLogin(w, r, u, "/admin/profile", "registration")
}

// validateNewAccount returns a translated error message, or "".
func (s *Server) validateNewAccount(username, email, password, confirm string) string {
	if !usernameRe.MatchString(username) {
		return s.t("The username must be 3–32 characters: lowercase letters, numbers, dots, dashes or underscores.")
	}
	if a, err := mail.ParseAddress(email); err != nil || a.Address != email || len(email) > 254 {
		return s.t("Enter a valid e-mail address.")
	}
	if password != confirm {
		return s.t("The passwords do not match.")
	}
	if err := auth.CheckPasswordPolicy(password, username, email); err != nil {
		return s.passwordPolicyMessage(err)
	}
	return ""
}

func (s *Server) passwordPolicyMessage(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "at least"):
		return s.t("The password must have at least %d characters.", auth.MinPasswordLen)
	case strings.Contains(msg, "at most"):
		return s.t("The password is too long.")
	case strings.Contains(msg, "common"):
		return s.t("That password is too common. Choose another one.")
	case strings.Contains(msg, "username"), strings.Contains(msg, "e-mail"):
		return s.t("The password must not contain your username or e-mail address.")
	default:
		return s.t("The password is too simple. Use a longer, less repetitive one.")
	}
}

// ------------------------------------------------------------ password reset --

func (s *Server) handleForgotForm(w http.ResponseWriter, r *http.Request) {
	s.renderAuth(w, r, http.StatusOK, "forgot", s.t("Reset your password"), &authForm{})
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	email := strings.TrimSpace(r.PostFormValue("email"))
	form := &authForm{Notice: s.t("If the address belongs to an account, you will receive a link to choose a new password. It is valid for one hour.")}
	if !s.resetIP.Allow(ipKey(clientIP(r))) || !s.resetIP.Allow("email:"+strings.ToLower(email)) {
		form.Notice, form.Error = "", s.t("Too many attempts. Wait a few minutes and try again.")
		s.renderAuth(w, r, http.StatusTooManyRequests, "forgot", s.t("Reset your password"), form)
		return
	}
	if u, err := s.store.UserByEmail(r.Context(), email); err == nil && u.Status == "active" {
		token := auth.NewToken(32)
		if err := s.store.CreatePasswordReset(r.Context(), auth.HashToken(token), u.ID, time.Hour); err != nil {
			s.serverError(w, r, err)
			return
		}
		link := s.cfg.AbsURL("/reset-password?token=" + token)
		body := s.t("Someone asked to reset the password of your account %s at %s.", u.Username, s.settings().SiteTitle) + "\n\n" +
			s.t("Open this link to choose a new password (valid for one hour):") + "\n" + link + "\n\n" +
			s.t("If it was not you, ignore this message: your password has not changed.")
		if err := s.mailer.Send(u.Email, s.t("Password reset")+" · "+s.settings().SiteTitle, body); err != nil {
			s.log.Error("send reset e-mail", "err", err)
		}
		s.audit(r, u.ID, "password.reset_requested", u.Username, nil)
	}
	s.renderAuth(w, r, http.StatusOK, "forgot", s.t("Reset your password"), form)
}

func (s *Server) handleResetForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	ok, err := s.store.PasswordResetValid(r.Context(), auth.HashToken(token))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form := &authForm{Token: token}
	if !ok {
		form.Error = s.t("This link is invalid or has expired. Request a new one.")
	}
	s.renderAuth(w, r, http.StatusOK, "reset", s.t("Choose a new password"), form)
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	token := r.PostFormValue("token")
	form := &authForm{Token: token}
	password := r.PostFormValue("password")
	if password != r.PostFormValue("password2") {
		form.Error = s.t("The passwords do not match.")
		s.renderAuth(w, r, http.StatusUnprocessableEntity, "reset", s.t("Choose a new password"), form)
		return
	}
	invalid := func() {
		form.Error = s.t("This link is invalid or has expired. Request a new one.")
		s.renderAuth(w, r, http.StatusBadRequest, "reset", s.t("Choose a new password"), form)
	}
	userID, err := s.store.PasswordResetUser(r.Context(), auth.HashToken(token))
	if err != nil {
		invalid()
		return
	}
	u, err := s.store.UserByID(r.Context(), userID)
	if err != nil {
		invalid()
		return
	}
	// Check the policy before burning the token so a rejected password can be retried.
	if err := auth.CheckPasswordPolicy(password, u.Username, u.Email); err != nil {
		form.Error = s.passwordPolicyMessage(err)
		s.renderAuth(w, r, http.StatusUnprocessableEntity, "reset", s.t("Choose a new password"), form)
		return
	}
	if consumed, err := s.store.ConsumePasswordReset(r.Context(), auth.HashToken(token)); err != nil || consumed != userID {
		invalid()
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.SetPassword(r.Context(), userID, hash)
	s.store.DeleteUserSessions(r.Context(), userID, nil)
	s.audit(r, userID, "password.reset", u.Username, nil)
	s.redirect(w, r, "/login", "success", s.t("Your password has been changed. You can log in now."))
}

// ------------------------------------------------------------------ setup --

func (s *Server) setupTokenValid(token string) bool {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) == 1
}

func (s *Server) handleSetupForm(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !s.setupTokenValid(token) {
		http.NotFound(w, r)
		return
	}
	s.renderAuth(w, r, http.StatusOK, "setup", s.t("Install sap13ns"), &authForm{Token: token,
		Values: map[string]string{"site_title": s.settings().SiteTitle}})
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	token := r.PostFormValue("token")
	if !s.setupTokenValid(token) {
		http.NotFound(w, r)
		return
	}
	vals := map[string]string{
		"site_title": strings.TrimSpace(r.PostFormValue("site_title")),
		"username":   strings.ToLower(strings.TrimSpace(r.PostFormValue("username"))),
		"email":      strings.TrimSpace(r.PostFormValue("email")),
	}
	form := &authForm{Token: token, Values: vals}
	password := r.PostFormValue("password")
	if msg := s.validateNewAccount(vals["username"], vals["email"], password, r.PostFormValue("password2")); msg != "" || vals["site_title"] == "" {
		if msg == "" {
			msg = s.t("The site title is required.")
		}
		form.Error = msg
		s.renderAuth(w, r, http.StatusUnprocessableEntity, "setup", s.t("Install sap13ns"), form)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	s.setupMu.Lock()
	n, err := s.store.CountUsers(ctx)
	if err != nil || n > 0 || s.setupToken == "" {
		s.setupMu.Unlock()
		http.NotFound(w, r)
		return
	}
	id, err := s.store.CreateUser(ctx, store.NewUser{Username: vals["username"], Email: vals["email"], Role: auth.RoleAdmin, PasswordHash: hash})
	if err == nil {
		s.setupToken = ""
	}
	s.setupMu.Unlock()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := s.settings()
	st.SiteTitle = vals["site_title"]
	if err := s.store.SaveSettings(ctx, st); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.ReloadSettings(ctx)
	s.contentChanged()
	u, _ := s.store.UserByID(ctx, id)
	s.audit(r, id, "setup.completed", u.Username, nil)
	s.flash(w, "success", s.t("Installation complete. Welcome to your new site!"))
	s.completeLogin(w, r, u, "/admin/", "setup")
}
