package theme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
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
	funcs := template.FuncMap{"join": strings.Join, "paragraphs": func(s string) template.HTML { return template.HTML(s) }}
	lang := func(string) template.FuncMap {
		return template.FuncMap{"T": func(k string, args ...any) string {
			if len(args) > 0 {
				return fmt.Sprintf(k, args...)
			}
			return k
		}, "plural": func(n int, one, many string, args ...any) string { return one }}
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

func TestLimitWriterDeadline(t *testing.T) {
	w := &limitWriter{ctx: context.Background(), max: 10, deadline: time.Now().Add(-time.Second)}
	if _, err := w.Write(nil); err == nil || !strings.Contains(err.Error(), "longer") {
		t.Fatalf("past the deadline: %v", err)
	}
	if _, err := w.Write([]byte("x")); err == nil {
		t.Fatal("the error is not sticky")
	}
	w = &limitWriter{ctx: context.Background(), max: 4, deadline: time.Now().Add(time.Minute)}
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("5")); err == nil {
		t.Fatal("over the cap")
	}
}

func TestBoundedFuncs(t *testing.T) {
	f := boundedFuncs(template.FuncMap{"join": strings.Join})
	printf := f["printf"].(func(string, ...any) (string, error))
	if s, err := printf("%s-%d", "a", 3); err != nil || s != "a-3" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := printf("%s", strings.Repeat("x", maxFormatted)); !errors.Is(err, errTooLong) {
		t.Fatalf("long: %v", err)
	}
	join := f["join"].(func([]string, string) (string, error))
	if s, err := join([]string{"a", "b"}, ", "); err != nil || s != "a, b" {
		t.Fatalf("%q %v", s, err)
	}
	if _, err := join(make([]string, 10), strings.Repeat("x", maxFormatted/5)); !errors.Is(err, errTooLong) {
		t.Fatalf("long join: %v", err)
	}
	for _, name := range []string{"print", "println", "html", "js", "urlquery"} {
		fn := f[name].(func(...any) (string, error))
		if s, err := fn("<a>"); err != nil || s == "" {
			t.Errorf("%s: %q %v", name, s, err)
		}
		if _, err := fn(strings.Repeat("x", maxFormatted/2)); !errors.Is(err, errTooLong) {
			t.Errorf("%s long: %v", name, err)
		}
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
