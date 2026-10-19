package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// The System page of a single site says how a network of sites is
// started; a site of a network does not.
func TestSystemMultisiteCard(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	id, err := st.CreateUser(ctx, store.NewUser{Username: "ada", Email: "ada@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTOTP(ctx, id, []byte("sealed"), true, nil); err != nil {
		t.Fatal(err)
	}
	system := func(s *Server) string {
		t.Helper()
		token := auth.NewToken(32)
		if err := st.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: id, CSRFToken: "c",
			ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "https://blog.sap13ns.site/admin/system", nil)
		req.AddCookie(&http.Cookie{Name: s.cookieName(), Value: token})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("system page: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		return rec.Body.String()
	}

	body := system(networkServer(t, st, nil))
	for _, want := range []string{`id="multisite"`, "Varios sitios", "Esta instalación tiene un solo sitio.", "sap13ns network init",
		"Todavía no se puede convertir este sitio en una red",
		`href="https://github.com/vicktor/sap13ns/blob/main/docs/MULTISITE.md" target="_blank" rel="noopener"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the System page of a single site lacks %s", want)
		}
	}

	body = system(networkServer(t, st, func(_ *config.Config, d *Deps) { d.Network = &fakeHooks{} }))
	if strings.Contains(body, `id="multisite"`) || strings.Contains(body, "network init") {
		t.Error("the System page of a site of a network explains how to start a network")
	}
}
