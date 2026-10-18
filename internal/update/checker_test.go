package update

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCheckerRun(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.1.0"))
	var logs syncBuffer
	c := NewChecker("v1.1.0", f.client(), slog.New(slog.NewTextHandler(&logs, nil)))
	c.Disabled = false
	c.Interval, c.Jitter = 20*time.Millisecond, 0

	if rel, newer, at := c.Latest(); rel != nil || newer || !at.IsZero() {
		t.Fatal("result before any check")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Readers race with the checker on purpose (go test -race).
	stop := make(chan struct{})
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
				c.Latest()
				c.LastError()
			}
		}
	})

	waitFor(t, "first check", func() bool { _, _, at := c.Latest(); return !at.IsZero() })
	rel, newer, _ := c.Latest()
	if rel == nil || rel.Version != "v1.1.0" || newer {
		t.Errorf("first check: %+v newer=%v", rel, newer)
	}
	rel.Assets[0].Name = "mutated" // a copy: must not affect the cache

	f.setLatest(t, latestJSON("v1.2.0"))
	waitFor(t, "newer release", func() bool { _, newer, _ := c.Latest(); return newer })
	rel, _, _ = c.Latest()
	if rel.Version != "v1.2.0" || rel.Assets[0].Name != "SHA256SUMS" {
		t.Errorf("second check: %+v", rel)
	}
	close(stop)
	readers.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !bytes.Contains([]byte(logs.String()), []byte("update available")) {
		t.Errorf("no update log line in %q", logs.String())
	}
}

func TestCheckerDisabled(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.2.0"))
	t.Setenv(EnvCheck, "false")
	c := NewChecker("v1.0.0", f.client(), nil)
	if !c.Disabled {
		t.Fatal("SAP13NS_UPDATE_CHECK=false did not disable the checker")
	}
	c.Jitter = 0
	done := make(chan struct{})
	go func() { c.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("disabled Run did not return")
	}
	if n := f.totalHits(); n != 0 {
		t.Errorf("%d requests while disabled", n)
	}
}

func TestEnabledFromEnv(t *testing.T) {
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false, "FALSE": false, "nonsense": true} {
		t.Setenv(EnvCheck, v)
		if got := EnabledFromEnv(); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

type panicTransport struct{}

func (panicTransport) RoundTrip(*http.Request) (*http.Response, error) { panic("boom") }

func TestCheckerSurvivesPanics(t *testing.T) {
	c := &Checker{Current: "v1.0.0", Client: &http.Client{Transport: panicTransport{}}, Interval: 10 * time.Millisecond}
	if err := c.CheckNow(context.Background()); !errContains(err, "panicked") {
		t.Errorf("err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c.Run(ctx) // must return normally
	if c.LastError() == nil {
		t.Error("LastError not set")
	}
}

func TestCheckerKeepsLastGoodResult(t *testing.T) {
	f := newFakeGitHub(t)
	f.setLatest(t, latestJSON("v1.2.0"))
	c := &Checker{Current: "v1.0.0", Client: f.client()}
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _, first := c.Latest()
	f.setOverride(func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusInternalServerError)
		return true
	})
	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("500 not reported")
	}
	rel, newer, at := c.Latest()
	if rel == nil || rel.Version != "v1.2.0" || !newer || !at.Equal(first) {
		t.Errorf("cached result lost: %+v %v %v", rel, newer, at)
	}
	if c.LastError() == nil {
		t.Error("LastError not set")
	}

	// No published release: the cache is cleared, which is not an error.
	f.setOverride(nil)
	f.mu.Lock()
	f.latest = nil
	f.mu.Unlock()
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rel, newer, at := c.Latest(); rel != nil || newer || at.IsZero() || c.LastError() != nil {
		t.Errorf("after 404: %+v %v %v %v", rel, newer, at, c.LastError())
	}
}
