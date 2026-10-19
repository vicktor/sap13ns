package autopost

import (
	"bytes"
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

// The poster keeps three keys in the plugin's private store (the host
// allows 1000 keys and 1 MiB per plugin, 256 KiB per value):
//
//	posted  what was sent where, for every post: {"<id>": {"t": unix, "<network key>": "d1", …}}
//	queue   posts waiting to go out: scheduled ones and retries
//	lock    "<token> <unix ms>" while an event updates the other two
const (
	KeyRecords = "posted"
	KeyQueue   = "queue"
	KeyLock    = "lock"

	MaxRecords     = 3000
	MaxRecordBytes = 200 << 10
	MaxQueue       = 100
	MaxAttempts    = 3
	lockTTL        = 30 * time.Second
	QueueGrace     = 48 * time.Hour // how long a queued post may stay invisible
)

// Delivery states, one letter followed by the number of attempts ("d1").
const (
	StateSending = 's' // request sent, outcome unknown: never retried
	StateDone    = 'd'
	StateFailed  = 'f' // the server refused, or nothing was sent: retried
)

// Record is what was sent for one post: the time of the last change and
// one state per network, stored under the network's key.
type Record struct {
	T int64             // last change, unix seconds
	S map[string]string // network key → state ("d1")
}

// Get returns the state of one network (0 when never tried) and the
// number of attempts.
func (r Record) Get(net string) (byte, int) {
	s := r.S[net]
	if s == "" {
		return 0, 0
	}
	n, _ := strconv.Atoi(s[1:])
	return s[0], n
}

// Set records the state of one network.
func (r *Record) Set(net string, state byte, attempts int) {
	if r.S == nil {
		r.S = map[string]string{}
	}
	r.S[net] = string(state) + strconv.Itoa(attempts)
}

// MarshalJSON writes {"t": unix, "<key>": "<state>", …}, keys sorted.
func (r Record) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`{"t":` + strconv.FormatInt(r.T, 10))
	keys := make([]string, 0, len(r.S))
	for k := range r.S {
		if k != "t" && r.S[k] != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(r.S[k])
		b.WriteByte(',')
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// UnmarshalJSON reads what MarshalJSON writes.
func (r *Record) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	*r = Record{}
	for k, v := range m {
		if k == "t" {
			if err := json.Unmarshal(v, &r.T); err != nil {
				return err
			}
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			if r.S == nil {
				r.S = map[string]string{}
			}
			r.S[k] = s
		}
	}
	return nil
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

// LoadRecords reads what was sent where.
func LoadRecords() (map[string]Record, error) {
	recs := map[string]Record{}
	raw, err := pluginsdk.KVGet(KeyRecords)
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
	if len(recs) > MaxRecords || len(b) > MaxRecordBytes {
		ids := make([]string, 0, len(recs))
		for id := range recs {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return recs[ids[i]].T < recs[ids[j]].T })
		for _, id := range ids {
			if len(recs) <= MaxRecords && len(b) <= MaxRecordBytes {
				break
			}
			delete(recs, id)
			if len(recs)%100 == 0 {
				b, _ = json.Marshal(recs)
			}
		}
		b, _ = json.Marshal(recs)
	}
	return pluginsdk.KVSet(KeyRecords, b)
}

// LoadQueue reads the queue.
func LoadQueue() ([]Item, error) {
	var q []Item
	raw, err := pluginsdk.KVGet(KeyQueue)
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
		return pluginsdk.KVDelete(KeyQueue)
	}
	b, _ := json.Marshal(q)
	return pluginsdk.KVSet(KeyQueue, b)
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
	if len(*q) >= MaxQueue {
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

// FindItem returns the queued item of a post, or nil.
func FindItem(q []Item, id int64) *Item {
	for i := range q {
		if q[i].ID == id {
			return &q[i]
		}
	}
	return nil
}

// ErrBusy is returned when the lock stays taken.
var ErrBusy = errors.New("another event is updating the plugin's records")

// update runs fn on the records and the queue while holding the lock, and
// saves both. Two event workers may run at once; without the lock, one
// could overwrite what the other wrote and a post could go out twice.
func (p *Poster) update(fn func(recs map[string]Record, q *[]Item)) error {
	token, err := p.acquireLock()
	if err != nil {
		return err
	}
	defer p.releaseLock(token)
	recs, err := LoadRecords()
	if err != nil {
		return err
	}
	q, err := LoadQueue()
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
func (p *Poster) acquireLock() (string, error) {
	token := newToken()
	for range 20 {
		v, err := pluginsdk.KVGet(KeyLock)
		switch {
		case errors.Is(err, pluginsdk.ErrNotFound):
		case err != nil:
			return "", err
		case p.lockHeld(string(v)):
			p.sleep(150 * time.Millisecond)
			continue
		}
		if err := pluginsdk.KVSet(KeyLock, []byte(token+" "+strconv.FormatInt(p.now().UnixMilli(), 10))); err != nil {
			return "", err
		}
		p.sleep(250 * time.Millisecond)
		v, err = pluginsdk.KVGet(KeyLock)
		if err == nil && strings.HasPrefix(string(v), token+" ") {
			return token, nil
		}
		if err != nil && !errors.Is(err, pluginsdk.ErrNotFound) {
			return "", err
		}
	}
	return "", ErrBusy
}

func (p *Poster) lockHeld(v string) bool {
	_, ms, ok := strings.Cut(v, " ")
	t, err := strconv.ParseInt(ms, 10, 64)
	return ok && err == nil && p.now().Sub(time.UnixMilli(t)) < lockTTL
}

func (p *Poster) releaseLock(token string) {
	if v, err := pluginsdk.KVGet(KeyLock); err == nil && strings.HasPrefix(string(v), token+" ") {
		pluginsdk.KVDelete(KeyLock)
	}
}

func newToken() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
