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
package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const ABIVersion = 1

// Hooks a plugin can subscribe to.
const (
	HookContentFilter = "content.filter" // transform the HTML of a post or page
	HookShortcode     = "shortcode"      // render [name ...] shortcodes
	HookWidget        = "widget"         // render widgets placed in theme areas
	HookHeadMeta      = "head.meta"      // add <meta> tags to <head>
	HookEvent         = "event"          // receive asynchronous events
)

var knownHooks = []string{HookContentFilter, HookShortcode, HookWidget, HookHeadMeta, HookEvent}

// Events delivered to plugins that subscribe to HookEvent.
var KnownEvents = []string{
	"post.published", "post.updated", "post.trashed",
	"comment.created", "comment.approved",
	"user.registered", "cron.hourly",
}

// Permissions.
const (
	PermKV        = "kv"         // private key-value storage
	PermPostsRead = "posts:read" // query published posts
	PermHTTP      = "http:"      // prefix: http:example.com, http:*.example.com, http:*
)

type WidgetDecl struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type SettingDecl struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Type     string   `json:"type"` // text | textarea | number | bool | select | secret | url
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
	for _, s := range m.Settings {
		if !keyRe.MatchString(s.Key) || seen[s.Key] {
			errs = append(errs, fmt.Errorf("invalid or duplicate setting key %q", s.Key))
		}
		seen[s.Key] = true
		switch s.Type {
		case "text", "textarea", "number", "bool", "secret", "url":
		case "select":
			if len(s.Choices) == 0 {
				errs = append(errs, fmt.Errorf("setting %q: select needs choices", s.Key))
			}
		default:
			errs = append(errs, fmt.Errorf("setting %q: unknown type %q", s.Key, s.Type))
		}
	}
	return errors.Join(errs...)
}

func validatePermission(p string) error {
	switch {
	case p == PermKV, p == PermPostsRead:
		return nil
	case strings.HasPrefix(p, PermHTTP) && hostPermRe.MatchString(strings.TrimPrefix(p, PermHTTP)):
		return nil
	}
	return fmt.Errorf("unknown permission %q", p)
}

func (m *Manifest) HasHook(h string) bool { return slices.Contains(m.Hooks, h) }

func (m *Manifest) HasEvent(e string) bool { return slices.Contains(m.Events, e) }

// PermissionRisk classifies permissions for the approval screen.
func PermissionRisk(p string) string {
	switch {
	case p == "http:*":
		return "high"
	case strings.HasPrefix(p, PermHTTP):
		return "medium"
	default:
		return "low"
	}
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
