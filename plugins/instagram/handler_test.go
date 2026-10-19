package main

import (
	"encoding/json"
	"net/url"
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
	secretToken = "IGAA-token-SECRET"
	igID        = "17841400000000001"
)

// fakeGraph answers like graph.instagram.com and records every request.
type fakeGraph struct {
	reqs []pluginsdk.HTTPRequest
	// The answers; zero values give a successful flow.
	container, status, publish, media []answer
}

type answer struct {
	status int
	body   string
	err    error
}

// next pops the first answer of a list, or gives def.
func next(list *[]answer, def answer) answer {
	if len(*list) == 0 {
		return def
	}
	a := (*list)[0]
	*list = (*list)[1:]
	return a
}

func (f *fakeGraph) fetch(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error) {
	f.reqs = append(f.reqs, req)
	u, _ := url.Parse(req.URL)
	var a answer
	switch {
	case req.Method == "POST" && u.Path == "/v26.0/"+igID+"/media":
		a = next(&f.container, answer{200, `{"id":"18000000000000001"}`, nil})
	case req.Method == "GET" && u.Path == "/v26.0/18000000000000001":
		a = next(&f.status, answer{200, `{"status_code":"FINISHED","id":"18000000000000001"}`, nil})
	case req.Method == "POST" && u.Path == "/v26.0/"+igID+"/media_publish":
		a = next(&f.publish, answer{200, `{"id":"18000000000000099"}`, nil})
	case req.Method == "GET" && u.Path == "/v26.0/"+igID+"/media":
		a = next(&f.media, answer{200, feedJSON, nil})
	default:
		a = answer{404, `{"error":{"message":"Unknown path","code":803}}`, nil}
	}
	if a.err != nil {
		return nil, a.err
	}
	return &pluginsdk.HTTPResponse{Status: a.status, Body: a.body}, nil
}

// paths lists the requests as "METHOD /path".
func (f *fakeGraph) paths() []string {
	var out []string
	for _, r := range f.reqs {
		u, _ := url.Parse(r.URL)
		out = append(out, r.Method+" "+u.Path)
	}
	return out
}

const feedJSON = `{"data":[
 {"id":"1","media_type":"IMAGE","media_url":"https://scontent-mad1-1.cdninstagram.com/v/t51.29350-15/1_n.jpg?stp=dst-jpg&oe=6720","permalink":"https://www.instagram.com/p/AAA/","timestamp":"2026-10-17T09:30:00+0000","caption":"Sunset in Málaga #sunset","alt_text":""},
 {"id":"2","media_type":"VIDEO","media_url":"https://scontent.cdninstagram.com/o1/v/2.mp4","thumbnail_url":"https://scontent-mad2-1.cdninstagram.com/v/t51/2_n.jpg?oe=1","permalink":"https://www.instagram.com/reel/BBB/","timestamp":"2026-10-16T09:30:00+0000","caption":"A reel","alt_text":""},
 {"id":"3","media_type":"CAROUSEL_ALBUM","media_url":"https://scontent.xx.fbcdn.net/v/3_n.jpg","permalink":"https://www.instagram.com/p/CCC/","timestamp":"2026-10-15T09:30:00+0000","caption":"","alt_text":"Three cups of coffee"},
 {"id":"4","media_type":"VIDEO","permalink":"https://www.instagram.com/reel/DDD/","timestamp":"2026-10-14T09:30:00+0000"},
 {"id":"5","media_type":"IMAGE","media_url":"https://evil.example/5.jpg","permalink":"https://www.instagram.com/p/EEE/","timestamp":"2026-10-13T09:30:00+0000"},
 {"id":"6","media_type":"IMAGE","media_url":"https://scontent.cdninstagram.com/6.jpg","permalink":"javascript:alert(1)"},
 {"id":"7","media_type":"STORY","media_url":"https://scontent.cdninstagram.com/7.jpg","permalink":"https://www.instagram.com/p/GGG/"}
],"paging":{"cursors":{"before":"a","after":"b"}}}`

func setup(t *testing.T) (*sdktest.Env, *fakeGraph) {
	t.Helper()
	env := sdktest.New(t)
	env.Plugin.ID = "instagram"
	env.Plugin.Settings = map[string]string{"app_id": "123", "app_secret": "app-SECRET", "publish": "true", "max_hashtags": "5",
		"count": "8", "layout": "carousel", "show_notice": "true"}
	env.HTTPHosts = []string{"graph.instagram.com", "api.instagram.com", "www.instagram.com"}
	env.OAuth[provider] = pluginsdk.OAuthToken{AccessToken: secretToken, AccountID: igID, AccountName: "sap13ns",
		Scopes: []string{"instagram_business_basic", "instagram_business_content_publish"}, ExpiresAt: "2099-01-01T00:00:00Z"}
	g := &fakeGraph{}
	env.HTTP = g.fetch
	setClock(t, t0)
	prev := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = prev })
	logged = map[string]bool{}
	return env, g
}

func setClock(t *testing.T, at time.Time) {
	prev := now
	now = func() time.Time { return at }
	t.Cleanup(func() { now = prev })
}

func samplePost() pluginsdk.Post {
	return pluginsdk.Post{
		ID: 12, Type: "post", Lang: "es", Title: "Hola, ¿qué tal?", Slug: "hola",
		URL: "https://example.test/es/hola", Excerpt: "Un saludo desde Málaga.", Image: "https://example.test/media/2026/10/playa.JPG",
		Tags: []string{"open source", "Go"}, PublishedAt: t0.Add(-time.Minute).Format(time.RFC3339),
	}
}

func event(t *testing.T, env *sdktest.Env, name string, payload any) {
	t.Helper()
	if _, err := env.Event(name, payload); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func logs(env *sdktest.Env) string {
	var b strings.Builder
	for _, l := range env.Logs {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func wantLog(t *testing.T, env *sdktest.Env, subs ...string) {
	t.Helper()
	all := logs(env)
	for _, s := range subs {
		if !strings.Contains(all, s) {
			t.Errorf("log lacks %q:\n%s", s, all)
		}
	}
	if strings.Contains(all, secretToken) || strings.Contains(all, "app-SECRET") {
		t.Error("the log contains a secret")
	}
}

func state(env *sdktest.Env) string {
	var recs map[string]map[string]any
	json.Unmarshal(env.KV[autopost.KeyRecords], &recs)
	s, _ := recs["12"]["i"].(string)
	return s
}

func queued(env *sdktest.Env) []autopost.Item {
	var q []autopost.Item
	json.Unmarshal(env.KV[autopost.KeyQueue], &q)
	return q
}

func storedFeed(t *testing.T, env *sdktest.Env) Feed {
	t.Helper()
	var f Feed
	if raw, ok := env.KV[feedKey]; ok {
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestPublishesWithTheFeaturedImage(t *testing.T) {
	env, g := setup(t)
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	event(t, env, pluginsdk.EventPostPublished, p) // never twice
	want := []string{"POST /v26.0/" + igID + "/media", "GET /v26.0/18000000000000001", "POST /v26.0/" + igID + "/media_publish"}
	if got := g.paths(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("requests %v, want %v", got, want)
	}
	for _, r := range g.reqs {
		if r.Headers["Authorization"] != "Bearer "+secretToken || strings.Contains(r.URL, secretToken) {
			t.Errorf("%s %s: the token goes in the Authorization header only: %v", r.Method, r.URL, r.Headers)
		}
	}
	var c Container
	if err := json.Unmarshal([]byte(g.reqs[0].Body), &c); err != nil || g.reqs[0].Headers["Content-Type"] != "application/json" {
		t.Fatalf("container body %q: %v", g.reqs[0].Body, err)
	}
	wantCaption := "Nueva entrada: Hola, ¿qué tal?\n\nUn saludo desde Málaga.\n\nEnlace en la bio.\n\n#OpenSource #Go"
	if c.ImageURL != p.Image || c.Caption != wantCaption {
		t.Errorf("container %+v\nwant caption %q", c, wantCaption)
	}
	if q := g.reqs[1].URL; !strings.HasSuffix(q, "?fields=status_code,status") {
		t.Errorf("status request %s", q)
	}
	if g.reqs[2].Body != `{"creation_id":"18000000000000001"}` {
		t.Errorf("publish body %s", g.reqs[2].Body)
	}
	if st := state(env); st != "d1" {
		t.Errorf("record %q, want d1", st)
	}
	wantLog(t, env, "posted to Instagram: media 18000000000000099 (https://www.instagram.com/sap13ns/)", "already posted to Instagram")
}

func TestEnglishCaptionAndTemplates(t *testing.T) {
	env, g := setup(t)
	p := samplePost()
	p.Lang, p.Title, p.Excerpt = "en", "Hello", "Greetings."
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	var c Container
	json.Unmarshal([]byte(g.reqs[0].Body), &c)
	if c.Caption != "New post: Hello\n\nGreetings.\n\nLink in bio.\n\n#OpenSource #Go" {
		t.Errorf("caption %q", c.Caption)
	}
	if got := defaultTemplate("es", "page"); !strings.Contains(got, "Nueva página") || !strings.Contains(got, "Enlace en la bio.") {
		t.Errorf("Spanish page template %q", got)
	}
	if got := defaultTemplate("en", "page"); !strings.Contains(got, "New page") || !strings.Contains(got, "Link in bio.") {
		t.Errorf("English page template %q", got)
	}
}

func TestPostsWithoutAJPEGAreSkipped(t *testing.T) {
	for _, tc := range []struct{ image, why string }{
		{"", "Instagram: not posted: it has no featured image (an Instagram post needs one)"},
		{"https://example.test/media/a.png", "Instagram: not posted: its featured image is a PNG file; Instagram publishes only JPEG images"},
		{"https://example.test/media/a.webp?x=.jpg", "its featured image is a WEBP file"},
		{"https://example.test/media/a", "its featured image is not a JPEG"},
		{"/media/a.jpg", "its featured image has no public address"},
	} {
		t.Run(tc.image, func(t *testing.T) {
			env, g := setup(t)
			p := samplePost()
			p.Image = tc.image
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			if len(g.reqs) != 0 || state(env) != "" {
				t.Errorf("requests %v, record %q", g.paths(), state(env))
			}
			wantLog(t, env, tc.why)
		})
	}
	if skipPost(pluginsdk.Post{Image: "https://example.test/a.jpeg"}) != "" || skipPost(pluginsdk.Post{Image: "http://example.test/a.jpg"}) != "" {
		t.Error("JPEG images must be accepted")
	}
}

func TestCaptionLimits(t *testing.T) {
	if captionLimit.Max != 2200 {
		t.Fatal(captionLimit.Max)
	}
	if n := captionLen("ab😀"); n != 4 {
		t.Errorf("an emoji counts as 2 UTF-16 units: got %d", n)
	}
	tags := strings.Repeat("#tag ", 30)
	if captionLen(tags) > captionMax {
		t.Error("30 hashtags are allowed")
	}
	if captionLen(tags+"#one") <= captionMax {
		t.Error("31 hashtags are over the limit")
	}
	mentions := strings.Repeat("@ana ", 21)
	if captionLen(mentions) <= captionMax || captionLen(strings.Repeat("me@example.com ", 25)) > captionMax {
		t.Error("21 @ tags are over the limit, e-mail addresses are not @ tags")
	}

	env, g := setup(t)
	p := samplePost()
	p.Excerpt = strings.Repeat("palabra ", 400) + strings.Repeat("#x ", 40)
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	var c Container
	json.Unmarshal([]byte(g.reqs[0].Body), &c)
	if captionLen(c.Caption) > captionMax || !strings.Contains(c.Caption, "Enlace en la bio.") || !strings.HasSuffix(c.Caption, "#OpenSource #Go") {
		t.Errorf("caption of %d units: %q", captionLen(c.Caption), c.Caption)
	}
}

func TestContainerStates(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		env, g := setup(t)
		g.status = []answer{{200, `{"status_code":"ERROR","status":"Error: 2207004"}`, nil}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 2 || state(env) != "f1" || len(queued(env)) != 1 {
			t.Errorf("requests %v, record %q, queue %v", g.paths(), state(env), queued(env))
		}
		wantLog(t, env, "Instagram failed (attempt 1 of 3): Instagram could not process the featured image (Error: 2207004). The image must be a public JPEG of at most 8 MB")
	})
	t.Run("still processing", func(t *testing.T) {
		env, g := setup(t)
		var waits []time.Duration
		sleep = func(d time.Duration) {
			if d == processWait || d == retryWait {
				waits = append(waits, d)
			}
		}
		g.status = []answer{{200, `{"status_code":"IN_PROGRESS"}`, nil}}
		g.publish = []answer{{400, `{"error":{"message":"Media ID is not available","code":9007,"error_subcode":2207027}}`, nil}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 3 || len(waits) != 2 || waits[0] != processWait || waits[1] != retryWait {
			t.Errorf("requests %v, waits %d", g.paths(), waits)
		}
		// A later try that does not count as an attempt.
		if st := state(env); st != "f0" {
			t.Errorf("record %q, want f0", st)
		}
		if q := queued(env); len(q) != 1 || q[0].At != t0.Add(10*time.Minute).Unix() {
			t.Errorf("queue %+v", q)
		}
		wantLog(t, env, "Instagram asked to wait: Instagram is still processing the featured image (Media ID is not available: (code 9007/2207027))")
	})
	t.Run("status unreadable", func(t *testing.T) {
		env, g := setup(t)
		g.status = []answer{{500, `{}`, nil}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 3 || state(env) != "d1" {
			t.Errorf("requests %v, record %q", g.paths(), state(env))
		}
	})
	t.Run("expired", func(t *testing.T) {
		env, g := setup(t)
		g.status = []answer{{200, `{"status_code":"EXPIRED"}`, nil}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 2 || state(env) != "f1" {
			t.Errorf("requests %v, record %q", g.paths(), state(env))
		}
	})
	t.Run("no answer to the container is retried", func(t *testing.T) {
		env, g := setup(t)
		g.container = []answer{{err: errTimeout}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 1 || state(env) != "f1" {
			t.Errorf("requests %v, record %q", g.paths(), state(env))
		}
	})
	t.Run("no answer to media_publish is never retried", func(t *testing.T) {
		env, g := setup(t)
		g.publish = []answer{{err: errTimeout}}
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if state(env) != "s1" || len(queued(env)) != 0 {
			t.Errorf("record %q, queue %v", state(env), queued(env))
		}
		wantLog(t, env, "whether it was posted is unknown, so it is not retried")
	})
}

type timeoutError struct{}

func (timeoutError) Error() string {
	return "context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
}

var errTimeout = timeoutError{}

func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		later      time.Duration
		want       string
	}{
		{"token", `{"error":{"message":"Error validating access token","code":190}}`, 400, 0, "connect Instagram again on the plugin's page"},
		{"permission", `{"error":{"message":"Application does not have permission for this action","code":10}}`, 403, 0, "lacks instagram_business_content_publish"},
		{"fetch", `{"error":{"message":"Media download has failed.","code":9004,"error_subcode":2207052}}`, 400, 0, "the site must be reachable from the internet. The image must be a public JPEG"},
		{"ratio", `{"error":{"message":"The aspect ratio is not supported.","code":36003,"error_subcode":2207009}}`, 400, 0, "between 4:5 (portrait) and 1.91:1 (landscape)"},
		{"spam", `{"error":{"message":"Action blocked","code":4,"error_subcode":2207051}}`, 400, 0, "flagged the activity as spam"},
		{"rate", `{"error":{"message":"Application request limit reached","code":4}}`, 400, time.Hour, "rate limited (HTTP 400: Application request limit reached"},
		{"user rate", `{"error":{"message":"User request limit reached","code":17}}`, 400, time.Hour, "rate limited"},
		{"publishing limit", `{"error":{"message":"The maximum number of posts has been reached.","code":9,"error_subcode":2207042}}`, 400, time.Hour, "the account reached Instagram's limit of published posts in 24 hours"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, g := setup(t)
			g.container = []answer{{tc.status, tc.body, nil}}
			p := samplePost()
			env.Posts = []pluginsdk.Post{p}
			event(t, env, pluginsdk.EventPostPublished, p)
			wantLog(t, env, tc.want)
			q := queued(env)
			if len(q) != 1 {
				t.Fatalf("queue %v", q)
			}
			if tc.later != 0 {
				if q[0].At != t0.Add(tc.later).Unix() || state(env) != "f0" {
					t.Errorf("queued at %d, record %q", q[0].At, state(env))
				}
			} else if state(env) != "f1" {
				t.Errorf("record %q", state(env))
			}
		})
	}
}

func TestConnection(t *testing.T) {
	t.Run("not connected", func(t *testing.T) {
		env, g := setup(t)
		delete(env.OAuth, provider)
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		if len(g.reqs) != 0 {
			t.Error(g.paths())
		}
		wantLog(t, env, "not posted: Instagram is not connected: connect it in Admin → Plugins → Instagram → Connections")
	})
	t.Run("expired", func(t *testing.T) {
		env, _ := setup(t)
		tok := env.OAuth[provider]
		tok.ExpiresAt = "2020-01-01T00:00:00Z"
		env.OAuth[provider] = tok
		p := samplePost()
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventPostPublished, p)
		wantLog(t, env, "not posted: the Instagram connection ended")
	})
	t.Run("queued posts wait for the connection", func(t *testing.T) {
		env, g := setup(t)
		p := samplePost()
		p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339) // scheduled
		event(t, env, pluginsdk.EventPostPublished, p)
		delete(env.OAuth, provider)
		setClock(t, t0.Add(2*time.Hour))
		p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
		env.Posts = []pluginsdk.Post{p}
		event(t, env, pluginsdk.EventCronHourly, map[string]string{})
		if len(g.reqs) != 0 || len(queued(env)) != 1 {
			t.Errorf("requests %v, queue %v", g.paths(), queued(env))
		}
		wantLog(t, env, "hourly run: 1 queued post(s) wait: Instagram is not connected")
		env.OAuth[provider] = pluginsdk.OAuthToken{AccessToken: secretToken, AccountID: igID, AccountName: "sap13ns"}
		event(t, env, pluginsdk.EventCronHourly, map[string]string{})
		want := "GET /v26.0/" + igID + "/media|POST /v26.0/" + igID + "/media|GET /v26.0/18000000000000001|POST /v26.0/" + igID + "/media_publish"
		if got := strings.Join(g.paths(), "|"); got != want {
			t.Errorf("requests %s, want %s", got, want)
		}
		if state(env) != "d1" || len(queued(env)) != 0 {
			t.Errorf("record %q, queue %v", state(env), queued(env))
		}
	})
}

func TestPublishingOff(t *testing.T) {
	env, g := setup(t)
	env.Plugin.Settings["publish"] = "false"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(g.reqs) != 0 || len(env.KV) != 0 || len(env.Logs) != 0 {
		t.Errorf("requests %v, kv %v, logs %v", g.paths(), env.KV, env.Logs)
	}
	event(t, env, pluginsdk.EventCronHourly, map[string]string{})
	if strings.Join(g.paths(), "|") != "GET /v26.0/"+igID+"/media" {
		t.Errorf("the feed is still refreshed: %v", g.paths())
	}
}

func TestDryRun(t *testing.T) {
	env, g := setup(t)
	env.Plugin.Settings["dry_run"] = "true"
	p := samplePost()
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	if len(g.reqs) != 0 || state(env) != "" {
		t.Errorf("requests %v, record %q", g.paths(), state(env))
	}
	wantLog(t, env, `dry run, Instagram (POST https://graph.instagram.com/v26.0/<IG_ID>/media, then media_publish, as @sap13ns, 91 characters): "Nueva entrada: Hola, ¿qué tal?\n\nUn saludo desde Málaga.\n\nEnlace en la bio.\n\n#OpenSource #Go"`)
	p.Image = ""
	env.Posts = []pluginsdk.Post{p}
	event(t, env, pluginsdk.EventPostPublished, p)
	wantLog(t, env, "dry run, Instagram: not posted: it has no featured image")
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
				URL: "https://example.test/sample-post", PublishedAt: "2026-01-01T00:00:00Z", Tags: []string{"sample"},
				Image: "https://example.test/sample.jpg"}
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
			if html, err := env.Shortcode("instagram_feed", map[string]string{}, "Sample text"); err != nil || html != "" {
				t.Errorf("shortcode %q, %v", html, err)
			}
			if html, err := env.Widget("feed", map[string]string{}); err != nil || html != "" {
				t.Errorf("widget %q, %v", html, err)
			}
			if _, err := env.Call(pluginsdk.HookShortcode, "not a shortcode"); err != nil {
				t.Error(err)
			}
			wantLog(t, env, "the Instagram connection is not available (permission denied)", "the plugin's storage is not available")
		})
	}
}
