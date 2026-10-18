package store_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

func TestPasskeys(t *testing.T) {
	// testutil drops and recreates the schema: only run against a database
	// that was explicitly configured for tests.
	if os.Getenv("SAP13NS_TEST_DATABASE_URL") == "" {
		t.Skip("SAP13NS_TEST_DATABASE_URL not set")
	}
	st := testutil.Store(t)
	ctx := context.Background()
	alice, err := st.CreateUser(ctx, store.NewUser{Username: "alice", Email: "alice@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser(ctx, store.NewUser{Username: "bob", Email: "bob@example.com", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}

	id, err := st.AddPasskey(ctx, alice, []byte{1, 2, 3}, "Laptop", []byte(`{"id":"AQID","authenticator":{"signCount":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddPasskey(ctx, bob, []byte{1, 2, 3}, "Clone", []byte(`{}`)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate credential id: err = %v, want ErrConflict", err)
	}
	if _, err := st.AddPasskey(ctx, alice, []byte{4, 5}, "Phone", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}

	u, err := st.UserByID(ctx, alice)
	if err != nil || u.PasskeyCount != 2 {
		t.Fatalf("PasskeyCount = %v (err %v), want 2", u.PasskeyCount, err)
	}
	list, err := st.ListPasskeys(ctx, alice)
	if err != nil || len(list) != 2 || list[0].Name != "Laptop" || list[0].LastUsedAt != nil {
		t.Fatalf("ListPasskeys = %+v, %v", list, err)
	}

	k, err := st.PasskeyByCredentialID(ctx, []byte{1, 2, 3})
	if err != nil || k.ID != id || k.UserID != alice {
		t.Fatalf("PasskeyByCredentialID = %+v, %v", k, err)
	}
	if _, err := st.PasskeyByCredentialID(ctx, []byte{9}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown credential: err = %v", err)
	}

	if err := st.RenamePasskey(ctx, bob, id, "Mine now"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rename by another user: err = %v, want ErrNotFound", err)
	}
	if err := st.RenamePasskey(ctx, alice, id, "Work laptop"); err != nil {
		t.Fatal(err)
	}

	if err := st.RecordPasskeyUse(ctx, id, []byte(`{"id":"AQID","authenticator":{"signCount":7}}`)); err != nil {
		t.Fatal(err)
	}
	k, _ = st.PasskeyByCredentialID(ctx, []byte{1, 2, 3})
	if k.Name != "Work laptop" || k.LastUsedAt == nil || !strings.Contains(string(k.Data), `"signCount": 7`) {
		t.Fatalf("after use: %+v data=%s", k, k.Data)
	}

	if _, err := st.DeletePasskey(ctx, bob, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete by another user: err = %v, want ErrNotFound", err)
	}
	deleted, err := st.DeletePasskey(ctx, alice, id)
	if err != nil || deleted.Name != "Work laptop" {
		t.Fatalf("DeletePasskey = %+v, %v", deleted, err)
	}
	if n, err := st.DeleteUserPasskeys(ctx, alice); err != nil || n != 1 {
		t.Fatalf("DeleteUserPasskeys = %d, %v; want 1", n, err)
	}
	if u, _ := st.UserByID(ctx, alice); u.PasskeyCount != 0 {
		t.Fatalf("PasskeyCount = %d after deleting all", u.PasskeyCount)
	}
}
