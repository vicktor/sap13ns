// Command my-plugin is a sap13ns plugin: a WebAssembly module that the CMS
// runs in a sandbox, with no access to files, the network or the database
// beyond the permissions declared in plugin.json.
//
// Every call from the host arrives in handle below, one per hook. Each
// example feature lives in its own file and registers itself in init():
//
//	hello.go     shortcode       [hello name="Ada"]
//	latest.go    widget          the newest posts, through QueryPosts
//	reading.go   content.filter  a reading-time note under posts
//	head.go      head.meta       a <meta> tag and a JSON-LD object
//	events.go    event           counts published posts in the KV store
//	comments.go  comment.check   holds comments with too many links
//	contact.go   form.submit     a contact form that e-mails the admin
//
// To drop a feature, delete its file and test, and remove its hook,
// shortcode, widget, form, permission and settings from plugin.json.
//
// Build:  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o plugin.wasm .
// Test:   go test ./...   (the SDK runs natively, against fakes in sdktest)
package main

import (
	"github.com/vicktor/sap13ns/pluginsdk"
)

// Feature registries, filled by the init() functions of the other files.
var (
	shortcodes    = map[string]func(req *pluginsdk.Request, sc pluginsdk.Shortcode) (string, error){}
	widgets       = map[string]func(req *pluginsdk.Request, w pluginsdk.Widget) (string, error){}
	events        = map[string]func(req *pluginsdk.Request) error{}
	forms         = map[string]func(req *pluginsdk.Request, f pluginsdk.FormSubmission) (any, error){}
	filters       []func(req *pluginsdk.Request, c pluginsdk.ContentFilter) (html string, changed bool)
	headTags      []func(req *pluginsdk.Request, page pluginsdk.Page) (meta []pluginsdk.Meta, jsonld []any)
	commentChecks []func(req *pluginsdk.Request, c pluginsdk.CommentCheck) (verdict, reason string)
)

func init() { pluginsdk.Handle(handle) }

// handle answers one call from the host. Return (nil, nil) when there is
// nothing to do: the host then keeps its default (no HTML, no change, "ok").
// An error is written to the plugin's activity log in the admin panel.
func handle(req *pluginsdk.Request) (any, error) {
	switch req.Hook {
	case pluginsdk.HookShortcode:
		// [name attr="value"]inner[/name] in a post, for every name listed
		// under "shortcodes" in plugin.json. Return HTML; the host
		// sanitizes it (no scripts, styles or event handlers survive).
		var sc pluginsdk.Shortcode
		if err := req.Decode(&sc); err != nil {
			return nil, err
		}
		if render := shortcodes[sc.Name]; render != nil {
			html, err := render(req, sc)
			return pluginsdk.HTML(html), err
		}

	case pluginsdk.HookWidget:
		// A widget placed in Appearance → Widgets. Config holds the
		// instance's values for the "config" fields of the widget.
		var w pluginsdk.Widget
		if err := req.Decode(&w); err != nil {
			return nil, err
		}
		if render := widgets[w.Widget]; render != nil {
			html, err := render(req, w)
			return pluginsdk.HTML(html), err
		}

	case pluginsdk.HookContentFilter:
		// The rendered HTML of every post. Return the full new HTML, or
		// nil to leave it unchanged (cheaper: the host skips sanitizing).
		var c pluginsdk.ContentFilter
		if err := req.Decode(&c); err != nil {
			return nil, err
		}
		changed := false
		for _, f := range filters {
			if html, ok := f(req, c); ok {
				c.HTML, changed = html, true
			}
		}
		if changed {
			return pluginsdk.HTML(c.HTML), nil
		}

	case pluginsdk.HookHeadMeta:
		// Extra <meta> tags and JSON-LD objects for the <head> of a page.
		var page pluginsdk.Page
		if err := req.Decode(&page); err != nil {
			return nil, err
		}
		var meta []pluginsdk.Meta
		var jsonld []any
		for _, f := range headTags {
			m, ld := f(req, page)
			meta, jsonld = append(meta, m...), append(jsonld, ld...)
		}
		return pluginsdk.Head(meta, jsonld...), nil

	case pluginsdk.HookEvent:
		// Something happened on the site (see "events" in plugin.json).
		// Events run in the background: nothing is returned to a visitor.
		if run := events[req.Event]; run != nil {
			return nil, run(req)
		}

	case pluginsdk.HookCommentCheck:
		// A visitor posted a comment. The strictest verdict of all plugins
		// wins: ok, pending (moderation) or spam. The host waits about 2
		// seconds at most and treats errors and timeouts as "ok".
		var c pluginsdk.CommentCheck
		if err := req.Decode(&c); err != nil {
			return nil, err
		}
		for _, check := range commentChecks {
			if verdict, reason := check(req, c); verdict != pluginsdk.VerdictOK {
				return pluginsdk.Verdict(verdict, reason), nil
			}
		}

	case pluginsdk.HookFormSubmit:
		// A visitor sent a form declared under "forms" in plugin.json. The
		// host already checked the token, the honeypot, the rate limit and
		// every field against its declaration.
		var f pluginsdk.FormSubmission
		if err := req.Decode(&f); err != nil {
			return nil, err
		}
		if submit := forms[f.Form]; submit != nil {
			return submit(req, f)
		}
	}
	return nil, nil
}

// main is required by Go but never runs: the host calls the exported
// functions the SDK provides.
func main() {}
