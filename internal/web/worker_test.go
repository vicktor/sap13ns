package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The service worker: off, /sw.js answers a worker that removes itself and
// no page registers one; on, public pages may register the site's own
// worker and there is a page for what was not kept.
func TestWorker(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	rec := e.get("/sw.js", nil)
	if !strings.Contains(rec.Body.String(), "registration.unregister()") || strings.Contains(rec.Body.String(), "self.SAP") {
		t.Errorf("off: /sw.js is not the worker that removes itself: %.80s", rec.Body)
	}
	home := e.get("/", nil)
	if strings.Contains(home.Body.String(), "data-sap-sw") || !strings.Contains(home.Header().Get("Content-Security-Policy"), "worker-src 'none'") {
		t.Error("off: a page registers a worker, or its policy allows one")
	}
	if rec := e.get("/api/offline", nil); rec.Code != http.StatusNotFound {
		t.Errorf("off: the offline page answers %d", rec.Code)
	}

	set := e.s.settings()
	set.Offline = true
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	rec = e.get("/sw.js", nil)
	if body := rec.Body.String(); !strings.HasPrefix(body, "self.SAP = {offline: true};\n") || !strings.Contains(body, `addEventListener("fetch"`) ||
		rec.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("on: /sw.js: %v %.60s", rec.Header(), body)
	}
	home = e.get("/", nil)
	if !strings.Contains(home.Body.String(), " data-sap-sw") || !strings.Contains(home.Header().Get("Content-Security-Policy"), "worker-src 'self'") {
		t.Error("on: the page does not register the worker, or its policy forbids it")
	}
	// The admin never does.
	if csp := e.get("/admin/settings", e.admin).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "worker-src 'none'") {
		t.Errorf("the admin's policy allows a worker: %s", csp)
	}
	off := e.get("/api/offline", nil)
	if off.Code != http.StatusOK || !strings.Contains(off.Body.String(), "No tienes conexión") || !strings.Contains(off.Body.String(), `name="robots" content="noindex`) {
		t.Errorf("offline page: %d", off.Code)
	}
}
