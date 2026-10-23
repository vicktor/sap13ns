package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	maxRetryWait = 2 * time.Second
	os.Exit(m.Run())
}

// A passing failure is tried again; a lasting one, or a rate limit asking
// for a long wait, is reported at once.
func TestRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status []int // per try; the last repeats
		header string
		body   string
		tries  int32
		ok     bool
	}{
		{"overloaded, then fine", []int{529, 200}, "", "", 2, true},
		{"unavailable twice, then fine", []int{503, 502, 200}, "", "", 3, true},
		{"always unavailable", []int{503}, "", "", 3, false},
		{"rate limited briefly", []int{429, 200}, "1", "", 2, true},
		{"rate limited for long", []int{429}, "120", "", 1, false},
		{"quota exhausted", []int{429}, "", `{"error":{"type":"insufficient_quota","message":"You exceeded your current quota"}}`, 1, false},
		{"bad request", []int{400}, "", "", 1, false},
		{"bad key", []int{401}, "", "", 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				i := int(n.Add(1)) - 1
				st := tc.status[min(i, len(tc.status)-1)]
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(st)
				if st == 200 {
					w.Write([]byte(`{"choices":[{"message":{"content":"hola"},"finish_reason":"stop"}]}`))
					return
				}
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			p, err := New(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, AllowInsecureLocal: true, TextModel: "m"}, srv.Client())
			if err != nil {
				t.Fatal(err)
			}
			res, err := p.Text(context.Background(), TextRequest{Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			if got := n.Load(); got != tc.tries {
				t.Errorf("tries: %d, want %d", got, tc.tries)
			}
			if tc.ok != (err == nil) {
				t.Fatalf("err: %v", err)
			}
			if tc.ok && res.Text != "hola" {
				t.Errorf("text: %q", res.Text)
			}
			var ae *APIError
			if !tc.ok && !errors.As(err, &ae) {
				t.Errorf("not an APIError: %v", err)
			}
		})
	}
}
