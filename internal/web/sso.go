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
//     first if needed, checks that the callback is exactly one of the
//     site's hosts, that the account is a member of the site (or a network
//     admin, an operator, while the site's owner allows support access, or
//     with an emergency access of their own), that the site is served (or,
//     for an emergency access only, suspended), and that the account has a
//     second factor. It stores a code (its hash, for 60 seconds, bound to
//     the site and the callback) and redirects to the callback with it.
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
	// ErrSSODenied when the account may not sign in to the site (any more):
	// ErrSSONoSupport for an operator once the site's support access is
	// closed, ErrSSONoEmergency for an emergency access that ended.
	RedeemSSO(ctx context.Context, code, verifier, redirectURI string) (*SSOGrant, error)
	// CheckSSO reads the access a redeemed grant gives now and returns its
	// current role: ErrSSODenied once the account lost its access to the
	// site, or for an operator grant once it is no longer one (and the
	// other way round), the support access closed or the emergency access
	// ended.
	CheckSSO(ctx context.Context, g *SSOGrant) (role string, err error)
	// OperatorAllowed reports whether an operator session of the account
	// may go on: while the site allows support access or, for an
	// emergency session, while the account's emergency access lasts. Every
	// request of such a session checks it. The answer may be a few
	// seconds old, but never past the end of the grant or the access.
	OperatorAllowed(ctx context.Context, accountID int64, emergency bool) (bool, error)
	// SupportStatus describes the support access of the site, and the
	// emergency accesses open now, for its Users page.
	SupportStatus(ctx context.Context) (*SupportStatus, error)
}

// SupportStatus is the support access of a tenant site: whether the
// network's admins may open it as operators now, and where its owner
// grants and revokes it, on the portal; and the emergency accesses that
// network admins opened without it.
type SupportStatus struct {
	Open        bool
	ExpiresAt   *time.Time // while open: nil until revoked
	GrantedBy   string     // the username of the account that allowed it
	GrantedAt   time.Time
	ManageURL   string
	Emergencies []EmergencyStatus // live now
}

// EmergencyStatus is an emergency access to a site, as its administrators
// see it.
type EmergencyStatus struct {
	By            string // the network admin's username
	Category      string // legal or security
	Justification string
	StartedAt     time.Time
	ExpiresAt     time.Time
}

// An SSOGrant is a redeemed code: who signs in, and with which role.
type SSOGrant struct {
	AccountID   int64
	Operator    bool   // a network admin who is not a member, or with an emergency access: their operator user
	Emergency   bool   // that emergency access (Operator too)
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
	// SSOAccess returns the access an account signs in to a site with: an
	// emergency access of its own that is live (on a suspended site too);
	// its membership's role; or admin as an operator for a network admin
	// who is not a member, while the site allows support access
	// (ErrSSONoSupport when it does not). ErrSSODenied otherwise. The
	// site's status is the caller's to check.
	SSOAccess(ctx context.Context, siteID, accountID int64) (SiteAccess, error)
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

	// The network admin area and "My sites" (admin_network.go).
	PortalAdmin
}

// SSOSite is a tenant site as /sso/authorize checks it.
type SSOSite struct {
	ID           int64
	Slug         string
	Status       string   // pending, active or suspended
	RedirectURIs []string // the callback on each of its routed hosts
}

// SiteAccess is the access an account signs in to a site with.
type SiteAccess struct {
	Role      string
	Operator  bool // a network admin who is not a member, or with an emergency access
	Emergency bool // that emergency access
}

// SSOCode is a code to issue.
type SSOCode struct {
	SiteID      int64
	AccountID   int64
	Challenge   []byte // sha256 of the site's verifier
	RedirectURI string
	Operator    bool
	Emergency   bool
}

// Errors of the network interfaces.
var (
	ErrSSOInvalid       = errors.New("sso: invalid, used or expired code")
	ErrSSODenied        = errors.New("sso: the account has no access to the site")
	ErrSSONoSupport     = fmt.Errorf("%w: the site does not allow support access", ErrSSODenied)
	ErrSSONoEmergency   = fmt.Errorf("%w: the emergency access ended", ErrSSODenied)
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
		s.renderAuth(w, r, status, "login", s.tu(r, "Log in"), &authForm{Error: msg, Next: "/admin/"})
	}
	if s.ssoFailures.Blocked(ip) {
		s.audit(r, 0, "sso.throttled", "", nil)
		s.renderAuth(w, r, http.StatusTooManyRequests, "login", s.tu(r, "Log in"), &authForm{Error: s.tu(r, "Too many attempts. Wait a few minutes and try again."), Next: "/admin/"})
		return
	}
	q := r.URL.Query()
	again := s.tu(r, "The sign-in with your sap13ns account could not be completed. Try again.")
	switch {
	case !ok:
		fail(http.StatusBadRequest, "no state", s.tu(r, "The sign-in expired or was started in another browser. Try again."))
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
	case errors.Is(err, ErrSSONoSupport):
		fail(http.StatusForbidden, "no support access", s.tu(r, "The owner of this site has not allowed support access."))
		return
	case errors.Is(err, ErrSSONoEmergency):
		fail(http.StatusForbidden, "emergency access ended", s.tu(r, "Your emergency access to this site has ended."))
		return
	case errors.Is(err, ErrSSODenied):
		fail(http.StatusForbidden, "denied", s.tu(r, "Your sap13ns account does not have access to this site."))
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
		s.renderAuth(w, r, http.StatusConflict, "login", s.tu(r, "Log in"), &authForm{Next: st.Next,
			Error: s.tu(r, "A user of this site already has the e-mail address of your sap13ns account. Log in as that user, then link your sap13ns account from your profile.")})
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.ssoFailures.Reset(ip)
	amr, method, action := store.AMRSSO, "sso", "login.success"
	switch {
	case g.Emergency:
		// Without the owner's consent: the session, and the site's audit
		// log, tell it apart from a consented operator's.
		amr, method, action = store.AMRSSOEmergency, "sso-emergency", "login.emergency"
	case g.Operator:
		amr, method, action = store.AMRSSOOperator, "sso-operator", "login.operator"
	}
	if err := s.newSession(w, r, u, false, amr); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.confirmSSO(ctx, u, g); errors.Is(err, errSSORoleChanged) {
		fail(http.StatusForbidden, "role changed during the sign-in", s.tu(r, "Your role on this site changed while you were signing in. Sign in again."))
		return
	} else if errors.Is(err, ErrSSONoSupport) {
		fail(http.StatusForbidden, "support access closed during the sign-in", s.tu(r, "The owner of this site has not allowed support access."))
		return
	} else if errors.Is(err, ErrSSONoEmergency) {
		fail(http.StatusForbidden, "emergency access ended during the sign-in", s.tu(r, "Your emergency access to this site has ended."))
		return
	} else if errors.Is(err, ErrSSODenied) {
		fail(http.StatusForbidden, "revoked during the sign-in", s.tu(r, "Your sap13ns account does not have access to this site."))
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

var (
	errSSOEmailTaken  = errors.New("sso: a local user has the account's e-mail address")
	errSSORoleChanged = fmt.Errorf("%w: the role changed during the sign-in", ErrSSODenied)
)

// confirmSSO checks a grant again once its user is synced and signed in.
// A change of the access (a membership removed or its role changed, an
// account disabled, an operator no longer a network admin) that the
// network applied between the redemption and the sync was undone by the
// sync, so the sign-in is refused here (ErrSSODenied). It fails closed on
// any mismatch or error: the user is disabled, with the lower of the two
// roles, and all its sessions are closed. It never re-enables the user:
// what the check read may already be stale, and a revocation running now
// must not be undone again (a linked local user would keep their password
// sign-in). The next sign-in through the portal restores the user with
// the access the network gives then, re-checked under its locks. A change
// after this check comes after the sync and applies itself.
func (s *Server) confirmSSO(ctx context.Context, u *store.User, g *SSOGrant) error {
	role, err := s.sso.CheckSSO(ctx, g)
	if err == nil && role == g.Role {
		return nil
	}
	keep := "" // the role the user has: a revocation keeps it too
	if err == nil {
		keep = lowerRole(g.Role, role)
		err = fmt.Errorf("%w (%s to %s)", errSSORoleChanged, g.Role, role)
	}
	if _, _, rerr := s.store.SetNetworkAccess(ctx, g.AccountID, g.Operator, keep, "disabled"); rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
		err = errors.Join(err, rerr)
	}
	if derr := s.store.DeleteUserSessions(ctx, u.ID, nil); derr != nil {
		err = errors.Join(err, derr)
	}
	return err
}

// operatorAllowed checks an operator session on each request: the site
// must still allow support access or, for an emergency session, the
// account's emergency access must still last. The network answers from a
// cache of a few seconds that never outlives the grant or the access, so
// a session ends at most that long after a revocation that did not reach
// the site, and when the grant or the access expires. Once it is closed,
// the sessions of that kind of the operator user end, and the user is
// disabled unless the other kind is still open; the next sign-in through
// the portal needs a new grant or emergency access. An error refuses the
// request (it is served signed out) without ending the session.
func (s *Server) operatorAllowed(w http.ResponseWriter, r *http.Request, u *store.User, sess *store.Session) bool {
	if s.sso == nil || !u.NetworkOperator || u.NetworkAccountID == nil {
		return false
	}
	ctx := r.Context()
	account, emergency := *u.NetworkAccountID, sess.AMR == store.AMRSSOEmergency
	ok, err := s.sso.OperatorAllowed(ctx, account, emergency)
	if err != nil {
		s.log.Error("check the operator access", "user", u.ID, "err", err)
		return false
	}
	if ok {
		return true
	}
	if _, err := s.store.DeleteNetworkSessions(ctx, account, true, sess.AMR); err != nil {
		s.log.Error("end an operator's sessions", "user", u.ID, "err", err)
	}
	if other, err := s.sso.OperatorAllowed(ctx, account, !emergency); err == nil && !other {
		if _, _, err := s.store.SetNetworkAccess(ctx, account, true, "", "disabled"); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Error("end an operator's access", "user", u.ID, "err", err)
		}
	}
	s.clearSessionCookie(w)
	action, reason := "support.session_ended", "support access closed"
	if emergency {
		action, reason = "emergency.session_ended", "emergency access ended"
	}
	s.audit(r, 0, action, u.Username, map[string]string{"network_account": strconv.FormatInt(account, 10), "reason": reason})
	return false
}

// lowerRole is the role of a and b that can do less; "" if either is not
// a role.
func lowerRole(a, b string) string {
	i, j := slices.Index(assignableRoles, a), slices.Index(assignableRoles, b)
	if i < 0 || j < 0 {
		return ""
	}
	return assignableRoles[min(i, j)]
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
	created := false
	if errors.Is(err, store.ErrNotFound) {
		created = true
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
	nu, err := s.store.UserByID(ctx, u.ID)
	if err == nil && (created || nu.Email != u.Email) {
		// A new user or a new address: the old address's Gravatar copy
		// goes (avatars.go), and the new one is looked up.
		if nu.Gravatar != u.Gravatar {
			s.media.RemoveAvatar(ctx, u.Gravatar)
		}
		s.gravatarSoon(u.ID)
	}
	return nu, err
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
		s.redirect(w, r, "/admin/profile", "info", s.tu(r, "Your user is already linked to a sap13ns account."))
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
			s.renderAuth(w, r, http.StatusForbidden, "login", s.tu(r, "Log in"), &authForm{Error: msg, Next: "/admin/profile"})
			return
		}
		s.redirect(w, r, "/admin/profile", "error", msg)
	}
	switch {
	case u == nil || u.ID != st.Link:
		refuse("signed out", s.tu(r, "Log in to this site again, then link your sap13ns account."))
		return
	case u.Linked():
		refuse("already linked", s.tu(r, "Your user is already linked to a sap13ns account."))
		return
	case g.Operator:
		refuse("operator", s.tu(r, "Only a member of this site can link their sap13ns account."))
		return
	}
	err := s.store.LinkNetworkAccount(ctx, u.ID, g.AccountID)
	if errors.Is(err, store.ErrConflict) {
		refuse("account linked to another user", s.tu(r, "Your sap13ns account is already linked to another user of this site."))
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
		s.renderAuth(w, r, http.StatusForbidden, "login", s.tu(r, "Log in"), &authForm{Error: s.tu(r, "Your sap13ns account does not have access to this site."), Next: "/admin/profile"})
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirect(w, r, "/admin/profile", "success", s.tu(r, "Your sap13ns account is linked. You can now log in with it."))
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
		s.ssoError(w, r, http.StatusForbidden, s.tu(r, "This sign-in link must be opened in the browser."))
		return
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && d != "document" {
		s.ssoError(w, r, http.StatusForbidden, s.tu(r, "This sign-in link must be opened in the browser."))
		return
	}
	q := r.URL.Query()
	siteID, err := strconv.ParseInt(q.Get("site"), 10, 64)
	challenge, cerr := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	redirect := q.Get("redirect_uri")
	invalid := s.tu(r, "This sign-in link is not valid. Start again from the site.")
	if err != nil || siteID < 1 || !ssoStateRe.MatchString(q.Get("state")) || cerr != nil || len(challenge) != sha256.Size ||
		(q.Get("code_challenge_method") != "" && q.Get("code_challenge_method") != "S256") || redirect == "" {
		s.ssoError(w, r, http.StatusBadRequest, invalid)
		return
	}
	ctx := r.Context()
	site, err := s.portal.SSOSite(ctx, siteID)
	if errors.Is(err, store.ErrNotFound) {
		s.ssoError(w, r, http.StatusNotFound, s.tu(r, "This site does not exist."))
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
	served := site.Status == "active" || site.Status == "pending"
	u := currentUser(r)
	if u == nil {
		// Signed in first even to a suspended site: a network admin's
		// emergency access opens it.
		next := url.QueryEscape(safeNext(r.URL.RequestURI()))
		if sess := rctx(r).Session; sess != nil && sess.MFAPending {
			http.Redirect(w, r, s.auth().TwoFactor+"?next="+next, http.StatusSeeOther)
			return
		}
		// The portal's own sign-in address, whatever it is: a sign-on
		// started from a site needs it, hidden administration or not.
		http.Redirect(w, r, s.auth().Login+"?next="+next, http.StatusSeeOther)
		return
	}
	acc, err := s.portal.SSOAccess(ctx, siteID, u.ID)
	if err != nil && !errors.Is(err, ErrSSODenied) {
		s.serverError(w, r, err)
		return
	}
	if !served && !(err == nil && acc.Emergency) {
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "site " + site.Status})
		s.ssoError(w, r, http.StatusForbidden, s.tu(r, "This site is suspended."))
		return
	}
	role, operator := acc.Role, acc.Operator
	if errors.Is(err, ErrSSONoSupport) {
		// A network admin who is not a member opens a site only with the
		// consent of its owner or one of its administrators.
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "no support access"})
		s.ssoError(w, r, http.StatusForbidden, s.tu(r, "The owner of this site has not allowed support access. Ask for it from the site's page in the network admin area."))
		return
	}
	if errors.Is(err, ErrSSODenied) {
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "no membership"})
		s.ssoError(w, r, http.StatusForbidden, s.tu(r, "Your sap13ns account does not have access to this site."))
		return
	}
	// The portal requires a second factor of every account
	// (mustEnroll2FA), and the site trusts it: a session that came
	// through the portal is not asked for the site's own.
	if !hasSecondFactor(u) && !(s.cfg.Dev && s.cfg.DevSkip2FA) {
		s.audit(r, u.ID, "sso.refused", site.Slug, map[string]string{"reason": "no second factor", "role": role})
		s.flash(w, "warning", s.tu(r, "Set up two-factor authentication to sign in to %s, then start again from the site.", site.Slug))
		http.Redirect(w, r, "/admin/profile/2fa", http.StatusSeeOther)
		return
	}
	code, err := s.portal.IssueSSOCode(ctx, SSOCode{SiteID: siteID, AccountID: u.ID, Challenge: challenge, RedirectURI: redirect,
		Operator: operator, Emergency: acc.Emergency})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	action := "sso.authorized"
	switch {
	case acc.Emergency:
		action = "sso.emergency" // a network admin's emergency access, without the owner's consent
	case operator:
		action = "sso.operator" // a network admin opening a site they are not a member of
	}
	s.audit(r, u.ID, action, site.Slug, map[string]string{"site": strconv.FormatInt(siteID, 10), "role": role})
	http.Redirect(w, r, redirect+"?"+url.Values{"code": {code}, "state": {q.Get("state")}}.Encode(), http.StatusSeeOther)
}

func (s *Server) ssoError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.renderAuth(w, r, status, "sso_error", s.tu(r, "Single sign-on"), &authForm{Error: msg})
}

// accountChanged applies a change of a portal account to its users on the
// network's sites; a no-op on any other server.
func (s *Server) accountChanged(r *http.Request, accountID int64, removed bool) error {
	if s.portal == nil {
		return nil
	}
	return s.portal.AccountChanged(r.Context(), accountID, removed)
}
