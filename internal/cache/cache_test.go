package cache

import (
	"testing"
	"time"
)

func TestMaxAge(t *testing.T) {
	c := New(10, 1<<20, time.Hour)
	c.Set("plain", &Entry{Body: []byte("a")})
	c.Set("fresh", &Entry{Body: []byte("b"), MaxAge: time.Minute})
	c.Set("stale", &Entry{Body: []byte("c"), MaxAge: time.Nanosecond})
	time.Sleep(time.Millisecond)
	for key, want := range map[string]bool{"plain": true, "fresh": true, "stale": false} {
		if _, ok := c.Get(key); ok != want {
			t.Errorf("%s: cached = %v, want %v", key, ok, want)
		}
	}

	// A max age longer than the cache's TTL does not extend it.
	c = New(10, 1<<20, time.Nanosecond)
	c.Set("long", &Entry{Body: []byte("d"), MaxAge: time.Hour})
	time.Sleep(time.Millisecond)
	if _, ok := c.Get("long"); ok {
		t.Error("an entry outlived the cache's TTL")
	}
}

func TestLookups(t *testing.T) {
	c := New(10, 1<<20, time.Minute)
	c.Get("/")
	c.Set("/", &Entry{Body: []byte("x")})
	c.Get("/")
	c.Get("/")
	c.Invalidate()
	c.Get("/")
	if h, m := c.Lookups(); h != 2 || m != 2 {
		t.Fatalf("lookups: %d hits, %d misses", h, m)
	}
}

// TestSetAtAfterInvalidate: a body rendered before an Invalidate is not
// kept, so a page rendered from old content cannot outlive the change.
func TestSetAtAfterInvalidate(t *testing.T) {
	c := New(10, 1<<20, time.Hour)
	gen := c.Generation()
	c.Invalidate() // the content changes while the page renders
	c.SetAt("/p", &Entry{Body: []byte("old")}, gen)
	if _, ok := c.Get("/p"); ok {
		t.Fatal("a page rendered before the change was cached")
	}
	gen = c.Generation()
	c.SetAt("/p", &Entry{Body: []byte("new")}, gen)
	if e, ok := c.Get("/p"); !ok || string(e.Body) != "new" {
		t.Fatal("a page rendered after the change was not cached")
	}
}

// Replacing a page in a full cache keeps the count of bytes right, and
// makes no room at the expense of another page.
func TestReplaceKeepsBytes(t *testing.T) {
	c := New(4, 1<<20, time.Minute)
	body := make([]byte, 1000)
	for round := 0; round < 5; round++ {
		for _, k := range []string{"a", "b", "c", "d"} {
			c.Set(k, &Entry{Body: body})
		}
	}
	if n, bytes := c.Stats(); n != 4 || bytes != 4000 {
		t.Fatalf("%d entries, %d bytes", n, bytes)
	}
}

// When the cache is full, the page served longest ago goes, not the one
// rendered first.
func TestEvictsLeastUsed(t *testing.T) {
	c := New(2, 1<<20, time.Minute)
	c.Set("home", &Entry{Body: []byte("home")})
	time.Sleep(time.Millisecond)
	c.Set("probe-1", &Entry{Body: []byte("x")})
	time.Sleep(time.Millisecond)
	c.Get("home")
	c.Set("probe-2", &Entry{Body: []byte("x")})
	if _, ok := c.Get("home"); !ok {
		t.Error("the page people read was evicted")
	}
	if _, ok := c.Get("probe-1"); ok {
		t.Error("the page nobody asked for again was kept")
	}
}
