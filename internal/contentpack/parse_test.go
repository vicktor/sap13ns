package contentpack

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustParse(t *testing.T, src string) Item {
	t.Helper()
	it, err := ParseBytes("test.md", []byte(src), "en")
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	return it
}

func parseErr(t *testing.T, src string) string {
	t.Helper()
	_, err := ParseBytes("test.md", []byte(src), "en")
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}

func TestParseScalarsAndDefaults(t *testing.T) {
	it := mustParse(t, `---
type: page            # page | post
key: features
title: Features
slug: features
excerpt: One-line summary used in cards and meta description.
order: 2
---
Body
`)
	if it.Type != "page" || it.Key != "features" || it.Title != "Features" || it.Slug != "features" {
		t.Fatalf("scalars: %+v", it)
	}
	if it.Excerpt != "One-line summary used in cards and meta description." {
		t.Errorf("excerpt = %q", it.Excerpt)
	}
	if it.Order != 2 || it.Lang != "en" || it.Comments != "closed" || it.Status != "published" {
		t.Errorf("defaults: order=%d lang=%q comments=%q status=%q", it.Order, it.Lang, it.Comments, it.Status)
	}
	if it.Body != "Body" {
		t.Errorf("body = %q", it.Body)
	}
	if it.Label("primary") != "Features" {
		t.Errorf("label falls back to the title, got %q", it.Label("primary"))
	}
}

func TestParseQuotesAndComments(t *testing.T) {
	it := mustParse(t, `---
type: post
key: k
title: "Plugins: a sandbox # not a comment"
slug: plugins
excerpt: 'It''s fast' # a comment
meta_description: "Say \"hi\" \\ bye"
menu_label: C# tips
---
`)
	if it.Title != "Plugins: a sandbox # not a comment" {
		t.Errorf("title = %q", it.Title)
	}
	if it.Excerpt != "It's fast" {
		t.Errorf("excerpt = %q", it.Excerpt)
	}
	if it.MetaDescription != `Say "hi" \ bye` {
		t.Errorf("meta = %q", it.MetaDescription)
	}
	if it.MenuLabel != "C# tips" {
		t.Errorf("a # not preceded by whitespace is kept, got %q", it.MenuLabel)
	}
	if it.Body != "" {
		t.Errorf("body = %q", it.Body)
	}
}

func TestParseLists(t *testing.T) {
	it := mustParse(t, `---
type: post
key: k
title: T
slug: t
categories: [Releases]
tags: [go, security, "a, b", 'it''s', ]
menu: []
---
`)
	if !reflect.DeepEqual(it.Categories, []string{"Releases"}) {
		t.Errorf("categories = %#v", it.Categories)
	}
	if !reflect.DeepEqual(it.Tags, []string{"go", "security", "a, b", "it's"}) {
		t.Errorf("tags = %#v", it.Tags)
	}
	if len(it.Menus) != 0 {
		t.Errorf("menus = %#v", it.Menus)
	}
	if msg := parseErr(t, "---\ntype: post\nkey: k\ntitle: T\nslug: t\ntags: [a, \"b]\n---\n"); !strings.Contains(msg, "test.md:6:") {
		t.Errorf("unterminated quote error should name the line: %s", msg)
	}
	if msg := parseErr(t, "---\ntype: post\nkey: k\ntitle: T\nslug: t\ntags: [a, b\n---\n"); !strings.Contains(msg, "must end with ]") {
		t.Errorf("unclosed list: %s", msg)
	}
	if msg := parseErr(t, "---\ntype: post\nkey: k\ntitle: [T]\nslug: t\n---\n"); !strings.Contains(msg, "test.md:4: title must be a single value") {
		t.Errorf("list for a scalar: %s", msg)
	}
}

func TestParseMenus(t *testing.T) {
	scalar := mustParse(t, "---\ntype: page\nkey: k\ntitle: Install\nslug: install\nmenu: primary\n---\n")
	if !reflect.DeepEqual(scalar.Menus, []string{"primary"}) {
		t.Errorf("scalar menu = %#v", scalar.Menus)
	}
	list := mustParse(t, `---
type: page
key: k
title: Install
slug: install
menu: [primary, cta, landing]
menu_label: Install
menu_label_cta: Get started
---
`)
	if !reflect.DeepEqual(list.Menus, []string{"primary", "cta", "landing"}) {
		t.Errorf("list menu = %#v", list.Menus)
	}
	if list.Label("primary") != "Install" || list.Label("cta") != "Get started" || list.Label("landing") != "Install" {
		t.Errorf("labels: %q %q %q", list.Label("primary"), list.Label("cta"), list.Label("landing"))
	}
	if len(list.Extra) != 0 {
		t.Errorf("menu_label_<loc> is not an unknown key: %#v", list.Extra)
	}
	if msg := parseErr(t, "---\ntype: page\nkey: k\ntitle: T\nslug: t\nmenu: [Primary Menu]\n---\n"); !strings.Contains(msg, "invalid menu location") {
		t.Errorf("bad location: %s", msg)
	}
}

func TestParseCRLFAndBody(t *testing.T) {
	src := "---\r\ntype: post\r\nkey: k\r\ntitle: T\r\nslug: t\r\n---\r\n\r\nFirst paragraph.\r\n\r\n---\r\n\r\nAfter a rule.\r\n\r\n```\r\n---\r\n```\r\n"
	it := mustParse(t, src)
	want := "First paragraph.\n\n---\n\nAfter a rule.\n\n```\n---\n```"
	if it.Body != want {
		t.Errorf("body = %q, want %q", it.Body, want)
	}
	if strings.Contains(it.Title, "\r") {
		t.Errorf("title keeps a CR: %q", it.Title)
	}
	bom := mustParse(t, "\xef\xbb\xbf---\ntype: post\nkey: k\ntitle: T\nslug: t\n---\nx")
	if bom.Body != "x" {
		t.Errorf("BOM file body = %q", bom.Body)
	}
}

func TestParseUnknownKeys(t *testing.T) {
	it := mustParse(t, "---\ntype: page\nkey: k\ntitle: T\nslug: t\nhero_image: hero.png\n# a full-line comment\n\n---\n")
	if it.Extra["hero_image"] != "hero.png" {
		t.Errorf("extra = %#v", it.Extra)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"no front matter":  {"# Title\n", "test.md:1: the file must start with a --- line"},
		"not closed":       {"---\ntype: page\n", "not closed"},
		"missing required": {"---\ntype: page\n---\n", "key is required"},
		"missing title":    {"---\ntype: page\nkey: k\nslug: s\n---\n", "title is required"},
		"missing slug":     {"---\ntype: page\nkey: k\ntitle: T\n---\n", "slug is required"},
		"bad type":         {"---\ntype: article\nkey: k\ntitle: T\nslug: s\n---\n", "test.md:2: type must be page or post"},
		"bad slug":         {"---\ntype: page\nkey: k\ntitle: T\nslug: Características\n---\n", "test.md:5: slug must be"},
		"double hyphen":    {"---\ntype: page\nkey: k\ntitle: T\nslug: a--b\n---\n", "slug must be"},
		"bad order":        {"---\ntype: page\nkey: k\ntitle: T\nslug: s\norder: first\n---\n", "test.md:6: order must be an integer"},
		"bad comments":     {"---\ntype: page\nkey: k\ntitle: T\nslug: s\ncomments: maybe\n---\n", "comments must be open or closed"},
		"bad status":       {"---\ntype: page\nkey: k\ntitle: T\nslug: s\nstatus: live\n---\n", "status must be published or draft"},
		"bad date":         {"---\ntype: post\nkey: k\ntitle: T\nslug: s\ndate: 18/10/2026\n---\n", "test.md:6: date must be"},
		"bad line":         {"---\ntype: post\nthis is not a pair\n---\n", "test.md:3: expected \"key: value\""},
		"uppercase key":    {"---\nType: post\n---\n", "test.md:2: expected \"key: value\""},
		"duplicate key":    {"---\ntype: post\ntype: page\n---\n", "test.md:3: duplicate key"},
		"bad lang":         {"---\ntype: page\nkey: k\ntitle: T\nslug: s\nlang: Spanish\n---\n", "invalid language code"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if msg := parseErr(t, c.src); !strings.Contains(msg, c.want) {
				t.Errorf("error %q does not contain %q", msg, c.want)
			}
		})
	}
}

func TestParseDate(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	cases := map[string]time.Time{
		"2026-10-18":                time.Date(2026, 10, 18, 9, 0, 0, 0, madrid),
		"2026-10-18 17:30":          time.Date(2026, 10, 18, 17, 30, 0, 0, madrid),
		"2026-10-18T17:30":          time.Date(2026, 10, 18, 17, 30, 0, 0, madrid),
		"2026-10-18T08:00:00Z":      time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC),
		"2026-10-18T08:00:00+02:00": time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := ParseDate(in, madrid)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("%s = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseDate("yesterday", madrid); err == nil {
		t.Error("expected an error for a bad date")
	}
	it := mustParse(t, "---\ntype: post\nkey: k\ntitle: T\nslug: s\n\ndate: 2026-10-18\n---\n")
	if it.Date != "2026-10-18" || it.DateLine != 7 {
		t.Errorf("date = %q line %d", it.Date, it.DateLine)
	}
}

func TestParseDirLanguagesAndPaths(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en", "features.md"), "---\ntype: page\nkey: features\ntitle: Features\nslug: features\nmenu: primary\n---\nEN\n")
	writeFile(t, filepath.Join(dir, "es", "caracteristicas.md"), "---\ntype: page\nkey: features\ntitle: Características\nslug: caracteristicas\nmenu: primary\n---\nES\n")
	writeFile(t, filepath.Join(dir, "es", "hello.md"), "---\ntype: post\nkey: hello\ntitle: Hola\nslug: hola\n---\n")
	writeFile(t, filepath.Join(dir, "en", "override.md"), "---\ntype: post\nkey: hello\ntitle: Hallo\nslug: hallo\nlang: de\n---\n")
	writeFile(t, filepath.Join(dir, "en", "notes.txt"), "ignored")
	writeFile(t, filepath.Join(dir, "en", "._features.md"), "\x00\x05\x16\x07 macOS metadata, ignored")
	writeFile(t, filepath.Join(dir, "assets", "x.md"), "---\nignored: because the directory is not a language\n---\n")
	writeFile(t, filepath.Join(dir, "site.json"), `{"title": "x", "language": "en"}`)

	items, err := Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 4 {
		t.Fatalf("got %d items", len(items))
	}
	byFile := map[string]Item{}
	for _, it := range items {
		byFile[filepath.Base(it.File)] = it
	}
	if got := byFile["features.md"]; got.Lang != "en" || got.Path("en") != "/features" {
		t.Errorf("en page: lang %q path %q", got.Lang, got.Path("en"))
	}
	if got := byFile["caracteristicas.md"]; got.Lang != "es" || got.Path("en") != "/es/caracteristicas" {
		t.Errorf("es page: lang %q path %q", got.Lang, got.Path("en"))
	}
	if got := byFile["hello.md"]; got.Path("en") != "/es/blog/hola" || got.Path("es") != "/blog/hola" {
		t.Errorf("post paths: %q %q", got.Path("en"), got.Path("es"))
	}
	if got := byFile["override.md"]; got.Lang != "de" {
		t.Errorf("lang override = %q", got.Lang)
	}
	// Deterministic order: language directories, then file names.
	var order []string
	for _, it := range items {
		order = append(order, filepath.Base(it.File))
	}
	if want := []string{"features.md", "override.md", "caracteristicas.md", "hello.md"}; !reflect.DeepEqual(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestParseDirDuplicatesAndErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "en", "a.md"), "---\ntype: page\nkey: a\ntitle: A\nslug: same\n---\n")
	writeFile(t, filepath.Join(dir, "en", "b.md"), "---\ntype: page\nkey: a\ntitle: B\nslug: same\n---\n")
	writeFile(t, filepath.Join(dir, "en", "c.md"), "---\ntype: post\nkey: a\ntitle: C\nslug: same\n---\n") // other type: fine
	writeFile(t, filepath.Join(dir, "es", "a.md"), "---\ntype: page\nkey: a\ntitle: A\nslug: same\n---\n") // other lang: fine
	writeFile(t, filepath.Join(dir, "es", "bad.md"), "---\ntype: page\n---\n")
	_, err := Parse(dir)
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{`page slug "same" in "en" is already used by`, `page key "a" in "en" is already used by`, "bad.md:1: key is required"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "c.md") {
		t.Errorf("a post may share a page's slug: %s", msg)
	}
	if _, err := Parse(t.TempDir()); err == nil {
		t.Error("an empty pack is an error")
	}
}

func TestLoadSite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "site.json")
	writeFile(t, p, `{
  "title": "sap13ns", "tagline": "Tag", "footer_text": "Foot",
  "language": "en", "languages": ["es"], "active_theme": "sap13ns",
  "timezone": "Europe/Madrid", "posts_per_page": 9,
  "site_text": {"es": {"site_title": "sap13ns", "tagline": "Lema", "footer_text": "Pie"}},
  "menu_links": [{"menu": "primary", "lang": "en", "label": "Blog", "url": "/blog", "order": 70}],
  "extra": true
}`)
	s, err := LoadSite(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "sap13ns" || s.Language != "en" || !reflect.DeepEqual(s.Languages, []string{"es"}) || s.PostsPerPage != 9 {
		t.Errorf("site = %+v", s)
	}
	if s.SiteText["es"].Tagline != "Lema" || s.SiteText["es"].FooterText != "Pie" {
		t.Errorf("site_text = %+v", s.SiteText)
	}
	if len(s.MenuLinks) != 1 || s.MenuLinks[0].URL != "/blog" || s.MenuLinks[0].Order != 70 {
		t.Errorf("menu_links = %+v", s.MenuLinks)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], `"extra"`) {
		t.Errorf("warnings = %v", s.Warnings)
	}

	writeFile(t, p, `{"title": "", "language": "English", "languages": ["en"], "timezone": "Mars/Base",
	  "menu_links": [{"menu": "Main Menu", "lang": "en", "label": "", "url": ""}]}`)
	_, err = LoadSite(p)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"title is required", "language must be", "timezone", "invalid menu location", "label must be", "url must be"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Releases":           "releases",
		"Ingeniería":         "ingenieria",
		"Diseño & código":    "diseno-codigo",
		"  Web   abierta  ":  "web-abierta",
		"WebAssembly (WASM)": "webassembly-wasm",
		"PostgreSQL 18":      "postgresql-18",
		"señal·ética":        "senal-etica",
		"Ünïcödé Straße":     "unicode-strasse",
		"go":                 "go",
		"!!!":                "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
