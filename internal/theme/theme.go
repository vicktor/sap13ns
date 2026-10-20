// Package theme discovers themes, parses their templates and serves their
// static files. A theme is a directory with a theme.json manifest,
// templates/*.html and static/ assets (see docs/THEMES.md). Themes come
// from the binary, from the network's themes directory and from the site's
// own directory, where uploads are installed after a strict validation
// (upload.go, validate.go, check.go).
package theme

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type MenuLocation struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type WidgetArea struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Option struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "text" | "textarea" | "select" | "bool"
	Default string   `json:"default"`
	Choices []Choice `json:"choices"`
	Help    string   `json:"help"`
}

// ContentGroup is a part of the theme's pages (the front page's intro, the
// footer) and its editable texts and images.
type ContentGroup struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Fields []ContentField `json:"fields"`
}

// ContentField is one editable text or image. Templates read it with
// .Site.Text, .Site.Lines and .Site.Image, and mark the element that holds
// only it with data-sap-edit="key", so that clicking it in the customiser's
// preview opens it.
type ContentField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Type  string `json:"type"` // "text" | "textarea" | "lines" | "image"
	// Default is the text shown until it is edited, in English: the
	// theme's catalog translates it (a "lines" default line by line). For
	// an image it is the file in static/ the theme shows instead, for the
	// customiser's thumbnail.
	Default string `json:"default"`
	Help    string `json:"help"`
}

// Content field types.
const (
	ContentText     = "text"
	ContentTextarea = "textarea"
	ContentLines    = "lines"
	ContentImage    = "image"
)

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type Manifest struct {
	// Slug is optional: a theme is named after its folder. An uploaded
	// archive with its files at the root names the theme here.
	Slug        string         `json:"slug,omitempty"`
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Author      string         `json:"author"`
	License     string         `json:"license"`
	Menus       []MenuLocation `json:"menus"`
	WidgetAreas []WidgetArea   `json:"widget_areas"`
	Options     []Option       `json:"options"`
	Swatches    []string       `json:"swatches"` // preview colours for the admin
	// Content lists the texts and images of the theme's own templates that
	// the customiser edits (Appearance → Customise → Texts and images).
	Content []ContentGroup `json:"content,omitempty"`
	// Motion: the templates animate elements with the motion library
	// (data-sap-animate, data-sap-stagger), so every page loads it.
	Motion bool `json:"motion,omitempty"`

	Tokens *Tokens `json:"tokens,omitempty"` // default colours, see tokens.go
}

type Theme struct {
	Manifest
	Slug    string
	Builtin bool
	// Own: the theme is in the site's own directory, so an administrator
	// can replace or delete it. Uploaded: it was installed by an upload
	// (Install), passed the validation, and renders under the bounds of
	// RenderContext and bounds.go.
	Own      bool
	Uploaded bool
	Upload   *UploadInfo // what the upload recorded, for Uploaded themes

	fsys   fs.FS
	static map[string]string             // static file -> short content hash
	pages  map[string]*template.Template // pristine sets, never executed

	funcs     template.FuncMap // the server's functions, for uploaded themes' slots
	langFuncs func(lang string) template.FuncMap
	mu        sync.Mutex
	byLang    map[string]map[string]*template.Template // per-language clones
	pools     map[string]*pool                         // uploaded themes: page and language -> slots
	i18n      map[string]map[string]string             // theme's own strings: lang -> key -> text
	own       map[string]bool                          // the pages the theme ships itself
}

// Pages every theme may provide. Missing ones fall back as described below.
// "front" is the theme's own front page (a landing, a one-page site), shown
// at / when the reading settings ask for it.
var pageNames = []string{"front", "home", "single", "page", "archive", "search", "404", "ask"}

// fallback: a page the theme lacks renders with another of its pages, when
// the theme has that one (a blog theme without page.html shows pages with
// single.html). Without it, the server's shared page is used. The front
// page is only rendered when the theme has one (internal/web): a theme
// without front.html shows the latest posts at /, so its fallback to home
// is a last resort.
var fallback = map[string]string{"page": "single", "archive": "home", "search": "home", "front": "home"}

// serverPages are the pages the server adds to a theme that lacks them:
// plain fallbacks from the shared templates (ui/shared/templates/fallback.html)
// that render inside the theme's layout, so a theme can be a landing page or
// a one-page site without any blog template, and the page builder's page.
var serverPages = map[string]string{
	"home":    `{{define "content"}}{{template "shared/fallback-home" .}}{{end}}`,
	"single":  `{{define "content"}}{{template "shared/fallback-single" .}}{{end}}`,
	"page":    `{{define "content"}}{{template "shared/fallback-single" .}}{{end}}`,
	"archive": `{{define "content"}}{{template "shared/fallback-list" .}}{{end}}`,
	"search":  `{{define "content"}}{{template "shared/fallback-list" .}}{{end}}`,
	"404":     `{{define "content"}}{{template "shared/not-found" .}}{{end}}`,
	"ask":     `{{define "content"}}{{template "shared/ask" .}}{{end}}`,
	"blocks":  `{{define "content"}}{{template "shared/blocks-main" .}}{{end}}`,
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// ValidSlug reports whether s can name a theme.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

// Source is a place themes are discovered from.
type Source struct {
	FS      fs.FS // directory containing one sub-directory per theme
	Builtin bool
	// Dir is the site's own themes directory on disk, where uploads are
	// installed; FS is then opened from it on every Load. "" for the
	// built-in themes and the network's.
	Dir string
}

// A Failure is a theme of the site's own directory that did not load.
type Failure struct {
	Slug string
	Err  string
}

type Manager struct {
	mu      sync.RWMutex
	themes  map[string]*Theme
	failed  []Failure
	sources []Source
	shared  fs.FS // shared partials (shared/*.html)
	funcs   template.FuncMap
	// langFuncs returns the language-dependent functions (T, date, ...) bound
	// to one language; they replace the defaults in funcs per rendered language.
	langFuncs func(lang string) template.FuncMap
	log       *slog.Logger
	reload    bool // re-parse on every request (development)

	// pageType is the Go type of the data every page template receives,
	// for the checks of uploaded templates (check.go).
	pageType  reflect.Type
	installMu sync.Mutex // one Install or Remove at a time
}

func NewManager(shared fs.FS, funcs template.FuncMap, langFuncs func(string) template.FuncMap, log *slog.Logger, reload bool, sources ...Source) *Manager {
	return &Manager{shared: shared, funcs: funcs, langFuncs: langFuncs, log: log, reload: reload, sources: sources}
}

// SetPageType names the type of the data page templates receive (a
// pointer to the web package's PageData). Uploads are checked against it.
func (m *Manager) SetPageType(t reflect.Type) { m.pageType = t }

// sourceFS is the file system of a source now: the site's own directory
// may appear after the server started.
func sourceFS(src Source) fs.FS {
	if src.Dir == "" {
		return src.FS
	}
	if fi, err := os.Stat(src.Dir); err == nil && fi.IsDir() {
		return os.DirFS(src.Dir)
	}
	return nil
}

// Load (re)discovers all themes. Later sources override earlier ones,
// except that an uploaded theme never shadows a built-in or network theme.
func (m *Manager) Load() error {
	themes := map[string]*Theme{}
	var failed []Failure
	for _, src := range m.sources {
		fsys := sourceFS(src)
		if fsys == nil {
			continue
		}
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		for _, e := range entries {
			if !e.IsDir() || !slugRe.MatchString(e.Name()) {
				continue
			}
			sub, err := fs.Sub(fsys, e.Name())
			if err != nil {
				return err
			}
			info := readUploadInfo(sub)
			if prev, taken := themes[e.Name()]; taken && info != nil {
				m.log.Error("uploaded theme not loaded: a built-in or network theme has its name", "theme", e.Name(), "builtin", prev.Builtin)
				failed = append(failed, Failure{Slug: e.Name(), Err: "a built-in or network theme has this name"})
				continue
			}
			t, err := m.loadTheme(e.Name(), sub, src.Builtin, src.Dir != "" && info != nil)
			if err != nil {
				m.log.Error("theme failed to load", "theme", e.Name(), "err", err)
				if src.Dir != "" {
					failed = append(failed, Failure{Slug: e.Name(), Err: err.Error()})
				}
				continue
			}
			t.Own, t.Upload = src.Dir != "", info
			themes[t.Slug] = t
		}
	}
	if len(themes) == 0 {
		return errors.New("no themes found")
	}
	m.mu.Lock()
	m.themes, m.failed = themes, failed
	m.mu.Unlock()
	return nil
}

// Failures lists the themes of the site's own directory that did not load
// at the last Load.
func (m *Manager) Failures() []Failure {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Failure(nil), m.failed...)
}

// loadTheme parses a theme. The template sets of an uploaded theme have
// their functions bound to closedGuard, which fails every call: they run
// only through the slots of RenderContext and of the checks' probe, whose
// functions are bounded (bounds.go).
func (m *Manager) loadTheme(slug string, fsys fs.FS, builtin, uploaded bool) (*Theme, error) {
	raw, err := fs.ReadFile(fsys, "theme.json")
	if err != nil {
		return nil, fmt.Errorf("theme.json: %w", err)
	}
	t := &Theme{Slug: slug, Builtin: builtin, Uploaded: uploaded, fsys: fsys, static: map[string]string{}, pages: map[string]*template.Template{},
		funcs: m.funcs, langFuncs: m.langFuncs, byLang: map[string]map[string]*template.Template{}}
	if err := json.Unmarshal(raw, &t.Manifest); err != nil {
		return nil, fmt.Errorf("theme.json: %w", err)
	}
	if t.Name == "" {
		t.Name = slug
	}
	if err := m.parse(t); err != nil {
		return nil, err
	}
	// Optional translations of the theme's own strings, i18n/<lang>.json.
	if files, _ := fs.Glob(fsys, "i18n/*.json"); len(files) > 0 {
		t.i18n = map[string]map[string]string{}
		for _, f := range files {
			raw, err := fs.ReadFile(fsys, f)
			if err != nil {
				continue
			}
			cat := map[string]string{}
			if err := json.Unmarshal(raw, &cat); err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			t.i18n[strings.TrimSuffix(path.Base(f), ".json")] = cat
		}
	}
	if uploaded {
		closed := t.funcsFor("en", closedGuard)
		for _, set := range t.pages {
			set.Funcs(closed)
		}
	}
	_ = fs.WalkDir(fsys, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		t.static[strings.TrimPrefix(p, "static/")] = hex.EncodeToString(sum[:6])
		return nil
	})
	return t, nil
}

func (m *Manager) parse(t *Theme) error {
	base := template.New("theme").Funcs(m.funcs)
	// Shared partials first so that a theme can redefine any of them.
	if _, err := base.ParseFS(m.shared, "*.html"); err != nil {
		return fmt.Errorf("shared templates: %w", err)
	}
	// The block builder's blocks (internal/builder), which themes may
	// redefine too.
	if matches, _ := fs.Glob(m.shared, "blocks/*.html"); len(matches) > 0 {
		if _, err := base.ParseFS(m.shared, "blocks/*.html"); err != nil {
			return fmt.Errorf("shared block templates: %w", err)
		}
	}
	if _, err := fs.Stat(t.fsys, "templates/layout.html"); err != nil {
		return errors.New("templates/layout.html is required")
	}
	if _, err := base.ParseFS(t.fsys, "templates/layout.html"); err != nil {
		return err
	}
	if matches, _ := fs.Glob(t.fsys, "templates/partials/*.html"); len(matches) > 0 {
		if _, err := base.ParseFS(t.fsys, "templates/partials/*.html"); err != nil {
			return err
		}
	}
	t.own = map[string]bool{}
	for _, name := range pageNames {
		file := "templates/" + name + ".html"
		if _, err := fs.Stat(t.fsys, file); err != nil {
			continue
		}
		set, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := set.ParseFS(t.fsys, file); err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		t.pages[name] = set
		t.own[name] = true
	}
	if !t.own["front"] && !t.own["home"] {
		return errors.New("templates/front.html or templates/home.html is required")
	}
	// The shared fallbacks of the pages the theme lacks (unless another of
	// its pages stands in, see fallback), the not-found and assistant pages
	// of a theme without its own, and the pages laid out with the block
	// builder. The front page has none of its own (see fallback).
	for _, name := range []string{"home", "single", "page", "archive", "search", "404", "ask", "blocks"} {
		if t.pages[name] != nil || t.own[fallback[name]] {
			continue
		}
		set, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := set.Parse(serverPages[name]); err != nil {
			return err
		}
		t.pages[name] = set
	}
	return nil
}

func (m *Manager) Get(slug string) (*Theme, bool) {
	if m.reload {
		if err := m.Load(); err != nil {
			m.log.Error("theme reload", "err", err)
		}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.themes[slug]
	return t, ok
}

// Active returns the requested theme or a deterministic fallback.
func (m *Manager) Active(slug string) *Theme {
	if t, ok := m.Get(slug); ok {
		return t
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, fb := range []string{"apple", "dashboard"} {
		if t, ok := m.themes[fb]; ok {
			return t
		}
	}
	for _, t := range m.themes {
		return t
	}
	return nil
}

func (m *Manager) List() []*Theme {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Theme, 0, len(m.themes))
	for _, t := range m.themes {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Bounds of rendering an uploaded theme. Templates cannot be interrupted,
// so the checks of check.go keep every loop that writes nothing short,
// and the writer and the functions (bounds.go) stop the rest: past
// MaxRenderBytes or RenderTimeout, the page fails.
const (
	MaxRenderBytes = 8 << 20
	RenderTimeout  = 5 * time.Second
)

var (
	// ErrRenderLimit: an uploaded theme wrote too much or took too long.
	ErrRenderLimit = errors.New("the theme went over its rendering limits")
	// ErrNoCredit: an uploaded theme rendered a page without the footer
	// credit (shared/footer-meta), which the licence requires.
	ErrNoCredit = errors.New("the theme rendered a page without the sap13ns credit")
)

// CreditMarker is the link that shared/footer-meta renders: every page of
// an uploaded theme must contain it.
const CreditMarker = `<a href="https://sap13ns.site">sap13ns</a>`

// Render executes a page template ("home", "single", ...) in a language into
// w. Output is buffered so that a template error never produces a
// half-written page.
func (t *Theme) Render(w io.Writer, page, lang string, data any) error {
	return t.RenderContext(context.Background(), w, page, lang, data)
}

// RenderContext is Render for a request: an uploaded theme stops when ctx
// ends, and within the bounds above.
func (t *Theme) RenderContext(ctx context.Context, w io.Writer, page, lang string, data any) error {
	if _, ok := t.pages[page]; !ok {
		for {
			fb, has := fallback[page]
			if !has {
				return fmt.Errorf("theme %s: no template for %q", t.Slug, page)
			}
			page = fb
			if _, ok := t.pages[page]; ok {
				break
			}
		}
	}
	if !t.Uploaded {
		set, err := t.forLang(page, lang)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := set.ExecuteTemplate(&buf, "layout", data); err != nil {
			return err
		}
		_, err = w.Write(buf.Bytes())
		return err
	}
	deadline := time.Now().Add(RenderTimeout)
	s, err := t.acquire(ctx, page, lang, deadline)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRenderLimit, err)
	}
	defer t.release(s)
	s.g.start(ctx, deadline)
	lw := &limitWriter{g: s.g, max: MaxRenderBytes}
	if err := s.set.ExecuteTemplate(lw, "layout", data); err != nil {
		if s.g.err != nil {
			return fmt.Errorf("%w: %w", ErrRenderLimit, s.g.err)
		}
		return err
	}
	if !bytes.Contains(lw.buf.Bytes(), []byte(CreditMarker)) {
		return ErrNoCredit
	}
	_, err = w.Write(lw.buf.Bytes())
	return err
}

// limitWriter buffers a page and fails past its size, or once its guard
// fails: past the time limit, when the request ends or when a function hit
// a limit. The template stops at the first failed write.
type limitWriter struct {
	g   *guard
	buf bytes.Buffer
	max int
}

func (w *limitWriter) Write(p []byte) (int, error) {
	// Every write, even an empty one, checks the guard: the checks of
	// check.go keep the work between two writes small, and every function
	// checks it too.
	if err := w.g.check(); err != nil {
		return 0, err
	}
	if w.buf.Len()+len(p) > w.max {
		return 0, w.g.fail(fmt.Errorf("more than %d MB of output", w.max>>20))
	}
	return w.buf.Write(p)
}

// A slot is a copy of one page's template set, in one language, whose
// functions are bound to the slot's guard; it serves one execution at a
// time. A pool keeps up to maxSlots of them.
type slot struct {
	set  *template.Template
	g    *guard
	pool *pool
}

type pool struct {
	free chan *slot
	made int // under Theme.mu
}

// newSlot copies the pristine set of page and binds its functions to a
// new guard, closed until an execution starts it.
func (t *Theme) newSlot(page, lang string) (*slot, error) {
	set, err := t.pages[page].Clone()
	if err != nil {
		return nil, err
	}
	g := &guard{err: errClosed}
	set.Funcs(t.funcsFor(lang, g))
	return &slot{set: set, g: g}, nil
}

// acquire takes a free slot of page in lang, makes one while the pool has
// fewer than maxSlots, or waits for one until the deadline.
func (t *Theme) acquire(ctx context.Context, page, lang string, deadline time.Time) (*slot, error) {
	key := page + "\x00" + lang
	t.mu.Lock()
	if t.pools == nil {
		t.pools = map[string]*pool{}
	}
	p := t.pools[key]
	if p == nil {
		p = &pool{free: make(chan *slot, maxSlots)}
		t.pools[key] = p
	}
	select {
	case s := <-p.free:
		t.mu.Unlock()
		return s, nil
	default:
	}
	if p.made < maxSlots {
		p.made++
		t.mu.Unlock()
		s, err := t.newSlot(page, lang)
		if err != nil {
			t.mu.Lock()
			p.made--
			t.mu.Unlock()
			return nil, err
		}
		s.pool = p
		return s, nil
	}
	t.mu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case s := <-p.free:
		return s, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errBusy
	}
}

func (t *Theme) release(s *slot) {
	s.g.stop()
	s.pool.free <- s // never blocks: a pool holds at most maxSlots
}

// forLang returns the page's template set with the language functions bound
// to lang, for a theme that is not an upload. Clones are made from the
// pristine set (html/template cannot clone a set after it has been
// executed) and kept for later requests.
func (t *Theme) forLang(page, lang string) (*template.Template, error) {
	if t.langFuncs == nil {
		return t.pages[page], nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if set, ok := t.byLang[lang][page]; ok {
		return set, nil
	}
	set, err := t.pages[page].Clone()
	if err != nil {
		return nil, err
	}
	funcs := t.langFuncs(lang)
	if cat := t.i18n[lang]; len(cat) > 0 {
		if core, ok := funcs["T"].(func(string, ...any) string); ok {
			funcs["T"] = func(key string, args ...any) string {
				v, ok := cat[key]
				if !ok || v == "" {
					return core(key, args...)
				}
				if len(args) > 0 {
					return fmt.Sprintf(v, args...)
				}
				return v
			}
		}
	}
	set.Funcs(funcs)
	if t.byLang[lang] == nil {
		t.byLang[lang] = map[string]*template.Template{}
	}
	t.byLang[lang][page] = set
	return set, nil
}

// Has reports whether the theme ships its own template for page ("front",
// "home", "single"…), rather than the server's shared fallback.
func (t *Theme) Has(page string) bool { return t.own[page] }

// Text returns the theme's own translation of key into lang, from its
// i18n/<lang>.json (themes uploaded at runtime ship their own catalogs).
func (t *Theme) Text(lang, key string) (string, bool) {
	v, ok := t.i18n[lang][key]
	return v, ok && v != ""
}

// Translate returns the theme's translation of s into lang, or s itself.
// It is nil-safe. Texts that reach a theme as data (the hosting plans) are
// translated by its catalog only, as its templates do.
func (t *Theme) Translate(lang, s string) string {
	if t != nil {
		if v, ok := t.i18n[lang][s]; ok && v != "" {
			return v
		}
	}
	return s
}

// StaticURL returns a cache-busted URL for a theme static file.
func (t *Theme) StaticURL(file string) string {
	h, ok := t.static[file]
	if !ok {
		return "/themes/" + t.Slug + "/static/" + file
	}
	return "/themes/" + t.Slug + "/static/" + file + "?v=" + h
}

// Static opens a static file of the theme.
func (t *Theme) Static(file string) (fs.File, string, error) {
	file = path.Clean("/" + file)[1:]
	if _, ok := t.static[file]; !ok {
		return nil, "", fs.ErrNotExist
	}
	f, err := t.fsys.Open("static/" + file)
	return f, t.static[file], err
}

// OptionValues merges stored option values over manifest defaults.
func (t *Theme) OptionValues(stored map[string]string) map[string]string {
	out := map[string]string{}
	for _, o := range t.Options {
		v, ok := stored[o.Key]
		if !ok {
			v = o.Default
		}
		if o.Type == "select" {
			valid := false
			for _, c := range o.Choices {
				if c.Value == v {
					valid = true
				}
			}
			if !valid {
				v = o.Default
			}
		}
		out[o.Key] = v
	}
	return out
}

// ContentField returns the content field key, or nil.
func (t *Theme) ContentField(key string) *ContentField {
	for i := range t.Content {
		for j := range t.Content[i].Fields {
			if f := &t.Content[i].Fields[j]; f.Key == key {
				return f
			}
		}
	}
	return nil
}

func (t *Theme) HasMenu(id string) bool {
	for _, ml := range t.Menus {
		if ml.ID == id {
			return true
		}
	}
	return false
}

func (t *Theme) HasWidgetArea(id string) bool {
	for _, a := range t.WidgetAreas {
		if a.ID == id {
			return true
		}
	}
	return false
}
