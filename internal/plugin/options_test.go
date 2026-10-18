package plugin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"

	"sap13ns/internal/testutil"
)

// TestSharedHosts runs two hosts as a network runs two sites: one shared
// compilation cache, one process-wide instance limit and the lower
// per-plugin limits of a multisite install.
func TestSharedHosts(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	dirA, dirB := t.TempDir(), t.TempDir()
	installPlugin(t, dirA, "./internal/plugin/testdata/callout", "callout")
	installPlugin(t, dirB, "./internal/plugin/testdata/callout", "callout")

	cache := wazero.NewCompilationCache()
	defer cache.Close(ctx)
	limit := NewInstanceLimit(8)
	open := func(dir string) *Host {
		h, err := NewHostWithOptions(ctx, HostOptions{Dir: dir, Store: st, Log: testutil.Log(), Links: testLinks{},
			Site:  func() SiteInfo { return SiteInfo{URL: "https://example.test", Title: "Test", Language: "en"} },
			Cache: cache, MemoryLimitPages: 512, MaxInstances: 2, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.Close(context.Background()) })
		if err := h.Load(ctx); err != nil {
			t.Fatal(err)
		}
		return h
	}
	a, b := open(dirA), open(dirB)
	if err := a.Approve(ctx, "callout", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	if err := b.Load(ctx); err != nil { // same store: b sees the approval
		t.Fatal(err)
	}
	if a.maxInstances != 2 || a.Get("callout").maxInst != 2 || cap(a.Get("callout").pool) != 2 {
		t.Fatalf("per-plugin limit: host %d, plugin %d", a.maxInstances, a.Get("callout").maxInst)
	}
	shortcode := func(h *Host) {
		t.Helper()
		html, err := h.Shortcode(ctx, "callout", map[string]string{"type": "info"}, "Hola", nil)
		if err != nil || !strings.Contains(html, "sc-callout") {
			t.Fatalf("shortcode: %q, %v", html, err)
		}
	}
	// A Go plugin runs within 32 MiB.
	shortcode(a)
	shortcode(b)
	if n := limit.InUse(); n != 2 {
		t.Fatalf("instances alive: %d, want one pooled per host", n)
	}

	// Closing one host gives its slots back and leaves the shared
	// compiled module to the other.
	a.Close(ctx)
	if n := limit.InUse(); n != 1 {
		t.Fatalf("instances alive after closing a: %d", n)
	}
	shortcode(b)
	b.Close(ctx)
	if n := limit.InUse(); n != 0 {
		t.Fatalf("instances alive after closing both: %d", n)
	}
}

func TestInstanceLimit(t *testing.T) {
	l := NewInstanceLimit(4)
	ctx := context.Background()
	for range 3 {
		if err := l.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if l.InUse() != 3 || l.tight() {
		t.Fatalf("3 of 4: in use %d, tight %v", l.InUse(), l.tight())
	}
	if err := l.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	if !l.tight() {
		t.Fatal("4 of 4 is not tight")
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := l.acquire(short); err == nil {
		t.Fatal("acquired a fifth slot")
	}
	got := make(chan error)
	go func() { got <- l.acquire(ctx) }()
	for l.waiting.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	l.release()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
	for range 4 {
		l.release()
	}
	if l.InUse() != 0 {
		t.Fatalf("in use after releasing all: %d", l.InUse())
	}
	var none *InstanceLimit // no limit
	if none.acquire(ctx) != nil || none.tight() || none.InUse() != 0 {
		t.Fatal("nil limit")
	}
	none.release()
}

// TestTightLimitClosesIdle checks that with every slot taken, a released
// instance is closed instead of pooled, so another host can start one.
func TestTightLimitClosesIdle(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	dir := t.TempDir()
	installPlugin(t, dir, "./internal/plugin/testdata/callout", "callout")
	limit := NewInstanceLimit(1)
	h, err := NewHostWithOptions(ctx, HostOptions{Dir: dir, Store: st, Log: testutil.Log(), Links: testLinks{},
		Site:  func() SiteInfo { return SiteInfo{URL: "https://example.test", Title: "Test", Language: "en"} },
		Limit: limit})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close(ctx)
	if err := h.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.Approve(ctx, "callout", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := h.Shortcode(ctx, "callout", nil, "x", nil); err != nil {
			t.Fatal(err)
		}
		if n := limit.InUse(); n != 0 {
			t.Fatalf("an idle instance kept the only slot: %d", n)
		}
	}
}
