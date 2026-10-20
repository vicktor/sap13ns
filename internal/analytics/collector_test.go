package analytics

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// fakeDB applies flushSQL with the same upsert semantics as PostgreSQL.
type fakeDB struct {
	mu    sync.Mutex
	fail  func() error
	execs int
	days  map[string][2]int64 // day -> views, visitors
	pages map[string]int64    // "day path"
	dims  map[string]int64    // "day dim value"
}

func newFakeDB() *fakeDB {
	return &fakeDB{days: map[string][2]int64{}, pages: map[string]int64{}, dims: map[string]int64{}}
}

func (f *fakeDB) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	if f.fail != nil {
		if err := f.fail(); err != nil {
			return pgconn.CommandTag{}, err
		}
	}
	if sql != flushSQL {
		return pgconn.CommandTag{}, fmt.Errorf("unexpected SQL %q", sql)
	}
	key := func(t time.Time) string { return t.Format(time.DateOnly) }
	dd, dv, du := args[0].([]time.Time), args[1].([]int64), args[2].([]int64)
	for i := range dd {
		cur := f.days[key(dd[i])]
		f.days[key(dd[i])] = [2]int64{cur[0] + dv[i], max(cur[1], du[i])}
	}
	pd, pp, pv := args[3].([]time.Time), args[4].([]string), args[5].([]int64)
	for i := range pd {
		f.pages[key(pd[i])+" "+pp[i]] += pv[i]
	}
	xd, xk, xv, xn := args[6].([]time.Time), args[7].([]string), args[8].([]string), args[9].([]int64)
	seen := map[string]bool{}
	for i := range xd {
		k := key(xd[i]) + " " + xk[i] + " " + xv[i]
		if seen[k] {
			return pgconn.CommandTag{}, errors.New("ON CONFLICT DO UPDATE command cannot affect row a second time")
		}
		seen[k] = true
		f.dims[k] += xn[i]
	}
	return pgconn.CommandTag{}, nil
}

func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("fakeDB: Query not supported")
}

func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return errRow{} }

type errRow struct{}

func (errRow) Scan(...any) error { return errors.New("fakeDB: QueryRow not supported") }

func (f *fakeDB) day(d string) [2]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.days[d]
}

func (f *fakeDB) page(d, p string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pages[d+" "+p]
}

func (f *fakeDB) dim(d, dim, v string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dims[d+" "+dim+" "+v]
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

var madrid = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		panic(err)
	}
	return loc
}()

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestCollector(db DB, clk *clock, opts Options) *Collector {
	opts.Now = clk.Now
	if opts.Log == nil {
		opts.Log = quietLog()
	}
	if opts.Location == nil {
		opts.Location = madrid
	}
	return New(db, opts)
}

func pageReq(ip, ua string) *http.Request {
	r := httptest.NewRequest("GET", "https://example.com/", nil)
	r.RemoteAddr = net.JoinHostPort(ip, "51234")
	r.Header.Set("User-Agent", ua)
	return r
}

// testViews returns the unflushed views of all days.
func (c *Collector) testViews() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	for _, b := range c.pending {
		n += b.views
	}
	return n
}

func (c *Collector) testBucket(d string) *bucket {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pending[dateNum(t.Date())]
}

func TestRecordFilters(t *testing.T) {
	cases := []struct {
		name   string
		method string
		header map[string]string
		count  bool
	}{
		{"plain GET", "GET", nil, true},
		{"navigation", "GET", map[string]string{"Sec-Fetch-Dest": "document", "Sec-Fetch-Mode": "navigate"}, true},
		{"HEAD", "HEAD", nil, false},
		{"POST", "POST", nil, false},
		{"Sec-Purpose prefetch", "GET", map[string]string{"Sec-Purpose": "prefetch"}, false},
		{"Sec-Purpose prerender", "GET", map[string]string{"Sec-Purpose": "prefetch;prerender"}, false},
		{"Purpose prefetch", "GET", map[string]string{"Purpose": "prefetch"}, false},
		{"X-Moz prefetch", "GET", map[string]string{"X-Moz": "prefetch"}, false},
		{"X-Purpose preview", "GET", map[string]string{"X-Purpose": "preview"}, false},
		{"image fetch", "GET", map[string]string{"Sec-Fetch-Dest": "image"}, false},
		{"iframe", "GET", map[string]string{"Sec-Fetch-Dest": "iframe"}, false},
		{"fetch API", "GET", map[string]string{"Sec-Fetch-Dest": "empty"}, false},
		{"bot", "GET", map[string]string{"User-Agent": "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"}, false},
		{"AI crawler", "GET", map[string]string{"User-Agent": "Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko); compatible; GPTBot/1.2; +https://openai.com/gptbot"}, false},
		{"empty UA", "GET", map[string]string{"User-Agent": ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCollector(newFakeDB(), &clock{t: time.Now()}, Options{})
			r := pageReq("203.0.113.7", chromeUA)
			r.Method = tc.method
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			c.Record(r, "/", "es")
			if got := c.testViews() == 1; got != tc.count {
				t.Errorf("counted = %v, want %v", got, tc.count)
			}
		})
	}
}

func TestEnabledToggle(t *testing.T) {
	c := newTestCollector(newFakeDB(), &clock{t: time.Now()}, Options{Disabled: true})
	r := pageReq("203.0.113.7", chromeUA)
	c.Record(r, "/", "")
	if c.Enabled() || c.testViews() != 0 {
		t.Fatal("disabled collector counted a view")
	}
	c.SetEnabled(true)
	c.Record(r, "/", "")
	c.SetEnabled(false)
	c.Record(r, "/", "")
	if !(c.testViews() == 1) {
		t.Fatalf("views = %d, want 1", c.testViews())
	}
}

func TestNilCollector(t *testing.T) {
	var c *Collector
	c.Record(pageReq("203.0.113.7", chromeUA), "/", "")
	c.SetEnabled(true)
	c.SetLocation(time.UTC)
	c.Run(context.Background())
	if c.Enabled() || c.Flush(context.Background()) != nil || c.Close(context.Background()) != nil {
		t.Fatal("nil collector misbehaved")
	}
}

func TestUniqueVisitorsAndDimensions(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, madrid)}
	c := newTestCollector(newFakeDB(), clk, Options{Hosts: []string{"www.alias.test"}})
	safari := "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1"

	r := pageReq("203.0.113.7", chromeUA)
	r.Header.Set("Referer", "https://www.google.com/")
	c.Record(r, "/", "es")
	r = pageReq("203.0.113.7", chromeUA) // same visitor, internal navigation
	r.Header.Set("Referer", "https://example.com/")
	c.Record(r, "/blog/hello", "es")
	r = pageReq("203.0.113.7", chromeUA) // same visitor, alias host
	r.Header.Set("Referer", "https://alias.test/x")
	c.Record(r, "/blog/hello", "es")
	c.Record(pageReq("203.0.113.7", safari), "/en/", "en") // same IP, other device
	c.Record(pageReq("2001:db8::1", safari), "/en/", "en")
	r = httptest.NewRequest("GET", "https://example.com/?utm_source=Newsletter", nil)
	r.RemoteAddr = "198.51.100.1:443"
	r.Header.Set("User-Agent", chromeUA)
	c.Record(r, "/?utm_source=Newsletter", "")

	b := c.testBucket("2026-10-18")
	if b == nil {
		t.Fatal("no bucket for 2026-10-18")
	}
	if b.views != 6 || b.visitors != 4 {
		t.Errorf("views, visitors = %d, %d; want 6, 4", b.views, b.visitors)
	}
	wantPages := map[string]int64{"/": 2, "/blog/hello": 2, "/en/": 2}
	if fmt.Sprint(b.pages) != fmt.Sprint(wantPages) {
		t.Errorf("pages = %v, want %v", b.pages, wantPages)
	}
	want := [numDims]map[string]int64{
		dimReferrer: {"google.com": 1, direct: 2, "newsletter": 1},
		dimBrowser:  {browserChrome: 4, browserSafari: 2},
		dimOS:       {osWindows: 4, osIOS: 2},
		dimDevice:   {deviceDesktop: 4, deviceMobile: 2},
		dimLang:     {"es": 3, "en": 2},
	}
	for i := range want {
		if fmt.Sprint(b.dims[i]) != fmt.Sprint(want[i]) {
			t.Errorf("%s = %v, want %v", dimNames[i], b.dims[i], want[i])
		}
	}
}

func TestClientIPOption(t *testing.T) {
	c := newTestCollector(newFakeDB(), &clock{t: time.Now()}, Options{
		ClientIP: func(r *http.Request) string { return r.Header.Get("X-Test-IP") },
	})
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.1"} {
		r := pageReq("10.0.0.1", chromeUA) // same proxy address
		r.Header.Set("X-Test-IP", ip)
		c.Record(r, "/", "")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.visitors.count(); n != 2 {
		t.Fatalf("visitors = %d, want 2", n)
	}
}

func TestCountryOption(t *testing.T) {
	c := newTestCollector(newFakeDB(), &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}, Options{
		Country: func(r *http.Request) string { return r.Header.Get("X-Test-Country") },
	})
	for _, cc := range []string{"ES", "ES", "US", "", "es", "ESP", "<b>", "\x00\x00"} {
		r := pageReq("192.0.2.1", chromeUA)
		r.Header.Set("X-Test-Country", cc)
		c.Record(r, "/", "")
	}
	b := c.testBucket("2026-10-18")
	if b == nil || b.views != 8 {
		t.Fatalf("bucket %+v", b)
	}
	if got, want := fmt.Sprint(b.dims[dimCountry]), fmt.Sprint(map[string]int64{"ES": 2, "US": 1}); got != want {
		t.Errorf("countries = %s, want %s", got, want)
	}
	plain := newTestCollector(newFakeDB(), &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}, Options{})
	plain.Record(pageReq("192.0.2.1", chromeUA), "/", "")
	if b := plain.testBucket("2026-10-18"); len(b.dims[dimCountry]) != 0 {
		t.Errorf("countries without Options.Country: %v", b.dims[dimCountry])
	}
}

func TestSaltRotation(t *testing.T) {
	// 23:59 in Madrid is 21:59 UTC: the day must follow the site time zone.
	clk := &clock{t: time.Date(2026, 10, 18, 23, 59, 0, 0, madrid)}
	c := newTestCollector(newFakeDB(), clk, Options{})
	r := pageReq("203.0.113.7", chromeUA)
	c.Record(r, "/", "")
	c.Record(r, "/", "")

	c.mu.Lock()
	salt1, hash1 := c.salt, c.hashLocked("203.0.113.7", chromeUA)
	c.mu.Unlock()

	clk.Set(time.Date(2026, 10, 19, 0, 1, 0, 0, madrid))
	c.Record(r, "/", "")

	c.mu.Lock()
	salt2, hash2 := c.salt, c.hashLocked("203.0.113.7", chromeUA)
	today, n := c.today, c.visitors.count()
	c.mu.Unlock()
	if salt1 == salt2 {
		t.Error("salt not rotated at local midnight")
	}
	if hash1 == hash2 {
		t.Error("visitor hash is the same on two days")
	}
	if today != dateNum(2026, 10, 19) || n != 1 {
		t.Errorf("today = %v, visitors = %d", today.time(), n)
	}
	if b := c.testBucket("2026-10-18"); b == nil || b.views != 2 || b.visitors != 1 {
		t.Errorf("2026-10-18 bucket = %+v", b)
	}
	if b := c.testBucket("2026-10-19"); b == nil || b.views != 1 || b.visitors != 1 {
		t.Errorf("2026-10-19 bucket = %+v", b)
	}

	// Rotation also happens without traffic (on flush or the midnight timer).
	clk.Set(time.Date(2026, 10, 20, 0, 0, 1, 0, madrid))
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	rotated := c.salt != salt2 && c.visitors.count() == 0 && c.today == dateNum(2026, 10, 20)
	c.mu.Unlock()
	if !rotated {
		t.Error("flush after midnight did not rotate the salt")
	}

	// Changing the time zone moves the day boundary.
	c.SetLocation(time.FixedZone("UTC-12", -12*3600))
	c.mu.Lock()
	today = c.today
	c.mu.Unlock()
	if today != dateNum(2026, 10, 19) {
		t.Errorf("after SetLocation today = %v, want 2026-10-19", today.time())
	}
}

func TestUntilMidnight(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 24, 23, 0, 0, 0, madrid)}
	c := newTestCollector(newFakeDB(), clk, Options{})
	if d := c.untilMidnight(); d != time.Hour {
		t.Errorf("untilMidnight = %v, want 1h", d)
	}
	// 25 October 2026 has 25 hours in Madrid (end of summer time).
	clk.Set(time.Date(2026, 10, 25, 0, 0, 0, 0, madrid))
	if d := c.untilMidnight(); d != 25*time.Hour {
		t.Errorf("untilMidnight = %v, want 25h", d)
	}
}

func TestMaxPaths(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, madrid)}
	db := newFakeDB()
	c := newTestCollector(db, clk, Options{MaxPaths: 3})
	r := pageReq("203.0.113.7", chromeUA)
	for _, p := range []string{"/a", "/b", "/c", "/d", "/e", "/a"} {
		c.Record(r, p, "")
	}
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The cap is per day, not per flush.
	c.Record(r, "/f", "")
	c.Record(r, "/b", "")
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int64{"/a": 2, "/b": 2, "/c": 1, otherPath: 3, "/d": 0, "/f": 0} {
		if got := db.page("2026-10-18", p); got != want {
			t.Errorf("page %s = %d, want %d", p, got, want)
		}
	}
	clk.Set(time.Date(2026, 10, 19, 12, 0, 0, 0, madrid))
	c.Record(r, "/f", "")
	if err := c.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.page("2026-10-19", "/f"); got != 1 {
		t.Errorf("next day /f = %d, want 1", got)
	}
}

func TestReferrerCap(t *testing.T) {
	c := newTestCollector(newFakeDB(), &clock{t: time.Now()}, Options{})
	n := dimCaps[dimReferrer] + 50
	for i := range n {
		r := httptest.NewRequest("GET", fmt.Sprintf("https://example.com/?utm_source=s%d", i), nil)
		r.Header.Set("User-Agent", chromeUA)
		c.Record(r, "/", "")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	refs := c.pending[c.today].dims[dimReferrer]
	if len(refs) != dimCaps[dimReferrer]+1 || refs[otherPath] != 50 {
		t.Errorf("%d referrer values, (other) = %d", len(refs), refs[otherPath])
	}
}

func TestFlush(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, madrid)}
	db := newFakeDB()
	c := newTestCollector(db, clk, Options{})
	ctx := context.Background()

	if err := c.Flush(ctx); err != nil || db.execs != 0 {
		t.Fatalf("empty flush: err %v, %d statements", err, db.execs)
	}
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "es")
	c.Record(pageReq("203.0.113.2", chromeUA), "/", "es")
	c.Record(pageReq("203.0.113.1", chromeUA), "/about", "es")
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.day("2026-10-18"); got != [2]int64{3, 2} {
		t.Errorf("after first flush day = %v, want [3 2]", got)
	}
	// A returning visitor adds a view but no visitor.
	c.Record(pageReq("203.0.113.2", chromeUA), "/about", "es")
	c.Record(pageReq("203.0.113.3", chromeUA), "/about", "es")
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.day("2026-10-18"); got != [2]int64{5, 3} {
		t.Errorf("after second flush day = %v, want [5 3]", got)
	}
	if db.page("2026-10-18", "/") != 2 || db.page("2026-10-18", "/about") != 3 {
		t.Errorf("pages = %v", db.pages)
	}
	if db.dim("2026-10-18", "browser", browserChrome) != 5 || db.dim("2026-10-18", "lang", "es") != 5 ||
		db.dim("2026-10-18", "referrer", direct) != 5 {
		t.Errorf("dims = %v", db.dims)
	}
	if n := c.testViews(); n != 0 {
		t.Errorf("%d views left in memory after flush", n)
	}

	// A restarted process starts counting visitors from zero; the stored
	// figure must not go down.
	c2 := newTestCollector(db, clk, Options{})
	c2.Record(pageReq("203.0.113.9", chromeUA), "/", "es")
	if err := c2.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.day("2026-10-18"); got != [2]int64{6, 3} {
		t.Errorf("after restart day = %v, want [6 3]", got)
	}
}

func TestFlushFailureKeepsData(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 1, 12, 0, 0, 0, madrid)}
	db := newFakeDB()
	var down atomic.Bool
	down.Store(true)
	db.fail = func() error {
		if down.Load() {
			return errors.New("connection refused")
		}
		return nil
	}
	c := newTestCollector(db, clk, Options{})
	ctx := context.Background()
	r := pageReq("203.0.113.1", chromeUA)

	for range 3 {
		c.Record(r, "/", "")
	}
	if err := c.Flush(ctx); err == nil {
		t.Fatal("flush succeeded on a failing database")
	}
	c.Record(r, "/", "")
	c.Record(pageReq("203.0.113.2", chromeUA), "/x", "")
	if err := c.Flush(ctx); err == nil {
		t.Fatal("flush succeeded on a failing database")
	}
	if n := c.testViews(); n != 5 {
		t.Fatalf("views kept after failures = %d, want 5", n)
	}
	down.Store(false)
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.day("2026-10-01"); got != [2]int64{5, 2} {
		t.Errorf("day = %v, want [5 2]", got)
	}
	if db.page("2026-10-01", "/") != 4 || db.page("2026-10-01", "/x") != 1 {
		t.Errorf("pages = %v", db.pages)
	}

	// While the database stays down, only the latest days are kept.
	down.Store(true)
	for d := 2; d <= 15; d++ {
		clk.Set(time.Date(2026, 10, d, 12, 0, 0, 0, madrid))
		c.Record(r, "/", "")
		c.Flush(ctx)
	}
	c.mu.Lock()
	kept := len(c.pending)
	_, newest := c.pending[dateNum(2026, 10, 15)]
	_, oldest := c.pending[dateNum(2026, 10, 15-maxPendingDays+1)]
	c.mu.Unlock()
	if kept != maxPendingDays || !newest || !oldest {
		t.Errorf("kept %d days (newest %v, oldest %v), want %d", kept, newest, oldest, maxPendingDays)
	}
	down.Store(false)
	if err := c.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := db.day("2026-10-15"); got[0] != 1 {
		t.Errorf("2026-10-15 = %v", got)
	}
	if got := db.day("2026-10-02"); got[0] != 0 {
		t.Errorf("2026-10-02 should have been discarded, got %v", got)
	}
}

func TestConcurrentRecordAndFlush(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, madrid)}
	db := newFakeDB()
	var calls atomic.Int64
	db.fail = func() error { // fail every third flush
		if calls.Add(1)%3 == 0 {
			return errors.New("transient")
		}
		return nil
	}
	c := newTestCollector(db, clk, Options{MaxPaths: 10})
	const workers, perWorker = 16, 500
	uas := []string{chromeUA, browserUAs[3].ua, browserUAs[7].ua}

	stop := make(chan struct{})
	flusher := make(chan struct{})
	go func() {
		defer close(flusher)
		for {
			select {
			case <-stop:
				return
			default:
				c.Flush(context.Background())
				c.SetEnabled(true)
			}
		}
	}()
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			rnd := rand.New(rand.NewPCG(uint64(w), 1))
			for i := range perWorker {
				r := pageReq(fmt.Sprintf("198.51.100.%d", w), uas[i%len(uas)])
				if rnd.IntN(2) == 0 {
					r.Header.Set("Referer", "https://www.google.com/")
				}
				c.Record(r, fmt.Sprintf("/p%d", rnd.IntN(20)), "es")
			}
		})
	}
	wg.Wait()
	close(stop)
	<-flusher
	db.fail = nil
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	const total = workers * perWorker
	if got := db.day("2026-10-18"); got != [2]int64{total, workers * int64(len(uas))} {
		t.Errorf("day = %v, want [%d %d]", got, total, workers*len(uas))
	}
	var pages, browsers int64
	for _, v := range db.pages {
		pages += v
	}
	for _, b := range []string{browserChrome, browserSafari, browserFirefox} {
		browsers += db.dim("2026-10-18", "browser", b)
	}
	if pages != total || browsers != total {
		t.Errorf("pages sum %d, browsers sum %d, want %d", pages, browsers, total)
	}
	if got := db.dim("2026-10-18", "referrer", "google.com") + db.dim("2026-10-18", "referrer", direct); got != total {
		t.Errorf("referrers sum %d, want %d", got, total)
	}
}

func TestRunAndClose(t *testing.T) {
	db := newFakeDB()
	c := newTestCollector(db, &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, madrid)}, Options{FlushEvery: 5 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		c.Run(context.Background())
		close(done)
	}()
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "")
	deadline := time.Now().Add(5 * time.Second)
	for db.day("2026-10-18")[0] != 1 {
		if time.Now().After(deadline) {
			t.Fatal("Run did not flush")
		}
		time.Sleep(time.Millisecond)
	}
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "")
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Close")
	}
	if got := db.day("2026-10-18"); got != [2]int64{2, 1} {
		t.Errorf("day = %v, want [2 1]", got)
	}
	c.Record(pageReq("203.0.113.1", chromeUA), "/", "")
	if c.testViews() != 0 {
		t.Error("Record after Close counted a view")
	}
	if err := c.Close(context.Background()); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestVisitorSet(t *testing.T) {
	s := newVisitorSet(1000)
	rnd := rand.New(rand.NewPCG(1, 2))
	hashes := make([]uint64, 500)
	for i := range hashes {
		hashes[i] = rnd.Uint64()
		s.add(hashes[i])
		s.add(hashes[i])
	}
	if s.count() != 500 || !s.exact() {
		t.Fatalf("count = %d (exact %v), want exactly 500", s.count(), s.exact())
	}
	const n = 200_000
	for range n - 500 {
		s.add(rnd.Uint64())
	}
	for _, h := range hashes { // returning visitors do not inflate the estimate
		s.add(h)
	}
	if len(s.m) > 1000 {
		t.Errorf("set holds %d hashes, cap 1000", len(s.m))
	}
	if est := float64(s.count()); est < n*0.85 || est > n*1.15 {
		t.Errorf("estimate %v for %d visitors", est, n)
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		cur, prev int64
		want      string
		up, down  bool
	}{
		{112, 100, "+12%", true, false},
		{97, 100, "-3%", false, true},
		{100, 100, "0%", false, false},
		{1001, 1000, "0%", false, false},
		{5, 0, "", false, false},
		{0, 4, "-100%", false, true},
	}
	for _, tc := range cases {
		d := Compare(tc.cur, tc.prev)
		if d.String() != tc.want || d.Up() != tc.up || d.Down() != tc.down {
			t.Errorf("Compare(%d, %d) = %q up %v down %v; want %q %v %v", tc.cur, tc.prev, d, d.Up(), d.Down(), tc.want, tc.up, tc.down)
		}
	}
}

func TestDayNum(t *testing.T) {
	for _, tc := range []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 10, 18, 23, 30, 0, 0, time.UTC), "2026-10-19"}, // 01:30 in Madrid
		{time.Date(2026, 10, 18, 21, 59, 0, 0, time.UTC), "2026-10-18"},
		{time.Date(1969, 12, 31, 23, 0, 0, 0, madrid), "1969-12-31"},
	} {
		if got := dayOf(tc.t, madrid).time().Format(time.DateOnly); got != tc.want {
			t.Errorf("dayOf(%v) = %s, want %s", tc.t, got, tc.want)
		}
	}
}

func BenchmarkRecord(b *testing.B) {
	c := New(newFakeDB(), Options{Log: quietLog()})
	r := pageReq("203.0.113.1", chromeUA)
	r.Header.Set("Referer", "https://www.google.com/search?q=x")
	b.ReportAllocs()
	for b.Loop() {
		c.Record(r, "/blog/hello-world", "es")
	}
}
