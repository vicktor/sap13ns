// Package license reads the licence files the licensor (BAUBER.XYZ SL)
// issues to a single-site install, such as one that may leave out the
// "Powered by sap13ns" credit that CPAL-1.0 (Exhibit B) otherwise asks for.
//
// A licence file is binary and signed, not encrypted: what it says (who it
// is for, its domains, when it ends) is no secret, and `sap13ns license`
// prints it. What matters is that nobody but the licensor can make one, so
// it carries an Ed25519 signature that the public key compiled into the
// binary (PublicKey) checks. The private key never leaves the licensor:
// scripts/licensesign issues licences with it.
//
// The file is, in this order:
//
//	"S13L"          4 bytes, the magic number
//	version         1 byte, 1
//	payload length  2 bytes, big-endian
//	payload         JSON of License
//	signature       64 bytes, Ed25519 over everything before it
package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// PublicKey is the licensor's licence-signing public key (standard base64).
// It is not the release-signing key of internal/update: a leak of either
// does not touch the other.
const PublicKey = "Xcd9MbzS42h0evAdfUD9ZgPSwdh9gWRgR2gnLieM5sc="

// TypeWhiteLabel lets the install leave out the "Powered by sap13ns" credit
// while the licence is in force.
const TypeWhiteLabel = "white-label"

// MaxSize bounds a licence file; real ones are a few hundred bytes.
const MaxSize = 8 << 10

const (
	magic   = "S13L"
	version = 1
	header  = len(magic) + 1 + 2
)

// License is what a licence file grants.
type License struct {
	ID       string    `json:"id"`       // the licensor's reference, random
	Type     string    `json:"type"`     // TypeWhiteLabel; others grant nothing here
	Licensee string    `json:"licensee"` // who it is issued to
	Domains  []string  `json:"domains"`  // the hosts of SAP13NS_BASE_URL it is valid for
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"` // the first instant it is no longer in force
}

var (
	ErrFormat    = errors.New("license: not a sap13ns licence file")
	ErrSignature = errors.New("license: the signature does not match: the file was changed, or not issued by the licensor")
)

// Check reports what a parsed licence lacks to be issued: the fields
// scripts/licensesign requires, and Parse checks again.
func (l *License) Check() error {
	switch {
	case l.ID == "":
		return errors.New("license: no id")
	case l.Type == "":
		return errors.New("license: no type")
	case strings.TrimSpace(l.Licensee) == "":
		return errors.New("license: no licensee")
	case len(l.Domains) == 0:
		return errors.New("license: no domains")
	case l.Expires.IsZero():
		return errors.New("license: no expiry date")
	case !l.Expires.After(l.Issued):
		return errors.New("license: it expires before it is issued")
	}
	for _, d := range l.Domains {
		if d != normHost(d) || d == "" || strings.ContainsAny(d, "/:* ") {
			return fmt.Errorf("license: %q is not a lower-case host name", d)
		}
	}
	return nil
}

// Encode signs the licence with the licensor's private key.
func Encode(l *License, priv ed25519.PrivateKey) ([]byte, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	if header+len(payload)+ed25519.SignatureSize > MaxSize {
		return nil, errors.New("license: too large")
	}
	var b bytes.Buffer
	b.WriteString(magic)
	b.WriteByte(version)
	binary.Write(&b, binary.BigEndian, uint16(len(payload)))
	b.Write(payload)
	b.Write(ed25519.Sign(priv, b.Bytes()))
	return b.Bytes(), nil
}

// Parse checks the signature on data with pub (nil = PublicKey) and returns
// the licence. It says nothing of the date or the domain: see Grants.
func Parse(data []byte, pub ed25519.PublicKey) (*License, error) {
	if pub == nil {
		var err error
		if pub, err = DecodePublicKey(PublicKey); err != nil {
			return nil, err
		}
	}
	if len(data) > MaxSize || len(data) < header+ed25519.SignatureSize ||
		string(data[:len(magic)]) != magic {
		return nil, ErrFormat
	}
	if v := data[len(magic)]; v != version {
		return nil, fmt.Errorf("license: version %d is newer than this sap13ns reads; update it", v)
	}
	n := int(binary.BigEndian.Uint16(data[len(magic)+1 : header]))
	if header+n+ed25519.SignatureSize != len(data) {
		return nil, ErrFormat
	}
	signed, sig := data[:header+n], data[header+n:]
	if !ed25519.Verify(pub, signed, sig) {
		return nil, ErrSignature
	}
	var l License
	dec := json.NewDecoder(bytes.NewReader(data[header : header+n]))
	if err := dec.Decode(&l); err != nil {
		return nil, fmt.Errorf("license: %w", err)
	}
	if err := l.Check(); err != nil {
		return nil, err
	}
	return &l, nil
}

// DecodePublicKey decodes a standard base64 Ed25519 public key.
func DecodePublicKey(b64 string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("license: the public key is not 32 base64-encoded bytes")
	}
	return ed25519.PublicKey(b), nil
}

// Covers reports whether host (a Host header or URL host, port allowed) is
// one of the licence's domains, or the www. name of one.
func (l *License) Covers(host string) bool {
	h := normHost(host)
	return h != "" && (slices.Contains(l.Domains, h) ||
		strings.HasPrefix(h, "www.") && slices.Contains(l.Domains, h[len("www."):]))
}

// InForce reports whether the licence holds at now.
func (l *License) InForce(now time.Time) bool {
	return !now.Before(l.Issued) && now.Before(l.Expires)
}

// HidesCredit reports whether the licence lets the install whose public
// host is host leave out the credit at now.
func (l *License) HidesCredit(host string, now time.Time) bool {
	return l != nil && l.Type == TypeWhiteLabel && l.Covers(host) && l.InForce(now)
}

func normHost(h string) string {
	h = strings.TrimSpace(h)
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
