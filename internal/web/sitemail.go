package web

// Settings → E-mail: a site may send its e-mail (password resets, plugin
// forms) through an SMTP server of its own, from its own address, instead
// of the server's (SAP13NS_SMTP_*; the network's on a network). The
// password is kept encrypted with the server secret, and the server is
// dialled through netguard: a site's administrator, a customer on a
// network, must not reach the server's internal networks with it.
// Messages the network sends about a site (billing, domains, suspension)
// keep using the network's server.

import (
	"context"
	"errors"
	"net"
	"net/http"
	netmail "net/mail"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/netguard"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
)

// mailPorts are the SMTP ports a site's server may use: submission with
// STARTTLS (587, and 2525 that some providers offer), implicit TLS (465)
// and 25, which still requires STARTTLS.
var mailPorts = []int{587, 465, 2525, 25}

// mailHostRe is a host name: letters, digits, hyphens and dots, no scheme
// and no port.
var mailHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

const mailPasswordAD = "site-smtp-password"

// siteMailer sends the site's e-mail: through the site's own server when
// one is set, through the server's otherwise. It is what the plugins'
// mail_send uses too.
type siteMailer struct {
	base *mail.Mailer // the server's; nil in some tests
	own  atomic.Pointer[mail.Mailer]
}

func (m *siteMailer) current() *mail.Mailer {
	if o := m.own.Load(); o != nil {
		return o
	}
	return m.base
}

func (m *siteMailer) Enabled() bool {
	c := m.current()
	return c != nil && c.Enabled()
}

// Own reports whether the site sends through a server of its own.
func (m *siteMailer) Own() bool { return m.own.Load() != nil }

func (m *siteMailer) Server() string {
	if c := m.current(); c != nil {
		return c.Server()
	}
	return ""
}

func (m *siteMailer) Send(to, subject, body string) error {
	return m.SendMessage(to, "", subject, body)
}

func (m *siteMailer) SendMessage(to, replyTo, subject, body string) error {
	c := m.current()
	if c == nil {
		return errors.New("SMTP is not configured")
	}
	return c.SendMessage(to, replyTo, subject, body)
}

// SiteMailer is the site's mailer, for the plugins' mail_send: the site's
// own SMTP server when Settings → E-mail has one, the server's otherwise.
func (s *Server) SiteMailer() plugin.Mailer { return s.mail }

// loadSiteMail puts the site's SMTP settings in use.
func (s *Server) loadSiteMail(ctx context.Context) error {
	st, err := s.store.MailSettings(ctx)
	if err != nil {
		return err
	}
	m, err := s.siteMailerFor(st)
	s.mail.own.Store(m)
	return err
}

// siteMailerFor is the mailer of st, nil when the site uses the server's.
func (s *Server) siteMailerFor(st store.MailSettings) (*mail.Mailer, error) {
	if !st.Enabled || st.Host == "" || st.From == "" {
		return nil, nil
	}
	var pass string
	if len(st.PasswordEnc) > 0 {
		b, err := s.keys.Decrypt(st.PasswordEnc, []byte(mailPasswordAD))
		if err != nil {
			return nil, err // the secret changed: back to the server's mailer
		}
		pass = string(b)
	}
	allow := netguard.Public
	if s.cfg.Dev {
		allow = func(netip.Addr) bool { return true } // a local test server
	}
	cfg := config.SMTP{Host: st.Host, Port: st.Port, Username: st.Username, Password: pass, From: st.From}
	return mail.NewDial(cfg, s.log, mail.Dial(netguard.Dialer(allow, nil, 10*time.Second))), nil
}

type mailSettingsData struct {
	M         store.MailSettings
	Ports     []int
	HasPass   bool
	Network   string // the server's SMTP server, "" when none
	OnNetwork bool
	Error     string
	Success   string
}

func (s *Server) adminMailSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.MailSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderMailSettings(w, r, http.StatusOK, st, "", "")
}

func (s *Server) renderMailSettings(w http.ResponseWriter, r *http.Request, status int, st store.MailSettings, msg, ok string) {
	d := mailSettingsData{M: st, Ports: mailPorts, HasPass: len(st.PasswordEnc) > 0, OnNetwork: s.network != nil,
		Error: msg, Success: ok}
	if s.mailer != nil {
		d.Network = s.mailer.Server()
	}
	s.renderAdminStatus(w, r, status, "mail_settings", s.tu(r, "E-mail"), "settings", d)
}

func (s *Server) adminMailSettingsSave(w http.ResponseWriter, r *http.Request) {
	old, err := s.store.MailSettings(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	st := store.MailSettings{Enabled: r.FormValue("enabled") == "1", Host: strings.ToLower(trimmed(r, "host")),
		Username: trimmed(r, "username"), From: trimmed(r, "from"), PasswordEnc: old.PasswordEnc}
	st.Port, _ = strconv.Atoi(r.FormValue("port"))
	fail := func(msg string) { s.renderMailSettings(w, r, http.StatusUnprocessableEntity, st, msg, "") }
	if !slices.Contains(mailPorts, st.Port) {
		fail(s.tu(r, "Choose a port."))
		return
	}
	if st.Host != "" && (len(st.Host) > 253 || !mailHostRe.MatchString(st.Host) || net.ParseIP(st.Host) != nil) {
		fail(s.tu(r, "Write the server's name, such as smtp.example.com, without https:// or a port."))
		return
	}
	if tooLong(st.Username, 254) || strings.ContainsAny(st.Username, "\r\n") {
		fail(s.tu(r, "The text is too long."))
		return
	}
	if st.From != "" {
		a, err := netmail.ParseAddress(st.From)
		if err != nil || tooLong(st.From, 254) || strings.ContainsAny(st.From, "\r\n") {
			fail(s.tu(r, "Write the sender as an address, such as hello@example.com, or as Name <hello@example.com>."))
			return
		}
		st.From = a.String()
	}
	if st.Enabled && (st.Host == "" || st.From == "") {
		fail(s.tu(r, "Write the server and the sender to use a server of your own."))
		return
	}
	pass := r.FormValue("password")
	switch {
	case pass != "":
		if len(pass) > 500 || strings.ContainsAny(pass, "\r\n") {
			fail(s.tu(r, "The text is too long."))
			return
		}
		enc, err := s.keys.Encrypt([]byte(pass), []byte(mailPasswordAD))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		st.PasswordEnc = enc
	case r.FormValue("remove_password") == "1", st.Host != old.Host, st.Username != old.Username:
		// A password goes only to the server and account it was given for.
		st.PasswordEnc = nil
	}
	if err := s.store.SaveMailSettings(r.Context(), st); err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.loadSiteMail(r.Context()); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "settings.mail_saved", st.Host, map[string]string{"enabled": strconv.FormatBool(st.Enabled)})
	if r.FormValue("test") != "1" {
		s.redirect(w, r, "/admin/settings/mail", "success", s.tu(r, "Settings saved."))
		return
	}
	if !st.Enabled && st.Host != "" {
		// Testing the network's server here would look like testing this one.
		s.renderMailSettings(w, r, http.StatusOK, st, s.tu(r, "Saved, but switched off: turn on «Send through this server» to send through %s, then test it.", st.Host), "")
		return
	}
	u := currentUser(r)
	if u == nil || u.Email == "" {
		s.renderMailSettings(w, r, http.StatusOK, st, s.tu(r, "Add an e-mail address to your profile to receive the test."), "")
		return
	}
	if !s.mail.Enabled() {
		s.renderMailSettings(w, r, http.StatusOK, st, s.tu(r, "There is no SMTP server to test: set one up here, or ask the server's administrator."), "")
		return
	}
	title := s.settings().SiteTitle
	err = s.mail.Send(u.Email, s.tu(r, "Test e-mail from %s", title),
		s.tu(r, "This message shows that %s can send e-mail through %s.", title, s.mail.Server()))
	if err != nil {
		s.log.Warn("test e-mail", "server", s.mail.Server(), "err", err)
		s.renderMailSettings(w, r, http.StatusOK, st, s.tu(r, "The test e-mail could not be sent: %s", s.mailError(r, err)), "")
		return
	}
	s.renderMailSettings(w, r, http.StatusOK, st, "", s.tu(r, "Settings saved. A test e-mail went to %s through %s.", u.Email, s.mail.Server()))
}

// mailError is what the administrator is told of a failed send: the SMTP
// server's reply, or why it could not be reached.
func (s *Server) mailError(r *http.Request, err error) string {
	if errors.Is(err, netguard.ErrForbidden) {
		return s.tu(r, "the server's address is not public")
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}
