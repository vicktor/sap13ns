package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSessionIdle: a session ends after the site's idle timeout (60
// minutes by default) without a request, and requests keep it alive.
func TestSessionIdle(t *testing.T) {
	ctx := context.Background()
	e := newCustomEnv(t)
	if got := e.s.settings().IdleMinutes; got != 60 {
		t.Fatalf("default idle timeout: %d minutes", got)
	}
	idle := func(minutes int) {
		t.Helper()
		if _, err := e.st.DB.Exec(ctx, `UPDATE sessions SET last_seen_at = now() - make_interval(mins => $1)`, minutes); err != nil {
			t.Fatal(err)
		}
	}
	// Within the timeout: still signed in, and the request counts as
	// activity.
	idle(50)
	if rec := e.get("/admin/", e.admin); rec.Code != http.StatusOK {
		t.Fatalf("after 50 minutes: %d", rec.Code)
	}
	var minutes float64
	if err := e.st.DB.QueryRow(ctx, `SELECT max(extract(epoch FROM now() - last_seen_at) / 60) FROM sessions WHERE user_id = $1`,
		e.userID("admin")).Scan(&minutes); err != nil || minutes > 1 {
		t.Errorf("the request did not count as activity: %.1f minutes %v", minutes, err)
	}
	// What a page sends by itself is not activity.
	idle(50)
	req := httptest.NewRequest("GET", "http://example.test/admin/", nil)
	req.Header.Set("X-Background", "1")
	if rec := e.do(req, e.admin); rec.Code != http.StatusOK {
		t.Fatalf("a background request: %d", rec.Code)
	}
	if err := e.st.DB.QueryRow(ctx, `SELECT max(extract(epoch FROM now() - last_seen_at) / 60) FROM sessions WHERE user_id = $1`,
		e.userID("admin")).Scan(&minutes); err != nil || minutes < 49 {
		t.Errorf("a background request counted as activity: %.1f minutes %v", minutes, err)
	}

	// Past it: signed out.
	idle(61)
	if rec := e.get("/admin/", e.admin); rec.Code == http.StatusOK {
		t.Fatal("still signed in after 61 minutes without activity")
	}
	var n int
	if err := e.st.DB.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id = $1`, e.userID("admin")).Scan(&n); err != nil || n != 0 {
		t.Errorf("the expired session was kept: %d %v", n, err)
	}
}
