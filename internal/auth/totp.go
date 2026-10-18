package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP implements RFC 6238 with SHA-1, 6 digits and 30 second steps, which is
// what every authenticator app supports.
const (
	totpPeriod = 30
	totpDigits = 6
	totpSkew   = 1 // accept one step before/after for clock drift
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 20 random bytes (160 bits, as recommended by RFC 4226).
func NewTOTPSecret() []byte {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func TOTPSecretString(secret []byte) string { return b32.EncodeToString(secret) }

// TOTPURI builds the otpauth:// URI encoded in the enrolment QR code.
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", TOTPSecretString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func hotp(secret []byte, counter uint64) string {
	m := hmac.New(sha1.New, secret)
	m.Write(uint64Bytes(counter))
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", totpDigits, code%1_000_000)
}

// TOTPCode returns the code for a given time (exported for tests and enrolment).
func TOTPCode(secret []byte, t time.Time) string {
	return hotp(secret, uint64(t.Unix()/totpPeriod))
}

// ValidateTOTP returns the matching time step, or -1. Callers must reject a
// step that is not greater than the last accepted one to stop replays.
func ValidateTOTP(secret []byte, code string, now time.Time) int64 {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return -1
	}
	step := now.Unix() / totpPeriod
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		if subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(step+d))), []byte(code)) == 1 {
			return step + d
		}
	}
	return -1
}

// NewRecoveryCodes returns n human-friendly one-time codes and their hashes.
func NewRecoveryCodes(n int) (codes, hashes []string) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for range n {
		var sb strings.Builder
		buf := make([]byte, 1)
		for written := 0; written < 10; {
			if _, err := rand.Read(buf); err != nil {
				panic(err)
			}
			// Rejection sampling keeps the distribution uniform.
			if int(buf[0]) >= 256-256%len(alphabet) {
				continue
			}
			if written == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(buf[0])%len(alphabet)])
			written++
		}
		code := sb.String()
		codes = append(codes, code)
		hashes = append(hashes, HashTokenHex(NormalizeRecoveryCode(code)))
	}
	return codes, hashes
}

func NormalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
