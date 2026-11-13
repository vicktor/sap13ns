// Package webpush sends Web Push messages: the payload encryption of
// RFC 8291 (aes128gcm, RFC 8188) and the VAPID authorisation of RFC 8292,
// with the standard library only.
//
// A subscription is what a browser's PushManager gave the site: the
// address of its push service and two keys. Only the browser can read a
// message encrypted for them; the push service sees its size and who
// sends it.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxPayload is the longest message that fits one record of every push
// service (4096 octets of body, less the header and the tag).
const MaxPayload = 3900

// Subscription is a browser's push subscription.
type Subscription struct {
	Endpoint string
	P256dh   []byte // the browser's public key, 65 octets, uncompressed
	Auth     []byte // the authentication secret, 16 octets
}

var b64 = base64.RawURLEncoding

// Hosts are the push services a subscription may name: the endpoint is
// given by the visitor's browser, so the server sends only to the
// services browsers use. A name is matched whole or as a parent domain.
var Hosts = []string{"fcm.googleapis.com", "push.services.mozilla.com", "push.apple.com", "notify.windows.com"}

// ValidEndpoint reports whether endpoint is an https address of a known
// push service, without credentials or a port of its own.
func ValidEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || len(endpoint) > 2048 {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range Hosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// ValidKeys reports whether the two keys of a subscription have the shape
// they must: a point of P-256 and 16 octets.
func ValidKeys(p256dh, auth []byte) bool {
	if len(auth) != 16 || len(p256dh) != 65 {
		return false
	}
	_, err := ecdh.P256().NewPublicKey(p256dh)
	return err == nil
}

// GenerateKey makes a VAPID key pair: the private scalar (32 octets) and
// the public point (65 octets), which the browser takes as
// applicationServerKey.
func GenerateKey() (private, public []byte, err error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// PublicKey is the public point of a VAPID private key.
func PublicKey(private []byte) ([]byte, error) {
	k, err := ecdh.P256().NewPrivateKey(private)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

func hmacSum(key []byte, parts ...[]byte) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write(p)
	}
	return m.Sum(nil)
}

// Encrypt returns the aes128gcm body of payload for sub (RFC 8291).
func Encrypt(payload []byte, sub Subscription) ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	sender, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return encrypt(payload, sub, salt, sender)
}

func encrypt(payload []byte, sub Subscription, salt []byte, sender *ecdh.PrivateKey) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, errors.New("webpush: the payload is too long")
	}
	if !ValidKeys(sub.P256dh, sub.Auth) {
		return nil, errors.New("webpush: the subscription's keys are not valid")
	}
	ua, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, err
	}
	shared, err := sender.ECDH(ua)
	if err != nil {
		return nil, err
	}
	as := sender.PublicKey().Bytes()
	// The key the two sides share, bound to both public keys.
	prkKey := hmacSum(sub.Auth, shared)
	ikm := hmacSum(prkKey, []byte("WebPush: info\x00"), sub.P256dh, as, []byte{1})
	prk := hmacSum(salt, ikm)
	cek := hmacSum(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16]
	nonce := hmacSum(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12]
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(salt)
	binary.Write(&out, binary.BigEndian, uint32(4096)) // the record size
	out.WriteByte(byte(len(as)))
	out.Write(as)
	// One record: the payload and the delimiter of a last record.
	return gcm.Seal(out.Bytes(), nonce, append(append([]byte{}, payload...), 2), nil), nil
}

// Authorization is the VAPID header for a push service (RFC 8292): a
// token signed with the site's private key, for the service of endpoint
// only, and the public key. subject is who to reach about the sender, an
// https: or mailto: address.
func Authorization(endpoint, subject string, private []byte, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), private)
	if err != nil {
		return "", err
	}
	claims, _ := json.Marshal(map[string]any{"aud": u.Scheme + "://" + u.Host, "exp": now.Add(12 * time.Hour).Unix(), "sub": subject})
	signed := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	pub, err := PublicKey(private)
	if err != nil {
		return "", err
	}
	return "vapid t=" + signed + "." + b64.EncodeToString(sig) + ", k=" + b64.EncodeToString(pub), nil
}

// Gone is the error of a subscription its push service no longer knows
// (404 or 410), or that belongs to another sender's key (403): it will
// never take a message again and is to be forgotten.
var Gone = errors.New("webpush: the subscription is gone")

// Send delivers payload to sub through its push service, kept there for
// ttl when the browser is not reachable now.
func Send(ctx context.Context, client *http.Client, sub Subscription, payload []byte, subject string, private []byte, ttl time.Duration) error {
	if !ValidEndpoint(sub.Endpoint) {
		return Gone
	}
	body, err := Encrypt(payload, sub)
	if err != nil {
		return err
	}
	auth, err := Authorization(sub.Endpoint, subject, private, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", auth)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusForbidden:
		return Gone
	}
	return fmt.Errorf("webpush: the push service answered %d", resp.StatusCode)
}
