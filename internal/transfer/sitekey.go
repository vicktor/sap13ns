package transfer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"

	"github.com/jackc/pgx/v5"

	"sap13ns/internal/auth"
)

// A site moved here keeps its own secret key: what its tables hold
// encrypted was sealed with it. The key is kept in site_key, sealed with
// a key derived from SAP13NS_SECRET_KEY for this purpose only, and the
// server uses it in place of SAP13NS_SECRET_KEY (config.Config.SecretKey;
// ServerKey stays the configured one). SAP13NS_SECRET_KEY can then be
// anything, and the env file is never rewritten.

var siteKeyAD = []byte("sap13ns:site-key")

func sealKeys(serverKey []byte) *auth.Keys {
	m := hmac.New(sha256.New, serverKey)
	m.Write([]byte("sap13ns:site-key-seal"))
	return auth.NewKeys(m.Sum(nil))
}

// SealSiteKey seals a site's key with the server's.
func SealSiteKey(serverKey, siteKey []byte) ([]byte, error) {
	if len(serverKey) != 32 || len(siteKey) != 32 {
		return nil, errors.New("transfer: keys must be 32 bytes")
	}
	return sealKeys(serverKey).Encrypt(siteKey, siteKeyAD)
}

// SiteKey returns the key of a site moved here, opened with the server's
// key, or nil for a site that keeps SAP13NS_SECRET_KEY. q works on the
// site's schema, migrated.
func SiteKey(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, serverKey []byte) ([]byte, error) {
	var sealed []byte
	err := q.QueryRow(ctx, `SELECT sealed FROM site_key`).Scan(&sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key, err := sealKeys(serverKey).Decrypt(sealed, siteKeyAD)
	if err != nil || len(key) != 32 {
		return nil, errors.New("the key of the site moved here does not open with this SAP13NS_SECRET_KEY: " +
			"put back the SAP13NS_SECRET_KEY the site was imported with")
	}
	return key, nil
}
