// Package theme discovers themes, parses their templates and serves their
// static files. A theme is a directory with a theme.json manifest,
// templates/*.html and static/ assets (see docs/THEMES.md).
package theme

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
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

type Choice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type Manifest struct {
	Name        string         `json:"name"`
	Version     string         `json:"version"`
	Description string         `json:"description"`
	Author      string         `json:"author"`
	License     string         `json:"license"`
	Menus       []MenuLocation `json:"menus"`
	WidgetAreas []WidgetArea   `json:"widget_areas"`
	Options     []Option       `json:"options"`
	Swatches    []string       `json:"swatches"` // preview colours for the admin

	Tokens *Tokens `json:"tokens,omitempty"` // default colours, see tokens.go
}

type Theme struct {
	Manifest
	Slug    string
	Builtin bool
	fsys    fs.FS
	static  map[string]string             // static file -> short content hash
	pages   map[string]*template.Template // pristine sets, never executed

	langFuncs func(lang string) template.FuncMap
	mu        sync.Mutex
	byLang    map[string]map[string]*template.Template // per-language clones
	i18n      map[string]map[string]string             // theme's own strings: lang -> key -> text
}

// Pages every theme may provide. Missing ones fall back as described below.
var pageNames = []string{"home", "single", "page", "archive", "search", "404", "ask"}

var fallback = map[string]string{"page": "single", "archive": "home", "search": "home"}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// Source is a place themes are discovered from.
type Source struct {
	FS      fs.FS // directory containing one sub-directory per theme
	Builtin bool
}

type Manager struct {
	mu      sync.RWMutex
	themes  map[string]*Theme
	sources []Source
	shared  fs.FS // shared partials (shared/*.html)
	funcs   template.FuncMap
	// langFuncs returns the language-dependent functions (T, date, ...) bound
	// to one language; they replace the defaults in funcs per rendered language.
	langFuncs func(lang string) template.FuncMap
	log       *slog.Logger
	reload    bool // re-parse on every request (development)
}

func NewManager(shared fs.FS, funcs template.FuncMap, langFuncs func(string) template.FuncMap, log *slog.Logger, reload bool, sources ...Source) *Manager {
	return &Manager{shared: shared, funcs: funcs, langFuncs: langFuncs, log: log, reload: reload, sources: sources}
}

// Load (re)discovers all themes. Later sources override earlier ones.
func (m *Manager) Load() error {
	themes := map[string]*Theme{}
	for _, src := range m.sources {
		if src.FS == nil {
			continue
		}
		entries, err := fs.ReadDir(src.FS, ".")
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
			sub, err := fs.Sub(src.FS, e.Name())
			if err != nil {
				return err
			}
			t, err := m.loadTheme(e.Name(), sub, src.Builtin)
			if err != nil {
				m.log.Error("theme failed to load", "theme", e.Name(), "err", err)
				continue
			}
			themes[t.Slug] = t
		}
	}
	if len(themes) == 0 {
		return errors.New("no themes found")
	}
	m.mu.Lock()
	m.themes = themes
	m.mu.Unlock()
	return nil
}

func (m *Manager) loadTheme(slug string, fsys fs.FS, builtin bool) (*Theme, error) {
	raw, err := fs.ReadFile(fsys, "theme.json")
	if err != nil {
		return nil, fmt.Errorf("theme.json: %w", err)
	}
	t := &Theme{Slug: slug, Builtin: builtin, fsys: fsys, static: map[string]string{}, pages: map[string]*template.Template{},
		langFuncs: m.langFuncs, byLang: map[string]map[string]*template.Template{}}
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
	}
	if t.pages["home"] == nil || t.pages["single"] == nil {
		return errors.New("templates/home.html and templates/single.html are required")
	}
	if t.pages["404"] == nil {
		set, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := set.Parse(`{{define "content"}}{{template "shared/not-found" .}}{{end}}`); err != nil {
			return err
		}
		t.pages["404"] = set
	}
	// The site assistant's page without JavaScript, for themes that have
	// no templates/ask.html.
	if t.pages["ask"] == nil {
		set, err := base.Clone()
		if err != nil {
			return err
		}
		if _, err := set.Parse(`{{define "content"}}{{template "shared/ask" .}}{{end}}`); err != nil {
			return err
		}
		t.pages["ask"] = set
	}
	// Pages laid out with the block builder.
	set, err := base.Clone()
	if err != nil {
		return err
	}
	if _, err := set.Parse(`{{define "content"}}{{template "shared/blocks-main" .}}{{end}}`); err != nil {
		return err
	}
	t.pages["blocks"] = set
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

// Render executes a page template ("home", "single", ...) in a language into
// w. Output is buffered so that a template error never produces a
// half-written page.
func (t *Theme) Render(w io.Writer, page, lang string, data any) error {
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

// forLang returns the page's template set with the language functions bound
// to lang. Clones are made from the pristine set (html/template cannot clone
// a set after it has been executed) and kept for later requests.
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
