// Package plugin hosts sandboxed WebAssembly plugins.
//
// Security model (see docs/PLUGINS.md):
//   - Plugins are WebAssembly modules executed by wazero. They have no access
//     to the filesystem, environment, network or database; the only imports
//     available are a minimal WASI (clock, random, stdout/stderr captured to the
//     plugin log) and the sap13ns host functions.
//   - Every capability beyond pure computation is a permission declared in
//     plugin.json and approved by an administrator. Host functions check the
//     granted permissions on every call.
//   - Approval pins the SHA-256 of the .wasm file. If the file changes, the
//     plugin stops loading until it is approved again.
//   - Calls run with a memory cap and a deadline; HTML returned by plugins is
//     sanitized before it reaches a page.
//   - Packages are extracted with strict limits and scanned (scan.go) before
//     they are installed, always disabled.
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

const ABIVersion = 1

// Hooks a plugin can subscribe to.
const (
	HookContentFilter = "content.filter" // transform the HTML of a post or page
	HookShortcode     = "shortcode"      // render [name ...] shortcodes
	HookWidget        = "widget"         // render widgets placed in theme areas
	HookHeadMeta      = "head.meta"      // add <meta> tags and JSON-LD to <head>
	HookEvent         = "event"          // receive asynchronous events
	HookCommentCheck  = "comment.check"  // classify a new comment before it is stored
	HookFormSubmit    = "form.submit"    // handle a submission of a declared form
)

var knownHooks = []string{HookContentFilter, HookShortcode, HookWidget, HookHeadMeta, HookEvent, HookCommentCheck, HookFormSubmit}

// Events delivered to plugins that subscribe to HookEvent.
var KnownEvents = []string{
	"post.published", "post.updated", "post.trashed",
	"comment.created", "comment.approved",
	"user.registered", "cron.hourly",
}

// Permissions.
const (
	PermKV          = "kv"           // private key-value storage
	PermPostsRead   = "posts:read"   // query published posts
	PermHTTP        = "http:"        // prefix: http:example.com, http:*.example.com, http:*
	PermMail        = "mail"         // e-mail the administrator or addresses saved in settings
	PermCommentsPII = "comments:pii" // comment.check also receives e-mail, IP and user agent
	PermAssetsJS    = "assets:js"    // link the JavaScript files declared in "assets"
	// PermCSP prefixes the Content-Security-Policy sources declared in "csp".
	// They are not listed in "permissions": they are derived from the
	// manifest ("csp:frame-src:https://www.youtube-nocookie.com") so that
	// approval pins them like any other permission.
	PermCSP = "csp:"
)

// ReservedIDs cannot be used by plugins (routes, built-in names, and the id
// of the skeleton that `sap13ns plugin new` renames).
var ReservedIDs = []string{"sap13ns", "admin", "api", "assets", "core", "plugin", "plugins", "static",
	"system", "theme", "themes", "uploads", "www", "my-plugin"}

// CSP directives a plugin may extend, and the sources it may add.
var (
	cspDirectives = []string{"frame-src", "img-src"}
	cspSourceRe   = regexp.MustCompile(`^https://(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

type WidgetDecl struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Config declares per-instance fields, edited in Appearance → Widgets
	// and passed to the widget hook as config. Same types as settings,
	// except secret.
	Config []SettingDecl `json:"config"`
}

// LocalText is a manifest string that may be translated: either "Send" or
// {"en": "Send", "es": "Enviar"}.
type LocalText map[string]string

func (t *LocalText) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = LocalText{"": s}
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.New(`must be a string or an object like {"en": "...", "es": "..."}`)
	}
	*t = m
	return nil
}

func (t LocalText) MarshalJSON() ([]byte, error) {
	if len(t) == 1 {
		if v, ok := t[""]; ok {
			return json.Marshal(v)
		}
	}
	return json.Marshal(map[string]string(t))
}

// In returns the text for lang, falling back to its base language, the
// untranslated text, English and finally any translation.
func (t LocalText) In(lang string) string {
	if v := t[lang]; v != "" {
		return v
	}
	if base, _, ok := strings.Cut(lang, "-"); ok && t[base] != "" {
		return t[base]
	}
	for _, k := range []string{"", "en"} {
		if v := t[k]; v != "" {
			return v
		}
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if t[k] != "" {
			return t[k]
		}
	}
	return ""
}

func (t LocalText) tooLong(max int) bool {
	for k, v := range t {
		if len(k) > 12 || len(v) > max {
			return true
		}
	}
	return false
}

// FormDecl declares a public form. The host renders it where the plugin
// returns pluginsdk.FormPlaceholder(id), protects it (rate limit, honeypot,
// signed timestamp token, size cap, same-origin check), validates the fields
// and only then calls the form.submit hook.
type FormDecl struct {
	ID     string      `json:"id"`
	Title  LocalText   `json:"title"`  // optional heading
	Submit LocalText   `json:"submit"` // button label (default "Send")
	Fields []FormField `json:"fields"`
}

// FormField is one input of a declared form.
type FormField struct {
	Name        string    `json:"name"`
	Label       LocalText `json:"label"`
	Type        string    `json:"type"` // text | email | url | tel | number | textarea | select | checkbox
	Required    bool      `json:"required"`
	MaxLength   int       `json:"max_length"` // default 200 (textarea 5000), at most 10000
	Choices     []string  `json:"choices"`    // select only
	Placeholder LocalText `json:"placeholder"`
}

// Limit returns the effective maximum length of the field value.
func (f *FormField) Limit() int {
	switch {
	case f.MaxLength > 0:
		return f.MaxLength
	case f.Type == "textarea":
		return 5000
	default:
		return 200
	}
}

var formFieldTypes = []string{"text", "email", "url", "tel", "number", "textarea", "select", "checkbox"}

// AssetsDecl lists static files under assets/ linked from every public page
// while the plugin is enabled. JavaScript needs the "assets:js" permission.
type AssetsDecl struct {
	CSS []string `json:"css"`
	JS  []string `json:"js"`
}

var assetPathRe = regexp.MustCompile(`^assets/[a-zA-Z0-9_-][a-zA-Z0-9_./-]{0,150}$`)

type SettingDecl struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | textarea | number | bool | select | secret | url | email | datetime
	Default  string   `json:"default"`
	Choices  []string `json:"choices"`
	Help     string   `json:"help"`
	Required bool     `json:"required"`
}

type Manifest struct {
	ABI         int           `json:"abi"`
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Version     string        `json:"version"`
	Description string        `json:"description"`
	Author      string        `json:"author"`
	Homepage    string        `json:"homepage"`
	License     string        `json:"license"`
	Wasm        string        `json:"wasm"`
	Hooks       []string      `json:"hooks"`
	Events      []string      `json:"events"`
	Shortcodes  []string      `json:"shortcodes"`
	Widgets     []WidgetDecl  `json:"widgets"`
	Permissions []string      `json:"permissions"`
	Settings    []SettingDecl `json:"settings"`
	Forms       []FormDecl    `json:"forms"`
	Assets      AssetsDecl    `json:"assets"`
	// CSP adds sources to the site's Content-Security-Policy while the
	// plugin is enabled: {"frame-src": ["https://www.youtube-nocookie.com"]}.
	CSP map[string][]string `json:"csp"`
}

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,49}$`)
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
	keyRe      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	hostPermRe = regexp.MustCompile(`^(\*|(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63})$`)
	wasmFileRe = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}\.wasm$`)
	versionRe  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([.+-][0-9A-Za-z.-]{1,40})?$`)
)

func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	// One parser reads the id everywhere: nothing may follow the object.
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("plugin.json: data after the manifest object")
	}
	if m.Wasm == "" {
		m.Wasm = "plugin.wasm"
	}
	return &m, m.Validate()
}

func (m *Manifest) Validate() error {
	var errs []error
	if m.ABI != ABIVersion {
		errs = append(errs, fmt.Errorf("abi must be %d", ABIVersion))
	}
	if !idRe.MatchString(m.ID) {
		errs = append(errs, errors.New("id must match ^[a-z0-9][a-z0-9-]{1,49}$"))
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > 80 {
		errs = append(errs, errors.New("name is required (max 80 characters)"))
	}
	if !versionRe.MatchString(m.Version) {
		errs = append(errs, errors.New("version must be semantic (1.2.3)"))
	}
	if len(m.Description) > 500 {
		errs = append(errs, errors.New("description is too long"))
	}
	if !wasmFileRe.MatchString(m.Wasm) {
		errs = append(errs, errors.New("wasm must be a plain .wasm file name"))
	}
	if len(m.Hooks) == 0 {
		errs = append(errs, errors.New("at least one hook is required"))
	}
	for _, h := range m.Hooks {
		if !slices.Contains(knownHooks, h) {
			errs = append(errs, fmt.Errorf("unknown hook %q", h))
		}
	}
	if m.HasHook(HookShortcode) != (len(m.Shortcodes) > 0) {
		errs = append(errs, errors.New(`"shortcodes" must be declared together with the "shortcode" hook`))
	}
	for _, s := range m.Shortcodes {
		if !nameRe.MatchString(s) {
			errs = append(errs, fmt.Errorf("invalid shortcode name %q", s))
		}
	}
	if m.HasHook(HookWidget) != (len(m.Widgets) > 0) {
		errs = append(errs, errors.New(`"widgets" must be declared together with the "widget" hook`))
	}
	for _, w := range m.Widgets {
		if !nameRe.MatchString(w.ID) || w.Title == "" {
			errs = append(errs, fmt.Errorf("invalid widget %q", w.ID))
		}
		if len(w.Config) > 10 {
			errs = append(errs, fmt.Errorf("widget %q: at most 10 config fields", w.ID))
		}
		seen := map[string]bool{}
		for _, s := range w.Config {
			if s.Type == "secret" {
				errs = append(errs, fmt.Errorf("widget %q: config field %q cannot be a secret (use a plugin setting)", w.ID, s.Key))
				continue
			}
			if err := validateSettingDecl(s, seen); err != nil {
				errs = append(errs, fmt.Errorf("widget %q: %w", w.ID, err))
			}
		}
	}
	if m.HasHook(HookEvent) != (len(m.Events) > 0) {
		errs = append(errs, errors.New(`"events" must be declared together with the "event" hook`))
	}
	for _, e := range m.Events {
		if !slices.Contains(KnownEvents, e) {
			errs = append(errs, fmt.Errorf("unknown event %q", e))
		}
	}
	for _, p := range m.Permissions {
		if err := validatePermission(p); err != nil {
			errs = append(errs, err)
		}
	}
	seen := map[string]bool{}
	if len(m.Settings) > 50 {
		errs = append(errs, errors.New("at most 50 settings"))
	}
	for _, s := range m.Settings {
		if err := validateSettingDecl(s, seen); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, m.validateForms()...)
	errs = append(errs, m.validateAssets()...)
	errs = append(errs, m.validateCSP()...)
	return errors.Join(errs...)
}

// validateSettingDecl checks one setting (or widget config field) and
// records its key in seen.
func validateSettingDecl(s SettingDecl, seen map[string]bool) error {
	if !keyRe.MatchString(s.Key) || seen[s.Key] {
		return fmt.Errorf("invalid or duplicate setting key %q", s.Key)
	}
	seen[s.Key] = true
	if s.Label == "" {
		return fmt.Errorf("setting %q: label is required", s.Key)
	}
	switch s.Type {
	case "text", "textarea", "number", "bool", "secret", "url", "email":
	case "select":
		if len(s.Choices) == 0 || len(s.Choices) > 100 {
			return fmt.Errorf("setting %q: select needs 1 to 100 choices", s.Key)
		}
		if s.Default != "" && !slices.Contains(s.Choices, s.Default) {
			return fmt.Errorf("setting %q: default is not one of the choices", s.Key)
		}
	case "datetime":
		if s.Default != "" {
			if _, err := ParseSettingTime(s.Default, time.UTC); err != nil {
				return fmt.Errorf("setting %q: default must be YYYY-MM-DDTHH:MM or RFC 3339", s.Key)
			}
		}
	default:
		return fmt.Errorf("setting %q: unknown type %q", s.Key, s.Type)
	}
	if s.Type == "secret" && s.Default != "" {
		return fmt.Errorf("setting %q: a secret cannot have a default", s.Key)
	}
	// E-mail settings are mail_send recipients: only an administrator may
	// enter one.
	if s.Type == "email" && s.Default != "" {
		return fmt.Errorf("setting %q: an e-mail setting cannot have a default", s.Key)
	}
	return nil
}

// Setting time formats: what <input type="datetime-local"> submits (local
// site time), optionally with seconds, or RFC 3339 with an offset.
const (
	settingTimeLayout = "2006-01-02T15:04"
	settingTimeSecs   = "2006-01-02T15:04:05"
)

// ParseSettingTime parses a datetime setting. Values without an offset are
// in loc, the site's time zone.
func ParseSettingTime(v string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation(settingTimeLayout, v, loc); err == nil {
		return t, nil
	}
	return time.ParseInLocation(settingTimeSecs, v, loc)
}

func (m *Manifest) validateForms() []error {
	var errs []error
	if m.HasHook(HookFormSubmit) != (len(m.Forms) > 0) {
		errs = append(errs, errors.New(`"forms" must be declared together with the "form.submit" hook`))
	}
	if len(m.Forms) > 5 {
		errs = append(errs, errors.New("at most 5 forms"))
	}
	ids := map[string]bool{}
	for _, f := range m.Forms {
		if !nameRe.MatchString(f.ID) || ids[f.ID] {
			errs = append(errs, fmt.Errorf("invalid or duplicate form id %q", f.ID))
		}
		ids[f.ID] = true
		if len(f.Fields) == 0 || len(f.Fields) > 20 {
			errs = append(errs, fmt.Errorf("form %q: between 1 and 20 fields", f.ID))
		}
		if f.Title.tooLong(200) || f.Submit.tooLong(60) {
			errs = append(errs, fmt.Errorf("form %q: title or submit label too long", f.ID))
		}
		names := map[string]bool{}
		for _, fd := range f.Fields {
			if !keyRe.MatchString(fd.Name) || names[fd.Name] {
				errs = append(errs, fmt.Errorf("form %q: invalid or duplicate field name %q", f.ID, fd.Name))
			}
			names[fd.Name] = true
			if !slices.Contains(formFieldTypes, fd.Type) {
				errs = append(errs, fmt.Errorf("form %q: field %q has unknown type %q", f.ID, fd.Name, fd.Type))
			}
			if fd.Type == "select" && (len(fd.Choices) == 0 || len(fd.Choices) > 50) {
				errs = append(errs, fmt.Errorf("form %q: select %q needs 1-50 choices", f.ID, fd.Name))
			}
			if fd.MaxLength < 0 || fd.MaxLength > 10000 {
				errs = append(errs, fmt.Errorf("form %q: max_length of %q must be 0-10000", f.ID, fd.Name))
			}
			if len(fd.Label) == 0 || fd.Label.tooLong(200) || fd.Placeholder.tooLong(200) {
				errs = append(errs, fmt.Errorf("form %q: field %q needs a label (max 200 characters)", f.ID, fd.Name))
			}
		}
	}
	return errs
}

func (m *Manifest) validateAssets() []error {
	var errs []error
	for kind, list := range map[string][]string{".css": m.Assets.CSS, ".js": m.Assets.JS} {
		if len(list) > 5 {
			errs = append(errs, fmt.Errorf("at most 5 %s assets", kind))
		}
		for _, a := range list {
			if !assetPathRe.MatchString(a) || strings.Contains(a, "..") || strings.Contains(a, "//") || !strings.HasSuffix(a, kind) {
				errs = append(errs, fmt.Errorf("invalid asset path %q (must be assets/....%s)", a, kind[1:]))
			}
		}
	}
	if len(m.Assets.JS) > 0 && !slices.Contains(m.Permissions, PermAssetsJS) {
		errs = append(errs, fmt.Errorf(`JavaScript assets need the %q permission`, PermAssetsJS))
	}
	return errs
}

func (m *Manifest) validateCSP() []error {
	var errs []error
	for dir, srcs := range m.CSP {
		if !slices.Contains(cspDirectives, dir) {
			errs = append(errs, fmt.Errorf("csp: only %s can be extended, not %q", strings.Join(cspDirectives, " and "), dir))
			continue
		}
		if len(srcs) == 0 || len(srcs) > 10 {
			errs = append(errs, fmt.Errorf("csp %s: between 1 and 10 sources", dir))
		}
		for _, src := range srcs {
			if !cspSourceRe.MatchString(src) {
				errs = append(errs, fmt.Errorf("csp %s: %q must be https://host or https://*.domain", dir, src))
			}
		}
	}
	return errs
}

// EffectivePermissions are the declared permissions plus the CSP sources,
// sorted. Approval pins this list.
func (m *Manifest) EffectivePermissions() []string {
	out := slices.Clone(m.Permissions)
	for dir, srcs := range m.CSP {
		for _, src := range srcs {
			out = append(out, PermCSP+dir+":"+src)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Capabilities are the hooks, events and forms the manifest subscribes to,
// as the entries an approval pins next to the permissions ("hook:widget",
// "event:post.published", "form:contact").
func (m *Manifest) Capabilities() []string {
	var out []string
	for _, h := range m.Hooks {
		out = append(out, "hook:"+h)
	}
	for _, e := range m.Events {
		out = append(out, "event:"+e)
	}
	for _, f := range m.Forms {
		out = append(out, "form:"+f.ID)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// Pinned is what an approval pins from the manifest: the effective
// permissions and the capabilities. Asking for anything outside the
// approved set stops the plugin until it is approved again.
func (m *Manifest) Pinned() []string {
	out := append(m.EffectivePermissions(), m.Capabilities()...)
	sort.Strings(out)
	return slices.Compact(out)
}

// Form returns the declared form with that id.
func (m *Manifest) Form(id string) *FormDecl {
	for i := range m.Forms {
		if m.Forms[i].ID == id {
			return &m.Forms[i]
		}
	}
	return nil
}

func validatePermission(p string) error {
	switch {
	case p == PermKV, p == PermPostsRead, p == PermMail, p == PermCommentsPII, p == PermAssetsJS:
		return nil
	case strings.HasPrefix(p, PermHTTP) && hostPermRe.MatchString(strings.TrimPrefix(p, PermHTTP)):
		return nil
	}
	return fmt.Errorf("unknown permission %q", p)
}

func (m *Manifest) HasHook(h string) bool { return slices.Contains(m.Hooks, h) }

func (m *Manifest) HasEvent(e string) bool { return slices.Contains(m.Events, e) }

// PermissionRisk classifies permissions for the approval screen. Enabling a
// plugin with a high-risk permission needs an explicit confirmation.
func PermissionRisk(p string) string {
	switch {
	case p == "http:*", p == PermCommentsPII, p == PermAssetsJS, strings.HasPrefix(p, PermCSP):
		return "high"
	case strings.HasPrefix(p, PermHTTP), p == PermMail:
		return "medium"
	default:
		return "low"
	}
}

// HighRisk reports whether any of perms is high risk.
func HighRisk(perms []string) bool {
	return slices.ContainsFunc(perms, func(p string) bool { return PermissionRisk(p) == "high" })
}

// hostAllowed reports whether a granted http permission covers host.
func hostAllowed(granted []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, g := range granted {
		if !strings.HasPrefix(g, PermHTTP) {
			continue
		}
		pat := strings.TrimPrefix(g, PermHTTP)
		switch {
		case pat == "*":
			return true
		case strings.HasPrefix(pat, "*."):
			if strings.HasSuffix(host, pat[1:]) {
				return true
			}
		case pat == host:
			return true
		}
	}
	return false
}
