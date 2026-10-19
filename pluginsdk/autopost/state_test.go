package autopost

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLockContention(t *testing.T) {
	h := setup(t)
	// Another worker writes its token between our write and our read back,
	// then holds the lock for one wait.
	var waits []time.Duration
	p := &Poster{Now: func() time.Time { return h.clock }, Sleep: func(d time.Duration) {
		waits = append(waits, d)
		switch len(waits) {
		case 1:
			h.env.KV[KeyLock] = []byte("other " + strconv.FormatInt(h.clock.UnixMilli(), 10))
		case 2:
			delete(h.env.KV, KeyLock) // the other worker is done
		}
	}}
	err := p.update(func(recs map[string]Record, q *[]Item) {
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
	if _, ok := h.env.KV[KeyLock]; ok {
		t.Error("lock not released")
	}
	if h.record(t, "1").T != 1 {
		t.Error("not saved")
	}
}

func TestLockBusy(t *testing.T) {
	h := setup(t)
	p := &Poster{Now: func() time.Time { return h.clock }, Sleep: func(time.Duration) {
		h.env.KV[KeyLock] = []byte("other " + strconv.FormatInt(h.clock.UnixMilli(), 10))
	}}
	ran := false
	err := p.update(func(map[string]Record, *[]Item) { ran = true })
	if !errors.Is(err, ErrBusy) || ran {
		t.Fatalf("err %v, ran %v", err, ran)
	}
	if !strings.HasPrefix(string(h.env.KV[KeyLock]), "other ") {
		t.Error("someone else's lock was released")
	}
}

func TestStaleLockIsTaken(t *testing.T) {
	h := setup(t)
	h.env.KV[KeyLock] = []byte("crashed " + strconv.FormatInt(t0.Add(-time.Minute).UnixMilli(), 10))
	waits := 0
	p := &Poster{Now: func() time.Time { return h.clock }, Sleep: func(time.Duration) { waits++ }}
	if err := p.update(func(map[string]Record, *[]Item) {}); err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Errorf("waited %d times", waits)
	}
}

func TestSaveRecordsKeepsTheNewest(t *testing.T) {
	h := setup(t)
	recs := map[string]Record{}
	for i := range MaxRecords + 50 {
		recs[strconv.Itoa(i)] = Record{T: int64(i), S: map[string]string{"b": "d1", "m": "d1"}}
	}
	if err := saveRecords(recs); err != nil {
		t.Fatal(err)
	}
	got, err := LoadRecords()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > MaxRecords || len(h.env.KV[KeyRecords]) > MaxRecordBytes {
		t.Fatalf("%d records, %d bytes", len(got), len(h.env.KV[KeyRecords]))
	}
	if _, ok := got["0"]; ok {
		t.Error("the oldest record was kept")
	}
	if _, ok := got[strconv.Itoa(MaxRecords+49)]; !ok {
		t.Error("the newest record was dropped")
	}
}

func TestUnreadableStateIsReset(t *testing.T) {
	h := setup(t)
	h.env.KV[KeyRecords] = []byte("{not json")
	h.env.KV[KeyQueue] = []byte("[1,2")
	recs, err := LoadRecords()
	if err != nil || len(recs) != 0 {
		t.Errorf("%v, %v", recs, err)
	}
	q, err := LoadQueue()
	if err != nil || len(q) != 0 {
		t.Errorf("%v, %v", q, err)
	}
	h.wantLog(t, "the record of sent posts was unreadable and has been reset", "the queue was unreadable and has been reset")
}

func TestEnqueue(t *testing.T) {
	var q []Item
	for i := range MaxQueue {
		if d := enqueue(&q, Item{ID: int64(i), Since: int64(i)}); d != nil {
			t.Fatalf("dropped %+v", d)
		}
	}
	// Replacing keeps the original queueing time.
	if d := enqueue(&q, Item{ID: 5, Title: "new", Since: 999}); d != nil || FindItem(q, 5).Since != 5 || FindItem(q, 5).Title != "new" {
		t.Fatalf("replace: %+v %+v", d, FindItem(q, 5))
	}
	d := enqueue(&q, Item{ID: 1000, Since: 1000})
	if d == nil || d.ID != 0 || len(q) != MaxQueue || FindItem(q, 1000) == nil {
		t.Fatalf("dropped %+v, len %d", d, len(q))
	}
	if !dequeue(&q, 1000) || dequeue(&q, 1000) || len(q) != MaxQueue-1 {
		t.Fatalf("dequeue: len %d", len(q))
	}
}
