package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// --- a minimal MaxMind DB writer (IPv4 tree, 24-bit records) ---------------

type mmdbEnc struct{ bytes.Buffer }

func (e *mmdbEnc) ctrl(typ, size int) {
	if typ <= 7 {
		e.WriteByte(byte(typ<<5 | size))
		return
	}
	e.WriteByte(byte(size))
	e.WriteByte(byte(typ - 7))
}

func (e *mmdbEnc) str(s string) { e.ctrl(2, len(s)); e.WriteString(s) }

func (e *mmdbEnc) uint(typ int, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	n := 0
	for n < 8 && b[n] == 0 {
		n++
	}
	e.ctrl(typ, 8-n)
	e.Write(b[n:])
}

// buildMMDB writes a database placing each prefix in a country, or with
// key "city" instead of "country" when noCountry is set.
func buildMMDB(t *testing.T, dbType string, prefixes map[string]string, noCountry bool) []byte {
	t.Helper()
	// Data section: one record per country.
	var data mmdbEnc
	offsets := map[string]int{}
	for _, c := range prefixes {
		if _, ok := offsets[c]; ok {
			continue
		}
		offsets[c] = data.Len()
		data.ctrl(7, 1)
		if noCountry {
			data.str("city")
		} else {
			data.str("country")
		}
		data.ctrl(7, 1)
		data.str("iso_code")
		data.str(c)
	}
	// Search tree: node 0 is the root; -1 is "no data".
	type node struct{ rec [2]int }
	nodes := []node{{rec: [2]int{-1, -1}}}
	leaves := map[[2]int]string{} // (node, side) → country
	for p, c := range prefixes {
		pfx := netip.MustParsePrefix(p)
		ip := pfx.Addr().As4()
		bits := binary.BigEndian.Uint32(ip[:])
		n := 0
		for i := range pfx.Bits() {
			side := int(bits>>(31-i)) & 1
			if i == pfx.Bits()-1 {
				leaves[[2]int{n, side}] = c
				break
			}
			if nodes[n].rec[side] < 0 {
				nodes = append(nodes, node{rec: [2]int{-1, -1}})
				nodes[n].rec[side] = len(nodes) - 1
			}
			n = nodes[n].rec[side]
		}
	}
	count := len(nodes)
	var out bytes.Buffer
	for i, nd := range nodes {
		for side, r := range nd.rec {
			v := count // no data
			if c, ok := leaves[[2]int{i, side}]; ok {
				v = count + 16 + offsets[c]
			} else if r >= 0 {
				v = r
			}
			out.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
		}
	}
	out.Write(make([]byte, 16))
	out.Write(data.Bytes())
	out.WriteString("\xab\xcd\xefMaxMind.com")
	var md mmdbEnc
	md.ctrl(7, 9)
	md.str("binary_format_major_version")
	md.uint(5, 2)
	md.str("binary_format_minor_version")
	md.uint(5, 0)
	md.str("build_epoch")
	md.uint(9, uint64(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Unix()))
	md.str("database_type")
	md.str(dbType)
	md.str("description")
	md.ctrl(7, 1)
	md.str("en")
	md.str("test")
	md.str("ip_version")
	md.uint(5, 4)
	md.str("languages")
	md.ctrl(11, 1)
	md.str("en")
	md.str("node_count")
	md.uint(6, uint64(count))
	md.str("record_size")
	md.uint(5, 24)
	out.Write(md.Bytes())
	return out.Bytes()
}

var testPrefixes = map[string]string{"8.8.8.0/24": "US", "81.0.0.0/8": "ES"}

func writeDB(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- tests -----------------------------------------------------------------

func TestCountry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "country.mmdb")
	writeDB(t, path, buildMMDB(t, "Test-Country", testPrefixes, false))
	d := newDB(path, nil, time.Now)
	d.check()
	for ip, want := range map[string]string{
		"81.33.1.2": "ES", "8.8.8.8": "US", "::ffff:81.1.1.1": "ES",
		"10.0.0.1": "", "127.0.0.1": "", "9.9.9.9": "",
	} {
		if got := d.Country(netip.MustParseAddr(ip)); got != want {
			t.Errorf("Country(%s) = %q, want %q", ip, got, want)
		}
	}
	if got := d.Country(netip.Addr{}); got != "" {
		t.Errorf("Country(invalid) = %q", got)
	}
	in := d.Info()
	if !in.Loaded || in.Type != "Test-Country" || in.Built.Year() != 2026 {
		t.Errorf("Info = %+v", in)
	}
	var none *DB
	if none.Country(netip.MustParseAddr("8.8.8.8")) != "" || none.Info().Loaded {
		t.Error("a nil DB found something")
	}
}

func TestLoadRejects(t *testing.T) {
	dir := t.TempDir()
	for name, b := range map[string][]byte{
		"garbage":    []byte("not a database"),
		"no-country": buildMMDB(t, "Test-City", testPrefixes, true),
		"truncated":  buildMMDB(t, "Test-Country", testPrefixes, false)[20:],
	} {
		p := filepath.Join(dir, name)
		writeDB(t, p, b)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if _, err := Load(dir); err == nil {
		t.Error("loaded a directory")
	}
}

func TestReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "country.mmdb")
	var now atomic.Int64
	now.Store(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC).Unix())
	d := newDB(path, nil, func() time.Time { return time.Unix(now.Load(), 0) })
	d.check() // no file yet
	ip := netip.MustParseAddr("81.1.1.1")
	if d.Country(ip) != "" {
		t.Fatal("located without a database")
	}
	writeDB(t, path, buildMMDB(t, "Test-Country", testPrefixes, false))
	if d.Country(ip) != "" {
		t.Fatal("reloaded before the recheck interval")
	}
	now.Add(int64(recheck / time.Second))
	d.Country(ip) // starts the check
	waitIdle(t, d)
	if got := d.Country(ip); got != "ES" {
		t.Fatalf("after the file appeared: %q", got)
	}
	writeDB(t, path, buildMMDB(t, "Test-Country", map[string]string{"81.0.0.0/8": "FR", "1.0.0.0/8": "AU", "8.8.8.0/24": "US"}, false))
	now.Add(int64(recheck / time.Second))
	d.Country(ip)
	waitIdle(t, d)
	if got := d.Country(ip); got != "FR" {
		t.Fatalf("after the file was replaced: %q", got)
	}
	os.Remove(path)
	now.Add(int64(recheck / time.Second))
	d.Country(ip)
	waitIdle(t, d)
	if got := d.Country(ip); got != "" {
		t.Fatalf("after the file was removed: %q", got)
	}
}

func waitIdle(t *testing.T, d *DB) {
	t.Helper()
	for range 500 {
		if !d.busy.Load() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the check did not finish")
}

func TestOpenShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), "country.mmdb")
	writeDB(t, path, buildMMDB(t, "Test-Country", testPrefixes, false))
	a, b := Open(path, nil), Open(path, nil)
	if a != b || a.Country(netip.MustParseAddr("8.8.8.8")) != "US" {
		t.Fatal("Open does not share a loaded database")
	}
	if Open("", nil) != nil {
		t.Fatal("Open(\"\") is not nil")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(r *http.Request, status int, body []byte, header ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: h,
		Body: io.NopCloser(bytes.NewReader(body)), Request: r}
}

func gz(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func TestUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "geoip", "country.mmdb")
	body := gz(t, buildMMDB(t, "DBIP-Country-Lite", testPrefixes, false))
	var asked []string
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		if strings.HasSuffix(r.URL.Path, "-2026-11.mmdb.gz") { // not out yet
			return respond(r, http.StatusNotFound, nil), nil
		}
		return respond(r, http.StatusOK, body), nil
	})}
	in, err := Update(context.Background(), c, path, time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"https://download.db-ip.com/free/dbip-country-lite-2026-11.mmdb.gz",
		"https://download.db-ip.com/free/dbip-country-lite-2026-10.mmdb.gz",
	}
	if strings.Join(asked, " ") != strings.Join(want, " ") {
		t.Errorf("asked for %q", asked)
	}
	if in.Type != "DBIP-Country-Lite" {
		t.Errorf("Info = %+v", in)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("written file: %v %v", fi, err)
	}
	if r, err := Load(path); err != nil || country(r, netip.MustParseAddr("81.2.3.4")) != "ES" {
		t.Fatalf("written database: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".geoip-*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestUpdateRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "country.mmdb")
	writeDB(t, path, buildMMDB(t, "Test-Country", testPrefixes, false))
	now := time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC)
	for name, rt := range map[string]roundTrip{
		"redirect elsewhere": func(r *http.Request) (*http.Response, error) {
			return respond(r, http.StatusFound, nil, "Location", "https://example.com/x.mmdb.gz"), nil
		},
		"plain http redirect": func(r *http.Request) (*http.Response, error) {
			return respond(r, http.StatusFound, nil, "Location", "http://download.db-ip.com/x.mmdb.gz"), nil
		},
		"not gzip": func(r *http.Request) (*http.Response, error) {
			return respond(r, http.StatusOK, []byte("<html>")), nil
		},
		"not a country database": func(r *http.Request) (*http.Response, error) {
			return respond(r, http.StatusOK, gz(t, buildMMDB(t, "Test-City", testPrefixes, true))), nil
		},
		"server error": func(r *http.Request) (*http.Response, error) {
			return respond(r, http.StatusInternalServerError, nil), nil
		},
	} {
		if _, err := Update(context.Background(), &http.Client{Transport: rt}, path, now); err == nil {
			t.Errorf("%s: no error", name)
		}
		if r, err := Load(path); err != nil || r.Metadata.DatabaseType != "Test-Country" {
			t.Fatalf("%s: the old database was replaced", name)
		}
	}
	if _, err := Update(context.Background(), nil, "", now); err == nil {
		t.Error("no path: no error")
	}
}
