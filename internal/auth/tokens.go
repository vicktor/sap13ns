package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// NewToken returns a URL-safe random token with n bytes of entropy.
func NewToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the SHA-256 of a token; only hashes are stored in the database.
func HashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func HashTokenHex(token string) string { return hex.EncodeToString(HashToken(token)) }

// Keys derives purpose-specific keys from the instance secret so that one
// leaked derived key does not compromise the others.
type Keys struct {
	encryption []byte
	signing    []byte
}

func NewKeys(secret []byte) *Keys {
	derive := func(purpose string) []byte {
		m := hmac.New(sha256.New, secret)
		m.Write([]byte("sap13ns:" + purpose))
		return m.Sum(nil)
	}
	return &Keys{encryption: derive("encryption"), signing: derive("signing")}
}

// Encrypt seals plaintext with AES-256-GCM; the random nonce is prepended.
func (k *Keys) Encrypt(plaintext, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.encryption)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, additional), nil
}

func (k *Keys) Decrypt(ciphertext, additional []byte) ([]byte, error) {
	block, err := aes.NewCipher(k.encryption)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], additional)
}

// Sign returns a timestamped HMAC token binding a purpose and a value, e.g.
// to prove that a comment form was rendered by this server.
func (k *Keys) Sign(purpose, value string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 36)
	m := hmac.New(sha256.New, k.signing)
	m.Write([]byte(purpose + "\x00" + value + "\x00" + ts))
	return ts + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

// Verify checks a token from Sign and returns its age.
func (k *Keys) Verify(purpose, value, token string, maxAge time.Duration) (time.Duration, bool) {
	ts, sig, ok := strings.Cut(token, ".")
	if !ok {
		return 0, false
	}
	sec, err := strconv.ParseInt(ts, 36, 64)
	if err != nil {
		return 0, false
	}
	at := time.Unix(sec, 0)
	expected := k.Sign(purpose, value, at)
	if !hmac.Equal([]byte(expected), []byte(ts+"."+sig)) {
		return 0, false
	}
	age := time.Since(at)
	if age < 0 || age > maxAge {
		return age, false
	}
	return age, true
}

// SignBytes returns a raw HMAC of data (used for cache keys and ETags).
func (k *Keys) SignBytes(data []byte) []byte {
	m := hmac.New(sha256.New, k.signing)
	m.Write(data)
	return m.Sum(nil)
}

func uint64Bytes(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}
