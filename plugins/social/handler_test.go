package main

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

const (
	secretPassword = "app-pass-SECRET"
	secretToken    = "masto-token-SECRET"
	secretJWT      = "jwt-SECRET"
)

// fakeNet answers like Bluesky and Mastodon and records every request.
type fakeNet struct {
	reqs []pluginsdk.HTTPRequest
	// Per URL suffix: an HTTP status to answer, or an error.
	status map[string]int
	body   map[string]string
	err    map[string]error
}

func (f *fakeNet) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	for suffix, err := range f.err {
		if strings.HasSuffix(req.URL, suffix) {
			return nil, err
		}
	}
	for suffix, st := range f.status {
		if strings.HasSuffix(req.URL, suffix) {
			return &pluginsdk.HTTPResponse{Status: st, Body: f.body[suffix]}, nil
		}
	}
	switch {
	case strings.HasSuffix(req.URL, "/xrpc/com.atproto.server.createSession"):
		return &pluginsdk.HTTPResponse{Status: 200, Body: `{"accessJwt":"` + secretJWT + `","refreshJwt":"r","did":"did:plc:abc","handle":"alice.bsky.social"}`}, nil
	case strings.HasSuffix(req.URL, "/xrpc/com.atproto.repo.createRecord"):
		return &pluginsdk.HTTPResponse{Status: 200, Body: `{"uri":"at://did:plc:abc/app.bsky.feed.post/3kxyz","cid":"bafy"}`}, nil
	case strings.HasSuffix(req.URL, "/api/v1/statuses"):
		return &pluginsdk.HTTPResponse{Status: 200, Body: `{"id":"1","uri":"https://mastodon.social/users/alice/statuses/1","url":"https://mastodon.social/@alice/1"}`}, nil
	}
	return &pluginsdk.HTTPResponse{Status: 404}, nil
}

func (f *fakeNet) count(suffix string) int {
	n := 0
	for _, r := range f.reqs {
		if strings.HasSuffix(r.URL, suffix) {
			n++
		}
	}
	return n
}

func (f *fakeNet) last(suffix string) pluginsdk.HTTPRequest {
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(f.reqs[i].URL, suffix) {
			return f.reqs[i]
		}
	}
	return pluginsdk.HTTPRequest{}
}

// setup returns a fake host with both networks configured, a fixed clock
// and no waiting in the lock.
func setup(t *testing.T) (*sdktest.Env, *fakeNet) {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "social"
	env.Plugin.Settings = map[string]string{
		"bluesky_handle":       "@alice.bsky.social",
		"bluesky_app_password": secretPassword,
		"mastodon_instance":    "https://mastodon.social/",
		"mastodon_token":       secretToken,
	}
	net := &fakeNet{}
	env.HTTP = net.fetch
	setClock(t, t0)
	prevSleep := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = prevSleep })
	return env, net
}

func setClock(t *testing.T, at time.Time) {
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}

func samplePost() pluginsdk.Post {
	return pluginsdk.Post{
		ID: 12, Type: "post", Lang: "es", Title: "Hola, ¿qué tal?", Slug: "hola",
		URL: "https://example.test/es/hola", Excerpt: "Un saludo desde Málaga.",
		Tags: []string{"open source", "Go", "go"}, PublishedAt: t0.Add(-time.Minute).Format(time.RFC3339),
	}
}

func event(t *testing.T, env *sdktest.Env, name string, payload any) {
	t.Helper()
	if _, err := env.Event(name, payload); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func logText(env *sdktest.Env) string {
	var b strings.Builder
	for _, l := range env.Logs {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func wantLog(t *testing.T, env *sdktest.Env, subs ...string) {
	t.Helper()
	logs := logText(env)
	for _, s := range subs {
		if !strings.Contains(logs, s) {
			t.Errorf("log lacks %q:\n%s", s, logs)
		}
	}
}

func noSecrets(t *testing.T, env *sdktest.Env) {
	t.Helper()
	logs := logText(env)
	for _, s := range []string{secretPassword, secretToken, secretJWT} {
		if strings.Contains(logs, s) {
			t.Errorf("the log contains the secret %q:\n%s", s, logs)
		}
	}
}

func records(t *testing.T, env *sdktest.Env) map[string]Record {
	t.Helper()
	recs := map[string]Record{}
	if raw, ok := env.KV[keyRecords]; ok {
		if err := json.Unmarshal(raw, &recs); err != nil {
			t.Fatal(err)
		}
	}
	return recs
}

func queued(t *testing.T, env *sdktest.Env) []Item {
	t.Helper()
	var q []Item
	if raw, ok := env.KV[keyQueue]; ok {
		if err := json.Unmarshal(raw, &q); err != nil {
			t.Fatal(err)
		}
	}
	return q
}

func TestPublishedPostsToBothNetworks(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)

	if len(net.reqs) != 3 {
		t.Fatalf("want 3 requests, got %d: %+v", len(net.reqs), net.reqs)
	}

	// Bluesky: sign in with the app password, then create the record.
	s := net.reqs[0]
	if s.Method != "POST" || s.URL != "https://bsky.social/xrpc/com.atproto.server.createSession" {
		t.Errorf("session request: %s %s", s.Method, s.URL)
	}
	var login map[string]string
	json.Unmarshal([]byte(s.Body), &login)
	if login["identifier"] != "alice.bsky.social" || login["password"] != secretPassword {
		t.Errorf("session body: %v", login)
	}
	c := net.reqs[1]
	if c.URL != "https://bsky.social/xrpc/com.atproto.repo.createRecord" || c.Headers["Authorization"] != "Bearer "+secretJWT {
		t.Errorf("record request: %s %v", c.URL, c.Headers)
	}
	var create struct {
		Repo       string      `json:"repo"`
		Collection string      `json:"collection"`
		Record     BlueskyPost `json:"record"`
	}
	if err := json.Unmarshal([]byte(c.Body), &create); err != nil {
		t.Fatal(err)
	}
	wantText := "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go"
	rec := create.Record
	if create.Repo != "did:plc:abc" || create.Collection != "app.bsky.feed.post" || rec.Type != "app.bsky.feed.post" {
		t.Errorf("record envelope: %+v", create)
	}
	if rec.Text != wantText {
		t.Errorf("text:\n%q\nwant\n%q", rec.Text, wantText)
	}
	if rec.CreatedAt != "2026-10-18T12:00:00.000Z" || len(rec.Langs) != 1 || rec.Langs[0] != "es" {
		t.Errorf("createdAt %q, langs %v", rec.CreatedAt, rec.Langs)
	}
	if rec.Embed == nil || rec.Embed.Type != "app.bsky.embed.external" || rec.Embed.External.URI != p.URL ||
		rec.Embed.External.Title != p.Title || rec.Embed.External.Description != p.Excerpt {
		t.Errorf("embed: %+v", rec.Embed)
	}
	if len(rec.Facets) != 3 {
		t.Errorf("facets: %+v", rec.Facets)
	}
	for _, f := range rec.Facets {
		got := rec.Text[f.Index.ByteStart:f.Index.ByteEnd]
		switch ft := f.Features[0]; ft.Type {
		case "app.bsky.richtext.facet#link":
			if got != ft.URI || got != p.URL {
				t.Errorf("link facet covers %q, uri %q", got, ft.URI)
			}
		case "app.bsky.richtext.facet#tag":
			if got != "#"+ft.Tag {
				t.Errorf("tag facet covers %q, tag %q", got, ft.Tag)
			}
		default:
			t.Errorf("facet type %q", ft.Type)
		}
	}

	// Mastodon.
	m := net.reqs[2]
	if m.Method != "POST" || m.URL != "https://mastodon.social/api/v1/statuses" {
		t.Errorf("status request: %s %s", m.Method, m.URL)
	}
	for k, v := range map[string]string{
		"Authorization": "Bearer " + secretToken, "Idempotency-Key": "sap13ns-example.test-12", "Content-Type": "application/json",
	} {
		if m.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, m.Headers[k], v)
		}
	}
	var st MastodonStatus
	json.Unmarshal([]byte(m.Body), &st)
	if st.Status != wantText || st.Language != "es" || st.Visibility != "public" {
		t.Errorf("status body: %+v", st)
	}

	if r := records(t, env)["12"]; r.B != "d1" || r.M != "d1" || r.T != t0.Unix() {
		t.Errorf("record: %+v", r)
	}
	if len(queued(t, env)) != 0 {
		t.Errorf("queue: %+v", queued(t, env))
	}
	if _, held := env.KV[keyLock]; held {
		t.Error("the lock was not released")
	}
	wantLog(t, env,
		"post 12 “Hola, ¿qué tal?”: posted to Bluesky: at://did:plc:abc/app.bsky.feed.post/3kxyz (bsky.app/profile/did:plc:abc/post/3kxyz)",
		"post 12 “Hola, ¿qué tal?”: posted to Mastodon: https://mastodon.social/@alice/1")
	noSecrets(t, env)
}

func TestNeverPostedTwice(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	event(t, env, pluginsdk.EventPostPublished, p) // unpublished and published again
	event(t, env, pluginsdk.EventCronHourly, map[string]string{"time": t0.Format(time.RFC3339)})
	if len(net.reqs) != 3 {
		t.Errorf("want 3 requests in total, got %d", len(net.reqs))
	}
	wantLog(t, env, "already posted to Bluesky and Mastodon; it is never posted twice")
}

func TestOneNetwork(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		{"Bluesky only", map[string]string{"mastodon_token": ""}, []string{"createSession", "createRecord"}},
		{"Mastodon only", map[string]string{"bluesky_app_password": ""}, []string{"/api/v1/statuses"}},
		{"custom Bluesky server", map[string]string{"mastodon_token": "", "bluesky_service": "https://pds.example.org/xrpc"},
			[]string{"https://pds.example.org/xrpc/com.atproto.server.createSession", "https://pds.example.org/xrpc/com.atproto.repo.createRecord"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, net := setup(t)
			for k, v := range tt.settings {
				env.Plugin.Settings[k] = v
			}
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			if len(net.reqs) != len(tt.want) {
				t.Fatalf("requests: %+v", net.reqs)
			}
			for i, w := range tt.want {
				if !strings.HasSuffix(net.reqs[i].URL, w) {
					t.Errorf("request %d: %s, want …%s", i, net.reqs[i].URL, w)
				}
			}
		})
	}
}

func TestNothingConfigured(t *testing.T) {
	env, net := setup(t)
	env.Plugin.Settings = map[string]string{}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(net.reqs) != 0 || len(env.KV) != 0 {
		t.Errorf("requests %d, kv %v", len(net.reqs), env.KV)
	}
	wantLog(t, env, "not posted: neither Bluesky nor Mastodon is configured")
}

func TestScheduledPost(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	p.PublishedAt = t0.Add(2 * time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p) // fired when it is scheduled
	if len(net.reqs) != 0 {
		t.Fatalf("a scheduled post was sent: %+v", net.reqs)
	}
	q := queued(t, env)
	if len(q) != 1 || q[0].ID != 12 || q[0].At != t0.Add(2*time.Hour).Unix() || q[0].Lang != "es" {
		t.Fatalf("queue: %+v", q)
	}
	wantLog(t, env, "scheduled for 2026-10-18 14:00 UTC; it will be posted within an hour after it goes live")

	// An hour later it is not due yet.
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 0 {
		t.Fatalf("sent before its time: %+v", net.reqs)
	}

	// Due, but the host has not made it public yet (posts_query does not
	// return it): it waits.
	setClock(t, t0.Add(2*time.Hour+30*time.Minute))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 0 || len(queued(t, env)) != 1 {
		t.Fatalf("requests %d, queue %+v", len(net.reqs), queued(t, env))
	}

	// Public: the next hourly run posts the current version.
	p.Title = "Hola, ¿qué tal? (editado)"
	env.Posts = []pluginsdk.Post{p}
	setClock(t, t0.Add(3*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 3 {
		t.Fatalf("want 3 requests, got %+v", net.reqs)
	}
	if !strings.Contains(net.last("/statuses").Body, "(editado)") {
		t.Errorf("not the current version: %s", net.last("/statuses").Body)
	}
	if len(queued(t, env)) != 0 {
		t.Errorf("queue: %+v", queued(t, env))
	}
	if _, ok := env.KV[keyQueue]; ok {
		t.Error("an empty queue should delete its key")
	}
	wantLog(t, env, "posted to Mastodon")
}

func TestScheduledPostTrashed(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	event(t, env, pluginsdk.EventPostTrashed, p)
	if len(queued(t, env)) != 0 {
		t.Fatalf("queue: %+v", queued(t, env))
	}
	env.Posts = []pluginsdk.Post{p}
	setClock(t, t0.Add(2*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 0 {
		t.Errorf("a trashed post was sent")
	}
	wantLog(t, env, "moved to the trash: removed from the queue, it will not be posted")
}

func TestScheduledPostRescheduled(t *testing.T) {
	env, _ := setup(t)
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)

	p.PublishedAt = t0.Add(5 * time.Hour).Format(time.RFC3339)
	p.Title = "Nuevo título"
	event(t, env, pluginsdk.EventPostUpdated, p)
	q := queued(t, env)
	if len(q) != 1 || q[0].At != t0.Add(5*time.Hour).Unix() || q[0].Title != "Nuevo título" {
		t.Fatalf("queue: %+v", q)
	}
	wantLog(t, env, "queued post updated: now due 2026-10-18 17:00 UTC")

	// Moved back to now: due on the next run.
	p.PublishedAt = t0.Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostUpdated, p)
	if q := queued(t, env); len(q) != 1 || q[0].At != t0.Unix() {
		t.Fatalf("queue: %+v", q)
	}
}

func TestUpdateOfUnqueuedPostWritesNothing(t *testing.T) {
	env, net := setup(t)
	event(t, env, pluginsdk.EventPostUpdated, samplePost())
	event(t, env, pluginsdk.EventPostTrashed, samplePost())
	if len(env.KV) != 0 || len(net.reqs) != 0 || len(env.Logs) != 0 {
		t.Errorf("kv %v, requests %d, logs %v", env.KV, len(net.reqs), env.Logs)
	}
}

func TestNeverPublicIsDropped(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	event(t, env, pluginsdk.EventPostPublished, p) // env.Posts is empty: not public
	if q := queued(t, env); len(q) != 1 {
		t.Fatalf("queue: %+v", q)
	}
	wantLog(t, env, "not publicly visible yet; it is checked again every hour for 48 hours")

	setClock(t, t0.Add(47*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(queued(t, env)) != 1 {
		t.Fatal("dropped too early")
	}
	setClock(t, t0.Add(49*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(queued(t, env)) != 0 || len(net.reqs) != 0 {
		t.Fatalf("queue %+v, requests %d", queued(t, env), len(net.reqs))
	}
	wantLog(t, env, "dropped from the queue: still not public 48 hours after 2026-10-18 11:59 UTC")
}

func TestFoundBySearchWhenNotAmongNewest(t *testing.T) {
	env, net := setup(t)
	p := samplePost()
	p.Title = "Notas de la semana" // the fake host searches substrings, the real one words
	for i := range 20 {
		env.Posts = append(env.Posts, pluginsdk.Post{ID: int64(100 + i), Type: "post", Lang: "es", Title: "Otra " + strconv.Itoa(i)})
	}
	env.Posts = append(env.Posts, p)
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(net.reqs) != 3 {
		t.Errorf("want 3 requests, got %d; log:\n%s", len(net.reqs), logText(env))
	}
}

func TestFailureIsRetriedThreeTimes(t *testing.T) {
	env, net := setup(t)
	net.status = map[string]int{"/api/v1/statuses": 503}
	net.body = map[string]string{"/api/v1/statuses": `{"error":"Service unavailable","detail":"` + secretToken + `"}`}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if r := records(t, env)["12"]; r.B != "d1" || r.M != "f1" {
		t.Fatalf("record: %+v", r)
	}
	if q := queued(t, env); len(q) != 1 {
		t.Fatalf("queue: %+v", q)
	}
	wantLog(t, env, "Mastodon failed (attempt 1 of 3): HTTP 503: Service unavailable; it will be retried on the next hourly run")

	for i := 2; i <= 3; i++ {
		setClock(t, t0.Add(time.Duration(i-1)*time.Hour))
		event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	}
	wantLog(t, env, "Mastodon failed (attempt 3 of 3): HTTP 503: Service unavailable; giving up")
	if r := records(t, env)["12"]; r.B != "d1" || r.M != "f3" {
		t.Fatalf("record: %+v", r)
	}
	if q := queued(t, env); len(q) != 0 {
		t.Fatalf("queue: %+v", q)
	}
	setClock(t, t0.Add(4*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if net.count("createRecord") != 1 || net.count("/api/v1/statuses") != 3 {
		t.Errorf("createRecord %d, statuses %d", net.count("createRecord"), net.count("/api/v1/statuses"))
	}
	noSecrets(t, env)
}

func TestRetrySucceeds(t *testing.T) {
	env, net := setup(t)
	net.err = map[string]error{"createSession": errors.New(`Post "https://bsky.social/xrpc/com.atproto.server.createSession": dial tcp: lookup bsky.social: no such host`)}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "Bluesky failed (attempt 1 of 3): signing in:")
	net.err = nil
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if r := records(t, env)["12"]; r.B != "d2" || r.M != "d1" {
		t.Fatalf("record: %+v", r)
	}
	if net.count("/api/v1/statuses") != 1 || len(queued(t, env)) != 0 {
		t.Errorf("statuses %d, queue %+v", net.count("/api/v1/statuses"), queued(t, env))
	}
}

func TestUnknownOutcomeIsNotRetried(t *testing.T) {
	env, net := setup(t)
	net.err = map[string]error{"/api/v1/statuses": errors.New(`Post "https://mastodon.social/api/v1/statuses": context deadline exceeded`)}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if r := records(t, env)["12"]; r.M != "s1" {
		t.Fatalf("record: %+v", r)
	}
	if q := queued(t, env); len(q) != 0 {
		t.Fatalf("queue: %+v", q)
	}
	wantLog(t, env, "Mastodon: no answer (", "whether it was posted is unknown, so it is not retried. Check the account")

	net.err = nil
	event(t, env, pluginsdk.EventPostPublished, p)
	if net.count("/api/v1/statuses") != 1 {
		t.Errorf("sent again after an unknown outcome")
	}
	wantLog(t, env, "Mastodon: an earlier attempt got no answer")
}

func TestHostNotInPermissions(t *testing.T) {
	env, _ := setup(t)
	env.HTTP = nil // the fake host denies every request, as the host does for an undeclared host
	env.Plugin.Settings["bluesky_app_password"] = ""
	env.Plugin.Settings["mastodon_instance"] = "https://social.example.org"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "Mastodon failed (attempt 1 of 3): the host social.example.org is not in this plugin's permissions (http:social.example.org); see “Adding a Mastodon instance or a Bluesky server”")
}

func TestRequestBudget(t *testing.T) {
	env, net := setup(t)
	a, b := samplePost(), samplePost()
	b.ID, b.Title, b.URL = 13, "Segunda", "https://example.test/es/segunda"
	// Both are published while the host has not made them public yet.
	event(t, env, pluginsdk.EventPostPublished, a)
	event(t, env, pluginsdk.EventPostPublished, b)
	if len(queued(t, env)) != 2 {
		t.Fatalf("queue: %+v", queued(t, env))
	}
	env.Posts = []pluginsdk.Post{b, a}

	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 4 {
		t.Fatalf("first run: %d requests", len(net.reqs))
	}
	// a: Bluesky (2) and Mastodon (1); b: only Mastodon fits.
	if r := records(t, env); r["12"].B != "d1" || r["12"].M != "d1" || r["13"].B != "" || r["13"].M != "d1" {
		t.Fatalf("records: %+v", r)
	}
	wantLog(t, env, "post 13 “Segunda”: Bluesky: this run used its 4 requests; it will be posted on the next hourly run")

	setClock(t, t0.Add(2*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 6 || records(t, env)["13"].B != "d1" || len(queued(t, env)) != 0 {
		t.Fatalf("second run: %d requests, records %+v, queue %+v", len(net.reqs), records(t, env), queued(t, env))
	}
}

func TestQueueWaitsWhenBudgetIsUsed(t *testing.T) {
	env, net := setup(t)
	env.Plugin.Settings["mastodon_token"] = ""
	var posts []pluginsdk.Post
	for i := range 3 {
		p := samplePost()
		p.ID, p.Title = int64(20+i), "Entrada "+strconv.Itoa(i)
		posts = append(posts, p)
		event(t, env, pluginsdk.EventPostPublished, p)
	}
	env.Posts = posts
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(net.reqs) != 4 || len(queued(t, env)) != 1 {
		t.Fatalf("requests %d, queue %+v", len(net.reqs), queued(t, env))
	}
	wantLog(t, env, "hourly run: 1 queued post(s) wait for the next hourly run")
}

func TestDryRun(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		{"configured", map[string]string{"dry_run": "true"}, []string{
			`dry run, Bluesky (POST https://bsky.social/xrpc/com.atproto.repo.createRecord as alice.bsky.social, 77 characters): "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go"`,
			`dry run, Mastodon (POST https://mastodon.social/api/v1/statuses, visibility public, 72 characters)`,
		}},
		{"nothing configured", map[string]string{"dry_run": "true", "bluesky_handle": "", "mastodon_instance": "", "mastodon_token": "", "bluesky_app_password": ""}, []string{
			"dry run, Bluesky (POST https://bsky.social/xrpc/com.atproto.repo.createRecord as (no handle configured)",
			"dry run, Mastodon (POST (no instance configured)/api/v1/statuses",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, net := setup(t)
			for k, v := range tt.settings {
				env.Plugin.Settings[k] = v
			}
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			event(t, env, pluginsdk.EventPostPublished, p) // still previews: nothing was recorded
			if len(net.reqs) != 0 || len(env.KV) != 0 {
				t.Fatalf("requests %d, kv %v", len(net.reqs), env.KV)
			}
			wantLog(t, env, tt.want...)
			if strings.Count(logText(env), "dry run, Bluesky") != 2 {
				t.Errorf("log:\n%s", logText(env))
			}
			noSecrets(t, env)
		})
	}
}

func TestDryRunScheduled(t *testing.T) {
	env, _ := setup(t)
	env.Plugin.Settings["dry_run"] = "true"
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(env.KV) != 0 {
		t.Errorf("kv: %v", env.KV)
	}
	wantLog(t, env, "scheduled for 2026-10-18 13:00 UTC; dry run, so it is not queued")
}

func TestOldPostsAreSkipped(t *testing.T) {
	tests := []struct {
		name   string
		age    time.Duration
		hours  string
		posted bool
	}{
		{"default 48 hours, 47 hours old", 47 * time.Hour, "", true},
		{"default 48 hours, 49 hours old", 49 * time.Hour, "", false},
		{"1 hour, 2 hours old", 2 * time.Hour, "1", false},
		{"invalid setting uses 48", 47 * time.Hour, "-3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, net := setup(t)
			env.Plugin.Settings["skip_older_than_hours"] = tt.hours
			p := samplePost()
			p.PublishedAt = t0.Add(-tt.age).Format(time.RFC3339)
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			if got := len(net.reqs) > 0; got != tt.posted {
				t.Fatalf("posted %v, want %v; log:\n%s", got, tt.posted, logText(env))
			}
			if !tt.posted {
				if len(env.KV) != 0 {
					t.Errorf("kv: %v", env.KV)
				}
				wantLog(t, env, "not posted: it was first published on")
			}
		})
	}
}

func TestPostTypes(t *testing.T) {
	tests := []struct {
		setting, typ string
		posted       bool
		prefix       string
	}{
		{"", "post", true, "Nueva entrada:"},
		{"", "page", false, ""},
		{"pages", "post", false, ""},
		{"pages", "page", true, "Nueva página:"},
		{"both", "page", true, "Nueva página:"},
		{"bogus", "post", true, "Nueva entrada:"},
	}
	for _, tt := range tests {
		t.Run(tt.setting+"/"+tt.typ, func(t *testing.T) {
			env, net := setup(t)
			env.Plugin.Settings["post_types"] = tt.setting
			p := samplePost()
			p.Type = tt.typ
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			if got := len(net.reqs) > 0; got != tt.posted {
				t.Fatalf("posted %v, want %v", got, tt.posted)
			}
			if tt.posted {
				var st MastodonStatus
				json.Unmarshal([]byte(net.last("/statuses").Body), &st)
				if !strings.HasPrefix(st.Status, tt.prefix) {
					t.Errorf("status %q", st.Status)
				}
			} else {
				wantLog(t, env, "not posted: the plugin posts")
			}
		})
	}
}

func TestTemplatesAndLanguage(t *testing.T) {
	tests := []struct {
		name     string
		settings map[string]string
		lang     string
		siteLang string
		want     string
		mlang    string
	}{
		{"English default", nil, "en", "en", "New post: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go", "en"},
		{"site language when the post has none", nil, "", "es-ES", "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go", ""},
		{"regional Spanish", nil, "es-MX", "en", "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go", "es"},
		{"general template", map[string]string{"template": "{title} — {site}\n{excerpt}\n{url}"}, "en", "en",
			"Hola, ¿qué tal? — Example\nUn saludo desde Málaga.\nhttps://example.test/es/hola", "en"},
		{"Spanish template wins", map[string]string{"template": "X {url}", "template_es": "¡Nuevo! {title} {url}"}, "es", "en",
			"¡Nuevo! Hola, ¿qué tal? https://example.test/es/hola", "es"},
		{"English template", map[string]string{"template_es": "ES {url}", "template_en": "EN {url}"}, "en-GB", "es", "EN https://example.test/es/hola", "en"},
		{"other languages use the general template", map[string]string{"template": "{url}", "template_en": "EN {url}"}, "fr", "en", "https://example.test/es/hola", "fr"},
		{"no hashtags", map[string]string{"max_hashtags": "0"}, "en", "en", "New post: Hola, ¿qué tal?\n\nhttps://example.test/es/hola", "en"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, net := setup(t)
			env.Site.Language = tt.siteLang
			for k, v := range tt.settings {
				env.Plugin.Settings[k] = v
			}
			p := samplePost()
			p.Lang = tt.lang
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			var st MastodonStatus
			json.Unmarshal([]byte(net.last("/statuses").Body), &st)
			if st.Status != tt.want || st.Language != tt.mlang {
				t.Errorf("status %q (%q), want %q (%q)", st.Status, st.Language, tt.want, tt.mlang)
			}
		})
	}
}

func TestMastodonOptions(t *testing.T) {
	env, net := setup(t)
	env.Plugin.Settings["mastodon_visibility"] = "unlisted"
	env.Plugin.Settings["bluesky_app_password"] = ""
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if !strings.Contains(net.last("/statuses").Body, `"visibility":"unlisted"`) {
		t.Errorf("body %s", net.last("/statuses").Body)
	}
}

func TestInvalidSettingsAreReported(t *testing.T) {
	env, net := setup(t)
	env.Plugin.Settings["mastodon_instance"] = "http://mastodon.social"
	env.Plugin.Settings["bluesky_service"] = "bsky.social"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "Mastodon instance must be an https:// address", "Bluesky server must be an https:// address")
	if net.count("/statuses") != 0 || net.last("createSession").URL != "https://bsky.social/xrpc/com.atproto.server.createSession" {
		t.Errorf("requests: %+v", net.reqs)
	}
}

// TestScannerDryRun replays what the package scanner sends: its sample
// payloads with storage and network denied. No call may fail.
func TestScannerDryRun(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run("configured="+strconv.FormatBool(configured), func(t *testing.T) { scannerDryRun(t, configured) })
	}
}

func scannerDryRun(t *testing.T, configured bool) {
	{
		env, _ := setup(t)
		if !configured {
			env.Plugin.Settings = map[string]string{}
		}
		env.HTTP = nil
		env.Denied["kv"] = true
		sample := pluginsdk.Post{ID: 1, Type: "post", Title: "Sample post", Slug: "sample-post", Lang: "en",
			URL: "https://example.test/sample-post", PublishedAt: "2026-01-01T00:00:00Z", Tags: []string{"sample"}}
		recent := sample
		recent.PublishedAt = t0.Format(time.RFC3339)
		env.Posts = []pluginsdk.Post{recent}
		for _, ev := range []struct {
			name    string
			payload any
		}{
			{pluginsdk.EventPostPublished, sample},
			{pluginsdk.EventPostPublished, recent},
			{pluginsdk.EventPostUpdated, sample},
			{pluginsdk.EventPostTrashed, sample},
			{pluginsdk.EventCronHourly, map[string]string{"time": "2026-01-01T00:00:00Z"}},
			{pluginsdk.EventPostPublished, "not a post"},
		} {
			event(t, env, ev.name, ev.payload)
		}
		if configured {
			wantLog(t, env, "not posted: the plugin's storage is not available")
		}
	}
}

func TestOtherHooksAreIgnored(t *testing.T) {
	env, _ := setup(t)
	resp, err := env.Call(pluginsdk.HookWidget, map[string]string{})
	if err != nil || resp.HTML != "" {
		t.Errorf("%+v, %v", resp, err)
	}
}
