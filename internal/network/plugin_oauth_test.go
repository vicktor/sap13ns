package network

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"sap13ns/internal/plugin"
)

// oauthProvider answers the "demo" connection of internal/plugin/testdata/oauth.
type oauthProvider struct{ exchanges int }

func (f *oauthProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	r.ParseForm()
	switch r.URL.Host + r.URL.Path {
	case "auth.example.test/token":
		f.exchanges++
		if r.PostForm.Get("code") != "good" || r.PostForm.Get("redirect_uri") != "https://alpha.sap13ns.site/admin/plugins/oauthdemo/oauth/demo/callback" {
			rec.WriteHeader(400)
			rec.WriteString(`{"error":"invalid_grant"}`)
			break
		}
		rec.WriteString(`{"access_token":"tenant-at","refresh_token":"tenant-rt","expires_in":3600}`)
	case "api.example.test/userinfo":
		rec.WriteString(`{"sub":"t-1","name":"Alpha Owner"}`)
	default:
		rec.WriteHeader(404)
	}
	return rec.Result(), nil
}

func oauthFixture(t *testing.T) []byte {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dir := filepath.Join(t.TempDir(), "oauthdemo")
	os.MkdirAll(dir, 0o755)
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", filepath.Join(dir, "plugin.wasm"), "./internal/plugin/testdata/oauth")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the plugin: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "internal", "plugin", "testdata", "oauth", "plugin.json"))
	os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644)
	return packPlugin(t, dir, nil)
}

// TestPluginOAuthTenant: a site of a network connects a plugin on its own
// host, with a redirect URI there, and the tokens stay in its schema.
func TestPluginOAuthTenant(t *testing.T) {
	pkg := oauthFixture(t)
	e := newNetEnv(t, nil)
	alpha, beta := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	b, csrf := e.siteAdmin(alpha, "admin")
	base := "https://" + alpha.PrimaryHost
	if rec := b.get(base + "/admin/"); rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	host := e.entry(alpha.ID).app.Plugins()
	if _, err := host.Install(e.ctx, bytes.NewReader(pkg), int64(len(pkg)), plugin.InstallOptions{Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	b.enable(alpha, csrf, "oauthdemo")
	if p := host.Get("oauthdemo"); p == nil || p.Status != plugin.StatusEnabled {
		t.Fatalf("not enabled: %+v", p)
	}
	if err := host.SaveSettings(e.ctx, "oauthdemo", map[string]string{"client_id": "c", "client_secret": "s"}); err != nil {
		t.Fatal(err)
	}
	fake := &oauthProvider{}
	host.SetOAuthTransport(fake)

	rec := b.do("POST", base+"/admin/plugins/oauthdemo/oauth/demo/connect", url.Values{"csrf": {csrf}})
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusSeeOther || loc == nil || loc.Host != "auth.example.test" ||
		loc.Query().Get("redirect_uri") != base+"/admin/plugins/oauthdemo/oauth/demo/callback" {
		t.Fatalf("connect: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	rec = b.get(base + "/admin/plugins/oauthdemo/oauth/demo/callback?" + url.Values{"state": {loc.Query().Get("state")}, "code": {"good"}}.Encode())
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/plugins/oauthdemo#connections" || fake.exchanges != 1 {
		t.Fatalf("callback: %d %s (%d exchanges)", rec.Code, rec.Header().Get("Location"), fake.exchanges)
	}
	list, err := e.siteStore(alpha).PluginOAuthConnections(e.ctx, "")
	if err != nil || len(list) != 1 || list[0].AccountName != "Alpha Owner" {
		t.Fatalf("alpha's connections: %+v %v", list, err)
	}
	if list, _ := e.siteStore(beta).PluginOAuthConnections(e.ctx, ""); len(list) != 0 {
		t.Fatalf("beta sees alpha's connection: %+v", list)
	}
	if !host.WantsCron() {
		t.Fatal("the site's renewals need its hourly cron")
	}
}
