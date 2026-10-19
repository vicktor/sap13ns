package network

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"sap13ns/internal/plugin"
)

// TestOfficialPlugins: every plan installs the official plugins, without
// the operator's review, and their files count against the storage like
// the catalogue's. The operator hides one from the sites: they neither
// list it nor install it, and the copies they have stay.
func TestOfficialPlugins(t *testing.T) {
	src := cronPlugin(t)
	bundle, err := plugin.LoadOfficial(fstest.MapFS{
		"hooks.zip": {Data: packPlugin(t, src, func(m map[string]any) { m["author"], m["license"] = "sap13ns", "MIT" })},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := newNetEnv(t, func(o *RegistryOptions) { o.Official = bundle })
	const form = `action="/admin/plugins/official/hooks/install"`
	plugins := func(b *browser, s *Site) string {
		t.Helper()
		rec := b.get("https://" + s.PrimaryHost + "/admin/plugins")
		if rec.Code != http.StatusOK {
			t.Fatalf("plugins page of %s: %d", s.Slug, rec.Code)
		}
		return rec.Body.String()
	}
	install := func(b *browser, s *Site, csrf string) int {
		t.Helper()
		return b.do("POST", "https://"+s.PrimaryHost+"/admin/plugins/official/hooks/install", url.Values{"csrf": {csrf}}).Code
	}
	hooks := func(s *Site) *plugin.Plugin {
		t.Helper()
		return e.entry(s.ID).app.Plugins().Get("hooks")
	}

	// Offered on the Personal plan, which cannot upload.
	free := e.personal("free")
	fb, fcsrf := e.siteAdmin(free, "admin")
	if body := plugins(fb, free); !strings.Contains(body, form) || strings.Contains(body, `action="/admin/plugins/upload"`) {
		t.Fatal("not offered on the Personal plan")
	}

	// The operator hides it.
	_, nb, ncsrf := e.networkAdmin("operator")
	hide := func(hidden string) {
		t.Helper()
		nb.post("/admin/network/plugins/official", "/admin/network/plugins#official", url.Values{"csrf": {ncsrf}, "plugin": {"hooks"}, "hidden": {hidden}})
	}
	if body := nb.page("/admin/network/plugins"); !strings.Contains(body, `id="official"`) || !strings.Contains(body, `name="hidden" value="1"`) {
		t.Fatal("not on the operator's page")
	}
	hide("1")
	if audits(t, e.portal, "network.official_plugin_hidden") != 1 || !strings.Contains(nb.page("/admin/network/plugins"), `name="hidden" value="0"`) {
		t.Fatal("not hidden")
	}
	if strings.Contains(plugins(fb, free), form) {
		t.Fatal("a hidden plugin is offered")
	}
	if code := install(fb, free, fcsrf); code != http.StatusNotFound || hooks(free) != nil {
		t.Fatalf("a hidden plugin installed: %d", code)
	}
	nb.post("/admin/network/plugins/official", "/admin/network/plugins#official", url.Values{"csrf": {ncsrf}, "plugin": {"nope"}, "hidden": {"1"}})
	if hidden, err := e.n.HiddenOfficialPlugins(e.ctx); err != nil || len(hidden) != 1 || hidden[0] != "hooks" {
		t.Fatalf("hidden %v %v", hidden, err)
	}

	// Offered again: installed disabled, held for nobody, its files counted.
	hide("0")
	if audits(t, e.portal, "network.official_plugin_offered") != 1 || !strings.Contains(plugins(fb, free), form) {
		t.Fatal("not offered again")
	}
	if code := install(fb, free, fcsrf); code != http.StatusSeeOther {
		t.Fatalf("install: %d", code)
	}
	if p := hooks(free); p == nil || p.Status != plugin.StatusAvailable || p.Record.Source != plugin.SourceOfficial || p.Record.ReviewRequired {
		t.Fatalf("installed: %+v", p)
	}
	if n, err := e.n.PendingPluginReviews(e.ctx); err != nil || n != 0 {
		t.Fatalf("%d reviews requested", n)
	}
	if e.storage(free.ID) <= 0 {
		t.Fatal("its files are not counted")
	}
	fb.enable(free, fcsrf, "hooks")
	if p := hooks(free); p.Status != plugin.StatusEnabled {
		t.Fatalf("enable: %s %s", p.Status, p.Err)
	}

	// Hidden again, the installed copy stays and runs.
	hide("1")
	if p := hooks(free); p == nil || p.Status != plugin.StatusEnabled {
		t.Fatal("hiding removed or stopped an installed copy")
	}
	hide("0")

	// An install that does not fit the plan's storage is refused, and
	// leaves nothing.
	e.addPlan("tinyup", 1000, 0, true)
	tiny := e.tenant("tiny", "tinyup")
	tb, tcsrf := e.siteAdmin(tiny, "admin")
	if code := install(tb, tiny, tcsrf); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the quota: %d", code)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.n.Dir(tiny.ID), "plugins")); len(ents) != 0 || e.storage(tiny.ID) != 0 {
		t.Fatalf("left %v, %d counted", ents, e.storage(tiny.ID))
	}
}
