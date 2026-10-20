// Package analytics implements cookie-less, privacy-first page view
// statistics.
//
// The web layer calls Collector.Record for every public HTML page it serves.
// Views are aggregated in memory per day (in the site's time zone) and
// periodically upserted into analytics_days, analytics_pages and
// analytics_dims; nothing about an individual visit is ever stored.
//
// Unique visitors are counted with sha256(dailySalt || siteSalt || IP || UA).
// The daily salt is random, held only in memory and replaced at local
// midnight together with the set of today's hashes, so visits cannot be
// linked across days, and nothing that identifies a visitor (IP address, User-Agent or
// hash) is persisted or logged. The visitors figure of a period is therefore
// the sum of its daily unique visitors.
package analytics

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the subset of pgx used by this package; *pgxpool.Pool, *pgx.Conn and
// pgx.Tx satisfy it. Table names are unqualified, so the connection's
// search_path selects the site schema.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Options configures a Collector. The zero value is usable.
type Options struct {
	// Location sets the day boundaries (the site time zone). Default UTC.
	Location *time.Location
	// Log receives flush errors. Default slog.Default().
	Log *slog.Logger
	// FlushEvery is the interval between database writes. Default 1 minute.
	FlushEvery time.Duration
	// Now replaces time.Now in tests.
	Now func() time.Time
	// MaxPaths bounds the distinct paths kept per day; further paths are
	// counted as "(other)". Default 5000.
	MaxPaths int
	// MaxVisitors bounds the visitor hashes kept for today; beyond it the
	// unique count becomes a sampled estimate. Default 200000.
	MaxVisitors int
	// ClientIP returns the visitor address, resolving trusted proxies.
	// Default: the host part of r.RemoteAddr.
	ClientIP func(*http.Request) string
	// SiteSalt is mixed into visitor hashes so that installs (or sites)
	// never produce comparable hashes. Default: random per process.
	SiteSalt []byte
	// Hosts lists extra host names of this site (aliases); referrers from
	// them count as same-site navigation. The request Host always does.
	Hosts []string
	// Disabled starts the collector switched off (see SetEnabled).
	Disabled bool
	// Country returns the ISO 3166-1 alpha-2 code of the visitor's
	// country, or "" when it is unknown. Only the code is kept. Nil
	// counts no countries.
	Country func(*http.Request) string
}

const (
	defaultFlushEvery  = time.Minute
	defaultMaxPaths    = 5000
	defaultMaxVisitors = 200_000
	// maxPendingDays bounds how many days of unflushed data are kept while
	// the database is unreachable; older days are discarded.
	maxPendingDays = 7
	flushTimeout   = 30 * time.Second
)

// Dimensions stored in analytics_dims.dim.
const (
	dimReferrer = iota
	dimBrowser
	dimOS
	dimDevice
	dimLang
	dimCountry
	numDims
)

var dimNames = [numDims]string{"referrer", "browser", "os", "device", "lang", "country"}

// dimCaps bounds the distinct values per day of dimensions whose values come
// from the request; the others have a fixed vocabulary.
var dimCaps = [numDims]int{dimReferrer: 1000, dimLang: 100}

// Collector aggregates page views in memory and flushes them to the
// database. All methods are safe for concurrent use and on a nil Collector
// (which records nothing).
type Collector struct {
	db          DB
	log         *slog.Logger
	now         func() time.Time
	clientIP    func(*http.Request) string
	country     func(*http.Request) string
	flushEvery  time.Duration
	maxPaths    int
	maxVisitors int
	siteSalt    []byte
	internal    map[string]bool

	enabled   atomic.Bool
	closed    atomic.Bool
	done      chan struct{}
	closeOnce sync.Once

	flushMu sync.Mutex // serialises flushes

	mu       sync.Mutex
	loc      *time.Location
	today    dayNum
	salt     [32]byte
	visitors *visitorSet
	paths    map[string]struct{}          // paths admitted today
	admitted [numDims]map[string]struct{} // capped dimension values admitted today
	pending  map[dayNum]*bucket           // unflushed aggregates
}

// bucket holds the unflushed aggregates of one day.
type bucket struct {
	views    int64
	visitors int64 // unique visitors of the day so far (not a delta)
	pages    map[string]int64
	dims     [numDims]map[string]int64
}

func newBucket() *bucket {
	b := &bucket{pages: make(map[string]int64)}
	for i := range b.dims {
		b.dims[i] = make(map[string]int64)
	}
	return b
}

// merge adds o's counts to b.
func (b *bucket) merge(o *bucket) {
	b.views += o.views
	b.visitors = max(b.visitors, o.visitors)
	for k, v := range o.pages {
		b.pages[k] += v
	}
	for i := range o.dims {
		for k, v := range o.dims[i] {
			b.dims[i][k] += v
		}
	}
}

// dayNum is a calendar date as days since 1970-01-01.
type dayNum int32

func dayOf(t time.Time, loc *time.Location) dayNum {
	y, m, d := t.In(loc).Date()
	return dateNum(y, m, d)
}

func dateNum(y int, m time.Month, d int) dayNum {
	return dayNum(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// time returns the date as midnight UTC, the form pgx encodes as a date.
func (d dayNum) time() time.Time { return time.Unix(int64(d)*86400, 0).UTC() }

// New returns a Collector writing to db. Start Run in a goroutine and call
// Close on shutdown.
func New(db DB, opts Options) *Collector {
	c := &Collector{
		db:          db,
		log:         opts.Log,
		now:         opts.Now,
		clientIP:    opts.ClientIP,
		country:     opts.Country,
		flushEvery:  opts.FlushEvery,
		maxPaths:    opts.MaxPaths,
		maxVisitors: opts.MaxVisitors,
		siteSalt:    slices.Clone(opts.SiteSalt),
		internal:    make(map[string]bool),
		done:        make(chan struct{}),
		loc:         opts.Location,
		pending:     make(map[dayNum]*bucket),
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.now == nil {
		c.now = time.Now
	}
	if c.clientIP == nil {
		c.clientIP = remoteHost
	}
	if c.flushEvery <= 0 {
		c.flushEvery = defaultFlushEvery
	}
	if c.maxPaths <= 0 {
		c.maxPaths = defaultMaxPaths
	}
	if c.maxVisitors <= 0 {
		c.maxVisitors = defaultMaxVisitors
	}
	if len(c.siteSalt) == 0 {
		c.siteSalt = make([]byte, 32)
		rand.Read(c.siteSalt)
	}
	if c.loc == nil {
		c.loc = time.UTC
	}
	for _, h := range opts.Hosts {
		if h = normalizeHost(h); h != "" {
			c.internal[h] = true
		}
	}
	c.enabled.Store(!opts.Disabled)
	c.today = dayOf(c.now(), c.loc)
	c.resetDayLocked()
	return c
}

func remoteHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return h
	}
	return r.RemoteAddr
}

// SetEnabled switches recording on or off at runtime. Data already recorded
// is still flushed.
func (c *Collector) SetEnabled(on bool) {
	if c != nil {
		c.enabled.Store(on)
	}
}

// Enabled reports whether Record counts page views.
func (c *Collector) Enabled() bool { return c != nil && c.enabled.Load() }

// SetLocation changes the time zone that sets day boundaries.
func (c *Collector) SetLocation(loc *time.Location) {
	if c == nil || loc == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loc = loc
	c.rotateLocked(c.now())
}

// purposeHeaders announce speculative loads that are not page views.
var purposeHeaders = []string{"Sec-Purpose", "Purpose", "X-Purpose", "X-Moz"}

// countable rejects requests that are not a person opening a page: anything
// but GET, prefetches and prerenders, and subresource or iframe loads.
func countable(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, k := range purposeHeaders {
		if v := r.Header.Get(k); v != "" {
			v = strings.ToLower(v)
			if strings.Contains(v, "prefetch") || strings.Contains(v, "prerender") || strings.Contains(v, "preview") {
				return false
			}
		}
	}
	if d := r.Header.Get("Sec-Fetch-Dest"); d != "" && !strings.EqualFold(d, "document") {
		return false
	}
	return true
}

// Record counts a page view. The web layer calls it for successful (200)
// public HTML GET responses, passing the canonical path visitors see (with
// any language prefix) and the content language. Record itself drops
// non-GET requests, prefetches and prerenders, non-document fetches and bots.
// It never blocks on I/O.
func (c *Collector) Record(r *http.Request, path, lang string) {
	if c == nil || r == nil || !c.enabled.Load() || c.closed.Load() || !countable(r) {
		return
	}
	ua := r.Header.Get("User-Agent")
	lua := strings.ToLower(strings.TrimSpace(ua))
	if isBotLower(lua) {
		return
	}
	browser, osName, device := parseUA(lua)
	source, hasSource := referrerSource(r, c.internal)
	ip := c.clientIP(r)
	var country string
	if c.country != nil {
		if country = c.country(r); !countryCode(country) {
			country = ""
		}
	}
	path = cleanPath(path)
	lang = cleanLang(lang)

	c.mu.Lock()
	defer c.mu.Unlock()
	// Read the clock under the lock so that a concurrent flush that already
	// rotated to a new day is never undone by an older timestamp.
	c.rotateLocked(c.now())
	b := c.pending[c.today]
	if b == nil {
		b = newBucket()
		c.pending[c.today] = b
	}
	b.views++
	b.pages[admit(c.paths, path, c.maxPaths)]++
	if hasSource {
		b.dims[dimReferrer][admit(c.admitted[dimReferrer], source, dimCaps[dimReferrer])]++
	}
	b.dims[dimBrowser][browser]++
	b.dims[dimOS][osName]++
	b.dims[dimDevice][device]++
	if lang != "" {
		b.dims[dimLang][admit(c.admitted[dimLang], lang, dimCaps[dimLang])]++
	}
	if country != "" {
		b.dims[dimCountry][country]++
	}
	c.visitors.add(c.hashLocked(ip, ua))
	b.visitors = max(b.visitors, c.visitors.count())
}

// countryCode accepts two upper-case ASCII letters, the form of ISO
// 3166-1 alpha-2 codes, so that no other value reaches analytics_dims.
func countryCode(c string) bool {
	return len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}

// admit returns v if it is, or can still be, one of the at most max values
// tracked today, and "(other)" otherwise.
func admit(set map[string]struct{}, v string, max int) string {
	if _, ok := set[v]; ok {
		return v
	}
	if len(set) >= max {
		return otherPath
	}
	set[v] = struct{}{}
	return v
}

func (c *Collector) hashLocked(ip, ua string) uint64 {
	buf := make([]byte, 0, len(c.salt)+len(c.siteSalt)+len(ip)+1+len(ua))
	buf = append(buf, c.salt[:]...)
	buf = append(buf, c.siteSalt...)
	buf = append(buf, ip...)
	buf = append(buf, 0)
	buf = append(buf, ua...)
	sum := sha256.Sum256(buf)
	clear(buf)
	return binary.BigEndian.Uint64(sum[:8])
}

// rotateLocked starts a new day (new salt, empty visitor set) when now falls
// on a different local date than the current one.
func (c *Collector) rotateLocked(now time.Time) {
	if d := dayOf(now, c.loc); d != c.today {
		c.today = d
		c.resetDayLocked()
		if n := c.trimPendingLocked(); n > 0 {
			c.log.Error("analytics: discarded unflushed statistics", "days", n)
		}
	}
}

func (c *Collector) resetDayLocked() {
	rand.Read(c.salt[:])
	c.visitors = newVisitorSet(c.maxVisitors)
	c.paths = make(map[string]struct{})
	for i := range c.admitted {
		c.admitted[i] = make(map[string]struct{})
	}
}

// trimPendingLocked discards the oldest unflushed days beyond
// maxPendingDays and returns how many it dropped.
func (c *Collector) trimPendingLocked() int {
	n := 0
	for len(c.pending) > maxPendingDays {
		delete(c.pending, slices.Min(slices.Collect(maps.Keys(c.pending))))
		n++
	}
	return n
}

// Run flushes every FlushEvery and rotates the daily salt at local midnight
// until ctx is cancelled or Close is called. It does not flush on exit; call
// Close for the final flush.
func (c *Collector) Run(ctx context.Context) {
	if c == nil {
		return
	}
	tick := time.NewTicker(c.flushEvery)
	defer tick.Stop()
	midnight := time.NewTimer(c.untilMidnight())
	defer midnight.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		case <-tick.C:
			fctx, cancel := context.WithTimeout(ctx, flushTimeout)
			if err := c.Flush(fctx); err != nil && ctx.Err() == nil {
				c.log.Warn("analytics flush failed; will retry", "err", err)
			}
			cancel()
		case <-midnight.C:
			c.mu.Lock()
			c.rotateLocked(c.now())
			c.mu.Unlock()
			midnight.Reset(c.untilMidnight())
		}
	}
}

func (c *Collector) untilMidnight() time.Duration {
	c.mu.Lock()
	loc := c.loc
	c.mu.Unlock()
	now := c.now().In(loc)
	y, m, d := now.Date()
	return max(time.Date(y, m, d+1, 0, 0, 0, 0, loc).Sub(now), time.Second)
}

// Close stops Record and Run and writes what is still in memory. Call it
// after the HTTP server has shut down. It may be called more than once.
func (c *Collector) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
	})
	return c.Flush(ctx)
}

// flushSQL upserts all aggregates in one statement, so a flush either
// applies completely or not at all and can safely be retried.
const flushSQL = `WITH
days AS (
	INSERT INTO analytics_days AS t (day, views, visitors)
	SELECT * FROM unnest($1::date[], $2::int8[], $3::int8[])
	ON CONFLICT (day) DO UPDATE
	SET views = t.views + EXCLUDED.views, visitors = GREATEST(t.visitors, EXCLUDED.visitors)
),
pages AS (
	INSERT INTO analytics_pages AS t (day, path, views)
	SELECT * FROM unnest($4::date[], $5::text[], $6::int8[])
	ON CONFLICT (day, path) DO UPDATE SET views = t.views + EXCLUDED.views
),
dims AS (
	INSERT INTO analytics_dims AS t (day, dim, value, views)
	SELECT * FROM unnest($7::date[], $8::text[], $9::text[], $10::int8[])
	ON CONFLICT (day, dim, value) DO UPDATE SET views = t.views + EXCLUDED.views
)
SELECT 1`

// Flush writes the in-memory aggregates. On failure they are kept (up to
// maxPendingDays days) and retried by the next flush.
func (c *Collector) Flush(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.db == nil {
		return errors.New("analytics: no database")
	}
	c.flushMu.Lock()
	defer c.flushMu.Unlock()

	c.mu.Lock()
	c.rotateLocked(c.now())
	snap := c.pending
	c.pending = make(map[dayNum]*bucket)
	c.mu.Unlock()
	if len(snap) == 0 {
		return nil
	}
	_, err := c.db.Exec(ctx, flushSQL, flushArgs(snap)...)
	if err == nil {
		return nil
	}
	c.mu.Lock()
	for d, b := range snap {
		if cur := c.pending[d]; cur != nil {
			b.merge(cur)
		}
		c.pending[d] = b
	}
	dropped := c.trimPendingLocked()
	c.mu.Unlock()
	if dropped > 0 {
		c.log.Error("analytics: discarded unflushed statistics", "days", dropped)
	}
	return fmt.Errorf("analytics: flush: %w", err)
}

// flushArgs flattens the buckets into the parameter arrays of flushSQL, in
// key order so that concurrent writers lock rows in the same order.
func flushArgs(snap map[dayNum]*bucket) []any {
	var (
		dDays           []time.Time
		dViews, dVisits []int64
		pDays           []time.Time
		pPaths          []string
		pViews          []int64
		xDays           []time.Time
		xDims, xValues  []string
		xViews          []int64
	)
	for _, d := range slices.Sorted(maps.Keys(snap)) {
		b, t := snap[d], d.time()
		dDays, dViews, dVisits = append(dDays, t), append(dViews, b.views), append(dVisits, b.visitors)
		for _, p := range slices.Sorted(maps.Keys(b.pages)) {
			pDays, pPaths, pViews = append(pDays, t), append(pPaths, p), append(pViews, b.pages[p])
		}
		for i, m := range b.dims {
			for _, v := range slices.Sorted(maps.Keys(m)) {
				xDays, xDims, xValues, xViews = append(xDays, t), append(xDims, dimNames[i]), append(xValues, v), append(xViews, m[v])
			}
		}
	}
	return []any{dDays, dViews, dVisits, pDays, pPaths, pViews, xDays, xDims, xValues, xViews}
}
