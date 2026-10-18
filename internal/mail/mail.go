// Package mail sends transactional e-mail (password resets) over SMTP with
// mandatory STARTTLS. Without SMTP configuration messages are logged instead,
// which is convenient in development.
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/config"
)

type Mailer struct {
	cfg       config.SMTP
	log       *slog.Logger
	logBodies bool // development only: bodies may contain reset links
}

func New(cfg config.SMTP, log *slog.Logger, logBodies bool) *Mailer {
	return &Mailer{cfg: cfg, log: log, logBodies: logBodies}
}

func (m *Mailer) Enabled() bool { return m.cfg.Enabled() }

// headerSafe rejects CR/LF to prevent header injection.
func headerSafe(s string) bool { return !strings.ContainsAny(s, "\r\n") }

func (m *Mailer) Send(to, subject, body string) error {
	if !headerSafe(to) || !headerSafe(subject) {
		return errors.New("invalid header value")
	}
	if !m.cfg.Enabled() {
		if m.logBodies {
			m.log.Warn("SMTP not configured; e-mail printed instead of sent", "to", to, "subject", subject, "body", body)
			return nil
		}
		return errors.New("SMTP is not configured")
	}
	msg := strings.Join([]string{
		"From: " + m.cfg.From,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
		"Auto-Submitted: auto-generated",
		"",
		strings.ReplaceAll(body, "\n", "\r\n"),
	}, "\r\n")

	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	var c *smtp.Client
	if m.cfg.Port == 465 {
		tc := tls.Client(conn, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
		c, err = smtp.NewClient(tc, m.cfg.Host)
	} else {
		c, err = smtp.NewClient(conn, m.cfg.Host)
		if err == nil {
			if ok, _ := c.Extension("STARTTLS"); !ok {
				c.Close()
				return errors.New("SMTP server does not support STARTTLS")
			}
			err = c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
		}
	}
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if m.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	from := m.cfg.From
	if i := strings.LastIndexByte(from, '<'); i >= 0 {
		from = strings.Trim(from[i:], "<>")
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
