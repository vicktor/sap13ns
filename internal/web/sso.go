package web

// Single sign-on through the network portal (multisite): an authorization
// code exchange with PKCE between a site (any of its hosts H) and the
// portal (P), which cannot share cookies.
//
//  1. H /sso/start keeps a random state and verifier, sealed with the
//     site's key, in a host-only cookie (SameSite=Lax: it must come back on
//     the top-level redirect from P) and sends the browser to
//     P /sso/authorize with the state, sha256(verifier) and its callback.
//  2. P /sso/authorize, a top-level navigation only, signs the account in
//     first if needed, checks that the site is served, that the callback is
//     exactly one of the site's hosts, and that the account is a member of
//     the site (or a network admin: an operator), with a second factor for
//     staff roles. It stores a code (its hash, for 60 seconds, bound to the
//     site and the callback) and redirects to the callback with it.
//  3. H /sso/callback compares the state with the cookie, redeems the code
//     once with the verifier, maps the account to its user on the site
//     (created just in time, never matched by e-mail) and opens a session
//     whose amr says it came from the portal.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// SSOClient is what a tenant site of a network uses to sign users in
// through the portal: the NetworkHooks of a tenant implement it. A server
// without it (a single site, the portal) has no /sso routes and shows no
// button for them.
type SSOClient interface {
	// SSOAuthorize returns the portal's authorization endpoint and the id
	// of this site in the network.
	SSOAuthorize() (endpoint string, siteID int64)
	// RedeemSSO exchanges a code issued for this site and redirectURI,
	// once, given the verifier whose sha256 the code was issued for. It
	// returns ErrSSOInvalid for a code that is unknown, used, expired, for
	// another site or callback, or whose verifier does not match, and
	// ErrSSODenied when the account may not sign in to the site (any more).
	RedeemSSO(ctx context.Context, code, verifier, redirectURI string) (*SSOGrant, error)
	// CheckSSO tells whether a redeemed grant still holds: ErrSSODenied
	// once the account lost its access to the site.
	CheckSSO(ctx context.Context, g *SSOGrant) error
}

// An SSOGrant is a redeemed code: who signs in, and with which role.
type SSOGrant struct {
	AccountID   int64
	Operator    bool   // a network admin who is not a member: their operator user
	Role        string // the membership's role; admin for an operator
	Username    string // of the account
	Email       string
	DisplayName string
}

// Portal is what the network portal's server needs from the network: the
// NetworkHooks of the portal implement it.
type Portal interface {
	// SSOSite describes a tenant site for /sso/authorize: ErrNotFound when
	// it is unknown or not served (being created, deleted).
	SSOSite(ctx context.Context, siteID int64) (*SSOSite, error)
	// SSOAccess returns the role an account signs in to a site with: its
	// membership's, or admin with operator true for a network admin who is
	// not a member. ErrSSODenied otherwise.
	SSOAccess(ctx context.Context, siteID, accountID int64) (role string, operator bool, err error)
	// IssueSSOCode stores a single sign-on code (only its hash) and returns
	// it.
	IssueSSOCode(ctx context.Context, c SSOCode) (string, error)

	// IsNetworkAdmin reports whether an account administers the network.
	IsNetworkAdmin(ctx context.Context, accountID int64) (bool, error)
	// CheckAccountChange tells whether an account may be disabled, or
	// deleted when remove: ErrLastNetworkAdmin for the last network admin,
	// ErrOwnsSites for deleting the owner of a site.
	CheckAccountChange(ctx context.Context, accountID int64, remove bool) error
	// AccountChanged applies the status of an account (disabled, enabled)
	// to its users on every site, loaded or not; removed revokes them all
	// before the account is deleted.
	AccountChanged(ctx context.Context, accountID int64, removed bool) error
}

// SSOSite is a tenant site as /sso/authorize checks it.
type SSOSite struct {
	ID           int64
	Slug         string
	Status       string   // pending, active or suspended
	RedirectURIs []string // the callback on each of its routed hosts
}

// SSOCode is a code to issue.
type SSOCode struct {
	SiteID      int64
	AccountID   int64
	Challenge   []byte // sha256 of the site's verifier
	RedirectURI string
	Operator    bool
}

// Errors of the network interfaces.
var (
	ErrSSOInvalid       = errors.New("sso: invalid, used or expired code")
	ErrSSODenied        = errors.New("sso: the account has no access to the site")
	ErrLastNetworkAdmin = errors.New("network: the network needs at least one admin")
	ErrOwnsSites        = errors.New("network: the account owns sites")
)

const (
	ssoStateTTL  = 10 * time.Minute
	ssoMaxNext   = 1024
	ssoCallback  = "/sso/callback"
	ssoStateAAD  = "sso-state"
	operatorMail = "operator.invalid" // RFC 2606: never delivered
)

// ssoState is what the cookie of a sign-in in progress holds, sealed.
type ssoState struct {
	State    string `json:"s"`
	Verifier string `json:"v"`
	Next     string `json:"n"`
	Link     int64  `json:"l,omitempty"` // linking this local user to the account
	At       int64  `json:"t"`
}

func (s *Server) ssoCookieName() string {
	if s.cfg.CookieSecure {
		return "__Host-sap13ns-sso"
	}
	return "sap13ns_sso"
}

func (s *Server) ssoRedirectURI() string { return s.cfg.AbsURL(ssoCallback) }

// isStaff reports whether a role needs a second factor on the portal to
// sign in to a site: every role that can write.
func isStaff(role string) bool { return auth.Can(role, auth.CapEditOwnPosts) }

// ------------------------------------------------------------ site side --

func (s *Server) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.URL.Query().Get("next"))
	if currentUser(r) != nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.startSSO(w, r, next, 0)
}

// startSSO sends the browser to the portal; link is the local user to link
// to the account, 0 for a sign-in.
func (s *Server) startSSO(w http.ResponseWriter, r *http.Request, next string, link int64) {
	if len(next) > ssoMaxNext {
		next = "/admin/"
	}
	st := ssoState{State: auth.NewToken(24), Verifier: auth.NewToken(32), Next: next, Link: link, At: time.Now().Unix()}
	raw, err := json.Marshal(st)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	sealed, err := s.keys.Encrypt(raw, []byte(ssoStateAAD))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: s.ssoCookieName(), Value: base64.RawURLEncoding.EncodeToString(sealed), Path: "/",
		MaxAge: int(ssoStateTTL / time.Second), HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	endpoint, siteID := s.sso.SSOAuthorize()
	sum := sha256.Sum256([]byte(st.Verifier))
	q := url.Values{
		"site":                  {strconv.FormatInt(siteID, 10)},
		"state":                 {st.State},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {s.ssoRedirectURI()},
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, endpoint+"?"+q.Encode(), http.StatusSeeOther)
}

// takeSSOState reads the cookie of the sign-in in progress and deletes it:
// it serves one callback.
func (s *Server) takeSSOState(w http.ResponseWriter, r *http.Request) (*ssoState, bool) {
	ck, err := r.Cookie(s.ssoCookieName())
	if err != nil {
		return nil, false
	}
	http.SetCookie(w, &http.Cookie{Name: s.ssoCookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode})
	if len(ck.Value) > 4096 {
		return nil, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(ck.Value)
	if err != nil {
		return nil, false
	}
	raw, err := s.keys.Decrypt(sealed, []byte(ssoStateAAD))
	if err != nil {
		return nil, false
	}
	var st ssoState
	if json.Unmarshal(raw, &st) != nil || st.State == "" || st.Verifier == "" {
		return nil, false
	}
	if age := time.Since(time.Unix(st.At, 0)); age < 0 || age > ssoStateTTL {
		return nil, false
	}
	st.Next = safeNext(st.Next)
	return &st, true
}

func (s *Server) handleSSOCallback(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	// The URL holds the code: no other site may see it in a Referer.
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex")
	ip := ipKey(clientIP(r))
	st, ok := s.takeSSOState(w, r)
	fail := func(status int, reason, msg string) {
		s.ssoFailures.Allow(ip)
		s.audit(r, uid(r), "sso.failed", "", map[string]string{"reason": reason})
		s.renderAuth(w, r, status, "login", s.t("Log in"), &authForm{Error: msg, Next: "/admin/"})
	}
	if s.ssoFailures.Blocked(ip) {
		s.audit(r, 0, "sso.throttled", "", nil)
		s.renderAuth(w, r, http.StatusTooManyRequests, "login", s.t("Log in"), &authForm{Error: s.t("Too many attempts. Wait a few minutes and try again."), Next: "/admin/"})
		return
	}
	q := r.URL.Query()
	again := s.t("The sign-in with your sap13ns account could not be completed. Try again.")
	switch {
	case !ok:
		fail(http.StatusBadRequest, "no state", s.t("The sign-in expired or was started in another browser. Try again."))
		return
	case subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1:
		fail(http.StatusBadRequest, "state mismatch", again)
		return
	case q.Get("code") == "" || len(q.Get("code")) > 128:
		fail(http.StatusBadRequest, "no code", again)
		return
	}
	ctx := r.Context()
	g, err := s.sso.RedeemSSO(ctx, q.Get("code"), st.Verifier, s.ssoRedirectURI())
	switch {
	case errors.Is(err, ErrSSODenied):
		fail(http.StatusForbidden, "denied", s.t("Your sap13ns account does not have access to this site."))
		return
	case errors.Is(err, ErrSSOInvalid):
		fail(http.StatusBadRequest, "invalid code", again)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	if st.Link != 0 {
		s.finishSSOLink(w, r, st, g)
		return
	}
	u, err := s.ssoUser(ctx, g)
	if errors.Is(err, errSSOEmailTaken) {
		s.audit(r, 0, "sso.failed", g.Username, map[string]string{"reason": "e-mail address of a local user", "network_account": strconv.FormatInt(g.AccountID, 10)})
		s.renderAuth(w, r, http.StatusConflict, "login", s.t("Log in"), &authForm{Next: st.Next,
			Error: s.t("A user of this site already has the e-mail address of your sap13ns account. Log in as that user, then link your sap13ns account from your profile.")})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.ssoFailures.Reset(ip)
	amr, method, action := store.AMRSSO, "sso", "login.success"
	if g.Operator {
		amr, method, action = store.AMRSSOOperator, "sso-operator", "login.operator"
	}
	if err := s.newSession(w, r, u, false, amr); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.confirmSSO(ctx, u, g); errors.Is(err, ErrSSODenied) {
		fail(http.StatusForbidden, "revoked during the sign-in", s.t("Your sap13ns account does not have access to this site."))
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.store.RecordLoginSuccess(ctx, u.ID)
	s.audit(r, u.ID, action, u.Username, map[string]string{"method": method,
		"network_account": strconv.FormatInt(g.AccountID, 10), "account": g.Username})
	next := st.Next
	if u.Role == auth.RoleSubscriber && next == "/admin/" {
		next = "/admin/profile"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

var errSSOEmailTaken = errors.New("sso: a local user has the account's e-mail address")

// confirmSSO checks a grant again once its user is synced and signed in.
// A revocation (a membership removed, an account disabled) that ran
// between the redemption and the sync was undone by the sync, so then the
// user is disabled and signed out here; a revocation after this check
// comes after the sync and does it itself. When the check fails, it fails
// closed the same way: the next sign-in through the portal restores the
// user.
func (s *Server) confirmSSO(ctx context.Context, u *store.User, g *SSOGrant) error {
	err := s.sso.CheckSSO(ctx, g)
	if err == nil {
		return nil
	}
	if _, _, rerr := s.store.SetNetworkAccess(ctx, g.AccountID, g.Operator, "", "disabled"); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
		err = errors.Join(err, rerr)
	}
	if derr := s.store.DeleteUserSessions(ctx, u.ID, nil); derr != nil {
		err = errors.Join(err, derr)
	}
	return err
}

// ssoUser returns the user an account signs in as, created on its first
// sign-in, with the role, e-mail address and name the network gave. A
// member is never matched to a local user by e-mail address: a local user
// links their account explicitly from their profile.
func (s *Server) ssoUser(ctx context.Context, g *SSOGrant) (*store.User, error) {
	lookup := s.store.UserByNetworkAccount
	if g.Operator {
		lookup = s.store.OperatorUser
	}
	p := s.ssoProfile(g)
	u, err := lookup(ctx, g.AccountID)
	if errors.Is(err, store.ErrNotFound) {
		if u, err = s.createSSOUser(ctx, g, p); errors.Is(err, store.ErrConflict) {
			u, err = lookup(ctx, g.AccountID) // created meanwhile by a parallel sign-in
		}
	}
	if err != nil {
		return nil, err
	}
	if err := s.store.SyncNetworkUser(ctx, u.ID, p); errors.Is(err, store.ErrConflict) {
		p.Email = u.Email // another user took the new address: keep the old one
		err = s.store.SyncNetworkUser(ctx, u.ID, p)
	} else if err != nil {
		return nil, err
	}
	return s.store.UserByID(ctx, u.ID)
}

// ssoProfile is what a grant sets on its user. An operator user gets an
// address of its own that is never delivered, so it conflicts with no
// user, and a name that tells who it is.
func (s *Server) ssoProfile(g *SSOGrant) store.NetworkProfile {
	p := store.NetworkProfile{Email: strings.ToLower(g.Email), DisplayName: g.DisplayName, Role: g.Role}
	if g.Operator {
		name := cmp.Or(g.DisplayName, g.Username)
		p.Email = fmt.Sprintf("operator-%d@%s", g.AccountID, operatorMail)
		p.DisplayName = truncateRunes(s.t("%s (network operator)", name), 100)
		p.Role = auth.RoleAdmin
	}
	return p
}

// createSSOUser creates the user of an account on its first sign-in. The
// username is the account's, or with a suffix when taken. ErrConflict
// means another sign-in created it meanwhile.
func (s *Server) createSSOUser(ctx context.Context, g *SSOGrant, p store.NetworkProfile) (*store.User, error) {
	base := g.Username
	if g.Operator {
		base = "operator-" + g.Username
	}
	for i := range 12 {
		id, err := s.store.CreateSSOUser(ctx, store.NewSSOUser{NetworkAccountID: g.AccountID, Operator: g.Operator,
			Username: ssoUsername(base, i), Email: p.Email, DisplayName: p.DisplayName, Role: p.Role})
		switch {
		case err == nil:
			u, err := s.store.UserByID(ctx, id)
			if err == nil {
				s.plugins.Emit("user.registered", map[string]any{"username": u.Username, "role": u.Role})
			}
			return u, err
		case errors.Is(err, store.ErrConflict) && strings.Contains(err.Error(), "users_username_key"):
			continue
		case errors.Is(err, store.ErrConflict) && strings.Contains(err.Error(), "users_email_key"):
			return nil, errSSOEmailTaken
		default:
			return nil, err
		}
	}
	return nil, errors.New("sso: no free username for " + g.Username)
}

// ssoUsername is the i-th username to try for base: base, base-2 … base-9,
// then random suffixes, within the 32 characters a username has.
func ssoUsername(base string, i int) string {
	suffix := ""
	switch {
	case i == 0:
	case i < 9:
		suffix = "-" + strconv.Itoa(i+1)
	default:
		suffix = "-" + strings.ToLower(auth.NewToken(3)) // base64url: letters, digits, - and _
	}
	if len(base)+len(suffix) > 32 {
		base = base[:32-len(suffix)]
	}
	return base + suffix
}

// adminNetworkLink starts linking the signed-in local user to their
// network account.
func (s *Server) adminNetworkLink(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.Linked() {
		s.redirect(w, r, "/admin/profile", "info", s.t("Your user is already linked to a sap13ns account."))
		return
	}
	s.startSSO(w, r, "/admin/profile", u.ID)
}

// finishSSOLink links the local user who started it to the account that
// signed in on the portal. The network then decides the user's role.
func (s *Server) finishSSOLink(w http.ResponseWriter, r *http.Request, st *ssoState, g *SSOGrant) {
	ctx := r.Context()
	u := currentUser(r)
	refuse := func(reason, msg string) {
		s.audit(r, uid(r), "sso.link_refused", g.Username, map[string]string{"reason": reason, "network_account": strconv.FormatInt(g.AccountID, 10)})
		if u == nil {
			s.renderAuth(w, r, http.StatusForbidden, "login", s.t("Log in"), &authForm{Error: msg, Next: "/admin/profile"})
			return
		}
		s.redirect(w, r, "/admin/profile", "error", msg)
	}
	switch {
	case u == nil || u.ID != st.Link:
		refuse("signed out", s.t("Log in to this site again, then link your sap13ns account."))
		return
	case u.Linked():
		refuse("already linked", s.t("Your user is already linked to a sap13ns account."))
		return
	case g.Operator:
		refuse("operator", s.t("Only a member of this site can link their sap13ns account."))
		return
	}
	err := s.store.LinkNetworkAccount(ctx, u.ID, g.AccountID)
	if errors.Is(err, store.ErrConflict) {
		refuse("account linked to another user", s.t("Your sap13ns account is already linked to another user of this site."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := s.ssoProfile(g)
	if err := s.store.SyncNetworkUser(ctx, u.ID, p); errors.Is(err, store.ErrConflict) {
		p.Email = u.Email
		err = s.store.SyncNetworkUser(ctx, u.ID, p)
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "user.network_linked", u.Username, map[string]string{"network_account": strconv.FormatInt(g.AccountID, 10),
		"account": g.Username, "role": g.Role})
	// A fresh session: the link and a role change close the old ones.
	if err := s.newSession(w, r, u, false, store.AMRSSO); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.confirmSSO(ctx, u, g); errors.Is(err, ErrSSODenied) {
		s.audit(r, u.ID, "sso.failed", g.Username, map[string]string{"reason": "revoked during the sign-in", "network_account": strconv.FormatInt(g.AccountID, 10)})
		s.renderAuth(w, r, http.StatusForbidden, "login", s.t("Log in"), &authForm{Error: s.t("Your sap13ns account does not have access to this site."), Next: "/admin/profile"})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/profile", "success", s.t("Your sap13ns account is linked. You can now log in with it."))
}

// ---------------------------------------------------------- portal side --

var ssoStateRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Server) handleSSOAuthorize(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex")
	// Only a top-level navigation: never a fetch, a frame or an image,
	// which could read or replay the redirect. Browsers send Sec-Fetch-*
	// to every HTTPS origin; a portal on plain HTTP (development) gets none.
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "navigate" && (m != "" || s.cfg.CookieSecure) {
		s.ssoError(w, r, http.StatusForbidden, s.t("This sign-in link must be opened in the browser."))
		return
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && d != "document" {
		s.ssoError(w, r, http.StatusForbidden, s.t("This sign-in link must be opened in the browser."))
		return
	}
	q := r.URL.Query()
	siteID, err := strconv.ParseInt(q.Get("site"), 10, 64)
	challenge, cerr := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	redirect := q.Get("redirect_uri")
	invalid := s.t("This sign-in link is not valid. Start again from the site.")
	if err != nil || siteID < 1 || !ssoStateRe.MatchString(q.Get("state")) || cerr != nil || len(challenge) != sha256.Size ||
		(q.Get("code_challenge_method") != "" && q.Get("code_challenge_method") != "S256") || redirect == "" {
		s.ssoError(w, r, http.StatusBadRequest, invalid)
		return
	}
	ctx := r.Context()
	site, err := s.portal.SSOSite(ctx, siteID)
	if errors.Is(err, store.ErrNotFound) {
		s.ssoError(w, r, http.StatusNotFound, s.t("This site does not exist."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Exactly the callback on one of the site's hosts: never another
	// address, which would receive the code.
	if !slices.Contains(site.RedirectURIs, redirect) {
		s.log.Warn("sso: foreign redirect_uri", "site", siteID, "redirect_uri", truncateRunes(redirect, 200), "ip", clientIP(r))
		s.audit(r, uid(r), "sso.refused", site.Slug, map[string]string{"reason": "foreign redirect_uri"})
		s.ssoError(w, r, http.StatusBadRequest, invalid)
		return
	}
	if site.Status != "active" && site.Status != "pending" {
		s.audit(r, uid(r), "sso.refused", site.Slug, map[string]string{"reason": "site " + site.Status})
		s.ssoError(w, r, http.StatusForbidden, s.t("This site is suspended."))
		return
	}
	u := currentUser(r)
	if u == nil {
		next := url.QueryEscape(safeNext(r.URL.RequestURI()))
		if sess := rctx(r).Session; sess != nil && sess.MFAPending {
			http.Redirect(w, r, "/login/2fa?next="+next, http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login?next="+next, http.StatusSeeOther)
		return
	}
	role, operator, err := s.portal.SSOAccess(ctx, siteID, u.ID)
	if errors.Is(err, ErrSSODenied) {
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "no membership"})
		s.ssoError(w, r, http.StatusForbidden, s.t("Your sap13ns account does not have access to this site."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// The site trusts the portal's second factor (mustEnroll2FA): staff
	// and operators must have one here.
	if (operator || isStaff(role)) && !hasSecondFactor(u) && !(s.cfg.Dev && s.cfg.DevSkip2FA) {
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "no second factor", "role": role})
		s.flash(w, "warning", s.t("Set up two-factor authentication to sign in to %s with your role, then start again from the site.", site.Slug))
		http.Redirect(w, r, "/admin/profile/2fa", http.StatusSeeOther)
		return
	}
	code, err := s.portal.IssueSSOCode(ctx, SSOCode{SiteID: siteID, AccountID: u.ID, Challenge: challenge, RedirectURI: redirect, Operator: operator})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	action := "sso.authorized"
	if operator {
		action = "sso.operator" // a network admin opening a site they are not a member of
	}
	s.audit(r, u.ID, action, site.Slug, map[string]string{"site": strconv.FormatInt(siteID, 10), "role": role})
	http.Redirect(w, r, redirect+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusSeeOther)
}

func (s *Server) ssoError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.renderAuth(w, r, status, "sso_error", s.t("Single sign-on"), &authForm{Error: msg})
}

// accountChanged applies a change of a portal account to its users on the
// network's sites; a no-op on any other server.
func (s *Server) accountChanged(r *http.Request, accountID int64, removed bool) error {
	if s.portal == nil {
		return nil
	}
	return s.portal.AccountChanged(r.Context(), accountID, removed)
}
