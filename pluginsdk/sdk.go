// Package pluginsdk is the Go SDK for sap13ns plugins (ABI 1).
//
// A plugin is a WebAssembly module built with the standard Go toolchain:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -trimpath -o plugin.wasm .
//
// Register the handler in init(); main() is never called in reactor mode:
//
//	func init() {
//		pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
//			switch req.Hook {
//			case pluginsdk.HookShortcode:
//				var sc pluginsdk.Shortcode
//				if err := req.Decode(&sc); err != nil {
//					return nil, err
//				}
//				return pluginsdk.HTML("<mark>" + pluginsdk.Escape(sc.Inner) + "</mark>"), nil
//			}
//			return nil, nil
//		})
//	}
//
//	func main() {}
//
// The package also compiles natively (go test, go vet, gopls). Outside the
// sandbox the host functions return ErrUnavailable unless the test installs
// fakes with package pluginsdk/sdktest, which also calls the registered
// handler with synthetic requests.
//
// The full contract is documented in docs/PLUGINS.md.
package pluginsdk

import (
	"encoding/json"
	"errors"
	"html"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk/internal/abi"
)

// ABIVersion is the host interface this SDK implements. The host refuses
// plugins built for another ABI.
const ABIVersion = 1

// Hooks a plugin can declare in plugin.json ("hooks").
const (
	HookContentFilter = "content.filter" // transform the HTML of a post or page
	HookShortcode     = "shortcode"      // render [name ...] shortcodes
	HookWidget        = "widget"         // render widgets placed in theme areas
	HookHeadMeta      = "head.meta"      // add <meta> tags and JSON-LD to <head>
	HookEvent         = "event"          // receive asynchronous events
	HookCommentCheck  = "comment.check"  // classify a new comment before it is stored
	HookFormSubmit    = "form.submit"    // handle a submission of a declared form
)

// Events delivered to the event hook (declare them in plugin.json "events").
const (
	EventPostPublished   = "post.published"   // payload: Post
	EventPostUpdated     = "post.updated"     // payload: Post
	EventPostTrashed     = "post.trashed"     // payload: Post
	EventCommentCreated  = "comment.created"  // payload: CommentEvent
	EventCommentApproved = "comment.approved" // payload: CommentEvent
	EventUserRegistered  = "user.registered"  // payload: {"username", "role"}
	EventCronHourly      = "cron.hourly"      // payload: {"time": RFC 3339}
)

// Site describes the site the plugin runs on.
type Site struct {
	URL      string `json:"url"`      // absolute base URL, no trailing slash
	Title    string `json:"title"`    // site title in the main language
	Language string `json:"language"` // main language (BCP 47, e.g. "es")
	Timezone string `json:"timezone"` // IANA time zone of the site, e.g. "Europe/Madrid"
}

// PluginInfo identifies the plugin and carries its settings.
type PluginInfo struct {
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
}

// Request is the envelope sent by the host for every call.
type Request struct {
	ABI     int             `json:"abi"`
	Hook    string          `json:"hook"`
	Event   string          `json:"event"` // event name, only for HookEvent
	Plugin  PluginInfo      `json:"plugin"`
	Site    Site            `json:"site"`
	Payload json.RawMessage `json:"payload"`
}

// Decode unmarshals the hook payload into v (Shortcode, Widget, ...).
func (r *Request) Decode(v any) error { return json.Unmarshal(r.Payload, v) }

// Setting returns a plugin setting, already merged with its default.
// Secret settings arrive decrypted; never echo them into HTML.
func (r *Request) Setting(key string) string { return r.Plugin.Settings[key] }

// SettingTime parses a "datetime" setting. The host sends these as RFC 3339
// with the site's offset already applied, so no time zone data is needed.
// ok is false when the setting is empty or invalid.
func (r *Request) SettingTime(key string) (t time.Time, ok bool) {
	return ParseTime(r.Setting(key))
}

// ParseTime parses a datetime value from a setting or a widget config.
func ParseTime(v string) (t time.Time, ok bool) {
	if v == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}

// Term is a category or tag.
type Term struct {
	Taxonomy string `json:"taxonomy"` // "category" | "tag"
	Slug     string `json:"slug"`
	Name     string `json:"name"` // in the post's language
	URL      string `json:"url"`  // absolute
}

// Post is the public view of a published post or page.
type Post struct {
	ID          int64    `json:"id"`
	Type        string   `json:"type"` // "post" | "page"
	Title       string   `json:"title"`
	Slug        string   `json:"slug"`
	Lang        string   `json:"lang"`
	URL         string   `json:"url"` // absolute
	Excerpt     string   `json:"excerpt"`
	Author      string   `json:"author"`
	Categories  []string `json:"categories"` // names
	Tags        []string `json:"tags"`       // names
	Terms       []Term   `json:"terms"`
	Image       string   `json:"image"` // absolute URL of the featured image, "" if none
	PublishedAt string   `json:"published_at"`
	UpdatedAt   string   `json:"updated_at"` // last edit, RFC 3339
	WordCount   int      `json:"word_count"`
}

// Page describes the page being rendered.
type Page struct {
	Kind        string `json:"kind"` // home | single | page | archive | search | 404
	URL         string `json:"url"`  // canonical, absolute
	Lang        string `json:"lang"` // language the page is shown in
	Title       string `json:"title"`
	Description string `json:"description"`
	Post        *Post  `json:"post"`
}

// Shortcode is the payload of HookShortcode.
type Shortcode struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
	Inner string            `json:"inner"` // raw text between the tags, not HTML
	Post  *Post             `json:"post"`
}

// ContentFilter is the payload of HookContentFilter.
type ContentFilter struct {
	HTML string `json:"html"`
	Post *Post  `json:"post"`
	// Context is where the HTML goes: ContextPage (the post's own page) or
	// ContextFeed (a full-text feed item). Leave feed items alone when the
	// filter adds page furniture such as related posts or forms.
	Context string `json:"context"`
}

// ContentFilter contexts.
const (
	ContextPage = "page"
	ContextFeed = "feed"
)

// Widget is the payload of HookWidget.
type Widget struct {
	Widget string `json:"widget"` // widget id declared in plugin.json
	// Config holds this widget instance's values for the fields declared in
	// the widget's "config" in plugin.json (defaults applied; datetime
	// values as RFC 3339). Empty when the widget declares no fields.
	Config map[string]string `json:"config"`
	Page   *Page             `json:"page"`
}

// CommentCheck is the payload of HookCommentCheck. Email, IP and UserAgent
// are only filled when the plugin was granted the "comments:pii" permission.
type CommentCheck struct {
	Author     string `json:"author"`
	URL        string `json:"url"`
	Content    string `json:"content"`
	Post       *Post  `json:"post"`
	ParentID   int64  `json:"parent_id"`
	Registered bool   `json:"registered"` // written by a signed-in user
	// Returning: the commenter's e-mail address had a comment approved on
	// this site before. The host checks it; the address itself is only
	// sent with "comments:pii".
	Returning bool   `json:"returning"`
	Email     string `json:"email"`
	IP        string `json:"ip"`
	UserAgent string `json:"user_agent"`
}

// FormSubmission is the payload of HookFormSubmit. The host has already
// checked the anti-bot token, the honeypot, the rate limit and every field
// declaration (required, max length, type, choices); unknown fields are
// dropped.
type FormSubmission struct {
	Form    string            `json:"form"`     // form id declared in plugin.json
	Fields  map[string]string `json:"fields"`   // declared fields only
	Lang    string            `json:"lang"`     // language of the page the form was on
	PageURL string            `json:"page_url"` // absolute URL of that page
}

// CommentEvent is the payload of the comment.created and comment.approved
// events.
type CommentEvent struct {
	ID      int64  `json:"id"`
	PostID  int64  `json:"post_id"`
	Post    *Post  `json:"post"`
	Author  string `json:"author"`
	Status  string `json:"status"` // approved | pending | spam
	Content string `json:"content"`
}

// Meta is one <meta> tag. Exactly one of Name and Property must be set.
type Meta struct {
	Name     string `json:"name,omitempty"`
	Property string `json:"property,omitempty"`
	Content  string `json:"content"`
}

// HeadResponse is the response of HookHeadMeta.
type HeadResponse struct {
	Meta []Meta `json:"meta,omitempty"`
	// JSONLD objects are validated, size-capped and rendered as
	// <script type="application/ld+json"> blocks with "<" escaped.
	JSONLD []any `json:"jsonld,omitempty"`
}

// Comment verdicts returned by HookCommentCheck.
const (
	VerdictOK      = "ok"      // no objection: the site's normal moderation applies
	VerdictPending = "pending" // hold the comment for moderation
	VerdictSpam    = "spam"    // store it as spam
)

// CommentVerdict is the response of HookCommentCheck.
type CommentVerdict struct {
	Verdict string `json:"verdict"`
	Reason  string `json:"reason,omitempty"` // written to the plugin's activity log, never shown to the visitor
}

// FormResult is the response of HookFormSubmit. Message is shown to the
// visitor after the redirect back to the page; Errors maps field names to
// messages shown next to the fields (the submitted values are kept).
type FormResult struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
}

type htmlResponse struct {
	HTML string `json:"html"`
}

type metaResponse struct {
	Meta []Meta `json:"meta"`
}

// HTML builds the response of the content.filter, shortcode and widget
// hooks. The host sanitizes it: scripts, styles, iframes, forms and event
// handlers are removed. Basecoat classes (alert, badge, card, btn, ...) are
// allowed, `<span data-icon="name"></span>` becomes a Lucide icon and
// FormPlaceholder marks where a declared form goes.
func HTML(s string) any { return htmlResponse{HTML: s} }

// MetaTags builds the response of the head.meta hook.
func MetaTags(m ...Meta) any { return metaResponse{Meta: m} }

// Head builds a head.meta response with meta tags and JSON-LD objects
// (maps or structs that marshal to a JSON object).
func Head(meta []Meta, jsonld ...any) any { return HeadResponse{Meta: meta, JSONLD: jsonld} }

// Verdict builds the response of the comment.check hook.
func Verdict(verdict, reason string) any { return CommentVerdict{Verdict: verdict, Reason: reason} }

// FormOK builds a successful form.submit response.
func FormOK(message string) any { return FormResult{OK: true, Message: message} }

// FormErrors builds a failed form.submit response; fieldErrors may be nil.
func FormErrors(message string, fieldErrors map[string]string) any {
	return FormResult{OK: false, Message: message, Errors: fieldErrors}
}

// FormPlaceholder returns the marker that the host replaces with the form
// declared in plugin.json under "forms" (with its anti-bot fields, labels in
// the page language and the result message after a submission). Return it
// from a shortcode, widget or content filter.
func FormPlaceholder(formID string) string {
	return `<div data-form="` + html.EscapeString(formID) + `"></div>`
}

// Escape escapes text for safe inclusion in HTML.
func Escape(s string) string { return html.EscapeString(s) }

// Handler handles every hook call. Return nil for "nothing to add".
type Handler func(req *Request) (any, error)

var handler Handler

// Handle registers the plugin entry point. Call it from init().
func Handle(h Handler) {
	handler = h
	abi.Dispatch = dispatch
}

// dispatch runs one call: envelope in, response JSON out. Panics become an
// error response instead of a trap, so the instance stays usable.
func dispatch(in []byte) []byte {
	if handler == nil {
		return errorJSON("no handler registered")
	}
	var req Request
	if err := json.Unmarshal(in, &req); err != nil {
		return errorJSON("invalid request: " + err.Error())
	}
	resp, err := safeCall(&req)
	switch {
	case err != nil:
		return errorJSON(err.Error())
	case resp == nil:
		return []byte("{}")
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return errorJSON(err.Error())
	}
	return out
}

func safeCall(req *Request) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("plugin panic")
		}
	}()
	return handler(req)
}

func errorJSON(msg string) []byte {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return b
}

// -------------------------------------------------------- host functions --

// Errors returned by host functions.
var (
	ErrNotFound    = errors.New("not found")
	ErrDenied      = errors.New("permission denied")
	ErrInvalid     = errors.New("invalid argument")
	ErrQuota       = errors.New("storage quota exceeded")
	ErrFailed      = errors.New("host call failed")
	ErrLimit       = errors.New("too many host calls in one request")
	ErrRateLimited = errors.New("rate limit exceeded")
	ErrUnavailable = errors.New("host functions are only available inside sap13ns (use pluginsdk/sdktest in tests)")
)

func codeErr(code int32) error {
	switch code {
	case abi.CodeNotFound:
		return ErrNotFound
	case abi.CodeDenied:
		return ErrDenied
	case abi.CodeInvalid:
		return ErrInvalid
	case abi.CodeQuota:
		return ErrQuota
	case abi.CodeLimit:
		return ErrLimit
	case abi.CodeRateLimited:
		return ErrRateLimited
	case abi.CodeUnavailable:
		return ErrUnavailable
	default:
		return ErrFailed
	}
}

func host() abi.Host {
	if abi.Current == nil {
		return unavailable{}
	}
	return abi.Current
}

// Level is the severity of a log line.
type Level uint32

const (
	Debug Level = iota
	Info
	Warn
	Error
)

// Log writes a line to the plugin log shown in the admin panel (lines are
// capped at 4 KiB; the panel keeps the latest 200).
func Log(level Level, msg string) { host().Log(uint32(level), []byte(msg)) }

// KVGet reads a value from the plugin's private storage ("kv" permission).
// It returns ErrNotFound for a missing key.
func KVGet(key string) ([]byte, error) {
	v, code := host().KVGet([]byte(key))
	if code < 0 {
		return nil, codeErr(code)
	}
	return v, nil
}

// KVSet stores a value (keys: 1-200 printable ASCII characters; values up
// to 256 KiB; 1 MiB and 1000 keys per plugin).
func KVSet(key string, value []byte) error {
	if code := host().KVSet([]byte(key), value); code < 0 {
		return codeErr(code)
	}
	return nil
}

// KVDelete removes a key; deleting a missing key is not an error.
func KVDelete(key string) error {
	if code := host().KVDelete([]byte(key)); code < 0 {
		return codeErr(code)
	}
	return nil
}

// PostsQuery filters QueryPosts. Only published, public content is returned.
type PostsQuery struct {
	Limit      int     `json:"limit,omitempty"`       // 1-20, default 5
	Category   string  `json:"category,omitempty"`    // category slug
	Tag        string  `json:"tag,omitempty"`         // tag slug (combined with Category: both must match)
	Search     string  `json:"search,omitempty"`      // full-text search
	Type       string  `json:"type,omitempty"`        // "post" (default) | "page"
	Exclude    int64   `json:"exclude,omitempty"`     // one post id to leave out
	ExcludeIDs []int64 `json:"exclude_ids,omitempty"` // more ids to leave out (max 50)
	Lang       string  `json:"lang,omitempty"`        // language code; "" = every language
}

// QueryPosts returns published posts ("posts:read" permission).
func QueryPosts(q PostsQuery) ([]Post, error) {
	b, _ := json.Marshal(q)
	raw, code := host().PostsQuery(b)
	if code < 0 {
		return nil, codeErr(code)
	}
	var out []Post
	return out, json.Unmarshal(raw, &out)
}

// HTTPRequest is an outgoing HTTPS request.
type HTTPRequest struct {
	Method  string            `json:"method"` // default GET
	URL     string            `json:"url"`    // absolute https:// URL
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"` // max 256 KiB
}

// HTTPResponse is the answer to Fetch. Binary bodies arrive in BodyBase64.
type HTTPResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"` // lower-case names
	Body       string            `json:"body"`
	BodyBase64 string            `json:"body_base64"`
	Error      string            `json:"error"`
}

// Fetch performs an HTTPS request ("http:<host>" permission). It is only
// available from the event, comment.check and form.submit hooks, never
// while a page renders, and never reaches private or loopback addresses.
func Fetch(req HTTPRequest) (*HTTPResponse, error) {
	b, _ := json.Marshal(req)
	raw, code := host().HTTPFetch(b)
	if code < 0 {
		return nil, codeErr(code)
	}
	var resp HTTPResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

// MailToAdmin addresses Mail to the site administrator.
const MailToAdmin = "admin"

// Mail is a plain-text e-mail sent through the site's mailer.
type Mail struct {
	// To is MailToAdmin or an address an administrator saved in one of the
	// plugin's settings of type "email". Anything else is ErrDenied: the
	// host is never an open relay.
	To      string `json:"to"`
	Subject string `json:"subject"` // max 200 characters, one line
	Body    string `json:"body"`    // plain text, max 64 KiB
	// ReplyTo may be any single address, e.g. the visitor who filled in a
	// contact form.
	ReplyTo string `json:"reply_to,omitempty"`
}

// SendMail queues an e-mail ("mail" permission; event and form.submit hooks
// only). Delivery is asynchronous and limited per plugin and hour
// (ErrRateLimited).
func SendMail(m Mail) error {
	b, _ := json.Marshal(m)
	if code := host().MailSend(b); code < 0 {
		return codeErr(code)
	}
	return nil
}

// unavailable is the host outside the sandbox when no fakes are installed.
type unavailable struct{}

func (unavailable) Log(uint32, []byte)                {}
func (unavailable) KVGet([]byte) ([]byte, int32)      { return nil, abi.CodeUnavailable }
func (unavailable) KVSet([]byte, []byte) int32        { return abi.CodeUnavailable }
func (unavailable) KVDelete([]byte) int32             { return abi.CodeUnavailable }
func (unavailable) PostsQuery([]byte) ([]byte, int32) { return nil, abi.CodeUnavailable }
func (unavailable) HTTPFetch([]byte) ([]byte, int32)  { return nil, abi.CodeUnavailable }
func (unavailable) MailSend([]byte) int32             { return abi.CodeUnavailable }
