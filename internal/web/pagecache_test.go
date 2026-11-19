package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A link from a campaign is the same page as the plain address: it is
// served from the cache, and what it carries does not reach the page.
func TestCacheIgnoresTrackingParams(t *testing.T) {
	e := newCustomEnv(t)
	if rec := e.get("/?utm_source=boletin&fbclid=abc", nil); rec.Code != http.StatusOK || rec.Header().Get("X-Cache") != "MISS" ||
		strings.Contains(rec.Body.String(), "boletin") {
		t.Fatalf("first visit: %d %s", rec.Code, rec.Header().Get("X-Cache"))
	}
	for _, path := range []string{"/", "/?utm_source=otro", "/?gclid=1&utm_medium=cpc", "/?ref=x"} {
		if rec := e.get(path, nil); rec.Header().Get("X-Cache") != "HIT" {
			t.Errorf("%s: %s", path, rec.Header().Get("X-Cache"))
		}
	}
	// A parameter the site does not know still gets a page of its own.
	if rec := e.get("/?utm_source=x&other=1", nil); rec.Header().Get("X-Cache") != "MISS" {
		t.Errorf("unknown parameter: %s", rec.Header().Get("X-Cache"))
	}
	if rec := e.get("/?other=1", nil); rec.Header().Get("X-Cache") != "MISS" {
		t.Errorf("unknown parameter, again: %s", rec.Header().Get("X-Cache"))
	}
}

// Addresses the site does not have are kept apart from its pages, and a
// vulnerability scan gets a bare answer that is not kept at all.
func TestCacheKeepsStraysApart(t *testing.T) {
	e := newCustomEnv(t)
	e.get("/", nil)
	pages, _ := e.s.pages.Stats()
	for _, path := range []string{"/no-such-page", "/blog/no-such-post"} {
		if rec := e.get(path, nil); rec.Code != http.StatusNotFound || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := e.get("/no-such-page", nil); rec.Header().Get("X-Cache") != "HIT" {
		t.Errorf("a page not found is rendered again: %s", rec.Header().Get("X-Cache"))
	}
	if n, _ := e.s.pages.Stats(); n != pages {
		t.Errorf("pages not found took room among the pages: %d, were %d", n, pages)
	}
	strays, _ := e.s.strays.Stats()
	if strays == 0 {
		t.Error("pages not found are not kept")
	}
	for _, path := range []string{"/wp-login.php", "/.env", "/xmlrpc.php", "/blog/.git/config"} {
		rec := e.get(path, nil)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if n, _ := e.s.strays.Stats(); n != strays {
		t.Errorf("a scan is kept: %d entries, were %d", n, strays)
	}
	e.s.contentChanged()
	if n, _ := e.s.strays.Stats(); n != 0 {
		t.Errorf("a change keeps %d pages not found", n)
	}
}

// Visitors who ask for a page while it is rendered wait for that render;
// a response made for one visitor is never given to another.
func TestCacheRendersOnce(t *testing.T) {
	e := newCustomEnv(t)
	run := func(h http.HandlerFunc, n int) []*httptest.ResponseRecorder {
		out := make([]*httptest.ResponseRecorder, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out[i] = httptest.NewRecorder()
				h(out[i], httptest.NewRequest("GET", "http://example.test/once-"+h2s(n), nil))
			}()
		}
		wg.Wait()
		return out
	}
	var renders atomic.Int32
	shared := e.s.cached(func(w http.ResponseWriter, r *http.Request) {
		renders.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("page"))
	})
	for _, rec := range run(shared, 8) {
		if rec.Body.String() != "page" || rec.Code != http.StatusOK {
			t.Fatalf("answer: %d %q", rec.Code, rec.Body.String())
		}
	}
	if n := renders.Load(); n != 1 {
		t.Errorf("rendered %d times", n)
	}

	var private atomic.Int32
	own := e.s.cached(func(w http.ResponseWriter, r *http.Request) {
		n := private.Add(1)
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte{'0' + byte(n)})
	})
	seen := map[string]bool{}
	for _, rec := range run(own, 5) {
		if seen[rec.Body.String()] {
			t.Errorf("two visitors got the same private answer %q", rec.Body.String())
		}
		seen[rec.Body.String()] = true
	}
}

func h2s(n int) string { return string(rune('a' + n)) }
