package web

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/store"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// openPush reads a message as the browser of key and auth would
// (RFC 8291).
func openPush(t *testing.T, body []byte, key *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	mac := func(k []byte, parts ...[]byte) []byte {
		m := hmac.New(sha256.New, k)
		for _, p := range parts {
			m.Write(p)
		}
		return m.Sum(nil)
	}
	salt, as := body[:16], body[21:21+int(body[20])]
	pub, err := ecdh.P256().NewPublicKey(as)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := key.ECDH(pub)
	ikm := mac(mac(auth, shared), []byte("WebPush: info\x00"), key.PublicKey().Bytes(), as, []byte{1})
	prk := mac(salt, ikm)
	block, _ := aes.NewCipher(mac(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16])
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, mac(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12], body[21+len(as):], nil)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimRight(plain, "\x00")[:len(bytes.TrimRight(plain, "\x00"))-1]
}

// Notifications of new posts: offered only with the setting and a
// published post; a browser subscribes with JSON; a post that goes live
// reaches it encrypted, and a subscription that is gone is forgotten.
func TestPush(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	b64 := base64.RawURLEncoding
	key, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	subscribe := func(endpoint, ctype string) int {
		body, _ := json.Marshal(map[string]any{"endpoint": endpoint, "lang": "es",
			"keys": map[string]string{"p256dh": b64.EncodeToString(key.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)}})
		req := httptest.NewRequest("POST", "http://example.test/api/push/subscribe", bytes.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		return e.do(req, nil).Code
	}
	const good = "https://fcm.googleapis.com/fcm/send/one"
	if code := subscribe(good, "application/json"); code != http.StatusNotFound {
		t.Errorf("off: subscribe answers %d", code)
	}

	set := e.s.settings()
	set.Push = true
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	e.s.ReloadSettings(ctx)
	e.s.contentChanged()
	if body := e.get("/", nil).Body.String(); strings.Contains(body, "data-sap-push") {
		t.Error("a site without posts offers notifications")
	}
	u, _ := e.st.UserByUsername(ctx, "admin")
	at := time.Now().Add(-time.Hour)
	id, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Hola <mundo>", Slug: "hola",
		Content: "El primer párrafo de la entrada.", Lang: e.s.lang(), AuthorID: u.ID, CommentStatus: "open", PublishedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	home := e.get("/", nil)
	body := home.Body.String()
	m := regexp.MustCompile(`data-sap-push="([A-Za-z0-9_-]{87})" data-sap-push-on="Avisarme de entradas nuevas"`).FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, " data-sap-sw") || !strings.Contains(body, `data-sap-push-toggle`) ||
		!strings.Contains(home.Header().Get("Content-Security-Policy"), "worker-src 'self'") {
		t.Fatalf("the page does not offer notifications: key %v", m)
	}
	if sw := e.get("/sw.js", nil).Body.String(); !strings.HasPrefix(sw, "self.SAP = {offline: false, push: true};\n") {
		t.Errorf("worker: %.50s", sw)
	}
	if mf := e.get("/manifest.webmanifest", nil).Body.String(); !strings.Contains(mf, `"display":"standalone"`) {
		t.Errorf("manifest: %s", mf)
	}
	if body := e.get("/?preview_theme="+e.slug, e.admin).Body.String(); strings.Contains(body, "data-sap-push=") {
		t.Error("a preview offers notifications")
	}

	// Subscribing: JSON only, a known push service only.
	for name, code := range map[string]int{
		subscribeCase(subscribe(good, "application/x-www-form-urlencoded")): http.StatusUnsupportedMediaType,
		subscribeCase(subscribe("https://evil.test/push", "application/json")): http.StatusBadRequest,
		subscribeCase(subscribe("https://127.0.0.1/push", "application/json")): http.StatusBadRequest,
	} {
		if name != subscribeCase(code) {
			t.Errorf("subscribe: got %s, want %d", name, code)
		}
	}
	const gone = "https://updates.push.services.mozilla.com/wpush/v2/two"
	if a, b := subscribe(good, "application/json"), subscribe(gone, "application/json"); a != http.StatusNoContent || b != http.StatusNoContent {
		t.Fatalf("subscribe: %d %d", a, b)
	}
	subscribe(good, "application/json") // again: the same browser, once
	if n, _ := e.st.CountPushSubscriptions(ctx); n != 2 {
		t.Fatalf("%d subscriptions", n)
	}

	// A post goes live.
	var mu sync.Mutex
	got := map[string]*http.Request{}
	bodies := map[string][]byte{}
	e.s.push.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got[r.URL.String()], bodies[r.URL.String()] = r, b
		mu.Unlock()
		status := http.StatusCreated
		if r.URL.String() == gone {
			status = http.StatusGone
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}, nil
	})}
	p, err := e.st.PostByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	e.s.pushPublished(p)
	for i := 0; e.s.jobs.Load() > 0 && i < 500; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	req := got[good]
	if req == nil || got[gone] == nil {
		t.Fatalf("sent to %d services", len(got))
	}
	if req.Header.Get("Content-Encoding") != "aes128gcm" || req.Header.Get("TTL") != "86400" || !strings.HasPrefix(req.Header.Get("Authorization"), "vapid t=") ||
		!strings.HasSuffix(req.Header.Get("Authorization"), ", k="+m[1]) {
		t.Errorf("headers: %v", req.Header)
	}
	var msg map[string]string
	json.Unmarshal(openPush(t, bodies[good], key, auth), &msg)
	if msg["title"] != "Hola <mundo>" || msg["url"] != "/blog/hola" && !strings.HasSuffix(msg["url"], "/hola") || !strings.Contains(msg["body"], "primer párrafo") || msg["icon"] == "" {
		t.Errorf("message: %v", msg)
	}
	if n, _ := e.st.CountPushSubscriptions(ctx); n != 1 {
		t.Errorf("the subscription that is gone was kept: %d", n)
	}
	// Unsubscribing forgets the browser.
	ub, _ := json.Marshal(map[string]string{"endpoint": good})
	ureq := httptest.NewRequest("POST", "http://example.test/api/push/unsubscribe", bytes.NewReader(ub))
	ureq.Header.Set("Content-Type", "application/json")
	if rec := e.do(ureq, nil); rec.Code != http.StatusNoContent {
		t.Errorf("unsubscribe: %d", rec.Code)
	}
	if n, _ := e.st.CountPushSubscriptions(ctx); n != 0 {
		t.Errorf("unsubscribed, %d left", n)
	}
	// The settings page says how many there are.
	if page := e.get("/admin/settings", e.admin).Body.String(); !strings.Contains(page, "Navegadores suscritos: 0.") {
		t.Error("the settings page lacks the count")
	}
}

func subscribeCase(code int) string { return http.StatusText(code) }
