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
// The fakes follow the host's rules (key format, quotas, the mail
// recipient allowlist, denied calls without permission) so that tests catch
// what would fail in production. An Env replaces global state: tests that
// use it must not run in parallel.
package sdktest

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

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
}

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

func (e *Env) KVGet(key []byte) ([]byte, int32) {
	if e.Denied["kv"] {
		return nil, abi.CodeDenied
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
	if e.Denied["kv"] {
		return abi.CodeDenied
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
	if e.Denied["kv"] {
		return abi.CodeDenied
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
			q.Search != "" && !strings.Contains(strings.ToLower(p.Title+" "+p.Excerpt), strings.ToLower(q.Search)):
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
	var req pluginsdk.HTTPRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, abi.CodeInvalid
	}
	if !strings.HasPrefix(req.URL, "https://") {
		b, _ := json.Marshal(pluginsdk.HTTPResponse{Error: "only absolute https:// URLs without credentials are allowed"})
		return b, 0
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
