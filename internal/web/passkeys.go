package web

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// Passkeys (WebAuthn). The ceremony handlers that need go-webauthn live in
// passkeys_webauthn.go; this file holds what does not: the server-side
// ceremony store, cookies, the profile list and the rename/delete endpoints.

const (
	ceremonyTTL         = 5 * time.Minute  // a registration or sign-in must finish within this time
	ceremonyMax         = 4096             // pending ceremonies kept in memory
	passkeyReauthWindow = 10 * time.Minute // a login younger than this may add a passkey without the password
	passkeyNameMax      = 100              // matches the CHECK constraint on passkeys.name
	passkeysPerUser     = 20
)

// ------------------------------------------------------------- ceremonies --

// ceremonyStore keeps WebAuthn ceremony state (challenge, user, options) on
// the server, keyed by a random id that the browser only holds in an HttpOnly
// cookie. Entries are single use and expire; the map is bounded so that
// unauthenticated "begin" requests cannot grow it without limit.
type ceremonyStore[T any] struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	items   map[string]ceremonyEntry[T]
	lastGC  time.Time
	evicted int // for tests and diagnostics
}

type ceremonyEntry[T any] struct {
	value   T
	expires time.Time
}

func newCeremonyStore[T any](max int, ttl time.Duration) *ceremonyStore[T] {
	return &ceremonyStore[T]{max: max, ttl: ttl, now: time.Now, items: map[string]ceremonyEntry[T]{}}
}

// Put stores v under a new random id. When the store is full, expired
// entries are dropped first and then the oldest pending one, so a flood of
// requests can delay but never block legitimate ceremonies for long.
func (c *ceremonyStore[T]) Put(v T) string {
	id := auth.NewToken(32)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if len(c.items) >= c.max || now.Sub(c.lastGC) > c.ttl {
		c.purgeLocked(now)
	}
	for len(c.items) >= c.max {
		oldest, first := "", true
		var at time.Time
		for k, e := range c.items {
			if first || e.expires.Before(at) {
				oldest, at, first = k, e.expires, false
			}
		}
		delete(c.items, oldest)
		c.evicted++
	}
	c.items[id] = ceremonyEntry[T]{value: v, expires: now.Add(c.ttl)}
	return id
}

// Take removes the entry and returns it if it has not expired. A ceremony
// can therefore be finished at most once: replaying the finish request finds
// nothing.
func (c *ceremonyStore[T]) Take(id string) (T, bool) {
	var zero T
	if id == "" {
		return zero, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[id]
	if !ok {
		return zero, false
	}
	delete(c.items, id)
	if !c.now().Before(e.expires) {
		return zero, false
	}
	return e.value, true
}

func (c *ceremonyStore[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *ceremonyStore[T]) purgeLocked(now time.Time) {
	for k, e := range c.items {
		if !now.Before(e.expires) {
			delete(c.items, k)
		}
	}
	c.lastGC = now
}

// passkeyCeremony is the server-side half of a WebAuthn ceremony.
type passkeyCeremony struct {
	Kind        string // ceremonyRegister, ceremonyLogin or ceremonySecondFactor
	UserID      int64  // register and second factor: the account it belongs to
	SessionHash []byte // register and second factor: the session that started it
	Session     any    // *webauthn.SessionData
}

const (
	ceremonyRegister     = "register"
	ceremonyLogin        = "login"
	ceremonySecondFactor = "2fa"
)

// passkeyState is embedded in Server; its zero value is ready to use.
type passkeyState struct {
	once       sync.Once
	ceremonies *ceremonyStore[passkeyCeremony]
	begin      *auth.Limiter // begin requests per IP / user (bounds memory use)
}

func (s *Server) passkeys() *passkeyState {
	s.pk.once.Do(func() {
		s.pk.ceremonies = newCeremonyStore[passkeyCeremony](ceremonyMax, ceremonyTTL)
		s.pk.begin = auth.NewLimiter(200, 15*time.Minute)
	})
	return &s.pk
}

// The ceremony id travels in its own cookie: HttpOnly, SameSite=Strict and
// short-lived. "reg" is used on the profile, "login" on the sign-in pages.
func (s *Server) ceremonyCookieName(purpose string) string {
	if s.cfg.CookieSecure {
		return "__Host-sap13ns-wa-" + purpose
	}
	return "sap13ns_wa_" + purpose
}

func (s *Server) startCeremony(w http.ResponseWriter, purpose string, c passkeyCeremony) {
	id := s.passkeys().ceremonies.Put(c)
	http.SetCookie(w, &http.Cookie{Name: s.ceremonyCookieName(purpose), Value: id, Path: "/", MaxAge: int(ceremonyTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
}

// takeCeremony consumes the ceremony referenced by the cookie and clears it.
func (s *Server) takeCeremony(w http.ResponseWriter, r *http.Request, purpose string) (passkeyCeremony, bool) {
	name := s.ceremonyCookieName(purpose)
	ck, err := r.Cookie(name)
	if err != nil || len(ck.Value) > 100 {
		return passkeyCeremony{}, false
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	return s.passkeys().ceremonies.Take(ck.Value)
}

// passkeyUserHandle is the opaque WebAuthn user.id: an HMAC of the user ID
// under the instance secret. It is stable, reveals nothing about the account
// and needs no column; sign-in maps credentials back through credential_id.
func (s *Server) passkeyUserHandle(userID int64) []byte {
	return s.keys.SignBytes([]byte("sap13ns:passkey-user-handle\x00" + strconv.FormatInt(userID, 10)))
}

// recentLogin reports whether the current session was created a few minutes
// ago, so that sensitive changes do not need the password again.
func recentLogin(r *http.Request) bool {
	sess := rctx(r).Session
	return sess != nil && !sess.MFAPending && time.Since(sess.CreatedAt) < passkeyReauthWindow
}

// passkeyReauth asks for the current password unless the login is recent.
// It returns "" when the user may proceed, or a translated error.
func (s *Server) passkeyReauth(r *http.Request, u *store.User, password string) (int, string) {
	if recentLogin(r) {
		return 0, ""
	}
	if password == "" {
		return http.StatusUnauthorized, s.t("Enter your current password to continue.")
	}
	if !s.loginUser.Allow("pw:" + u.Username) {
		return http.StatusTooManyRequests, s.t("Too many attempts. Wait a few minutes and try again.")
	}
	if len(password) > auth.MaxPasswordLen || !auth.VerifyPassword(u.PasswordHash, password) {
		s.audit(r, u.ID, "passkey.reauth_failed", u.Username, nil)
		return http.StatusUnauthorized, s.t("The current password is not correct.")
	}
	s.loginUser.Reset("pw:" + u.Username)
	return 0, ""
}

// ------------------------------------------------------------------- JSON --

// readJSON decodes a small JSON request body. Requiring application/json
// also means a cross-site form cannot reach these endpoints without a CORS
// preflight (CrossOriginProtection blocks them anyway).
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "expected application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}

type passkeyReply struct {
	Error    string `json:"error,omitempty"`
	Reauth   bool   `json:"reauth,omitempty"`  // ask for the current password and retry
	Unknown  bool   `json:"unknown,omitempty"` // the credential is not (or no longer) registered here
	Redirect string `json:"redirect,omitempty"`
}

func passkeyFail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, passkeyReply{Error: msg})
}

// ---------------------------------------------------------------- profile --

type passkeyView struct {
	ID           int64
	Name         string
	CreatedAt    time.Time
	LastUsedAt   time.Time
	Used         bool
	Synced       bool // backed up to a passkey provider (iCloud, Google…)
	CloneWarning bool // the sign counter went backwards at some point
}

// passkeySection is the passkey part of the profile and 2FA pages.
type passkeySection struct {
	Available  bool // this build supports passkeys
	List       []passkeyView
	Recent     bool // a passkey can be added without the password
	LastFactor bool // deleting a passkey would leave the account without a second factor
	Max        bool
}

// credentialSummary reads the few fields of the stored webauthn.Credential
// JSON that the profile shows.
type credentialSummary struct {
	Flags struct {
		BackupEligible bool `json:"backupEligible"`
		BackupState    bool `json:"backupState"`
	} `json:"flags"`
	Authenticator struct {
		CloneWarning bool `json:"cloneWarning"`
	} `json:"authenticator"`
}

func (s *Server) passkeySection(r *http.Request, u *store.User) (passkeySection, error) {
	sec := passkeySection{Available: passkeysAvailable, Recent: recentLogin(r)}
	keys, err := s.store.ListPasskeys(r.Context(), u.ID)
	if err != nil {
		return sec, err
	}
	for _, k := range keys {
		v := passkeyView{ID: k.ID, Name: k.Name, CreatedAt: k.CreatedAt}
		if k.LastUsedAt != nil {
			v.LastUsedAt, v.Used = *k.LastUsedAt, true
		}
		var sum credentialSummary
		if json.Unmarshal(k.Data, &sum) == nil {
			v.Synced = sum.Flags.BackupState
			v.CloneWarning = sum.Authenticator.CloneWarning
		}
		if v.Name == "" {
			v.Name = s.t("Passkey")
		}
		sec.List = append(sec.List, v)
	}
	sec.LastFactor = !u.TOTPEnabled && len(keys) == 1
	sec.Max = len(keys) >= passkeysPerUser
	return sec, nil
}

func (s *Server) adminPasskeyRename(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	name := cleanPasskeyName(r.FormValue("name"))
	if name == "" {
		s.redirect(w, r, "/admin/profile#passkeys", "error", s.t("Enter a name for the passkey."))
		return
	}
	err := s.store.RenamePasskey(r.Context(), u.ID, id, name)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "passkey.renamed", u.Username, map[string]string{"id": strconv.FormatInt(id, 10), "name": name})
	s.redirect(w, r, "/admin/profile#passkeys", "success", s.t("Saved."))
}

func (s *Server) adminPasskeyDelete(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	// Removing the only second factor is like disabling two-factor
	// authentication: it needs the password unless the login is recent.
	lastFactor := !u.TOTPEnabled && u.PasskeyCount <= 1
	if lastFactor {
		if _, msg := s.passkeyReauth(r, u, r.FormValue("current_password")); msg != "" {
			s.redirect(w, r, "/admin/profile#passkeys", "error", msg)
			return
		}
	}
	k, err := s.store.DeletePasskey(r.Context(), u.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, "passkey.deleted", u.Username, map[string]string{"id": strconv.FormatInt(id, 10), "name": k.Name})
	u.PasskeyCount--
	if s.mustEnroll2FA(u) {
		s.redirect(w, r, "/admin/profile/2fa", "warning", s.t("Passkey deleted. Your role requires two-factor authentication: set up an authenticator app or add a new passkey."))
		return
	}
	s.redirect(w, r, "/admin/profile#passkeys", "success", s.t("Passkey deleted."))
}

// cleanPasskeyName normalises a user-supplied label.
func cleanPasskeyName(name string) string {
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u200b' || r == '\ufeff' {
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if utf8.RuneCountInString(name) > passkeyNameMax {
		name = string([]rune(name)[:passkeyNameMax])
	}
	return strings.TrimSpace(name)
}

// knownAuthenticators names a few common passkey providers by AAGUID (see
// github.com/passkeydeveloper/passkey-authenticator-aaguids). It is only used
// to suggest a default label.
var knownAuthenticators = map[string]string{
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	"adce0002-35bc-c60a-648b-0b25f1f05503": "Chrome on Mac",
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"dd4ec289-e01d-41c9-bb89-70fa845d4bf2": "iCloud Keychain",
	"08987058-cadc-4b81-b6e1-30de50dcbe96": "Windows Hello",
	"9ddd1817-af5a-4672-a2b9-3e3dd95000a9": "Windows Hello",
	"6028b017-b1d4-4c02-b4b3-afcdafc96bb2": "Windows Hello",
	"771b48fd-d3d4-4f74-9232-fc157ab0507a": "Edge on Mac",
	"bada5566-a7aa-401f-bd96-45619a55120d": "1Password",
	"d548826e-79b4-db40-a3d8-11116f7e8349": "Bitwarden",
	"531126d6-e717-415c-9320-3d9aa6981239": "Dashlane",
	"b84e4048-15dc-4dd0-8640-f4f60813c8af": "NordPass",
	"0ea242b4-43c4-4a1b-8b17-dd6d0b6baec6": "Keeper",
	"f3809540-7f14-49c1-a8b3-8f813b225541": "Enpass",
	"fdb141b2-5d84-443e-8a35-4698c205a502": "KeePassXC",
	"50726f74-6f6e-5061-7373-50726f746f6e": "Proton Pass",
	"53414d53-554e-4700-0000-000000000000": "Samsung Pass",
	"cb69481e-8ff7-4039-93ec-0a2729a154a8": "YubiKey 5",
	"ee882879-721c-4913-9775-3dfcce97072a": "YubiKey 5",
	"fa2b99dc-9e39-4257-8f92-4a30d23c4118": "YubiKey 5 NFC",
	"2fc0579f-8113-47ea-b116-bb5a8db9202a": "YubiKey 5 NFC",
}

// formatAAGUID renders 16 bytes as a lowercase UUID string ("" otherwise).
func formatAAGUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (s *Server) defaultPasskeyName(aaguid []byte) string {
	if name, ok := knownAuthenticators[formatAAGUID(aaguid)]; ok {
		return name
	}
	return s.t("Passkey")
}
