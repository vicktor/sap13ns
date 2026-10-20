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
