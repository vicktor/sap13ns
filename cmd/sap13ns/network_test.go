package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/network"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

const testPassword = "correct horse battery staple 42"

// netCLI is a network on the test database, set up for the commands
// through SAP13NS_* variables, as an operator's env file would.
type netCLI struct {
	t    *testing.T
	pool *pgxpool.Pool
	out  *bytes.Buffer
	data string
	dns  *cliDNS
}

// cliDNS answers the DNS checks of the commands: no test asks a real DNS
// server.
type cliDNS struct {
	mu    sync.Mutex
	txt   map[string][]string
	cname map[string]string
}

func (d *cliDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.txt[name]; ok {
		return v, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (d *cliDNS) LookupCNAME(_ context.Context, host string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.cname[host]; ok {
		return c + ".", nil
	}
	return "", &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (d *cliDNS) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if host == "sap13ns.test" {
		return []netip.Addr{netip.MustParseAddr("192.0.2.80")}, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func (d *cliDNS) set(change func(*cliDNS)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	change(d)
}

func newNetCLI(t *testing.T) *netCLI {
	pool := testutil.Schemas(t)
	data := t.TempDir()
	for k, v := range map[string]string{
		"DATABASE_URL": testutil.URL(), "MULTISITE": "true", "BASE_URL": "https://sap13ns.test",
		"SITES_DOMAIN": "sap13ns.test", "SECRET_KEY": strings.Repeat("ab", 32), "DATA_DIR": data,
		"LOG_LEVEL": "error", "UPDATE_CHECK": "false",
	} {
		t.Setenv("SAP13NS_"+k, v)
	}
	buf := &bytes.Buffer{}
	prev, prevDNS := out, dnsResolver
	dns := &cliDNS{txt: map[string][]string{}, cname: map[string]string{}}
	out, dnsResolver = buf, dns
	t.Cleanup(func() { out, dnsResolver = prev, prevDNS })
	return &netCLI{t: t, pool: pool, out: buf, data: data, dns: dns}
}

// run runs a command with stdin (the password) and returns its output.
func (c *netCLI) run(stdin string, cmd func([]string) error, args ...string) (string, error) {
	c.t.Helper()
	f, err := os.CreateTemp(c.t.TempDir(), "stdin")
	if err != nil {
		c.t.Fatal(err)
	}
	f.WriteString(stdin)
	f.Seek(0, io.SeekStart)
	prev := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = prev; f.Close() }()
	c.out.Reset()
	err = cmd(args)
	return c.out.String(), err
}

func (c *netCLI) must(stdin string, cmd func([]string) error, args ...string) string {
	c.t.Helper()
	s, err := c.run(stdin, cmd, args...)
	if err != nil {
		c.t.Fatalf("%v: %v", args, err)
	}
	return s
}

func (c *netCLI) fails(want string, cmd func([]string) error, args ...string) {
	c.t.Helper()
	if _, err := c.run("", cmd, args...); err == nil || !strings.Contains(err.Error(), want) {
		c.t.Fatalf("%v: got %v, want an error with %q", args, err, want)
	}
}

func (c *netCLI) network() *network.Network {
	c.t.Helper()
	cfg, err := config.Load(true)
	if err != nil {
		c.t.Fatal(err)
	}
	n, err := network.New(c.pool, cfg, testutil.Log())
	if err != nil {
		c.t.Fatal(err)
	}
	return n
}

func (c *netCLI) store(schema string) *store.Store {
	c.t.Helper()
	h, err := db.Scope(c.pool, schema)
	if err != nil {
		c.t.Fatal(err)
	}
	return store.New(h)
}

func TestNetworkCLI(t *testing.T) {
	c := newNetCLI(t)
	ctx := context.Background()

	c.fails("give -username and -email", networkCmd, "init")
	got := c.must(testPassword+"\n", networkCmd, "init", "-username", "Root", "-email", "root@example.test")
	if !strings.Contains(got, "created super administrator root") || strings.Contains(got, "password:") {
		t.Fatalf("init: %q", got)
	}
	if got := c.must("", networkCmd, "init"); !strings.Contains(got, "administrators: 1") {
		t.Fatalf("second init: %q", got)
	}
	n := c.network()
	portal, err := n.NetworkSite(ctx)
	if err != nil || portal.PrimaryHost != "sap13ns.test" {
		t.Fatalf("portal %+v %v", portal, err)
	}
	if admins, err := n.NetworkAdmins(ctx); err != nil || len(admins) != 1 || admins[0].Username != "root" {
		t.Fatalf("admins %+v %v", admins, err)
	}

	// A network account, then its sites.
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "Owner@Example.test", "-role", "subscriber")
	c.fails("no network account", siteCmd, "create", "-slug", "alpha", "-owner", "nobody@example.test")
	c.fails("unknown language", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-lang", "xx")
	c.fails("reserved", siteCmd, "create", "-slug", "admin", "-owner", "owner@example.test", "-plan", "pro")
	got = c.must("", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-plan", "pro", "-title", "Alpha", "-lang", "en")
	if !strings.Contains(got, "created site alpha") || !strings.Contains(got, "active") || !strings.Contains(got, "alpha.sap13ns.test") {
		t.Fatalf("create: %q", got)
	}
	got = c.must("", siteCmd, "create", "-slug", "beta", "-owner", "owner@example.test")
	if !strings.Contains(got, "pending") || !strings.Contains(got, "approves") {
		t.Fatalf("create on a hand-approved plan: %q", got)
	}
	alpha, err := n.SiteBySlug(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	set, err := c.store(alpha.Schema).LoadSettings(ctx)
	if err != nil || set.SiteTitle != "Alpha" || set.Language != "en" {
		t.Fatalf("settings %q %q %v", set.SiteTitle, set.Language, err)
	}

	got = c.must("", siteCmd, "list")
	for _, want := range []string{"alpha", "beta", "network", "owner@example.test", "pending"} {
		if !strings.Contains(got, want) {
			t.Fatalf("list lacks %q:\n%s", want, got)
		}
	}
	got = c.must("", siteCmd, "list", "-json", "-status", "active")
	var views []map[string]any
	if err := json.Unmarshal([]byte(got), &views); err != nil || len(views) != 2 {
		t.Fatalf("list -json: %v %s", err, got)
	}
	if _, ok := views[0]["secret"]; ok || strings.Contains(got, "ecret") {
		t.Fatalf("list -json shows the secret: %s", got)
	}
	c.fails("unknown status", siteCmd, "list", "-status", "gone")

	// Status changes.
	c.fails("-reason is required", siteCmd, "suspend", "-slug", "alpha")
	if got := c.must("", siteCmd, "suspend", "-slug", "alpha", "-reason", "spam"); !strings.Contains(got, "suspended") {
		t.Fatalf("suspend: %q", got)
	}
	c.fails("not allowed", siteCmd, "suspend", "-slug", "alpha", "-reason", "again")
	if got := c.must("", siteCmd, "resume", "-slug", "alpha"); !strings.Contains(got, "is active") {
		t.Fatalf("resume: %q", got)
	}
	c.fails("no site", siteCmd, "resume", "-slug", "nope")

	// Domains.
	got = c.must("", siteCmd, "domain", "add", "WWW.Alpha.Example", "-slug", "alpha")
	d, err := n.Domain(ctx, "www.alpha.example")
	if err != nil || !strings.Contains(got, "_sap13ns-verify.www.alpha.example") || !strings.Contains(got, d.VerifyToken) {
		t.Fatalf("domain add: %v %q", err, got)
	}
	c.fails("not verified", siteCmd, "domain", "primary", "www.alpha.example")
	// verify runs the DNS check: the TXT record, then the address.
	c.fails(`no TXT record _sap13ns-verify.www.alpha.example with the value "sap13ns-site-verification=`+d.VerifyToken+`"`,
		siteCmd, "domain", "verify", "www.alpha.example")
	c.dns.set(func(c *cliDNS) {
		c.txt["_sap13ns-verify.www.alpha.example"] = []string{"sap13ns-site-verification=" + d.VerifyToken}
	})
	c.fails("does not point to the network. It needs a CNAME to sap13ns.test, or A/AAAA records with the addresses of sap13ns.test",
		siteCmd, "domain", "verify", "www.alpha.example")
	if d, _ := n.Domain(ctx, "www.alpha.example"); d.VerifiedAt != nil || d.CheckError != network.CheckAddress {
		t.Fatalf("after a failed check: %+v", d)
	}
	c.dns.set(func(c *cliDNS) { c.cname["www.alpha.example"] = "sap13ns.test" })
	if got := c.must("", siteCmd, "domain", "verify", "www.alpha.example"); !strings.Contains(got, "verified") {
		t.Fatalf("domain verify: %q", got)
	}
	// -force records it without a check.
	c.must("", siteCmd, "domain", "add", "shop.alpha.example", "-slug", "alpha")
	c.must("", siteCmd, "domain", "verify", "shop.alpha.example", "-force")
	if d, _ := n.Domain(ctx, "shop.alpha.example"); d.VerifiedAt == nil {
		t.Fatalf("-force: %+v", d)
	}
	c.must("", siteCmd, "domain", "remove", "shop.alpha.example")
	c.must("", siteCmd, "domain", "primary", "www.alpha.example")
	if s, _ := n.Site(ctx, alpha.ID); s.PrimaryHost != "www.alpha.example" {
		t.Fatalf("primary %s", s.PrimaryHost)
	}
	c.fails("primary", siteCmd, "domain", "remove", "www.alpha.example")
	c.must("", siteCmd, "domain", "primary", "alpha.sap13ns.test")
	c.must("", siteCmd, "domain", "remove", "www.alpha.example")
	c.fails("no site has the host", siteCmd, "domain", "verify", "www.alpha.example")
	c.fails("belongs to the network", siteCmd, "domain", "add", "-slug", "alpha", "x.sap13ns.test")

	// -site works on that site's schema only.
	c.must(testPassword, userCmd, "create", "-site", "alpha", "-username", "editor", "-email", "editor@example.test", "-role", "editor")
	if _, err := c.store(alpha.Schema).UserByUsername(ctx, "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(n.DB()).UserByUsername(ctx, "editor"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the site's user is in the portal: %v", err)
	}
	c.must(testPassword+"x\n", userCmd, "reset-password", "-site", "alpha", "-username", "editor")
	c.fails("-site SLUG", userCmd, "create", "-username", "x", "-email", "x@example.test")
	c.fails("no site", userCmd, "create", "-site", "nope", "-username", "x", "-email", "x@example.test")
	c.must("", pluginCmd, "list", "-site", "alpha")
	c.must("", assistantCmd, "status", "-site", "alpha")
	c.fails("-site SLUG", backupCmd, "create")

	// migrate goes through every site, or one.
	c.must("", migrate)
	c.must("", migrate, "-site", "alpha")
	c.fails("no site", migrate, "-site", "nope")

	// Deleting needs -yes, and a deleted site is out of reach.
	c.fails("-yes", siteCmd, "delete", "-slug", "beta")
	if got := c.must("", siteCmd, "delete", "-slug", "beta", "-yes"); !strings.Contains(got, "deleted") {
		t.Fatalf("delete: %q", got)
	}
	if got := c.must("", siteCmd, "list"); strings.Contains(got, "beta") {
		t.Fatalf("a deleted site is listed:\n%s", got)
	}
	c.fails("is deleted", userCmd, "create", "-site", "beta", "-username", "x", "-email", "x@example.test")

	// Purging needs -yes and a deleted site; then nothing of it is left.
	beta, err := n.SiteBySlug(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	backups := filepath.Join(c.data, "backups", "sites", strconv.FormatInt(beta.ID, 10))
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backups, "sap13ns-site-beta-20261019-000000.zip"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	c.fails("only a deleted site", siteCmd, "purge", "-slug", "alpha", "-yes")
	c.fails("-yes", siteCmd, "purge", "-slug", "beta")
	if got := c.must("", siteCmd, "purge", "-slug", "beta", "-yes"); !strings.Contains(got, "purged") {
		t.Fatalf("purge: %q", got)
	}
	var schemas int
	if err := n.DB().QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname = $1`, beta.Schema).Scan(&schemas); err != nil || schemas != 0 {
		t.Fatalf("the schema of a purged site: %d %v", schemas, err)
	}
	if _, err := os.Stat(n.Dir(beta.ID)); !os.IsNotExist(err) {
		t.Fatalf("the directory of a purged site: %v", err)
	}
	if _, err := os.Stat(backups); !os.IsNotExist(err) {
		t.Fatalf("the backups of a purged site: %v", err)
	}
	c.fails("no site", siteCmd, "purge", "-slug", "beta", "-yes")
}

// TestSingleSiteFlags: on a single site, -site and the network commands
// are refused before anything is touched.
func TestSingleSiteFlags(t *testing.T) {
	t.Setenv("SAP13NS_DATABASE_URL", "postgres:///nowhere?host=/nonexistent")
	t.Setenv("SAP13NS_SECRET_KEY", strings.Repeat("ab", 32))
	t.Setenv("SAP13NS_MULTISITE", "")
	t.Setenv("SAP13NS_LOG_LEVEL", "error")
	for _, c := range []struct {
		cmd  func([]string) error
		args []string
	}{
		{userCmd, []string{"create", "-site", "a", "-username", "x", "-email", "x@example.test"}},
		{assistantCmd, []string{"status", "-site", "a"}},
		{pluginCmd, []string{"list", "-site", "a"}},
		{themeCmd, []string{"list", "-site", "a"}},
		{backupCmd, []string{"list", "-site", "a"}},
		{exportCmd, []string{"-site", "a", "-o", "a.xml"}},
		{migrate, []string{"-site", "a"}},
		{siteCmd, []string{"list"}},
		{networkCmd, []string{"init"}},
	} {
		if err := c.cmd(c.args); err == nil || !strings.Contains(err.Error(), "SAP13NS_MULTISITE=true") {
			t.Errorf("%v: %v", c.args, err)
		}
	}
}

// TestServeNetwork runs `serve` on a network, on a Unix socket, and
// stops it as SIGTERM would.
func TestServeNetwork(t *testing.T) {
	c := newNetCLI(t)
	c.must(testPassword, networkCmd, "init", "-username", "root", "-email", "root@example.test")
	c.must(testPassword, userCmd, "create", "-site", "network", "-username", "owner", "-email", "owner@example.test", "-role", "subscriber")
	c.must("", siteCmd, "create", "-slug", "alpha", "-owner", "owner@example.test", "-plan", "pro", "-title", "Alpha site")

	dir, err := os.MkdirTemp("", "s13") // a socket path is short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s.sock")
	t.Setenv("SAP13NS_LISTEN", "unix:"+sock)
	cfg, err := config.Load(true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serveNetwork(ctx, cfg, testutil.Log()) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("serve did not stop")
		}
	}()

	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(host, path string) (int, string) {
		t.Helper()
		var resp *http.Response
		for deadline := time.Now().Add(20 * time.Second); ; {
			req, _ := http.NewRequest("GET", "http://"+host+path, nil)
			resp, err = client.Do(req)
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("x", "/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz: %d %q", code, body)
	}
	if code, body := get("alpha.sap13ns.test", "/"); code != http.StatusOK || !strings.Contains(body, "Alpha site") {
		t.Fatalf("alpha: %d", code)
	}
	if code, _ := get("sap13ns.test", "/"); code != http.StatusOK {
		t.Fatalf("portal: %d", code)
	}
	if code, _ := get("nope.sap13ns.test", "/"); code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", code)
	}

	// A suspension from the CLI reaches the running server.
	c.must("", siteCmd, "suspend", "-slug", "alpha", "-reason", "abuse")
	for deadline := time.Now().Add(10 * time.Second); ; {
		code, _ := get("alpha.sap13ns.test", "/")
		if code == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alpha after the suspension: %d", code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
