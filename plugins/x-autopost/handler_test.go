package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/autopost"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

const (
	secretKey         = "consumer-SECRET"
	secretToken       = "token-SECRET"
	secretTokenSecret = "token-secret-SECRET"
)

// fakeX answers like POST /2/tweets and records every request.
type fakeX struct {
	reqs    []pluginsdk.HTTPRequest
	status  int
	body    string
	headers map[string]string
}

func (f *fakeX) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.status != 0 {
		return &pluginsdk.HTTPResponse{Status: f.status, Body: f.body, Headers: f.headers}, nil
	}
	return &pluginsdk.HTTPResponse{Status: 201, Body: `{"data":{"id":"1846000000000000001","text":"…"}}`}, nil
}

func setup(t *testing.T) (*sdktest.Env, *fakeX) {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "x-autopost"
	env.Plugin.Settings = map[string]string{
		"api_key": "consumer-key", "api_key_secret": secretKey,
		"access_token": secretToken, "access_token_secret": secretTokenSecret,
		"max_hashtags": "2",
	}
	env.HTTPHosts = []string{"api.x.com"}
	x := &fakeX{}
	env.HTTP = x.fetch
	setClock(t, t0)
	prevSleep, prevNonce := sleep, newNonce
	sleep = func(time.Duration) {}
	newNonce = func() string { return "nonce" }
	t.Cleanup(func() { sleep, newNonce = prevSleep, prevNonce })
	return env, x
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
		Tags: []string{"open source", "Go", "Málaga"}, PublishedAt: t0.Add(-time.Minute).Format(time.RFC3339),
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
	for _, s := range []string{secretKey, secretToken, secretTokenSecret} {
		if strings.Contains(logs, s) {
			t.Errorf("the log contains the secret %q", s)
		}
	}
}

func state(t *testing.T, env *sdktest.Env) string {
	t.Helper()
	var recs map[string]map[string]any
	json.Unmarshal(env.KV[autopost.KeyRecords], &recs)
	s, _ := recs["12"]["x"].(string)
	return s
}

func queued(t *testing.T, env *sdktest.Env) []autopost.Item {
	t.Helper()
	var q []autopost.Item
	json.Unmarshal(env.KV[autopost.KeyQueue], &q)
	return q
}

// authParams parses an OAuth Authorization header.
func authParams(t *testing.T, h string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(h, "OAuth ")
	if !ok {
		t.Fatalf("header %q", h)
	}
	out := map[string]string{}
	for _, part := range strings.Split(rest, ", ") {
		k, v, _ := strings.Cut(part, "=")
		out[k] = strings.Trim(v, `"`)
	}
	return out
}

func TestPublishedPostGoesToX(t *testing.T) {
	env, x := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(x.reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(x.reqs))
	}
	r := x.reqs[0]
	if r.Method != "POST" || r.URL != "https://api.x.com/2/tweets" || r.Headers["Content-Type"] != "application/json" {
		t.Errorf("request %s %s %v", r.Method, r.URL, r.Headers)
	}
	var body map[string]any
	json.Unmarshal([]byte(r.Body), &body)
	if len(body) != 1 || body["text"] != "Nueva entrada: Hola, ¿qué tal?\n\nhttps://example.test/es/hola\n\n#OpenSource #Go" {
		t.Errorf("body %s", r.Body)
	}

	// The header is signed with the four credentials, and the JSON body is
	// not part of the signature.
	a := authParams(t, r.Headers["Authorization"])
	if a["oauth_consumer_key"] != "consumer-key" || a["oauth_token"] != secretToken || a["oauth_nonce"] != "nonce" ||
		a["oauth_timestamp"] != strconv.FormatInt(t0.Unix(), 10) || a["oauth_signature_method"] != "HMAC-SHA1" || a["oauth_version"] != "1.0" {
		t.Errorf("oauth parameters %v", a)
	}
	o := OAuth1{ConsumerKey: "consumer-key", ConsumerSecret: secretKey, Token: secretToken, TokenSecret: secretTokenSecret}
	params := map[string]string{}
	for k, v := range a {
		if k != "oauth_signature" {
			params[k] = v
		}
	}
	if want := encode(o.Signature("POST", "https://api.x.com/2/tweets", params)); a["oauth_signature"] != want {
		t.Errorf("signature %s, want %s", a["oauth_signature"], want)
	}
	for _, s := range []string{secretKey, secretTokenSecret} {
		if strings.Contains(r.Headers["Authorization"], s) {
			t.Errorf("the header carries the secret %q", s)
		}
	}

	if state(t, env) != "d1" {
		t.Errorf("record %q", state(t, env))
	}
	wantLog(t, env, "post 12 “Hola, ¿qué tal?”: posted to X: https://x.com/i/web/status/1846000000000000001",
		"already posted to X; it is never posted twice")
}

func TestLongTitleFits(t *testing.T) {
	env, x := setup(t)
	p := samplePost()
	p.Title = strings.Repeat("Una entrada con un título muy largo ", 12)
	p.URL = "https://example.test/es/" + strings.Repeat("a", 200)
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	var body TweetRequest
	json.Unmarshal([]byte(x.reqs[0].Body), &body)
	if n := xLen(body.Text); n > 280 || !strings.Contains(body.Text, "…\n\n"+p.URL+"\n\n#OpenSource #Go") {
		t.Errorf("%d: %q", n, body.Text)
	}
}

func TestRateLimitWaitsForTheReset(t *testing.T) {
	env, x := setup(t)
	reset := t0.Add(40 * time.Minute)
	x.status, x.body = 429, `{"title":"Too Many Requests","detail":"Too Many Requests","type":"about:blank","status":429}`
	x.headers = map[string]string{"x-rate-limit-limit": "100", "x-rate-limit-remaining": "0", "x-rate-limit-reset": strconv.FormatInt(reset.Unix(), 10)}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if q := queued(t, env); len(q) != 1 || q[0].At != reset.Unix() || state(t, env) != "f0" {
		t.Fatalf("queue %+v, record %q", q, state(t, env))
	}
	wantLog(t, env, "X asked to wait: rate limited (HTTP 429: Too Many Requests); it will be tried again on the first hourly run after 2026-10-18 12:40 UTC")

	// The 24-hour cap of the user ran out: its reset decides.
	x.headers = map[string]string{"x-rate-limit-remaining": "40", "x-rate-limit-reset": strconv.FormatInt(t0.Add(time.Hour).Unix(), 10),
		"x-user-limit-24hour-remaining": "0", "x-user-limit-24hour-reset": strconv.FormatInt(t0.Add(20*time.Hour).Unix(), 10)}
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if q := queued(t, env); len(q) != 1 || q[0].At != t0.Add(20*time.Hour).Unix() {
		t.Fatalf("queue %+v", q)
	}

	// No usable header: in 15 minutes.
	if r := rateLimited(&pluginsdk.HTTPResponse{Status: 429}); !r.Failed || r.Later != t0.Add(time.Hour+15*time.Minute) {
		t.Errorf("%+v", r)
	}

	x.status = 0
	setClock(t, t0.Add(21*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(x.reqs) != 3 || state(t, env) != "d1" || len(queued(t, env)) != 0 {
		t.Fatalf("requests %d, record %q, queue %+v", len(x.reqs), state(t, env), queued(t, env))
	}
}

func TestRefusalsAreExplained(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"not enrolled", 403, `{"client_id":"1","required_enrollment":"Appropriate Level of API Access","registration_url":"https://developer.x.com","title":"Client Forbidden","detail":"This request must be made using an approved developer account that is enrolled in the requested endpoint.","reason":"client-not-enrolled","type":"https://api.x.com/2/problems/client-forbidden"}`,
			"X failed (attempt 1 of 3): HTTP 403: Client Forbidden: This request must be made using an approved developer account that is enrolled in the requested endpoint.. The app cannot use this endpoint"},
		{"read-only token", 403, `{"title":"Forbidden","detail":"You are not permitted to perform this action.","type":"about:blank","status":403}`,
			"HTTP 403: Forbidden: You are not permitted to perform this action.. The access token may be read-only: set the app's permissions to “Read and write”"},
		{"duplicate", 403, `{"detail":"You are not allowed to create a Tweet with duplicate content.","type":"about:blank","title":"Forbidden","status":403}`,
			"X refuses a text identical to a recent post"},
		{"no credits", 402, `{"title":"Payment Required","detail":"Your account has no credits.","type":"about:blank","status":402}`,
			"The developer account has no credits left"},
		{"wrong keys", 401, `{"title":"Unauthorized","type":"about:blank","status":401,"detail":"Unauthorized"}`,
			"HTTP 401: Unauthorized. The keys or the tokens are wrong"},
		{"server error", 503, `{"title":"Service Unavailable","detail":"Service Unavailable","type":"about:blank","status":503}`,
			"X failed (attempt 1 of 3): HTTP 503: Service Unavailable; it will be retried on the next hourly run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, x := setup(t)
			x.status, x.body = tt.status, tt.body
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			wantLog(t, env, tt.want)
			if state(t, env) != "f1" || len(queued(t, env)) != 1 {
				t.Errorf("record %q, queue %+v", state(t, env), queued(t, env))
			}
		})
	}
}

func TestTimeoutIsNotRetried(t *testing.T) {
	env, _ := setup(t)
	env.HTTP = func(pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
		return nil, errTimeout{}
	}
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if state(t, env) != "s1" || len(queued(t, env)) != 0 {
		t.Errorf("record %q, queue %+v", state(t, env), queued(t, env))
	}
	wantLog(t, env, "X: no answer (", "whether it was posted is unknown, so it is not retried")
}

type errTimeout struct{}

func (errTimeout) Error() string { return "context deadline exceeded" }

func TestNotConfigured(t *testing.T) {
	env, x := setup(t)
	env.Plugin.Settings["access_token_secret"] = ""
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(x.reqs) != 0 || len(env.KV) != 0 {
		t.Errorf("requests %d, kv %v", len(x.reqs), env.KV)
	}
	wantLog(t, env, "not posted: X is not configured")
}

func TestDryRun(t *testing.T) {
	env, x := setup(t)
	env.Plugin.Settings["dry_run"] = "true"
	env.Plugin.Settings["template_es"] = "{title} {url}"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(x.reqs) != 0 || len(env.KV) != 0 {
		t.Fatalf("requests %d, kv %v", len(x.reqs), env.KV)
	}
	wantLog(t, env, `dry run, X (POST https://api.x.com/2/tweets, 39 characters): "Hola, ¿qué tal? https://example.test/es/hola"`)
}

// TestScannerDryRun replays what the package scanner sends: its sample
// payloads with storage and network denied. No call may fail.
func TestScannerDryRun(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run("configured="+strconv.FormatBool(configured), func(t *testing.T) {
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
			if _, err := env.Call(pluginsdk.HookWidget, map[string]string{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
