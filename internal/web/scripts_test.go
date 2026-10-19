package web

import (
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var scriptSrc = regexp.MustCompile(`<script src="/static/([^"?]+)\?v=[0-9a-f]{12}"`)

// scripts lists the core scripts a page loads, in order.
func scripts(body string) []string {
	var out []string
	for _, m := range scriptSrc.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestScriptBundles: a public page loads the colour-scheme script and the
// site bundle (npm run build joins site.js, onepage.js, video.js and
// assistant.js into site.min.js), whatever it shows: a video, the assistant,
// a one-page menu. An admin page loads Basecoat's files, the admin bundle and
// the sidebar script. Both bundles are served cache-busted and immutable.
func TestScriptBundles(t *testing.T) {
	e := newCustomEnv(t)
	e.fakeVideos()
	e.setChat(true, true, nil)
	video := e.publishPost("Video post", "https://youtu.be/dQw4w9WgXcQ\n")
	plain := e.publishPost("Plain post", "No video here.\n")

	public := []string{"theme-init.js", "site.min.js"}
	for _, p := range []string{"/", video, plain} {
		body := e.get(p, nil).Body.String()
		if got := scripts(body); !slices.Equal(got, public) {
			t.Errorf("%s loads %v, want %v", p, got, public)
		}
		if !strings.Contains(body, "data-assistant") {
			t.Errorf("%s: no assistant widget", p)
		}
		if strings.Contains(body, "data-sap-onepage") {
			t.Errorf("%s is marked one-page", p)
		}
	}

	admin := scripts(e.get("/admin/", e.admin).Body.String())
	want := []string{"theme-init.js", "vendor/basecoat/basecoat.min.js", "vendor/basecoat/popover.min.js",
		"vendor/basecoat/dropdown-menu.min.js", "vendor/basecoat/sidebar.min.js", "vendor/basecoat/tabs.min.js",
		"vendor/basecoat/toast.min.js", "vendor/basecoat/select.min.js", "vendor/basecoat/combobox.min.js",
		"admin.min.js", "nav-groups.js"}
	if !slices.Equal(admin, want) {
		t.Errorf("/admin/ loads %v, want %v", admin, want)
	}

	for _, f := range []string{"site.min.js", "admin.min.js"} {
		u := e.s.assetURL(f)
		rec := e.get(u, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") ||
			!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || rec.Body.Len() < 1000 {
			t.Errorf("GET %s: %d %q %q %d bytes", u, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"), rec.Body.Len())
		}
	}
}
