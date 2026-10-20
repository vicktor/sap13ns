// Package geoip maps visitor addresses to countries for the analytics
// (docs/DEPLOY.md, "Visitors by country"). It reads a MaxMind DB file,
// SAP13NS_GEOIP_DB: DB-IP's free "IP to Country Lite" (CC BY 4.0), which
// Update downloads, or MaxMind's GeoLite2 Country kept by geoipupdate.
//
// Lookups happen in memory and only the country code leaves this package:
// the address is neither stored nor logged. A missing or unreadable file
// turns lookups off ("" for every address) without stopping the server, and
// a replaced file is picked up within a few minutes.
package geoip

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// MaxSize bounds the database file. DB-IP's country database is about
// 10 MB and GeoLite2 Country about 8 MB.
const MaxSize = 128 << 20

// recheck is how often a lookup checks whether the file was replaced.
const recheck = 5 * time.Minute

// DB is a country database. Its methods are safe for concurrent use and on
// a nil DB, which finds nothing.
type DB struct {
	path string
	log  *slog.Logger
	now  func() time.Time

	r       atomic.Pointer[maxminddb.Reader]
	stamp   atomic.Pointer[fileStamp] // of the file r was read from
	checked atomic.Int64              // unix seconds of the last check
	busy    atomic.Bool               // a check is running
}

type fileStamp struct {
	mod  time.Time
	size int64
}

var (
	sharedMu sync.Mutex
	shared   = map[string]*DB{}
)

// Open returns the database at path, shared by every caller that opens the
// same path (each site of a network). It reads the file now; if that
// fails, it logs why and tries again later. An empty path returns nil.
func Open(path string, log *slog.Logger) *DB {
	if path == "" {
		return nil
	}
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if d := shared[path]; d != nil {
		return d
	}
	d := newDB(path, log, time.Now)
	d.check()
	shared[path] = d
	return d
}

func newDB(path string, log *slog.Logger, now func() time.Time) *DB {
	if log == nil {
		log = slog.Default()
	}
	return &DB{path: path, log: log, now: now}
}

// Country returns the ISO 3166-1 alpha-2 code of the country of ip, or ""
// when the database has none (private addresses among them) or is not
// loaded.
func (d *DB) Country(ip netip.Addr) string {
	if d == nil || !ip.IsValid() {
		return ""
	}
	if now := d.now().Unix(); now-d.checked.Load() >= int64(recheck/time.Second) && d.busy.CompareAndSwap(false, true) {
		go func() {
			defer d.busy.Store(false)
			d.check()
		}()
	}
	r := d.r.Load()
	if r == nil {
		return ""
	}
	return country(r, ip.Unmap())
}

func country(r *maxminddb.Reader, ip netip.Addr) string {
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := r.Lookup(ip).Decode(&rec); err != nil {
		return ""
	}
	if c := rec.Country.ISOCode; validCode(c) {
		return c
	}
	return ""
}

// validCode accepts two upper-case ASCII letters.
func validCode(c string) bool {
	return len(c) == 2 && c[0] >= 'A' && c[0] <= 'Z' && c[1] >= 'A' && c[1] <= 'Z'
}

// Info describes the loaded database.
type Info struct {
	Path   string
	Loaded bool
	Type   string    // e.g. "DBIP-Country-Lite" or "GeoLite2-Country"
	Built  time.Time // when its publisher built it
}

// Info reports what is loaded; on a nil DB, nothing.
func (d *DB) Info() Info {
	if d == nil {
		return Info{}
	}
	in := Info{Path: d.path}
	if r := d.r.Load(); r != nil {
		in.Loaded, in.Type, in.Built = true, r.Metadata.DatabaseType, r.Metadata.BuildTime()
	}
	return in
}

// check reloads the file if it changed since it was last read.
func (d *DB) check() {
	d.checked.Store(d.now().Unix())
	fi, err := os.Stat(d.path)
	if err != nil {
		if d.r.Load() != nil || d.stamp.Load() == nil {
			d.log.Warn("geoip: country database unavailable; visitors are not located", "path", d.path, "err", err)
		}
		d.r.Store(nil)
		d.stamp.Store(&fileStamp{})
		return
	}
	st := &fileStamp{mod: fi.ModTime(), size: fi.Size()}
	if old := d.stamp.Load(); old != nil && *old == *st {
		return
	}
	d.stamp.Store(st)
	r, err := Load(d.path)
	if err != nil {
		d.log.Error("geoip: country database rejected; visitors are not located", "path", d.path, "err", err)
		d.r.Store(nil)
		return
	}
	d.r.Store(r)
	d.log.Info("geoip: country database loaded", "path", d.path, "type", r.Metadata.DatabaseType,
		"built", r.Metadata.BuildTime().Format(time.DateOnly))
}

// Load reads and checks a country database. The file is read into memory,
// so replacing it does not disturb lookups in progress.
func Load(path string) (*maxminddb.Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > MaxSize {
		return nil, fmt.Errorf("larger than %d MB", MaxSize>>20)
	}
	b := make([]byte, fi.Size())
	if _, err := f.ReadAt(b, 0); err != nil {
		return nil, err
	}
	return parse(b)
}

// parse checks the structure of a database, and that it is one of
// countries: it must place a well-known address in a country.
func parse(b []byte) (*maxminddb.Reader, error) {
	r, err := maxminddb.OpenBytes(b)
	if err != nil {
		return nil, err
	}
	if err := r.Verify(); err != nil {
		return nil, fmt.Errorf("damaged: %w", err)
	}
	if country(r, probe) == "" {
		return nil, fmt.Errorf("a %q database without countries", r.Metadata.DatabaseType)
	}
	return r, nil
}

// probe is an address every country database places (Google's resolver).
var probe = netip.MustParseAddr("8.8.8.8")
