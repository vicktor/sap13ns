package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

type fixture struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	n      *Network
	portal *store.Store // the network schema: accounts
	cfg    *config.Config
}

func setup(t *testing.T) *fixture {
	t.Helper()
	pool := testutil.Schemas(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool, testutil.Log()); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse("https://sap13ns.site")
	cfg := &config.Config{Multisite: true, BaseURL: base, SitesDomain: "sap13ns.site",
		SecretKey: bytes.Repeat([]byte{7}, 32), DataDir: t.TempDir()}
	n, err := New(pool, cfg, testutil.Log())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: ctx, pool: pool, n: n, portal: store.New(n.DB()), cfg: cfg}
}

// account creates a network account.
func (f *fixture) account(name string) int64 {
	f.t.Helper()
	id, err := f.portal.CreateUser(f.ctx, store.NewUser{Username: name, Email: name + "@example.com", Role: "subscriber", PasswordHash: "x"})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) site(slug string, owner int64, plan string) *Site {
	f.t.Helper()
	s, err := f.n.CreateSite(f.ctx, NewSite{Slug: slug, OwnerID: owner, Plan: plan})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// schemas lists the site schemas that exist.
func (f *fixture) schemas() []string {
	f.t.Helper()
	rows, err := f.n.db.Query(f.ctx, `SELECT nspname::text FROM pg_namespace WHERE nspname ~ '^site_' ORDER BY 1`)
	if err != nil {
		f.t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		f.t.Fatal(err)
	}
	return names
}

func (f *fixture) dirs() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.cfg.DataDir, "sites"))
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// listener receives the network's notifications.
type listener struct {
	t    *testing.T
	conn *pgx.Conn
}

func listen(t *testing.T) *listener {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testutil.URL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	if _, err := conn.Exec(ctx, `LISTEN `+Channel); err != nil {
		t.Fatal(err)
	}
	return &listener{t: t, conn: conn}
}

func (l *listener) next() string {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, err := l.conn.WaitForNotification(ctx)
	if err != nil {
		l.t.Fatalf("no notification: %v", err)
	}
	return n.Payload
}

func (l *listener) none() {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if n, err := l.conn.WaitForNotification(ctx); err == nil {
		l.t.Fatalf("unexpected notification %q", n.Payload)
	}
}

func TestMigrate(t *testing.T) {
	f := setup(t)
	if err := Migrate(f.ctx, f.pool, testutil.Log()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	plans, err := f.n.Plans(f.ctx)
	if err != nil || len(plans) != 3 {
		t.Fatalf("Plans = %+v, %v", plans, err)
	}
	if p := plans[0]; p.Slug != "personal" || p.Paid || !p.HandApproved || p.SitesPerGrant != 1 || p.StorageBytes != 1<<30 || p.CustomDomains != 0 {
		t.Fatalf("personal plan: %+v", p)
	}
	if p := plans[2]; p.Slug != "business" || !p.Paid || p.HandApproved || p.SitesPerGrant != 1 || !p.PluginUploads {
		t.Fatalf("business plan: %+v", p)
	}
	var site, net int
	if err := f.n.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM schema_migrations), (SELECT count(*) FROM network_migrations)`).
		Scan(&site, &net); err != nil || site < 10 || net != 2 {
		t.Fatalf("migrations: site %d, network %d, %v", site, net, err)
	}
}

func TestCreateSite(t *testing.T) {
	f := setup(t)
	l := listen(t)
	alice, bob := f.account("alice"), f.account("bob")

	s := f.site("blog", alice, "pro")
	if s.Kind != KindTenant || s.Slug != "blog" || s.Schema != db.SiteSchema(s.ID) || s.OwnerID != alice || s.Plan != "pro" ||
		s.Status != StatusActive || s.PrimaryHost != "blog.sap13ns.site" || s.RPID != "blog.sap13ns.site" {
		t.Fatalf("site: %+v", s)
	}
	if got := l.next(); got != "" {
		t.Fatalf("first notification %q, want the network-wide one of the new row", got)
	}
	if got := l.next(); got != strconv.FormatInt(s.ID, 10) {
		t.Fatalf("second notification %q, want the site id", got)
	}

	// The schema exists with every site migration, and is empty.
	h, err := db.Scope(f.pool, s.Schema)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.New(h).CountUsers(f.ctx); err != nil || n != 0 {
		t.Fatalf("site schema: %d users, %v", n, err)
	}
	fi, err := os.Stat(f.n.Dir(s.ID))
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750 {
		t.Fatalf("site directory: %v, %v", fi, err)
	}
	domains, err := f.n.Domains(f.ctx, s.ID)
	if err != nil || len(domains) != 1 || domains[0].Host != "blog.sap13ns.site" || domains[0].Kind != DomainDefault ||
		!domains[0].Primary || domains[0].VerifiedAt == nil {
		t.Fatalf("Domains = %+v, %v", domains, err)
	}
	m, err := f.n.Member(f.ctx, s.ID, alice)
	if err != nil || m.Role != "admin" || m.Username != "alice" {
		t.Fatalf("owner membership: %+v, %v", m, err)
	}

	// The secret opens and builds the site's configuration.
	secret, err := f.n.SiteSecret(s)
	if err != nil || len(secret) != 32 {
		t.Fatalf("SiteSecret: %d bytes, %v", len(secret), err)
	}
	if _, err := config.ForSite(f.cfg, config.SiteParams{ID: s.ID, Host: s.PrimaryHost, Secret: secret, RPID: s.RPID}); err != nil {
		t.Fatal(err)
	}

	// A Personal site waits for approval.
	p := f.site("diary", bob, "personal")
	if p.Status != StatusPending {
		t.Fatalf("personal site status %s", p.Status)
	}

	// A taken name changes nothing.
	before := f.schemas()
	if _, err := f.n.CreateSite(f.ctx, NewSite{Slug: "blog", OwnerID: bob, Plan: "pro"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate slug: err = %v", err)
	}
	if after := f.schemas(); fmt.Sprint(after) != fmt.Sprint(before) {
		t.Fatalf("schemas %v -> %v", before, after)
	}

	sites, total, err := f.n.ListSites(f.ctx, SiteQuery{})
	if err != nil || total != 2 || len(sites) != 2 || sites[0].ID != s.ID || sites[1].ID != p.ID {
		t.Fatalf("ListSites = %d %+v, %v", total, sites, err)
	}
	sites, total, err = f.n.ListSites(f.ctx, SiteQuery{Status: StatusPending})
	if err != nil || total != 1 || sites[0].Slug != "diary" {
		t.Fatalf("pending sites = %d %+v, %v", total, sites, err)
	}
	sites, total, err = f.n.ListSites(f.ctx, SiteQuery{OwnerID: alice})
	if err != nil || total != 1 || sites[0].Slug != "blog" {
		t.Fatalf("alice's sites = %d %+v, %v", total, sites, err)
	}
	got, err := f.n.SiteBySlug(f.ctx, "diary")
	if err != nil || got.ID != p.ID {
		t.Fatalf("SiteBySlug = %+v, %v", got, err)
	}
	if _, err := f.n.Site(f.ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing site: %v", err)
	}
}

func TestSlugs(t *testing.T) {
	for _, ok := range []string{"a", "blog", "my-site", "x1", strings.Repeat("a", 63)} {
		if err := ValidSlug(ok); err != nil {
			t.Errorf("ValidSlug(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "My", "a_b", "a.b", "xn--bcher-kva", "ab--c", strings.Repeat("a", 64),
		"www", "admin", "network", "mail", "api", "sso"} {
		if err := ValidSlug(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidSlug(%q) = %v, want ErrInvalid", bad, err)
		}
	}

	f := setup(t)
	alice := f.account("alice")
	for _, tc := range []struct {
		ns   NewSite
		want error
	}{
		{NewSite{Slug: "www", OwnerID: alice, Plan: "pro"}, ErrInvalid},
		{NewSite{Slug: "Blog", OwnerID: alice, Plan: "pro"}, ErrInvalid},
		{NewSite{Slug: "blog", OwnerID: alice, Plan: "gold"}, ErrInvalid},
		{NewSite{Slug: "blog", OwnerID: 999, Plan: "pro"}, store.ErrNotFound},
	} {
		if _, err := f.n.CreateSite(f.ctx, tc.ns); !errors.Is(err, tc.want) {
			t.Errorf("CreateSite(%+v) = %v, want %v", tc.ns, err, tc.want)
		}
	}
	if s := f.schemas(); len(s) != 0 {
		t.Fatalf("refused sites left schemas %v", s)
	}
	if d := f.dirs(); len(d) != 0 {
		t.Fatalf("refused sites left directories %v", d)
	}
}

func TestSiteStatus(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	s := f.site("blog", alice, "pro")
	p := f.site("diary", alice, "personal")
	status := func(id int64) *Site {
		t.Helper()
		s, err := f.n.Site(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if err := f.n.SuspendSite(f.ctx, s.ID, strings.Repeat("x", 501)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long reason: %v", err)
	}
	if err := f.n.SuspendSite(f.ctx, s.ID, "spam"); err != nil {
		t.Fatal(err)
	}
	if got := status(s.ID); got.Status != StatusSuspended || got.SuspendedReason != "spam" || got.SuspendedAt == nil {
		t.Fatalf("suspended: %+v", got)
	}
	if err := f.n.SuspendSite(f.ctx, s.ID, "again"); !errors.Is(err, ErrState) {
		t.Fatalf("suspending twice: %v", err)
	}
	if err := f.n.ResumeSite(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := status(s.ID); got.Status != StatusActive || got.SuspendedReason != "" || got.SuspendedAt != nil {
		t.Fatalf("resumed: %+v", got)
	}
	if err := f.n.ResumeSite(f.ctx, s.ID); !errors.Is(err, ErrState) {
		t.Fatalf("resuming an active site: %v", err)
	}

	// An unapproved Personal site goes back to pending, not active.
	if err := f.n.SuspendSite(f.ctx, p.ID, "check"); err != nil {
		t.Fatal(err)
	}
	if err := f.n.ResumeSite(f.ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got := status(p.ID); got.Status != StatusPending {
		t.Fatalf("resumed personal site: %s", got.Status)
	}

	// The portal is not a tenant: it cannot be suspended or deleted.
	net, err := f.n.InitNetworkSite(f.ctx, "sap13ns.site")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{f.n.SuspendSite(f.ctx, net.ID, "x"), f.n.DeleteSite(f.ctx, net.ID), f.n.SuspendSite(f.ctx, 999, "x")} {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("not a tenant: err = %v", err)
		}
	}

	if err := f.n.PurgeSite(f.ctx, s.ID); !errors.Is(err, ErrState) {
		t.Fatalf("purging a live site: %v", err)
	}
	if err := f.n.DeleteSite(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if got := status(s.ID); got.Status != StatusDeleted || got.DeletedAt == nil {
		t.Fatalf("deleted: %+v", got)
	}
	if err := f.n.ResumeSite(f.ctx, s.ID); !errors.Is(err, ErrState) {
		t.Fatalf("resuming a deleted site: %v", err)
	}
	if _, total, _ := f.n.ListSites(f.ctx, SiteQuery{}); total != 2 { // diary and the portal
		t.Fatalf("ListSites shows %d sites, want the deleted one left out", total)
	}
	if _, total, _ := f.n.ListSites(f.ctx, SiteQuery{Status: StatusDeleted}); total != 1 {
		t.Fatalf("deleted sites: %d", total)
	}

	if err := f.n.PurgeSite(f.ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.Site(f.ctx, s.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged site still there: %v", err)
	}
	if _, err := f.n.Domain(f.ctx, "blog.sap13ns.site"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged site's domain still there: %v", err)
	}
	if got := f.schemas(); fmt.Sprint(got) != fmt.Sprint([]string{p.Schema}) {
		t.Fatalf("schemas after purge: %v", got)
	}
	if _, err := os.Stat(f.n.Dir(s.ID)); !os.IsNotExist(err) {
		t.Fatalf("purged site directory: %v", err)
	}
	// The name is free again.
	f.site("blog", alice, "pro")
}

func TestInitNetworkSite(t *testing.T) {
	f := setup(t)
	net, err := f.n.InitNetworkSite(f.ctx, "SAP13NS.site.")
	if err != nil {
		t.Fatal(err)
	}
	if net.Kind != KindNetwork || net.Schema != Schema || net.Status != StatusActive || net.PrimaryHost != "sap13ns.site" || net.OwnerID != 0 {
		t.Fatalf("network site: %+v", net)
	}
	again, err := f.n.InitNetworkSite(f.ctx, "sap13ns.site")
	if err != nil || again.ID != net.ID {
		t.Fatalf("second InitNetworkSite = %+v, %v", again, err)
	}
	if _, err := f.n.SiteSecret(net); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.AddDomain(f.ctx, net.ID, "example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("custom domain on the portal: %v", err)
	}
	// A second network row is impossible.
	_, err = f.n.db.Exec(f.ctx, `INSERT INTO sites (kind, slug, schema_name, status, secret, rp_id)
		VALUES ('network', 'other', 'network', 'active', '\x00', 'x')`)
	if !errors.Is(mapErr(err), store.ErrConflict) {
		t.Fatalf("second network site: %v", err)
	}
}

func TestDomains(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	a, b := f.site("blog", alice, "pro"), f.site("shop", alice, "pro")

	d, err := f.n.AddDomain(f.ctx, a.ID, "WWW.Example.org.")
	if err != nil {
		t.Fatal(err)
	}
	if d.Host != "www.example.org" || d.Kind != DomainCustom || d.Primary || d.VerifiedAt != nil || len(d.VerifyToken) < 20 {
		t.Fatalf("custom domain: %+v", d)
	}
	idn, err := f.n.AddDomain(f.ctx, a.ID, "Bücher.example")
	if err != nil || idn.Host != "xn--bcher-kva.example" {
		t.Fatalf("IDN domain: %+v, %v", idn, err)
	}

	// Hosts are unique across the network, and the network's own are refused.
	if _, err := f.n.AddDomain(f.ctx, b.ID, "www.example.org"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("host of another site: %v", err)
	}
	for _, bad := range []string{"blog.sap13ns.site", "new.sap13ns.site", "sap13ns.site", "site", "x.y.sap13ns.site",
		"example.org:443", "192.0.2.1", "localhost", "exa mple.org", ""} {
		if _, err := f.n.AddDomain(f.ctx, b.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddDomain(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := f.n.AddDomain(f.ctx, 999, "example.net"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing site: %v", err)
	}

	if err := f.n.SetPrimaryDomain(f.ctx, "www.example.org"); !errors.Is(err, ErrState) {
		t.Fatalf("unverified primary: %v", err)
	}
	if err := f.n.VerifyDomain(f.ctx, "www.example.org"); err != nil {
		t.Fatal(err)
	}
	if err := f.n.SetPrimaryDomain(f.ctx, "www.example.org"); err != nil {
		t.Fatal(err)
	}
	s, _ := f.n.Site(f.ctx, a.ID)
	if s.PrimaryHost != "www.example.org" || s.RPID != "blog.sap13ns.site" {
		t.Fatalf("after switching primary: host %s, rp_id %s", s.PrimaryHost, s.RPID)
	}
	ds, _ := f.n.Domains(f.ctx, a.ID)
	if len(ds) != 3 || ds[0].Host != "www.example.org" || !ds[0].Primary || ds[1].Primary || ds[2].Primary {
		t.Fatalf("Domains = %+v", ds)
	}

	for _, host := range []string{"www.example.org", "blog.sap13ns.site"} {
		if err := f.n.RemoveDomain(f.ctx, host); !errors.Is(err, ErrState) {
			t.Errorf("RemoveDomain(%s) = %v, want ErrState", host, err)
		}
	}
	if err := f.n.SetPrimaryDomain(f.ctx, "blog.sap13ns.site"); err != nil {
		t.Fatal(err)
	}
	if err := f.n.RemoveDomain(f.ctx, "www.example.org"); err != nil {
		t.Fatal(err)
	}
	if err := f.n.RemoveDomain(f.ctx, "www.example.org"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	// Removed, the host can go to another site.
	if _, err := f.n.AddDomain(f.ctx, b.ID, "www.example.org"); err != nil {
		t.Fatal(err)
	}
}

func TestMembers(t *testing.T) {
	f := setup(t)
	alice, bob, carol := f.account("alice"), f.account("bob"), f.account("carol")
	s := f.site("blog", alice, "pro")

	if err := f.n.SetMember(f.ctx, s.ID, bob, "editor", alice); err != nil {
		t.Fatal(err)
	}
	if err := f.n.SetMember(f.ctx, s.ID, carol, "author", 0); err != nil {
		t.Fatal(err)
	}
	ms, err := f.n.Members(f.ctx, s.ID)
	if err != nil || len(ms) != 3 || ms[0].Username != "alice" || ms[1].Username != "bob" || ms[2].Role != "author" ||
		ms[1].InvitedBy == nil || *ms[1].InvitedBy != alice || ms[2].InvitedBy != nil {
		t.Fatalf("Members = %+v, %v", ms, err)
	}
	if err := f.n.SetMember(f.ctx, s.ID, bob, "subscriber", 0); err != nil {
		t.Fatal(err)
	}
	if m, _ := f.n.Member(f.ctx, s.ID, bob); m.Role != "subscriber" {
		t.Fatalf("role change: %+v", m)
	}
	for _, tc := range []struct {
		err  error
		want error
	}{
		{f.n.SetMember(f.ctx, s.ID, bob, "owner", 0), ErrInvalid},
		{f.n.SetMember(f.ctx, s.ID, alice, "editor", 0), ErrState},
		{f.n.SetMember(f.ctx, s.ID, 999, "editor", 0), store.ErrNotFound},
		{f.n.SetMember(f.ctx, 999, bob, "editor", 0), store.ErrNotFound},
		{f.n.RemoveMember(f.ctx, s.ID, alice), ErrState},
	} {
		if !errors.Is(tc.err, tc.want) {
			t.Errorf("err = %v, want %v", tc.err, tc.want)
		}
	}
	if err := f.n.RemoveMember(f.ctx, s.ID, bob); err != nil {
		t.Fatal(err)
	}
	if err := f.n.RemoveMember(f.ctx, s.ID, bob); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	if _, err := f.n.Member(f.ctx, s.ID, bob); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed member: %v", err)
	}

	// Network admins: never fewer than one.
	if err := f.n.AddNetworkAdmin(f.ctx, alice, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.n.AddNetworkAdmin(f.ctx, alice, 0); err != nil {
		t.Fatalf("adding twice: %v", err)
	}
	if err := f.n.AddNetworkAdmin(f.ctx, bob, alice); err != nil {
		t.Fatal(err)
	}
	admins, err := f.n.NetworkAdmins(f.ctx)
	if err != nil || len(admins) != 2 || admins[1].GrantedBy == nil || *admins[1].GrantedBy != alice {
		t.Fatalf("NetworkAdmins = %+v, %v", admins, err)
	}
	if ok, _ := f.n.IsNetworkAdmin(f.ctx, carol); ok {
		t.Fatal("carol is not an admin")
	}
	if err := f.n.RemoveNetworkAdmin(f.ctx, carol); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing a non-admin: %v", err)
	}
	if err := f.n.RemoveNetworkAdmin(f.ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := f.n.RemoveNetworkAdmin(f.ctx, bob); !errors.Is(err, ErrState) {
		t.Fatalf("removing the last admin: %v", err)
	}
	if ok, _ := f.n.IsNetworkAdmin(f.ctx, bob); !ok {
		t.Fatal("bob must still be an admin")
	}
}

func TestSecretWrap(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	a, b := f.site("blog", alice, "pro"), f.site("shop", alice, "pro")
	sa, err := f.n.SiteSecret(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := f.n.SiteSecret(b)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sa, sb) {
		t.Fatal("two sites share a secret")
	}
	if bytes.Contains(a.Secret, sa) {
		t.Fatal("the secret is stored in the clear")
	}
	sealed, err := f.n.wrapSecret(a.ID, sa)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.n.unwrapSecret(a.ID, sealed); err != nil || !bytes.Equal(again, sa) {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := f.n.wrapSecret(a.ID, sa[:16]); err == nil {
		t.Fatal("wrapped a short secret")
	}

	// Copying a's sealed secret into b's row does not give b a's secret.
	if _, err := f.n.db.Exec(f.ctx, `UPDATE sites SET secret = $2 WHERE id = $1`, b.ID, a.Secret); err != nil {
		t.Fatal(err)
	}
	swapped, err := f.n.Site(f.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.SiteSecret(swapped); err == nil {
		t.Fatal("a secret sealed for another site opened")
	}

	// Another master key opens nothing; neither does a tampered seal.
	other, err := New(f.pool, &config.Config{Multisite: true, BaseURL: f.cfg.BaseURL, SitesDomain: f.cfg.SitesDomain,
		SecretKey: bytes.Repeat([]byte{8}, 32), DataDir: f.cfg.DataDir}, testutil.Log())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.SiteSecret(a); err == nil {
		t.Fatal("opened with another master key")
	}
	tampered := bytes.Clone(a.Secret)
	tampered[len(tampered)-1] ^= 1
	if _, err := f.n.unwrapSecret(a.ID, tampered); err == nil {
		t.Fatal("opened a tampered seal")
	}
	// The sealing key is not the encryption key of auth.NewKeys(master),
	// which a site keeping the master as its own secret would use.
	forged, err := auth.NewKeys(f.cfg.SecretKey).Encrypt(sa, secretAD(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.unwrapSecret(a.ID, forged); err == nil {
		t.Fatal("a value sealed with the master's own keys opened")
	}
}

// Every failing provisioning step leaves no row, schema or directory, and
// the name can be used again.
func TestProvisioningFailure(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	boom := errors.New("boom")
	for _, step := range []string{"row", "schema", "migrate", "dir", "status"} {
		t.Run(step, func(t *testing.T) {
			l := listen(t)
			f.n.hook = func(s string) error {
				if s == step {
					return boom
				}
				return nil
			}
			defer func() { f.n.hook = nil }()
			if _, err := f.n.CreateSite(f.ctx, NewSite{Slug: "blog", OwnerID: alice, Plan: "pro"}); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want the injected failure", err)
			}
			f.assertEmpty()
			// The last notification announces the removal.
			var last string
			for {
				ctx, cancel := context.WithTimeout(f.ctx, 300*time.Millisecond)
				n, err := l.conn.WaitForNotification(ctx)
				cancel()
				if err != nil {
					break
				}
				last = n.Payload
			}
			if last == "" {
				t.Fatal("no notification for the cleanup")
			}
		})
	}

	// A context canceled halfway: the cleanup still runs.
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(f.ctx)
		f.n.hook = func(s string) error {
			if s == "schema" {
				cancel()
			}
			return nil
		}
		defer func() { f.n.hook = nil }()
		if _, err := f.n.CreateSite(ctx, NewSite{Slug: "blog", OwnerID: alice, Plan: "pro"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		f.assertEmpty()
	})

	// A directory that is already there is not the site's: provisioning
	// fails and leaves it alone.
	t.Run("existing directory", func(t *testing.T) {
		var last int64
		var called bool
		if err := f.n.db.QueryRow(f.ctx, `SELECT last_value, is_called FROM sites_id_seq`).Scan(&last, &called); err != nil {
			t.Fatal(err)
		}
		next := last
		if called {
			next++
		}
		dir := f.n.Dir(next)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, "keep")
		if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.n.CreateSite(f.ctx, NewSite{Slug: "blog", OwnerID: alice, Plan: "pro"}); !errors.Is(err, os.ErrExist) {
			t.Fatalf("err = %v, want os.ErrExist", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("the existing directory was touched: %v", err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		f.assertEmpty()
	})

	s := f.site("blog", alice, "pro")
	if s.Status != StatusActive {
		t.Fatalf("after the failures: %+v", s)
	}
}

func (f *fixture) assertEmpty() {
	f.t.Helper()
	if _, err := f.n.SiteBySlug(f.ctx, "blog"); !errors.Is(err, store.ErrNotFound) {
		f.t.Fatalf("site row left: %v", err)
	}
	if _, err := f.n.Domain(f.ctx, "blog.sap13ns.site"); !errors.Is(err, store.ErrNotFound) {
		f.t.Fatalf("domain left: %v", err)
	}
	if s := f.schemas(); len(s) != 0 {
		f.t.Fatalf("schemas left: %v", s)
	}
	if d := f.dirs(); len(d) != 0 {
		f.t.Fatalf("directories left: %v", d)
	}
}

// A mutation that fails sends nothing; the next one is the next message.
func TestNotifyOnCommitOnly(t *testing.T) {
	f := setup(t)
	alice := f.account("alice")
	s := f.site("blog", alice, "pro")
	l := listen(t)
	if err := f.n.ResumeSite(f.ctx, s.ID); !errors.Is(err, ErrState) {
		t.Fatal(err)
	}
	l.none()
	if err := f.n.SuspendSite(f.ctx, s.ID, "x"); err != nil {
		t.Fatal(err)
	}
	if got := l.next(); got != strconv.FormatInt(s.ID, 10) {
		t.Fatalf("payload %q", got)
	}
	if err := f.n.AddNetworkAdmin(f.ctx, alice, 0); err != nil {
		t.Fatal(err)
	}
	if got := l.next(); got != "" {
		t.Fatalf("network-wide payload %q", got)
	}
	if _, err := f.n.AddDomain(f.ctx, s.ID, "example.org"); err != nil {
		t.Fatal(err)
	}
	if got := l.next(); got != strconv.FormatInt(s.ID, 10) {
		t.Fatalf("domain payload %q", got)
	}
}

func TestCanonicalHost(t *testing.T) {
	for in, want := range map[string]string{
		"Example.ORG": "example.org", "example.org.": "example.org", "bücher.example": "xn--bcher-kva.example",
		"a-b.c": "a-b.c", "localhost": "localhost",
	} {
		if got, err := CanonicalHost(in); err != nil || got != want {
			t.Errorf("CanonicalHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", ".", "example.org:80", "[::1]", "10.0.0.1", "a..b", "-a.b", "a_b.c", "exa mple.org", "a/b"} {
		if got, err := CanonicalHost(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CanonicalHost(%q) = %q, %v; want ErrInvalid", bad, got, err)
		}
	}
}
