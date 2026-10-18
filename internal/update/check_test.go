package update

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func latestJSON(tag string) map[string]any {
	return map[string]any{
		"tag_name":     tag,
		"draft":        false,
		"prerelease":   false,
		"published_at": "2026-10-01T12:00:00Z",
		"body":         "## Changes\n\n* faster",
		"html_url":     "https://github.com/" + DefaultRepo + "/releases/tag/" + tag,
		"assets": []map[string]any{
			{"name": "SHA256SUMS", "browser_download_url": assetURL(tag, "SHA256SUMS"), "size": 300, "state": "uploaded"},
			{"name": "half.tar.gz", "browser_download_url": assetURL(tag, "half.tar.gz"), "size": 5, "state": "starter"},
		},
	}
}

func TestCheckNewer(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.2.0"))
	var mu sync.Mutex
	var accept, ua, apiVer string
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		defer mu.Unlock()
		accept, ua, apiVer = r.Header.Get("Accept"), r.Header.Get("User-Agent"), r.Header.Get("X-GitHub-Api-Version")
		return false
	})
	rel, newer, err := Check(context.Background(), f.client(), DefaultRepo, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !newer {
		t.Error("v1.2.0 not newer than v1.1.0")
	}
	if rel.Version != "v1.2.0" || rel.Notes != "## Changes\n\n* faster" ||
		rel.URL != "https://github.com/vicktor/sap13ns/releases/tag/v1.2.0" ||
		!rel.Published.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("unexpected release %+v", rel)
	}
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "SHA256SUMS" || rel.Assets[0].Size != 300 {
		t.Errorf("assets = %+v, want only the uploaded SHA256SUMS", rel.Assets)
	}
	mu.Lock()
	defer mu.Unlock()
	if accept != "application/vnd.github+json" || ua != "sap13ns/v1.1.0" || apiVer == "" {
		t.Errorf("headers: Accept %q, User-Agent %q, X-GitHub-Api-Version %q", accept, ua, apiVer)
	}
}

func TestCheckNotNewer(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.2.0"))
	for _, cur := range []string{"v1.2.0", "1.2.0", "v1.3.0", "dev", ""} {
		rel, newer, err := Check(context.Background(), f.client(), DefaultRepo, cur)
		if err != nil {
			t.Fatalf("%q: %v", cur, err)
		}
		if newer || rel == nil || rel.Version != "v1.2.0" {
			t.Errorf("current %q: newer=%v rel=%+v; want the release, not newer", cur, newer, rel)
		}
	}
}

func TestCheckNoRelease(t *testing.T) {
	f := newFakeGitHub(t)
	if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("404: err = %v, want ErrNoRelease", err)
	}
	for _, mod := range []func(m map[string]any){
		func(m map[string]any) { m["draft"] = true },
		func(m map[string]any) { m["prerelease"] = true },
		func(m map[string]any) { m["tag_name"] = "v2.0.0-rc.1" },
	} {
		m := latestJSON("v2.0.0")
		mod(m)
		f.setLatest(t, m)
		if rel, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); !errors.Is(err, ErrNoRelease) {
			t.Errorf("%v: rel=%+v err=%v, want ErrNoRelease", m, rel, err)
		}
	}
}

func TestCheckRejectsBadData(t *testing.T) {
	f := newFakeGitHub(t)
	m := latestJSON("v1.0.0")
	m["tag_name"] = "latest-and-greatest"
	f.setLatest(t, m)
	if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); err == nil {
		t.Error("non-version tag accepted")
	}
	if _, _, err := Check(context.Background(), f.client(), "../../evil", "v1.0.0"); err == nil {
		t.Error("bad repo accepted")
	}

	m = latestJSON("v1.1.0")
	m["html_url"] = "javascript:alert(1)"
	f.setLatest(t, m)
	rel, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if rel.URL != "" {
		t.Errorf("URL = %q, want it dropped", rel.URL)
	}
}

func TestCheckCapsNotes(t *testing.T) {
	f := newFakeGitHub(t)
	m := latestJSON("v1.1.0")
	m["body"] = strings.Repeat("é", maxNotes) // 2 bytes each
	f.setLatest(t, m)
	rel, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(rel.Notes) > maxNotes || len(rel.Notes) < maxNotes-4 || !utf8.ValidString(rel.Notes) {
		t.Errorf("notes: %d bytes, valid UTF-8 %v", len(rel.Notes), utf8.ValidString(rel.Notes))
	}
}

func TestCheckLimitsResponseSize(t *testing.T) {
	f := newFakeGitHub(t)
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		w.Write([]byte(`{"tag_name":"v1.1.0","body":"`))
		w.Write([]byte(strings.Repeat("x", maxAPIResponse)))
		w.Write([]byte(`"}`))
		return true
	})
	if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); !errors.Is(err, errTooLarge) {
		t.Errorf("err = %v, want too large", err)
	}
}

func TestCheckStatusErrors(t *testing.T) {
	f := newFakeGitHub(t)
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1792000000")
		w.WriteHeader(http.StatusForbidden)
		return true
	})
	if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); !errContains(err, "rate limit") {
		t.Errorf("err = %v, want rate limit", err)
	}
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusBadGateway)
		return true
	})
	if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); !errContains(err, "502") {
		t.Errorf("err = %v, want 502", err)
	}
}

func TestCheckHonoursContext(t *testing.T) {
	f := newFakeGitHub(t)
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := Check(ctx, f.client(), DefaultRepo, "v1.0.0")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s", d)
	}
}

func TestCheckRedirects(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.1.0"))
	var target atomic.Value
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/repos/"+DefaultRepo+"/releases/latest" {
			http.Redirect(w, r, target.Load().(string), http.StatusMovedPermanently)
			return true
		}
		return false
	})
	for _, bad := range []string{
		"http://api.github.com/repos/" + DefaultRepo + "/releases/latest",
		"https://evil.example.com/repos/" + DefaultRepo + "/releases/latest",
		"https://github.com/repos/" + DefaultRepo + "/releases/latest",
		"https://api.github.com:8443/x",
	} {
		target.Store(bad)
		if _, _, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0"); err == nil {
			t.Errorf("redirect to %s followed", bad)
		}
	}
	if n := f.hitCount("evil.example.com/repos/" + DefaultRepo + "/releases/latest"); n != 0 {
		t.Errorf("disallowed host contacted %d times", n)
	}
}

func TestCheckFollowsAllowedRedirect(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.1.0"))
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		switch r.URL.Path {
		case "/repos/" + DefaultRepo + "/releases/latest":
			http.Redirect(w, r, "https://api.github.com/repositories/1/releases/latest", http.StatusMovedPermanently)
			return true
		case "/repositories/1/releases/latest":
			w.Write([]byte(`{"tag_name":"v1.1.0"}`))
			return true
		}
		return false
	})
	rel, newer, err := Check(context.Background(), f.client(), DefaultRepo, "v1.0.0")
	if err != nil || !newer || rel.Version != "v1.1.0" {
		t.Errorf("rel=%+v newer=%v err=%v", rel, newer, err)
	}
}

func TestCheckDoesNotModifyClient(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.1.0"))
	c := f.client()
	if _, _, err := Check(context.Background(), c, DefaultRepo, "v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if c.CheckRedirect != nil {
		t.Error("caller's client was modified")
	}
}
