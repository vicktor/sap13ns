package web

import (
	"bytes"
	"context"
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"sap13ns/internal/netguard"
	"sap13ns/internal/store"
)

// Settings → E-mail: a site's own SMTP server is validated, its password
// is kept encrypted and only for the server and account it was given for,
// and the site's e-mail goes through it.
func TestSiteMail(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	if rec := e.get("/admin/settings/mail", e.admin); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="host"`) {
		t.Fatalf("GET: %d", rec.Code)
	}
	if rec := e.get("/admin/settings/mail", e.author); rec.Code == http.StatusOK {
		t.Error("an author opened the e-mail settings")
	}
	if e.s.mail.Own() || e.s.mail.Enabled() {
		t.Fatal("a site without SMTP settings has a mailer")
	}

	good := func() url.Values {
		return url.Values{"enabled": {"1"}, "host": {"smtp.example.com"}, "port": {"587"}, "username": {"web@example.com"},
			"password": {"s3cret pass"}, "from": {"Reonoa <hola@example.com>"}}
	}
	for name, change := range map[string]func(url.Values){
		"a scheme":       func(v url.Values) { v.Set("host", "https://smtp.example.com") },
		"a port in host": func(v url.Values) { v.Set("host", "smtp.example.com:25") },
		"an IP address":  func(v url.Values) { v.Set("host", "10.0.0.1") },
		"another port":   func(v url.Values) { v.Set("port", "22") },
		"a bad sender":   func(v url.Values) { v.Set("from", "not an address") },
		"a header":       func(v url.Values) { v.Set("from", "a@example.com\r\nBcc: b@example.com") },
		"no server":      func(v url.Values) { v.Set("host", "") },
	} {
		v := good()
		change(v)
		if rec := e.postForm("/admin/settings/mail", v); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if st, _ := e.st.MailSettings(ctx); st.Enabled || st.Host != "" {
		t.Fatalf("a refused form was saved: %+v", st)
	}

	if rec := e.postForm("/admin/settings/mail", good()); rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	st, err := e.st.MailSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Host != "smtp.example.com" || st.Port != 587 || st.From != `"Reonoa" <hola@example.com>` {
		t.Errorf("saved %+v", st)
	}
	if len(st.PasswordEnc) == 0 || bytes.Contains(st.PasswordEnc, []byte("s3cret")) {
		t.Fatal("the password is not kept encrypted")
	}
	if p, err := e.s.keys.Decrypt(st.PasswordEnc, []byte(mailPasswordAD)); err != nil || string(p) != "s3cret pass" {
		t.Fatalf("password %q %v", p, err)
	}
	if !e.s.mail.Own() || !e.s.mail.Enabled() || e.s.mail.Server() != "smtp.example.com:587" {
		t.Fatalf("the site does not send through its server: %q", e.s.mail.Server())
	}
	if body := e.get("/admin/settings/mail", e.admin).Body.String(); strings.Contains(body, "s3cret") {
		t.Error("the page shows the password")
	}

	v := good()
	v.Del("password")
	e.postForm("/admin/settings/mail", v)
	if st, _ := e.st.MailSettings(ctx); len(st.PasswordEnc) == 0 {
		t.Error("an empty password field removed the saved one")
	}
	v.Set("username", "other@example.com")
	e.postForm("/admin/settings/mail", v)
	if st, _ := e.st.MailSettings(ctx); len(st.PasswordEnc) != 0 {
		t.Error("the password stayed for another account")
	}

	v.Del("enabled")
	e.postForm("/admin/settings/mail", v)
	if e.s.mail.Own() {
		t.Error("switched off, the site still sends through its server")
	}
	v.Set("test", "1")
	off := e.s.i18n.T(e.s.lang(), "Saved, but switched off: turn on «Send through this server» to send through %s, then test it.", "smtp.example.com")
	if rec := e.postForm("/admin/settings/mail", v); !strings.Contains(rec.Body.String(), html.EscapeString(off)) {
		t.Errorf("a test with the server switched off does not say so: %d", rec.Code)
	}
	v.Del("test")

	// Outside development the server is dialled through netguard.
	e.s.cfg.Dev = false
	defer func() { e.s.cfg.Dev = true }()
	m, err := e.s.siteMailerFor(store.MailSettings{Enabled: true, Host: "localhost", Port: 587, From: "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send("b@example.com", "x", "y"); !errors.Is(err, netguard.ErrForbidden) {
		t.Errorf("a local server: %v", err)
	}
}
