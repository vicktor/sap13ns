package web

import (
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newTestStore(max int) (*ceremonyStore[string], *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	st := newCeremonyStore[string](max, 5*time.Minute)
	st.now = clock.now
	return st, clock
}

func TestCeremonyStoreSingleUse(t *testing.T) {
	st, _ := newTestStore(10)
	id := st.Put("challenge")
	if len(id) < 40 {
		t.Fatalf("id %q is too short to be unguessable", id)
	}
	v, ok := st.Take(id)
	if !ok || v != "challenge" {
		t.Fatalf("first Take = %q, %v; want the value", v, ok)
	}
	if _, ok := st.Take(id); ok {
		t.Fatal("second Take succeeded: a finish request could be replayed")
	}
	if st.Len() != 0 {
		t.Fatalf("Len = %d after Take, want 0", st.Len())
	}
	for _, bad := range []string{"", "nope", strings.Repeat("a", 43)} {
		if _, ok := st.Take(bad); ok {
			t.Fatalf("Take(%q) succeeded", bad)
		}
	}
}

func TestCeremonyStoreExpiry(t *testing.T) {
	st, clock := newTestStore(10)
	fresh := st.Put("fresh")
	old := st.Put("old")
	clock.add(4*time.Minute + 59*time.Second)
	if _, ok := st.Take(fresh); !ok {
		t.Fatal("entry expired before its TTL")
	}
	clock.add(time.Second)
	if _, ok := st.Take(old); ok {
		t.Fatal("expired entry was accepted")
	}
	if st.Len() != 0 {
		t.Fatalf("expired entry still stored after Take (Len = %d)", st.Len())
	}
}

func TestCeremonyStoreBound(t *testing.T) {
	st, clock := newTestStore(3)
	var ids []string
	for i := range 5 {
		ids = append(ids, st.Put(string(rune('a'+i))))
		clock.add(time.Second) // distinct expiry times: oldest first
	}
	if st.Len() != 3 {
		t.Fatalf("Len = %d, want the bound 3", st.Len())
	}
	if st.evicted != 2 {
		t.Fatalf("evicted = %d, want 2", st.evicted)
	}
	for _, id := range ids[:2] {
		if _, ok := st.Take(id); ok {
			t.Fatal("one of the oldest entries survived eviction")
		}
	}
	for i, id := range ids[2:] {
		if v, ok := st.Take(id); !ok || v != string(rune('c'+i)) {
			t.Fatalf("recent entry %d lost (%q, %v)", i, v, ok)
		}
	}
}

func TestCeremonyStorePurgesExpiredBeforeEvicting(t *testing.T) {
	st, clock := newTestStore(3)
	for range 3 {
		st.Put("stale")
	}
	clock.add(6 * time.Minute)
	keep := st.Put("new")
	if st.Len() != 1 || st.evicted != 0 {
		t.Fatalf("Len = %d, evicted = %d; want expired entries purged (1, 0)", st.Len(), st.evicted)
	}
	if _, ok := st.Take(keep); !ok {
		t.Fatal("new entry missing")
	}
}

func TestCeremonyStoreConcurrent(t *testing.T) {
	st := newCeremonyStore[int](64, time.Minute)
	var wg sync.WaitGroup
	var mu sync.Mutex
	taken := 0
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				id := st.Put(g*1000 + i)
				if _, ok := st.Take(id); ok {
					mu.Lock()
					taken++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if st.Len() > 64 {
		t.Fatalf("Len = %d exceeds the bound", st.Len())
	}
	if taken == 0 {
		t.Fatal("no ceremony could be taken")
	}
}

func TestCleanPasskeyName(t *testing.T) {
	cases := map[string]string{
		"  MacBook   Pro ":       "MacBook Pro",
		"line\nbreak\ttab":       "line break tab",
		"zero\u200bwidth":        "zero width",
		"bad\xffutf8":            "badutf8",
		strings.Repeat("é", 150): strings.Repeat("é", 100),
		"":                       "",
	}
	for in, want := range cases {
		if got := cleanPasskeyName(in); got != want {
			t.Errorf("cleanPasskeyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatAAGUID(t *testing.T) {
	b := []byte{0xea, 0x9b, 0x8d, 0x66, 0x4d, 0x01, 0x1d, 0x21, 0x3c, 0xe4, 0xb6, 0xb4, 0x8c, 0xb5, 0x75, 0xd4}
	if got := formatAAGUID(b); got != "ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4" {
		t.Fatalf("formatAAGUID = %q", got)
	}
	if knownAuthenticators[formatAAGUID(b)] != "Google Password Manager" {
		t.Fatal("known AAGUID not mapped")
	}
	if formatAAGUID(nil) != "" || formatAAGUID(make([]byte, 15)) != "" {
		t.Fatal("short AAGUID should format as empty")
	}
}
