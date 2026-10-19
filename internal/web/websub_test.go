package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"sap13ns/internal/netguard"
)

// TestWebSubHub: the hub is an https address of the settings, and the
// ping only dials public addresses, whatever the settings hold.
func TestWebSubHub(t *testing.T) {
	var hits atomic.Int32
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer hub.Close()

	resp, err := webSubClient.Post(hub.URL, "application/x-www-form-urlencoded", strings.NewReader("hub.mode=publish"))
	if err == nil {
		resp.Body.Close()
	}
	if !errors.Is(err, netguard.ErrForbidden) || hits.Load() != 0 {
		t.Fatalf("the ping reached a loopback hub: %v, %d requests", err, hits.Load())
	}

	// The ping of a stored loopback hub is refused too, and an http:// hub
	// is not pinged at all.
	e := newCustomEnv(t)
	ctx := context.Background()
	for _, h := range []string{hub.URL, "http://127.0.0.1:9/hub"} {
		set := e.s.settings()
		set.WebSubHub = h
		if err := e.st.SaveSettings(ctx, set); err != nil {
			t.Fatal(err)
		}
		if err := e.s.ReloadSettings(ctx); err != nil {
			t.Fatal(err)
		}
		e.s.pingWebSub()
		e.waitJobs()
	}
	if hits.Load() != 0 {
		t.Errorf("the hub got %d pings", hits.Load())
	}

	// The settings page refuses an http:// hub with its message.
	page := e.get("/admin/settings", e.admin)
	if page.Code != 200 {
		t.Fatalf("settings page: %d", page.Code)
	}
	form := url.Values{"csrf": {e.csrf}, "site_title": {"T"}, "language": {"es"}, "timezone": {"UTC"}, "posts_per_page": {"10"},
		"front_page": {"posts"}, "comment_moderation": {"all"}, "default_role": {"subscriber"}, "feed_items": {"20"},
		"websub_hub": {"http://hub.example.com/"}, "two_factor": {"staff"}, "og_cards": {"classic"}, "ai_access": {"open"}, "video_embeds": {"on"},
		"session_days": {"14"}, "idle_hours": {"48"}}
	post := func(f url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://example.test/admin/settings", strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return e.do(req, e.admin)
	}
	if rec := post(form); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "WebSub") {
		t.Errorf("an http:// hub: %d", rec.Code)
	}
	form.Set("websub_hub", "https://hub.example.com/")
	form.Set("default_role", "admin")
	if rec := post(form); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("administrators by registration: %d", rec.Code)
	}
	form.Set("default_role", "author")
	if rec := post(form); rec.Code != http.StatusSeeOther {
		t.Errorf("valid settings: %d %s", rec.Code, rec.Body.String())
	}
	if set := e.s.settings(); set.WebSubHub != "https://hub.example.com/" || set.DefaultRole != "author" {
		t.Errorf("saved: %+v", set)
	}
}
