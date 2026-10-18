//go:build wasip1

// Package pluginsdk is the Go SDK for sap13ns plugins (ABI 1).
//
// Build a plugin with the standard Go toolchain:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// Register the handler in init(); main() is never called in reactor mode:
//
//	func init() {
//		pluginsdk.Handle(func(req *pluginsdk.Request) (any, error) {
//			switch req.Hook {
//			case pluginsdk.HookShortcode:
//				var sc pluginsdk.Shortcode
//				req.Decode(&sc)
//				return pluginsdk.HTML("<mark>" + pluginsdk.Escape(sc.Inner) + "</mark>"), nil
//			}
//			return nil, nil
//		})
//	}
//	func main() {}
//
// The full contract is documented in docs/PLUGINS.md.
package pluginsdk

import (
	"encoding/json"
	"errors"
	"html"
	"unsafe"
)

const (
	HookContentFilter = "content.filter"
	HookShortcode     = "shortcode"
	HookWidget        = "widget"
	HookHeadMeta      = "head.meta"
	HookEvent         = "event"
)

type Site struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Language string `json:"language"`
}

type PluginInfo struct {
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
}

// Request is the envelope sent by the host for every call.
type Request struct {
	ABI     int             `json:"abi"`
	Hook    string          `json:"hook"`
	Event   string          `json:"event"`
	Plugin  PluginInfo      `json:"plugin"`
	Site    Site            `json:"site"`
	Payload json.RawMessage `json:"payload"`
}

// Decode unmarshals the hook payload.
func (r *Request) Decode(v any) error { return json.Unmarshal(r.Payload, v) }

// Setting returns a plugin setting (already merged with its default).
func (r *Request) Setting(key string) string { return r.Plugin.Settings[key] }

type Post struct {
	ID          int64    `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Slug        string   `json:"slug"`
	URL         string   `json:"url"`
	Excerpt     string   `json:"excerpt"`
	Author      string   `json:"author"`
	Categories  []string `json:"categories"`
	Tags        []string `json:"tags"`
	PublishedAt string   `json:"published_at"`
	WordCount   int      `json:"word_count"`
}

type Page struct {
	Kind        string `json:"kind"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Post        *Post  `json:"post"`
}

// Payloads per hook.
type Shortcode struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
	Inner string            `json:"inner"`
	Post  *Post             `json:"post"`
}

type ContentFilter struct {
	HTML string `json:"html"`
	Post *Post  `json:"post"`
}

type Widget struct {
	Widget string            `json:"widget"`
	Config map[string]string `json:"config"`
	Page   *Page             `json:"page"`
}

type Meta struct {
	Name     string `json:"name,omitempty"`
	Property string `json:"property,omitempty"`
	Content  string `json:"content"`
}

type htmlResponse struct {
	HTML string `json:"html"`
}

type metaResponse struct {
	Meta []Meta `json:"meta"`
}

// HTML builds the response of content.filter, shortcode and widget hooks.
// The host sanitizes it: scripts, styles, iframes, forms and event handlers
// are removed. Basecoat classes (alert, badge, card, btn, ...) are allowed and
// `<span data-icon="name"></span>` is replaced with a Lucide icon.
func HTML(s string) any { return htmlResponse{HTML: s} }

// MetaTags builds the response of the head.meta hook.
func MetaTags(m ...Meta) any { return metaResponse{Meta: m} }

// Escape escapes text for safe inclusion in HTML.
func Escape(s string) string { return html.EscapeString(s) }

type Handler func(req *Request) (any, error)

var handler Handler

// Handle registers the plugin entry point. Call it from init().
func Handle(h Handler) { handler = h }

// ---------------------------------------------------------------- ABI ----

var (
	inputs     = map[uintptr][]byte{}
	lastOutput []byte
)

//go:wasmexport sap13ns_abi
func abi() int32 { return 1 }

//go:wasmexport sap13ns_alloc
func alloc(size uint32) uint32 {
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(unsafe.SliceData(buf)))
	inputs[ptr] = buf // keep alive until sap13ns_handle consumes it
	return uint32(ptr)
}

//go:wasmexport sap13ns_handle
func handle(ptr, size uint32) uint64 {
	buf := inputs[uintptr(ptr)]
	delete(inputs, uintptr(ptr))
	var out []byte
	if handler == nil {
		out = errorJSON("no handler registered")
	} else if uint32(len(buf)) < size {
		out = errorJSON("invalid input")
	} else {
		var req Request
		if err := json.Unmarshal(buf[:size], &req); err != nil {
			out = errorJSON("invalid request: " + err.Error())
		} else {
			resp, err := safeCall(&req)
			switch {
			case err != nil:
				out = errorJSON(err.Error())
			case resp == nil:
				out = []byte("{}")
			default:
				out, err = json.Marshal(resp)
				if err != nil {
					out = errorJSON(err.Error())
				}
			}
		}
	}
	lastOutput = out
	p := uintptr(unsafe.Pointer(unsafe.SliceData(out)))
	return uint64(p)<<32 | uint64(len(out))
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

// -------------------------------------------------------- host imports ---

//go:wasmimport sap13ns log
func hostLog(level, ptr, size uint32)

//go:wasmimport sap13ns result
func hostResult(ptr, capacity uint32) int32

//go:wasmimport sap13ns kv_get
func hostKVGet(kptr, klen uint32) int32

//go:wasmimport sap13ns kv_set
func hostKVSet(kptr, klen, vptr, vlen uint32) int32

//go:wasmimport sap13ns kv_delete
func hostKVDelete(kptr, klen uint32) int32

//go:wasmimport sap13ns posts_query
func hostPostsQuery(qptr, qlen uint32) int32

//go:wasmimport sap13ns http_fetch
func hostHTTPFetch(rptr, rlen uint32) int32

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(b)))), uint32(len(b))
}

var (
	ErrNotFound = errors.New("not found")
	ErrDenied   = errors.New("permission denied")
	ErrInvalid  = errors.New("invalid argument")
	ErrQuota    = errors.New("storage quota exceeded")
	ErrFailed   = errors.New("host call failed")
	ErrLimit    = errors.New("too many host calls in one request")
)

func codeErr(code int32) error {
	switch code {
	case -1:
		return ErrNotFound
	case -2:
		return ErrDenied
	case -3:
		return ErrInvalid
	case -4:
		return ErrQuota
	case -6:
		return ErrLimit
	default:
		return ErrFailed
	}
}

// fetchResult copies the pending result of a host call.
func fetchResult(n int32) ([]byte, error) {
	if n < 0 {
		return nil, codeErr(n)
	}
	buf := make([]byte, n)
	if n == 0 {
		return buf, nil
	}
	p, c := ptrLen(buf)
	if got := hostResult(p, c); got != n {
		return nil, ErrFailed
	}
	return buf, nil
}

type Level uint32

const (
	Debug Level = iota
	Info
	Warn
	Error
)

// Log writes a line to the plugin log shown in the admin panel.
func Log(level Level, msg string) {
	b := []byte(msg)
	p, n := ptrLen(b)
	hostLog(uint32(level), p, n)
}

// KVGet reads a value from the plugin's private storage ("kv" permission).
func KVGet(key string) ([]byte, error) {
	k := []byte(key)
	p, n := ptrLen(k)
	return fetchResult(hostKVGet(p, n))
}

// KVSet stores a value (max 256 KiB; 1 MiB and 1000 keys per plugin).
func KVSet(key string, value []byte) error {
	k := []byte(key)
	kp, kn := ptrLen(k)
	vp, vn := ptrLen(value)
	if code := hostKVSet(kp, kn, vp, vn); code < 0 {
		return codeErr(code)
	}
	return nil
}

func KVDelete(key string) error {
	k := []byte(key)
	p, n := ptrLen(k)
	if code := hostKVDelete(p, n); code < 0 {
		return codeErr(code)
	}
	return nil
}

type PostsQuery struct {
	Limit    int    `json:"limit,omitempty"`
	Category string `json:"category,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Search   string `json:"search,omitempty"`
	Type     string `json:"type,omitempty"`
	Exclude  int64  `json:"exclude,omitempty"`
}

// QueryPosts returns published posts ("posts:read" permission).
func QueryPosts(q PostsQuery) ([]Post, error) {
	b, _ := json.Marshal(q)
	p, n := ptrLen(b)
	raw, err := fetchResult(hostPostsQuery(p, n))
	if err != nil {
		return nil, err
	}
	var out []Post
	return out, json.Unmarshal(raw, &out)
}

type HTTPRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type HTTPResponse struct {
	Status     int               `json:"status"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	BodyBase64 string            `json:"body_base64"`
	Error      string            `json:"error"`
}

// Fetch performs an HTTPS request ("http:<host>" permission, event hooks only).
func Fetch(req HTTPRequest) (*HTTPResponse, error) {
	b, _ := json.Marshal(req)
	p, n := ptrLen(b)
	raw, err := fetchResult(hostHTTPFetch(p, n))
	if err != nil {
		return nil, err
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
