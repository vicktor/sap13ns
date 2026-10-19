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

const secretToken = "li-token-SECRET"

// fakeLinkedIn answers like POST /rest/posts and records every request.
type fakeLinkedIn struct {
	reqs   []pluginsdk.HTTPRequest
	status int
	body   string
}

func (f *fakeLinkedIn) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.status != 0 {
		return &pluginsdk.HTTPResponse{Status: f.status, Body: f.body}, nil
	}
	return &pluginsdk.HTTPResponse{Status: 201, Headers: map[string]string{"x-restli-id": "urn:li:share:7310000000000000001"}}, nil
}

func setup(t *testing.T) (*sdktest.Env, *fakeLinkedIn) {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "linkedin"
	env.Plugin.Settings = map[string]string{"client_id": "cid", "client_secret": "csecret", "max_hashtags": "3",
		"visibility": "PUBLIC", "linkedin_version": DefaultVersion}
	env.HTTPHosts = []string{"api.linkedin.com", "www.linkedin.com"}
	env.OAuth[provider] = pluginsdk.OAuthToken{AccessToken: secretToken, AccountID: "abc123", AccountName: "Ana García",
		Scopes: []string{"openid", "profile", "w_member_social"}, ExpiresAt: "2099-01-01T00:00:00Z"}
	li := &fakeLinkedIn{}
	env.HTTP = li.fetch
	setClock(t, t0)
	prev := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = prev })
	return env, li
}

func setClock(t *testing.T, at time.Time) {
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}

func samplePost() pluginsdk.Post {
	return pluginsdk.Post{
		ID: 12, Type: "post", Lang: "es", Title: "Hola, ¿qué tal? (2026)", Slug: "hola",
		URL: "https://example.test/es/hola", Excerpt: "Un saludo desde Málaga.",
		Tags: []string{"open source", "Go"}, PublishedAt: t0.Add(-time.Minute).Format(time.RFC3339),
	}
}

func event(t *testing.T, env *sdktest.Env, name string, payload any) {
	t.Helper()
	if _, err := env.Event(name, payload); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func wantLog(t *testing.T, env *sdktest.Env, subs ...string) {
	t.Helper()
	var b strings.Builder
	for _, l := range env.Logs {
		b.WriteString(l.Text + "\n")
	}
	logs := b.String()
	for _, s := range subs {
		if !strings.Contains(logs, s) {
			t.Errorf("log lacks %q:\n%s", s, logs)
		}
	}
	if strings.Contains(logs, secretToken) {
		t.Error("the log contains the access token")
	}
}

func state(env *sdktest.Env) string {
	var recs map[string]map[string]any
	json.Unmarshal(env.KV[autopost.KeyRecords], &recs)
	s, _ := recs["12"]["l"].(string)
	return s
}

func queued(env *sdktest.Env) []autopost.Item {
	var q []autopost.Item
	json.Unmarshal(env.KV[autopost.KeyQueue], &q)
	return q
}

func TestArticleShare(t *testing.T) {
	env, li := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(li.reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(li.reqs))
	}
	r := li.reqs[0]
	for k, v := range map[string]string{"Authorization": "Bearer " + secretToken, "LinkedIn-Version": "202610",
		"X-Restli-Protocol-Version": "2.0.0", "Content-Type": "application/json"} {
		if r.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, r.Headers[k], v)
		}
	}
	if r.Method != "POST" || r.URL != "https://api.linkedin.com/rest/posts" {
		t.Errorf("%s %s", r.Method, r.URL)
	}
	var s Share
	if err := json.Unmarshal([]byte(r.Body), &s); err != nil {
		t.Fatal(err)
	}
	want := Share{
		Author:         "urn:li:person:abc123",
		Commentary:     `Nueva entrada: Hola, ¿qué tal? \(2026\)` + "\n\nUn saludo desde Málaga.\n\n" + `{hashtag|\#|OpenSource} {hashtag|\#|Go}`,
		Visibility:     "PUBLIC",
		Distribution:   Distribution{FeedDistribution: "MAIN_FEED", TargetEntities: []string{}, ThirdPartyDistributionChannels: []string{}},
		Content:        Content{Article: Article{Source: p.URL, Title: p.Title, Description: p.Excerpt}},
		LifecycleState: "PUBLISHED",
	}
	got, _ := json.Marshal(s)
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Errorf("share\n%s\nwant\n%s", got, exp)
	}
	if !strings.Contains(r.Body, `"targetEntities":[]`) || !strings.Contains(r.Body, `"isReshareDisabledByAuthor":false`) {
		t.Errorf("body %s", r.Body)
	}
	if state(env) != "d1" {
		t.Errorf("record %q", state(env))
	}
	wantLog(t, env, "posted to LinkedIn: https://www.linkedin.com/feed/update/urn:li:share:7310000000000000001/",
		"already posted to LinkedIn; it is never posted twice")
}

func TestConnection(t *testing.T) {
	tests := []struct {
		name  string
		token *pluginsdk.OAuthToken
		want  string
	}{
		{"not connected", nil, "not posted: LinkedIn is not connected: connect it in Admin → Plugins → LinkedIn → Connections"},
		{"expired", &pluginsdk.OAuthToken{AccessToken: secretToken, AccountID: "abc123", ExpiresAt: "2001-01-01T00:00:00Z"},
			"not posted: the LinkedIn connection expired (LinkedIn tokens last 60 days): connect it again"},
		{"no member id", &pluginsdk.OAuthToken{AccessToken: secretToken}, "the LinkedIn connection has no member id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, li := setup(t)
			delete(env.OAuth, provider)
			if tt.token != nil {
				env.OAuth[provider] = *tt.token
			}
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			if len(li.reqs) != 0 || len(env.KV) != 0 {
				t.Errorf("requests %d, kv %v", len(li.reqs), env.KV)
			}
			wantLog(t, env, tt.want)
		})
	}
}

func TestQueuedPostWaitsForTheConnection(t *testing.T) {
	env, li := setup(t)
	p := samplePost()
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(queued(env)) != 1 {
		t.Fatalf("queue %+v", queued(env))
	}
	tok := env.OAuth[provider]
	delete(env.OAuth, provider)
	env.Posts = []pluginsdk.Post{p}
	setClock(t, t0.Add(2*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	wantLog(t, env, "hourly run: 1 queued post(s) wait: LinkedIn is not connected")
	env.OAuth[provider] = tok
	setClock(t, t0.Add(3*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(li.reqs) != 1 || state(env) != "d1" {
		t.Errorf("requests %d, record %q", len(li.reqs), state(env))
	}
}

func TestRefusals(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"revoked", 401, `{"status":401,"serviceErrorCode":65601,"code":"REVOKED_ACCESS_TOKEN","message":"The token used in the request has been revoked by the user"}`,
			"LinkedIn failed (attempt 1 of 3): HTTP 401: The token used in the request has been revoked by the user. The token was revoked or expired: connect LinkedIn again"},
		{"no product", 403, `{"status":403,"serviceErrorCode":100,"code":"ACCESS_DENIED","message":"Not enough permissions to access: partnerApiPostsExternal.CREATE.20261001"}`,
			"HTTP 403: Not enough permissions to access: partnerApiPostsExternal.CREATE.20261001. The app may lack the “Share on LinkedIn” product"},
		{"old version", 426, `{"status":426,"code":"NONEXISTENT_VERSION","message":"Requested version 20231001 is not active"}`,
			"HTTP 426: Requested version 20231001 is not active. The “LinkedIn API version” setting is not a version LinkedIn supports"},
		{"bad request", 400, `{"status":400,"message":"Unpermitted fields present in REQUEST_BODY"}`, "HTTP 400: Unpermitted fields present in REQUEST_BODY; it will be retried"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, li := setup(t)
			li.status, li.body = tt.status, tt.body
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			wantLog(t, env, tt.want)
			if state(env) != "f1" || len(queued(env)) != 1 {
				t.Errorf("record %q, queue %+v", state(env), queued(env))
			}
		})
	}
}

func TestRateLimitWaitsForMidnight(t *testing.T) {
	env, li := setup(t)
	li.status, li.body = 429, `{"status":429,"message":"Resource level throttle APPLICATION DAY limit for calls to this resource is reached."}`
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	midnight := time.Date(2026, 10, 19, 0, 5, 0, 0, time.UTC)
	if q := queued(env); len(q) != 1 || q[0].At != midnight.Unix() || state(env) != "f0" {
		t.Fatalf("queue %+v, record %q", q, state(env))
	}
	wantLog(t, env, "LinkedIn asked to wait: rate limited (HTTP 429: Resource level throttle", "after 2026-10-19 00:05 UTC")
}

func TestSettings(t *testing.T) {
	env, li := setup(t)
	env.Plugin.Settings["linkedin_version"] = "2026-10"
	env.Plugin.Settings["visibility"] = "CONNECTIONS"
	env.Plugin.Settings["template"] = "{title} {url}"
	p := samplePost()
	p.Lang = "en"
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "LinkedIn API version must look like 202610 (year and month); using 202610")
	var s Share
	json.Unmarshal([]byte(li.reqs[0].Body), &s)
	if li.reqs[0].Headers["LinkedIn-Version"] != "202610" || s.Visibility != "CONNECTIONS" ||
		s.Commentary != `Hola, ¿qué tal? \(2026\) https://example.test/es/hola` {
		t.Errorf("%v %+v", li.reqs[0].Headers, s)
	}
	if defaultTemplate("en", "page") != "New page: {title}\n\n{excerpt}\n\n{hashtags}" {
		t.Error("default template")
	}
}

func TestDryRun(t *testing.T) {
	env, li := setup(t)
	env.Plugin.Settings["dry_run"] = "true"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(li.reqs) != 0 || len(env.KV) != 0 {
		t.Fatalf("requests %d, kv %v", len(li.reqs), env.KV)
	}
	wantLog(t, env, "dry run, LinkedIn (POST https://api.linkedin.com/rest/posts as Ana García, LinkedIn-Version 202610, visibility PUBLIC, ")

	// Not connected: the dry run still shows the text.
	delete(env.OAuth, provider)
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "as (not connected)")
}

func TestScannerDryRun(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run("configured="+strconv.FormatBool(configured), func(t *testing.T) {
			env, _ := setup(t)
			if !configured {
				env.Plugin.Settings = map[string]string{}
			}
			env.HTTP = nil
			env.Denied["kv"] = true
			env.Denied["oauth"] = true
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
				{pluginsdk.EventOAuthConnected, map[string]string{"provider": provider}},
				{pluginsdk.EventPostPublished, "not a post"},
			} {
				event(t, env, ev.name, ev.payload)
			}
			wantLog(t, env, "the LinkedIn connection is not available (permission denied)")
		})
	}
}
