package plugin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
)

// Limits applied to every plugin.
const (
	MaxWasmBytes       = 32 << 20
	memoryLimitPages   = 2048 // 128 MiB per instance
	maxInstances       = 4    // concurrent instances per plugin
	maxInputBytes      = 4 << 20
	maxOutputBytes     = 2 << 20
	renderTimeout      = 300 * time.Millisecond
	commentTimeout     = 2 * time.Second // every comment.check plugin together
	formTimeout        = 5 * time.Second
	eventTimeout       = 15 * time.Second
	instantiateTimeout = 10 * time.Second
	failureThreshold   = 5
	cooldown           = time.Minute
	maxJSONLDBytes     = 16 << 10 // per object
	maxJSONLDTotal     = 64 << 10 // per page
)

// Plugin statuses shown in the admin.
const (
	StatusAvailable = "available" // installed, not enabled
	StatusEnabled   = "enabled"
	StatusModified  = "modified" // wasm or permissions changed since approval
	StatusInvalid   = "invalid"  // manifest rejected
	StatusError     = "error"    // failed to compile or instantiate
)

type SiteInfo struct {
	URL      string `json:"url"`
	Title    string `json:"title"`
	Language string `json:"language"`
	Timezone string `json:"timezone"` // IANA name, e.g. "Europe/Madrid"
}

type PostInfo struct {
	ID          int64      `json:"id"`
	Type        string     `json:"type"`
	Title       string     `json:"title"`
	Slug        string     `json:"slug"`
	Lang        string     `json:"lang"`
	URL         string     `json:"url"` // absolute
	Excerpt     string     `json:"excerpt"`
	Author      string     `json:"author"`
	Categories  []string   `json:"categories"`
	Tags        []string   `json:"tags"`
	Terms       []TermInfo `json:"terms"`
	Image       string     `json:"image,omitempty"` // absolute URL of the featured image
	PublishedAt string     `json:"published_at,omitempty"`
	WordCount   int        `json:"word_count"`
}

// TermInfo is a category or tag of a post.
type TermInfo struct {
	Taxonomy string `json:"taxonomy"`
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}

type PageInfo struct {
	Kind        string    `json:"kind"`
	URL         string    `json:"url"`
	Lang        string    `json:"lang"` // language the page is shown in
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Post        *PostInfo `json:"post,omitempty"`
}

type MetaTag struct {
	Name     string `json:"name,omitempty"`
	Property string `json:"property,omitempty"`
	Content  string `json:"content"`
}

type LogLine struct {
	At    time.Time
	Level string
	Text  string
}

// Links converts posts to the public representation given to plugins.
type Links interface {
	PostInfo(p *store.Post) PostInfo
}

type Plugin struct {
	Manifest
	Dir          string
	SHA256       string // of the .wasm file
	AssetsSHA256 string // of the files under assets/ ("" when there are none)
	Status       string
	Err          string
	Record       *store.PluginRecord
	Report       *Report // latest scanner report, nil if never scanned

	assets map[string]assetFile // enabled plugins only, by path ("assets/x.css")
	dryRun bool                 // scanner instance: nothing is ever granted

	compiled wazero.CompiledModule
	pool     chan *instance
	created  atomic.Int32
	retired  atomic.Bool

	failMu    sync.Mutex
	failures  int
	pausedTil time.Time

	logMu sync.Mutex
	logs  []LogLine
}

// Granted reports whether perm is both requested by the manifest and
// granted at approval.
func (p *Plugin) Granted(perm string) bool {
	return p.Record != nil && slices.Contains(p.Record.GrantedPermissions, perm) && slices.Contains(p.EffectivePermissions(), perm)
}

// Enabled reports whether the administrator has switched the plugin on,
// even if it cannot run right now (modified, error).
func (p *Plugin) Enabled() bool { return p.Record != nil && p.Record.Enabled }

// AssetsVersion is the cache-busting segment of asset URLs.
func (p *Plugin) AssetsVersion() string {
	if len(p.AssetsSHA256) < 16 {
		return "0"
	}
	return p.AssetsSHA256[:16]
}

// SettingValues merges stored values over declared defaults. Secret
// settings stay encrypted; the host decrypts them only for the plugin.
func (p *Plugin) SettingValues() map[string]string {
	out := map[string]string{}
	for _, s := range p.Settings {
		out[s.Key] = s.Default
	}
	if p.Record != nil {
		for _, s := range p.Settings {
			v, ok := p.Record.Settings[s.Key]
			// An empty number or select means "the default", so that a
			// default changed in a later version takes effect.
			if !ok || (v == "" && (s.Type == "number" || s.Type == "select")) {
				continue
			}
			out[s.Key] = v
		}
	}
	return out
}

func (p *Plugin) Logs() []LogLine {
	p.logMu.Lock()
	defer p.logMu.Unlock()
	return slices.Clone(p.logs)
}

func (p *Plugin) addLog(level, text string) {
	if len(text) > 2000 {
		text = text[:2000] + "…"
	}
	p.logMu.Lock()
	p.logs = append(p.logs, LogLine{At: time.Now(), Level: level, Text: text})
	if len(p.logs) > 200 {
		p.logs = p.logs[len(p.logs)-200:]
	}
	p.logMu.Unlock()
}

func (p *Plugin) paused() bool {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	return time.Now().Before(p.pausedTil)
}

func (p *Plugin) recordResult(err error) {
	p.failMu.Lock()
	defer p.failMu.Unlock()
	if err == nil {
		p.failures = 0
		return
	}
	p.failures++
	if p.failures >= failureThreshold {
		p.pausedTil = time.Now().Add(cooldown)
		p.failures = 0
	}
}

type instance struct {
	mod    api.Module
	alloc  api.Function
	handle api.Function
}

type Host struct {
	dir   string
	store *store.Store
	log   *slog.Logger
	site  func() SiteInfo
	links Links
	rt    wazero.Runtime
	httpc *http.Client

	mu         sync.RWMutex
	plugins    map[string]*Plugin
	shortcodes map[string]*Plugin
	compiled   map[string]wazero.CompiledModule // by sha256, reused across reloads
	csp        map[string][]string              // sources added by enabled plugins
	gen        atomic.Uint64

	// Optional services wired by the server (SetMailer, SetSecrets, ...).
	secrets     Secrets
	mailer      Mailer
	mailLimit   *auth.Limiter
	mailq       chan mailJob
	blocklist   string // optional extra list of known-bad SHA-256 hashes
	installMu   sync.Mutex
	mailPerHour int

	tzMu   sync.Mutex
	tzName string
	tzLoc  *time.Location

	events chan eventJob
	wg     sync.WaitGroup
	closed atomic.Bool
}

// Secrets encrypts secret settings at rest (auth.Keys implements it).
type Secrets interface {
	Encrypt(plaintext, additional []byte) ([]byte, error)
	Decrypt(ciphertext, additional []byte) ([]byte, error)
}

// Mailer delivers the e-mails of the mail_send host function.
type Mailer interface {
	SendMessage(to, replyTo, subject, body string) error
}

// SetSecrets enables encryption of "secret" settings with the instance keys.
func (h *Host) SetSecrets(s Secrets) { h.secrets = s }

// SetMailer enables the mail_send host function.
func (h *Host) SetMailer(m Mailer) { h.mailer = m }

// SetBlocklistFile adds a file of known-bad SHA-256 hashes (one per line,
// # comments) to the list embedded in the binary.
func (h *Host) SetBlocklistFile(path string) { h.blocklist = path }

// Dir is the plugins directory.
func (h *Host) Dir() string { return h.dir }

type eventJob struct {
	name string
	data any
}

// NewHost creates the runtime. cacheDir stores compiled machine code so that
// restarts are fast; it may be empty.
func NewHost(ctx context.Context, dir, cacheDir string, st *store.Store, log *slog.Logger, site func() SiteInfo, links Links) (*Host, error) {
	rc := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(memoryLimitPages).
		WithCloseOnContextDone(true)
	if cacheDir != "" {
		if err := os.MkdirAll(cacheDir, 0o750); err == nil {
			if cache, err := wazero.NewCompilationCacheWithDir(cacheDir); err == nil {
				rc = rc.WithCompilationCache(cache)
			}
		}
	}
	h := &Host{
		dir: dir, store: st, log: log.With("component", "plugins"), site: site, links: links,
		rt:          wazero.NewRuntimeWithConfig(ctx, rc),
		httpc:       newHTTPClient(),
		plugins:     map[string]*Plugin{},
		shortcodes:  map[string]*Plugin{},
		compiled:    map[string]wazero.CompiledModule{},
		events:      make(chan eventJob, 256),
		mailq:       make(chan mailJob, 64),
		mailPerHour: MailPerHour,
	}
	h.mailLimit = auth.NewLimiter(h.mailPerHour, time.Hour)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, h.rt); err != nil {
		return nil, err
	}
	if err := h.registerHostModule(ctx); err != nil {
		return nil, err
	}
	for range 2 {
		h.wg.Add(1)
		go h.eventWorker()
	}
	h.wg.Add(1)
	go h.mailWorker()
	return h, nil
}

func (h *Host) Close(ctx context.Context) {
	if h.closed.Swap(true) {
		return
	}
	close(h.events)
	close(h.mailq)
	h.wg.Wait()
	h.rt.Close(ctx)
}

// Generation changes whenever the set of active plugins or their settings
// change; renderers include it in cache keys.
func (h *Host) Generation() uint64 { return h.gen.Load() }

// Load scans the plugins directory and (re)compiles approved plugins.
func (h *Host) Load(ctx context.Context) error {
	records, err := h.store.ListPluginRecords(ctx)
	if err != nil {
		return err
	}
	found := map[string]*Plugin{}
	entries, err := os.ReadDir(h.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := h.inspect(ctx, e.Name(), records)
		if p != nil {
			found[p.ID] = p
		}
	}

	shortcodes := map[string]*Plugin{}
	csp := map[string][]string{}
	ids := make([]string, 0, len(found))
	for id := range found {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		p := found[id]
		if p.Status != StatusEnabled {
			continue
		}
		for _, sc := range p.Shortcodes {
			if other, taken := shortcodes[sc]; taken {
				p.addLog("warn", fmt.Sprintf("shortcode [%s] is already provided by %s", sc, other.ID))
				continue
			}
			shortcodes[sc] = p
		}
		for _, perm := range p.EffectivePermissions() {
			if rest, ok := strings.CutPrefix(perm, PermCSP); ok && p.Granted(perm) {
				dir, src, _ := strings.Cut(rest, ":")
				if !slices.Contains(csp[dir], src) {
					csp[dir] = append(csp[dir], src)
				}
			}
		}
	}

	h.mu.Lock()
	old := h.plugins
	for id, np := range found {
		if op, ok := old[id]; ok {
			np.logs = op.Logs() // keep the log across reloads
		}
	}
	h.plugins = found
	h.shortcodes = shortcodes
	h.csp = csp
	h.mu.Unlock()
	h.gen.Add(1)

	// Retire the previous generation: idle instances are closed now, busy
	// ones when they are released.
	for _, p := range old {
		p.retired.Store(true)
		if p.pool == nil {
			continue
		}
	drain:
		for {
			select {
			case inst := <-p.pool:
				inst.mod.Close(ctx)
			default:
				break drain
			}
		}
	}
	return nil
}

func (h *Host) inspect(ctx context.Context, dirName string, records map[string]store.PluginRecord) *Plugin {
	dir := filepath.Join(h.dir, dirName)
	p := &Plugin{Dir: dir, Status: StatusInvalid}
	raw, err := readLimited(filepath.Join(dir, "plugin.json"), 64<<10)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		p.ID, p.Name, p.Err = dirName, dirName, err.Error()
		return p
	}
	m, err := ParseManifest(raw)
	if m != nil {
		p.Manifest = *m
	}
	if err == nil && m.ID != dirName {
		err = fmt.Errorf("plugin id %q must match its directory name %q", m.ID, dirName)
	}
	if err != nil {
		if p.ID == "" || !idRe.MatchString(p.ID) {
			p.ID = dirName
		}
		if p.Name == "" {
			p.Name = dirName
		}
		p.Err = err.Error()
		return p
	}
	wasm, err := readLimited(filepath.Join(dir, m.Wasm), MaxWasmBytes)
	if err != nil {
		p.Err = "wasm: " + err.Error()
		return p
	}
	sum := sha256.Sum256(wasm)
	p.SHA256 = hex.EncodeToString(sum[:])
	assets, assetsSHA, err := readAssets(dir)
	if err != nil {
		p.Err = err.Error()
		return p
	}
	p.AssetsSHA256 = assetsSHA
	if rec, ok := records[m.ID]; ok {
		r := rec
		p.Record = &r
		if len(r.ScanReport) > 0 {
			var rep Report
			if json.Unmarshal(r.ScanReport, &rep) == nil {
				p.Report = &rep
			}
		}
	}
	switch {
	case p.Record == nil || !p.Record.Enabled:
		p.Status = StatusAvailable
		return p
	case p.Record.WasmSHA256 != p.SHA256:
		p.Status = StatusModified
		p.Err = "the .wasm file changed since it was approved"
		return p
	case p.Record.AssetsSHA256 != p.AssetsSHA256:
		p.Status = StatusModified
		p.Err = "the files under assets/ changed since the plugin was approved"
		return p
	case !subset(m.EffectivePermissions(), p.Record.GrantedPermissions):
		p.Status = StatusModified
		p.Err = "the plugin now requests permissions that were not approved"
		return p
	case p.Record.ReviewRequired:
		p.Status = StatusAvailable
		p.Err = "waiting for review by the network operator"
		return p
	}
	compiled, err := h.compile(ctx, p.SHA256, wasm)
	if err != nil {
		p.Status = StatusError
		p.Err = err.Error()
		return p
	}
	p.compiled = compiled
	p.pool = make(chan *instance, maxInstances)
	p.assets = assets
	p.Status = StatusEnabled
	return p
}

func (h *Host) compile(ctx context.Context, sha string, wasm []byte) (wazero.CompiledModule, error) {
	h.mu.RLock()
	c, ok := h.compiled[sha]
	h.mu.RUnlock()
	if ok {
		return c, nil
	}
	c, err := h.rt.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	for _, f := range c.ImportedFunctions() {
		mod, name, _ := f.Import()
		if mod != "wasi_snapshot_preview1" && mod != "sap13ns" {
			c.Close(ctx)
			return nil, fmt.Errorf("imports %s.%s: only wasi_snapshot_preview1 and sap13ns imports are allowed", mod, name)
		}
	}
	exports := c.ExportedFunctions()
	for _, name := range []string{"sap13ns_alloc", "sap13ns_handle"} {
		if _, ok := exports[name]; !ok {
			c.Close(ctx)
			return nil, fmt.Errorf("missing export %s", name)
		}
	}
	if len(c.ExportedMemories()) == 0 {
		c.Close(ctx)
		return nil, errors.New("module does not export its memory")
	}
	h.mu.Lock()
	h.compiled[sha] = c
	h.mu.Unlock()
	return c, nil
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("file larger than %d bytes", max)
	}
	return b, nil
}

func subset(want, have []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

func (h *Host) List() []*Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*Plugin, 0, len(h.plugins))
	for _, p := range h.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Host) Get(id string) *Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plugins[id]
}

func (h *Host) enabled() []*Plugin {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []*Plugin
	for _, p := range h.plugins {
		if p.Status == StatusEnabled {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ErrReviewRequired is returned by Approve while the network operator has
// not reviewed a plugin uploaded by a tenant.
var ErrReviewRequired = errors.New("the network operator must review this plugin first")

// Approve re-reads the plugin from disk and pins its current wasm hash,
// assets hash and requested permissions (CSP sources included). The caller
// passes the wasm hash it showed to the administrator; a different build on
// disk is refused.
func (h *Host) Approve(ctx context.Context, id, reviewedSHA string, userID int64) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid plugin id")
	}
	if p := h.Get(id); p != nil && p.Record != nil && p.Record.ReviewRequired {
		return ErrReviewRequired
	}
	raw, err := readLimited(filepath.Join(h.dir, id, "plugin.json"), 64<<10)
	if err != nil {
		return err
	}
	m, err := ParseManifest(raw)
	if err != nil {
		return err
	}
	if m.ID != id {
		return errors.New("plugin id does not match its directory")
	}
	wasm, err := readLimited(filepath.Join(h.dir, id, m.Wasm), MaxWasmBytes)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(wasm)
	sha := hex.EncodeToString(sum[:])
	if reviewedSHA != "" && reviewedSHA != sha {
		return errors.New("the plugin changed on disk while it was being reviewed")
	}
	_, assetsSHA, err := readAssets(filepath.Join(h.dir, id))
	if err != nil {
		return err
	}
	if _, err := h.compile(ctx, sha, wasm); err != nil {
		return err
	}
	if err := h.store.ApprovePlugin(ctx, id, sha, assetsSHA, m.EffectivePermissions(), userID); err != nil {
		return err
	}
	return h.Load(ctx)
}

func (h *Host) Disable(ctx context.Context, id string) error {
	if err := h.store.DisablePlugin(ctx, id); err != nil {
		return err
	}
	return h.Load(ctx)
}

// SaveSettings validates values against the manifest before storing them.
// Secret values are encrypted with the instance keys.
func (h *Host) SaveSettings(ctx context.Context, id string, values map[string]string) error {
	p := h.Get(id)
	if p == nil || p.Record == nil {
		return errors.New("plugin must be installed before it can be configured")
	}
	clean := map[string]string{}
	for _, s := range p.Settings {
		v, ok := values[s.Key]
		if !ok {
			// Keys that were not submitted keep their stored value (or
			// their default, when nothing was stored), and an e-mail
			// recipient keeps its mark only while the key is still an
			// e-mail setting.
			if old, stored := p.Record.Settings[s.Key]; stored {
				clean[s.Key] = old
			}
			if m := p.Record.Settings[emailMark+s.Key]; s.Type == "email" && m != "" {
				clean[emailMark+s.Key] = m
			}
			continue
		}
		v, err := h.cleanSetting(s, v)
		if err != nil {
			return err
		}
		if s.Type == "email" && v != "" {
			clean[emailMark+s.Key] = "1"
		}
		if s.Type == "secret" && v != "" {
			enc, err := h.sealSecret(id, s.Key, v)
			if err != nil {
				return err
			}
			v = enc
		}
		clean[s.Key] = v
	}
	if err := h.store.SavePluginSettings(ctx, id, clean); err != nil {
		return err
	}
	return h.Load(ctx)
}

// cleanSetting validates and normalizes one setting or widget config value.
func (h *Host) cleanSetting(s SettingDecl, v string) (string, error) {
	if len(v) > 4000 {
		return "", fmt.Errorf("%s: value too long", s.Label)
	}
	switch s.Type {
	case "text", "textarea", "secret":
		if !utf8.ValidString(v) {
			return "", fmt.Errorf("%s: invalid text", s.Label)
		}
	case "bool":
		if v != "true" {
			v = "false"
		}
	case "select":
		if v != "" && !slices.Contains(s.Choices, v) {
			return "", fmt.Errorf("%s: invalid choice", s.Label)
		}
	case "number":
		if v != "" {
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return "", fmt.Errorf("%s: must be a number", s.Label)
			}
		}
	case "url":
		if v != "" && !validHTTPS(v) {
			return "", fmt.Errorf("%s: must be an https:// URL", s.Label)
		}
	case "email":
		if v != "" && !validEmail(v) {
			return "", fmt.Errorf("%s: must be one e-mail address", s.Label)
		}
	case "datetime":
		// Stored as local site time, the format of <input type="datetime-local">.
		if v != "" {
			loc := h.siteLocation()
			t, err := ParseSettingTime(v, loc)
			if err != nil {
				return "", fmt.Errorf("%s: must be a date and time", s.Label)
			}
			v = t.In(loc).Format(settingTimeLayout)
		}
	}
	if s.Required && v == "" {
		return "", fmt.Errorf("%s is required", s.Label)
	}
	return v, nil
}

func validHTTPS(v string) bool {
	u, err := url.Parse(v)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// siteLocation is the site's time zone (UTC if unknown).
func (h *Host) siteLocation() *time.Location {
	name := ""
	if h.site != nil {
		name = h.site().Timezone
	}
	h.tzMu.Lock()
	defer h.tzMu.Unlock()
	if h.tzLoc == nil || h.tzName != name {
		loc, err := time.LoadLocation(name)
		if err != nil {
			loc = time.UTC
		}
		h.tzName, h.tzLoc = name, loc
	}
	return h.tzLoc
}

// WidgetConfig returns the per-instance fields of a plugin widget kind
// ("plugin:<id>:<widget>"), or nil.
func (h *Host) WidgetConfig(kind string) []SettingDecl {
	parts := strings.SplitN(kind, ":", 3)
	if len(parts) != 3 || parts[0] != "plugin" {
		return nil
	}
	p := h.Get(parts[1])
	if p == nil {
		return nil
	}
	for _, w := range p.Widgets {
		if w.ID == parts[2] {
			return w.Config
		}
	}
	return nil
}

// CleanWidgetConfig validates the submitted config of a plugin widget.
// Missing fields get their default.
func (h *Host) CleanWidgetConfig(kind string, values map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for _, s := range h.WidgetConfig(kind) {
		v, ok := values[s.Key]
		if !ok {
			v = s.Default
		}
		v, err := h.cleanSetting(s, v)
		if err != nil {
			return nil, err
		}
		out[s.Key] = v
	}
	return out, nil
}

// ---------------------------------------------------------------- calls --

type envelope struct {
	ABI     int            `json:"abi"`
	Hook    string         `json:"hook"`
	Event   string         `json:"event,omitempty"`
	Plugin  envelopePlugin `json:"plugin"`
	Site    SiteInfo       `json:"site"`
	Payload any            `json:"payload"`
}

type envelopePlugin struct {
	ID       string            `json:"id"`
	Version  string            `json:"version"`
	Settings map[string]string `json:"settings"`
}

type response struct {
	HTML    *string           `json:"html,omitempty"`
	Meta    []MetaTag         `json:"meta,omitempty"`
	JSONLD  []json.RawMessage `json:"jsonld,omitempty"`
	Verdict string            `json:"verdict,omitempty"` // comment.check
	Reason  string            `json:"reason,omitempty"`
	OK      bool              `json:"ok,omitempty"` // form.submit
	Message string            `json:"message,omitempty"`
	Errors  map[string]string `json:"errors,omitempty"`
	Error   string            `json:"error,omitempty"`
}

type stateKey struct{}

// access is what a call may do beyond computing: render hooks run while a
// page is being served and get neither the network nor e-mail.
type access uint8

const (
	accessNet  access = 1 << iota // http_fetch
	accessMail                    // mail_send
)

// callState is attached to the context of each call so that host functions
// know which plugin is calling and what it is allowed to do.
type callState struct {
	plugin    *Plugin
	allowHTTP bool
	allowMail bool
	dryRun    bool // scanner sandbox: no storage, network or e-mail at all
	pending   []byte
	httpCalls int
	kvOps     int
	dbQueries int
	mailCalls int
}

func (p *Plugin) acquire(ctx context.Context, h *Host) (*instance, error) {
	select {
	case inst := <-p.pool:
		return inst, nil
	default:
	}
	if p.created.Add(1) <= maxInstances {
		inst, err := h.instantiate(p)
		if err != nil {
			p.created.Add(-1)
			return nil, err
		}
		return inst, nil
	}
	p.created.Add(-1)
	select {
	case inst := <-p.pool:
		return inst, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Plugin) release(inst *instance, broken bool) {
	if broken || p.retired.Load() {
		inst.mod.Close(context.Background())
		p.created.Add(-1)
		return
	}
	select {
	case p.pool <- inst:
	default:
		inst.mod.Close(context.Background())
		p.created.Add(-1)
	}
}

type logWriter struct {
	p     *Plugin
	level string
}

func (w logWriter) Write(b []byte) (int, error) {
	if s := strings.TrimSpace(string(b)); s != "" {
		w.p.addLog(w.level, s)
	}
	return len(b), nil
}

func (h *Host) instantiate(p *Plugin) (*instance, error) {
	ctx, cancel := context.WithTimeout(context.Background(), instantiateTimeout)
	defer cancel()
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize").
		WithStdout(logWriter{p, "info"}).
		WithStderr(logWriter{p, "error"}).
		WithSysWalltime().
		WithSysNanotime().
		WithRandSource(rand.Reader).
		WithArgs(p.ID)
	mod, err := h.rt.InstantiateModule(ctx, p.compiled, cfg)
	if err != nil {
		p.addLog("error", "instantiate: "+err.Error())
		return nil, err
	}
	inst := &instance{mod: mod, alloc: mod.ExportedFunction("sap13ns_alloc"), handle: mod.ExportedFunction("sap13ns_handle")}
	if inst.alloc == nil || inst.handle == nil || mod.Memory() == nil {
		mod.Close(ctx)
		return nil, errors.New("plugin exports are incomplete")
	}
	return inst, nil
}

// call runs one hook invocation inside the sandbox.
func (h *Host) call(ctx context.Context, p *Plugin, hook, event string, payload any, timeout time.Duration, acc access) (*response, error) {
	if p.paused() {
		return nil, errors.New("plugin paused after repeated failures")
	}
	in, err := json.Marshal(envelope{
		ABI: ABIVersion, Hook: hook, Event: event,
		Plugin:  envelopePlugin{ID: p.ID, Version: p.Version, Settings: h.pluginSettings(p)},
		Site:    h.site(),
		Payload: payload,
	})
	if err != nil {
		return nil, err
	}
	if len(in) > maxInputBytes {
		return nil, errors.New("input too large")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if p.dryRun {
		acc = 0
	}
	ctx = context.WithValue(ctx, stateKey{}, &callState{plugin: p, allowHTTP: acc&accessNet != 0, allowMail: acc&accessMail != 0, dryRun: p.dryRun})

	inst, err := p.acquire(ctx, h)
	if err != nil {
		p.recordResult(err)
		return nil, err
	}
	out, err := invoke(ctx, inst, in)
	p.release(inst, err != nil)
	p.recordResult(err)
	if err != nil {
		p.addLog("error", fmt.Sprintf("%s: %v", hook, err))
		return nil, err
	}
	var resp response
	if err := json.Unmarshal(out, &resp); err != nil {
		p.addLog("error", hook+": invalid JSON response")
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	if resp.Error != "" {
		p.addLog("warn", hook+": "+resp.Error)
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

func invoke(ctx context.Context, inst *instance, in []byte) ([]byte, error) {
	res, err := inst.alloc.Call(ctx, uint64(len(in)))
	if err != nil {
		return nil, fmt.Errorf("alloc: %w", err)
	}
	ptr := uint32(res[0])
	if !inst.mod.Memory().Write(ptr, in) {
		return nil, errors.New("alloc returned an out-of-bounds pointer")
	}
	res, err = inst.handle.Call(ctx, uint64(ptr), uint64(len(in)))
	if err != nil {
		return nil, err
	}
	optr, olen := uint32(res[0]>>32), uint32(res[0])
	if olen > maxOutputBytes {
		return nil, errors.New("response too large")
	}
	view, ok := inst.mod.Memory().Read(optr, olen)
	if !ok {
		return nil, errors.New("response out of bounds")
	}
	return slices.Clone(view), nil
}

// ---------------------------------------------------------------- hooks --

func (h *Host) HasShortcode(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.shortcodes[name]
	return ok
}

type shortcodePayload struct {
	Name  string            `json:"name"`
	Attrs map[string]string `json:"attrs"`
	Inner string            `json:"inner"`
	Post  *PostInfo         `json:"post,omitempty"`
}

// Shortcode renders one shortcode; the caller sanitizes the HTML.
func (h *Host) Shortcode(ctx context.Context, name string, attrs map[string]string, inner string, post *PostInfo) (string, error) {
	h.mu.RLock()
	p := h.shortcodes[name]
	h.mu.RUnlock()
	if p == nil {
		return "", errors.New("unknown shortcode")
	}
	resp, err := h.call(ctx, p, HookShortcode, "", shortcodePayload{Name: name, Attrs: attrs, Inner: inner, Post: post}, renderTimeout, 0)
	if err != nil || resp.HTML == nil {
		return "", err
	}
	return qualifyForms(p, *resp.HTML, ""), nil
}

type filterPayload struct {
	HTML string    `json:"html"`
	Post *PostInfo `json:"post,omitempty"`
}

// FilterContent passes rendered HTML through every content.filter plugin in
// id order. sanitize is applied to each plugin's output.
func (h *Host) FilterContent(ctx context.Context, html string, post *PostInfo, sanitize func(string) string) string {
	for _, p := range h.enabled() {
		if !p.HasHook(HookContentFilter) {
			continue
		}
		resp, err := h.call(ctx, p, HookContentFilter, "", filterPayload{HTML: html, Post: post}, renderTimeout, 0)
		if err != nil || resp.HTML == nil {
			continue
		}
		html = sanitize(qualifyForms(p, *resp.HTML, html))
	}
	return html
}

type widgetPayload struct {
	Widget string            `json:"widget"`
	Config map[string]string `json:"config"`
	Page   *PageInfo         `json:"page"`
}

func (h *Host) Widget(ctx context.Context, pluginID, widgetID string, config map[string]string, page *PageInfo) (string, error) {
	p := h.Get(pluginID)
	if p == nil || p.Status != StatusEnabled || !p.HasHook(HookWidget) {
		return "", errors.New("widget plugin not available")
	}
	cfg := map[string]string{}
	for k, v := range config {
		cfg[k] = v
	}
	for _, w := range p.Widgets {
		if w.ID != widgetID {
			continue
		}
		for _, d := range w.Config {
			if _, ok := cfg[d.Key]; !ok {
				// A field added after the widget was placed: its default,
				// normalized like a saved value ("" bool -> "false").
				v, err := h.cleanSetting(d, d.Default)
				if err != nil {
					v = d.Default
				}
				cfg[d.Key] = v
			}
		}
		cfg = h.exportTimes(w.Config, cfg)
	}
	resp, err := h.call(ctx, p, HookWidget, "", widgetPayload{Widget: widgetID, Config: cfg, Page: page}, renderTimeout, 0)
	if err != nil || resp.HTML == nil {
		return "", err
	}
	return qualifyForms(p, *resp.HTML, ""), nil
}

// HeadOutput is what head.meta plugins add to <head>.
type HeadOutput struct {
	Meta []MetaTag
	// JSONLD are compact JSON objects, already safe to place between
	// <script type="application/ld+json"> and </script> ("<", ">" and "&"
	// are escaped as \u003c, \u003e and \u0026).
	JSONLD []string
}

// Head collects <meta> tags and JSON-LD objects. Names are validated and
// http-equiv is never allowed, so plugins cannot set refresh or CSP
// directives; JSON-LD must be a valid JSON object within the size caps.
func (h *Host) Head(ctx context.Context, page *PageInfo) HeadOutput {
	var out HeadOutput
	total := 0
	for _, p := range h.enabled() {
		if !p.HasHook(HookHeadMeta) {
			continue
		}
		resp, err := h.call(ctx, p, HookHeadMeta, "", page, renderTimeout, 0)
		if err != nil {
			continue
		}
		for i, m := range resp.Meta {
			if i >= 20 {
				break
			}
			if (m.Name == "") == (m.Property == "") || !metaNameRe.MatchString(m.Name+m.Property) || len(m.Content) > 1000 {
				continue
			}
			out.Meta = append(out.Meta, m)
		}
		for i, raw := range resp.JSONLD {
			if i >= 5 {
				break
			}
			ld, ok := safeJSONLD(raw)
			if !ok || total+len(ld) > maxJSONLDTotal {
				p.addLog("warn", "head.meta: JSON-LD object dropped (invalid, not an object or too large)")
				continue
			}
			total += len(ld)
			out.JSONLD = append(out.JSONLD, ld)
		}
	}
	return out
}

// HeadMeta collects the <meta> tags of head.meta plugins.
func (h *Host) HeadMeta(ctx context.Context, page *PageInfo) []MetaTag { return h.Head(ctx, page).Meta }

// safeJSONLD validates one JSON-LD object and returns it compacted with
// HTML-significant characters escaped, so that it cannot close the script
// element it is placed in.
func safeJSONLD(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxJSONLDBytes || trimmed[0] != '{' || !json.Valid(trimmed) {
		return "", false
	}
	var compact, safe bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "", false
	}
	json.HTMLEscape(&safe, compact.Bytes())
	return safe.String(), true
}

// ---------------------------------------------------------- forms ------

// formPlaceholderRe matches the marker returned by pluginsdk.FormPlaceholder.
var (
	formPlaceholderRe = regexp.MustCompile(`<div data-form="([a-z][a-z0-9_-]{0,39})"></div>`)
	qualifiedFormRe   = regexp.MustCompile(`<div data-form="[a-z0-9][a-z0-9-]{1,49}\.[a-z][a-z0-9_-]{0,39}"></div>`)
	formAttrRe        = regexp.MustCompile(`(?i)data-form`)
)

// qualifyForms turns the plugin's own form placeholders into
// <div data-form="plugin.form"></div>, which the web layer replaces with the
// rendered form, and neutralizes any other data-form attribute so that a
// plugin cannot place another plugin's form. Qualified placeholders already
// present in input (content.filter receives them) are kept.
func qualifyForms(p *Plugin, out, input string) string {
	if !strings.Contains(strings.ToLower(out), "data-form") {
		return out
	}
	nonce := make([]byte, 8)
	rand.Read(nonce)
	marker := "sapform" + hex.EncodeToString(nonce) + "x"
	var keep []string
	slot := func(html string) string {
		keep = append(keep, html)
		return marker + strconv.Itoa(len(keep)-1) + "x"
	}
	allowed := map[string]bool{}
	for _, m := range qualifiedFormRe.FindAllString(input, -1) {
		allowed[m] = true
	}
	out = qualifiedFormRe.ReplaceAllStringFunc(out, func(m string) string {
		if allowed[m] {
			return slot(m)
		}
		return ""
	})
	out = formPlaceholderRe.ReplaceAllStringFunc(out, func(m string) string {
		id := formPlaceholderRe.FindStringSubmatch(m)[1]
		if p.Form(id) == nil {
			return ""
		}
		return slot(`<div data-form="` + p.ID + "." + id + `"></div>`)
	})
	out = formAttrRe.ReplaceAllString(out, "data-blocked")
	for i, html := range keep {
		out = strings.Replace(out, marker+strconv.Itoa(i)+"x", html, 1)
	}
	return out
}

// Form returns a form declared by an enabled plugin with the form.submit hook.
func (h *Host) Form(pluginID, formID string) (*FormDecl, bool) {
	p := h.Get(pluginID)
	if p == nil || p.Status != StatusEnabled || !p.HasHook(HookFormSubmit) {
		return nil, false
	}
	f := p.Manifest.Form(formID)
	return f, f != nil
}

type formPayload struct {
	Form    string            `json:"form"`
	Fields  map[string]string `json:"fields"`
	Lang    string            `json:"lang"`
	PageURL string            `json:"page_url"`
}

// FormResult is a plugin's answer to a form submission.
type FormResult struct {
	OK      bool
	Message string
	Errors  map[string]string // by declared field name
}

// SubmitForm passes validated fields to the plugin's form.submit hook. The
// plugin may use the network and e-mail while it handles the submission.
func (h *Host) SubmitForm(ctx context.Context, pluginID, formID string, fields map[string]string, lang, pageURL string) (*FormResult, error) {
	f, ok := h.Form(pluginID, formID)
	if !ok {
		return nil, errors.New("form not available")
	}
	p := h.Get(pluginID)
	resp, err := h.call(ctx, p, HookFormSubmit, "", formPayload{Form: formID, Fields: fields, Lang: lang, PageURL: pageURL}, formTimeout, accessNet|accessMail)
	if err != nil {
		return nil, err
	}
	out := &FormResult{OK: resp.OK, Message: truncate(resp.Message, 500), Errors: map[string]string{}}
	for _, fd := range f.Fields {
		if msg := resp.Errors[fd.Name]; msg != "" {
			out.Errors[fd.Name] = truncate(msg, 200)
		}
	}
	return out, nil
}

// ------------------------------------------------------- comment.check --

// CommentInput is a new comment as given to comment.check plugins.
type CommentInput struct {
	Author     string
	URL        string
	Content    string
	Post       *PostInfo
	ParentID   int64
	Registered bool
	// Personal data, only sent to plugins granted "comments:pii".
	Email     string
	IP        string
	UserAgent string
}

type commentPayload struct {
	Author     string    `json:"author"`
	URL        string    `json:"url"`
	Content    string    `json:"content"`
	Post       *PostInfo `json:"post,omitempty"`
	ParentID   int64     `json:"parent_id,omitempty"`
	Registered bool      `json:"registered"`
	Email      string    `json:"email,omitempty"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
}

// Comment verdicts, from the most lenient to the strictest.
const (
	VerdictOK      = "ok"
	VerdictPending = "pending"
	VerdictSpam    = "spam"
)

var verdictRank = map[string]int{VerdictOK: 0, VerdictPending: 1, VerdictSpam: 2}

// CommentVerdict is the combined answer of the comment.check plugins.
type CommentVerdict struct {
	Verdict string // ok | pending | spam
	Reason  string
	Plugin  string // id of the plugin that decided; "" for ok
}

// CheckComment asks every comment.check plugin about a new comment; the
// strictest verdict wins. The whole check is bounded by commentTimeout and
// fails open: errors, timeouts and unknown verdicts count as "ok", so the
// site's normal moderation applies.
func (h *Host) CheckComment(ctx context.Context, in CommentInput) CommentVerdict {
	out := CommentVerdict{Verdict: VerdictOK}
	ctx, cancel := context.WithTimeout(ctx, commentTimeout)
	defer cancel()
	for _, p := range h.enabled() {
		if !p.HasHook(HookCommentCheck) {
			continue
		}
		if ctx.Err() != nil {
			p.addLog("warn", "comment.check skipped: time budget used up by other plugins")
			continue
		}
		pl := commentPayload{Author: in.Author, URL: in.URL, Content: in.Content, Post: in.Post, ParentID: in.ParentID, Registered: in.Registered}
		if p.Granted(PermCommentsPII) {
			pl.Email, pl.IP, pl.UserAgent = in.Email, in.IP, in.UserAgent
		}
		resp, err := h.call(ctx, p, HookCommentCheck, "", pl, commentTimeout, accessNet)
		if err != nil {
			continue
		}
		rank, known := verdictRank[resp.Verdict]
		if !known {
			if resp.Verdict != "" {
				p.addLog("warn", "comment.check: unknown verdict "+truncate(resp.Verdict, 40))
			}
			continue
		}
		if resp.Verdict != VerdictOK {
			p.addLog("info", fmt.Sprintf("comment.check: %s by %q: %s", resp.Verdict, truncate(in.Author, 60), truncate(resp.Reason, 300)))
		}
		if rank > verdictRank[out.Verdict] {
			out = CommentVerdict{Verdict: resp.Verdict, Reason: truncate(resp.Reason, 300), Plugin: p.ID}
		}
	}
	return out
}

// ------------------------------------------------------ assets and CSP --

// AssetLinks returns the stylesheet and script URLs of enabled plugins, in
// plugin id order. Scripts are only linked with the "assets:js" permission.
func (h *Host) AssetLinks() (css, js []string) {
	for _, p := range h.enabled() {
		base := "/plugin-assets/" + p.ID + "/" + p.AssetsVersion() + "/"
		for _, a := range p.Assets.CSS {
			if _, ok := p.assets[a]; ok {
				css = append(css, base+a)
			}
		}
		if !p.Granted(PermAssetsJS) {
			continue
		}
		for _, a := range p.Assets.JS {
			if _, ok := p.assets[a]; ok {
				js = append(js, base+a)
			}
		}
	}
	return css, js
}

// Asset returns a file under assets/ of an enabled plugin. version must be
// the current AssetsVersion, so URLs are immutable.
func (h *Host) Asset(id, version, name string) ([]byte, string, bool) {
	p := h.Get(id)
	if p == nil || p.Status != StatusEnabled || version != p.AssetsVersion() {
		return nil, "", false
	}
	a, ok := p.assets[name]
	if !ok {
		return nil, "", false
	}
	// By the type it is served with, not the name: "x.JS" is JavaScript too.
	if strings.HasPrefix(a.contentType, "text/javascript") && !p.Granted(PermAssetsJS) {
		return nil, "", false
	}
	return a.data, a.contentType, true
}

// CSPSources returns the Content-Security-Policy sources added by enabled
// plugins, by directive ("frame-src", "img-src").
func (h *Host) CSPSources() map[string][]string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string][]string, len(h.csp))
	for k, v := range h.csp {
		out[k] = slices.Clone(v)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// WidgetsOf lists the widgets offered by enabled plugins as "plugin:<id>:<widget>".
func (h *Host) WidgetKinds() map[string]string {
	out := map[string]string{}
	for _, p := range h.enabled() {
		for _, w := range p.Widgets {
			out["plugin:"+p.ID+":"+w.ID] = p.Name + " — " + w.Title
		}
	}
	return out
}

// Emit queues an event for asynchronous delivery. It never blocks.
func (h *Host) Emit(event string, data any) {
	if h.closed.Load() {
		return
	}
	select {
	case h.events <- eventJob{name: event, data: data}:
	default:
		h.log.Warn("plugin event queue full; event dropped", "event", event)
	}
}

func (h *Host) eventWorker() {
	defer h.wg.Done()
	for job := range h.events {
		for _, p := range h.enabled() {
			if !p.HasHook(HookEvent) || !p.HasEvent(job.name) {
				continue
			}
			if _, err := h.call(context.Background(), p, HookEvent, job.name, job.data, eventTimeout, accessNet|accessMail); err != nil {
				h.log.Debug("plugin event failed", "plugin", p.ID, "event", job.name, "err", err)
			}
		}
	}
}

// RunCron emits cron.hourly until ctx is cancelled.
func (h *Host) RunCron(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			h.Emit("cron.hourly", map[string]string{"time": now.UTC().Format(time.RFC3339)})
		}
	}
}
