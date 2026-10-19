package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The plugin's private store (the host allows 1000 keys, 1 MiB in all and
// 256 KiB per value) holds two kinds of keys:
//
//	want-00 … want-15  the sources that galleries asked for, one per slot:
//	                   {"s": "c:UC…", "seen": unix}. A gallery is rendered
//	                   while the page is built, without network access, so it
//	                   registers its source here and the hourly run fetches
//	                   it. Each key has one source, so two galleries never
//	                   overwrite each other's registration.
//	feed-<hash>        the last good list of one source and how its fetches
//	                   went (record), written only by the background runs.
//
// The default source of the settings needs no slot: every run fetches it.
// A slot not rendered for 30 days is freed with its record.
const (
	slots     = 16
	keepFor   = 30 * 24 * time.Hour // YouTube's limit for stored API data, too
	seenEvery = 24 * time.Hour      // a render refreshes "seen" at most daily
	maxRecord = 64 << 10
)

// now is replaced in tests.
var now = time.Now

type want struct {
	S    string `json:"s"`
	Seen int64  `json:"seen"`
}

// record is what the plugin knows about one source.
type record struct {
	S       string  `json:"s"`
	Title   string  `json:"title,omitempty"`
	Author  string  `json:"author,omitempty"`
	Channel string  `json:"channel,omitempty"` // channel id (handles, playlists)
	Uploads string  `json:"uploads,omitempty"` // uploads playlist (Data API)
	Meta    int64   `json:"meta,omitempty"`    // when the name and uploads were read (Data API)
	Videos  []Video `json:"videos,omitempty"`
	Via     string  `json:"via,omitempty"`     // "feed" or "api"
	Fetched int64   `json:"fetched,omitempty"` // last good fetch
	Tried   int64   `json:"tried,omitempty"`   // last attempt
	Fails   int     `json:"fails,omitempty"`   // failed attempts in a row
	Err     string  `json:"err,omitempty"`     // the last error
}

func slotKey(i int) string { return fmt.Sprintf("want-%02d", i) }

func recordKey(source string) string {
	sum := sha256.Sum256([]byte(source))
	return "feed-" + hex.EncodeToString(sum[:8])
}

// firstSlot spreads sources over the slots, so that registering one is
// usually a single read.
func firstSlot(source string) int {
	h := fnv.New32a()
	h.Write([]byte(source))
	return int(h.Sum32() % slots)
}

func getJSON(key string, v any) (bool, error) {
	raw, err := pluginsdk.KVGet(key)
	if errors.Is(err, pluginsdk.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if json.Unmarshal(raw, v) != nil {
		return false, nil // unreadable: treated as absent and overwritten
	}
	return true, nil
}

func loadRecord(source string) (record, bool, error) {
	var r record
	ok, err := getJSON(recordKey(source), &r)
	if ok && r.S != source { // a hash collision: not this source's record
		return record{}, false, nil
	}
	return r, ok, err
}

// saveRecord stores r, dropping its oldest videos if it is too large.
func saveRecord(r record) error {
	for {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if len(b) <= maxRecord || len(r.Videos) == 0 {
			return pluginsdk.KVSet(recordKey(r.S), b)
		}
		r.Videos = r.Videos[:len(r.Videos)-1]
	}
}

// register records that a gallery shows source. It reports false when all
// slots are taken by other sources.
func register(source string) (bool, error) {
	start := firstSlot(source)
	free := -1
	for i := range slots {
		k := slotKey((start + i) % slots)
		var w want
		ok, err := getJSON(k, &w)
		if err != nil {
			return false, err
		}
		switch {
		case ok && w.S == source:
			if now().Sub(time.Unix(w.Seen, 0)) < seenEvery {
				return true, nil
			}
			return true, setJSON(k, want{S: source, Seen: now().Unix()})
		case !ok && free < 0:
			free = (start + i) % slots
			// Keep looking: the source may sit in a later slot if this one
			// was freed after it registered.
		}
	}
	if free < 0 {
		return false, nil
	}
	return true, setJSON(slotKey(free), want{S: source, Seen: now().Unix()})
}

func setJSON(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return pluginsdk.KVSet(key, b)
}

// registered lists the sources in the slots, with their slot keys.
func registered() (map[string]string, map[string]int64, error) {
	keys, seen := map[string]string{}, map[string]int64{}
	for i := range slots {
		var w want
		ok, err := getJSON(slotKey(i), &w)
		if err != nil {
			return nil, nil, err
		}
		if _, valid := SourceFromKey(w.S); ok && valid {
			if _, dup := keys[w.S]; !dup {
				keys[w.S], seen[w.S] = slotKey(i), w.Seen
				continue
			}
		}
		if ok { // invalid or duplicate: free the slot
			_ = pluginsdk.KVDelete(slotKey(i))
		}
	}
	return keys, seen, nil
}
