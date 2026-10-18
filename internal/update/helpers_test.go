package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// fakeGitHub is one TLS server that answers for api.github.com, github.com
// and the asset hosts. Its client dials the server whatever the host, so
// the real host allowlist is exercised (the httptest certificate is valid
// for example.com, which the client verifies against).
type fakeGitHub struct {
	srv *httptest.Server

	mu       sync.Mutex
	latest   []byte            // JSON for /releases/latest; nil answers 404
	files    map[string][]byte // asset name -> contents
	hits     map[string]int    // host+path -> requests
	override func(w http.ResponseWriter, r *http.Request) bool
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{files: map[string][]byte{}, hits: map[string]int{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	f.mu.Lock()
	f.hits[host+r.URL.Path]++
	override := f.override
	latest := f.latest
	f.mu.Unlock()
	if override != nil && override(w, r) {
		return
	}
	switch host {
	case "api.github.com":
		if r.URL.Path != "/repos/"+DefaultRepo+"/releases/latest" || latest == nil {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(latest)
	case "github.com":
		// /<owner>/<repo>/releases/download/<tag>/<name>: GitHub redirects
		// to a signed URL on the assets host.
		http.Redirect(w, r, "https://release-assets.githubusercontent.com/assets/"+path.Base(r.URL.Path)+"?sig=secret", http.StatusFound)
	case "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		f.mu.Lock()
		b, ok := f.files[path.Base(r.URL.Path)]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) client() *http.Client {
	tr := f.srv.Client().Transport.(*http.Transport).Clone()
	addr := f.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	tr.TLSClientConfig.ServerName = "example.com"
	return &http.Client{Transport: tr}
}

func (f *fakeGitHub) setLatest(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.latest = b
	f.mu.Unlock()
}

func (f *fakeGitHub) setFile(name string, b []byte) {
	f.mu.Lock()
	f.files[name] = b
	f.mu.Unlock()
}

func (f *fakeGitHub) setOverride(h func(w http.ResponseWriter, r *http.Request) bool) {
	f.mu.Lock()
	f.override = h
	f.mu.Unlock()
}

func (f *fakeGitHub) hitCount(hostPath string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[hostPath]
}

func (f *fakeGitHub) totalHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.hits {
		n += v
	}
	return n
}

func newKey(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub), priv
}

func sign(priv ed25519.PrivateKey, data []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)) + "\n")
}

type tarEntry struct {
	name     string
	typ      byte
	body     string
	linkname string
	mode     int64
}

func makeTarGz(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Linkname: e.linkname}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeBinary is a shell script that answers `version` like sap13ns does.
func fakeBinary(version string) string {
	return fmt.Sprintf("#!/bin/sh\necho 'sap13ns %s (go-test fake)'\n", version)
}

func assetURL(version, name string) string {
	return "https://github.com/" + DefaultRepo + "/releases/download/" + version + "/" + name
}

// publish puts a complete signed release for this platform on f and returns
// it. The tarball holds binary (as ./sap13ns) plus a LICENSE.
func publish(t *testing.T, f *fakeGitHub, version string, priv ed25519.PrivateKey, binary string, goos, goarch string) *Release {
	t.Helper()
	name := AssetName(version, goos, goarch)
	tgz := makeTarGz(t,
		tarEntry{name: "./", typ: tar.TypeDir},
		tarEntry{name: "./sap13ns", body: binary},
		tarEntry{name: "./LICENSE", body: "licence text", mode: 0o644},
	)
	sum := sha256.Sum256(tgz)
	sums := fmt.Sprintf("%x  %s\n%x  %s\n", sha256.Sum256([]byte("other")), "sap13ns_x_plan9_386.tar.gz", sum, name)
	f.setFile(name, tgz)
	f.setFile(sumsName, []byte(sums))
	f.setFile(sigName, sign(priv, []byte(sums)))
	return &Release{
		Version: version,
		URL:     "https://github.com/" + DefaultRepo + "/releases/tag/" + version,
		Assets: []Asset{
			{Name: name, URL: assetURL(version, name), Size: int64(len(tgz))},
			{Name: sumsName, URL: assetURL(version, sumsName), Size: int64(len(sums))},
			{Name: sigName, URL: assetURL(version, sigName), Size: 89},
		},
	}
}

func errContains(err error, sub string) bool {
	return err != nil && strings.Contains(err.Error(), sub)
}
