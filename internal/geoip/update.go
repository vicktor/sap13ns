package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// DB-IP publishes "IP to Country Lite" on the first days of every month at
// this address (CC BY 4.0: the analytics page credits it).
const (
	dbipHost = "download.db-ip.com"
	dbipURL  = "https://" + dbipHost + "/free/dbip-country-lite-%04d-%02d.mmdb.gz"
)

// maxDownload bounds the compressed file (about 4 MB).
const maxDownload = 64 << 20

// Update downloads this month's DB-IP country database, or last month's
// while this month's is not out yet, checks it and puts it at path in
// place of the old one. A running server picks it up by itself. client nil
// means http.DefaultClient; its redirects are limited to DB-IP's host.
func Update(ctx context.Context, client *http.Client, path string, now time.Time) (Info, error) {
	if path == "" {
		return Info{}, errors.New("geoip: SAP13NS_GEOIP_DB is not set")
	}
	if client == nil {
		client = http.DefaultClient
	}
	cc := *client
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return checkURL(req.URL)
	}
	var (
		b   []byte
		err error
	)
	for _, month := range []time.Time{now, now.AddDate(0, 0, -now.Day())} { // this month, then the last
		y, m, _ := month.Date()
		b, err = download(ctx, &cc, fmt.Sprintf(dbipURL, y, m))
		if !errors.Is(err, errNotPublished) {
			break
		}
	}
	if err != nil {
		return Info{}, fmt.Errorf("geoip: %w", err)
	}
	r, err := parse(b)
	if err != nil {
		return Info{}, fmt.Errorf("geoip: the downloaded database: %w", err)
	}
	if err := writeAtomic(path, b); err != nil {
		return Info{}, fmt.Errorf("geoip: %w", err)
	}
	return Info{Path: path, Loaded: true, Type: r.Metadata.DatabaseType, Built: r.Metadata.BuildTime()}, nil
}

var errNotPublished = errors.New("not published yet")

func checkURL(u *url.URL) error {
	if u.Scheme != "https" || u.Host != dbipHost || u.User != nil {
		return fmt.Errorf("refusing to follow %s://%s", u.Scheme, u.Host)
	}
	return nil
}

func download(ctx context.Context, c *http.Client, raw string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sap13ns")
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return nil, errNotPublished
	case res.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%s: %s", raw, res.Status)
	}
	zr, err := gzip.NewReader(io.LimitReader(res.Body, maxDownload))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", raw, err)
	}
	b, err := io.ReadAll(io.LimitReader(zr, MaxSize+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", raw, err)
	}
	if len(b) > MaxSize {
		return nil, fmt.Errorf("%s: larger than %d MB", raw, MaxSize>>20)
	}
	return b, nil
}

// writeAtomic replaces path with b through a temporary file in the same
// directory, readable by the file's group (the server's).
func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".geoip-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once renamed
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(0o640); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
