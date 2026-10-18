package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"sap13ns/internal/store"
)

// passkeysAvailable reports whether this build includes WebAuthn support.
const passkeysAvailable = true

// webAuthn returns the relying party configuration. The RP ID is the host of
// SAP13NS_BASE_URL and the only accepted origin is the base URL origin, so a
// passkey created for one site never works on another (or on a look-alike
// phishing domain). Built per request because the display name follows the
// site title setting; validation is cheap.
func (s *Server) webAuthn() (*webauthn.WebAuthn, error) {
	base := s.cfg.BaseURL
	name := strings.TrimSpace(s.settings().SiteTitle)
	if name == "" {
		name = "sap13ns"
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: ceremonyTTL, TimeoutUVD: ceremonyTTL}
	return webauthn.New(&webauthn.Config{
		RPID:                  base.Hostname(),
		RPDisplayName:         name,
		RPOrigins:             []string{base.Scheme + "://" + base.Host},
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
		// Some password managers add extension outputs nobody asked for; none
		// of them is used for any decision here.
		ExtensionsUnsolicitedOutputPolicy: protocol.UnsolicitedOutputPolicyIgnore,
	})
}

// passkeyUser adapts a store.User to webauthn.User.
type passkeyUser struct {
	user   *store.User
	handle []byte
	creds  []webauthn.Credential
	rows   []store.Passkey // parallel to creds
}

func (p *passkeyUser) WebAuthnID() []byte                         { return p.handle }
func (p *passkeyUser) WebAuthnName() string                       { return p.user.Username }
func (p *passkeyUser) WebAuthnDisplayName() string                { return p.user.Name() }
func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential { return p.creds }

func (p *passkeyUser) row(credentialID []byte) (*store.Passkey, *webauthn.Credential) {
	for i := range p.rows {
		if bytes.Equal(p.rows[i].CredentialID, credentialID) {
			return &p.rows[i], &p.creds[i]
		}
	}
	return nil, nil
}

func (s *Server) loadPasskeyUser(ctx context.Context, u *store.User) (*passkeyUser, error) {
	rows, err := s.store.ListPasskeys(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	pu := &passkeyUser{user: u, handle: s.passkeyUserHandle(u.ID)}
	for _, row := range rows {
		var c webauthn.Credential
		if err := json.Unmarshal(row.Data, &c); err != nil || !bytes.Equal(c.ID, row.CredentialID) {
			s.log.Error("unreadable passkey", "id", row.ID, "user", u.Username, "err", err)
			continue
		}
		pu.creds = append(pu.creds, c)
		pu.rows = append(pu.rows, row)
	}
	return pu, nil
}

// webauthnDetail extracts a short reason from a go-webauthn error for the
// audit log. The developer info (expected/received challenges and the like)
// only goes to the server log.
func (s *Server) webauthnDetail(err error) string {
	msg := err.Error()
	var pe *protocol.Error
	if errors.As(err, &pe) {
		msg = pe.Details
		if pe.DevInfo != "" {
			s.log.Info("webauthn ceremony rejected", "details", pe.Details, "info", pe.DevInfo)
		}
	}
	msg = strings.Join(strings.Fields(msg), " ")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

// ----------------------------------------------------------- registration --

// adminPasskeyBegin starts registering a passkey for the logged-in user. It
// needs a recent login or the current password: a stolen session must not be
// able to plant a permanent credential.
func (s *Server) adminPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, 8<<10, &req) {
		return
	}
	if !s.passkeys().begin.Allow("reg:" + strconv.FormatInt(u.ID, 10)) {
		passkeyFail(w, http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if status, msg := s.passkeyReauth(r, u, req.Password); msg != "" {
		writeJSON(w, status, passkeyReply{Error: msg, Reauth: status == http.StatusUnauthorized})
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.log.Error("webauthn config", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Passkeys are not available on this site address."))
		return
	}
	ctx := r.Context()
	pu, err := s.loadPasskeyUser(ctx, u)
	if err != nil {
		s.log.Error("load passkeys", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
		return
	}
	if len(pu.creds) >= passkeysPerUser {
		passkeyFail(w, http.StatusConflict, s.t("You have reached the maximum number of passkeys. Delete one first."))
		return
	}
	creation, session, err := wa.BeginRegistration(pu,
		webauthn.WithExclusions(webauthn.Credentials(pu.creds).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithConveyancePreference(protocol.PreferNoAttestation),
	)
	if err != nil {
		s.log.Error("begin passkey registration", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
		return
	}
	s.startCeremony(w, "reg", passkeyCeremony{Kind: ceremonyRegister, UserID: u.ID, SessionHash: rctx(r).Session.TokenHash, Session: session})
	writeJSON(w, http.StatusOK, creation)
}

func (s *Server) adminPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var req struct {
		Name       string          `json:"name"`
		Next       string          `json:"next"`
		Credential json.RawMessage `json:"credential"`
	}
	if !readJSON(w, r, 64<<10, &req) {
		return
	}
	c, ok := s.takeCeremony(w, r, "reg")
	session, _ := c.Session.(*webauthn.SessionData)
	if !ok || session == nil || c.Kind != ceremonyRegister || c.UserID != u.ID || !bytes.Equal(c.SessionHash, rctx(r).Session.TokenHash) {
		passkeyFail(w, http.StatusBadRequest, s.t("The request expired. Try again."))
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.log.Error("webauthn config", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Passkeys are not available on this site address."))
		return
	}
	ctx := r.Context()
	pu, err := s.loadPasskeyUser(ctx, u)
	if err != nil {
		s.log.Error("load passkeys", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	var cred *webauthn.Credential
	if err == nil {
		cred, err = wa.CreateCredential(pu, *session, parsed)
	}
	if err == nil && !cred.Flags.UserVerified {
		err = errors.New("user verification flag not set")
	}
	if err != nil {
		s.audit(r, u.ID, "passkey.register_failed", u.Username, map[string]string{"reason": s.webauthnDetail(err)})
		passkeyFail(w, http.StatusBadRequest, s.t("The passkey could not be verified. Try again."))
		return
	}
	data, err := json.Marshal(cred)
	if err != nil {
		s.log.Error("encode passkey", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
		return
	}
	name := cleanPasskeyName(req.Name)
	if name == "" {
		name = s.defaultPasskeyName(cred.Authenticator.AAGUID)
	}
	firstFactor := !u.TOTPEnabled && u.PasskeyCount == 0
	if _, err := s.store.AddPasskey(ctx, u.ID, cred.ID, name, data); err != nil {
		if errors.Is(err, store.ErrConflict) {
			passkeyFail(w, http.StatusConflict, s.t("This passkey is already registered."))
			return
		}
		s.log.Error("store passkey", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
		return
	}
	if firstFactor {
		// Like enabling TOTP: other sessions were opened with the password alone.
		s.store.DeleteUserSessions(ctx, u.ID, s.ownSessionHash(r, u.ID))
	}
	s.audit(r, u.ID, "passkey.added", u.Username, map[string]string{
		"name": name, "aaguid": formatAAGUID(cred.Authenticator.AAGUID), "synced": strconv.FormatBool(cred.Flags.BackupState),
	})
	next := "/admin/profile#passkeys"
	if req.Next != "" {
		next = safeNext(req.Next)
	}
	s.flash(w, "success", s.t("Passkey “%s” added.", name))
	writeJSON(w, http.StatusOK, passkeyReply{Redirect: next})
}

// ---------------------------------------------------------------- sign-in --

// loginPasskeyBegin starts a sign-in. Without a mode it is a discoverable
// ("usernameless") login, also used by the conditional-UI autofill on
// /login; mode "2fa" confirms a password login of an account that has
// passkeys, restricted to that account's credentials.
//
// Like POST /login these endpoints are protected against cross-site requests
// by http.CrossOriginProtection (Sec-Fetch-Site / Origin), and the ceremony
// cookie is SameSite=Strict.
func (s *Server) loginPasskeyBegin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, 4<<10, &req) {
		return
	}
	if !s.passkeys().begin.Allow("ip:" + ipKey(clientIP(r))) {
		passkeyFail(w, http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.log.Error("webauthn config", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Passkeys are not available on this site address."))
		return
	}
	var (
		assertion *protocol.CredentialAssertion
		session   *webauthn.SessionData
		c         passkeyCeremony
	)
	if req.Mode == ceremonySecondFactor {
		u, ok := s.pendingMFA(r)
		if !ok || u.PasskeyCount == 0 {
			writeJSON(w, http.StatusUnauthorized, passkeyReply{Error: s.t("Your session expired. Log in again."), Redirect: "/login"})
			return
		}
		if !validCSRF(r) {
			passkeyFail(w, http.StatusForbidden, s.t("Your session form expired. Go back, reload the page and try again."))
			return
		}
		pu, err := s.loadPasskeyUser(r.Context(), u)
		if err != nil {
			s.log.Error("load passkeys", "err", err)
			passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
			return
		}
		if assertion, session, err = wa.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationRequired)); err != nil {
			s.log.Error("begin passkey login", "err", err)
			passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
			return
		}
		c = passkeyCeremony{Kind: ceremonySecondFactor, UserID: u.ID, SessionHash: rctx(r).Session.TokenHash}
	} else {
		if assertion, session, err = wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired)); err != nil {
			s.log.Error("begin passkey login", "err", err)
			passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
			return
		}
		c = passkeyCeremony{Kind: ceremonyLogin}
	}
	c.Session = session
	s.startCeremony(w, "login", c)
	writeJSON(w, http.StatusOK, assertion)
}

func (s *Server) loginPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Next       string          `json:"next"`
		Credential json.RawMessage `json:"credential"`
	}
	if !readJSON(w, r, 64<<10, &req) {
		return
	}
	ip := ipKey(clientIP(r))
	c, ok := s.takeCeremony(w, r, "login")
	session, _ := c.Session.(*webauthn.SessionData)
	if !s.loginIP.Allow(ip) {
		s.audit(r, 0, "login.throttled", "", map[string]string{"method": "passkey"})
		passkeyFail(w, http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if !ok || session == nil {
		passkeyFail(w, http.StatusBadRequest, s.t("The request expired. Try again."))
		return
	}
	secondFactor := c.Kind == ceremonySecondFactor
	if secondFactor {
		sess := rctx(r).Session
		if sess == nil || !sess.MFAPending || sess.UserID != c.UserID || !bytes.Equal(sess.TokenHash, c.SessionHash) {
			writeJSON(w, http.StatusUnauthorized, passkeyReply{Error: s.t("Your session expired. Log in again."), Redirect: "/login"})
			return
		}
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		s.audit(r, 0, "login.failed", "", map[string]string{"method": "passkey", "reason": s.webauthnDetail(err)})
		passkeyFail(w, http.StatusBadRequest, s.t("The passkey could not be verified. Try again."))
		return
	}
	ctx := r.Context()
	key, err := s.store.PasskeyByCredentialID(ctx, parsed.RawID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && secondFactor && key.UserID != c.UserID) {
		s.audit(r, 0, "login.failed", "", map[string]string{"method": "passkey", "reason": "unknown passkey"})
		writeJSON(w, http.StatusUnauthorized, passkeyReply{Unknown: true,
			Error: s.t("This passkey is not registered on this site. Sign in with your password and add it in your profile.")})
		return
	}
	if err != nil {
		s.serverErrorJSON(w, r, err)
		return
	}
	u, err := s.store.UserByID(ctx, key.UserID)
	if err != nil {
		s.serverErrorJSON(w, r, err)
		return
	}
	// The same gates as the password login.
	throttled := s.loginUser.Blocked(u.Username) || s.loginUser.Blocked(strings.ToLower(u.Email))
	if secondFactor {
		throttled = !s.mfaUser.Allow(u.Username)
	}
	if throttled {
		s.audit(r, u.ID, "login.throttled", u.Username, map[string]string{"method": "passkey"})
		passkeyFail(w, http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		s.audit(r, u.ID, "login.locked", u.Username, map[string]string{"method": "passkey"})
		passkeyFail(w, http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again."))
		return
	}
	if u.Status != "active" {
		s.audit(r, u.ID, "login.failed", u.Username, map[string]string{"method": "passkey", "reason": "disabled"})
		passkeyFail(w, http.StatusUnauthorized, s.t("This account cannot sign in."))
		return
	}
	wa, err := s.webAuthn()
	if err != nil {
		s.log.Error("webauthn config", "err", err)
		passkeyFail(w, http.StatusInternalServerError, s.t("Passkeys are not available on this site address."))
		return
	}
	pu, err := s.loadPasskeyUser(ctx, u)
	if err != nil {
		s.serverErrorJSON(w, r, err)
		return
	}
	row, stored := pu.row(parsed.RawID)
	if row == nil {
		s.serverErrorJSON(w, r, errors.New("passkey row vanished or unreadable"))
		return
	}
	storedCount := stored.Authenticator.SignCount
	var cred *webauthn.Credential
	if secondFactor {
		cred, err = wa.ValidateLogin(pu, *session, parsed)
	} else {
		// The user handle in the response must equal the HMAC handle of the
		// credential's owner; go-webauthn checks it against WebAuthnID().
		_, cred, err = wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) { return pu, nil }, *session, parsed)
	}
	if err == nil && !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		err = errors.New("user verification flag not set")
	}
	if err != nil {
		if !secondFactor {
			s.loginUser.Allow(u.Username)
		}
		s.store.RecordLoginFailure(ctx, u.ID, 10, 15*time.Minute)
		s.audit(r, u.ID, "login.failed", u.Username, map[string]string{"method": "passkey", "reason": s.webauthnDetail(err)})
		passkeyFail(w, http.StatusUnauthorized, s.t("The passkey could not be verified. Try again."))
		return
	}
	// A counter that does not increase hints at a cloned authenticator. Many
	// passkey providers always report 0, which is fine; nothing is blocked,
	// but the event is recorded for the administrators.
	if got := parsed.Response.AuthenticatorData.Counter; (got != 0 || storedCount != 0) && got <= storedCount {
		s.audit(r, u.ID, "passkey.clone_warning", u.Username, map[string]string{
			"name": row.Name, "stored_count": strconv.FormatUint(uint64(storedCount), 10), "received_count": strconv.FormatUint(uint64(got), 10),
		})
		s.log.Warn("passkey sign counter did not increase", "user", u.Username, "passkey", row.ID, "stored", storedCount, "received", got)
	}
	if data, err := json.Marshal(cred); err == nil {
		if err := s.store.RecordPasskeyUse(ctx, row.ID, data); err != nil {
			s.log.Error("record passkey use", "err", err)
		}
	}
	method := "passkey"
	if secondFactor {
		s.mfaUser.Reset(u.Username)
		method = "password+passkey"
	} else {
		s.loginUser.Reset(u.Username)
		s.loginUser.Reset(strings.ToLower(u.Email))
	}
	next, err := s.establishLogin(w, r, u, safeNext(req.Next), method)
	if err != nil {
		s.serverErrorJSON(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, passkeyReply{Redirect: next})
}

func (s *Server) serverErrorJSON(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "path", r.URL.Path, "err", err)
	passkeyFail(w, http.StatusInternalServerError, s.t("Something went wrong. The error has been logged."))
}
