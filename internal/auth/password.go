// Package auth implements password hashing, tokens, TOTP two-factor
// authentication, roles and brute-force throttling.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommendation: m=19 MiB, t=2, p=1 as minimum;
// we use a little more memory).
const (
	argonTime    = 2
	argonMemory  = 32 * 1024 // KiB
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

const (
	MinPasswordLen = 10
	MaxPasswordLen = 256
)

var ErrWeakPassword = errors.New("weak password")

// HashPassword returns a PHC-formatted argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC argon2id hash in constant time.
func VerifyPassword(hash, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &p); err != nil {
		return false
	}
	if mem > 256*1024 || t > 10 || p > 8 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash reports whether a hash uses outdated parameters.
func NeedsRehash(hash string) bool {
	return !strings.HasPrefix(hash, fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argonMemory, argonTime, argonThreads))
}

// dummyHash is verified when a login name does not exist so that response
// time does not reveal which accounts exist.
var dummyHash, _ = HashPassword("sap13ns-timing-equalizer")

func VerifyDummy(password string) { VerifyPassword(dummyHash, password) }

// commonPasswords is a short deny-list of the most used passwords.
var commonPasswords = map[string]bool{}

func init() {
	for _, p := range strings.Fields(`123456789012 1234567890 password123 qwertyuiop 1q2w3e4r5t password1234
		iloveyou123 administrator adminadmin welcome123 contraseña contrasena123 qwerty1234 1234567890a
		abcdefghij passw0rd123 letmein1234 football123 superman123 aaaaaaaaaa 0987654321 1111111111
		monkey12345 dragon12345 sunshine123 princess123 baseball123 trustno1234 changeme123 wordpress123`) {
		commonPasswords[p] = true
	}
}

// CheckPasswordPolicy enforces length, a deny-list and that the password is
// not derived from the username or e-mail.
func CheckPasswordPolicy(password, username, email string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLen {
		return fmt.Errorf("%w: at least %d characters", ErrWeakPassword, MinPasswordLen)
	}
	if n > MaxPasswordLen {
		return fmt.Errorf("%w: at most %d characters", ErrWeakPassword, MaxPasswordLen)
	}
	lower := strings.ToLower(password)
	if commonPasswords[lower] {
		return fmt.Errorf("%w: too common", ErrWeakPassword)
	}
	if username != "" && strings.Contains(lower, strings.ToLower(username)) {
		return fmt.Errorf("%w: must not contain the username", ErrWeakPassword)
	}
	if local, _, ok := strings.Cut(strings.ToLower(email), "@"); ok && len(local) >= 4 && strings.Contains(lower, local) {
		return fmt.Errorf("%w: must not contain the e-mail address", ErrWeakPassword)
	}
	distinct := map[rune]bool{}
	for _, r := range password {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return fmt.Errorf("%w: too repetitive", ErrWeakPassword)
	}
	return nil
}
