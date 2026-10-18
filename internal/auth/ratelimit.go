package auth

import (
	"sync"
	"time"
)

// Limiter is a fixed-window counter keyed by an arbitrary string (IP, account,
// IP+route). It is in-memory: sap13ns is designed to run as a single process
// per site, behind nginx which can add its own limit_req zones.
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	count int
	reset time.Time
}

func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{limit: limit, window: window, buckets: map[string]*bucket{}, lastGC: time.Now()}
}

// Allow counts one hit and reports whether the key is still under the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.lastGC) > l.window {
		for k, b := range l.buckets {
			if now.After(b.reset) {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		b = &bucket{reset: now.Add(l.window)}
		l.buckets[key] = b
	}
	b.count++
	return b.count <= l.limit
}

// Blocked reports whether the key is over the limit without counting a hit.
func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	return ok && time.Now().Before(b.reset) && b.count >= l.limit
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.buckets, key)
	l.mu.Unlock()
}
