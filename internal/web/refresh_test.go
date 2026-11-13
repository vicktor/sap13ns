package web

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// Open pages follow the site's changes: with the setting on, every public
// page carries the site's version and /api/site-version answers the same
// one, which changes when a design is published and not when a server
// starts again or content changes.
func TestSiteVersion(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	version := func() (int, string) {
		rec := e.get("/api/site-version", nil)
		var v struct{ Version string }
		json.Unmarshal(rec.Body.Bytes(), &v)
		return rec.Code, v.Version
	}
	if code, _ := version(); code != http.StatusNotFound {
		t.Errorf("off: %d", code)
	}
	if body := e.get("/", nil).Body.String(); strings.Contains(body, "data-sap-version") || strings.Contains(body, "refresh.css") {
		t.Error("a site that does not follow its changes says its version")
	}

	set := e.s.settings()
	set.Refresh = "ask"
	if err := e.st.SaveSettings(ctx, set); err != nil {
		t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		t.Fatal(err)
	}
	e.s.contentChanged()
	code, v1 := version()
	if code != http.StatusOK || len(v1) != 16 {
		t.Fatalf("on: %d %q", code, v1)
	}
	body := e.get("/", nil).Body.String()
	m := regexp.MustCompile(`data-sap-version="([0-9a-f]+)" data-sap-refresh="ask" data-sap-refresh-text="[^"]+"`).FindStringSubmatch(body)
	if m == nil || m[1] != v1 || !strings.Contains(body, "/static/refresh.css?v=") {
		t.Errorf("the page's version %v is not the site's %s, or its styles are missing", m, v1)
	}
	if body := e.get("/?preview_theme="+e.slug, e.admin).Body.String(); strings.Contains(body, "data-sap-version") {
		t.Error("a preview follows the site's changes")
	}

	// Content is no new version, and neither is a cache that was dropped.
	e.s.contentChanged()
	if _, v := version(); v != v1 {
		t.Errorf("the version changed without a change: %s, then %s", v1, v)
	}
	// A draft is not a change; publishing it is.
	if rec, res := e.layout(`{"layout": {"pages": {"home": {"sections": [{"id": "a", "layout": "1", "columns": [[{"id": "t1", "type": "rich-text", "settings": {"text": "One"}}]]}]}}}}`); rec.Code != http.StatusOK || !res.OK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	if _, v := version(); v != v1 {
		t.Error("a draft changed the version")
	}
	if rec := e.builderPost("/publish"); rec.Code != http.StatusSeeOther {
		t.Fatalf("publish: %d", rec.Code)
	}
	_, v2 := version()
	if v2 == v1 || v2 == "" {
		t.Errorf("publishing a layout kept the version %s", v1)
	}
	if body := e.get("/", nil).Body.String(); !strings.Contains(body, `data-sap-version="`+v2+`"`) {
		t.Error("the page does not carry the new version")
	}
}
