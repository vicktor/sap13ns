package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/ui"
)

type fakeHooks struct {
	mu    sync.Mutex
	wakes []time.Time
}

func (f *fakeHooks) Wakeup(_ context.Context, at time.Time) {
	f.mu.Lock()
	f.wakes = append(f.wakes, at)
	f.mu.Unlock()
}

// networkServer builds a server as a network builds one of its sites.
func networkServer(t *testing.T, st *store.Store, edit func(*config.Config, *Deps)) *Server {
	t.Helper()
	ctx := context.Background()
	base, _ := url.Parse("https://blog.sap13ns.site")
	cfg := &config.Config{BaseURL: base, SecretKey: []byte("0123456789abcdef0123456789abcdef"), DataDir: t.TempDir(),
		UploadsDir: t.TempDir(), PluginsDir: t.TempDir(), ThemesDir: t.TempDir(), MaxUploadBytes: 10 << 20,
		CookieSecure: true, HSTS: true}
	var s *Server
	host, err := plugin.NewHost(ctx, cfg.PluginsDir, "", st, testutil.Log(), func() plugin.SiteInfo { return s.SiteInfo() }, webLinks{&s})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close(context.Background()) })
	d := Deps{Config: cfg, Log: testutil.Log(), Store: st, UI: ui.FS, Plugins: host}
	if edit != nil {
		edit(cfg, &d)
	}
	if s, err = New(ctx, d); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestNetworkSiteOptions(t *testing.T) {
	st := testutil.Store(t)
	hooks := &fakeHooks{}

	// Single-site defaults: the installer token, HSTS with subdomains,
	// passkeys on the BaseURL host.
	s := networkServer(t, st, nil)
	if s.setupToken == "" {
		t.Fatal("a site without users has no installer token")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "https://blog.sap13ns.site/healthz", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=63072000; includeSubDomains" {
		t.Fatalf("HSTS = %q", got)
	}
	wa, err := s.webAuthn()
	if err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "blog.sap13ns.site" || len(wa.Config.RPOrigins) != 1 || wa.Config.RPOrigins[0] != "https://blog.sap13ns.site" {
		t.Fatalf("passkeys: %s %v", wa.Config.RPID, wa.Config.RPOrigins)
	}
	if n, b := s.pages.Limits(); n != 2000 || b != 64<<20 {
		t.Fatalf("default page cache: %d entries, %d bytes", n, b)
	}

	// A site of a network on a custom domain.
	s = networkServer(t, st, func(cfg *config.Config, d *Deps) {
		cfg.BaseURL, _ = url.Parse("https://www.example.org")
		cfg.HSTSNoSubdomains = true
		cfg.PasskeyRPID = "blog.sap13ns.site"
		cfg.PasskeyOrigins = []string{"https://www.example.org", "https://blog.sap13ns.site"}
		d.Network = hooks
		d.Hosts = []string{"blog.sap13ns.site"}
		d.CacheSizes = CacheSizes{Pages: 500, PagesBytes: 8 << 20, Content: 300, ContentBytes: 4 << 20}
	})
	if s.setupToken != "" {
		t.Fatal("a site of a network offers the web installer")
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "https://www.example.org/healthz", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "max-age=63072000" {
		t.Fatalf("HSTS on a custom domain = %q", got)
	}
	if wa, err = s.webAuthn(); err != nil {
		t.Fatal(err)
	}
	if wa.Config.RPID != "blog.sap13ns.site" || len(wa.Config.RPOrigins) != 2 {
		t.Fatalf("passkeys: %s %v", wa.Config.RPID, wa.Config.RPOrigins)
	}
	pn, pb := s.pages.Limits()
	cn, cb := s.content.Limits()
	if pn != 500 || pb != 8<<20 || cn != 300 || cb != 4<<20 {
		t.Fatalf("cache sizes: pages %d/%d, content %d/%d", pn, pb, cn, cb)
	}

	// Scheduling a post asks the network to wake the site at its date.
	ctx := context.Background()
	at := time.Now().Add(time.Hour).Truncate(time.Microsecond)
	author, err := st.CreateUser(ctx, store.NewUser{Username: "ada", Email: "ada@example.test", Role: "author", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Soon", Slug: "soon",
		Lang: s.mainLang(), AuthorID: author, CommentStatus: "open", PublishedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	s.afterPostSave(httptest.NewRequest("POST", "/admin/posts", nil), id, "", store.StatusPublished)
	hooks.mu.Lock()
	if len(hooks.wakes) != 1 || !hooks.wakes[0].Equal(at) {
		t.Fatalf("wake-ups %v, want %v", hooks.wakes, at)
	}
	hooks.mu.Unlock()
}

func TestBusy(t *testing.T) {
	s := &Server{}
	release := make(chan struct{})
	s.goJob(func() { <-release })
	if !s.Busy() {
		t.Fatal("not busy while a job runs")
	}
	close(release)
	for deadline := time.Now().Add(5 * time.Second); s.Busy(); {
		if time.Now().After(deadline) {
			t.Fatal("still busy after the job ended")
		}
		time.Sleep(time.Millisecond)
	}
}
