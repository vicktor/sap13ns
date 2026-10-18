package main

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

const (
	visitorEmail = "Visitor.Person@Example.ORG"
	visitorIP    = "203.0.113.7"
	// md5("visitor.person@example.org")
	visitorHash = "66c98541b94a07c5c15cb995afddea32"
)

func setup(t *testing.T) *sdktest.Env {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "antispam"
	return env
}

func comment(content string) pluginsdk.CommentCheck {
	return pluginsdk.CommentCheck{Author: "Ana Pérez", Content: content, Email: visitorEmail, IP: visitorIP, UserAgent: "Mozilla/5.0",
		Post: &pluginsdk.Post{ID: 3, Title: "Hola"}}
}

func logText(env *sdktest.Env) string {
	var b strings.Builder
	for _, l := range env.Logs {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

// noPII fails when the log holds the e-mail address, its hash or the IP.
func noPII(t *testing.T, env *sdktest.Env) {
	t.Helper()
	logs := strings.ToLower(logText(env))
	for _, s := range []string{strings.ToLower(visitorEmail), visitorIP, visitorHash, "example.org", "stopforumspam.org/api"} {
		if strings.Contains(logs, s) {
			t.Errorf("the log contains %q:\n%s", s, logText(env))
		}
	}
}

func TestCommentCheckVerdicts(t *testing.T) {
	tests := []struct {
		name    string
		c       pluginsdk.CommentCheck
		verdict string
		reason  string
		log     string
	}{
		{"clean", comment("Muy buen artículo."), "ok", "", `comment.check: ok by "Ana Pérez": no spam signals; first comment`},
		{"first link", comment("Mira https://example.net/x"), "pending", "first comment with a link", ""},
		{"registered", func() pluginsdk.CommentCheck {
			c := comment("Mira https://example.net/x")
			c.Registered = true
			return c
		}(),
			"ok", "", `comment.check: ok by "Ana Pérez": no spam signals; 1 link; signed-in user`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setup(t)
			v, reason, err := env.CheckComment(tt.c)
			if err != nil || v != tt.verdict || reason != tt.reason {
				t.Fatalf("got %s %q %v, want %s %q", v, reason, err, tt.verdict, tt.reason)
			}
			if tt.log != "" && !strings.Contains(logText(env), tt.log) {
				t.Errorf("log:\n%s", logText(env))
			}
			if tt.verdict != "ok" && len(env.Logs) != 0 {
				t.Errorf("held comments are logged by the host, not the plugin:\n%s", logText(env))
			}
			noPII(t, env)
		})
	}
}

func TestLogOKOff(t *testing.T) {
	env := setup(t)
	env.Plugin.Settings["log_ok"] = "false"
	if v, _, _ := env.CheckComment(comment("Hola")); v != "ok" || len(env.Logs) != 0 {
		t.Errorf("%s, %v", v, env.Logs)
	}
}

func TestKnownCommenters(t *testing.T) {
	env := setup(t)
	c := comment("Mira https://example.net/x")
	if v, _, _ := env.CheckComment(c); v != "pending" {
		t.Fatalf("first comment: %s", v)
	}
	// A moderator approves it.
	if _, err := env.Event(pluginsdk.EventCommentApproved, map[string]any{"id": 9, "post_id": 3, "author": "ana  PÉREZ"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logText(env), `comment 9 approved: "ana PÉREZ" is now a known commenter`) {
		t.Errorf("log:\n%s", logText(env))
	}
	if strings.Contains(string(env.KV[keyKnown]), "ana") || strings.Contains(strings.ToLower(string(env.KV[keyKnown])), "perez") {
		t.Errorf("the store holds the name: %q", env.KV[keyKnown])
	}
	if v, _, _ := env.CheckComment(c); v != "ok" {
		t.Errorf("known commenter: %s", v)
	}
	if !strings.Contains(logText(env), "known commenter") {
		t.Errorf("log:\n%s", logText(env))
	}
	// Approving again does not add a second entry.
	env.Event(pluginsdk.EventCommentApproved, map[string]any{"id": 10, "post_id": 3, "author": "Ana Pérez"})
	if n := len(strings.Fields(string(env.KV[keyKnown]))); n != 1 {
		t.Errorf("%d entries", n)
	}
	// Another site with the same storage would not match (the hash is salted with the site address).
	env.Site.URL = "https://other.test"
	if v, _, _ := env.CheckComment(c); v != "pending" {
		t.Errorf("other site: %s", v)
	}
}

func TestKnownListIsCapped(t *testing.T) {
	env := setup(t)
	var keys []string
	for i := range maxKnown {
		keys = append(keys, nameKey(env.Site.URL, "user"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))))
	}
	env.KV[keyKnown] = []byte(strings.Join(keys, "\n"))
	if _, err := learn(env.Site.URL, "Nuevo"); err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(string(env.KV[keyKnown]))
	if len(got) != maxKnown || got[len(got)-1] != nameKey(env.Site.URL, "Nuevo") || got[0] != keys[1] {
		t.Errorf("len %d", len(got))
	}
	if len(env.KV[keyKnown]) > 256<<10 {
		t.Errorf("%d bytes", len(env.KV[keyKnown]))
	}
}

// sfsFake answers like StopForumSpam and records the requests.
type sfsFake struct {
	reqs   []pluginsdk.HTTPRequest
	status int
	body   string
	err    error
}

func (f *sfsFake) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	if f.err != nil {
		return nil, f.err
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	return &pluginsdk.HTTPResponse{Status: st, Body: f.body}, nil
}

func TestStopForumSpam(t *testing.T) {
	clean := `{"success":1,"ip":{"value":"203.0.113.7","appears":0,"frequency":0},"emailhash":{"value":"x","appears":0,"frequency":0}}`
	tests := []struct {
		name     string
		settings map[string]string
		c        pluginsdk.CommentCheck
		fake     sfsFake
		requests int
		query    url.Values
		verdict  string
		reason   string
		log      string
	}{
		{"off by default", map[string]string{"sfs_enabled": ""}, comment("Hola"), sfsFake{}, 0, nil, "ok", "", "no spam signals; first comment\n"},
		{"not listed", nil, comment("Hola"), sfsFake{body: clean}, 1, url.Values{"json": {""}, "ip": {visitorIP}, "emailhash": {visitorHash}},
			"ok", "", "no spam signals; first comment; not listed by StopForumSpam"},
		{"listed with high confidence", nil, comment("Hola"),
			sfsFake{body: `{"success":1,"ip":{"appears":1,"frequency":250,"confidence":97.31,"lastseen":"2026-10-17 10:00:00"},"emailhash":{"appears":0}}`},
			1, nil, "spam", "listed by StopForumSpam: IP address (confidence 97%, seen 250 times)", ""},
		{"listed with low confidence", nil, comment("Hola"),
			sfsFake{body: `{"success":1,"ip":{"appears":0},"emailhash":{"appears":"1","frequency":"2","confidence":"12.5"}}`},
			1, nil, "pending", "listed by StopForumSpam: e-mail address (confidence 12%, seen 2 times)", ""},
		{"custom threshold", map[string]string{"sfs_threshold": "10"}, comment("Hola"),
			sfsFake{body: `{"success":1,"emailhash":{"appears":true,"frequency":2,"confidence":12.5}}`},
			1, nil, "spam", "e-mail address (confidence 12%", ""},
		{"both listed", nil, comment("Hola"),
			sfsFake{body: `{"success":1,"ip":{"appears":1,"frequency":3,"confidence":40},"emailhash":{"appears":1,"frequency":9,"confidence":70}}`},
			1, nil, "spam", "IP address (confidence 40%, seen 3 times) and e-mail address (confidence 70%, seen 9 times)", ""},
		{"IP only", map[string]string{"sfs_check_email": "false"}, comment("Hola"), sfsFake{body: clean}, 1, url.Values{"json": {""}, "ip": {visitorIP}}, "ok", "", ""},
		{"e-mail only", map[string]string{"sfs_check_ip": "false"}, comment("Hola"), sfsFake{body: clean}, 1, url.Values{"json": {""}, "emailhash": {visitorHash}}, "ok", "", ""},
		{"private IP and no e-mail are not sent", nil, func() pluginsdk.CommentCheck { c := comment("Hola"); c.IP, c.Email = "192.168.1.20", ""; return c }(),
			sfsFake{body: clean}, 0, nil, "ok", "", "StopForumSpam not asked: no public IP address or e-mail address to look up"},
		{"loopback IP is left out", nil, func() pluginsdk.CommentCheck { c := comment("Hola"); c.IP = "::1"; return c }(),
			sfsFake{body: clean}, 1, url.Values{"json": {""}, "emailhash": {visitorHash}}, "ok", "", ""},
		{"no PII granted", nil, func() pluginsdk.CommentCheck { c := comment("Hola"); c.IP, c.Email, c.UserAgent = "", "", ""; return c }(),
			sfsFake{body: clean}, 0, nil, "ok", "", "StopForumSpam not asked"},
		{"not asked when the local checks already hold it", nil, comment("Mira https://example.net/x"), sfsFake{body: clean}, 0, nil, "pending", "first comment with a link", ""},
		{"timeout: the local checks decide", nil, comment("Hola"),
			sfsFake{err: errors.New(`Get "https://api.stopforumspam.org/api?json&ip=203.0.113.7": context deadline exceeded`)},
			1, nil, "ok", "", `comment.check by "Ana Pérez": StopForumSpam: no answer in time; judged by the local checks only`},
		{"network error: the local checks decide", nil, comment("Hola"),
			sfsFake{err: errors.New(`Get "https://api.stopforumspam.org/api?json&ip=203.0.113.7": dial tcp: lookup api.stopforumspam.org: no such host`)},
			1, nil, "ok", "", "StopForumSpam: network error; judged by the local checks only"},
		{"server error: the local checks decide", nil, comment("Hola"), sfsFake{status: 503, body: "<html>busy</html>"}, 1, nil, "ok", "", "StopForumSpam: HTTP 503"},
		{"API error: the local checks decide", nil, comment("Hola"), sfsFake{body: `{"success":0,"error":"rate limit exceeded"}`}, 1, nil, "ok", "", "StopForumSpam: unexpected answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setup(t)
			env.Plugin.Settings["sfs_enabled"] = "true"
			for k, v := range tt.settings {
				env.Plugin.Settings[k] = v
			}
			fake := tt.fake
			env.HTTP = fake.fetch
			v, reason, err := env.CheckComment(tt.c)
			if err != nil || v != tt.verdict || !strings.Contains(reason, tt.reason) {
				t.Fatalf("got %s %q %v, want %s %q", v, reason, err, tt.verdict, tt.reason)
			}
			if len(fake.reqs) != tt.requests {
				t.Fatalf("%d requests, want %d", len(fake.reqs), tt.requests)
			}
			if len(fake.reqs) == 1 {
				r := fake.reqs[0]
				u, err := url.Parse(r.URL)
				if err != nil || r.Method != "GET" || u.Scheme != "https" || u.Host != "api.stopforumspam.org" || u.Path != "/api" {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				for _, leak := range []string{"Ana", "Hola", "Mozilla", "@", url.QueryEscape(visitorEmail)} {
					if strings.Contains(r.URL, leak) || strings.Contains(r.Body, leak) {
						t.Errorf("the request carries %q: %s", leak, r.URL)
					}
				}
				if tt.query != nil {
					if got := u.Query(); got.Encode() != tt.query.Encode() {
						t.Errorf("query %v, want %v", got, tt.query)
					}
				}
			}
			if tt.log != "" && !strings.Contains(logText(env), tt.log) {
				t.Errorf("log lacks %q:\n%s", tt.log, logText(env))
			}
			noPII(t, env)
		})
	}
}

func TestSFSDenied(t *testing.T) {
	env := setup(t)
	env.Plugin.Settings["sfs_enabled"] = "true"
	// env.HTTP is nil: the host denies the request.
	v, _, err := env.CheckComment(comment("Hola"))
	if err != nil || v != "ok" || !strings.Contains(logText(env), "StopForumSpam: the request was denied") {
		t.Errorf("%s %v\n%s", v, err, logText(env))
	}
	noPII(t, env)
}

// TestScannerDryRun replays the scanner's sample calls: storage and
// network denied. Nothing may fail.
func TestScannerDryRun(t *testing.T) {
	env := setup(t)
	env.Denied["kv"] = true
	env.Denied["http"] = true
	env.Plugin.Settings["sfs_enabled"] = "true"
	v, _, err := env.CheckComment(pluginsdk.CommentCheck{Author: "Sample Visitor", URL: "https://visitor.example", Content: "Nice post, thanks!",
		Post: &pluginsdk.Post{ID: 1, Title: "Sample post"}})
	if err != nil || v != "ok" {
		t.Fatalf("%s %v", v, err)
	}
	if _, err := env.Event(pluginsdk.EventCommentApproved, map[string]any{"id": 1, "post_id": 1, "author": "Sample Visitor", "status": "pending", "content": "Nice post!"}); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Event(pluginsdk.EventCommentApproved, "garbage"); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Call(pluginsdk.HookCommentCheck, "garbage"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the list of known commenters is not available", "could not remember \"Sample Visitor\""} {
		if !strings.Contains(logText(env), want) {
			t.Errorf("log lacks %q:\n%s", want, logText(env))
		}
	}
}

// TestVerdictIsAlwaysExplicit guards the host contract: a comment.check
// answer without a verdict holds the comment, so "ok" must be spelled out
// (sdktest's CheckComment would hide an empty verdict).
func TestVerdictIsAlwaysExplicit(t *testing.T) {
	tests := []struct {
		name    string
		denied  []string
		payload any
	}{
		{"clean comment", nil, comment("Muy buen artículo.")},
		{"storage denied", []string{"kv", "http"}, comment("Hola")},
		{"StopForumSpam denied", []string{"http"}, comment("Hola")},
		{"unreadable payload", nil, "garbage"},
		{"empty payload", nil, map[string]any{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := setup(t)
			env.Plugin.Settings["sfs_enabled"] = "true"
			for _, d := range tt.denied {
				env.Denied[d] = true
			}
			resp, err := env.Call(pluginsdk.HookCommentCheck, tt.payload)
			if err != nil || resp.Verdict != "ok" {
				t.Errorf("verdict %q, err %v, raw %s", resp.Verdict, err, resp.Raw)
			}
		})
	}
}

func TestSFSQueryNormalizes(t *testing.T) {
	cfg := ConfigFrom(map[string]string{"sfs_enabled": "true"})
	tests := []struct {
		ip, email, want string
		ok              bool
	}{
		{"203.0.113.7", "", "https://api.stopforumspam.org/api?json&ip=203.0.113.7", true},
		{"::ffff:203.0.113.7", "", "https://api.stopforumspam.org/api?json&ip=203.0.113.7", true},
		{"2001:db8::1", "", "https://api.stopforumspam.org/api?json&ip=2001%3Adb8%3A%3A1", true},
		{"10.0.0.1", "not-an-address", "", false},
		{"127.0.0.1", "", "", false},
		{"garbage", " " + visitorEmail + " ", "https://api.stopforumspam.org/api?json&emailhash=" + visitorHash, true},
	}
	for _, tt := range tests {
		got, ok := SFSQuery(cfg, tt.ip, tt.email)
		if got != tt.want || ok != tt.ok {
			t.Errorf("SFSQuery(%q, %q) = %q %v, want %q %v", tt.ip, tt.email, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOtherHooksAndEvents(t *testing.T) {
	env := setup(t)
	if r, err := env.Event(pluginsdk.EventCronHourly, map[string]string{}); err != nil || r.HTML != "" {
		t.Errorf("%+v %v", r, err)
	}
	if len(env.KV) != 0 {
		t.Errorf("kv %v", env.KV)
	}
}
