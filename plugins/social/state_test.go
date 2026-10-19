package main

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
)

// The records, the queue and the lock are autopost's (tested there); these
// tests pin down what the plugin stores and how it explains a busy lock.

func TestStoredRecordFormat(t *testing.T) {
	env, _ := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	want := `{"12":{"t":` + strconv.FormatInt(t0.Unix(), 10) + `,"b":"d1","m":"d1"}}`
	if got := string(env.KV[autopost.KeyRecords]); got != want {
		t.Errorf("stored %s, want %s", got, want)
	}
}

func TestRecordsOfEarlierVersionsAreRead(t *testing.T) {
	env, net := setup(t)
	env.KV[autopost.KeyRecords] = []byte(`{"12":{"t":1760000000,"b":"d1","m":"d1"},"13":{"t":1760000000,"m":"f3"}}`)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(net.reqs) != 0 {
		t.Errorf("sent again: %+v", net.reqs)
	}
	wantLog(t, env, "already posted to Bluesky and Mastodon; it is never posted twice")
	if r := records(t, env)["13"]; r.M != "f3" || r.B != "" {
		t.Errorf("record 13: %+v", r)
	}
}

func TestLockBusy(t *testing.T) {
	env, _ := setup(t)
	sleep = func(time.Duration) {
		env.KV[autopost.KeyLock] = []byte("other " + strconv.FormatInt(now().UnixMilli(), 10))
	}
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "could not be queued (another event is updating the plugin's records); it will not be posted")
	if _, ok := env.KV[autopost.KeyQueue]; ok {
		t.Error("queued although the lock was busy")
	}
}

func TestQueueFullIsLogged(t *testing.T) {
	env, _ := setup(t)
	var q []autopost.Item
	for i := range autopost.MaxQueue {
		q = append(q, autopost.Item{ID: int64(1000 + i), Title: "Vieja " + strconv.Itoa(i), At: t0.Add(time.Hour).Unix(), Since: int64(i)})
	}
	b, _ := json.Marshal(q)
	env.KV[autopost.KeyQueue] = b
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "the queue is full (100 posts): post 1000 “Vieja 0” was dropped and will not be posted")
	if len(queued(t, env)) != autopost.MaxQueue || autopost.FindItem(queued(t, env), 12) == nil {
		t.Error("the new post was not queued")
	}
}
