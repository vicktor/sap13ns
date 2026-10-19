package autopost

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/sdktest"
)

// The plugin behaviour (queue, scheduling, retries, budgets, dry run) is
// tested end to end through the social plugin, which used this code before
// it moved here. These tests cover what the other plugins rely on.

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// build makes the Poster of one call in the tests.
var build func(req *pluginsdk.Request) *Poster

func TestMain(m *testing.M) {
	pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) { return build(req).Handle(req) })
	os.Exit(m.Run())
}

// fakeNet is a network whose answers the test sets.
type fakeNet struct {
	key, name string
	cost      int
	ready     bool
	skip      string
	texts     []string
	answer    func(n int) Result // n: 1 for the first send
}

func (f *fakeNet) network() Network {
	n := Network{Key: f.key, Name: f.name, Cost: f.cost, Ready: f.ready, Limit: Limit{Max: 100, Len: Runes}, Target: "POST " + f.name,
		Send: func(p pluginsdk.Post, text string) Result {
			f.texts = append(f.texts, text)
			if f.answer != nil {
				return f.answer(len(f.texts))
			}
			return Result{Done: true, URL: "https://" + f.key + ".example/1"}
		}}
	if f.skip != "" {
		n.Skip = func(pluginsdk.Post) string { return f.skip }
	}
	return n
}

type harness struct {
	env   *sdktest.Env
	clock time.Time
	nets  []*fakeNet
	tune  func(p *Poster)
}

func setup(t *testing.T, nets ...*fakeNet) *harness {
	t.Helper()
	h := &harness{env: sdktest.New(t), clock: t0, nets: nets}
	h.env.Site.Language = "es"
	build = func(req *pluginsdk.Request) *Poster {
		p := &Poster{Config: ConfigFrom(req.Plugin.Settings), NotConfigured: "the account is not connected",
			Now: func() time.Time { return h.clock }, Sleep: func(time.Duration) {}}
		for _, n := range h.nets {
			p.Networks = append(p.Networks, n.network())
		}
		if h.tune != nil {
			h.tune(p)
		}
		return p
	}
	return h
}

func (h *harness) event(t *testing.T, name string, payload any) {
	t.Helper()
	if _, err := h.env.Event(name, payload); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func (h *harness) logs() string {
	var b strings.Builder
	for _, l := range h.env.Logs {
		b.WriteString(l.Text + "\n")
	}
	return b.String()
}

func (h *harness) wantLog(t *testing.T, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(h.logs(), s) {
			t.Errorf("log lacks %q:\n%s", s, h.logs())
		}
	}
}

func (h *harness) record(t *testing.T, id string) Record {
	t.Helper()
	recs, err := LoadRecords()
	if err != nil {
		t.Fatal(err)
	}
	return recs[id]
}

func (h *harness) queue(t *testing.T) []Item {
	t.Helper()
	q, err := LoadQueue()
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func post() pluginsdk.Post {
	return pluginsdk.Post{ID: 7, Type: "post", Lang: "es", Title: "Hola", URL: "https://example.test/hola",
		Excerpt: "Un saludo.", Tags: []string{"Go"}, PublishedAt: t0.Add(-time.Minute).Format(time.RFC3339)}
}

func TestPostedOnceToEveryNetwork(t *testing.T) {
	a := &fakeNet{key: "a", name: "Alpha", cost: 1, ready: true}
	b := &fakeNet{key: "bb", name: "Beta", cost: 2, ready: true}
	h := setup(t, a, b)
	p := post()
	h.env.Posts = []pluginsdk.Post{p}
	h.event(t, pluginsdk.EventPostPublished, p)
	h.event(t, pluginsdk.EventPostPublished, p)
	if len(a.texts) != 1 || len(b.texts) != 1 {
		t.Fatalf("sent %d and %d times", len(a.texts), len(b.texts))
	}
	if a.texts[0] != "Nueva entrada: Hola\n\nhttps://example.test/hola\n\n#Go" {
		t.Errorf("text %q", a.texts[0])
	}
	if got, want := string(h.env.KV[KeyRecords]), `{"7":{"t":`+strconv.FormatInt(t0.Unix(), 10)+`,"a":"d1","bb":"d1"}}`; got != want {
		t.Errorf("stored %s", got)
	}
	h.wantLog(t, "post 7 “Hola”: posted to Alpha: https://a.example/1", "already posted to Alpha and Beta; it is never posted twice")
}

func TestRecordJSON(t *testing.T) {
	var r Record
	if st, n := r.Get("b"); st != 0 || n != 0 {
		t.Errorf("empty record: %c %d", st, n)
	}
	r.Set("b", StateDone, 1)
	r.Set("m", StateFailed, 2)
	if st, n := r.Get("b"); st != StateDone || n != 1 {
		t.Errorf("b: %c %d", st, n)
	}
	b, _ := r.MarshalJSON()
	if string(b) != `{"t":0,"b":"d1","m":"f2"}` {
		t.Errorf("json %s", b)
	}
	var back Record
	if err := back.UnmarshalJSON([]byte(`{"t":5,"b":"d1","m":"f2","x":3}`)); err != nil || back.T != 5 || len(back.S) != 2 || back.S["m"] != "f2" {
		t.Errorf("%+v, %v", back, err)
	}
}

func TestLaterDoesNotCountAsAnAttempt(t *testing.T) {
	reset := t0.Add(90 * time.Minute)
	x := &fakeNet{key: "x", name: "X", cost: 1, ready: true, answer: func(n int) Result {
		if n <= 2 {
			return Result{Failed: true, Detail: "HTTP 429: Too Many Requests", Later: reset}
		}
		return Result{Done: true, URL: "https://x.example/1"}
	}}
	h := setup(t, x)
	p := post()
	h.env.Posts = []pluginsdk.Post{p}
	h.event(t, pluginsdk.EventPostPublished, p)
	if st, n := h.record(t, "7").Get("x"); st != StateFailed || n != 0 {
		t.Fatalf("record %c%d", st, n)
	}
	if q := h.queue(t); len(q) != 1 || q[0].At != reset.Unix() {
		t.Fatalf("queue %+v", q)
	}
	h.wantLog(t, "X asked to wait: HTTP 429: Too Many Requests; it will be tried again on the first hourly run after 2026-10-18 13:30 UTC (this does not count as an attempt)")

	h.clock = t0.Add(time.Hour) // before the reset: not due
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	if len(x.texts) != 1 {
		t.Fatalf("sent before the reset")
	}
	h.clock = t0.Add(2 * time.Hour)
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	h.clock = t0.Add(4 * time.Hour)
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	if len(x.texts) != 3 {
		t.Fatalf("sent %d times", len(x.texts))
	}
	if st, n := h.record(t, "7").Get("x"); st != StateDone || n != 1 || len(h.queue(t)) != 0 {
		t.Fatalf("record %c%d, queue %+v", st, n, h.queue(t))
	}
}

func TestSkippedPostIsNotRecorded(t *testing.T) {
	ig := &fakeNet{key: "i", name: "Instagram", cost: 3, ready: true, skip: "it has no featured image"}
	h := setup(t, ig)
	p := post()
	h.env.Posts = []pluginsdk.Post{p}
	h.event(t, pluginsdk.EventPostPublished, p)
	if len(ig.texts) != 0 || len(h.queue(t)) != 0 {
		t.Fatalf("sent %v, queue %+v", ig.texts, h.queue(t))
	}
	if st, _ := h.record(t, "7").Get("i"); st != 0 {
		t.Errorf("recorded %c", st)
	}
	h.wantLog(t, "post 7 “Hola”: Instagram: not posted: it has no featured image")

	h.env.Plugin.Settings["dry_run"] = "true"
	h.event(t, pluginsdk.EventPostPublished, p)
	h.wantLog(t, "dry run, Instagram: not posted: it has no featured image")
}

func TestNotReady(t *testing.T) {
	n := &fakeNet{key: "l", name: "LinkedIn", cost: 1, ready: false}
	h := setup(t, n)
	p := post()
	h.event(t, pluginsdk.EventPostPublished, p)
	h.wantLog(t, "not posted: the account is not connected")
	if len(h.env.KV) != 0 {
		t.Errorf("kv %v", h.env.KV)
	}

	// Queued while connected, due while not: kept, and sent once connected.
	n.ready = true
	p.PublishedAt = t0.Add(time.Hour).Format(time.RFC3339)
	h.event(t, pluginsdk.EventPostPublished, p)
	n.ready = false
	h.env.Posts = []pluginsdk.Post{p}
	h.clock = t0.Add(2 * time.Hour)
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	h.wantLog(t, "hourly run: 1 queued post(s) wait: the account is not connected")
	if len(h.queue(t)) != 1 || len(n.texts) != 0 {
		t.Fatalf("queue %+v, sent %v", h.queue(t), n.texts)
	}
	n.ready = true
	h.clock = t0.Add(3 * time.Hour)
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	if len(n.texts) != 1 || len(h.queue(t)) != 0 {
		t.Fatalf("queue %+v, sent %v", h.queue(t), n.texts)
	}
}

func TestHTTPUsedLeavesLessBudget(t *testing.T) {
	ig := &fakeNet{key: "i", name: "Instagram", cost: 3, ready: true}
	h := setup(t, ig)
	h.tune = func(p *Poster) { p.HTTPUsed = 2 }
	p := post()
	h.env.Posts = []pluginsdk.Post{p}
	h.event(t, pluginsdk.EventPostPublished, p)
	if len(ig.texts) != 0 || len(h.queue(t)) != 1 {
		t.Fatalf("sent %v, queue %+v", ig.texts, h.queue(t))
	}
	h.wantLog(t, "Instagram: this run used its 4 requests; it will be posted on the next hourly run")
	h.tune = nil
	h.clock = t0.Add(time.Hour)
	h.event(t, pluginsdk.EventCronHourly, map[string]string{})
	if len(ig.texts) != 1 {
		t.Fatalf("not sent on the next run")
	}
}

func TestTextAndDefaultTemplate(t *testing.T) {
	n := &fakeNet{key: "l", name: "LinkedIn", cost: 1, ready: true}
	h := setup(t, n)
	h.tune = func(p *Poster) {
		p.DefaultTemplate = func(lang, typ string) string { return "{title} ({excerpt})" }
		p.Networks[0].Text = func(tpl string, f Fields) string { return strings.ToUpper(RenderTemplate(tpl, f)) }
	}
	p := post()
	h.env.Posts = []pluginsdk.Post{p}
	h.event(t, pluginsdk.EventPostPublished, p)
	if len(n.texts) != 1 || n.texts[0] != "HOLA (UN SALUDO.)" {
		t.Errorf("%q", n.texts)
	}
}

func TestOtherEventsAreIgnored(t *testing.T) {
	n := &fakeNet{key: "a", name: "Alpha", cost: 1, ready: true}
	h := setup(t, n)
	h.event(t, pluginsdk.EventOAuthConnected, map[string]string{"provider": "x"})
	h.event(t, pluginsdk.EventCommentCreated, map[string]string{})
	if _, err := h.env.Call(pluginsdk.HookWidget, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if len(h.env.Logs) != 0 || len(h.env.KV) != 0 {
		t.Errorf("logs %v, kv %v", h.env.Logs, h.env.KV)
	}
}
