package store

import (
	"context"
	"errors"
)

// MailSettings are a site's own SMTP server (Settings → E-mail). Without
// them, or while they are off, the site sends through the server's
// (SAP13NS_SMTP_*, the network's on a network). They are kept apart from
// Settings because they hold the password, encrypted with the server
// secret, and they are not part of site backups.
type MailSettings struct {
	Enabled     bool   `json:"enabled"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username,omitempty"`
	PasswordEnc []byte `json:"password_enc,omitempty"`
	From        string `json:"from"` // "Name <address>" or an address
}

const mailSettingsKey = "mail"

// MailSettings returns the site's SMTP settings; zero when it never saved them.
func (s *Store) MailSettings(ctx context.Context) (MailSettings, error) {
	var st MailSettings
	err := s.GetJSON(ctx, mailSettingsKey, &st)
	if errors.Is(err, ErrNotFound) {
		return MailSettings{Port: 587}, nil
	}
	return st, err
}

func (s *Store) SaveMailSettings(ctx context.Context, st MailSettings) error {
	return s.SetJSON(ctx, mailSettingsKey, st)
}
