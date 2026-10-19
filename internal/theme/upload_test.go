package theme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// The checks of uploads run with the web package's functions and page
// data (internal/web/themes_upload_test.go); these tests cover what the
// theme package does on its own: the bounds of rendering, the credit and
// the rules for removing a theme.

var sharedFS = fstest.MapFS{
	"widgets.html": {Data: []byte(`{{define "shared/footer-meta"}}<p class="footer-meta">Powered by ` + CreditMarker + `</p>{{end}}`)},
}

func testManager(t *testing.T, own string) *Manager {
	t.Helper()
	tr := func(k string, args ...any) string {
		if len(args) > 0 {
			return fmt.Sprintf(k, args...)
		}
		return k
	}
	funcs := template.FuncMap{"join": strings.Join, "paragraphs": func(s string) template.HTML { return template.HTML(s) },
		"upper": strings.ToUpper, "list": func(v ...any) []any { return v },
		"dict": func(kv ...any) (map[string]any, error) {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[kv[i].(string)] = kv[i+1]
			}
			return m, nil
		},
		"T": tr, "plural": func(n int, one, many string, args ...any) string { return one }}
	lang := func(string) template.FuncMap {
		return template.FuncMap{"T": tr, "plural": func(n int, one, many string, args ...any) string { return one }}
	}
	builtin := fstest.MapFS{
		"base/theme.json":             {Data: []byte(`{"name": "Base"}`)},
		"base/templates/layout.html":  {Data: []byte(`{{define "layout"}}{{template "content" .}}{{template "shared/footer-meta" .}}{{end}}`)},
		"base/templates/home.html":    {Data: []byte(`{{define "content"}}home{{end}}`)},
		"base/templates/single.html":  {Data: []byte(`{{define "content"}}single{{end}}`)},
		"base/static/theme.css":       {Data: []byte(`body{}`)},
		"apple/theme.json":            {Data: []byte(`{"name": "Apple"}`)},
		"apple/templates/layout.html": {Data: []byte(`{{define "layout"}}{{template "content" .}}{{end}}`)},
		"apple/templates/home.html":   {Data: []byte(`{{define "content"}}apple{{end}}`)},
		"apple/templates/single.html": {Data: []byte(`{{define "content"}}apple{{end}}`)},
	}
	return NewManager(sharedFS, funcs, lang, slog.New(slog.NewTextHandler(io.Discard, nil)), false,
		Source{FS: builtin, Builtin: true}, Source{Dir: own})
}

// writeTheme writes an uploaded theme into dir by hand, as Install leaves
// it, with layout as its layout template.
func writeTheme(t *testing.T, dir, slug, layout, home string) {
	t.Helper()
	files := map[string]string{
		"theme.json":            `{"name": "` + slug + `"}`,
		"templates/layout.html": layout,
		"templates/home.html":   `{{define "content"}}` + home + `{{end}}`,
		"templates/single.html": `{{define "content"}}single{{end}}`,
		"static/theme.css":      `body{}`,
		uploadFile:              `{"sha256": "x", "source": "upload"}`,
	}
	for name, content := range files {
		p := filepath.Join(dir, slug, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderBounds(t *testing.T) {
	dir := t.TempDir()
	credit := `{{define "layout"}}{{template "content" .}}{{template "shared/footer-meta" .}}{{end}}`
	writeTheme(t, dir, "fine", credit, "hello")
	writeTheme(t, dir, "nocredit", `{{define "layout"}}{{template "content" .}}{{end}}`, "hello")
	big := `{{define "big"}}` + strings.Repeat("x", 1<<20) + `{{end}}`
	writeTheme(t, dir, "huge", credit+big, strings.Repeat(`{{template "big"}}`, 9))
	writeTheme(t, dir, "grow", credit, `{{$a := printf "%s%s" "abcdefgh" "abcdefgh"}}{{$a = printf "%s%s" $a $a}}{{$a = printf "%s%s" $a $a}}`+
		strings.Repeat(`{{$a = printf "%s%s" $a $a}}`, 20)+`{{len $a}}`)
	m := testManager(t, dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	render := func(ctx context.Context, slug string) (string, error) {
		th, ok := m.Get(slug)
		if !ok {
			t.Fatalf("%s not loaded: %v", slug, m.Failures())
		}
		if !th.Uploaded || !th.Own {
			t.Fatalf("%s: not an upload", slug)
		}
		var buf bytes.Buffer
		err := th.RenderContext(ctx, &buf, "home", "en", nil)
		return buf.String(), err
	}
	ctx := context.Background()
	if out, err := render(ctx, "fine"); err != nil || !strings.Contains(out, CreditMarker) {
		t.Fatalf("fine: %q %v", out, err)
	}
	if out, err := render(ctx, "nocredit"); !errors.Is(err, ErrNoCredit) || out != "" {
		t.Fatalf("no credit: %q %v", out, err)
	}
	if out, err := render(ctx, "huge"); !errors.Is(err, ErrRenderLimit) || out != "" {
		t.Fatalf("over the output cap: %d bytes, %v", len(out), err)
	}
	if _, err := render(ctx, "grow"); !errors.Is(err, ErrRenderLimit) || !errors.Is(err, errTooLong) {
		t.Fatalf("doubling text: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := render(cancelled, "fine"); !errors.Is(err, ErrRenderLimit) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// A built-in theme keeps the unbounded path: no credit check.
	th, _ := m.Get("apple")
	var buf bytes.Buffer
	if err := th.Render(&buf, "home", "en", nil); err != nil || buf.String() != "apple" {
		t.Fatalf("built in: %q %v", buf.String(), err)
	}
}

// running returns a guard that an execution started, with a minute left.
func running() *guard {
	g := &guard{}
	g.start(context.Background(), time.Now().Add(time.Minute))
	return g
}

func TestLimitWriterDeadline(t *testing.T) {
	g := &guard{}
	g.start(context.Background(), time.Now().Add(-time.Second))
	w := &limitWriter{g: g, max: 10}
	if _, err := w.Write(nil); !errors.Is(err, errTimeout) || !strings.Contains(err.Error(), "longer") {
		t.Fatalf("past the deadline: %v", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("the error is not sticky")
	}
	g = running()
	w = &limitWriter{g: g, max: 4}
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("5")); err == nil {
		t.Fatal("over the cap")
	}
	// The functions stop with the writer.
	printf := textFuncs(g, nil)["printf"].(func(string, ...any) (string, error))
	if _, err := printf("x"); err == nil {
		t.Fatal("a function ran after the output cap")
	}
}

// Every function an uploaded theme calls checks its guard: the deadline,
// the request and the text made, even when the template writes nothing.
func TestGuardedFuncs(t *testing.T) {
	upper := func(g *guard) func(string) (string, error) {
		return guardFunc(strings.ToUpper, g).(func(string) (string, error))
	}
	if s, err := upper(running())("abc"); err != nil || s != "ABC" {
		t.Fatalf("%q %v", s, err)
	}
	late := &guard{}
	late.start(context.Background(), time.Now().Add(-time.Millisecond))
	if _, err := upper(late)("abc"); !errors.Is(err, errTimeout) {
		t.Fatalf("past the deadline: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	gone := &guard{}
	gone.start(ctx, time.Now().Add(time.Minute))
	if _, err := upper(gone)("abc"); !errors.Is(err, context.Canceled) {
		t.Fatalf("request gone: %v", err)
	}
	if _, err := upper(closedGuard)("abc"); !errors.Is(err, errClosed) {
		t.Fatalf("closed: %v", err)
	}
	// 17 texts of 1 MB: more than maxMade for one page.
	g := running()
	big := strings.Repeat("x", maxFormatted)
	var err error
	for range maxMade/maxFormatted + 1 {
		if _, err = upper(g)(big); err != nil {
			break
		}
	}
	if !errors.Is(err, errTooMuch) {
		t.Fatalf("text made: %v", err)
	}
	// Variadic functions keep their arguments.
	join := guardFunc(func(sep string, s ...string) string { return strings.Join(s, sep) }, running()).(func(string, ...string) (string, error))
	if s, err := join("-", "a", "b"); err != nil || s != "a-b" {
		t.Fatalf("%q %v", s, err)
	}
}

func TestBoundedFuncs(t *testing.T) {
	funcs := func() template.FuncMap { return textFuncs(running(), template.FuncMap{"join": strings.Join}) }
	printf := func(format string, args ...any) (string, error) {
		return funcs()["printf"].(func(string, ...any) (string, error))(format, args...)
	}
	if s, err := printf("%s-%d %v", "a", 3, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); err != nil || s != "a-3 2026-01-02 00:00:00 +0000 UTC" {
		t.Fatalf("%q %v", s, err)
	}
	if s, err := printf("%[2]s %[1]s", "a", "b"); err != nil || s != "b a" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := printf("%s", strings.Repeat("x", maxFormatted+1)); !errors.Is(err, errTooLong) {
		t.Fatalf("long: %v", err)
	}
	// An argument repeated by explicit indexes: 20 times 100 kB.
	if _, err := printf(strings.Repeat("%[1]s", 20), strings.Repeat("x", 100<<10)); !errors.Is(err, errTooLong) {
		t.Fatalf("repeated argument: %v", err)
	}
	// A width or a precision over 99, or taken from an argument.
	for _, f := range []string{"%999999s", "%100d", "%.100s", "%*s", "%.*s", "%[1]*s", "%1000000000000x"} {
		if _, err := printf(f, 5, "x"); !errors.Is(err, errWidth) {
			t.Errorf("%s: %v", f, err)
		}
	}
	if s, err := printf("%5s|%-3d|%.2s", "ab", 7, "xyz"); err != nil || s != "   ab|7  |xy" {
		t.Fatalf("%q %v", s, err)
	}
	// Values fmt would write field by field, or with their own methods.
	for _, v := range []any{[]string{"a"}, map[string]int{}, struct{ A string }{"a"}, &struct{ A string }{"a"}, [2]int{}, []byte("x")} {
		if _, err := printf("%v", v); !errors.Is(err, errValue) {
			t.Errorf("%T: %v", v, err)
		}
	}
	join := funcs()["join"].(func([]string, string) (string, error))
	if s, err := join([]string{"a", "b"}, ", "); err != nil || s != "a, b" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := join(make([]string, 10), strings.Repeat("x", maxFormatted/5)); !errors.Is(err, errTooLong) {
		t.Fatalf("long join: %v", err)
	}
	for _, name := range []string{"print", "println", "html", "js", "urlquery"} {
		fn := func(args ...any) (string, error) { return funcs()[name].(func(...any) (string, error))(args...) }
		if s, err := fn("<a>"); err != nil || s == "" {
			t.Errorf("%s: %q %v", name, s, err)
		}
		if _, err := fn(strings.Repeat("x", maxFormatted)); !errors.Is(err, errTooLong) {
			t.Errorf("%s long: %v", name, err)
		}
		if _, err := fn([]int{1}); !errors.Is(err, errValue) {
			t.Errorf("%s of a list: %v", name, err)
		}
	}
	// print, html and urlquery write their arguments as text/template's do.
	n, s := 5, "x"
	for name, want := range map[string]string{"print": "1 2x<b>", "html": "1 2x&lt;b&gt;", "urlquery": "1+2x%3Cb%3E"} {
		if got, err := funcs()[name].(func(...any) (string, error))(1, 2, "x", "<b>"); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, err, want)
		}
	}
	if got, err := funcs()["html"].(func(...any) (string, error))(&n, &s, nil); err != nil || got != "5x&lt;no value&gt;" {
		t.Errorf("html of pointers: %q %v", got, err)
	}
}

// formatBound is an upper bound of fmt's text, whatever the format: random
// formats over fmt's syntax, with every kind of value a template formats.
func TestFormatBound(t *testing.T) {
	n := 7
	values := []any{nil, "", "abc", "\xff\x00\n\"日本", strings.Repeat("é", 40), 0, -12345, uint64(1<<64 - 1), int8(-128),
		3.14, -1e308, float32(1e-30), complex(1e308, -1e-300), true, false, (*int)(nil), &n, uintptr(1 << 40),
		template.HTML("<b>"), time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), errors.New("boom")}
	pieces := []string{"%", "%", "%", "#", "0", "+", "-", " ", "1", "2", "9", "[", "]", "[1]", "[2]", "[9]", ".", "*",
		"s", "d", "v", "q", "x", "X", "T", "p", "w", "c", "U", "e", "f", "g", "t", "b", "o", "é", "a", " text "}
	rng := rand.New(rand.NewPCG(1, 2))
	for range 50000 {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
		}
		format := b.String()
		args := make([]any, rng.IntN(4))
		for i := range args {
			args[i] = values[rng.IntN(len(values))]
		}
		xs, err := textValues(args)
		if err != nil {
			t.Fatal(err)
		}
		bound, err := formatBound(format, xs)
		if err != nil {
			if !errors.Is(err, errWidth) {
				t.Fatalf("%q: %v", format, err)
			}
			continue
		}
		if got := len(fmt.Sprintf(format, xs...)); got > bound {
			t.Fatalf("formatBound(%q, %#v) = %d < %d", format, xs, bound, got)
		}
		if got := len(fmt.Sprint(xs...)); got > printBound(xs) {
			t.Fatalf("printBound(%#v) = %d < %d", xs, printBound(xs), got)
		}
		if got := len(fmt.Sprintln(xs...)); got > printBound(xs) {
			t.Fatalf("printBound(%#v) = %d < %d for Sprintln", xs, printBound(xs), got)
		}
	}
}

// A list or a dict that holds itself prints in constant space.
func TestListsPrintFixed(t *testing.T) {
	f := textFuncs(running(), template.FuncMap{"list": func(v ...any) []any { return v },
		"dict": func(kv ...any) (map[string]any, error) { return map[string]any{"a": kv[1]}, nil }})
	list := f["list"].(func(...any) (themeList, error))
	dict := f["dict"].(func(...any) (themeMap, error))
	l, _ := list(1)
	for range 64 {
		l, _ = list(l, l)
	}
	d, _ := dict("a", l)
	for range 64 {
		d, _ = dict("a", d, "b", d)
	}
	if s := fmt.Sprint(l, d); s != "[...] map[...]" {
		t.Fatalf("%q", s)
	}
	var buf bytes.Buffer
	tmpl := template.Must(template.New("x").Parse(`<a title="{{.}}">{{.}}</a>`))
	if err := tmpl.Execute(&buf, l); err != nil || buf.String() != `<a title="[...]">[...]</a>` {
		t.Fatalf("%q %v", buf.String(), err)
	}
	if s, err := f["printf"].(func(string, ...any) (string, error))("%v %s", d, l); err != nil || s != "map[...] [...]" {
		t.Fatalf("%q %v", s, err)
	}
}

// T formats the translation it uses, which may repeat its arguments where
// the key does not: the bound is the translation's.
func TestTranslatedFormat(t *testing.T) {
	dir := t.TempDir()
	layout := `{{define "layout"}}{{T "Hi %s" .Name}}|{{plural 2 "%d post" "%d posts"}}{{template "shared/footer-meta" .}}{{end}}`
	writeTheme(t, dir, "tr", layout, "")
	catalog := `{"Hi %s": "` + strings.Repeat("%[1]s", 20) + `", "%d posts": "%[1]d entradas, %[1]d"}`
	if err := os.MkdirAll(filepath.Join(dir, "tr", "i18n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tr", "i18n", "es.json"), []byte(catalog), 0o640); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	th, _ := m.Get("tr")
	var buf bytes.Buffer
	if err := th.Render(&buf, "home", "es", map[string]any{"Name": "ab"}); err != nil ||
		!strings.HasPrefix(buf.String(), strings.Repeat("ab", 20)+"|2 entradas, 2") {
		t.Fatalf("%q %v", buf.String(), err)
	}
	// 20 times 100 kB: the key's own format would make 100 kB.
	buf.Reset()
	err := th.Render(&buf, "home", "es", map[string]any{"Name": strings.Repeat("x", 100<<10)})
	if !errors.Is(err, ErrRenderLimit) || !errors.Is(err, errTooLong) || buf.Len() != 0 {
		t.Fatalf("translation longer than its key: %v", err)
	}
	if err := th.Render(&buf, "home", "en", map[string]any{"Name": strings.Repeat("x", 100<<10)}); err != nil {
		t.Fatalf("in English: %v", err)
	}
}

// The template sets an uploaded theme loads with never run unbounded: their
// functions fail, so only a slot of RenderContext or of the probe runs them.
func TestUploadedSetsClosed(t *testing.T) {
	dir := t.TempDir()
	writeTheme(t, dir, "closed", `{{define "layout"}}{{printf "%s" "x"}}{{upper "y"}}{{template "shared/footer-meta" .}}{{end}}`, "")
	m := testManager(t, dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	th, _ := m.Get("closed")
	for _, name := range []string{"printf", "upper"} {
		set, err := th.pages["home"].Clone()
		if err != nil {
			t.Fatal(err)
		}
		if name == "upper" {
			set = template.Must(set.Parse(`{{define "layout"}}{{upper "y"}}{{end}}`))
		}
		if err := set.ExecuteTemplate(io.Discard, "layout", nil); !errors.Is(err, errClosed) {
			t.Errorf("%s on the loaded set: %v", name, err)
		}
	}
	var buf bytes.Buffer
	if err := th.Render(&buf, "home", "en", nil); err != nil || !strings.HasPrefix(buf.String(), "xY") {
		t.Fatalf("through RenderContext: %q %v", buf.String(), err)
	}
	// Slots are reused, and a busy pool waits for one.
	ctx := context.Background()
	var slots []*slot
	for range maxSlots {
		s, err := th.acquire(ctx, "home", "en", time.Now().Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		slots = append(slots, s)
	}
	if _, err := th.acquire(ctx, "home", "en", time.Now().Add(10*time.Millisecond)); !errors.Is(err, errBusy) {
		t.Fatalf("all slots busy: %v", err)
	}
	th.release(slots[0])
	if s, err := th.acquire(ctx, "home", "en", time.Now().Add(time.Second)); err != nil || s != slots[0] {
		t.Fatalf("reuse: %v", err)
	}
}

func TestBoundedFormat(t *testing.T) {
	for f, ok := range map[string]bool{
		"%d posts": true, "%5.2f": true, "%-99s": true, "%[1]s and %[2]s": true, "100%": true, "50%% off": true,
		"%100d": false, "%.100s": false, "%*d": false, "%.*s": false, "%[1]*d": false, "%0999d": false,
	} {
		if got := boundedFormat(f); got != ok {
			t.Errorf("boundedFormat(%q) = %v", f, got)
		}
	}
}

// A theme of the site's directory never takes the place of a built-in one
// with its slug; it is listed as a failure, which the admin can delete.
func TestUploadedThemeNeverShadows(t *testing.T) {
	dir := t.TempDir()
	credit := `{{define "layout"}}{{template "content" .}}{{template "shared/footer-meta" .}}{{end}}`
	writeTheme(t, dir, "apple", credit, "mine")
	m := testManager(t, dir)
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if th, _ := m.Get("apple"); !th.Builtin {
		t.Fatal("an uploaded theme shadowed a built-in one")
	}
	if f := m.Failures(); len(f) != 1 || f[0].Slug != "apple" {
		t.Fatalf("failures %+v", f)
	}
	// Removing: only the site's own themes, never the active one.
	if _, err := m.Remove(context.Background(), "base", "", nil); !errors.Is(err, ErrNotOwn) {
		t.Fatalf("remove a built-in: %v", err)
	}
	if _, err := m.Remove(context.Background(), "base", "base", nil); !errors.Is(err, ErrNotOwn) {
		t.Fatalf("remove the active built-in: %v", err)
	}
	// The shadowed folder can go, even while the built-in theme it
	// shadows is the active one.
	if n, err := m.Remove(context.Background(), "apple", "apple", nil); err != nil || n <= 0 || len(m.Failures()) != 0 {
		t.Fatalf("remove the shadowed folder: %d %v %v", n, err, m.Failures())
	}
	if th, ok := m.Get("apple"); !ok || !th.Builtin {
		t.Fatal("the built-in theme went with it")
	}
	writeTheme(t, dir, "mine", credit, "mine")
	if err := m.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Remove(context.Background(), "mine", "mine", nil); !errors.Is(err, ErrActive) {
		t.Fatalf("remove the active theme: %v", err)
	}
	q := &countQuota{}
	n, err := m.Remove(context.Background(), "mine", "base", q)
	if err != nil || n <= 0 || q.released != n {
		t.Fatalf("remove: %d %v, released %d", n, err, q.released)
	}
	if _, ok := m.Get("mine"); ok {
		t.Fatal("still listed")
	}
	if _, err := os.Stat(filepath.Join(dir, "mine")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("files left: %v", err)
	}
}

// An install interrupted by a crash leaves folders that the next install
// sweeps once they are old enough: a moved-aside version comes back.
func TestSweepLeftovers(t *testing.T) {
	dir := t.TempDir()
	credit := `{{define "layout"}}{{template "content" .}}{{template "shared/footer-meta" .}}{{end}}`
	for _, name := range []string{".old-my-theme-0a1b2c3d4e5f", ".old-kept-0a1b2c3d4e5f", ".staging-123", ".trash-x-0a1b2c3d4e5f", ".staging-new"} {
		writeTheme(t, dir, name, credit, "old")
	}
	writeTheme(t, dir, "kept", credit, "new")
	old := time.Now().Add(-2 * leftoverAge)
	for _, name := range []string{".old-my-theme-0a1b2c3d4e5f", ".old-kept-0a1b2c3d4e5f", ".staging-123", ".trash-x-0a1b2c3d4e5f"} {
		if err := os.Chtimes(filepath.Join(dir, name), old, old); err != nil {
			t.Fatal(err)
		}
	}
	sweepLeftovers(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ents, _ := os.ReadDir(dir)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != ".staging-new kept my-theme" {
		t.Fatalf("after the sweep: %v", names)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "kept", "templates", "home.html")); !strings.Contains(string(b), "new") {
		t.Fatal("the sweep replaced a theme with its old version")
	}
}

type countQuota struct{ reserved, released int64 }

func (q *countQuota) Reserve(_ context.Context, n int64) error { q.reserved += n; return nil }
func (q *countQuota) Release(_ context.Context, n int64)       { q.released += n }
