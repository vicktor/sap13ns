package update

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
)

// Checker periodically checks for a new release and caches the answer in
// memory for the admin UI. Create it with NewChecker, adjust the fields if
// needed, then start Run in a goroutine. Fields must not change after Run
// starts. Latest and LastError are safe for concurrent use.
type Checker struct {
	Repo     string        // GitHub "owner/name"; DefaultRepo if empty
	Current  string        // running version, e.g. web.Version
	Client   *http.Client  // nil means NewHTTPClient()
	Log      *slog.Logger  // nil discards
	Interval time.Duration // between checks; 24h if <= 0
	Retry    time.Duration // after a failed check; 1h if <= 0 (capped at Interval)
	Jitter   time.Duration // max random delay before the first check; none if <= 0
	Disabled bool          // Run returns at once and nothing is ever fetched

	mu      sync.RWMutex
	rel     *Release
	newer   bool
	checked time.Time
	err     error
}

// NewChecker returns a Checker for DefaultRepo with the default schedule: a
// first check 0-10 minutes after Run starts, then every 24 hours (1 hour
// after a failure). It is disabled when SAP13NS_UPDATE_CHECK is false.
func NewChecker(current string, c *http.Client, log *slog.Logger) *Checker {
	return &Checker{
		Repo:     DefaultRepo,
		Current:  current,
		Client:   c,
		Log:      log,
		Interval: 24 * time.Hour,
		Retry:    time.Hour,
		Jitter:   10 * time.Minute,
		Disabled: !EnabledFromEnv(),
	}
}

// Latest returns the cached result of the last successful check: the latest
// release (nil if none is known yet or none is published), whether it is
// newer than the running version, and when the check ran (zero if no check
// has succeeded yet). The release is a copy.
func (c *Checker) Latest() (*Release, bool, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rel.clone(), c.newer, c.checked
}

// LastError returns the error of the most recent check, or nil if it
// succeeded (or none has run).
func (c *Checker) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *Checker) logger() *slog.Logger {
	if c.Log == nil {
		return discardLogger()
	}
	return c.Log
}

// Run checks for releases until ctx is done. It never panics and returns
// immediately when the Checker is disabled.
func (c *Checker) Run(ctx context.Context) {
	log := c.logger()
	if c.Disabled {
		log.Info("update checks disabled", "env", EnvCheck)
		return
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	retry := c.Retry
	if retry <= 0 {
		retry = time.Hour
	}
	retry = min(retry, interval)
	var wait time.Duration
	if c.Jitter > 0 {
		wait = rand.N(c.Jitter)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		next := interval
		if err := c.CheckNow(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			next = retry
		}
		t.Reset(next)
	}
}

// CheckNow runs one check right away and updates the cache. ErrNoRelease is
// not an error here: it clears the cached release and returns nil. It never
// panics.
func (c *Checker) CheckNow(ctx context.Context) (err error) {
	log := c.logger()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("update: check panicked: %v", r)
			log.Error("update check failed", "err", err)
			c.mu.Lock()
			c.err = err
			c.mu.Unlock()
		}
	}()
	repo := c.Repo
	if repo == "" {
		repo = DefaultRepo
	}
	cctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	rel, newer, err := Check(cctx, c.Client, repo, c.Current)
	if errors.Is(err, ErrNoRelease) {
		rel, newer, err = nil, false, nil
	}
	now := time.Now()
	c.mu.Lock()
	c.err = err
	if err == nil {
		c.rel, c.newer, c.checked = rel, newer, now
	}
	c.mu.Unlock()
	switch {
	case err != nil:
		if ctx.Err() == nil {
			log.Warn("update check failed", "repo", repo, "err", err)
		}
	case rel == nil:
		log.Debug("update check: no published release", "repo", repo)
	case newer:
		log.Info("update available", "current", c.Current, "latest", rel.Version, "url", rel.URL)
	default:
		log.Debug("update check: up to date", "current", c.Current, "latest", rel.Version)
	}
	return err
}
