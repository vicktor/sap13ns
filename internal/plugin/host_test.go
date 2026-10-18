package plugin

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/content"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

type testLinks struct{}

func (testLinks) PostInfo(p *store.Post) PostInfo {
	return PostInfo{ID: p.ID, Title: p.Title, Lang: p.Lang, URL: "/blog/" + p.Slug}
}

// installPlugin compiles a plugin package to wasm and copies it with its
// manifest into dir/<id>.
func installPlugin(t *testing.T, dir, pkg, id string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	dst := filepath.Join(dir, id)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", filepath.Join(dst, "plugin.wasm"), pkg)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	manifest, err := os.ReadFile(filepath.Join(root, strings.TrimPrefix(pkg, "./"), "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}

func newTestHost(t *testing.T, st *store.Store, dir string) *Host {
	t.Helper()
	ctx := context.Background()
	h, err := NewHost(ctx, dir, "", st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() SiteInfo { return SiteInfo{URL: "https://example.test", Title: "Test", Language: "es"} }, testLinks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	if err := h.Load(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func adminUser(t *testing.T, st *store.Store) int64 {
	id, err := st.CreateUser(context.Background(), store.NewUser{Username: "admin", Email: "a@example.test", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestApprovalPinsHash(t *testing.T) {
	st := testutil.Store(t)
	dir := t.TempDir()
	pdir := installPlugin(t, dir, "./internal/plugin/testdata/callout", "callout")
	h := newTestHost(t, st, dir)
	ctx := context.Background()

	p := h.Get("callout")
	if p == nil || p.Status != StatusAvailable {
		t.Fatalf("status = %v, want available", p)
	}
	if h.HasShortcode("callout") {
		t.Fatal("shortcode active before approval")
	}
	if err := h.Approve(ctx, "callout", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	if got := h.Get("callout").Status; got != StatusEnabled {
		t.Fatalf("status = %s, want enabled (%s)", got, h.Get("callout").Err)
	}

	html, err := h.Shortcode(ctx, "callout", map[string]string{"type": "warning", "title": "Ojo"}, "Texto <b>importante</b>", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, `class="alert sc-callout"`) || !strings.Contains(html, "Texto &lt;b&gt;importante&lt;/b&gt;") {
		t.Fatalf("unexpected html: %s", html)
	}

	// Full pipeline: markdown + shortcode + sanitizer + icon expansion.
	out := content.Render("Hola\n\n[callout type=\"info\"]Dentro[/callout]\n\n`[callout]` en código", h.HasShortcode,
		func(sc content.Shortcode) (string, error) { return h.Shortcode(ctx, sc.Name, sc.Attrs, sc.Inner, nil) },
		func(name string) string { return "<svg data-test=\"" + name + "\"></svg>" })
	if !strings.Contains(string(out), `<div class="alert sc-callout"`) || !strings.Contains(string(out), `<code>[callout]</code>`) {
		t.Fatalf("render pipeline: %s", out)
	}
	if !strings.Contains(string(out), `data-test="info"`) {
		t.Fatalf("icon placeholder not expanded: %s", out)
	}

	// Tampering with the wasm file disables the plugin until re-approved.
	f, err := os.OpenFile(filepath.Join(pdir, "plugin.wasm"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte{0})
	f.Close()
	if err := h.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.Get("callout").Status; got != StatusModified {
		t.Fatalf("status after tampering = %s, want modified", got)
	}
	if h.HasShortcode("callout") {
		t.Fatal("tampered plugin still active")
	}
}

func TestSandbox(t *testing.T) {
	st := testutil.Store(t)
	dir := t.TempDir()
	installPlugin(t, dir, "./internal/plugin/testdata/evil", "evil")
	h := newTestHost(t, st, dir)
	ctx := context.Background()
	if err := h.Approve(ctx, "evil", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	call := func(name string) (string, error) {
		return h.Shortcode(ctx, name, nil, "", nil)
	}

	if out, _ := call("readfile"); !strings.HasPrefix(out, "read:") || strings.Contains(out, "root:") {
		t.Errorf("filesystem access: %q", out)
	}
	if out, _ := call("env"); out != "env:" {
		t.Errorf("environment leaked: %q", out)
	}
	if out, _ := call("dial"); strings.Contains(out, "dial:ok") {
		t.Errorf("raw sockets available: %q", out)
	}
	if out, _ := call("kv"); !strings.Contains(out, "permission denied") {
		t.Errorf("kv without permission: %q", out)
	}
	if out, _ := call("posts"); !strings.Contains(out, "permission denied") {
		t.Errorf("posts without permission: %q", out)
	}
	if out, _ := call("fetch"); !strings.Contains(out, "permission denied") {
		t.Errorf("http from a render hook: %q", out)
	}
	if _, err := call("panic"); err == nil || !strings.Contains(err.Error(), "panic") {
		t.Errorf("panic not reported: %v", err)
	}

	start := time.Now()
	if _, err := call("loop"); err == nil {
		t.Error("infinite loop returned without error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("infinite loop not interrupted quickly: %s", d)
	}
	if _, err := call("mem"); err == nil {
		t.Error("allocating 4 GiB succeeded")
	}
	// The plugin keeps working after traps (broken instances are discarded).
	if out, _ := call("kv"); !strings.Contains(out, "kv:") {
		t.Errorf("plugin unusable after a trap: %q", out)
	}

	raw, _ := call("xss")
	clean := string(content.SanitizePluginHTML(raw, nil))
	for _, bad := range []string{"<script", "onerror", "javascript:", "onclick", "<iframe"} {
		if strings.Contains(clean, bad) {
			t.Errorf("sanitizer let %q through: %s", bad, clean)
		}
	}
	if !strings.Contains(clean, `<div class="alert">ok</div>`) {
		t.Errorf("sanitizer removed allowed markup: %s", clean)
	}

	// Events may use the network, but only towards granted public hosts.
	h.Emit("post.published", map[string]any{"id": 1})
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		n := 0
		for _, l := range h.Get("evil").Logs() {
			if strings.HasPrefix(l.Text, "fetch ") {
				n++
			}
		}
		if n == 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs := map[string]string{}
	for _, l := range h.Get("evil").Logs() {
		if u, res, ok := strings.Cut(strings.TrimPrefix(l.Text, "fetch "), ": "); ok && strings.HasPrefix(l.Text, "fetch ") {
			logs[u] = res
		}
	}
	for _, u := range []string{"https://127.0.0.1/", "https://localhost/", "https://169.254.169.254/latest/meta-data/"} {
		if logs[u] != "permission denied" {
			t.Errorf("fetch %s = %q, want permission denied", u, logs[u])
		}
	}
	if !strings.Contains(logs["http://example.com/"], "https://") {
		t.Errorf("plain http allowed: %q", logs["http://example.com/"])
	}
}

func TestAddrAllowed(t *testing.T) {
	for _, tc := range []struct {
		host string
		ok   bool
	}{
		{"127.0.0.1", false}, {"10.1.2.3", false}, {"192.168.1.1", false}, {"169.254.169.254", false},
		{"100.64.0.1", false}, {"::1", false}, {"fe80::1", false}, {"fd00::1", false}, {"::ffff:127.0.0.1", false},
		{"93.184.216.34", true}, {"2606:4700::1111", true},
	} {
		addr := mustAddr(t, tc.host)
		if got := addrAllowed(addr); got != tc.ok {
			t.Errorf("addrAllowed(%s) = %v, want %v", tc.host, got, tc.ok)
		}
	}
}

func TestHostAllowed(t *testing.T) {
	g := []string{"http:api.example.com", "http:*.hooks.test"}
	for host, want := range map[string]bool{
		"api.example.com": true, "API.EXAMPLE.COM.": true, "example.com": false, "evil-api.example.com": false,
		"a.hooks.test": true, "hooks.test": false, "xhooks.test": false,
	} {
		if got := hostAllowed(g, host); got != want {
			t.Errorf("hostAllowed(%s) = %v, want %v", host, got, want)
		}
	}
	if !hostAllowed([]string{"http:*"}, "anything.test") {
		t.Error("wildcard permission")
	}
}

func TestManifestValidation(t *testing.T) {
	bad := []string{
		`{"abi":2,"id":"x1","name":"X","version":"1.0.0","hooks":["widget"]}`,
		`{"abi":1,"id":"../x","name":"X","version":"1.0.0","hooks":["shortcode"],"shortcodes":["a"]}`,
		`{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["shortcode"]}`,
		`{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["event"],"events":["db.dump"]}`,
		`{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["head.meta"],"permissions":["fs:/"]}`,
		`{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["head.meta"],"wasm":"../../bin/x.wasm"}`,
		`{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["head.meta"],"unknown":true}`,
	}
	for _, m := range bad {
		if _, err := ParseManifest([]byte(m)); err == nil {
			t.Errorf("accepted invalid manifest %s", m)
		}
	}
	good := `{"abi":1,"id":"ok-id","name":"X","version":"1.0.0","hooks":["head.meta","event"],"events":["cron.hourly"],"permissions":["kv","http:*.example.com"]}`
	if _, err := ParseManifest([]byte(good)); err != nil {
		t.Errorf("rejected valid manifest: %v", err)
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
