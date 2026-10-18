package siteapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/internal/web"
	"sap13ns/ui"
)

func params(t *testing.T, st *store.Store) Params {
	t.Helper()
	base, _ := url.Parse("http://example.test")
	dir := t.TempDir()
	cfg := &config.Config{BaseURL: base, SecretKey: []byte("0123456789abcdef0123456789abcdef"), DataDir: dir,
		UploadsDir: dir + "/uploads", PluginsDir: dir + "/plugins", ThemesDir: dir + "/themes", MaxUploadBytes: 10 << 20}
	return Params{Config: cfg, Store: st, Log: testutil.Log(), UI: ui.FS,
		Background: web.BackgroundOptions{Cron: true, Maintenance: true}}
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "http://example.test"+path, nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// settle waits until at most want goroutines run: some (pool connections,
// timers) take a moment to exit.
func settle(want int) int {
	n := runtime.NumGoroutine()
	for deadline := time.Now().Add(5 * time.Second); n > want && time.Now().Before(deadline); n = runtime.NumGoroutine() {
		time.Sleep(20 * time.Millisecond)
	}
	return n
}

func TestOpenCloseLeavesNoGoroutines(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	p := params(t, st)
	// A first round warms up what lives for the process (the pool's
	// connections, wazero's and net/http's package state).
	a, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(a, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before := settle(0)

	for range 3 {
		a, err := Open(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if rec := get(a, "/"); rec.Code != http.StatusOK {
			t.Fatalf("home: %d", rec.Code)
		}
		if err := a.Close(ctx); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(ctx); err != nil { // a second Close is harmless
			t.Fatal(err)
		}
	}
	if after := settle(before); after > before {
		buf := make([]byte, 1<<20)
		t.Fatalf("%d goroutines before, %d after three Open/Close rounds:\n%s", before, after, buf[:runtime.Stack(buf, true)])
	}
}

func TestCloseOrder(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	a, err := Open(ctx, params(t, st))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var steps []string
	stepc := make(chan string, 8)
	a.step = func(s string) {
		mu.Lock()
		steps = append(steps, s)
		mu.Unlock()
		stepc <- s
	}
	entered, release := make(chan struct{}), make(chan struct{})
	inner := a.handler
	a.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(entered)
			<-release
		}
		inner.ServeHTTP(w, r)
	})

	slow := make(chan int)
	go func() { slow <- get(a, "/slow").Code }()
	<-entered
	if a.Active() != 1 || !a.Busy() {
		t.Fatalf("in flight: active %d, busy %v", a.Active(), a.Busy())
	}
	closed := make(chan error)
	go func() { closed <- a.Close(ctx) }()

	if s := <-stepc; s != "background" {
		t.Fatalf("first step %q", s)
	}
	// Closing: new requests are refused, the one in flight goes on.
	if rec := get(a, "/healthz"); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("request while closing: %d", rec.Code)
	}
	select {
	case s := <-stepc:
		t.Fatalf("step %q ran with a request in flight", s)
	case <-closed:
		t.Fatal("Close returned with a request in flight")
	case <-time.After(200 * time.Millisecond):
	}
	// The plugin host is still open for the request: it closes last.
	if a.host.Generation() == 0 {
		t.Fatal("plugins were never loaded")
	}
	close(release)
	if code := <-slow; code != http.StatusNotFound {
		t.Fatalf("slow request: %d", code)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"background", "requests", "server", "plugins"}; !slices.Equal(steps, want) {
		t.Fatalf("steps %v, want %v", steps, want)
	}
}

// TestCloseFlushes checks that what a request recorded in memory reaches
// the database on Close: here an analytics page view.
func TestCloseFlushes(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	a, err := Open(ctx, params(t, st))
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(a, "/"); rec.Code != http.StatusOK {
		t.Fatalf("home: %d", rec.Code)
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	var views int64
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(sum(views), 0) FROM analytics_days`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 1 {
		t.Fatalf("views after Close: %d", views)
	}
}

func TestPluginLimits(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	p := params(t, st)
	lim := plugin.NewInstanceLimit(3)
	p.Plugins = PluginLimits{MemoryLimitPages: 512, MaxInstances: 2, Limit: lim}
	a, err := Open(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	if a.Plugins() == nil || a.Server() == nil || a.Store() != st || a.Config() != p.Config {
		t.Fatal("accessors")
	}
	if lim.InUse() != 0 {
		t.Fatalf("instances in use without plugins: %d", lim.InUse())
	}
}

func TestOpenNeedsParams(t *testing.T) {
	if _, err := Open(context.Background(), Params{}); err == nil {
		t.Fatal("Open accepted empty params")
	}
}
