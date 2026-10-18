package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

func TestNetworkLink(t *testing.T) {
	if os.Getenv("SAP13NS_TEST_DATABASE_URL") == "" {
		t.Skip("SAP13NS_TEST_DATABASE_URL not set")
	}
	st := testutil.Store(t)
	ctx := context.Background()

	local, err := st.CreateUser(ctx, store.NewUser{Username: "alice", Email: "alice@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.UserByID(ctx, local)
	if err != nil || u.NetworkAccountID != nil || u.SSOOnly() {
		t.Fatalf("local user: %+v, %v", u, err)
	}
	if _, err := st.UserByNetworkAccount(ctx, 41); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unlinked account: err = %v", err)
	}

	id, err := st.CreateSSOUser(ctx, store.NewSSOUser{NetworkAccountID: 41, Username: "bob", Email: "bob@example.com", DisplayName: "Bob", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	u, err = st.UserByNetworkAccount(ctx, 41)
	if err != nil || u.ID != id || u.Username != "bob" || u.Role != "editor" || u.DisplayName != "Bob" ||
		u.NetworkAccountID == nil || *u.NetworkAccountID != 41 || !u.SSOOnly() || u.PasswordHash != store.SSOPasswordHash {
		t.Fatalf("SSO user: %+v, %v", u, err)
	}
	// The SSO hash never verifies, whatever is typed.
	for _, pw := range []string{"", "!sso", "sso", u.PasswordHash} {
		if auth.VerifyPassword(u.PasswordHash, pw) {
			t.Fatalf("the SSO password hash accepted %q", pw)
		}
	}

	for _, tc := range []struct {
		n    store.NewSSOUser
		want string
	}{
		{store.NewSSOUser{NetworkAccountID: 41, Username: "bob2", Email: "bob2@example.com", Role: "author"}, "network_account_id"},
		{store.NewSSOUser{NetworkAccountID: 42, Username: "bob", Email: "other@example.com", Role: "author"}, "users_username_key"},
		{store.NewSSOUser{NetworkAccountID: 42, Username: "alice2", Email: "ALICE@example.com", Role: "author"}, "users_email_key"},
	} {
		if _, err := st.CreateSSOUser(ctx, tc.n); !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("CreateSSOUser(%+v): err = %v, want a conflict on %s", tc.n, err, tc.want)
		}
	}

	// A local user links explicitly, once, to an account nobody else has.
	if err := st.LinkNetworkAccount(ctx, local, 41); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("linking a taken account: err = %v", err)
	}
	if err := st.LinkNetworkAccount(ctx, local, 43); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkNetworkAccount(ctx, local, 44); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("relinking: err = %v", err)
	}
	if err := st.LinkNetworkAccount(ctx, 9999, 45); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing user: err = %v", err)
	}
	u, err = st.UserByNetworkAccount(ctx, 43)
	if err != nil || u.ID != local || u.SSOOnly() {
		t.Fatalf("linked local user: %+v, %v", u, err)
	}
	users, _, err := st.ListUsers(ctx, store.UserListQuery{Limit: 10})
	if err != nil || len(users) != 2 || users[0].NetworkAccountID == nil || *users[0].NetworkAccountID != 43 {
		t.Fatalf("ListUsers = %+v, %v", users, err)
	}

	// Sessions record how they were authenticated.
	expires := time.Now().Add(time.Hour)
	for i, amr := range []string{store.AMRLocal, store.AMRSSO, store.AMRSSOOperator} {
		hash := auth.HashToken(amr + "token")
		if err := st.CreateSession(ctx, store.Session{TokenHash: hash, UserID: id, CSRFToken: "c", ExpiresAt: expires, AMR: amr}); err != nil {
			t.Fatal(err)
		}
		sess, err := st.SessionByHash(ctx, hash)
		if err != nil || sess.AMR != amr {
			t.Fatalf("session %d: %+v, %v", i, sess, err)
		}
	}
	if err := st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken("bad"), UserID: id, CSRFToken: "c", ExpiresAt: expires, AMR: "password"}); err == nil {
		t.Fatal("CreateSession accepted an unknown amr")
	}
	list, err := st.ListUserSessions(ctx, id)
	if err != nil || len(list) != 3 {
		t.Fatalf("ListUserSessions = %+v, %v", list, err)
	}
}
