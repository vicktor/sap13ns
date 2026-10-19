package network

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/db"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/ui"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// tlog shows the registry's warnings and errors with the test's output.
type tlog struct{ t *testing.T }

func (l tlog) Write(p []byte) (int, error) {
	l.t.Log(strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

// netEnv is a running network: the portal, a registry and its router.
type netEnv struct {
	*fixture
	reg   *Registry
	clock *fakeClock
	owner int64
}

func newNetEnv(t *testing.T, edit func(*RegistryOptions)) *netEnv {
	t.Helper()
	f := setup(t)
	f.cfg.DatabaseURL = testutil.URL()
	f.cfg.MaxLoadedSites, f.cfg.SiteIdle, f.cfg.UnknownHost = 50, 15*time.Minute, "404"
	f.cfg.CookieSecure, f.cfg.HSTS, f.cfg.MaxUploadBytes = true, true, 10<<20
	if _, err := f.n.InitNetworkSite(f.ctx, "sap13ns.site"); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	o := RegistryOptions{Config: f.cfg, Network: f.n, Log: slog.New(slog.NewTextHandler(tlog{t}, &slog.HandlerOptions{Level: slog.LevelWarn})), UI: ui.FS, Now: clock.now}
	if edit != nil {
		edit(&o)
	}
	reg, err := NewRegistry(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reg.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := reg.Start(f.ctx); err != nil {
		t.Fatal(err)
	}
	return &netEnv{fixture: f, reg: reg, clock: clock, owner: f.account("owner")}
}

// tenant creates a site and makes the registry see it.
func (e *netEnv) tenant(slug, plan string) *Site {
	e.t.Helper()
	s := e.site(slug, e.owner, plan)
	e.refresh()
	return s
}

func (e *netEnv) refresh() {
	e.t.Helper()
	if err := e.reg.Refresh(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// siteStore is a store of the site's schema, independent of the registry.
func (e *netEnv) siteStore(s *Site) *store.Store {
	e.t.Helper()
	h, err := db.Scope(e.pool, s.Schema)
	if err != nil {
		e.t.Fatal(err)
	}
	return store.New(h)
}

func (e *netEnv) do(method, target string, mod ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "192.0.2.10:4000"
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	for _, m := range mod {
		m(req)
	}
	rec := httptest.NewRecorder()
	e.reg.ServeHTTP(rec, req)
	return rec
}

func (e *netEnv) entry(id int64) *entry {
	e.reg.mu.Lock()
	defer e.reg.mu.Unlock()
	return e.reg.entries[id]
}

// waitFor polls cond for a few seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func publish(t *testing.T, st *store.Store, title, slug string) {
	t.Helper()
	ctx := context.Background()
	author, err := st.CreateUser(ctx, store.NewUser{Username: "writer-" + slug, Email: slug + "@example.test", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: title, Slug: slug,
		Lang: "es", AuthorID: author, CommentStatus: "open", PublishedAt: &at}); err != nil {
		t.Fatal(err)
	}
}

// TestIsolation is guard test 7 of the multisite plan: two sites on one
// pool, as a browser sees them.
func TestIsolation(t *testing.T) {
	e := newNetEnv(t, nil)
	a, b := e.tenant("alpha", "pro"), e.tenant("beta", "pro")
	sa, sb := e.siteStore(a), e.siteStore(b)
	publish(t, sa, "Hello from alpha", "hello-alpha")

	if rec := e.do("GET", "https://alpha.sap13ns.site/blog/hello-alpha"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Hello from alpha") {
		t.Fatalf("post on its site: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "https://beta.sap13ns.site/blog/hello-alpha"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "Hello from alpha") {
		t.Fatalf("post of alpha on beta: %d", rec.Code)
	}

	// A session of alpha is anonymous on beta.
	ctx := context.Background()
	uid, err := sa.CreateUser(ctx, store.NewUser{Username: "boss", Email: "boss@example.test", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken(32)
	if err := sa.CreateSession(ctx, store.Session{TokenHash: auth.HashToken(token), UserID: uid, CSRFToken: auth.NewToken(24),
		ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		t.Fatal(err)
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__Host-sap13ns", Value: token}) }
	if rec := e.do("GET", "https://alpha.sap13ns.site/admin/", cookie); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/profile/2fa" {
		t.Fatalf("alpha admin with its cookie: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("GET", "https://beta.sap13ns.site/admin/", cookie); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login?") {
		t.Fatalf("beta admin with alpha's cookie: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Plugin key-value data stays with its site, also through the stores
	// the loaded sites use.
	for _, st := range []*store.Store{sa, sb} {
		if err := st.RecordPluginInstall(ctx, store.PluginInstall{ID: "kv", Version: "1.0.0", Source: "cli", Report: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := e.entry(a.ID).app.Store().PluginKVSet(ctx, "kv", "k", []byte("alpha"), 1<<20, 10); err != nil || !ok {
		t.Fatalf("kv set: %v %v", ok, err)
	}
	if v, err := e.entry(b.ID).app.Store().PluginKVGet(ctx, "kv", "k"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("beta reads alpha's key: %q %v", v, err)
	}
	if v, err := sa.PluginKVGet(ctx, "kv", "k"); err != nil || string(v) != "alpha" {
		t.Fatalf("alpha's key: %q %v", v, err)
	}
	for _, s := range []*Site{a, b} {
		if got := e.entry(s.ID).app.Store().DB.(*db.Scoped).Schema(); got != s.Schema {
			t.Fatalf("site %d runs on schema %s", s.ID, got)
		}
	}
	if got := e.reg.Loaded(); len(got) != 3 {
		t.Fatalf("loaded %v, want the portal, alpha and beta", got)
	}
}

func TestRouting(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	publish(t, e.siteStore(a), "Routed", "routed")

	// Host names are canonicalised.
	for _, h := range []string{"ALPHA.sap13ns.site", "alpha.sap13ns.site.", "alpha.sap13ns.site:8443"} {
		rec := e.do("GET", "https://alpha.sap13ns.site/blog/routed", func(r *http.Request) { r.Host = h })
		if rec.Code != http.StatusOK {
			t.Errorf("host %q: %d", h, rec.Code)
		}
	}
	// The portal answers on the base URL host.
	if rec := e.do("GET", "https://sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("portal: %d", rec.Code)
	}

	// Unknown hosts, IP addresses and sites being created are a 404 page.
	if _, err := e.n.db.Exec(e.ctx, `UPDATE sites SET status = 'provisioning' WHERE slug = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	for _, h := range []string{"nope.sap13ns.site", "192.0.2.1", "[2001:db8::1]:443", "alpha.sap13ns.site", "bad_host"} {
		rec := e.do("GET", "https://x/", func(r *http.Request) { r.Host = h; r.Header.Set("Accept-Language", "es-ES,es;q=0.9,en;q=0.8") })
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No hay ningún sitio en esta dirección") ||
			!strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") || rec.Header().Get("X-Robots-Tag") == "" {
			t.Errorf("unknown host %q: %d %v", h, rec.Code, rec.Header())
		}
	}
	if !slices.Equal(e.reg.Loaded(), []int64{e.portalID()}) {
		t.Fatalf("a site that is not served stays loaded: %v", e.reg.Loaded())
	}
	// The page's stylesheet comes from the router.
	if rec := e.do("GET", "https://nope.sap13ns.site/static/admin.css"); rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("stylesheet of the 404 page: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if _, err := e.n.db.Exec(e.ctx, `UPDATE sites SET status = 'active' WHERE slug = 'alpha'`); err != nil {
		t.Fatal(err)
	}
	e.refresh()

	// /healthz answers on any host without loading a site.
	if rec := e.do("GET", "http://x/healthz"); rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz: %d %q", rec.Code, rec.Body.String())
	}
}

func (e *netEnv) portalID() int64 {
	e.t.Helper()
	s, err := e.n.NetworkSite(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return s.ID
}

func TestUnknownHostRedirect(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) { o.Config.UnknownHost = "redirect" })
	if rec := e.do("GET", "https://nope.example/x"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://sap13ns.site/" {
		t.Fatalf("unknown host: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAliases(t *testing.T) {
	e := newNetEnv(t, nil)
	a := e.tenant("alpha", "pro")
	if _, err := e.n.AddDomain(e.ctx, a.ID, "www.alpha.example"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	// Not verified: not routed.
	if rec := e.do("GET", "https://www.alpha.example/"); rec.Code != http.StatusNotFound {
		t.Fatalf("unverified domain: %d", rec.Code)
	}
	if err := e.n.VerifyDomain(e.ctx, "www.alpha.example"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	rec := e.do("GET", "https://www.alpha.example/blog/x?y=1")
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://alpha.sap13ns.site/blog/x?y=1" {
		t.Fatalf("alias GET: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := e.do("POST", "https://www.alpha.example/login"); rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("alias POST: %d", rec.Code)
	}
	// ACME challenges are answered on every host.
	if rec := e.do("GET", "https://www.alpha.example/.well-known/acme-challenge/abc"); rec.Code != http.StatusNotFound {
		t.Fatalf("ACME challenge on an alias: %d", rec.Code)
	}

	// The site loaded with its default host...
	if rec := e.do("GET", "https://alpha.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("alpha: %d", rec.Code)
	}
	if got := e.entry(a.ID).app.Config().BaseURL.String(); got != "https://alpha.sap13ns.site" {
		t.Fatalf("base URL %s", got)
	}
	// ...moves to its custom domain: the old host redirects, and the site
	// reloads with the new base URL once idle.
	if err := e.n.SetPrimaryDomain(e.ctx, a.ID, "www.alpha.example"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if rec := e.do("GET", "https://alpha.sap13ns.site/a"); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "https://www.alpha.example/a" {
		t.Fatalf("old primary: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if got := e.reg.Sweep(e.ctx); !slices.Equal(got, []int64{a.ID}) {
		t.Fatalf("sweep after the move unloaded %v", got)
	}
	rec = e.do("GET", "https://www.alpha.example/")
	if rec.Code != http.StatusOK || rec.Header().Get("Strict-Transport-Security") != "max-age=63072000" {
		t.Fatalf("custom domain: %d %q", rec.Code, rec.Header().Get("Strict-Transport-Security"))
	}
	cfg := e.entry(a.ID).app.Config()
	if cfg.BaseURL.String() != "https://www.alpha.example" || !cfg.HSTSNoSubdomains || cfg.PasskeyRPID != "alpha.sap13ns.site" {
		t.Fatalf("reloaded config: %s nosub=%v rp=%s", cfg.BaseURL, cfg.HSTSNoSubdomains, cfg.PasskeyRPID)
	}
}

func TestStatuses(t *testing.T) {
	e := newNetEnv(t, nil)
	b := e.tenant("beta", "pro")
	if rec := e.do("GET", "https://beta.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("beta: %d", rec.Code)
	}

	// Suspending through another Network (the CLI) reaches the running
	// registry through NOTIFY: only the 503 page, and the site unloads.
	other, err := New(e.pool, e.cfg, testutil.Log())
	if err != nil {
		t.Fatal(err)
	}
	before := e.reg.refreshes.Load()
	if err := other.SuspendSite(e.ctx, b.ID, "abuse"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the notification", func() bool { return e.reg.refreshes.Load() > before && e.entry(b.ID) == nil })
	for _, p := range []string{"/", "/login", "/admin/", "/blog/x"} {
		rec := e.do("GET", "https://beta.sap13ns.site"+p)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Este sitio está suspendido") ||
			!strings.Contains(rec.Header().Get("X-Robots-Tag"), "noindex") {
			t.Fatalf("suspended %s: %d", p, rec.Code)
		}
	}
	if rec := e.do("GET", "https://beta.sap13ns.site/static/admin.css"); rec.Code != http.StatusOK {
		t.Fatalf("stylesheet of the suspended page: %d", rec.Code)
	}
	if e.entry(b.ID) != nil {
		t.Fatal("a suspended site was loaded")
	}
	before = e.reg.refreshes.Load()
	if err := other.ResumeSite(e.ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the resume", func() bool { return e.reg.refreshes.Load() > before })
	if rec := e.do("GET", "https://beta.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("resumed: %d", rec.Code)
	}

	// A site waiting for approval serves its sign-in and admin only.
	e.tenant("gamma", "personal")
	rec := e.do("GET", "https://gamma.sap13ns.site/", func(r *http.Request) { r.Header.Set("Accept-Language", "es") })
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Este sitio aún no está abierto") {
		t.Fatalf("pending home: %d", rec.Code)
	}
	if rec := e.do("GET", "https://gamma.sap13ns.site/login"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="password"`) {
		t.Fatalf("pending login: %d", rec.Code)
	}
	if rec := e.do("GET", "https://gamma.sap13ns.site/admin/"); rec.Code != http.StatusSeeOther {
		t.Fatalf("pending admin: %d", rec.Code)
	}
}

// TestSingleflight: concurrent first requests load the site once.
func TestSingleflight(t *testing.T) {
	// More requests than MaxInflight would get the busy page.
	e := newNetEnv(t, func(o *RegistryOptions) { o.MaxInflight = 64 })
	e.tenant("alpha", "pro")
	opened := e.reg.opened.Load()
	var wg sync.WaitGroup
	codes := make(chan int, 50)
	for range 50 {
		wg.Go(func() {
			req := httptest.NewRequest("GET", "https://alpha.sap13ns.site/", nil)
			rec := httptest.NewRecorder()
			e.reg.ServeHTTP(rec, req)
			codes <- rec.Code
		})
	}
	wg.Wait()
	close(codes)
	for c := range codes {
		if c != http.StatusOK {
			t.Fatalf("a concurrent request got %d", c)
		}
	}
	if n := e.reg.opened.Load() - opened; n != 1 {
		t.Fatalf("50 concurrent requests opened the site %d times", n)
	}
}

func TestEviction(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) { o.Config.MaxLoadedSites = 3 })
	portal := e.portalID()
	var ids []int64
	for _, slug := range []string{"one", "two", "three"} {
		s := e.tenant(slug, "pro")
		ids = append(ids, s.ID)
		if rec := e.do("GET", "https://"+slug+".sap13ns.site/"); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", slug, rec.Code)
		}
		e.clock.add(time.Minute)
	}
	// Four loaded (the portal counts), three allowed: the least recently
	// used tenant went when the third loaded.
	waitFor(t, "the LRU eviction", func() bool { return len(e.reg.Loaded()) == 3 })
	if want := []int64{portal, ids[1], ids[2]}; !slices.Equal(e.reg.Loaded(), want) {
		t.Fatalf("loaded %v, want %v", e.reg.Loaded(), want)
	}

	// Nothing is idle yet.
	if got := e.reg.Sweep(e.ctx); len(got) != 0 {
		t.Fatalf("early sweep unloaded %v", got)
	}
	// A request in flight protects its site from the idle sweep.
	e.clock.add(20 * time.Minute)
	busy, err := e.reg.acquire(e.ctx, e.reg.table.Load().sites[ids[1]], time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.reg.Sweep(e.ctx); !slices.Equal(got, []int64{ids[2]}) {
		t.Fatalf("idle sweep unloaded %v", got)
	}
	e.reg.release(busy)
	e.clock.add(16 * time.Minute)
	if got := e.reg.Sweep(e.ctx); !slices.Equal(got, []int64{ids[1]}) {
		t.Fatalf("sweep after the request unloaded %v", got)
	}
	// The portal is never unloaded.
	e.clock.add(24 * time.Hour)
	if got := e.reg.Sweep(e.ctx); len(got) != 0 || !slices.Equal(e.reg.Loaded(), []int64{portal}) {
		t.Fatalf("unloaded %v, loaded %v", got, e.reg.Loaded())
	}
	// An unloaded site loads again on its next request.
	if rec := e.do("GET", "https://one.sap13ns.site/"); rec.Code != http.StatusOK || e.entry(ids[0]) == nil {
		t.Fatalf("reload: %d", rec.Code)
	}
}

func TestMemoryPressure(t *testing.T) {
	var heap struct {
		sync.Mutex
		n uint64
	}
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Config.MemoryLimitMB = 1000
		o.Heap = func() uint64 { heap.Lock(); defer heap.Unlock(); return heap.n }
	})
	for _, slug := range []string{"one", "two", "three"} {
		e.tenant(slug, "pro")
		if rec := e.do("GET", "https://"+slug+".sap13ns.site/"); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", slug, rec.Code)
		}
		e.clock.add(time.Minute)
	}
	// 4 sites at 900 MB: 225 MB each; getting to 700 MB takes one.
	heap.Lock()
	heap.n = 900 << 20
	heap.Unlock()
	if got := e.reg.Sweep(e.ctx); len(got) != 1 {
		t.Fatalf("memory sweep unloaded %v", got)
	}
	heap.Lock()
	heap.n = 800 << 20
	heap.Unlock()
	if got := e.reg.Sweep(e.ctx); len(got) != 0 {
		t.Fatalf("sweep below 85%% unloaded %v", got)
	}
}

func TestInflightCapAndLoadWait(t *testing.T) {
	e := newNetEnv(t, func(o *RegistryOptions) { o.MaxInflight = 1 })
	a := e.tenant("alpha", "pro")
	if rec := e.do("GET", "https://alpha.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("alpha: %d", rec.Code)
	}
	held, err := e.reg.acquire(e.ctx, e.reg.table.Load().sites[a.ID], time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "https://alpha.sap13ns.site/")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), "Este sitio está ocupado") {
		t.Fatalf("over the cap: %d", rec.Code)
	}
	e.reg.release(held)
	if rec := e.do("GET", "https://alpha.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("after release: %d", rec.Code)
	}

	// A request does not wait for a slow load longer than LoadWait; the
	// load goes on and the next request is served.
	e.reg.o.LoadWait = time.Nanosecond
	b := e.tenant("beta", "pro")
	if rec := e.do("GET", "https://beta.sap13ns.site/"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("while loading: %d", rec.Code)
	}
	waitFor(t, "beta to load", func() bool { return slices.Contains(e.reg.Loaded(), b.ID) })
	e.reg.o.LoadWait = time.Second
	if rec := e.do("GET", "https://beta.sap13ns.site/"); rec.Code != http.StatusOK {
		t.Fatalf("after the load: %d", rec.Code)
	}
}

// TestUnloadRace runs requests while the site is unloaded again and again:
// every request is served, and the site never runs twice.
func TestUnloadRace(t *testing.T) {
	e := newNetEnv(t, nil)
	e.tenant("alpha", "pro")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	// At most the portal and alpha are open at any moment (opened is read
	// first, so a check never sees an unload before its open).
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if live := e.reg.opened.Load() - e.reg.unloaded.Load(); live > 2 {
				t.Errorf("%d sites open at once", live)
				return
			}
			time.Sleep(100 * time.Microsecond)
		}
	})
	for i := range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(i) * time.Millisecond):
				}
				req := httptest.NewRequest("GET", "https://alpha.sap13ns.site/no-such-page", nil)
				rec := httptest.NewRecorder()
				e.reg.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound {
					t.Errorf("request: %d", rec.Code)
				}
				io.Copy(io.Discard, rec.Body)
			}
		})
	}
	for deadline := time.Now().Add(10 * time.Second); e.reg.unloaded.Load() < 5 && time.Now().Before(deadline); {
		e.clock.add(time.Hour)
		e.reg.Sweep(e.ctx)
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if n := e.reg.unloaded.Load(); n < 5 {
		t.Fatalf("the site was unloaded %d times only", n)
	}
}
