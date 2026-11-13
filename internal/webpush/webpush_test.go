package webpush

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func dec(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291, section 5 and appendix A.
func TestEncryptRFC8291(t *testing.T) {
	sub := Subscription{
		P256dh: dec(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"),
		Auth:   dec(t, "BTBZMqHH6r4Tts7J_aSIgg"),
	}
	sender, err := ecdh.P256().NewPrivateKey(dec(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt([]byte("When I grow up, I want to be a watermelon"), sub, dec(t, "DGv6ra1nlYgDCS1FRnbzlw"), sender)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Errorf("got  %s\nwant %s", b64.EncodeToString(got), want)
	}
	if _, err := Encrypt(make([]byte, MaxPayload+1), sub); err == nil {
		t.Error("a payload that is too long was encrypted")
	}
	if _, err := Encrypt([]byte("x"), Subscription{P256dh: sub.P256dh[:64], Auth: sub.Auth}); err == nil {
		t.Error("a key that is no point was accepted")
	}
}

func TestAuthorization(t *testing.T) {
	priv, pub, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	h, err := Authorization("https://fcm.googleapis.com/fcm/send/abc", "https://example.com/", priv, now)
	if err != nil {
		t.Fatal(err)
	}
	tok, key, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || key != b64.EncodeToString(pub) {
		t.Fatalf("header %q", h)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", tok)
	}
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	json.Unmarshal(dec(t, parts[1]), &claims)
	if claims.Aud != "https://fcm.googleapis.com" || claims.Sub != "https://example.com/" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Errorf("claims %+v", claims)
	}
	sig := dec(t, parts[2])
	x, y := elliptic.Unmarshal(elliptic.P256(), pub) //nolint:staticcheck // the test reads the raw point
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the signature does not verify")
	}
}

func TestValidEndpoint(t *testing.T) {
	for in, want := range map[string]bool{
		"https://fcm.googleapis.com/fcm/send/x":                true,
		"https://updates.push.services.mozilla.com/wpush/v2/x": true,
		"https://web.push.apple.com/x":                         true,
		"https://wns2-par02p.notify.windows.com/w/?token=x":    true,
		"http://fcm.googleapis.com/x":                          false,
		"https://fcm.googleapis.com.evil.test/x":               false,
		"https://evilfcm.googleapis.com.test/x":                false,
		"https://user@fcm.googleapis.com/x":                    false,
		"https://fcm.googleapis.com:8443/x":                    false,
		"https://127.0.0.1/x":                                  false,
		"https://example.com/x":                                false,
		"":                                                     false,
	} {
		if got := ValidEndpoint(in); got != want {
			t.Errorf("%q: %v", in, got)
		}
	}
}
