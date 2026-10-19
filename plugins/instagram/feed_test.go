package main

import (
	"encoding/json"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func TestFeedRefresh(t *testing.T) {
	env, g := setup(t)
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 1 {
		t.Fatalf("requests %v", g.paths())
	}
	u, _ := url.Parse(g.reqs[0].URL)
	if u.Host != "graph.instagram.com" || u.Query().Get("fields") != fullFields || u.Query().Get("limit") != "24" ||
		g.reqs[0].Headers["Authorization"] != "Bearer "+secretToken || u.Query().Get("access_token") != "" {
		t.Errorf("feed request %s %v", g.reqs[0].URL, g.reqs[0].Headers)
	}
	f := storedFeed(t, env)
	want := []Media{
		{ID: "1", Kind: "image", Image: "https://scontent-mad1-1.cdninstagram.com/v/t51.29350-15/1_n.jpg?stp=dst-jpg&oe=6720", Link: "https://www.instagram.com/p/AAA/", Alt: "Sunset in Málaga #sunset", Time: time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC).Unix()},
		{ID: "2", Kind: "video", Image: "https://scontent-mad2-1.cdninstagram.com/v/t51/2_n.jpg?oe=1", Link: "https://www.instagram.com/reel/BBB/", Alt: "A reel", Time: time.Date(2026, 10, 16, 9, 30, 0, 0, time.UTC).Unix()},
		{ID: "3", Kind: "album", Image: "https://scontent.xx.fbcdn.net/v/3_n.jpg", Link: "https://www.instagram.com/p/CCC/", Alt: "Three cups of coffee", Time: time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC).Unix()},
		// A video without a thumbnail (copyrighted audio) and an image on a
		// host outside img-src get a placeholder; a post without an
		// instagram.com link and an unknown type are left out.
		{ID: "4", Kind: "video", Link: "https://www.instagram.com/reel/DDD/", Time: time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC).Unix()},
		{ID: "5", Kind: "image", Link: "https://www.instagram.com/p/EEE/", Time: time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC).Unix()},
	}
	if f.Account != igID || f.User != "sap13ns" || f.Fetched != t0.Unix() || !slices.Equal(f.Items, want) {
		t.Errorf("feed %+v\nwant items %+v", f, want)
	}
	wantLog(t, env, "feed: 5 posts from @sap13ns", "feed: some images are on hosts outside the plugin's img-src (evil.example)")

	// Within the hour: nothing is fetched. After it: fetched again.
	setClock(t, t0.Add(30*time.Minute))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 1 {
		t.Errorf("fetched again within the hour: %v", g.paths())
	}
	setClock(t, t0.Add(56*time.Minute))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 2 {
		t.Errorf("not fetched after an hour: %v", g.paths())
	}
	// Connecting fetches at once.
	event(t, env, pluginsdk.EventOAuthConnected, map[string]string{"provider": provider})
	if len(g.reqs) != 3 {
		t.Errorf("not fetched when the account connected: %v", g.paths())
	}
}

func TestFeedFallsBackToTheBasicFields(t *testing.T) {
	env, g := setup(t)
	g.media = []answer{{400, `{"error":{"message":"(#100) Tried accessing nonexisting field (caption) on node type (Media)","type":"OAuthException","code":100}}`, nil}}
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 2 {
		t.Fatalf("requests %v", g.paths())
	}
	u, _ := url.Parse(g.reqs[1].URL)
	if u.Query().Get("fields") != basicFields {
		t.Errorf("fields %q", u.Query().Get("fields"))
	}
	f := storedFeed(t, env)
	if !f.Basic || len(f.Items) != 5 {
		t.Errorf("feed %+v", f)
	}
	wantLog(t, env, "feed: Instagram refused the caption fields")
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	u, _ = url.Parse(g.reqs[2].URL)
	if len(g.reqs) != 3 || u.Query().Get("fields") != basicFields {
		t.Errorf("the basic fields are remembered: %v", g.paths())
	}
}

func TestFeedFailuresKeepTheLastList(t *testing.T) {
	env, g := setup(t)
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	g.media = []answer{
		{400, `{"error":{"message":"Error validating access token","code":190}}`, nil},
		{500, `{"error":{"message":"An unexpected error has occurred.","code":2}}`, nil},
	}
	setClock(t, t0.Add(time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	f := storedFeed(t, env)
	if f.Fails != 1 || len(f.Items) != 5 || f.Fetched != t0.Unix() || !strings.Contains(f.Err, "code 190") {
		t.Errorf("feed %+v", f)
	}
	wantLog(t, env, "feed: fetching the latest posts failed (HTTP 400: Error validating access token: (code 190). The token is no longer valid: connect Instagram again on the plugin's page); the last good list (5 posts) stays")
	// The next try waits an hour, then two.
	setClock(t, t0.Add(90*time.Minute))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 2 {
		t.Errorf("retried too soon: %v", g.paths())
	}
	setClock(t, t0.Add(2*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	setClock(t, t0.Add(3*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 3 || storedFeed(t, env).Fails != 2 {
		t.Errorf("backoff: %v, %+v", g.paths(), storedFeed(t, env))
	}
	setClock(t, t0.Add(4*time.Hour))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if f := storedFeed(t, env); len(g.reqs) != 4 || f.Fails != 0 || f.Fetched != t0.Add(4*time.Hour).Unix() {
		t.Errorf("recovery: %v, %+v", g.paths(), f)
	}
}

func TestFeedOfAnotherAccountStartsOver(t *testing.T) {
	env, _ := setup(t)
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	env.OAuth[provider] = pluginsdk.OAuthToken{AccessToken: secretToken, AccountID: "17841400000000002", AccountName: "other"}
	setClock(t, t0.Add(10*time.Minute))
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if f := storedFeed(t, env); f.Account != "17841400000000002" || f.User != "other" || len(f.Items) != 0 || f.Fails != 1 {
		t.Errorf("feed %+v", f) // the fake only knows the first account
	}
}

func TestDisconnectingRemovesTheFeed(t *testing.T) {
	env, _ := setup(t)
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if _, ok := env.KV[feedKey]; !ok {
		t.Fatal("no feed")
	}
	tok := env.OAuth[provider]
	tok.ExpiresAt = "2020-01-01T00:00:00Z"
	env.OAuth[provider] = tok
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if _, ok := env.KV[feedKey]; !ok {
		t.Error("an ended connection keeps the feed (it shows until its images expire)")
	}
	delete(env.OAuth, provider)
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if _, ok := env.KV[feedKey]; ok {
		t.Error("the feed of a disconnected account is kept")
	}
	wantLog(t, env, "feed: Instagram is not connected, so the stored feed was removed")
}

func TestHourlyBudget(t *testing.T) {
	p := samplePost()
	p.PublishedAt = t0.Add(30 * time.Minute).Format(time.RFC3339)
	t.Run("feed and post", func(t *testing.T) {
		// The feed takes one request and a post three: four, the host's limit.
		env, g := setup(t)
		event(t, env, pluginsdk.EventPostPublished, p)
		setClock(t, t0.Add(time.Hour))
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventCronHourly, map[string]string{})
		if len(g.reqs) != 4 || state(env) != "d1" {
			t.Errorf("requests %v, record %q", g.paths(), state(env))
		}
	})
	t.Run("field fallback", func(t *testing.T) {
		// With the field fallback (two requests) the post waits an hour.
		hourlyFallback(t, p)
	})
}

func hourlyFallback(t *testing.T, p pluginsdk.Post) {
	env, g := setup(t)
	g.media = []answer{{400, `{"error":{"message":"(#100) Tried accessing nonexisting field (caption)","code":100}}`, nil}}
	event(t, env, pluginsdk.EventPostPublished, p)
	setClock(t, t0.Add(time.Hour))
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if len(g.reqs) != 2 || len(queued(env)) != 1 {
		t.Errorf("requests %v, queue %v", g.paths(), queued(env))
	}
	wantLog(t, env, "Instagram: this run used its 4 requests; it will be posted on the next hourly run")
}

// TestManifest keeps plugin.json in step with the code.
func TestManifest(t *testing.T) {
	raw, err := os.ReadFile("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Permissions []string            `json:"permissions"`
		CSP         map[string][]string `json:"csp"`
		Shortcodes  []string            `json:"shortcodes"`
		Events      []string            `json:"events"`
		Assets      map[string][]string `json:"assets"`
		Widgets     []struct {
			ID string `json:"id"`
		} `json:"widgets"`
		OAuth []struct {
			ID, AuthorizeURL, TokenURL, UserinfoURL string
			Scopes                                  []string
			Exchange, Refresh                       struct{ URL, GrantType, Token string }
		} `json:"oauth"`
		Settings []struct{ Key, Type, Default string } `json:"settings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	var oauth struct {
		OAuth []struct {
			UserinfoURL string `json:"userinfo_url"`
			Exchange    struct {
				URL       string `json:"url"`
				GrantType string `json:"grant_type"`
			} `json:"exchange"`
			Refresh struct {
				URL       string `json:"url"`
				GrantType string `json:"grant_type"`
				Token     string `json:"token"`
			} `json:"refresh"`
		} `json:"oauth"`
	}
	json.Unmarshal(raw, &oauth)
	wantPerms := []string{"kv", "posts:read", "oauth:" + provider, "http:www.instagram.com", "http:api.instagram.com", "http:graph.instagram.com", "assets:js"}
	if !slices.Equal(m.Permissions, wantPerms) {
		t.Errorf("permissions %v", m.Permissions)
	}
	var hosts []string
	for _, h := range imageHosts {
		hosts = append(hosts, "https://*"+h)
	}
	if !slices.Equal(m.CSP["img-src"], hosts) || len(m.CSP) != 1 {
		t.Errorf("csp %v, want img-src %v", m.CSP, hosts)
	}
	if !slices.Equal(m.Shortcodes, []string{"instagram_feed"}) || len(m.Widgets) != 1 || m.Widgets[0].ID != "feed" {
		t.Errorf("shortcodes %v, widgets %v", m.Shortcodes, m.Widgets)
	}
	if !slices.Contains(m.Events, pluginsdk.EventOAuthConnected) || !slices.Contains(m.Events, pluginsdk.EventCronHourly) {
		t.Errorf("events %v", m.Events)
	}
	if !slices.Equal(m.Assets["js"], []string{"assets/instagram.js"}) || !slices.Equal(m.Assets["css"], []string{"assets/instagram.css"}) {
		t.Errorf("assets %v", m.Assets)
	}
	if len(oauth.OAuth) != 1 {
		t.Fatal(oauth.OAuth)
	}
	o := oauth.OAuth[0]
	if !strings.HasPrefix(o.UserinfoURL, graphBase+"me?") || o.Exchange.GrantType != "ig_exchange_token" ||
		o.Refresh.GrantType != "ig_refresh_token" || o.Refresh.Token != "access_token" {
		t.Errorf("oauth %+v", o)
	}
	for _, s := range m.Settings {
		if s.Key == "count" && s.Default != "8" || s.Key == "layout" && s.Default != layoutCarousel || s.Key == "publish" && s.Default != "true" {
			t.Errorf("setting %+v", s)
		}
	}
}

func TestParseTime(t *testing.T) {
	for _, s := range []string{"2026-10-17T09:30:00+0000", "2026-10-17T11:30:00+02:00", "2026-10-17T09:30:00Z"} {
		if got, ok := parseTime(s); !ok || !got.Equal(time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)) {
			t.Errorf("%s: %v %v", s, got, ok)
		}
	}
	if _, ok := parseTime("yesterday"); ok {
		t.Error("garbage parsed")
	}
}

func TestURLChecks(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://scontent-mad1-1.cdninstagram.com/v/1.jpg": true,
		"https://scontent.xx.fbcdn.net/v/1.jpg":            true,
		"https://cdninstagram.com.evil.example/1.jpg":      false,
		"http://scontent.cdninstagram.com/1.jpg":           false,
		"https://user@scontent.cdninstagram.com/1.jpg":     false,
		"https://scontent.cdninstagram.com:8443/1.jpg":     false,
		"https://evilcdninstagram.com/1.jpg":               false,
	} {
		if allowedImage(raw) != ok {
			t.Errorf("allowedImage(%s) = %v", raw, !ok)
		}
	}
	for raw, want := range map[string]string{
		"https://www.instagram.com/p/AAA/":   "https://www.instagram.com/p/AAA/",
		"https://instagram.com/reel/BBB/":    "https://instagram.com/reel/BBB/",
		"http://www.instagram.com/p/AAA/":    "",
		"https://www.instagram.com.evil/p/A": "",
		"javascript:alert(1)":                "",
	} {
		if got := permalink(raw); got != want {
			t.Errorf("permalink(%s) = %q", raw, got)
		}
	}
	if profile("sap13ns") != "https://www.instagram.com/sap13ns/" || profile(`a"><script>`) != "" || profile("") != "" {
		t.Error("profile")
	}
}
