package plugin

import (
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
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

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
	eventTimeout       = 15 * time.Second
	instantiateTimeout = 10 * time.Second
	failureThreshold   = 5
	cooldown           = time.Minute
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
}

type PostInfo struct {
	ID          int64    `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Slug        string   `json:"slug"`
	Lang        string   `json:"lang"`
	URL         string   `json:"url"`
	Excerpt     string   `json:"excerpt"`
	Author      string   `json:"author"`
	Categories  []string `json:"categories"`
	Tags        []string `json:"tags"`
	PublishedAt string   `json:"published_at,omitempty"`
	WordCount   int      `json:"word_count"`
}

type PageInfo struct {
	Kind        string    `json:"kind"`
	URL         string    `json:"url"`
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
	Dir    string
	SHA256 string
	Status string
	Err    string
	Record *store.PluginRecord

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

// SettingValues merges stored values over declared defaults.
func (p *Plugin) SettingValues() map[string]string {
	out := map[string]string{}
	for _, s := range p.Settings {
		out[s.Key] = s.Default
	}
	if p.Record != nil {
		for k, v := range p.Record.Settings {
			if _, declared := out[k]; declared {
				out[k] = v
			}
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
	gen        atomic.Uint64

	events chan eventJob
	wg     sync.WaitGroup
	closed atomic.Bool
}

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
		rt:         wazero.NewRuntimeWithConfig(ctx, rc),
		httpc:      newHTTPClient(),
		plugins:    map[string]*Plugin{},
		shortcodes: map[string]*Plugin{},
		compiled:   map[string]wazero.CompiledModule{},
		events:     make(chan eventJob, 256),
	}
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
	return h, nil
}

func (h *Host) Close(ctx context.Context) {
	if h.closed.Swap(true) {
		return
	}
	close(h.events)
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
	if rec, ok := records[m.ID]; ok {
		r := rec
		p.Record = &r
	}
	switch {
	case p.Record == nil || !p.Record.Enabled:
		p.Status = StatusAvailable
		return p
	case p.Record.WasmSHA256 != p.SHA256:
		p.Status = StatusModified
		p.Err = "the .wasm file changed since it was approved"
		return p
	case !subset(m.Permissions, p.Record.GrantedPermissions):
		p.Status = StatusModified
		p.Err = "the plugin now requests permissions that were not approved"
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

// Approve re-reads the plugin from disk and pins its current hash and
// requested permissions.
func (h *Host) Approve(ctx context.Context, id string, userID int64) error {
	if !idRe.MatchString(id) {
		return errors.New("invalid plugin id")
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
	if _, err := h.compile(ctx, sha, wasm); err != nil {
		return err
	}
	if err := h.store.ApprovePlugin(ctx, id, sha, m.Permissions, userID); err != nil {
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
func (h *Host) SaveSettings(ctx context.Context, id string, values map[string]string) error {
	p := h.Get(id)
	if p == nil || p.Record == nil {
		return errors.New("plugin must be enabled before it can be configured")
	}
	clean := map[string]string{}
	for _, s := range p.Settings {
		v, ok := values[s.Key]
		if !ok {
			if s.Type == "secret" && p.Record.Settings[s.Key] != "" {
				clean[s.Key] = p.Record.Settings[s.Key] // keep existing secret
			}
			continue
		}
		if len(v) > 4000 {
			return fmt.Errorf("%s: value too long", s.Label)
		}
		switch s.Type {
		case "bool":
			if v != "true" {
				v = "false"
			}
		case "select":
			if !slices.Contains(s.Choices, v) {
				return fmt.Errorf("%s: invalid choice", s.Label)
			}
		case "number":
			var f float64
			if _, err := fmt.Sscanf(v, "%g", &f); err != nil && v != "" {
				return fmt.Errorf("%s: must be a number", s.Label)
			}
		case "url":
			if v != "" && !strings.HasPrefix(v, "https://") {
				return fmt.Errorf("%s: must be an https:// URL", s.Label)
			}
		}
		if s.Required && v == "" {
			return fmt.Errorf("%s is required", s.Label)
		}
		clean[s.Key] = v
	}
	if err := h.store.SavePluginSettings(ctx, id, clean); err != nil {
		return err
	}
	return h.Load(ctx)
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
	HTML  *string   `json:"html,omitempty"`
	Meta  []MetaTag `json:"meta,omitempty"`
	Error string    `json:"error,omitempty"`
}

type stateKey struct{}

// callState is attached to the context of each call so that host functions
// know which plugin is calling and what it is allowed to do.
type callState struct {
	plugin    *Plugin
	allowHTTP bool
	pending   []byte
	httpCalls int
	kvOps     int
	dbQueries int
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
func (h *Host) call(ctx context.Context, p *Plugin, hook, event string, payload any, timeout time.Duration, allowHTTP bool) (*response, error) {
	if p.paused() {
		return nil, errors.New("plugin paused after repeated failures")
	}
	in, err := json.Marshal(envelope{
		ABI: ABIVersion, Hook: hook, Event: event,
		Plugin:  envelopePlugin{ID: p.ID, Version: p.Version, Settings: p.SettingValues()},
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
	ctx = context.WithValue(ctx, stateKey{}, &callState{plugin: p, allowHTTP: allowHTTP})

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
	resp, err := h.call(ctx, p, HookShortcode, "", shortcodePayload{Name: name, Attrs: attrs, Inner: inner, Post: post}, renderTimeout, false)
	if err != nil || resp.HTML == nil {
		return "", err
	}
	return *resp.HTML, nil
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
		resp, err := h.call(ctx, p, HookContentFilter, "", filterPayload{HTML: html, Post: post}, renderTimeout, false)
		if err != nil || resp.HTML == nil {
			continue
		}
		html = sanitize(*resp.HTML)
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
	resp, err := h.call(ctx, p, HookWidget, "", widgetPayload{Widget: widgetID, Config: config, Page: page}, renderTimeout, false)
	if err != nil || resp.HTML == nil {
		return "", err
	}
	return *resp.HTML, nil
}

// HeadMeta collects <meta> tags. Names are validated and http-equiv is never
// allowed, so plugins cannot set refresh or CSP directives.
func (h *Host) HeadMeta(ctx context.Context, page *PageInfo) []MetaTag {
	var out []MetaTag
	for _, p := range h.enabled() {
		if !p.HasHook(HookHeadMeta) {
			continue
		}
		resp, err := h.call(ctx, p, HookHeadMeta, "", page, renderTimeout, false)
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
			out = append(out, m)
		}
	}
	return out
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
			if _, err := h.call(context.Background(), p, HookEvent, job.name, job.data, eventTimeout, true); err != nil {
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
