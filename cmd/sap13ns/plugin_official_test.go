package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"sap13ns/internal/config"
	"sap13ns/internal/plugin"
	"sap13ns/internal/testutil"
)

// cliOfficialBundle builds internal/plugin/testdata/hooks as an official
// plugin and makes it the bundle the commands see.
func cliOfficialBundle(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	root := filepath.Join("..", "..")
	dir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-trimpath", "-o", filepath.Join(dir, "plugin.wasm"), "./internal/plugin/testdata/hooks")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "testdata", "hooks", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["author"], m["license"] = "sap13ns", "MIT"
	raw, _ = json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := plugin.Pack(dir, &buf); err != nil {
		t.Fatal(err)
	}
	bundle := fstest.MapFS{"hooks.zip": {Data: buf.Bytes()}}
	prev := officialFS
	officialFS = func(*config.Config) fs.FS { return bundle }
	t.Cleanup(func() { officialFS = prev })
}

// TestPluginCLIOfficial: plugin list -official and plugin install
// -official, on a single site and on a site of a network.
func TestPluginCLIOfficial(t *testing.T) {
	st := testutil.Store(t)
	cliOfficialBundle(t)
	data := t.TempDir()
	for k, v := range map[string]string{
		"DATABASE_URL": testutil.URL(), "MULTISITE": "", "BASE_URL": "http://127.0.0.1:8177", "SECRET_KEY": strings.Repeat("ab", 32),
		"DATA_DIR": data, "PLUGINS_DIR": "", "LOG_LEVEL": "error", "UPDATE_CHECK": "false",
	} {
		t.Setenv("SAP13NS_"+k, v)
	}
	buf := &bytes.Buffer{}
	prev := out
	out = buf
	t.Cleanup(func() { out = prev })
	run := func(args ...string) (string, error) {
		t.Helper()
		buf.Reset()
		err := pluginCmd(args)
		return buf.String(), err
	}
	must := func(args ...string) string {
		t.Helper()
		s, err := run(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, s)
		}
		return s
	}
	fails := func(want string, args ...string) {
		t.Helper()
		if s, err := run(args...); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%v: got %v, want an error with %q\n%s", args, err, want, s)
		}
	}

	if got := must("list", "-official"); !strings.Contains(got, "hooks") || !strings.Contains(got, "1.0.0") ||
		!strings.Contains(got, "mail posts:read") || !strings.Contains(got, "INSTALLED") {
		t.Fatalf("list -official: %q", got)
	}
	fails("no official plugin", "install", "-official", "nope")
	fails("usage", "install", "-official")
	if got := must("install", "-official", "hooks"); !strings.Contains(got, "hooks 1.0.0 installed, disabled") {
		t.Fatalf("install -official: %q", got)
	}
	recs, err := st.ListPluginRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := recs["hooks"]; !ok || r.Source != plugin.SourceOfficial || r.Enabled || r.ScanReport == nil {
		t.Fatalf("record: %+v", r)
	}
	if got := must("list", "-official"); !strings.Contains(got, "official") {
		t.Fatalf("list -official after the install: %q", got)
	}
	if got := must("list"); !strings.Contains(got, "hooks") || !strings.Contains(got, "official") {
		t.Fatalf("list: %q", got)
	}

	// A binary without the bundle says so.
	prevFS := officialFS
	officialFS = func(*config.Config) fs.FS { return fstest.MapFS{"README.md": {Data: []byte("x")}} }
	fails("carries no official plugins", "list", "-official")
	officialFS = prevFS
}

// TestPluginCLIOfficialNetwork: on a network, -site names the site.
func TestPluginCLIOfficialNetwork(t *testing.T) {
	c := newNetCLI(t)
	cliOfficialBundle(t)
	c.must(testPassword+"\n", networkCmd, "init", "-username", "Root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	c.must("", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-plan", "personal")
	alpha, err := c.network().SiteBySlug(context.Background(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	c.fails("name the site with -site", pluginCmd, "install", "-official", "hooks")
	if got := c.must("", pluginCmd, "install", "-site", "alpha", "-official", "hooks"); !strings.Contains(got, "hooks 1.0.0 installed") {
		t.Fatalf("install: %q", got)
	}
	if _, err := os.Stat(filepath.Join(c.network().Dir(alpha.ID), "plugins", "hooks", "plugin.wasm")); err != nil {
		t.Fatal(err)
	}
	if got := c.must("", pluginCmd, "list", "-site", "alpha", "-official"); !strings.Contains(got, "official") {
		t.Fatalf("list: %q", got)
	}
}
