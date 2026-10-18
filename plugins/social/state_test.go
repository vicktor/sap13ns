package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func TestRecordStates(t *testing.T) {
	var r Record
	if st, n := r.Get(netBluesky); st != 0 || n != 0 {
		t.Errorf("empty record: %c %d", st, n)
	}
	r.Set(netBluesky, stateDone, 1)
	r.Set(netMastodon, stateFailed, 2)
	if st, n := r.Get(netBluesky); st != stateDone || n != 1 {
		t.Errorf("bluesky: %c %d", st, n)
	}
	if st, n := r.Get(netMastodon); st != stateFailed || n != 2 {
		t.Errorf("mastodon: %c %d", st, n)
	}
	b, _ := json.Marshal(r)
	if string(b) != `{"t":0,"b":"d1","m":"f2"}` {
		t.Errorf("json %s", b)
	}
}

func TestLockContention(t *testing.T) {
	env, _ := setup(t)
	// Another worker writes its token between our write and our read back,
	// then holds the lock for one wait.
	var waits []time.Duration
	sleep = func(d time.Duration) {
		waits = append(waits, d)
		switch len(waits) {
		case 1:
			env.KV[keyLock] = []byte("other " + strconv.FormatInt(now().UnixMilli(), 10))
		case 2:
			delete(env.KV, keyLock) // the other worker is done
		}
	}
	err := update(func(recs map[string]Record, q *[]Item) {
		recs["1"] = Record{T: 1}
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{250 * time.Millisecond, 150 * time.Millisecond, 250 * time.Millisecond}
	if len(waits) != len(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
	if _, ok := env.KV[keyLock]; ok {
		t.Error("lock not released")
	}
	if records(t, env)["1"].T != 1 {
		t.Error("not saved")
	}
}

func TestLockBusy(t *testing.T) {
	env, _ := setup(t)
	sleep = func(time.Duration) {
		env.KV[keyLock] = []byte("other " + strconv.FormatInt(now().UnixMilli(), 10))
	}
	ran := false
	err := update(func(map[string]Record, *[]Item) { ran = true })
	if !errors.Is(err, errBusy) || ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if !strings.HasPrefix(string(env.KV[keyLock]), "other ") {
		t.Error("someone else's lock was released")
	}

	// The handler explains it and does not fail.
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "could not be queued (another event is updating the plugin's records); it will not be posted")
}

func TestStaleLockIsTaken(t *testing.T) {
	env, _ := setup(t)
	env.KV[keyLock] = []byte("crashed " + strconv.FormatInt(t0.Add(-time.Minute).UnixMilli(), 10))
	waits := 0
	sleep = func(time.Duration) { waits++ }
	if err := update(func(map[string]Record, *[]Item) {}); err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Errorf("waited %d times", waits)
	}
}

func TestSaveRecordsKeepsTheNewest(t *testing.T) {
	env, _ := setup(t)
	recs := map[string]Record{}
	for i := range maxRecords + 50 {
		recs[strconv.Itoa(i)] = Record{T: int64(i), B: "d1", M: "d1"}
	}
	if err := saveRecords(recs); err != nil {
		t.Fatal(err)
	}
	got := records(t, env)
	if len(got) > maxRecords || len(env.KV[keyRecords]) > maxRecordBytes {
		t.Fatalf("%d records, %d bytes", len(got), len(env.KV[keyRecords]))
	}
	if _, ok := got["0"]; ok {
		t.Error("the oldest record was kept")
	}
	if _, ok := got[strconv.Itoa(maxRecords+49)]; !ok {
		t.Error("the newest record was dropped")
	}
}

func TestUnreadableStateIsReset(t *testing.T) {
	env, _ := setup(t)
	env.KV[keyRecords] = []byte("{not json")
	env.KV[keyQueue] = []byte("[1,2")
	recs, err := loadRecords()
	if err != nil || len(recs) != 0 {
		t.Errorf("%v, %v", recs, err)
	}
	q, err := loadQueue()
	if err != nil || len(q) != 0 {
		t.Errorf("%v, %v", q, err)
	}
	wantLog(t, env, "the record of sent posts was unreadable and has been reset", "the queue was unreadable and has been reset")
}

func TestEnqueue(t *testing.T) {
	var q []Item
	for i := range maxQueue {
		if d := enqueue(&q, Item{ID: int64(i), Since: int64(i)}); d != nil {
			t.Fatalf("dropped %+v", d)
		}
	}
	// Replacing keeps the original queueing time.
	if d := enqueue(&q, Item{ID: 5, Title: "new", Since: 999}); d != nil || findItem(q, 5).Since != 5 || findItem(q, 5).Title != "new" {
		t.Fatalf("replace: %+v %+v", d, findItem(q, 5))
	}
	d := enqueue(&q, Item{ID: 1000, Since: 1000})
	if d == nil || d.ID != 0 || len(q) != maxQueue || findItem(q, 1000) == nil {
		t.Fatalf("dropped %+v, len %d", d, len(q))
	}
	if !dequeue(&q, 1000) || dequeue(&q, 1000) || len(q) != maxQueue-1 {
		t.Fatalf("dequeue: len %d", len(q))
	}
}

func TestQueueFullIsLogged(t *testing.T) {
	env, _ := setup(t)
	var q []Item
	for i := range maxQueue {
		q = append(q, Item{ID: int64(1000 + i), Title: "Vieja " + strconv.Itoa(i), At: t0.Add(time.Hour).Unix(), Since: int64(i)})
	}
	b, _ := json.Marshal(q)
	env.KV[keyQueue] = b
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "the queue is full (100 posts): post 1000 “Vieja 0” was dropped and will not be posted")
	if len(queued(t, env)) != maxQueue || findItem(queued(t, env), 12) == nil {
		t.Error("the new post was not queued")
	}
}
