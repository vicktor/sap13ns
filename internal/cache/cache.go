// Package cache is a small in-memory cache for rendered public pages. Every
// content change bumps a generation counter, which invalidates all entries at
// once; entries also expire after a TTL so scheduled posts appear on time.
package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

type Entry struct {
	Body        []byte
	ContentType string
	ETag        string
	Status      int
	// MaxAge, when set and shorter than the cache's TTL, expires this
	// entry sooner.
	MaxAge  time.Duration
	created time.Time
	gen     uint64
}

type Cache struct {
	mu      sync.RWMutex
	entries map[string]*Entry
	max     int
	ttl     time.Duration
	gen     atomic.Uint64
	bytes   int
	maxSize int
}

func New(maxEntries, maxBytes int, ttl time.Duration) *Cache {
	return &Cache{entries: map[string]*Entry{}, max: maxEntries, ttl: ttl, maxSize: maxBytes}
}

// Limits returns the maximum number of entries and of body bytes.
func (c *Cache) Limits() (entries, bytes int) { return c.max, c.maxSize }

// Generation identifies the current content version (useful as an ETag salt).
func (c *Cache) Generation() uint64 { return c.gen.Load() }

// Invalidate drops every cached page.
func (c *Cache) Invalidate() {
	c.gen.Add(1)
	c.mu.Lock()
	c.entries = map[string]*Entry{}
	c.bytes = 0
	c.mu.Unlock()
}

func (c *Cache) Get(key string) (*Entry, bool) {
	c.mu.RLock()
	e, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || e.gen != c.gen.Load() || time.Since(e.created) > c.ttl {
		return nil, false
	}
	if e.MaxAge > 0 && time.Since(e.created) > e.MaxAge {
		return nil, false
	}
	return e, true
}

func (c *Cache) Set(key string, e *Entry) {
	if len(e.Body) > c.maxSize/8 {
		return // do not let one huge page evict everything
	}
	e.created = time.Now()
	e.gen = c.gen.Load()
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[key]; ok {
		c.bytes -= len(old.Body)
	}
	for (len(c.entries) >= c.max || c.bytes+len(e.Body) > c.maxSize) && len(c.entries) > 0 {
		// Evict the oldest entry. The cache is small, so a scan is fine.
		var oldestKey string
		var oldest time.Time
		for k, v := range c.entries {
			if oldestKey == "" || v.created.Before(oldest) {
				oldestKey, oldest = k, v.created
			}
		}
		c.bytes -= len(c.entries[oldestKey].Body)
		delete(c.entries, oldestKey)
	}
	c.entries[key] = e
	c.bytes += len(e.Body)
}

// Stats returns the number of entries and bytes held.
func (c *Cache) Stats() (int, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries), c.bytes
}
