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
	CSP         string // the page's own Content-Security-Policy ("" for the usual one)
	// MaxAge, when set and shorter than the cache's TTL, expires this
	// entry sooner.
	MaxAge  time.Duration
	created time.Time
	used    atomic.Int64 // when it was last served, to evict what nobody asks for
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

	hits, misses atomic.Uint64 // lookups since start
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
	if !ok || e.gen != c.gen.Load() || time.Since(e.created) > c.ttl ||
		e.MaxAge > 0 && time.Since(e.created) > e.MaxAge {
		c.misses.Add(1)
		return nil, false
	}
	c.hits.Add(1)
	e.used.Store(time.Now().UnixNano())
	return e, true
}

// Lookups returns how many lookups found an entry, and how many did not,
// since the cache was created. Invalidate does not reset them.
func (c *Cache) Lookups() (hits, misses uint64) { return c.hits.Load(), c.misses.Load() }

// Set stores e under key, as of the current generation. A body rendered
// before an Invalidate must go through SetAt instead.
func (c *Cache) Set(key string, e *Entry) { c.SetAt(key, e, c.gen.Load()) }

// SetAt stores e under key when nothing invalidated the cache since gen,
// the Generation read before e was rendered: a page rendered from content
// that changed meanwhile would otherwise stay cached as current.
func (c *Cache) SetAt(key string, e *Entry, gen uint64) {
	if len(e.Body) > c.maxSize/8 {
		return // do not let one huge page evict everything
	}
	e.created = time.Now()
	e.gen = gen
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.gen.Load() {
		return // invalidated while it was rendered
	}
	if old, ok := c.entries[key]; ok {
		// Out before making room: counted again below, it would be taken
		// off the total twice and the size limit would stop holding.
		c.bytes -= len(old.Body)
		delete(c.entries, key)
	}
	e.used.Store(e.created.UnixNano())
	for (len(c.entries) >= c.max || c.bytes+len(e.Body) > c.maxSize) && len(c.entries) > 0 {
		// Evict the entry served longest ago. The cache is small, so a
		// scan is fine.
		var oldestKey string
		var oldest int64
		for k, v := range c.entries {
			if u := v.used.Load(); oldestKey == "" || u < oldest {
				oldestKey, oldest = k, u
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
