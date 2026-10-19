package plugin

// OAuth connections (docs/PLUGINS.md, OAuth connections). A plugin has no
// routes, so it cannot receive an authorization code: it declares its
// providers in plugin.json and the core runs the authorization-code flow
// with the administrator, keeps the tokens sealed with the site's key, and
// renews them. The plugin only ever gets a valid access token, through the
// oauth_token host function and the "oauth:<provider>" permission; never
// the refresh token or the client secret.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/tetratelabs/wazero/api"

	"sap13ns/internal/store"
)

// PermOAuth prefixes the permission to use a declared OAuth provider:
// "oauth:linkedin".
const PermOAuth = "oauth:"

// OAuthDecl declares an OAuth 2.0 provider (authorization code grant,
// RFC 6749 §4.1) the administrator connects the plugin to.
type OAuthDecl struct {
	ID           string   `json:"id"`            // ^[a-z][a-z0-9_-]{0,39}$; the permission is oauth:<id>
	Name         string   `json:"name"`          // shown on the plugin's page: "LinkedIn"
	AuthorizeURL string   `json:"authorize_url"` // https
	TokenURL     string   `json:"token_url"`     // https
	Scopes       []string `json:"scopes"`
	// ScopeSeparator joins the scopes: " " (RFC 6749, the default) or ",".
	ScopeSeparator string `json:"scope_separator"`
	// PKCE adds a S256 code challenge (RFC 7636).
	PKCE bool `json:"pkce"`
	// TokenAuth is how the client authenticates at the token endpoint:
	// "body" (client_id and client_secret in the form, the default) or
	// "basic" (HTTP Basic, RFC 6749 §2.3.1).
	TokenAuth string `json:"token_auth"`
	// The settings that hold the client ID (text or secret) and the
	// client secret (secret). Without a secret the client is public and
	// needs PKCE.
	ClientIDSetting     string `json:"client_id_setting"`
	ClientSecretSetting string `json:"client_secret_setting"`
	// UserinfoURL, optional, names the connected account: a GET with the
	// access token whose JSON gives an id (sub, user_id or id) and a name
	// (name, username or preferred_username). UserinfoToken "query" sends
	// the token as access_token in the query instead of a Bearer header.
	UserinfoURL   string `json:"userinfo_url"`
	UserinfoToken string `json:"userinfo_token"`
	// Exchange, optional, trades the token of the code exchange for a
	// long-lived one (Meta): GET url?grant_type=…&client_secret=…&access_token=….
	Exchange *OAuthEndpoint `json:"exchange"`
	// Refresh, optional, changes how tokens are renewed. Without it, a
	// refresh token renews them at the token URL (RFC 6749 §6).
	Refresh *OAuthEndpoint `json:"refresh"`
}

// OAuthEndpoint is a non-standard token request of a provider.
type OAuthEndpoint struct {
	URL       string `json:"url"`
	GrantType string `json:"grant_type"`
	// Token is what a refresh sends: "refresh_token" (a POST with the
	// client's credentials, RFC 6749 §6) or "access_token" (Meta: a GET
	// with the current access token, no client secret).
	Token string `json:"token"`
}

// Limits of the declarations and of the token responses.
const (
	maxOAuthProviders = 3
	maxOAuthScopes    = 30
	maxOAuthURL       = 500
	maxOAuthToken     = 8 << 10
	maxOAuthBody      = 64 << 10
	oauthStateTTL     = 10 * time.Minute
	oauthTimeout      = 10 * time.Second
	// oauthSoon is how long before the end of a connection the
	// administrator is warned.
	oauthSoon = 7 * 24 * time.Hour
	// oauthRetry spaces out renewals after one failed.
	oauthRetry = 15 * time.Minute
	// maxOAuthPerCall is the oauth_token calls one request may make.
	maxOAuthPerCall = 4
)

var (
	oauthHostRe  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
	oauthGrantRe = regexp.MustCompile(`^[a-z][a-z0-9_:.-]{0,79}$`)
	oauthScopeRe = regexp.MustCompile(`^[\x21-\x7e]{1,100}$`)
)

// checkOAuthURL checks a URL of a provider declaration: https, a plain
// host name, no credentials, port or fragment.
func checkOAuthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > maxOAuthURL || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" ||
		u.Opaque != "" || !oauthHostRe.MatchString(u.Host) {
		return nil, fmt.Errorf("%q must be an https:// URL on a host name, without a port, credentials or fragment", raw)
	}
	return u, nil
}

func (m *Manifest) validateOAuth() []error {
	var errs []error
	if len(m.OAuth) > maxOAuthProviders {
		errs = append(errs, fmt.Errorf("at most %d oauth providers", maxOAuthProviders))
	}
	settings := map[string]SettingDecl{}
	for _, s := range m.Settings {
		settings[s.Key] = s
	}
	ids := map[string]bool{}
	for _, d := range m.OAuth {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("oauth %q: "+format, append([]any{d.ID}, args...)...))
		}
		if !nameRe.MatchString(d.ID) || ids[d.ID] {
			errs = append(errs, fmt.Errorf("invalid or duplicate oauth provider id %q", d.ID))
			continue
		}
		ids[d.ID] = true
		if strings.TrimSpace(d.Name) == "" || len(d.Name) > 60 || strings.ContainsFunc(d.Name, unicode.IsControl) {
			bad("name is required (at most 60 characters)")
		}
		urls := []string{d.AuthorizeURL, d.TokenURL}
		if d.UserinfoURL != "" {
			urls = append(urls, d.UserinfoURL)
		}
		for _, e := range []*OAuthEndpoint{d.Exchange, d.Refresh} {
			if e != nil && e.URL != "" {
				urls = append(urls, e.URL)
			}
		}
		for _, raw := range urls {
			u, err := checkOAuthURL(raw)
			if err != nil {
				bad("%v", err)
				continue
			}
			// The hosts the core contacts for the plugin are approved like
			// the plugin's own: they must be among its http: permissions.
			if !hostAllowed(m.Permissions, u.Hostname()) {
				bad("the host %s must be in an http: permission", u.Hostname())
			}
		}
		switch d.ScopeSeparator {
		case "", " ", ",":
		default:
			bad(`scope_separator must be " " or ","`)
		}
		if len(d.Scopes) > maxOAuthScopes {
			bad("at most %d scopes", maxOAuthScopes)
		}
		for _, sc := range d.Scopes {
			if !oauthScopeRe.MatchString(sc) || d.ScopeSeparator == "," && strings.Contains(sc, ",") {
				bad("invalid scope %q", sc)
			}
		}
		switch d.TokenAuth {
		case "", "body", "basic":
		default:
			bad(`token_auth must be "body" or "basic"`)
		}
		switch d.UserinfoToken {
		case "", "header", "query":
		default:
			bad(`userinfo_token must be "header" or "query"`)
		}
		if s, ok := settings[d.ClientIDSetting]; !ok || s.Type != "text" && s.Type != "secret" {
			bad("client_id_setting must name a text or secret setting")
		}
		switch s, ok := settings[d.ClientSecretSetting]; {
		case d.ClientSecretSetting == "" && !d.PKCE:
			bad("client_secret_setting is required unless the provider uses PKCE")
		case d.ClientSecretSetting != "" && (!ok || s.Type != "secret"):
			bad("client_secret_setting must name a secret setting")
		case d.ClientSecretSetting != "" && d.ClientSecretSetting == d.ClientIDSetting:
			bad("the client ID and the client secret need two settings")
		}
		if e := d.Exchange; e != nil && (e.URL == "" || !oauthGrantRe.MatchString(e.GrantType) || e.Token != "") {
			bad("exchange needs a url and a grant_type")
		}
		if e := d.Refresh; e != nil {
			if e.GrantType != "" && !oauthGrantRe.MatchString(e.GrantType) {
				bad("invalid refresh grant_type")
			}
			switch e.Token {
			case "", "refresh_token":
			case "access_token":
				if e.URL == "" || e.GrantType == "" {
					bad("a refresh with the access token needs a url and a grant_type")
				}
			default:
				bad(`refresh token must be "refresh_token" or "access_token"`)
			}
		}
		if d.Exchange != nil && d.ClientSecretSetting == "" {
			bad("exchange needs the client secret")
		}
		if !slices.Contains(m.Permissions, PermOAuth+d.ID) {
			bad("needs the %q permission", PermOAuth+d.ID)
		}
	}
	for _, p := range m.Permissions {
		if id, ok := strings.CutPrefix(p, PermOAuth); ok && !ids[id] {
			errs = append(errs, fmt.Errorf("permission %q: no oauth provider %q is declared", p, id))
		}
	}
	return errs
}

// OAuthProvider returns the declared provider with that id.
func (m *Manifest) OAuthProvider(id string) *OAuthDecl {
	for i := range m.OAuth {
		if m.OAuth[i].ID == id {
			return &m.OAuth[i]
		}
	}
	return nil
}

// oauthCapabilities pin each provider's declaration: changing its URLs,
// scopes or settings needs a new approval.
func (m *Manifest) oauthCapabilities() []string {
	var out []string
	for _, d := range m.OAuth {
		raw, _ := json.Marshal(d)
		sum := sha256.Sum256(raw)
		out = append(out, "oauth-provider:"+d.ID+":"+hex.EncodeToString(sum[:])[:16])
	}
	return out
}

// Separator returns the scope separator.
func (d *OAuthDecl) Separator() string { return cmpOr(d.ScopeSeparator, " ") }

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AuthorizeOrigin is the origin the administrator's browser is sent to.
func (d *OAuthDecl) AuthorizeOrigin() string {
	u, err := url.Parse(d.AuthorizeURL)
	if err != nil {
		return ""
	}
	return "https://" + u.Host
}

// ------------------------------------------------------------- state --

// OAuthConnection is what the admin may show of a connection: never a
// token.
type OAuthConnection struct {
	Provider         string
	AccountID        string
	AccountName      string
	Scopes           []string
	ExpiresAt        *time.Time
	RefreshExpiresAt *time.Time
	Refreshable      bool
	ConnectedAt      time.Time
	ConnectedBy      string
	RefreshedAt      *time.Time
	RefreshError     string
}

// Expired reports whether the access token can no longer be used.
func (c *OAuthConnection) Expired(now time.Time) bool {
	return c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) && !c.Refreshable ||
		c.RefreshExpiresAt != nil && !now.Before(*c.RefreshExpiresAt)
}

// Ending returns when the connection stops working without a new
// authorization, if that is within a week: a token that cannot be renewed
// (or whose renewal failed) ends with it, a renewable one with its refresh
// token.
func (c *OAuthConnection) Ending(now time.Time) (time.Time, bool) {
	soon := func(t *time.Time) bool { return t != nil && t.Before(now.Add(oauthSoon)) }
	switch {
	case soon(c.RefreshExpiresAt):
		return *c.RefreshExpiresAt, true
	case soon(c.ExpiresAt) && (!c.Refreshable || c.RefreshError != ""):
		return *c.ExpiresAt, true
	}
	return time.Time{}, false
}

func connectionOf(c *store.PluginOAuth) *OAuthConnection {
	out := &OAuthConnection{Provider: c.Provider, AccountID: c.AccountID, AccountName: c.AccountName, ExpiresAt: c.ExpiresAt,
		RefreshExpiresAt: c.RefreshExpiresAt, Refreshable: c.Refreshable, ConnectedAt: c.ConnectedAt, ConnectedBy: c.ConnectedByName,
		RefreshedAt: c.RefreshedAt, RefreshError: c.RefreshError}
	if c.Scopes != "" {
		out.Scopes = strings.Fields(c.Scopes)
	}
	return out
}

// ------------------------------------------------------------ errors --

var (
	// ErrOAuthUnavailable: the plugin is not enabled with the provider's
	// permission, or it declares no such provider.
	ErrOAuthUnavailable = errors.New("enable the plugin with this connection's permission first")
	// ErrOAuthNotConfigured: the client ID or secret settings are empty.
	ErrOAuthNotConfigured = errors.New("enter the client ID and the client secret in the plugin's settings first")
	// ErrOAuthState: the callback's state is unknown, used, expired or
	// from another session, plugin or provider.
	ErrOAuthState = errors.New("the authorization expired, was used already or was started in another session")
	// ErrOAuthKeys: tokens cannot be sealed without the site's keys.
	ErrOAuthKeys = errors.New("OAuth connections need the site's secret key")

	errOAuthExpired = errors.New("expired: the connection ended; connect the account again on the plugin's page")
)

// OAuthError is a refusal from the provider, reduced to its error code
// and a short description: never a body that could hold a token.
type OAuthError struct {
	Status int
	Code   string // "invalid_grant"…
	Detail string
}

func (e *OAuthError) Error() string {
	msg := "HTTP " + strconv.Itoa(e.Status)
	if e.Code != "" {
		msg += ": " + e.Code
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// ------------------------------------------------------------- flow --

// oauthPlugin returns an enabled plugin with a granted provider.
func (h *Host) oauthPlugin(pluginID, provider string) (*Plugin, *OAuthDecl, error) {
	p := h.Get(pluginID)
	if p == nil {
		return nil, nil, ErrOAuthUnavailable
	}
	d := p.OAuthProvider(provider)
	if d == nil || p.Status != StatusEnabled || !p.Granted(PermOAuth+provider) {
		return p, d, ErrOAuthUnavailable
	}
	return p, d, nil
}

// oauthClient returns the client ID and secret from the plugin's settings.
func (h *Host) oauthClient(p *Plugin, d *OAuthDecl) (id, secret string, err error) {
	vals := h.pluginSettings(p)
	id = strings.TrimSpace(vals[d.ClientIDSetting])
	if d.ClientSecretSetting != "" {
		secret = strings.TrimSpace(vals[d.ClientSecretSetting])
	}
	if id == "" || d.ClientSecretSetting != "" && secret == "" {
		return "", "", ErrOAuthNotConfigured
	}
	return id, secret, nil
}

func oauthAD(kind, pluginID, provider string) []byte {
	return []byte("plugin-oauth-" + kind + "\x00" + pluginID + "\x00" + provider)
}

func (h *Host) oauthSeal(kind, pluginID, provider string, plain []byte) (string, error) {
	if h.secrets == nil {
		return "", ErrOAuthKeys
	}
	enc, err := h.secrets.Encrypt(plain, oauthAD(kind, pluginID, provider))
	if err != nil {
		return "", err
	}
	return secretPrefix + base64.RawStdEncoding.EncodeToString(enc), nil
}

func (h *Host) oauthOpen(kind, pluginID, provider, sealed string) ([]byte, error) {
	raw, ok := strings.CutPrefix(sealed, secretPrefix)
	if !ok || h.secrets == nil {
		return nil, ErrOAuthKeys
	}
	enc, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return nil, err
	}
	return h.secrets.Decrypt(enc, oauthAD(kind, pluginID, provider))
}

// sealedTokens is what plugin_oauth.tokens holds, sealed.
type sealedTokens struct {
	Access  string `json:"a"`
	Refresh string `json:"r,omitempty"`
}

func (h *Host) sealTokens(pluginID, provider string, t sealedTokens) (string, error) {
	raw, _ := json.Marshal(t)
	return h.oauthSeal("tokens", pluginID, provider, raw)
}

func (h *Host) openTokens(pluginID, provider, sealed string) (sealedTokens, error) {
	var t sealedTokens
	raw, err := h.oauthOpen("tokens", pluginID, provider, sealed)
	if err != nil {
		return t, err
	}
	return t, json.Unmarshal(raw, &t)
}

// OAuthBegin starts connecting a plugin to a provider: it records a
// single-use authorization for state (only its hash), bound to the
// administrator's session and user and to redirectURI, for ten minutes,
// and returns the provider's authorization URL to send the browser to.
// state must be unguessable; the web layer also signs it.
func (h *Host) OAuthBegin(ctx context.Context, pluginID, provider, redirectURI, state string, session []byte, userID int64) (string, error) {
	p, d, err := h.oauthPlugin(pluginID, provider)
	if err != nil {
		return "", err
	}
	if h.secrets == nil {
		return "", ErrOAuthKeys
	}
	if len(state) < 32 || len(session) == 0 || userID <= 0 {
		return "", errors.New("oauth: a state, a session and a user are required")
	}
	clientID, _, err := h.oauthClient(p, d)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(d.AuthorizeURL)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	if len(d.Scopes) > 0 {
		q.Set("scope", strings.Join(d.Scopes, d.Separator()))
	}
	sealedVerifier := ""
	if d.PKCE {
		verifier := randomURLToken(32) // 43 characters (RFC 7636 §4.1)
		sum := sha256.Sum256([]byte(verifier))
		q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
		q.Set("code_challenge_method", "S256")
		if sealedVerifier, err = h.oauthSeal("verifier", pluginID, provider, []byte(verifier)); err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256([]byte(state))
	if err := h.store.CreatePluginOAuthState(ctx, store.PluginOAuthState{StateHash: sum[:], PluginID: pluginID, Provider: provider,
		SessionHash: session, UserID: userID, Verifier: sealedVerifier, RedirectURI: redirectURI,
		ExpiresAt: time.Now().Add(oauthStateTTL)}); err != nil {
		return "", err
	}
	// Spaces as %20: some providers do not read "+" in a query.
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
	return u.String(), nil
}

// OAuthAbort forgets the authorization of state, when the provider sent
// the administrator back without a code.
func (h *Host) OAuthAbort(ctx context.Context, state string) {
	sum := sha256.Sum256([]byte(state))
	h.store.TakePluginOAuthState(ctx, sum[:])
}

// OAuthFinish completes a connection: it takes the authorization of state
// (once), checks that it was started by the same session and user, for
// this plugin, provider and redirectURI, exchanges the code (with the PKCE
// verifier), trades the token for a long-lived one when the provider says
// so, names the account and stores the tokens sealed. A previous
// connection to the provider is replaced.
func (h *Host) OAuthFinish(ctx context.Context, pluginID, provider, redirectURI, state, code string, session []byte, userID int64) (*OAuthConnection, error) {
	sum := sha256.Sum256([]byte(state))
	st, err := h.store.TakePluginOAuthState(ctx, sum[:])
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrOAuthState
	} else if err != nil {
		return nil, err
	}
	if st.PluginID != pluginID || st.Provider != provider || st.UserID != userID || st.RedirectURI != redirectURI ||
		subtle.ConstantTimeCompare(st.SessionHash, session) != 1 {
		return nil, ErrOAuthState
	}
	p, d, err := h.oauthPlugin(pluginID, provider)
	if err != nil {
		return nil, err
	}
	clientID, secret, err := h.oauthClient(p, d)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirectURI}}
	if st.Verifier != "" {
		v, err := h.oauthOpen("verifier", pluginID, provider, st.Verifier)
		if err != nil {
			return nil, ErrOAuthState
		}
		form.Set("code_verifier", string(v))
	}
	tok, err := h.oauthPost(ctx, d, d.TokenURL, form, clientID, secret)
	if err != nil {
		return nil, fmt.Errorf("exchanging the code: %w", err)
	}
	if e := d.Exchange; e != nil {
		long, err := h.oauthGet(ctx, e.URL, url.Values{"grant_type": {e.GrantType}, "client_secret": {secret}, "access_token": {tok.Access}})
		if err != nil {
			return nil, fmt.Errorf("getting a long-lived token: %w", err)
		}
		long.UserID = cmpOr(long.UserID, tok.UserID)
		long.Scope = cmpOr(long.Scope, tok.Scope)
		tok = long
	}
	accountID, accountName := tok.UserID, ""
	if d.UserinfoURL != "" {
		id, name, err := h.oauthAccount(ctx, d, tok.Access)
		if err != nil {
			p.addLog("warn", fmt.Sprintf("oauth: %s connected, but the account could not be read (%v)", d.Name, err))
		}
		accountID, accountName = cmpOr(id, accountID), name
	}
	now := time.Now()
	c := store.PluginOAuth{PluginID: pluginID, Provider: provider, AccountID: clip(accountID, 200), AccountName: clip(accountName, 200),
		Scopes: clip(tok.scopes(d), 2000), ExpiresAt: tok.expiresAt(now), RefreshExpiresAt: tok.refreshExpiresAt(now),
		Refreshable: d.refreshable(tok), ConnectedBy: &userID}
	if c.Tokens, err = h.sealTokens(pluginID, provider, sealedTokens{Access: tok.Access, Refresh: tok.Refresh}); err != nil {
		return nil, err
	}
	if err := h.store.SavePluginOAuth(ctx, c); err != nil {
		return nil, err
	}
	h.oauthBackoff(pluginID, provider, false)
	p.addLog("info", fmt.Sprintf("oauth: connected to %s%s", d.Name, accountSuffix(accountName, accountID)))
	// Only to this plugin: it may fetch what it shows at once.
	if !h.closed.Load() {
		select {
		case h.events <- eventJob{name: "oauth.connected", data: map[string]string{"provider": provider}, only: pluginID}:
		default:
		}
	}
	saved, err := h.store.PluginOAuthConnection(ctx, pluginID, provider)
	if err != nil {
		return nil, err
	}
	return connectionOf(saved), nil
}

func accountSuffix(name, id string) string {
	switch {
	case name != "":
		return " as " + name
	case id != "":
		return " as " + id
	}
	return ""
}

// refreshable reports whether a connection with these tokens can be
// renewed.
func (d *OAuthDecl) refreshable(t *oauthTokens) bool {
	if d.Refresh != nil && d.Refresh.Token == "access_token" {
		return t.ExpiresIn > 0
	}
	return t.Refresh != ""
}

// OAuthDisconnect deletes a connection and its tokens. It works whatever
// the plugin's status, and reports whether there was a connection.
func (h *Host) OAuthDisconnect(ctx context.Context, pluginID, provider string) (bool, error) {
	if !idRe.MatchString(pluginID) || !nameRe.MatchString(provider) {
		return false, errors.New("invalid plugin or provider")
	}
	ok, err := h.store.DeletePluginOAuth(ctx, pluginID, provider)
	if ok {
		if p := h.Get(pluginID); p != nil {
			p.addLog("info", "oauth: "+provider+" disconnected")
		}
	}
	return ok, err
}

// OAuthConnections returns the connections of a plugin by provider.
func (h *Host) OAuthConnections(ctx context.Context, pluginID string) (map[string]*OAuthConnection, error) {
	list, err := h.store.PluginOAuthConnections(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	out := map[string]*OAuthConnection{}
	for i := range list {
		if list[i].PluginID == pluginID {
			out[list[i].Provider] = connectionOf(&list[i])
		}
	}
	return out, nil
}

// OAuthAlert is a connection of an enabled plugin that ends within a week
// and will not renew itself, or has ended.
type OAuthAlert struct {
	PluginID, PluginName   string
	Provider, ProviderName string
	Account                string
	EndsAt                 time.Time
	Ended                  bool
	RefreshError           string
}

// OAuthAlerts lists the connections the administrator must renew.
func (h *Host) OAuthAlerts(ctx context.Context) ([]OAuthAlert, error) {
	if h.inert || h.store == nil {
		return nil, nil
	}
	list, err := h.store.PluginOAuthConnections(ctx, "")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []OAuthAlert
	for i := range list {
		p := h.Get(list[i].PluginID)
		if p == nil || !p.Enabled() {
			continue
		}
		d := p.OAuthProvider(list[i].Provider)
		if d == nil {
			continue
		}
		c := connectionOf(&list[i])
		at, ending := c.Ending(now)
		if !ending {
			continue
		}
		out = append(out, OAuthAlert{PluginID: p.ID, PluginName: p.Name, Provider: d.ID, ProviderName: d.Name,
			Account: cmpOr(c.AccountName, c.AccountID), EndsAt: at, Ended: !now.Before(at), RefreshError: c.RefreshError})
	}
	return out, nil
}

// ---------------------------------------------------- token requests --

// oauthTokens is a token response.
type oauthTokens struct {
	Access, Refresh, Type, Scope, UserID string
	ExpiresIn, RefreshExpiresIn          int64 // seconds
}

func (t *oauthTokens) expiresAt(now time.Time) *time.Time {
	if t.ExpiresIn <= 0 {
		return nil
	}
	at := now.Add(time.Duration(t.ExpiresIn) * time.Second).UTC().Truncate(time.Second)
	return &at
}

func (t *oauthTokens) refreshExpiresAt(now time.Time) *time.Time {
	if t.RefreshExpiresIn <= 0 || t.Refresh == "" {
		return nil
	}
	at := now.Add(time.Duration(t.RefreshExpiresIn) * time.Second).UTC().Truncate(time.Second)
	return &at
}

// scopes are the granted scopes, or the requested ones when the provider
// does not say.
func (t *oauthTokens) scopes(d *OAuthDecl) string {
	if t.Scope == "" {
		return strings.Join(d.Scopes, " ")
	}
	return strings.Join(strings.FieldsFunc(t.Scope, func(r rune) bool { return r == ' ' || r == ',' }), " ")
}

// newOAuthClient is the client of the token requests: guarded like plugin
// requests (public addresses only, checked after DNS resolution), no
// proxy, and no redirects, which token endpoints never need.
func newOAuthClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:            safeDialContext,
			Proxy:                  nil,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           4,
			IdleConnTimeout:        30 * time.Second,
			TLSHandshakeTimeout:    5 * time.Second,
			ResponseHeaderTimeout:  8 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
		},
		Timeout:       oauthTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// SetOAuthTransport replaces the guarded transport of the OAuth token
// requests. Tests use it to reach a fake provider on loopback; nothing
// else may, since it lifts the SSRF guard.
func (h *Host) SetOAuthTransport(rt http.RoundTripper) { h.oauthc.Transport = rt }

// oauthPost sends a token request with the client's credentials.
func (h *Host) oauthPost(ctx context.Context, d *OAuthDecl, endpoint string, form url.Values, clientID, secret string) (*oauthTokens, error) {
	basic := d.TokenAuth == "basic" && secret != ""
	if !basic {
		form.Set("client_id", clientID)
		if secret != "" {
			form.Set("client_secret", secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basic {
		// RFC 6749 §2.3.1: each part form-encoded, then Basic.
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(secret))
	}
	return h.oauthDo(req)
}

// oauthGet sends a token request whose parameters go in the query (Meta's
// long-lived token exchange and renewal).
func (h *Host) oauthGet(ctx context.Context, endpoint string, params url.Values) (*oauthTokens, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return h.oauthDo(req)
}

func (h *Host) oauthDo(req *http.Request) (*oauthTokens, error) {
	body, status, err := h.oauthSend(req)
	if err != nil {
		return nil, err
	}
	return parseTokenResponse(status, body)
}

// oauthSend sends a request to a provider and reads at most maxOAuthBody.
// Its errors never carry the URL, which can hold a secret.
func (h *Host) oauthSend(req *http.Request) ([]byte, int, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sap13ns-oauth")
	resp, err := h.oauthc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, 0, fmt.Errorf("%s: %w", req.URL.Hostname(), err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxOAuthBody+1))
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", req.URL.Hostname(), err)
	}
	if len(body) > maxOAuthBody {
		return nil, 0, fmt.Errorf("%s: the answer is too large", req.URL.Hostname())
	}
	return body, resp.StatusCode, nil
}

// jsonString reads a string or a number.
func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func jsonInt(raw json.RawMessage) int64 {
	n, err := strconv.ParseInt(jsonString(raw), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// jsonScope reads a scope list given as a string or an array.
func jsonScope(raw json.RawMessage) string {
	if s := jsonString(raw); s != "" {
		return s
	}
	var list []string
	json.Unmarshal(raw, &list)
	return strings.Join(list, " ")
}

// parseTokenResponse reads a token response (RFC 6749 §5.1), also when it
// is wrapped in {"data": [...]}, or turns an error answer into an
// *OAuthError.
func parseTokenResponse(status int, body []byte) (*oauthTokens, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		obj = nil
	}
	if status < 200 || status > 299 {
		return nil, oauthErrorOf(status, obj)
	}
	if _, ok := obj["access_token"]; !ok {
		var data []map[string]json.RawMessage
		if json.Unmarshal(obj["data"], &data) == nil && len(data) > 0 {
			obj = data[0]
		}
	}
	t := &oauthTokens{
		Access: jsonString(obj["access_token"]), Refresh: jsonString(obj["refresh_token"]), Type: jsonString(obj["token_type"]),
		ExpiresIn: jsonInt(obj["expires_in"]), RefreshExpiresIn: jsonInt(obj["refresh_token_expires_in"]),
		Scope: jsonScope(obj["scope"]), UserID: jsonString(obj["user_id"]),
	}
	if t.Scope == "" {
		t.Scope = jsonScope(obj["permissions"])
	}
	switch {
	case t.Access == "" || len(t.Access) > maxOAuthToken || !printable(t.Access):
		return nil, errors.New("the answer has no valid access token")
	case len(t.Refresh) > maxOAuthToken || !printable(t.Refresh):
		return nil, errors.New("the answer has an invalid refresh token")
	case t.Type != "" && !strings.EqualFold(t.Type, "bearer"):
		return nil, fmt.Errorf("unsupported token type %q", clip(t.Type, 20))
	}
	return t, nil
}

func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// oauthErrorOf reduces an error answer: RFC 6749 §5.2
// ({"error", "error_description"}), Meta ({"error": {"message", "type"}})
// or Instagram ({"error_type", "error_message"}).
func oauthErrorOf(status int, obj map[string]json.RawMessage) *OAuthError {
	e := &OAuthError{Status: status}
	if obj == nil {
		return e
	}
	var nested struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	switch {
	case jsonString(obj["error"]) != "":
		e.Code, e.Detail = jsonString(obj["error"]), jsonString(obj["error_description"])
	case json.Unmarshal(obj["error"], &nested) == nil && (nested.Message != "" || nested.Type != ""):
		e.Code, e.Detail = nested.Type, nested.Message
	default:
		e.Code, e.Detail = jsonString(obj["error_type"]), cmpOr(jsonString(obj["error_message"]), jsonString(obj["message"]))
	}
	e.Code, e.Detail = clip(oneLine(e.Code, 60), 60), clip(oneLine(e.Detail, 200), 200)
	return e
}

// clip shortens s to n bytes on a character boundary.
func clip(s string, n int) string { return truncate(s, n) }

// oauthAccount reads the connected account's id and name.
func (h *Host) oauthAccount(ctx context.Context, d *OAuthDecl, access string) (id, name string, err error) {
	u, err := url.Parse(d.UserinfoURL)
	if err != nil {
		return "", "", err
	}
	if d.UserinfoToken == "query" {
		q := u.Query()
		q.Set("access_token", access)
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", "", err
	}
	if d.UserinfoToken != "query" {
		req.Header.Set("Authorization", "Bearer "+access)
	}
	body, status, err := h.oauthSend(req)
	if err != nil {
		return "", "", err
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		obj = nil
	}
	if status < 200 || status > 299 {
		return "", "", oauthErrorOf(status, obj)
	}
	// Instagram documents its /me wrapped in {"data": [...]}.
	var data []map[string]json.RawMessage
	if _, ok := obj["data"]; ok && json.Unmarshal(obj["data"], &data) == nil && len(data) > 0 {
		obj = data[0]
	}
	for _, k := range []string{"sub", "user_id", "id"} {
		if id = jsonString(obj[k]); id != "" {
			break
		}
	}
	for _, k := range []string{"name", "username", "preferred_username"} {
		if name = jsonString(obj[k]); name != "" {
			break
		}
	}
	if name == "" {
		name = strings.TrimSpace(jsonString(obj["given_name"]) + " " + jsonString(obj["family_name"]))
	}
	return oneLine(id, 200), oneLine(name, 200), nil
}

// ------------------------------------------------------------ renewal --

// oauthLock serializes the renewals of one connection in this host, so a
// rotated refresh token is never sent twice.
func (h *Host) oauthLock(pluginID, provider string) *sync.Mutex {
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	key := pluginID + "\x00" + provider
	m, ok := h.oauthLocks[key]
	if !ok {
		m = &sync.Mutex{}
		h.oauthLocks[key] = m
	}
	return m
}

// oauthBackoff records a failed renewal (failed) or clears the record.
func (h *Host) oauthBackoff(pluginID, provider string, failed bool) {
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	key := pluginID + "\x00" + provider
	if failed {
		h.oauthFailed[key] = time.Now()
	} else {
		delete(h.oauthFailed, key)
	}
}

// oauthMayRetry reports whether a renewal may be tried now.
func (h *Host) oauthMayRetry(pluginID, provider string) bool {
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	at, ok := h.oauthFailed[pluginID+"\x00"+provider]
	return !ok || time.Since(at) > oauthRetry
}

// oauthDue reports whether a connection should be renewed now: once half
// of its token's life has passed (at the latest five minutes before it
// ends), so a 60-day token is renewed after 30 days and an hour-long one
// after half an hour.
func oauthDue(c *store.PluginOAuth, now time.Time) bool {
	if !c.Refreshable || c.ExpiresAt == nil {
		return false
	}
	issued := c.ConnectedAt
	if c.RefreshedAt != nil {
		issued = *c.RefreshedAt
	}
	margin := max(5*time.Minute, c.ExpiresAt.Sub(issued)/2)
	return !now.Before(c.ExpiresAt.Add(-margin))
}

// oauthRenew renews a connection if it is due, and returns it as stored
// afterwards. A failed renewal is recorded with the connection and logged;
// the old token stays in use while it lasts.
func (h *Host) oauthRenew(ctx context.Context, p *Plugin, d *OAuthDecl, c *store.PluginOAuth) (*store.PluginOAuth, error) {
	if !oauthDue(c, time.Now()) || !h.oauthMayRetry(p.ID, d.ID) {
		return c, nil
	}
	mu := h.oauthLock(p.ID, d.ID)
	mu.Lock()
	defer mu.Unlock()
	// Renewed meanwhile by another call?
	c, err := h.store.PluginOAuthConnection(ctx, p.ID, d.ID)
	if err != nil || !oauthDue(c, time.Now()) {
		return c, err
	}
	old, err := h.openTokens(p.ID, d.ID, c.Tokens)
	if err != nil {
		return c, err
	}
	var tok *oauthTokens
	if r := d.Refresh; r != nil && r.Token == "access_token" {
		tok, err = h.oauthGet(ctx, r.URL, url.Values{"grant_type": {r.GrantType}, "access_token": {old.Access}})
	} else {
		endpoint, grant := d.TokenURL, "refresh_token"
		if r := d.Refresh; r != nil {
			endpoint, grant = cmpOr(r.URL, endpoint), cmpOr(r.GrantType, grant)
		}
		var clientID, secret string
		if clientID, secret, err = h.oauthClient(p, d); err == nil {
			tok, err = h.oauthPost(ctx, d, endpoint, url.Values{"grant_type": {grant}, "refresh_token": {old.Refresh}}, clientID, secret)
		}
	}
	if err != nil {
		h.oauthBackoff(p.ID, d.ID, true)
		var oe *OAuthError
		dead := errors.As(err, &oe) && oe.Code == "invalid_grant" // the refresh token itself was refused
		msg := clip(oneLine(err.Error(), 300), 300)
		if ferr := h.store.FailPluginOAuthRefresh(context.WithoutCancel(ctx), p.ID, d.ID, c.Tokens, msg, dead); ferr != nil {
			err = errors.Join(err, ferr)
		}
		p.addLog("warn", fmt.Sprintf("oauth: renewing the %s token failed: %s", d.Name, msg))
		h.log.Warn("plugin oauth renewal failed", "plugin", p.ID, "provider", d.ID, "err", msg)
		fresh, rerr := h.store.PluginOAuthConnection(ctx, p.ID, d.ID)
		if rerr != nil {
			return c, err
		}
		return fresh, err
	}
	now := time.Now()
	next := sealedTokens{Access: tok.Access, Refresh: cmpOr(tok.Refresh, old.Refresh)}
	if tok.Refresh == "" {
		tok.Refresh = old.Refresh
	}
	nc := store.PluginOAuth{PluginID: p.ID, Provider: d.ID, ExpiresAt: tok.expiresAt(now), Refreshable: d.refreshable(tok)}
	if tok.Scope != "" {
		nc.Scopes = clip(tok.scopes(d), 2000)
	}
	// A rotated refresh token brings its own lifetime; otherwise the old
	// one keeps its end.
	if nc.RefreshExpiresAt = tok.refreshExpiresAt(now); nc.RefreshExpiresAt == nil && next.Refresh == old.Refresh {
		nc.RefreshExpiresAt = c.RefreshExpiresAt
	}
	if nc.Tokens, err = h.sealTokens(p.ID, d.ID, next); err != nil {
		return c, err
	}
	if _, err := h.store.RefreshPluginOAuth(context.WithoutCancel(ctx), nc, c.Tokens); err != nil {
		return c, err
	}
	h.oauthBackoff(p.ID, d.ID, false)
	until := "without an end date"
	if nc.ExpiresAt != nil {
		until = "until " + nc.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	p.addLog("info", fmt.Sprintf("oauth: the %s token was renewed; it is valid %s", d.Name, until))
	return h.store.PluginOAuthConnection(ctx, p.ID, d.ID)
}

// oauthTokenFor is the token oauth_token gives the plugin: renewed first
// when due.
func (h *Host) oauthTokenFor(ctx context.Context, p *Plugin, d *OAuthDecl) (*oauthTokenResponse, error) {
	c, err := h.store.PluginOAuthConnection(ctx, p.ID, d.ID)
	if err != nil {
		return nil, err
	}
	if c, err = h.oauthRenew(ctx, p, d, c); c == nil {
		return nil, err
	}
	now := time.Now()
	if c.ExpiresAt != nil && !now.Before(*c.ExpiresAt) || c.RefreshExpiresAt != nil && !now.Before(*c.RefreshExpiresAt) && c.Refreshable {
		return nil, errOAuthExpired
	}
	tok, err := h.openTokens(p.ID, d.ID, c.Tokens)
	if err != nil {
		p.addLog("error", "oauth: the stored "+d.Name+" token cannot be decrypted; connect the account again")
		return nil, errOAuthExpired
	}
	out := &oauthTokenResponse{AccessToken: tok.Access, TokenType: "bearer", AccountID: c.AccountID, AccountName: c.AccountName,
		Scopes: strings.Fields(c.Scopes)}
	if c.ExpiresAt != nil {
		out.ExpiresAt = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if out.Scopes == nil {
		out.Scopes = []string{}
	}
	return out, nil
}

// oauthTokenResponse is the JSON oauth_token returns.
type oauthTokenResponse struct {
	AccessToken string   `json:"access_token,omitempty"`
	TokenType   string   `json:"token_type,omitempty"`
	ExpiresAt   string   `json:"expires_at,omitempty"`
	AccountID   string   `json:"account_id,omitempty"`
	AccountName string   `json:"account_name,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// renewOAuth renews the due connections of the enabled plugins (from the
// hourly cron), so a token is renewed in time even while its plugin does
// not ask for it.
func (h *Host) renewOAuth(ctx context.Context) {
	if h.store == nil {
		return
	}
	for _, p := range h.enabled() {
		if len(p.OAuth) == 0 {
			continue
		}
		list, err := h.store.PluginOAuthConnections(ctx, p.ID)
		if err != nil {
			h.log.Warn("plugin oauth connections", "plugin", p.ID, "err", err)
			continue
		}
		for i := range list {
			d := p.OAuthProvider(list[i].Provider)
			if d == nil || !p.Granted(PermOAuth+d.ID) || list[i].PluginID != p.ID {
				continue
			}
			rctx, cancel := context.WithTimeout(ctx, oauthTimeout+2*time.Second)
			h.oauthRenew(rctx, p, d, &list[i])
			cancel()
		}
	}
}

// wantsOAuth reports whether an enabled plugin has a granted provider.
func (h *Host) wantsOAuth() bool {
	for _, p := range h.enabled() {
		for _, d := range p.OAuth {
			if p.Granted(PermOAuth + d.ID) {
				return true
			}
		}
	}
	return false
}

// randomURLToken returns n random bytes, base64url without padding.
func randomURLToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ------------------------------------------------------ host function --

// oauth_token(pptr, plen) -> len | -1 not connected | -2 denied
//
// It returns the JSON of oauthTokenResponse: a valid access token of the
// provider, renewed first when due, or {"error": "expired: ..."} when the
// connection ended and the administrator must connect it again. It needs
// the granted "oauth:<provider>" permission and, like http_fetch, a hook
// that may reach the network (events and forms, not page rendering).
func (h *Host) hostOAuthToken(ctx context.Context, m api.Module, pptr, plen uint32) int32 {
	st := state(ctx)
	if st == nil {
		return codeInvalid
	}
	if !st.allowHTTP || st.dryRun {
		return codeDenied
	}
	raw, ok := readMem(m, pptr, plen, 40)
	if !ok || !nameRe.Match(raw) {
		return codeInvalid
	}
	provider := string(raw)
	d := st.plugin.OAuthProvider(provider)
	if d == nil || !st.granted(PermOAuth+provider) {
		return codeDenied
	}
	st.oauthCalls++
	if st.oauthCalls > maxOAuthPerCall {
		return codeLimit
	}
	if h.store == nil || h.secrets == nil {
		return codeFailed
	}
	out, err := h.oauthTokenFor(ctx, st.plugin, d)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return codeNotFound
	case errors.Is(err, errOAuthExpired):
		out = &oauthTokenResponse{Error: errOAuthExpired.Error()}
	case err != nil:
		h.log.Warn("plugin oauth token", "plugin", st.plugin.ID, "provider", provider, "err", clip(oneLine(err.Error(), 300), 300))
		return codeFailed
	}
	b, _ := json.Marshal(out)
	return st.setPending(b)
}
