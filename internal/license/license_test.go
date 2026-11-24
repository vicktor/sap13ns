package license

import (
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

var issued = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

func sample() *License {
	return &License{ID: "abc", Type: TypeWhiteLabel, Licensee: "Socratex",
		Domains: []string{"socratex.org"}, Issued: issued, Expires: issued.AddDate(1, 0, 0)}
}

func TestRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	data, err := Encode(sample(), priv)
	if err != nil {
		t.Fatal(err)
	}
	l, err := Parse(data, pub)
	if err != nil {
		t.Fatal(err)
	}
	if l.Licensee != "Socratex" || l.Domains[0] != "socratex.org" || !l.Expires.Equal(issued.AddDate(1, 0, 0)) {
		t.Errorf("got %+v", l)
	}
}

// A changed byte anywhere, another key, a truncated or padded file: none
// is accepted.
func TestTampering(t *testing.T) {
	pub, priv := testKey(t)
	data, _ := Encode(sample(), priv)
	for i := range data {
		bad := append([]byte(nil), data...)
		bad[i] ^= 1
		if _, err := Parse(bad, pub); err == nil {
			t.Fatalf("byte %d changed: accepted", i)
		}
	}
	other, _ := testKey(t)
	if _, err := Parse(data, other); !errors.Is(err, ErrSignature) {
		t.Errorf("another key: %v", err)
	}
	for _, bad := range [][]byte{nil, data[:10], data[:len(data)-1], append(append([]byte(nil), data...), 0)} {
		if _, err := Parse(bad, pub); err == nil {
			t.Errorf("%d bytes: accepted", len(bad))
		}
	}
	// Signed by the licensor's key it would be fine; this test key is not it.
	if _, err := Parse(data, nil); !errors.Is(err, ErrSignature) {
		t.Errorf("checked against the compiled-in key: %v", err)
	}
}

func TestEncodeRefusesIncomplete(t *testing.T) {
	_, priv := testKey(t)
	for name, change := range map[string]func(*License){
		"no licensee":   func(l *License) { l.Licensee = " " },
		"no domains":    func(l *License) { l.Domains = nil },
		"upper case":    func(l *License) { l.Domains = []string{"Socratex.org"} },
		"wildcard":      func(l *License) { l.Domains = []string{"*.socratex.org"} },
		"port":          func(l *License) { l.Domains = []string{"socratex.org:443"} },
		"no expiry":     func(l *License) { l.Expires = time.Time{} },
		"expires first": func(l *License) { l.Expires = l.Issued.Add(-time.Hour) },
	} {
		l := sample()
		change(l)
		if _, err := Encode(l, priv); err == nil {
			t.Errorf("%s: issued", name)
		}
	}
}

func TestHidesCredit(t *testing.T) {
	l := sample()
	in := issued.AddDate(0, 6, 0)
	for _, c := range []struct {
		host string
		at   time.Time
		want bool
	}{
		{"socratex.org", in, true},
		{"SOCRATEX.org.", in, true},
		{"socratex.org:443", in, true},
		{"www.socratex.org", in, true},
		{"blog.socratex.org", in, false},
		{"socratex.org.evil.example", in, false},
		{"evilsocratex.org", in, false},
		{"socratex.org", issued.Add(-time.Second), false},
		{"socratex.org", l.Expires.Add(-time.Second), true},
		{"socratex.org", l.Expires, false}, // expired: the credit is back
	} {
		if got := l.HidesCredit(c.host, c.at); got != c.want {
			t.Errorf("%s at %s: %v, want %v", c.host, c.at, got, c.want)
		}
	}
	l.Type = "support"
	if l.HidesCredit("socratex.org", in) {
		t.Error("a licence of another type leaves out the credit")
	}
	var none *License
	if none.HidesCredit("socratex.org", in) {
		t.Error("no licence leaves out the credit")
	}
}

// The running install follows the file: absent, garbage, then a licence
// that is not the licensor's; each holds none.
func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sap13ns.license")
	var states []string
	f := NewFile(path)
	f.OnChange = func(l *License, err error) {
		switch {
		case err != nil:
			states = append(states, "error")
		case l == nil:
			states = append(states, "none")
		default:
			states = append(states, l.Licensee)
		}
	}
	now := issued
	if f.Current(now) != nil {
		t.Fatal("no file holds a licence")
	}
	os.WriteFile(path, []byte("not a licence"), 0o600)
	if f.Current(now.Add(30*time.Second)) != nil || len(states) != 1 {
		t.Fatalf("read again within a minute: %v", states)
	}
	if f.Current(now.Add(2*time.Minute)) != nil {
		t.Fatal("garbage holds a licence")
	}
	_, priv := testKey(t)
	data, _ := Encode(sample(), priv)
	os.WriteFile(path, data, 0o600)
	if f.Current(now.Add(4*time.Minute)) != nil {
		t.Fatal("a licence signed by another key holds")
	}
	os.Remove(path)
	f.Current(now.Add(6 * time.Minute))
	want := []string{"none", "error", "error", "none"}
	if len(states) != len(want) {
		t.Fatalf("states %v, want %v", states, want)
	}
	for i := range want {
		if states[i] != want[i] {
			t.Fatalf("states %v, want %v", states, want)
		}
	}
	if NewFile("") != nil || NewFile("off") != nil {
		t.Error("an empty path watches a file")
	}
	var nilFile *File
	if nilFile.Current(now) != nil {
		t.Error("a nil File holds a licence")
	}
}
