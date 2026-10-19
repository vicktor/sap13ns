package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// fakeSSO is the SSO client of a tenant whose portal grants a fixed
// account, and whose second check of the grant answers role (the grant's
// when empty) and check.
type fakeSSO struct {
	grant SSOGrant
	role  string
	check error
}

func (f *fakeSSO) Wakeup(context.Context, time.Time) {}

func (f *fakeSSO) SSOAuthorize() (string, int64) { return "https://sap13ns.site/sso/authorize", 7 }

func (f *fakeSSO) RedeemSSO(_ context.Context, code, _, _ string) (*SSOGrant, error) {
	if code != "good-code" {
		return nil, ErrSSOInvalid
	}
	g := f.grant
	return &g, nil
}

func (f *fakeSSO) CheckSSO(_ context.Context, g *SSOGrant) (string, error) {
	if f.check != nil {
		return "", f.check
	}
	if f.role != "" {
		return f.role, nil
	}
	return g.Role, nil
}

// ssoSignIn runs /sso/start and then /sso/callback with the code the
// portal would have sent.
func ssoSignIn(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "https://blog.sap13ns.site/sso/start?next=/admin/", nil))
	loc, err := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || err != nil {
		t.Fatalf("start: %d %v", rec.Code, err)
	}
	req := httptest.NewRequest("GET", "https://blog.sap13ns.site/sso/callback?code=good-code&state="+url.QueryEscape(loc.Query().Get("state")), nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A revocation that runs between the redemption of a code and the sync of
// its user would be undone by the sync: the callback checks the grant
// again once the user is signed in, and revokes the user if it no longer
// holds.
func TestSSORevokedAfterRedemption(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	sso := &fakeSSO{grant: SSOGrant{AccountID: 42, Role: "editor", Username: "nina", Email: "nina@example.com", DisplayName: "Nina"}}
	s := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sso })
	h := s.Handler()

	if rec := ssoSignIn(t, h); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}
	u, err := st.UserByNetworkAccount(ctx, 42)
	if err != nil || u.Status != "active" || u.Role != "editor" {
		t.Fatalf("the user: %+v %v", u, err)
	}

	// The network revoked the account (its user here is disabled) while
	// the next sign-in was between its redemption and its sync.
	sso.check = errors.Join(ErrSSODenied, errors.New("the account is disabled"))
	if _, _, err := st.SetNetworkAccess(ctx, 42, false, "", "disabled"); err != nil {
		t.Fatal(err)
	}
	if rec := ssoSignIn(t, h); rec.Code != http.StatusForbidden {
		t.Fatalf("sign-in after the revocation: %d", rec.Code)
	}
	u, err = st.UserByID(ctx, u.ID)
	if err != nil || u.Status != "disabled" {
		t.Fatalf("the user after the refused sign-in: %+v %v", u, err)
	}
	if list, err := st.ListUserSessions(ctx, u.ID); err != nil || len(list) != 0 {
		t.Fatalf("%d sessions left (%v)", len(list), err)
	}

	// A check that fails fails closed.
	sso.check = errors.New("database unavailable")
	if rec := ssoSignIn(t, h); rec.Code != http.StatusInternalServerError {
		t.Fatalf("sign-in with a failed check: %d", rec.Code)
	}
	u, err = st.UserByID(ctx, u.ID)
	if err != nil || u.Status != "disabled" {
		t.Fatalf("the user after a failed check: %+v %v", u, err)
	}
	if list, err := st.ListUserSessions(ctx, u.ID); err != nil || len(list) != 0 {
		t.Fatalf("%d sessions left after a failed check (%v)", len(list), err)
	}
	var refused int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'sso.failed' AND details->>'reason' = 'revoked during the sign-in'`).Scan(&refused); err != nil || refused != 1 {
		t.Fatalf("%d refusals audited (%v)", refused, err)
	}

	// The next sign-in through the portal restores the user.
	sso.check = nil
	if rec := ssoSignIn(t, h); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in once the grant holds again: %d", rec.Code)
	}
	if u, err = st.UserByID(ctx, u.ID); err != nil || u.Status != "active" {
		t.Fatalf("the user after signing in again: %+v %v", u, err)
	}
	if list, err := st.ListUserSessions(ctx, u.ID); err != nil || len(list) != 1 || list[0].AMR != store.AMRSSO {
		t.Fatalf("sessions after signing in again: %+v %v", list, err)
	}
}

// A role lowered between the redemption and the sync is not undone by the
// sync: the sign-in is refused and the user gets the current role, with no
// session, so they sign in again with it.
func TestSSORoleLoweredAfterRedemption(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	sso := &fakeSSO{grant: SSOGrant{AccountID: 43, Role: "admin", Username: "ada", Email: "ada@example.com"}}
	s := networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = sso })
	h := s.Handler()
	if rec := ssoSignIn(t, h); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in: %d", rec.Code)
	}
	u, err := st.UserByNetworkAccount(ctx, 43)
	if err != nil || u.Role != "admin" {
		t.Fatalf("the user: %+v %v", u, err)
	}

	// The membership became author, and the network applied it, while the
	// next sign-in still held a grant for admin.
	if _, _, err := st.SetNetworkAccess(ctx, 43, false, "author", "active"); err != nil {
		t.Fatal(err)
	}
	sso.role = "author"
	if rec := ssoSignIn(t, h); rec.Code != http.StatusForbidden {
		t.Fatalf("sign-in with a lowered role: %d", rec.Code)
	}
	if u, err = st.UserByID(ctx, u.ID); err != nil || u.Role != "author" || u.Status != "active" {
		t.Fatalf("the user after the refused sign-in: %+v %v", u, err)
	}
	if list, err := st.ListUserSessions(ctx, u.ID); err != nil || len(list) != 0 {
		t.Fatalf("%d sessions left (%v)", len(list), err)
	}
	var refused int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'sso.failed' AND details->>'reason' = 'role changed during the sign-in'`).Scan(&refused); err != nil || refused != 1 {
		t.Fatalf("%d refusals audited (%v)", refused, err)
	}

	// The next grant has the current role.
	sso.grant.Role = "author"
	if rec := ssoSignIn(t, h); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in with the current role: %d", rec.Code)
	}
	if u, err = st.UserByID(ctx, u.ID); err != nil || u.Role != "author" {
		t.Fatalf("the user: %+v %v", u, err)
	}
}
