// Package sdktest runs a plugin's handler natively, without WebAssembly,
// against fake host functions, so plugin authors can use plain `go test`:
//
//	func TestHello(t *testing.T) {
//		env := sdktest.New(t)
//		env.Plugin.Settings["greeting"] = "Hola"
//		html, err := env.Shortcode("hello", map[string]string{"name": "Ada"}, "")
//		if err != nil || html != "<p>Hola, Ada</p>" {
//			t.Fatalf("got %q, %v", html, err)
//		}
//	}
//
// The handler is the one registered with pluginsdk.Handle in the plugin's
// init(), and requests go through the same JSON envelope the host sends.
// The fakes follow the host's rules (key format, quotas, per-request call
// limits, the mail recipient allowlist, the http: host allowlist, denied
// calls without permission, word search) so that tests catch what would
// fail in production. An Env replaces global state: tests that
// use it must not run in parallel.
package sdktest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
	"github.com/vicktor/sap13ns/pluginsdk/internal/abi"
)

// LogLine is a line written with pluginsdk.Log.
type LogLine struct {
	Level pluginsdk.Level
	Text  string
}

// Env is a fake host. Change its fields before calling the handler.
type Env struct {
	Site   pluginsdk.Site
	Plugin pluginsdk.PluginInfo // ID, Version and Settings sent with every request

	// KV is the plugin's private storage.
	KV map[string][]byte
	// Posts are the published posts QueryPosts filters.
	Posts []pluginsdk.Post
	// HTTP answers Fetch. When nil every request is denied, as for a plugin
	// without an "http:" permission.
	HTTP func(req pluginsdk.HTTPRequest) (*pluginsdk.HTTPResponse, error)
	// HTTPHosts are the hosts of the plugin's "http:" permissions, as in
	// plugin.json ("api.example.com", "*.example.com", "*"). When set,
	// requests to any other host are denied, as the host does.
	HTTPHosts []string
	// MailRecipients are the addresses saved in the plugin's "email"
	// settings; pluginsdk.MailToAdmin is always accepted.
	MailRecipients []string
	// Mails records every accepted SendMail call.
	Mails []pluginsdk.Mail
	// Logs records every pluginsdk.Log call.
	Logs []LogLine
	// Denied simulates permissions that were not granted: "kv",
	// "posts:read", "http" or "mail".
	Denied map[string]bool

	t testing.TB

	calls counts // host calls in the current request
}

// counts are the host calls made during one request.
type counts struct{ kv, queries, http, mails int }

// Per-request limits, as in the host.
const (
	maxKVOps   = 200
	maxQueries = 10
	maxHTTP    = 4
	maxMails   = 5
)

var mu sync.Mutex // one Env at a time: the SDK host is global

// New installs a fresh fake host until the end of the test.
func New(t testing.TB) *Env {
	t.Helper()
	mu.Lock()
	prev := abi.Current
	e := &Env{
		Site:   pluginsdk.Site{URL: "https://example.test", Title: "Example", Language: "en", Timezone: "UTC"},
		Plugin: pluginsdk.PluginInfo{ID: "test-plugin", Version: "0.0.0", Settings: map[string]string{}},
		KV:     map[string][]byte{},
		Denied: map[string]bool{},
		t:      t,
	}
	abi.Current = e
	t.Cleanup(func() {
		abi.Current = prev
		mu.Unlock()
	})
	return e
}

// Response is a decoded handler response.
type Response struct {
	HTML    string            `json:"html"`
	MaxAge  int               `json:"max_age"` // seconds, from pluginsdk.HTMLMaxAge
	Meta    []pluginsdk.Meta  `json:"meta"`
	JSONLD  []json.RawMessage `json:"jsonld"`
	Verdict string            `json:"verdict"`
	Reason  string            `json:"reason"`
	OK      bool              `json:"ok"`
	Message string            `json:"message"`
	Errors  map[string]string `json:"errors"`
	Error   string            `json:"error"`
	Raw     json.RawMessage   `json:"-"`
}

// Call sends one request for hook with payload and decodes the response.
// A handler error (or panic) is returned as an error.
func (e *Env) Call(hook string, payload any) (*Response, error) {
	return e.call(hook, "", payload)
}

// Event delivers an event (the plugin must handle pluginsdk.HookEvent).
func (e *Env) Event(name string, payload any) (*Response, error) {
	return e.call(pluginsdk.HookEvent, name, payload)
}

func (e *Env) call(hook, event string, payload any) (*Response, error) {
	e.t.Helper()
	if abi.Dispatch == nil {
		e.t.Fatal("sdktest: no handler registered; call pluginsdk.Handle from the plugin's init()")
	}
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	settings := map[string]string{}
	for k, v := range e.Plugin.Settings {
		settings[k] = v
	}
	in, err := json.Marshal(map[string]any{
		"abi": pluginsdk.ABIVersion, "hook": hook, "event": event, "site": e.Site,
		"plugin":  pluginsdk.PluginInfo{ID: e.Plugin.ID, Version: e.Plugin.Version, Settings: settings},
		"payload": json.RawMessage(p),
	})
	if err != nil {
		return nil, err
	}
	e.calls = counts{}
	out := abi.Dispatch(in)
	resp := &Response{Raw: out}
	if err := json.Unmarshal(out, resp); err != nil {
		return nil, fmt.Errorf("sdktest: the handler returned invalid JSON: %w", err)
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// Shortcode renders [name attrs]inner[/name].
func (e *Env) Shortcode(name string, attrs map[string]string, inner string) (string, error) {
	if attrs == nil {
		attrs = map[string]string{}
	}
	r, err := e.Call(pluginsdk.HookShortcode, pluginsdk.Shortcode{Name: name, Attrs: attrs, Inner: inner})
	if err != nil {
		return "", err
	}
	return r.HTML, nil
}

// Widget renders a widget declared in plugin.json.
func (e *Env) Widget(id string, config map[string]string) (string, error) {
	if config == nil {
		config = map[string]string{}
	}
	r, err := e.Call(pluginsdk.HookWidget, pluginsdk.Widget{Widget: id, Config: config,
		Page: &pluginsdk.Page{Kind: "home", URL: e.Site.URL + "/", Lang: e.Site.Language, Title: e.Site.Title}})
	if err != nil {
		return "", err
	}
	return r.HTML, nil
}

// Filter runs content.filter for a page. An empty response leaves the HTML
// unchanged, as in the host.
func (e *Env) Filter(html string, post *pluginsdk.Post) (string, error) {
	return e.FilterIn(pluginsdk.ContextPage, html, post)
}

// FilterIn runs content.filter for a page or a feed (pluginsdk.ContextFeed).
func (e *Env) FilterIn(context, html string, post *pluginsdk.Post) (string, error) {
	r, err := e.Call(pluginsdk.HookContentFilter, pluginsdk.ContentFilter{HTML: html, Post: post, Context: context})
	if err != nil {
		return html, err
	}
	if !strings.Contains(string(r.Raw), `"html"`) {
		return html, nil
	}
	return r.HTML, nil
}

// Head runs head.meta for page.
func (e *Env) Head(page *pluginsdk.Page) (*Response, error) {
	return e.Call(pluginsdk.HookHeadMeta, page)
}

// CheckComment runs comment.check. An empty response means "ok".
func (e *Env) CheckComment(c pluginsdk.CommentCheck) (verdict, reason string, err error) {
	r, err := e.Call(pluginsdk.HookCommentCheck, c)
	if err != nil {
		return "", "", err
	}
	if r.Verdict == "" {
		r.Verdict = pluginsdk.VerdictOK
	}
	return r.Verdict, r.Reason, nil
}

// SubmitForm runs form.submit with already validated fields.
func (e *Env) SubmitForm(form string, fields map[string]string) (*pluginsdk.FormResult, error) {
	r, err := e.Call(pluginsdk.HookFormSubmit, pluginsdk.FormSubmission{Form: form, Fields: fields,
		Lang: e.Site.Language, PageURL: e.Site.URL + "/contact"})
	if err != nil {
		return nil, err
	}
	return &pluginsdk.FormResult{OK: r.OK, Message: r.Message, Errors: r.Errors}, nil
}

// ------------------------------------------------------------ fake host --

const (
	kvMaxBytes = 1 << 20
	kvMaxKeys  = 1000
	kvMaxValue = 256 << 10
)

var kvKeyRe = regexp.MustCompile(`^[\x21-\x7e]{1,200}$`)

func (e *Env) Log(level uint32, msg []byte) {
	e.Logs = append(e.Logs, LogLine{Level: pluginsdk.Level(level), Text: string(msg)})
}

// kvOp counts a key-value operation and reports the code that refuses it.
func (e *Env) kvOp() int32 {
	if e.Denied["kv"] {
		return abi.CodeDenied
	}
	if e.calls.kv++; e.calls.kv > maxKVOps {
		return abi.CodeLimit
	}
	return 0
}

func (e *Env) KVGet(key []byte) ([]byte, int32) {
	if code := e.kvOp(); code != 0 {
		return nil, code
	}
	if !kvKeyRe.Match(key) {
		return nil, abi.CodeInvalid
	}
	v, ok := e.KV[string(key)]
	if !ok {
		return nil, abi.CodeNotFound
	}
	return slices.Clone(v), 0
}

func (e *Env) KVSet(key, value []byte) int32 {
	if code := e.kvOp(); code != 0 {
		return code
	}
	if !kvKeyRe.Match(key) || len(value) > kvMaxValue {
		return abi.CodeInvalid
	}
	used, keys := 0, 0
	for k, v := range e.KV {
		if k != string(key) {
			used += len(v)
			keys++
		}
	}
	if used+len(value) > kvMaxBytes || keys+1 > kvMaxKeys {
		return abi.CodeQuota
	}
	e.KV[string(key)] = slices.Clone(value)
	return 0
}

func (e *Env) KVDelete(key []byte) int32 {
	if code := e.kvOp(); code != 0 {
		return code
	}
	if !kvKeyRe.Match(key) {
		return abi.CodeInvalid
	}
	delete(e.KV, string(key))
	return 0
}

func (e *Env) PostsQuery(raw []byte) ([]byte, int32) {
	if e.Denied["posts:read"] {
		return nil, abi.CodeDenied
	}
	if e.calls.queries++; e.calls.queries > maxQueries {
		return nil, abi.CodeLimit
	}
	var q pluginsdk.PostsQuery
	if err := json.Unmarshal(raw, &q); err != nil {
		return nil, abi.CodeInvalid
	}
	if q.Limit <= 0 || q.Limit > 20 {
		q.Limit = 5
	}
	typ := "post"
	if q.Type == "page" {
		typ = "page"
	}
	hasTerm := func(p pluginsdk.Post, tax, slug string) bool {
		for _, t := range p.Terms {
			if t.Taxonomy == tax && t.Slug == slug {
				return true
			}
		}
		return false
	}
	match := searchMatcher(q.Search)
	out := []pluginsdk.Post{}
	for _, p := range e.Posts {
		pt := p.Type
		if pt == "" {
			pt = "post"
		}
		switch {
		case pt != typ, q.Lang != "" && p.Lang != q.Lang,
			q.Category != "" && !hasTerm(p, "category", q.Category),
			q.Tag != "" && !hasTerm(p, "tag", q.Tag),
			q.Exclude == p.ID, slices.Contains(q.ExcludeIDs, p.ID),
			!match(p.Title + " " + p.Excerpt):
			continue
		}
		out = append(out, p)
		if len(out) == q.Limit {
			break
		}
	}
	b, _ := json.Marshal(out)
	return b, 0
}

func (e *Env) HTTPFetch(raw []byte) ([]byte, int32) {
	if e.HTTP == nil || e.Denied["http"] {
		return nil, abi.CodeDenied
	}
	if e.calls.http++; e.calls.http > maxHTTP {
		return nil, abi.CodeLimit
	}
	var req pluginsdk.HTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, abi.CodeInvalid
	}
	u, err := url.Parse(req.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		b, _ := json.Marshal(pluginsdk.HTTPResponse{Error: "only absolute https:// URLs without credentials are allowed"})
		return b, 0
	}
	if len(e.HTTPHosts) > 0 && !hostAllowed(e.HTTPHosts, u.Hostname()) {
		return nil, abi.CodeDenied
	}
	resp, err := e.HTTP(req)
	if err != nil {
		b, _ := json.Marshal(pluginsdk.HTTPResponse{Error: err.Error()})
		return b, 0
	}
	if resp == nil {
		resp = &pluginsdk.HTTPResponse{Status: 200}
	}
	b, _ := json.Marshal(resp)
	return b, 0
}

func (e *Env) MailSend(raw []byte) int32 {
	if e.Denied["mail"] {
		return abi.CodeDenied
	}
	if e.calls.mails++; e.calls.mails > maxMails {
		return abi.CodeLimit
	}
	var m pluginsdk.Mail
	if err := json.Unmarshal(raw, &m); err != nil {
		return abi.CodeInvalid
	}
	if strings.TrimSpace(m.Subject) == "" || len(m.Subject) > 200 || strings.ContainsAny(m.Subject+m.To+m.ReplyTo, "\r\n") || len(m.Body) > 64<<10 {
		return abi.CodeInvalid
	}
	to := strings.ToLower(strings.TrimSpace(m.To))
	if to != pluginsdk.MailToAdmin && !slices.ContainsFunc(e.MailRecipients, func(a string) bool { return strings.EqualFold(a, to) }) {
		return abi.CodeDenied
	}
	e.Mails = append(e.Mails, m)
	return 0
}

// hostAllowed matches a host against "http:" permission hosts, as the host
// does: "*" is any host, "*.example.com" its subdomains only.
func hostAllowed(hosts []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range hosts {
		pat = strings.ToLower(strings.TrimPrefix(pat, "http:"))
		switch {
		case pat == "*", pat == host:
			return true
		case strings.HasPrefix(pat, "*.") && strings.HasSuffix(host, pat[1:]):
			return true
		}
	}
	return false
}

// searchMatcher approximates the host's posts_query search
// (websearch_to_tsquery with the "simple" configuration): whole words,
// ignoring case; every word must appear, "-word" must not, and "or"
// separates alternatives. An empty search matches everything.
func searchMatcher(q string) func(text string) bool {
	type group struct{ want, not []string }
	var groups []group
	cur := group{}
	for _, tok := range strings.Fields(strings.ToLower(q)) {
		if tok == "or" {
			groups, cur = append(groups, cur), group{}
			continue
		}
		neg := strings.HasPrefix(tok, "-")
		for _, w := range words(tok) {
			if neg {
				cur.not = append(cur.not, w)
			} else {
				cur.want = append(cur.want, w)
			}
		}
	}
	groups = append(groups, cur)
	return func(text string) bool {
		have := map[string]bool{}
		for _, w := range words(strings.ToLower(text)) {
			have[w] = true
		}
		for _, g := range groups {
			if len(g.want) == 0 && len(g.not) == 0 {
				continue
			}
			ok := true
			for _, w := range g.want {
				ok = ok && have[w]
			}
			for _, w := range g.not {
				ok = ok && !have[w]
			}
			if ok {
				return true
			}
		}
		return strings.TrimSpace(q) == ""
	}
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
