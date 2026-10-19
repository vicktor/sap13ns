package network

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// A portal admin who is not a network admin cannot take over the account
// of a network admin, which opens every site as an operator.
func TestPortalProtectsNetworkAdmins(t *testing.T) {
	e := newNetEnv(t, nil)
	e.tenant("alpha", "pro")
	_, bb, bcsrf := e.networkAdmin("boss")
	hash, err := auth.HashPassword("an old and long password")
	if err != nil {
		t.Fatal(err)
	}
	nadia, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "nadia", Email: "nadia@example.com", Role: "admin", PasswordHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	e.mfa(nadia)
	if err := e.n.AddNetworkAdmin(e.ctx, nadia, 0); err != nil {
		t.Fatal(err)
	}
	nb := e.browser()
	nb.portalSignIn(nadia)
	pam, err := e.portal.CreateUser(e.ctx, store.NewUser{Username: "pam", Email: "pam@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	e.mfa(pam)
	pb := e.browser()
	csrf := pb.portalSignIn(pam)
	ordy := e.account("ordy")

	userURL := "https://sap13ns.site/admin/users/" + strconv.FormatInt(nadia, 10)
	form := func(change map[string]string) url.Values {
		v := url.Values{"csrf": {csrf}, "role": {"admin"}, "status": {"active"}, "email": {"nadia@example.com"}, "display_name": {""}}
		for k, x := range change {
			v.Set(k, x)
		}
		return v
	}
	for name, rec := range map[string]func() int{
		"set the password": func() int {
			return pb.do("POST", userURL, form(map[string]string{"password": "a new and long password", "password_confirm": "a new and long password"})).Code
		},
		"change the e-mail": func() int {
			return pb.do("POST", userURL, form(map[string]string{"email": "pam+nadia@example.com"})).Code
		},
		"disable":            func() int { return pb.do("POST", userURL, form(map[string]string{"status": "disabled"})).Code },
		"demote":             func() int { return pb.do("POST", userURL, form(map[string]string{"role": "subscriber"})).Code },
		"edit the biography": func() int { return pb.do("POST", userURL, form(map[string]string{"bio": "hello"})).Code },
		"reset 2FA":          func() int { return pb.do("POST", userURL+"/reset-2fa", url.Values{"csrf": {csrf}}).Code },
		"close the sessions": func() int { return pb.do("POST", userURL+"/logout", url.Values{"csrf": {csrf}}).Code },
		"unlock":             func() int { return pb.do("POST", userURL+"/unlock", url.Values{"csrf": {csrf}}).Code },
		"delete":             func() int { return pb.do("POST", userURL+"/delete", url.Values{"csrf": {csrf}}).Code },
	} {
		if code := rec(); code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	got, err := e.portal.UserByID(e.ctx, nadia)
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != hash || got.Email != "nadia@example.com" || !got.TOTPEnabled || got.Status != "active" ||
		got.Role != "admin" || got.Bio != "" {
		t.Fatalf("the network admin's account changed: %+v", got)
	}
	if n := len(sessions(t, e.portal, nadia)); n != 1 {
		t.Fatalf("the network admin has %d sessions", n)
	}
	if n := audits(t, e.portal, "user.change_refused"); n != 9 {
		t.Fatalf("%d refusals audited", n)
	}
	// The page shows the account without its controls.
	page := pb.get(userURL)
	for _, action := range []string{"/reset-2fa", "/logout", "/delete", "/unlock"} {
		if strings.Contains(page.Body.String(), `action="/admin/users/`+strconv.FormatInt(nadia, 10)+action+`"`) {
			t.Errorf("the page offers %s", action)
		}
	}
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), `type="submit"><svg xmlns="http://www.w3.org/2000/svg" class="lucide lucide-save"`) {
		t.Fatalf("user page: %d", page.Code)
	}
	if admin, err := e.n.IsNetworkAdmin(e.ctx, pam); err != nil || admin {
		t.Fatalf("pam became a network admin: %v %v", admin, err)
	}

	// Other accounts stay theirs to manage.
	rec := pb.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(ordy, 10), url.Values{"csrf": {csrf},
		"role": {"subscriber"}, "status": {"active"}, "email": {"ordy2@example.com"}})
	if o, _ := e.portal.UserByID(e.ctx, ordy); rec.Code != http.StatusSeeOther || o.Email != "ordy2@example.com" {
		t.Fatalf("editing an ordinary account: %d", rec.Code)
	}
	// And another network admin may change a network admin.
	if rec := bb.do("POST", userURL+"/reset-2fa", url.Values{"csrf": {bcsrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("a network admin resetting 2FA: %d", rec.Code)
	}
	if got, _ := e.portal.UserByID(e.ctx, nadia); got.TOTPEnabled {
		t.Fatal("2FA not reset by a network admin")
	}
}

// A code minted just before its account is disabled or deleted, or stops
// being a network admin, cannot be redeemed, and leaves no active user.
func TestSSORevokedWhileSigningIn(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	_, bb, bcsrf := e.networkAdmin("boss")
	ed, dora := e.account("edith"), e.account("dora")
	olga, _, _ := e.networkAdmin("olga")
	for id, role := range map[int64]string{ed: "editor", dora: "author"} {
		e.mfa(id)
		if err := e.n.SetMember(e.ctx, a.ID, id, role, 0); err != nil {
			t.Fatal(err)
		}
	}
	const host = "alpha.sap13ns.site"
	// mint returns a callback with a fresh code for an account.
	mint := func(account int64) (*browser, string) {
		b := e.browser()
		b.portalSignIn(account)
		return b, b.authorize(b.startSSO(host, "/admin/"))
	}
	noActiveUser := func(name string, account int64, operator bool) {
		t.Helper()
		lookup := sa.UserByNetworkAccount
		if operator {
			lookup = sa.OperatorUser
		}
		u, err := lookup(e.ctx, account)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			t.Fatal(err)
		case u.Status != "disabled" || len(sessions(t, sa, u.ID)) != 0:
			t.Errorf("%s: the user is %s with %d sessions", name, u.Status, len(sessions(t, sa, u.ID)))
		}
	}
	edit := func(account int64, status string) {
		t.Helper()
		var email string
		if err := e.portal.DB.QueryRow(e.ctx, `SELECT email FROM users WHERE id = $1`, account).Scan(&email); err != nil {
			t.Fatal(err)
		}
		rec := bb.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(account, 10), url.Values{"csrf": {bcsrf},
			"role": {"subscriber"}, "status": {status}, "email": {email}})
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("set the account %d %s: %d %s", account, status, rec.Code, rec.Body.String())
		}
	}

	// A member who signed in before: their user exists and is active.
	if rec := e.browser().signedInSSO(ed, host); rec.Code != http.StatusSeeOther {
		t.Fatalf("first sign-in: %d", rec.Code)
	}
	b, callback := mint(ed)
	edit(ed, "disabled")
	if rec := b.get(callback); rec.Code != http.StatusForbidden {
		t.Errorf("redeemed after the account was disabled: %d", rec.Code)
	}
	noActiveUser("disabled member", ed, false)

	// A member whose account is deleted: the code goes with it.
	b, callback = mint(dora)
	if rec := bb.do("POST", "https://sap13ns.site/admin/users/"+strconv.FormatInt(dora, 10)+"/delete", url.Values{"csrf": {bcsrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete the account: %d", rec.Code)
	}
	if rec := b.get(callback); rec.Code == http.StatusSeeOther {
		t.Error("redeemed after the account was deleted")
	}
	noActiveUser("deleted member", dora, false)

	// An operator disabled, and one no longer a network admin.
	b, callback = mint(olga)
	edit(olga, "disabled")
	if rec := b.get(callback); rec.Code != http.StatusForbidden {
		t.Errorf("an operator code redeemed after the account was disabled: %d", rec.Code)
	}
	noActiveUser("disabled operator", olga, true)
	edit(olga, "active")
	b, callback = mint(olga)
	if err := e.n.RemoveNetworkAdmin(e.ctx, olga); err != nil {
		t.Fatal(err)
	}
	if rec := b.get(callback); rec.Code != http.StatusForbidden {
		t.Errorf("an operator code redeemed after the admin role was revoked: %d", rec.Code)
	}
	noActiveUser("revoked operator", olga, true)

	// Every refused code was used.
	var unused int
	if err := e.n.db.QueryRow(e.ctx, `SELECT count(*) FROM sso_codes WHERE used_at IS NULL`).Scan(&unused); err != nil || unused != 0 {
		t.Fatalf("%d codes left unused (%v)", unused, err)
	}
}

// signedInSSO signs an account in to a site through the portal on this
// browser.
func (b *browser) signedInSSO(account int64, host string) *httptest.ResponseRecorder {
	b.e.t.Helper()
	b.portalSignIn(account)
	return b.sso(host, "/admin/")
}
