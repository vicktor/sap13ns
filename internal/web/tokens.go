package web

// Personal access tokens (Profile → Access tokens for AI agents): a key
// for an AI agent to use the design tools at /admin/mcp (design_mcp.go)
// as the user. Only users who can change the appearance have them, never
// in a network admin's operator or emergency session, and never while the
// user still has to set up a required second factor. A token is shown
// once; the site keeps its SHA-256.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// tokenLifetimes are the expiry choices, in days (0: never).
var tokenLifetimes = []int{30, 90, 365, 0}

func (s *Server) canHaveTokens(r *http.Request, u *store.User) bool {
	if u == nil || u.NetworkOperator || !auth.Can(u.Role, auth.CapAppearance) || s.mustEnroll2FA(r, u) {
		return false
	}
	if sess := rctx(r).Session; sess != nil && (sess.AMR == store.AMRSSOOperator || sess.AMR == store.AMRSSOEmergency) {
		return false
	}
	return true
}

func (s *Server) adminTokenCreate(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !s.canHaveTokens(r, u) {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "You do not have permission to access this page."))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" || utf8.RuneCountInString(name) > 80 {
		s.redirect(w, r, "/admin/profile#tokens", "error", s.tu(r, "Give the token a name of up to 80 characters, such as the agent that will use it."))
		return
	}
	days, err := strconv.Atoi(r.FormValue("days"))
	valid := err == nil
	if valid {
		valid = false
		for _, d := range tokenLifetimes {
			valid = valid || d == days
		}
	}
	if !valid {
		days = 90
	}
	var expires *time.Time
	if days > 0 {
		t := time.Now().Add(time.Duration(days) * 24 * time.Hour)
		expires = &t
	}
	token := userTokenPrefix + auth.NewToken(32)
	t, err := s.store.CreateUserToken(r.Context(), u.ID, name, auth.HashToken(token), expires)
	if errors.Is(err, store.ErrTooManyTokens) {
		s.redirect(w, r, "/admin/profile#tokens", "error", s.tu(r, "You already have %d tokens. Revoke one first.", store.MaxUserTokens))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "profile.token_created", name, map[string]string{"id": strconv.FormatInt(t.ID, 10), "days": strconv.Itoa(days)})
	// Rendered, not redirected: the token must not travel in a flash.
	w.Header().Set("Cache-Control", "no-store")
	sessions, err := s.store.ListUserSessions(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pks, err := s.passkeySection(r, u)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var cur []byte
	if sess := rctx(r).Session; sess != nil {
		cur = sess.TokenHash
	}
	s.renderProfileData(w, r, http.StatusOK, profileData{User: u, Sessions: sessions, Current: cur, Tab: "profile", Passkeys: pks,
		CanLink: s.sso != nil && !u.Linked(), Langs: s.uiLanguages(""), Lang: s.userLang(u), SiteLang: langName(s.lang()), NewToken: token})
}

func (s *Server) adminTokenDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	if err := s.store.DeleteUserToken(r.Context(), u.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "profile.token_revoked", strconv.FormatInt(id, 10), nil)
	s.redirect(w, r, "/admin/profile#tokens", "success", s.tu(r, "Token revoked. Agents using it can no longer get in."))
}
