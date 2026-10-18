package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The plugin keeps three keys in its private store (the host allows 1000
// keys and 1 MiB per plugin, 256 KiB per value):
//
//	posted  what was sent where, for every post: {"<id>": {"t": unix, "b": "d1", "m": "f2"}}
//	queue   posts waiting to go out: scheduled ones and retries
//	lock    "<token> <unix ms>" while an event updates the other two
const (
	keyRecords = "posted"
	keyQueue   = "queue"
	keyLock    = "lock"

	maxRecords     = 3000
	maxRecordBytes = 200 << 10
	maxQueue       = 100
	maxAttempts    = 3
	lockTTL        = 30 * time.Second
	queueGrace     = 48 * time.Hour // how long a queued post may stay invisible
)

// Delivery states, one letter followed by the number of attempts ("d1").
const (
	stateSending = 's' // request sent, outcome unknown: never retried
	stateDone    = 'd'
	stateFailed  = 'f' // the server refused, or nothing was sent: retried
)

// Record is what was sent for one post.
type Record struct {
	T int64  `json:"t"`           // last change, unix seconds
	B string `json:"b,omitempty"` // Bluesky state
	M string `json:"m,omitempty"` // Mastodon state
}

func (r *Record) slot(net string) *string {
	if net == netBluesky {
		return &r.B
	}
	return &r.M
}

// Get returns the state of one network (0 when never tried) and the
// number of attempts.
func (r Record) Get(net string) (byte, int) {
	s := *r.slot(net)
	if s == "" {
		return 0, 0
	}
	n, _ := strconv.Atoi(s[1:])
	return s[0], n
}

// Set records the state of one network.
func (r *Record) Set(net string, state byte, attempts int) {
	*r.slot(net) = string(state) + strconv.Itoa(attempts)
}

// Item is a post waiting in the queue.
type Item struct {
	ID    int64  `json:"id"`
	Type  string `json:"type,omitempty"`
	Lang  string `json:"lang,omitempty"`
	Title string `json:"title,omitempty"`
	At    int64  `json:"at"`    // due from (unix seconds)
	Since int64  `json:"since"` // queued at
}

// Test hooks.
var (
	now   = time.Now
	sleep = time.Sleep
)

func loadRecords() (map[string]Record, error) {
	recs := map[string]Record{}
	raw, err := pluginsdk.KVGet(keyRecords)
	switch {
	case errors.Is(err, pluginsdk.ErrNotFound):
		return recs, nil
	case err != nil:
		return nil, err
	}
	if json.Unmarshal(raw, &recs) != nil {
		pluginsdk.Log(pluginsdk.Warn, "the record of sent posts was unreadable and has been reset")
		recs = map[string]Record{}
	}
	return recs, nil
}

// saveRecords keeps the newest records within the size caps.
func saveRecords(recs map[string]Record) error {
	b, _ := json.Marshal(recs)
	if len(recs) > maxRecords || len(b) > maxRecordBytes {
		ids := make([]string, 0, len(recs))
		for id := range recs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return recs[ids[i]].T < recs[ids[j]].T })
		for _, id := range ids {
			if len(recs) <= maxRecords && len(b) <= maxRecordBytes {
				break
			}
			delete(recs, id)
			if len(recs)%100 == 0 {
				b, _ = json.Marshal(recs)
			}
		}
		b, _ = json.Marshal(recs)
	}
	return pluginsdk.KVSet(keyRecords, b)
}

func loadQueue() ([]Item, error) {
	var q []Item
	raw, err := pluginsdk.KVGet(keyQueue)
	switch {
	case errors.Is(err, pluginsdk.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	if json.Unmarshal(raw, &q) != nil {
		pluginsdk.Log(pluginsdk.Warn, "the queue was unreadable and has been reset")
		q = nil
	}
	return q, nil
}

func saveQueue(q []Item) error {
	if len(q) == 0 {
		return pluginsdk.KVDelete(keyQueue)
	}
	b, _ := json.Marshal(q)
	return pluginsdk.KVSet(keyQueue, b)
}

// enqueue adds or replaces the item for a post. It reports an item that
// had to be dropped to make room.
func enqueue(q *[]Item, it Item) (dropped *Item) {
	for i := range *q {
		if (*q)[i].ID == it.ID {
			it.Since = (*q)[i].Since
			(*q)[i] = it
			return nil
		}
	}
	if len(*q) >= maxQueue {
		sort.Slice(*q, func(i, j int) bool { return (*q)[i].Since < (*q)[j].Since })
		d := (*q)[0]
		*q = (*q)[1:]
		dropped = &d
	}
	*q = append(*q, it)
	return dropped
}

// dequeue removes the item of a post and reports whether it was there.
func dequeue(q *[]Item, id int64) bool {
	for i := range *q {
		if (*q)[i].ID == id {
			*q = append((*q)[:i], (*q)[i+1:]...)
			return true
		}
	}
	return false
}

func findItem(q []Item, id int64) *Item {
	for i := range q {
		if q[i].ID == id {
			return &q[i]
		}
	}
	return nil
}

var errBusy = errors.New("another event is updating the plugin's records")

// update runs fn on the records and the queue while holding the lock, and
// saves both. Two event workers may run at once; without the lock, one
// could overwrite what the other wrote and a post could go out twice.
func update(fn func(recs map[string]Record, q *[]Item)) error {
	token, err := acquireLock()
	if err != nil {
		return err
	}
	defer releaseLock(token)
	recs, err := loadRecords()
	if err != nil {
		return err
	}
	q, err := loadQueue()
	if err != nil {
		return err
	}
	fn(recs, &q)
	if err := saveRecords(recs); err != nil {
		return err
	}
	return saveQueue(q)
}

// acquireLock takes the "lock" key. The store has no compare-and-swap, so
// a worker writes its random token, waits, and reads it back: when two
// workers race, both writes land before either reads, and only the last
// writer still finds its token. A lock older than lockTTL is stale (the
// host stops an event call after 15 seconds).
func acquireLock() (string, error) {
	token := newToken()
	for range 20 {
		v, err := pluginsdk.KVGet(keyLock)
		switch {
		case errors.Is(err, pluginsdk.ErrNotFound):
		case err != nil:
			return "", err
		case lockHeld(string(v)):
			sleep(150 * time.Millisecond)
			continue
		}
		if err := pluginsdk.KVSet(keyLock, []byte(token+" "+strconv.FormatInt(now().UnixMilli(), 10))); err != nil {
			return "", err
		}
		sleep(250 * time.Millisecond)
		v, err = pluginsdk.KVGet(keyLock)
		if err == nil && strings.HasPrefix(string(v), token+" ") {
			return token, nil
		}
		if err != nil && !errors.Is(err, pluginsdk.ErrNotFound) {
			return "", err
		}
	}
	return "", errBusy
}

func lockHeld(v string) bool {
	_, ms, ok := strings.Cut(v, " ")
	t, err := strconv.ParseInt(ms, 10, 64)
	return ok && err == nil && now().Sub(time.UnixMilli(t)) < lockTTL
}

func releaseLock(token string) {
	if v, err := pluginsdk.KVGet(keyLock); err == nil && strings.HasPrefix(string(v), token+" ") {
		pluginsdk.KVDelete(keyLock)
	}
}

var newToken = func() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
