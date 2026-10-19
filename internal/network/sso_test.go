package network

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// browser keeps the cookies of one visitor across the hosts of a network.
type browser struct {
	e   *netEnv
	jar *cookiejar.Jar
	ip  string
}

func (e *netEnv) browser() *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{e: e, jar: jar, ip: "192.0.2.10"}
}

// do sends a request as a top-level navigation of this browser, with its
// cookies, and keeps the cookies of the answer.
func (b *browser) do(method, target string, form url.Values, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	b.e.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.RemoteAddr = b.ip + ":4000"
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	for _, c := range b.jar.Cookies(req.URL) {
		req.AddCookie(c)
	}
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	b.e.reg.ServeHTTP(rec, req)
	b.jar.SetCookies(req.URL, rec.Result().Cookies())
	return rec
}

func (b *browser) get(target string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	b.e.t.Helper()
	return b.do("GET", target, nil, mod...)
}

func (b *browser) cookie(host, name string) *http.Cookie {
	for _, c := range b.jar.Cookies(&url.URL{Scheme: "https", Host: host, Path: "/"}) {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// portalSignIn signs the browser in to the portal as an account, as a
// finished password and second-factor login would, and returns the
// session's CSRF token.
func (b *browser) portalSignIn(accountID int64) string {
	b.e.t.Helper()
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := b.e.portal.CreateSession(b.e.ctx, store.Session{TokenHash: auth.HashToken(token), UserID: accountID,
		CSRFToken: csrf, ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		b.e.t.Fatal(err)
	}
	b.jar.SetCookies(&url.URL{Scheme: "https", Host: "sap13ns.site"}, []*http.Cookie{{Name: "__Host-sap13ns", Value: token, Path: "/", Secure: true}})
	return csrf
}

// networkAdmin creates a portal admin with a second factor who is also a
// network admin, signed in on a new browser.
func (e *netEnv) networkAdmin(name string) (int64, *browser, string) {
	e.t.Helper()
	id, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: name, Email: name + "@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		e.t.Fatal(err)
	}
	e.mfa(id)
	if err := e.n.AddNetworkAdmin(e.ctx, id, 0); err != nil {
		e.t.Fatal(err)
	}
	b := e.browser()
	return id, b, b.portalSignIn(id)
}

// siteSignIn signs the browser in to a site as one of its users and returns
// the session's CSRF token.
func (b *browser) siteSignIn(s *Site, userID int64) string {
	b.e.t.Helper()
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := b.e.siteStore(s).CreateSession(b.e.ctx, store.Session{TokenHash: auth.HashToken(token), UserID: userID,
		CSRFToken: csrf, ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		b.e.t.Fatal(err)
	}
	b.jar.SetCookies(&url.URL{Scheme: "https", Host: s.PrimaryHost}, []*http.Cookie{{Name: "__Host-sap13ns", Value: token, Path: "/", Secure: true}})
	return csrf
}

// mfa gives an account a second factor.
func (e *netEnv) mfa(accountID int64) {
	e.t.Helper()
	if err := e.portal.SetTOTP(e.ctx, accountID, []byte("sealed"), true, nil); err != nil {
		e.t.Fatal(err)
	}
}

// startSSO opens /sso/start on a site and returns the authorize URL.
func (b *browser) startSSO(host, next string) string {
	b.e.t.Helper()
	rec := b.get("https://" + host + "/sso/start?next=" + url.QueryEscape(next))
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "https://sap13ns.site/sso/authorize?") {
		b.e.t.Fatalf("sso start on %s: %d %s", host, rec.Code, loc)
	}
	return loc
}

// authorize opens the authorize URL and returns the callback URL it
// redirects to.
func (b *browser) authorize(authURL string) string {
	b.e.t.Helper()
	rec := b.get(authURL)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc, "/sso/callback?") {
		b.e.t.Fatalf("authorize: %d %s %s", rec.Code, loc, rec.Body.String())
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		b.e.t.Fatalf("authorize Referrer-Policy = %q", got)
	}
	return loc
}

// sso signs the browser in to a site through the portal and returns the
// callback's answer.
func (b *browser) sso(host, next string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	return b.get(b.authorize(b.startSSO(host, next)))
}

func query(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get(key)
}

// audits counts the audit entries of a store with an action.
func audits(t *testing.T, st *store.Store, action string) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action = $1`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessions(t *testing.T, st *store.Store, userID int64) []store.Session {
	t.Helper()
	list, err := st.ListUserSessions(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestSSOFlow(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	e.mfa(e.owner)

	// The sites of the test network are in Spanish.
	b := e.browser()
	if rec := b.get("https://alpha.sap13ns.site/login?next=/admin/posts"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Iniciar sesión con tu cuenta de sap13ns") || !strings.Contains(rec.Body.String(), `href="/sso/start?next=%2fadmin%2fposts"`) {
		t.Fatalf("tenant login page: %d %s", rec.Code, rec.Body.String())
	}
	if rec := b.get("https://sap13ns.site/login"); strings.Contains(rec.Body.String(), "/sso/start") {
		t.Fatal("the portal offers to sign in through itself")
	}

	// Not signed in on the portal: its login first, then back.
	authURL := b.startSSO("alpha.sap13ns.site", "/admin/posts")
	ck := b.cookie("alpha.sap13ns.site", "__Host-sap13ns-sso")
	if ck == nil {
		t.Fatal("no state cookie")
	}
	if q := query(t, authURL, "redirect_uri"); q != "https://alpha.sap13ns.site/sso/callback" {
		t.Fatalf("redirect_uri = %q", q)
	}
	if query(t, authURL, "site") != strconv.FormatInt(a.ID, 10) || query(t, authURL, "code_challenge_method") != "S256" {
		t.Fatalf("authorize URL %s", authURL)
	}
	rec := b.get(authURL)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?next=%2Fsso%2Fauthorize%3F") {
		t.Fatalf("authorize without a portal session: %d %s", rec.Code, loc)
	}

	b.portalSignIn(e.owner)
	cbURL := b.authorize(authURL)
	if !strings.HasPrefix(cbURL, "https://alpha.sap13ns.site/sso/callback?") || query(t, cbURL, "state") != query(t, authURL, "state") {
		t.Fatalf("callback URL %s", cbURL)
	}
	rec = b.get(cbURL)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/posts" {
		t.Fatalf("callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("callback headers %v", rec.Header())
	}
	if b.cookie("alpha.sap13ns.site", "__Host-sap13ns-sso") != nil {
		t.Fatal("the state cookie outlived its callback")
	}
	if b.cookie("sap13ns.site", "__Host-sap13ns-sso") != nil {
		t.Fatal("the state cookie reached the portal")
	}

	// The account got a user of its own, linked, with the membership's
	// role and no password; its session passed the portal's second factor.
	u, err := sa.UserByNetworkAccount(e.ctx, e.owner)
	if err != nil || u.Role != "admin" || !u.SSOOnly() || u.Username != "owner" || u.Email != "owner@example.com" || u.NetworkOperator {
		t.Fatalf("site user: %+v, %v", u, err)
	}
	if list := sessions(t, sa, u.ID); len(list) != 1 || list[0].AMR != store.AMRSSO {
		t.Fatalf("sessions: %+v", list)
	}
	if rec := b.get("https://alpha.sap13ns.site/admin/posts"); rec.Code != http.StatusOK {
		t.Fatalf("admin after SSO: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if audits(t, sa, "login.success") != 1 || audits(t, e.portal, "sso.authorized") != 1 {
		t.Fatal("the sign-in is not in both audit logs")
	}
	if m, _ := e.n.Member(e.ctx, a.ID, e.owner); m.LastSSOAt == nil {
		t.Fatal("last_sso_at not recorded")
	}

	// Replay: the code works once, even with a fresh state.
	r := e.browser()
	authURL2 := r.startSSO("alpha.sap13ns.site", "/admin/")
	replay := "https://alpha.sap13ns.site/sso/callback?" + url.Values{"code": {query(t, cbURL, "code")}, "state": {query(t, authURL2, "state")}}.Encode()
	before := len(sessions(t, sa, u.ID))
	if rec := r.get(replay); rec.Code != http.StatusBadRequest || len(sessions(t, sa, u.ID)) != before || r.cookie("alpha.sap13ns.site", "__Host-sap13ns") != nil {
		t.Fatalf("replayed code: %d", rec.Code)
	}

	// Signed in on the portal, the next sign-in is silent and the user is
	// the same.
	c := e.browser()
	c.portalSignIn(e.owner)
	if rec := c.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("second sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if n, _ := sa.CountUsers(e.ctx); n != 1 {
		t.Fatalf("%d users after two sign-ins", n)
	}

	// The user has no password to use or reset.
	rec = c.do("POST", "https://alpha.sap13ns.site/login", url.Values{"login": {"owner"}, "password": {"x"}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("password login of an SSO user: %d", rec.Code)
	}
	if rec := c.do("POST", "https://alpha.sap13ns.site/forgot-password", url.Values{"email": {"owner@example.com"}}); rec.Code != http.StatusOK {
		t.Fatalf("forgot password: %d", rec.Code)
	}
	var resets int
	if err := sa.DB.QueryRow(e.ctx, `SELECT count(*) FROM password_resets`).Scan(&resets); err != nil || resets != 0 || audits(t, sa, "password.reset_refused") != 1 {
		t.Fatalf("a reset for an SSO user: %d %v", resets, err)
	}
}

func TestSSOCodes(t *testing.T) {
	e := newNetEnv(t, nil)
	a, b := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	acct := e.account("ana")
	if err := e.n.SetMember(e.ctx, a.ID, acct, "editor", 0); err != nil {
		t.Fatal(err)
	}
	cb := "https://alpha.sap13ns.site/sso/callback"
	issue := func(site int64, verifier string) string {
		t.Helper()
		sum := sha256.Sum256([]byte(verifier))
		code, err := e.n.IssueSSOCode(e.ctx, SSOCode{SiteID: site, AccountID: acct, Challenge: sum[:], RedirectURI: cb})
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	expect := func(what string, err, want error) {
		t.Helper()
		if !errors.Is(err, want) {
			t.Fatalf("%s: err = %v, want %v", what, err, want)
		}
	}

	code := issue(a.ID, "verifier-1")
	var stored int
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM sso_codes WHERE code_hash = $1 AND expires_at <= now() + interval '60 seconds'`,
		auth.HashToken(code)).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("the code is not stored as its hash for 60 s: %d %v", stored, err)
	}
	_, err := e.n.RedeemSSOCode(e.ctx, b.ID, code, "verifier-1", cb)
	expect("code of alpha redeemed for beta", err, ErrSSOCode)
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-1", "https://beta.sap13ns.site/sso/callback")
	expect("another callback", err, ErrSSOCode)
	g, err := e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-1", cb)
	if err != nil || g.AccountID != acct || g.Role != "editor" || g.Operator || g.Username != "ana" {
		t.Fatalf("redeem: %+v, %v", g, err)
	}
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-1", cb)
	expect("replay", err, ErrSSOCode)

	// A wrong verifier uses the code up.
	code = issue(a.ID, "verifier-2")
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "wrong", cb)
	expect("wrong verifier", err, ErrSSOCode)
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-2", cb)
	expect("after a wrong verifier", err, ErrSSOCode)

	code = issue(a.ID, "verifier-3")
	if _, err := e.n.db.Exec(e.ctx, `UPDATE sso_codes SET expires_at = now() - interval '1 second' WHERE code_hash = $1`, auth.HashToken(code)); err != nil {
		t.Fatal(err)
	}
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-3", cb)
	expect("expired", err, ErrSSOCode)

	// The access is checked again when the code is redeemed.
	code = issue(a.ID, "verifier-4")
	if err := e.n.RemoveMember(e.ctx, a.ID, acct); err != nil {
		t.Fatal(err)
	}
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-4", cb)
	expect("membership removed meanwhile", err, ErrSSODenied)
	if err := e.n.SetMember(e.ctx, a.ID, acct, "author", 0); err != nil {
		t.Fatal(err)
	}
	code = issue(a.ID, "verifier-5")
	if err := e.portal.UpdateUserRoleStatus(e.ctx, acct, "subscriber", "disabled"); err != nil {
		t.Fatal(err)
	}
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-5", cb)
	expect("account disabled meanwhile", err, ErrSSODenied)
	if err := e.portal.UpdateUserRoleStatus(e.ctx, acct, "subscriber", "active"); err != nil {
		t.Fatal(err)
	}
	code = issue(a.ID, "verifier-6")
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	_, err = e.n.RedeemSSOCode(e.ctx, a.ID, code, "verifier-6", cb)
	expect("site suspended meanwhile", err, ErrSSODenied)
	if _, err := e.n.IssueSSOCode(e.ctx, SSOCode{SiteID: a.ID, AccountID: acct, Challenge: []byte("short"), RedirectURI: cb}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a malformed challenge: %v", err)
	}
}

func TestSSOCallbackChecks(t *testing.T) {
	e := newNetEnv(t, nil)
	a, bt := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	e.mfa(e.owner)
	if err := e.n.SetMember(e.ctx, bt.ID, e.owner, "admin", 0); err != nil {
		t.Fatal(err)
	}
	sb := e.siteStore(bt)

	// State mismatch: the code is not even tried.
	b := e.browser()
	b.portalSignIn(e.owner)
	cbURL := b.authorize(b.startSSO("alpha.sap13ns.site", "/admin/"))
	forged := "https://alpha.sap13ns.site/sso/callback?" + url.Values{"code": {query(t, cbURL, "code")}, "state": {"x" + query(t, cbURL, "state")[1:]}}.Encode()
	if rec := b.get(forged); rec.Code != http.StatusBadRequest || b.cookie("alpha.sap13ns.site", "__Host-sap13ns") != nil {
		t.Fatalf("state mismatch: %d", rec.Code)
	}
	if audits(t, e.siteStore(a), "sso.failed") != 1 {
		t.Fatal("the failure is not audited")
	}

	// A callback in another browser (no state cookie) fails.
	cbURL = b.authorize(b.startSSO("alpha.sap13ns.site", "/admin/"))
	if rec := e.browser().get(cbURL); rec.Code != http.StatusBadRequest {
		t.Fatalf("callback without the state cookie: %d", rec.Code)
	}

	// A code of alpha on beta's callback, with beta's own state.
	cbA := b.authorize(b.startSSO("alpha.sap13ns.site", "/admin/"))
	authB := b.startSSO("beta.sap13ns.site", "/admin/")
	cross := "https://beta.sap13ns.site/sso/callback?" + url.Values{"code": {query(t, cbA, "code")}, "state": {query(t, authB, "state")}}.Encode()
	if rec := b.get(cross); rec.Code != http.StatusBadRequest || b.cookie("beta.sap13ns.site", "__Host-sap13ns") != nil {
		t.Fatalf("code of alpha on beta: %d", rec.Code)
	}
	if n, _ := sb.CountUsers(e.ctx); n != 0 || audits(t, sb, "sso.failed") != 1 {
		t.Fatalf("beta after a foreign code: %d users", n)
	}
	// The code of alpha still works on alpha: it was never redeemed.
	if rec := b.get(cbA); rec.Code != http.StatusSeeOther {
		t.Fatalf("alpha's code on alpha: %d", rec.Code)
	}

	// Failed callbacks are limited per address.
	c := e.browser()
	c.ip = "192.0.2.99"
	for i := range 10 {
		if rec := c.get("https://beta.sap13ns.site/sso/callback?code=x&state=y"); rec.Code != http.StatusBadRequest {
			t.Fatalf("failure %d: %d", i, rec.Code)
		}
	}
	if rec := c.get("https://beta.sap13ns.site/sso/callback?code=x&state=y"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after 10 failures: %d", rec.Code)
	}
	if audits(t, sb, "sso.throttled") != 1 {
		t.Fatal("throttling not audited")
	}
}

func TestSSOAuthorizeChecks(t *testing.T) {
	e := newNetEnv(t, nil)
	a, bt := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	e.mfa(e.owner)
	b := e.browser()
	b.portalSignIn(e.owner)
	authURL := b.startSSO("alpha.sap13ns.site", "/admin/")
	with := func(key, value string) string {
		u, _ := url.Parse(authURL)
		q := u.Query()
		q.Set(key, value)
		u.RawQuery = q.Encode()
		return u.String()
	}
	for _, tc := range []struct {
		name   string
		target string
		mod    func(*http.Request)
		status int
	}{
		{"foreign redirect_uri", with("redirect_uri", "https://evil.example/sso/callback"), nil, http.StatusBadRequest},
		{"another site's callback", with("redirect_uri", "https://beta.sap13ns.site/sso/callback"), nil, http.StatusBadRequest},
		{"callback with a query", with("redirect_uri", "https://alpha.sap13ns.site/sso/callback?x=1"), nil, http.StatusBadRequest},
		{"plain http", with("redirect_uri", "http://alpha.sap13ns.site/sso/callback"), nil, http.StatusBadRequest},
		{"unknown site", with("site", "9999"), nil, http.StatusNotFound},
		{"the portal itself", with("site", strconv.FormatInt(e.portalID(), 10)), nil, http.StatusNotFound},
		{"bad challenge", with("code_challenge", "abc"), nil, http.StatusBadRequest},
		{"plain challenge method", with("code_challenge_method", "plain"), nil, http.StatusBadRequest},
		{"bad state", with("state", "short"), nil, http.StatusBadRequest},
		{"fetch", authURL, func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "cors") }, http.StatusForbidden},
		{"frame", authURL, func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "iframe") }, http.StatusForbidden},
		{"no Sec-Fetch-Mode", authURL, func(r *http.Request) { r.Header.Del("Sec-Fetch-Mode") }, http.StatusForbidden},
	} {
		var mods []func(*http.Request)
		if tc.mod != nil {
			mods = append(mods, tc.mod)
		}
		rec := b.get(tc.target, mods...)
		if rec.Code != tc.status || rec.Header().Get("Location") != "" {
			t.Errorf("%s: %d (want %d) %s", tc.name, rec.Code, tc.status, rec.Header().Get("Location"))
		}
	}
	var issued int
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM sso_codes`).Scan(&issued); err != nil || issued != 0 {
		t.Fatalf("%d codes issued by refused requests", issued)
	}
	// Each side has only its own endpoints.
	for _, target := range []string{"https://alpha.sap13ns.site/sso/authorize", "https://sap13ns.site/sso/start", "https://sap13ns.site/sso/callback"} {
		if rec := b.get(target); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", target, rec.Code)
		}
	}

	// Not a member, not a network admin.
	stranger := e.account("stranger")
	e.mfa(stranger)
	s := e.browser()
	s.portalSignIn(stranger)
	if rec := s.get(s.startSSO("alpha.sap13ns.site", "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("a stranger: %d", rec.Code)
	}

	// Staff need a second factor on the portal; a subscriber does not.
	ed, sub := e.account("edith"), e.account("subby")
	for acct, role := range map[int64]string{ed: "editor", sub: "subscriber"} {
		if err := e.n.SetMember(e.ctx, a.ID, acct, role, 0); err != nil {
			t.Fatal(err)
		}
	}
	x := e.browser()
	x.portalSignIn(ed)
	rec := x.get(x.startSSO("alpha.sap13ns.site", "/admin/"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("an editor without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	y := e.browser()
	y.portalSignIn(sub)
	if rec := y.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile" {
		t.Fatalf("a subscriber: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// A suspended site refuses everyone; a pending one lets members in.
	if err := e.n.SuspendSite(e.ctx, bt.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	u, _ := url.Parse(authURL)
	q := u.Query()
	q.Set("site", strconv.FormatInt(bt.ID, 10))
	q.Set("redirect_uri", "https://beta.sap13ns.site/sso/callback")
	u.RawQuery = q.Encode()
	if rec := b.get(u.String()); rec.Code != http.StatusForbidden {
		t.Fatalf("a suspended site: %d", rec.Code)
	}
	p := e.tenant("gamma", "personal")
	if p.Status != StatusPending {
		t.Fatalf("personal site is %s", p.Status)
	}
	if rec := b.sso("gamma.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("a pending site: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSSOMembershipAndRoles(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	ed := e.account("edith")
	e.mfa(ed)
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.portalSignIn(ed)
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	u, err := sa.UserByNetworkAccount(e.ctx, ed)
	if err != nil || u.Role != "editor" {
		t.Fatalf("user: %+v %v", u, err)
	}

	// A new role reaches the user at once and ends their sessions.
	if err := e.n.SetMember(e.ctx, a.ID, ed, "author", 0); err != nil {
		t.Fatal(err)
	}
	if u, _ = sa.UserByID(e.ctx, u.ID); u.Role != "author" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("after a role change: %+v", u)
	}
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", rec.Code)
	}

	// The site's admins see the role read-only, and cannot change it.
	local, err := sa.CreateUser(e.ctx, store.NewUser{Username: "boss", Email: "boss@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sa.SetTOTP(e.ctx, local, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	admin := e.browser()
	csrf := admin.siteSignIn(a, local)
	page := admin.get("https://alpha.sap13ns.site/admin/users/" + strconv.FormatInt(u.ID, 10))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `id="role" name="role" disabled`) {
		t.Fatalf("user page: %d", page.Code)
	}
	if users := admin.get("https://alpha.sap13ns.site/admin/users"); !strings.Contains(users.Body.String(), "Cuenta de sap13ns") {
		t.Fatal("the users page does not show the linked user")
	}
	rec := admin.do("POST", "https://alpha.sap13ns.site/admin/users/"+strconv.FormatInt(u.ID, 10), url.Values{"csrf": {csrf},
		"role": {"admin"}, "status": {"disabled"}, "email": {"other@example.com"}, "display_name": {"X"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Role != "author" || got.Status != "active" || got.Email != "edith@example.com" {
		t.Fatalf("a site admin changed a linked user: %+v", got)
	}

	// Removing the membership disables the user and ends its sessions.
	if len(sessions(t, sa, u.ID)) != 1 {
		t.Fatal("no session to revoke")
	}
	if err := e.n.RemoveMember(e.ctx, a.ID, ed); err != nil {
		t.Fatal(err)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("after the removal: %+v", got)
	}
	if rec := b.get("https://alpha.sap13ns.site/admin/"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Fatalf("the removed member's browser: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := b.get(b.startSSO("alpha.sap13ns.site", "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("a removed member signs in again: %d", rec.Code)
	}
	if audits(t, sa, "user.network_access") != 2 {
		t.Fatal("the site's audit log does not show the network's changes")
	}

	// Also on a site that is not loaded.
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "active" || got.Role != "editor" {
		t.Fatalf("a member again: %+v", got)
	}
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	e.unloadAll()
	if e.entry(a.ID) != nil {
		t.Fatal("alpha is still loaded")
	}
	if err := e.n.RemoveMember(e.ctx, a.ID, ed); err != nil {
		t.Fatal(err)
	}
	if e.entry(a.ID) != nil {
		t.Fatal("the removal loaded the site")
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("removed on an unloaded site: %+v", got)
	}
}

func TestSSOAccountDisabled(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	ed := e.account("edith")
	e.mfa(ed)
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.portalSignIn(ed)
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	u, _ := sa.UserByNetworkAccount(e.ctx, ed)

	// A portal admin disables the account on the portal's Users page.
	boss, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "boss", Email: "boss@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	e.mfa(boss)
	if err := e.n.AddNetworkAdmin(e.ctx, boss, 0); err != nil {
		t.Fatal(err)
	}
	pa := e.browser()
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := e.portal.CreateSession(e.ctx, store.Session{TokenHash: auth.HashToken(token), UserID: boss, CSRFToken: csrf,
		ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	pa.jar.SetCookies(&url.URL{Scheme: "https", Host: "sap13ns.site"}, []*http.Cookie{{Name: "__Host-sap13ns", Value: token, Path: "/", Secure: true}})
	rec := pa.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(ed, 10), url.Values{"csrf": {csrf},
		"role": {"subscriber"}, "status": {"disabled"}, "email": {"edith@example.com"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("disable the account: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("the account's user after disabling it: %+v", got)
	}
	// Enabled again, the membership applies again.
	rec = pa.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(ed, 10), url.Values{"csrf": {csrf},
		"role": {"subscriber"}, "status": {"active"}, "email": {"edith@example.com"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("enable the account: %d", rec.Code)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "active" {
		t.Fatalf("after enabling: %+v", got)
	}

	// The last network admin can be neither disabled nor deleted, and an
	// owner of sites cannot be deleted.
	rec = pa.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(boss, 10)+"/delete", url.Values{"csrf": {csrf}})
	if got, err := e.portal.UserByID(e.ctx, boss); err != nil || got.Status != "active" || rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting oneself: %d %v", rec.Code, err)
	}
	rec = pa.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(e.owner, 10)+"/delete", url.Values{"csrf": {csrf}})
	if _, err := e.portal.UserByID(e.ctx, e.owner); err != nil {
		t.Fatalf("the owner of a site was deleted: %d", rec.Code)
	}
	if err := e.n.CheckAccountChange(e.ctx, boss, false); !errors.Is(err, ErrState) {
		t.Fatalf("disabling the last network admin: %v", err)
	}
	// Deleting a member's account revokes their users first.
	rec = pa.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(ed, 10)+"/delete", url.Values{"csrf": {csrf}})
	if _, err := e.portal.UserByID(e.ctx, ed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete the account: %d %v", rec.Code, err)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" {
		t.Fatalf("the deleted account's user: %+v", got)
	}
}

func TestSSOOperator(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	op, other := e.account("olga"), e.account("otto")
	for _, id := range []int64{op, other} {
		if err := e.n.AddNetworkAdmin(e.ctx, id, 0); err != nil {
			t.Fatal(err)
		}
	}
	b := e.browser()
	b.portalSignIn(op)
	// An operator always needs a second factor.
	if rec := b.get(b.startSSO("alpha.sap13ns.site", "/admin/")); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("operator without a second factor: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	e.mfa(op)
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("operator sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := sa.UserByNetworkAccount(e.ctx, op); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("an operator became a linked member")
	}
	u, err := sa.OperatorUser(e.ctx, op)
	if err != nil || !u.NetworkOperator || u.Role != "admin" || u.Username != "operator-olga" ||
		u.Email != "operator-"+strconv.FormatInt(op, 10)+"@operator.invalid" || !strings.Contains(u.DisplayName, "olga") {
		t.Fatalf("operator user: %+v, %v", u, err)
	}
	if list := sessions(t, sa, u.ID); len(list) != 1 || list[0].AMR != store.AMRSSOOperator {
		t.Fatalf("operator session: %+v", list)
	}
	if audits(t, sa, "login.operator") != 1 || audits(t, e.portal, "sso.operator") != 1 {
		t.Fatal("the operator's sign-in is not in both audit logs")
	}
	if rec := b.get("https://alpha.sap13ns.site/admin/audit"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "login.operator") {
		t.Fatalf("the site's audit log: %d", rec.Code)
	}
	if rec := b.get("https://alpha.sap13ns.site/admin/users"); !strings.Contains(rec.Body.String(), "Operador de la red") {
		t.Fatal("the users page does not tell the operator apart")
	}

	// No longer a network admin: the operator user is disabled at once.
	if err := e.n.RemoveNetworkAdmin(e.ctx, op); err != nil {
		t.Fatal(err)
	}
	if got, _ := sa.UserByID(e.ctx, u.ID); got.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0 {
		t.Fatalf("the operator after the revocation: %+v", got)
	}
	if rec := b.get(b.startSSO("alpha.sap13ns.site", "/admin/")); rec.Code != http.StatusForbidden {
		t.Fatalf("a former network admin: %d", rec.Code)
	}

	// A suspended site refuses operators too.
	e.mfa(other)
	if err := e.n.SuspendSite(e.ctx, a.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	c := e.browser()
	c.portalSignIn(other)
	sum := sha256.Sum256([]byte("v"))
	target := "https://sap13ns.site/sso/authorize?" + url.Values{"site": {strconv.FormatInt(a.ID, 10)}, "state": {auth.NewToken(24)},
		"code_challenge": {b64(sum[:])}, "redirect_uri": {"https://alpha.sap13ns.site/sso/callback"}}.Encode()
	if rec := c.get(target); rec.Code != http.StatusForbidden {
		t.Fatalf("operator on a suspended site: %d", rec.Code)
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestSSOLinking(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	ed := e.account("edith")
	e.mfa(ed)
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	// A local user has the account's e-mail address: never linked by it.
	local, err := sa.CreateUser(e.ctx, store.NewUser{Username: "eddie", Email: "EDITH@example.com", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	b := e.browser()
	b.portalSignIn(ed)
	if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusConflict || b.cookie("alpha.sap13ns.site", "__Host-sap13ns") != nil {
		t.Fatalf("sign-in onto a local user's e-mail: %d", rec.Code)
	}
	if u, _ := sa.UserByID(e.ctx, local); u.Linked() {
		t.Fatal("a local user was linked by e-mail")
	}
	if n, _ := sa.CountUsers(e.ctx); n != 1 {
		t.Fatalf("%d users", n)
	}

	// The local user links their account explicitly from their profile.
	if err := sa.SetTOTP(e.ctx, local, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	csrf := b.siteSignIn(a, local)
	if rec := b.get("https://alpha.sap13ns.site/admin/profile"); !strings.Contains(rec.Body.String(), "/admin/profile/network/link") {
		t.Fatal("the profile offers no link")
	}
	rec := b.do("POST", "https://alpha.sap13ns.site/admin/profile/network/link", url.Values{"csrf": {csrf}})
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "https://sap13ns.site/sso/authorize?") {
		t.Fatalf("link: %d %s", rec.Code, loc)
	} else if rec := b.get(b.authorize(loc)); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile" {
		t.Fatalf("link callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	u, err := sa.UserByNetworkAccount(e.ctx, ed)
	if err != nil || u.ID != local || u.Role != "editor" || u.SSOOnly() || u.Email != "edith@example.com" {
		t.Fatalf("linked user: %+v %v", u, err)
	}
	if audits(t, sa, "user.network_linked") != 1 {
		t.Fatal("the link is not audited")
	}
	// From now on the account signs in as that user.
	c := e.browser()
	c.portalSignIn(ed)
	if rec := c.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in after the link: %d", rec.Code)
	}
	if n, _ := sa.CountUsers(e.ctx); n != 1 {
		t.Fatalf("%d users after the link", n)
	}
	// A linked user has nothing to link.
	d := e.browser()
	csrf = d.siteSignIn(a, local)
	if rec := d.get("https://alpha.sap13ns.site/admin/profile"); strings.Contains(rec.Body.String(), "/admin/profile/network/link") {
		t.Fatal("a linked user is offered a link")
	}
	rec = d.do("POST", "https://alpha.sap13ns.site/admin/profile/network/link", url.Values{"csrf": {csrf}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile" {
		t.Fatalf("linking a linked user: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// The link is bound to the user who asked for it: another user signed
	// in meanwhile is refused.
	other, err := sa.CreateUser(e.ctx, store.NewUser{Username: "olivia", Email: "olivia@example.com", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sa.SetTOTP(e.ctx, other, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	f := e.browser()
	f.portalSignIn(e.owner)
	e.mfa(e.owner)
	csrf = f.siteSignIn(a, other)
	rec = f.do("POST", "https://alpha.sap13ns.site/admin/profile/network/link", url.Values{"csrf": {csrf}})
	cb := f.authorize(rec.Header().Get("Location"))
	f.siteSignIn(a, local)
	if rec := f.get(cb); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile" {
		t.Fatalf("a link finished by another user: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if u, _ := sa.UserByID(e.ctx, other); u.Linked() {
		t.Fatal("the link went to a user who did not finish it")
	}
	if audits(t, sa, "sso.link_refused") != 1 {
		t.Fatal("the refused link is not audited")
	}
}

// TestReconcileAccess: a revocation that never reached a site (here the
// membership row is deleted behind the network's back) is applied by the
// daily maintenance, without loading the site.
func TestReconcileAccess(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	ed, op := e.account("edith"), e.account("olga")
	e.mfa(ed)
	e.mfa(op)
	if err := e.n.SetMember(e.ctx, a.ID, ed, "editor", 0); err != nil {
		t.Fatal(err)
	}
	if err := e.n.AddNetworkAdmin(e.ctx, op, 0); err != nil {
		t.Fatal(err)
	}
	for _, acct := range []int64{ed, op} {
		b := e.browser()
		b.portalSignIn(acct)
		if rec := b.sso("alpha.sap13ns.site", "/admin/"); rec.Code != http.StatusSeeOther {
			t.Fatalf("sign-in of %d: %d", acct, rec.Code)
		}
	}
	member, _ := sa.UserByNetworkAccount(e.ctx, ed)
	operator, _ := sa.OperatorUser(e.ctx, op)
	e.unloadAll()
	if _, err := e.n.db.Exec(e.ctx, `DELETE FROM site_members WHERE account_id = $1`, ed); err != nil {
		t.Fatal(err)
	}
	if _, err := e.n.db.Exec(e.ctx, `DELETE FROM network_admins WHERE account_id = $1`, op); err != nil {
		t.Fatal(err)
	}
	e.reg.Maintain(e.ctx)
	for _, id := range []int64{member.ID, operator.ID} {
		if u, _ := sa.UserByID(e.ctx, id); u.Status != "disabled" || len(sessions(t, sa, id)) != 0 {
			t.Fatalf("after the sweep: %+v", u)
		}
	}
	if got := e.reg.Loaded(); len(got) != 1 {
		t.Fatalf("the sweep loaded sites: %v", got)
	}
}
