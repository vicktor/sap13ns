package wpimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func allowAll(netip.Addr) bool { return true }

func newMediaTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/file.jpg", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "image-bytes") })
	for i := 1; i <= 4; i++ {
		mux.HandleFunc(fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, fmt.Sprintf("/r%d", i+1), http.StatusFound)
		})
	}
	mux.HandleFunc("/r5", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "end") })
	mux.HandleFunc("/to-other-host", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://other.example/x.jpg", http.StatusFound)
	})
	mux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	mux.HandleFunc("/to-ftp", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://127.0.0.1/x", http.StatusFound)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write(make([]byte, 100))
	})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		for range 10 {
			w.Write(make([]byte, 10))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fetchString(f *HTTPFetcher, raw string) (string, error) {
	rc, err := f.Fetch(context.Background(), raw)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}

func TestHTTPFetcherSSRFGuard(t *testing.T) {
	srv := newMediaTestServer(t)

	// The host is allowed, but it resolves to loopback: refused at dial time.
	f := &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}}
	if _, err := fetchString(f, srv.URL+"/file.jpg"); !errors.Is(err, ErrForbiddenAddress) {
		t.Fatalf("loopback: err = %v, want ErrForbiddenAddress", err)
	}
	port := srv.URL[strings.LastIndexByte(srv.URL, ':'):]
	f = &HTTPFetcher{AllowedHosts: []string{"localhost"}}
	if _, err := fetchString(f, "http://localhost"+port+"/file.jpg"); !errors.Is(err, ErrForbiddenAddress) {
		t.Fatalf("localhost: err = %v, want ErrForbiddenAddress", err)
	}

	// With an injected guard the same request goes through.
	f = &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}, AllowAddr: allowAll}
	if body, err := fetchString(f, srv.URL+"/file.jpg"); err != nil || body != "image-bytes" {
		t.Fatalf("allowed: %q, %v", body, err)
	}

	// A redirect is re-checked by the guard too: only 127.0.0.1 is allowed
	// here, so the metadata endpoint is never dialled.
	f = &HTTPFetcher{AllowedHosts: []string{"127.0.0.1", "169.254.169.254"}, AllowAddr: func(a netip.Addr) bool {
		return a == netip.MustParseAddr("127.0.0.1")
	}}
	if _, err := fetchString(f, srv.URL+"/to-metadata"); !errors.Is(err, ErrForbiddenAddress) {
		t.Fatalf("redirect to metadata: err = %v", err)
	}
}

func TestHTTPFetcherHostsAndSchemes(t *testing.T) {
	srv := newMediaTestServer(t)
	f := &HTTPFetcher{AllowedHosts: []string{"https://www.127.0.0.1/"}, AllowAddr: allowAll}
	if _, err := fetchString(f, srv.URL+"/file.jpg"); err != nil {
		t.Errorf("allowed host given as URL with www.: %v", err)
	}
	f = &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}, AllowAddr: allowAll}
	for raw, want := range map[string]error{
		"ftp://127.0.0.1/x":              ErrForbiddenScheme,
		"file:///etc/passwd":             ErrForbiddenScheme,
		"gopher://127.0.0.1:70/_x":       ErrForbiddenScheme,
		"http://other.example/x.jpg":     ErrForbiddenHost,
		"http://user:pw@127.0.0.1/x.jpg": ErrForbiddenHost,
		srv.URL + "/to-other-host":       ErrForbiddenHost,
		srv.URL + "/to-ftp":              ErrForbiddenScheme,
	} {
		if _, err := fetchString(f, raw); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", raw, err, want)
		}
	}
	if _, err := fetchString(&HTTPFetcher{AllowAddr: allowAll}, srv.URL+"/file.jpg"); !errors.Is(err, ErrForbiddenHost) {
		t.Errorf("no allowed hosts must refuse everything: %v", err)
	}
	if _, err := fetchString(f, srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404: %v", err)
	}
}

func TestHTTPFetcherRedirectLimit(t *testing.T) {
	srv := newMediaTestServer(t)
	f := &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}, AllowAddr: allowAll}
	if body, err := fetchString(f, srv.URL+"/r2"); err != nil || body != "end" {
		t.Errorf("3 redirects: %q, %v", body, err)
	}
	if _, err := fetchString(f, srv.URL+"/r1"); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Errorf("4 redirects: err = %v", err)
	}
}

func TestHTTPFetcherLimits(t *testing.T) {
	srv := newMediaTestServer(t)
	f := &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}, AllowAddr: allowAll, MaxBytes: 10, Timeout: 200 * time.Millisecond}
	if _, err := f.Fetch(context.Background(), srv.URL+"/big"); !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("declared length: err = %v", err)
	}
	if _, err := fetchString(f, srv.URL+"/stream"); !errors.Is(err, ErrMediaTooLarge) {
		t.Errorf("streamed body: err = %v", err)
	}
	start := time.Now()
	if _, err := fetchString(f, srv.URL+"/slow"); err == nil {
		t.Error("timeout not enforced")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Errorf("timeout took %v", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(ctx, srv.URL+"/file.jpg"); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"8.8.8.8": true, "93.184.215.14": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "127.8.9.10": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false,
		"::1": false, "::": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false, "::ffff:10.0.0.1": false,
		"64:ff9b::7f00:1": false, "192.0.2.1": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestMediaName(t *testing.T) {
	for raw, want := range map[string]string{
		"https://x.example/wp-content/uploads/2020/01/Caf%C3%A9%20photo.JPG": "Cafe-photo.JPG",
		"https://x.example/a/../../etc/passwd":                               "passwd",
		"https://x.example/uploads/file.php%00.jpg":                          "file-php.jpg",
		"https://x.example/":                                                 "file",
		"https://x.example/uploads/archive.tar.gz":                           "archive-tar.gz",
	} {
		u, _ := url.Parse(raw)
		if got := mediaName(u); got != want {
			t.Errorf("mediaName(%s) = %q, want %q", raw, got, want)
		}
	}
}
