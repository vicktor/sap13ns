package network

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strconv"

	"sap13ns/internal/auth"
)

// Site secrets are random, not derived from the master key, and stored
// sealed (sites.secret). The master key can then be rotated by resealing
// one row per site, and a site adopted from a single-site install can keep
// its old key. The seal is AES-256-GCM with the AAD "site:<id>", so a
// sealed secret copied to another row does not open.
//
// The sealing key is derived from the master key for this purpose only:
// it never equals the encryption key of auth.NewKeys(master), which an
// adopted site that kept the master as its own secret would use.

const secretLen = 32

func sealKeys(master []byte) *auth.Keys { return auth.NewKeys(subKey(master, "sap13ns:site-secrets")) }

// subKey derives a key for one purpose from the master key.
func subKey(master []byte, purpose string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte(purpose))
	return m.Sum(nil)
}

func secretAD(siteID int64) []byte { return []byte("site:" + strconv.FormatInt(siteID, 10)) }

func newSecret() []byte {
	b := make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return b
}

func (n *Network) wrapSecret(siteID int64, secret []byte) ([]byte, error) {
	if len(secret) != secretLen {
		return nil, errors.New("network: a site secret must be 32 bytes")
	}
	return n.keys.Encrypt(secret, secretAD(siteID))
}

func (n *Network) unwrapSecret(siteID int64, sealed []byte) ([]byte, error) {
	b, err := n.keys.Decrypt(sealed, secretAD(siteID))
	if err != nil {
		return nil, errors.New("network: the site secret does not open with this SAP13NS_SECRET_KEY")
	}
	if len(b) != secretLen {
		return nil, errors.New("network: malformed site secret")
	}
	return b, nil
}

// SiteSecret returns the secret of a site (its SecretKey in
// config.ForSite).
func (n *Network) SiteSecret(s *Site) ([]byte, error) { return n.unwrapSecret(s.ID, s.Secret) }
