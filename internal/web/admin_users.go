package web

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"rsc.io/qr"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// ----------------------------------------------------------------- users --

type usersData struct {
	Users []store.User
	Total int
	Page  *Pagination
	Role  string
	Query string
	Roles []string
	// The support access of a tenant site of a network (admin_support.go).
	Support *SupportStatus
}

var assignableRoles = []string{auth.RoleSubscriber, auth.RoleContributor, auth.RoleAuthor, auth.RoleEditor, auth.RoleAdmin}

func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")
	if !auth.ValidRole(role) {
		role = ""
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := listPage(r)
	const per = 30
	users, total, err := s.store.ListUsers(r.Context(), store.UserListQuery{Role: role, Search: q, Limit: per, Offset: (page - 1) * per})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	vals := r.URL.Query()
	vals.Del("page")
	s.renderAdmin(w, r, "users", s.t("Users"), "users", usersData{Users: users, Total: total, Role: role, Query: q, Roles: assignableRoles,
		Page: newPagination("/admin/users", vals, page, per, total), Support: s.supportStatus(r)})
}

type userEditData struct {
	User     *store.User
	New      bool
	Error    string
	Roles    []string
	Sessions []store.Session
	Others   []store.User // reassignment targets when deleting
	Self     bool
	// Protected: a network admin's account on the portal, which only
	// another network admin may change.
	Protected bool
}

func (s *Server) adminUserNew(w http.ResponseWriter, r *http.Request) {
	s.renderAdmin(w, r, "user_edit", s.t("New user"), "users", userEditData{User: &store.User{Role: s.settings().DefaultRole}, New: true, Roles: assignableRoles})
}

func (s *Server) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	u := &store.User{Username: strings.ToLower(trimmed(r, "username")), Email: strings.ToLower(trimmed(r, "email")),
		DisplayName: trimmed(r, "display_name"), Role: r.FormValue("role")}
	fail := func(msg string) {
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "user_edit", s.t("New user"), "users", userEditData{User: u, New: true, Roles: assignableRoles, Error: msg})
	}
	if !auth.ValidRole(u.Role) {
		fail(s.t("Invalid request."))
		return
	}
	if tooLong(u.DisplayName, 100) {
		fail(s.t("The name is too long (100 characters max)."))
		return
	}
	if msg := s.validateNewAccount(u.Username, u.Email, r.FormValue("password"), r.FormValue("password_confirm")); msg != "" {
		fail(msg)
		return
	}
	hash, err := auth.HashPassword(r.FormValue("password"))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	id, err := s.store.CreateUser(r.Context(), store.NewUser{Username: u.Username, Email: u.Email, DisplayName: u.DisplayName, Role: u.Role, PasswordHash: hash})
	if errors.Is(err, store.ErrConflict) {
		fail(s.t("That username or e-mail address is already registered."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "user.created", u.Username, map[string]string{"role": u.Role})
	s.plugins.Emit("user.registered", map[string]any{"username": u.Username, "role": u.Role})
	s.redirect(w, r, fmt.Sprintf("/admin/users/%d", id), "success", s.t("User “%s” created.", u.Username))
}

func (s *Server) loadUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	u, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return u, true
}

// protectedAccount reports whether, on the portal, u is the account of a
// network admin and the signed-in user is not one: whoever takes over that
// account opens every site of the network as an operator, so only another
// network admin may change it.
func (s *Server) protectedAccount(r *http.Request, u *store.User) (bool, error) {
	if s.portal == nil {
		return false, nil
	}
	admin, err := s.portal.IsNetworkAdmin(r.Context(), u.ID)
	if err != nil || !admin {
		return false, err
	}
	actor, err := s.portal.IsNetworkAdmin(r.Context(), uid(r))
	return !actor, err
}

// refuseProtected answers 403 to a change of a protected account.
func (s *Server) refuseProtected(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	protected, err := s.protectedAccount(r, u)
	if err != nil {
		s.serverError(w, r, err)
		return true
	}
	if protected {
		s.audit(r, uid(r), "user.change_refused", u.Username, map[string]string{"reason": "network administrator", "path": r.URL.Path})
		s.adminError(w, r, http.StatusForbidden, s.t("Only a network administrator can change the account of a network administrator."))
	}
	return protected
}

func (s *Server) userEditData(r *http.Request, u *store.User, msg string) (userEditData, error) {
	d := userEditData{User: u, Roles: assignableRoles, Error: msg, Self: u.ID == uid(r)}
	var err error
	if d.Protected, err = s.protectedAccount(r, u); err != nil {
		return d, err
	}
	if d.Sessions, err = s.store.ListUserSessions(r.Context(), u.ID); err != nil {
		return d, err
	}
	users, _, err := s.store.ListUsers(r.Context(), store.UserListQuery{Limit: 500})
	if err != nil {
		return d, err
	}
	for _, o := range users {
		if o.ID != u.ID && auth.Can(o.Role, auth.CapEditOwnPosts) {
			d.Others = append(d.Others, o)
		}
	}
	return d, nil
}

func (s *Server) adminUserEdit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok {
		return
	}
	d, err := s.userEditData(r, u, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "user_edit", u.Name(), "users", d)
}

// wouldRemoveLastAdmin reports whether changing u leaves no active admin.
func (s *Server) wouldRemoveLastAdmin(r *http.Request, u *store.User, newRole, newStatus string) bool {
	if u.Role != auth.RoleAdmin || u.Status != "active" || (newRole == auth.RoleAdmin && newStatus == "active") {
		return false
	}
	n, err := s.store.CountActiveAdmins(r.Context())
	return err != nil || n <= 1
}

func (s *Server) adminUserUpdate(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok || s.refuseProtected(w, r, u) {
		return
	}
	edited := *u
	edited.Email = strings.ToLower(trimmed(r, "email"))
	edited.DisplayName, edited.Bio, edited.Website = trimmed(r, "display_name"), trimmed(r, "bio"), trimmed(r, "website")
	role, status := r.FormValue("role"), r.FormValue("status")
	if u.ID == uid(r) {
		role, status = u.Role, u.Status // nobody can demote or lock themselves out
	}
	if u.Linked() {
		// The network decides: the role of the membership, the account's
		// e-mail address and name, synced at every sign-in.
		role, status, edited.Email, edited.DisplayName = u.Role, u.Status, u.Email, u.DisplayName
	}
	fail := func(msg string) {
		d, err := s.userEditData(r, &edited, msg)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "user_edit", u.Name(), "users", d)
	}
	if !auth.ValidRole(role) || (status != "active" && status != "disabled") {
		fail(s.t("Invalid request."))
		return
	}
	if msg := s.profileError(edited.Email, edited.DisplayName, edited.Bio, edited.Website); msg != "" {
		fail(msg)
		return
	}
	if s.wouldRemoveLastAdmin(r, u, role, status) {
		fail(s.t("This is the last active administrator. Promote someone else first."))
		return
	}
	pw := r.FormValue("password")
	if pw != "" && u.SSOOnly() {
		fail(s.t("This user signs in with their sap13ns account and has no password here."))
		return
	}
	ctx := r.Context()
	if s.portal != nil && status == "disabled" && u.Status != "disabled" {
		if err := s.portal.CheckAccountChange(ctx, u.ID, false); errors.Is(err, ErrLastNetworkAdmin) {
			fail(s.t("This is the last network administrator. Make someone else a network administrator first."))
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	err := s.store.UpdateUserProfile(ctx, u.ID, store.UserProfile{Email: edited.Email, DisplayName: edited.DisplayName, Bio: edited.Bio, Website: edited.Website})
	if errors.Is(err, store.ErrConflict) {
		fail(s.t("That username or e-mail address is already registered."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if role != u.Role || status != u.Status {
		if err := s.store.UpdateUserRoleStatus(ctx, u.ID, role, status); err != nil {
			s.serverError(w, r, err)
			return
		}
		// A role change or suspension must take effect immediately.
		if err := s.store.DeleteUserSessions(ctx, u.ID, nil); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, uid(r), "user.role_changed", u.Username, map[string]string{"from": u.Role + "/" + u.Status, "to": role + "/" + status})
		if status != u.Status {
			// On the portal, the account's users on the network's sites
			// follow its status, applied once it changed here and its
			// sessions are closed (a sign-in redeemed meanwhile is checked
			// again, see confirmSSO).
			if err := s.accountChanged(r, u.ID, false); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
	}
	if pw != "" {
		if msg := s.checkNewPassword(u, pw, r.FormValue("password_confirm")); msg != "" {
			fail(msg)
			return
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if err := s.store.SetPassword(ctx, u.ID, hash); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.store.DeleteUserSessions(ctx, u.ID, s.ownSessionHash(r, u.ID))
		s.audit(r, uid(r), "user.password_set", u.Username, nil)
	}
	s.audit(r, uid(r), "user.updated", u.Username, nil)
	s.contentChanged()
	s.redirect(w, r, fmt.Sprintf("/admin/users/%d", u.ID), "success", s.t("Saved."))
}

func (s *Server) adminUserAction(w http.ResponseWriter, r *http.Request) {
	u, ok := s.loadUser(w, r)
	if !ok || s.refuseProtected(w, r, u) {
		return
	}
	ctx := r.Context()
	back := fmt.Sprintf("/admin/users/%d", u.ID)
	self := u.ID == uid(r)
	switch r.PathValue("action") {
	case "logout":
		if err := s.store.DeleteUserSessions(ctx, u.ID, s.ownSessionHash(r, u.ID)); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, uid(r), "user.sessions_revoked", u.Username, nil)
		s.redirect(w, r, back, "success", s.t("All sessions were closed."))
	case "reset-2fa":
		if err := s.store.DisableTOTP(ctx, u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		// Passkeys are second factors too: a user who lost the device that
		// holds them needs the same reset.
		n, err := s.store.DeleteUserPasskeys(ctx, u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, uid(r), "user.2fa_reset", u.Username, map[string]string{"passkeys_deleted": fmt.Sprint(n)})
		s.redirect(w, r, back, "success", s.t("Two-factor authentication was disabled for this user."))
	case "unlock":
		if err := s.store.RecordLoginSuccess(ctx, u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.loginUser.Reset(u.Username)
		s.audit(r, uid(r), "user.unlocked", u.Username, nil)
		s.redirect(w, r, back, "success", s.t("The account was unlocked."))
	case "delete":
		if self {
			s.redirect(w, r, back, "error", s.t("You cannot delete your own account."))
			return
		}
		if s.wouldRemoveLastAdmin(r, u, "", "") {
			s.redirect(w, r, back, "error", s.t("This is the last active administrator. Promote someone else first."))
			return
		}
		if s.portal != nil {
			switch err := s.portal.CheckAccountChange(ctx, u.ID, true); {
			case errors.Is(err, ErrLastNetworkAdmin):
				s.redirect(w, r, back, "error", s.t("This is the last network administrator. Make someone else a network administrator first."))
				return
			case errors.Is(err, ErrOwnsSites):
				s.redirect(w, r, back, "error", s.t("This account owns sites of the network, so it cannot be deleted. Disable it instead."))
				return
			case err != nil:
				s.serverError(w, r, err)
				return
			}
			// The account stops first, disabled and signed out, so nothing
			// signs in with it while its users on the sites are revoked;
			// then it goes, and its memberships with it. A failure leaves
			// it disabled.
			if err := s.store.UpdateUserRoleStatus(ctx, u.ID, u.Role, "disabled"); err != nil {
				s.serverError(w, r, err)
				return
			}
			if err := s.store.DeleteUserSessions(ctx, u.ID, nil); err != nil {
				s.serverError(w, r, err)
				return
			}
			if err := s.accountChanged(r, u.ID, true); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
		var reassign int64
		if to := formInt64Ptr(r, "reassign_to"); to != nil && *to != u.ID {
			if o, err := s.store.UserByID(ctx, *to); err == nil {
				reassign = o.ID
			}
		}
		if err := s.store.DeleteUser(ctx, u.ID, reassign); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, uid(r), "user.deleted", u.Username, map[string]string{"reassigned_to": fmt.Sprint(reassign)})
		s.contentChanged()
		s.redirect(w, r, "/admin/users", "success", s.t("User “%s” deleted.", u.Username))
	default:
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
	}
}

// ownSessionHash returns the current session hash when acting on oneself, so
// that "log out everywhere else" keeps the current browser signed in.
func (s *Server) ownSessionHash(r *http.Request, userID int64) []byte {
	if sess := rctx(r).Session; sess != nil && sess.UserID == userID {
		return sess.TokenHash
	}
	return nil
}

func (s *Server) profileError(email, name, bio, website string) string {
	switch {
	case !validEmail(email):
		return s.t("Enter a valid e-mail address.")
	case tooLong(name, 100):
		return s.t("The name is too long (100 characters max).")
	case tooLong(bio, 2000):
		return s.t("The biography is too long (2000 characters max).")
	case website != "" && (!validHTTPURL(website) || len(website) > 300):
		return s.t("The website must be a valid http(s) address.")
	}
	return ""
}

func validEmail(email string) bool {
	a, err := mail.ParseAddress(email)
	return err == nil && a.Address == email && len(email) <= 254
}

func (s *Server) checkNewPassword(u *store.User, pw, confirm string) string {
	if pw != confirm {
		return s.t("The passwords do not match.")
	}
	if err := auth.CheckPasswordPolicy(pw, u.Username, u.Email); err != nil {
		return s.passwordPolicyMessage(err)
	}
	return ""
}

// --------------------------------------------------------------- profile --

type profileData struct {
	User     *store.User
	Sessions []store.Session
	Current  []byte
	Error    string
	Tab      string
	Passkeys passkeySection
	CanLink  bool // a local user of a network's site: link to a network account
}

func (s *Server) renderProfile(w http.ResponseWriter, r *http.Request, status int, u *store.User, tab, msg string) {
	sessions, err := s.store.ListUserSessions(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var cur []byte
	if sess := rctx(r).Session; sess != nil {
		cur = sess.TokenHash
	}
	pks, err := s.passkeySection(r, u)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "profile", s.t("Your profile"), "profile", profileData{User: u, Sessions: sessions, Current: cur, Error: msg, Tab: tab, Passkeys: pks,
		CanLink: s.sso != nil && !u.Linked()})
}

func (s *Server) adminProfile(w http.ResponseWriter, r *http.Request) {
	s.renderProfile(w, r, http.StatusOK, currentUser(r), "profile", "")
}

func (s *Server) adminProfileUpdate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	edited := *u
	edited.Email = strings.ToLower(trimmed(r, "email"))
	edited.DisplayName, edited.Bio, edited.Website = trimmed(r, "display_name"), trimmed(r, "bio"), trimmed(r, "website")
	if u.Linked() {
		// They come from the network account, at every sign-in.
		edited.Email, edited.DisplayName = u.Email, u.DisplayName
	}
	if msg := s.profileError(edited.Email, edited.DisplayName, edited.Bio, edited.Website); msg != "" {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, &edited, "profile", msg)
		return
	}
	// Changing the e-mail address (used for password resets) needs the password.
	if edited.Email != u.Email && !auth.VerifyPassword(u.PasswordHash, r.FormValue("current_password")) {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, &edited, "profile", s.t("Enter your current password to change your e-mail address."))
		return
	}
	err := s.store.UpdateUserProfile(r.Context(), u.ID, store.UserProfile{Email: edited.Email, DisplayName: edited.DisplayName, Bio: edited.Bio, Website: edited.Website})
	if errors.Is(err, store.ErrConflict) {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, &edited, "profile", s.t("That username or e-mail address is already registered."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if edited.Email != u.Email {
		s.audit(r, u.ID, "profile.email_changed", u.Username, map[string]string{"from": u.Email, "to": edited.Email})
	}
	s.contentChanged()
	s.redirect(w, r, "/admin/profile", "success", s.t("Saved."))
}

func (s *Server) adminPasswordChange(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.SSOOnly() {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, u, "security", s.t("You sign in with your sap13ns account and have no password here."))
		return
	}
	if !s.loginUser.Allow("pw:" + u.Username) {
		s.renderProfile(w, r, http.StatusTooManyRequests, u, "security", s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if !auth.VerifyPassword(u.PasswordHash, r.FormValue("current_password")) {
		s.audit(r, u.ID, "profile.password_failed", u.Username, nil)
		s.renderProfile(w, r, http.StatusUnprocessableEntity, u, "security", s.t("The current password is not correct."))
		return
	}
	pw := r.FormValue("password")
	if msg := s.checkNewPassword(u, pw, r.FormValue("password_confirm")); msg != "" {
		s.renderProfile(w, r, http.StatusUnprocessableEntity, u, "security", msg)
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SetPassword(r.Context(), u.ID, hash); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.DeleteUserSessions(r.Context(), u.ID, s.ownSessionHash(r, u.ID))
	s.loginUser.Reset("pw:" + u.Username)
	s.audit(r, u.ID, "profile.password_changed", u.Username, nil)
	s.redirect(w, r, "/admin/profile#security", "success", s.t("Password changed. Other sessions were closed."))
}

func (s *Server) adminRevokeSessions(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if err := s.store.DeleteUserSessions(r.Context(), u.ID, s.ownSessionHash(r, u.ID)); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "profile.sessions_revoked", u.Username, nil)
	s.redirect(w, r, "/admin/profile#security", "success", s.t("All other sessions were closed."))
}

// ------------------------------------------------------------------- 2FA --

type twofaData struct {
	Enabled  bool
	Secret   string
	URI      string
	QR       template.URL // data: URI with a PNG, generated server-side
	Codes    []string
	Error    string
	Required bool
	Portal   bool // the network portal, where every account needs a second factor
	Passkeys passkeySection
}

// pendingSecret returns the not-yet-confirmed TOTP secret, creating one if needed.
func (s *Server) pendingSecret(r *http.Request, u *store.User) ([]byte, error) {
	if len(u.TOTPSecret) > 0 && !u.TOTPEnabled {
		if secret, err := s.keys.Decrypt(u.TOTPSecret, []byte(u.Username)); err == nil {
			return secret, nil
		}
	}
	secret := auth.NewTOTPSecret()
	enc, err := s.keys.Encrypt(secret, []byte(u.Username))
	if err != nil {
		return nil, err
	}
	if err := s.store.SetTOTP(r.Context(), u.ID, enc, false, nil); err != nil {
		return nil, err
	}
	u.TOTPSecret = enc
	return secret, nil
}

func (s *Server) render2FA(w http.ResponseWriter, r *http.Request, status int, d twofaData) {
	u := currentUser(r)
	d.Enabled = u.TOTPEnabled
	d.Required, d.Portal = s.mustEnroll2FA(r, u), s.portal != nil
	if pks, err := s.passkeySection(r, u); err == nil {
		d.Passkeys = pks
	}
	if !u.TOTPEnabled && d.Codes == nil {
		secret, err := s.pendingSecret(r, u)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Secret = auth.TOTPSecretString(secret)
		d.URI = auth.TOTPURI(s.settings().SiteTitle, u.Username, secret)
		if code, err := qr.Encode(d.URI, qr.M); err == nil {
			code.Scale = 6
			d.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG()))
		}
	}
	s.renderAdminStatus(w, r, status, "twofa", s.t("Two-factor authentication"), "profile", d)
}

func (s *Server) admin2FA(w http.ResponseWriter, r *http.Request) {
	s.render2FA(w, r, http.StatusOK, twofaData{})
}

func (s *Server) admin2FAEnable(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.TOTPEnabled && r.FormValue("regenerate") != "1" {
		s.redirect(w, r, "/admin/profile/2fa", "", "")
		return
	}
	if !s.mfaUser.Allow("enroll:" + u.Username) {
		s.render2FA(w, r, http.StatusTooManyRequests, twofaData{Error: s.t("Too many attempts. Wait a few minutes and try again.")})
		return
	}
	ctx := r.Context()
	if r.FormValue("regenerate") == "1" {
		// New recovery codes for an already enabled account.
		if !auth.VerifyPassword(u.PasswordHash, r.FormValue("current_password")) {
			s.render2FA(w, r, http.StatusUnprocessableEntity, twofaData{Error: s.t("The current password is not correct.")})
			return
		}
		codes, hashes := auth.NewRecoveryCodes(10)
		if err := s.store.SetTOTP(ctx, u.ID, u.TOTPSecret, true, hashes); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.audit(r, u.ID, "2fa.recovery_regenerated", u.Username, nil)
		s.render2FA(w, r, http.StatusOK, twofaData{Codes: codes})
		return
	}
	secret, err := s.keys.Decrypt(u.TOTPSecret, []byte(u.Username))
	step := int64(-1)
	if err == nil {
		step = auth.ValidateTOTP(secret, r.FormValue("code"), time.Now())
	}
	if step <= 0 {
		s.render2FA(w, r, http.StatusUnprocessableEntity, twofaData{Error: s.t("The code is not valid. Check the time on your phone and try again.")})
		return
	}
	codes, hashes := auth.NewRecoveryCodes(10)
	if err := s.store.SetTOTP(ctx, u.ID, u.TOTPSecret, true, hashes); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.UseTOTPStep(ctx, u.ID, step)
	s.mfaUser.Reset("enroll:" + u.Username)
	// Other sessions were authenticated without the second factor.
	s.store.DeleteUserSessions(ctx, u.ID, s.ownSessionHash(r, u.ID))
	s.audit(r, u.ID, "2fa.enabled", u.Username, nil)
	u.TOTPEnabled = true
	s.render2FA(w, r, http.StatusOK, twofaData{Codes: codes})
}

func (s *Server) admin2FADisable(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !auth.VerifyPassword(u.PasswordHash, r.FormValue("current_password")) {
		s.render2FA(w, r, http.StatusUnprocessableEntity, twofaData{Error: s.t("The current password is not correct.")})
		return
	}
	if err := s.store.DisableTOTP(r.Context(), u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "2fa.disabled", u.Username, nil)
	s.redirect(w, r, "/admin/profile#security", "warning", s.t("Two-factor authentication was disabled."))
}
