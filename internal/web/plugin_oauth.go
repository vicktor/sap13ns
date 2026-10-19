package web

// OAuth connections of plugins (docs/PLUGINS.md, OAuth connections). The
// administrator connects a plugin to a provider it declares from the
// plugin's page; the plugin host keeps the tokens and gives the plugin only
// a valid access token.
//
//  1. POST …/oauth/{provider}/connect (CSRF-checked) creates a state: a
//     random nonce signed with the site's key over the plugin, the
//     provider and the session, valid ten minutes. The host stores its
//     hash, bound to the session, the user and the redirect URI, with the
//     PKCE verifier, and the browser goes to the provider.
//  2. GET …/oauth/{provider}/callback, a signed-in admin request like any
//     other, checks the signature, then the host takes the stored state
//     (once), checks the binding, and exchanges the code over the guarded
//     client.
//  3. POST …/oauth/{provider}/disconnect deletes the tokens.
//
// The redirect URI is always on the site's primary host: the session
// cookie is host-only, so a connection started on another host of a
// network site is sent there first.

import (
	"cmp"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/plugin"
)

const oauthStateMaxAge = 10 * time.Minute

// oauthRow is one provider on a plugin's page.
type oauthRow struct {
	*plugin.OAuthDecl
	Conn        *plugin.OAuthConnection // nil: not connected
	RedirectURI string
	Available   bool // the plugin is enabled with the provider's permission
	Ended       bool
	Ending      bool
	EndsAt      time.Time
}

func (s *Server) oauthRedirectURI(pluginID, provider string) string {
	return s.cfg.AbsURL("/admin/plugins/" + pluginID + "/oauth/" + provider + "/callback")
}

func oauthStateValue(pluginID, provider string, session []byte, nonce string) string {
	return pluginID + "\x00" + provider + "\x00" + hex.EncodeToString(session) + "\x00" + nonce
}

// newOAuthState returns a state for the session: nonce.ts.signature.
func (s *Server) newOAuthState(pluginID, provider string, session []byte) string {
	nonce := auth.NewToken(32)
	return nonce + "." + s.keys.Sign("plugin-oauth", oauthStateValue(pluginID, provider, session, nonce), time.Now())
}

// validOAuthState checks the signature and the age of a state.
func (s *Server) validOAuthState(pluginID, provider string, session []byte, state string) bool {
	nonce, sig, ok := strings.Cut(state, ".")
	if !ok || nonce == "" || len(state) > 200 {
		return false
	}
	_, ok = s.keys.Verify("plugin-oauth", oauthStateValue(pluginID, provider, session, nonce), sig, oauthStateMaxAge)
	return ok
}

// oauthRows lists the providers of a plugin with their connections.
func (s *Server) oauthRows(r *http.Request, p *plugin.Plugin) ([]oauthRow, error) {
	if len(p.OAuth) == 0 {
		return nil, nil
	}
	conns, err := s.plugins.OAuthConnections(r.Context(), p.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []oauthRow
	for i := range p.OAuth {
		d := &p.OAuth[i]
		row := oauthRow{OAuthDecl: d, Conn: conns[d.ID], RedirectURI: s.oauthRedirectURI(p.ID, d.ID),
			Available: p.Status == plugin.StatusEnabled && p.Granted(plugin.PermOAuth+d.ID)}
		if row.Conn != nil {
			row.EndsAt, row.Ending = row.Conn.Ending(now)
			row.Ended = row.Conn.Expired(now) || row.Ending && !now.Before(row.EndsAt)
		}
		out = append(out, row)
	}
	return out, nil
}

// oauthFormAction lets the plugin's page send its Connect forms to the
// providers: Chrome checks form-action on the redirect that follows the
// POST too.
func oauthFormAction(w http.ResponseWriter, p *plugin.Plugin) {
	var origins []string
	for i := range p.OAuth {
		if o := p.OAuth[i].AuthorizeOrigin(); o != "" && !slices.Contains(origins, o) {
			origins = append(origins, o)
		}
	}
	if len(origins) == 0 {
		return
	}
	h := w.Header()
	if policy := h.Get("Content-Security-Policy"); policy != "" {
		h.Set("Content-Security-Policy", strings.Replace(policy, "form-action 'self'", "form-action 'self' "+strings.Join(origins, " "), 1))
	}
}

// oauthProvider loads the plugin and the provider of the request.
func (s *Server) oauthProvider(w http.ResponseWriter, r *http.Request) (*plugin.Plugin, *plugin.OAuthDecl, bool) {
	if s.pluginsInert(w, r) {
		return nil, nil, false
	}
	p, ok := s.loadPlugin(w, r)
	if !ok {
		return nil, nil, false
	}
	d := p.OAuthProvider(r.PathValue("provider"))
	if d == nil {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return nil, nil, false
	}
	return p, d, true
}

func oauthBack(p *plugin.Plugin) string { return "/admin/plugins/" + p.ID + "#connections" }

// oauthMessage turns an error of the plugin host into a flash message.
func (s *Server) oauthMessage(r *http.Request, d *plugin.OAuthDecl, err error) string {
	switch {
	case errors.Is(err, plugin.ErrOAuthUnavailable):
		return s.tu(r, "Enable the plugin first: the connection is one of the permissions you approve.")
	case errors.Is(err, plugin.ErrOAuthNotConfigured):
		return s.tu(r, "Enter the client ID and the client secret in the plugin's settings first.")
	case errors.Is(err, plugin.ErrOAuthState):
		return s.tu(r, "The authorization could not be verified: it expired, was used already or was started in another session. Connect again.")
	case errors.Is(err, plugin.ErrOAuthKeys):
		return s.tu(r, "The site has no secret key to protect the connection.")
	}
	return s.tu(r, "Connecting to %s failed: %s", d.Name, err.Error())
}

func (s *Server) adminPluginOAuthConnect(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.oauthProvider(w, r)
	if !ok {
		return
	}
	if !sameHost(r.Host, s.cfg.BaseURL.Hostname()) {
		// The provider sends the administrator back to the primary host,
		// where this host's session cookie does not exist.
		s.redirect(w, r, oauthBack(p), "error", s.tu(r, "Connect accounts from %s: the provider sends you back there.", s.cfg.AbsURL(oauthBack(p))))
		return
	}
	sess := rctx(r).Session
	if sess == nil {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "You do not have permission to access this page."))
		return
	}
	state := s.newOAuthState(p.ID, d.ID, sess.TokenHash)
	to, err := s.plugins.OAuthBegin(r.Context(), p.ID, d.ID, s.oauthRedirectURI(p.ID, d.ID), state, sess.TokenHash, uid(r))
	if err != nil {
		s.redirect(w, r, oauthBack(p), "error", s.oauthMessage(r, d, err))
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) adminPluginOAuthCallback(w http.ResponseWriter, r *http.Request) {
	// The URL holds the code: never send it on as a referrer.
	w.Header().Set("Referrer-Policy", "no-referrer")
	p, d, ok := s.oauthProvider(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	state, sess := q.Get("state"), rctx(r).Session
	fail := func(reason, msg string) {
		s.audit(r, uid(r), "plugin.oauth_failed", p.ID, map[string]string{"provider": d.ID, "reason": truncateRunes(oneLine(reason), 200)})
		s.redirect(w, r, oauthBack(p), "error", msg)
	}
	if sess == nil || !s.validOAuthState(p.ID, d.ID, sess.TokenHash, state) {
		fail("state", s.oauthMessage(r, d, plugin.ErrOAuthState))
		return
	}
	if e := q.Get("error"); e != "" {
		s.plugins.OAuthAbort(r.Context(), state)
		why := truncateRunes(oneLine(cmp.Or(q.Get("error_description"), q.Get("error_reason"), e)), 200)
		fail(e, s.tu(r, "%s did not authorize the connection: %s", d.Name, why))
		return
	}
	// Instagram appends "#_", which browsers do not send; a code pasted by
	// hand might still carry it.
	code := strings.TrimSuffix(q.Get("code"), "#_")
	if code == "" || len(code) > 4096 {
		s.plugins.OAuthAbort(r.Context(), state)
		fail("code", s.tu(r, "%s sent no authorization code. Connect again.", d.Name))
		return
	}
	c, err := s.plugins.OAuthFinish(r.Context(), p.ID, d.ID, s.oauthRedirectURI(p.ID, d.ID), state, code, sess.TokenHash, uid(r))
	if err != nil {
		s.log.Warn("plugin oauth connection failed", "plugin", p.ID, "provider", d.ID, "err", err)
		fail(err.Error(), s.oauthMessage(r, d, err))
		return
	}
	account := cmp.Or(c.AccountName, c.AccountID)
	s.audit(r, uid(r), "plugin.oauth_connected", p.ID, map[string]string{"provider": d.ID, "account": account})
	msg := s.tu(r, "Connected to %s.", d.Name)
	if account != "" {
		msg = s.tu(r, "Connected to %s as %s.", d.Name, account)
	}
	s.redirect(w, r, oauthBack(p), "success", msg)
}

func (s *Server) adminPluginOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	p, d, ok := s.oauthProvider(w, r)
	if !ok {
		return
	}
	found, err := s.plugins.OAuthDisconnect(r.Context(), p.ID, d.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if found {
		s.audit(r, uid(r), "plugin.oauth_disconnected", p.ID, map[string]string{"provider": d.ID})
	}
	s.redirect(w, r, oauthBack(p), "success", s.tu(r, "Disconnected from %s.", d.Name))
}

// oauthAlerts are the connections that need the administrator, for the
// Plugins page and the dashboard. Errors only cost the notice.
func (s *Server) oauthAlerts(r *http.Request) []plugin.OAuthAlert {
	if u := currentUser(r); u == nil || !auth.Can(u.Role, auth.CapPlugins) {
		return nil
	}
	alerts, err := s.plugins.OAuthAlerts(r.Context())
	if err != nil {
		s.log.Error("plugin oauth alerts", "err", err)
	}
	return alerts
}
