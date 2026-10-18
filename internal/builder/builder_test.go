package builder

import (
	"strings"
	"testing"
)

var ctx = Context{
	Menus: []string{"primary", "footer"},
	Areas: []string{"top", "sidebar", "footer"},
	Langs: []string{"en"},
	Media: func(id int64) bool { return id == 7 },
}

const valid = `{
  "version": 1,
  "header": {"sections": [{"id": "h", "layout": "1", "columns": [[{"id": "hdr", "type": "site-header", "settings": {"menu": "primary", "search": "false"}}]]}]},
  "footer": {"sections": [{"id": "f", "layout": "1-1-1", "style": {"background": "muted"}, "columns": [
     [{"id": "txt", "type": "rich-text", "settings": {"text": "**Hola**"}, "i18n": {"en": {"text": "**Hello**"}}}],
     [{"id": "soc", "type": "social-links", "settings": {"github": "https://github.com/x", "email": "a@example.com"}}],
     [{"id": "meta", "type": "footer-meta"}]]}]},
  "pages": {
    "home": {"sections": [
      {"id": "s1", "layout": "1", "style": {"padding": "large", "width": "full"}, "columns": [[{"id": "hero", "type": "hero", "settings": {"title": "  Bienvenidos  ", "button_url": "/blog", "image": "7"}}]]},
      {"id": "s2", "layout": "2-1", "columns": [[{"id": "pl", "type": "post-list", "settings": {"layout": "bento"}}], [{"id": "wa", "type": "widget-area", "settings": {"area": "sidebar"}}, {"id": "cta", "type": "button", "settings": {"label": "Escríbenos", "url": "mailto:hola@example.com"}}]]}
    ]},
    "single": {"sections": [{"id": "s3", "layout": "1", "columns": [[{"id": "bc", "type": "breadcrumbs"}, {"id": "c", "type": "single-content"}, {"id": "cm", "type": "comments"}]]}]}
  }
}`

func TestParseValid(t *testing.T) {
	l, err := Parse([]byte(valid), ctx)
	if err != nil {
		t.Fatal(err)
	}
	hero := l.Pages["home"].Sections[0].Columns[0][0]
	if hero.Settings["title"] != "Bienvenidos" || hero.Settings["image"] != "7" {
		t.Errorf("hero settings: %v", hero.Settings)
	}
	if _, ok := hero.Settings["align"]; ok {
		t.Error("defaults should not be stored")
	}
	if got := l.Footer.Sections[0].Columns[0][0].Value("text", "en"); got != "**Hello**" {
		t.Errorf("translation: %q", got)
	}
	if got := l.Footer.Sections[0].Columns[0][0].Value("text", "fr"); got != "**Hola**" {
		t.Errorf("main value: %q", got)
	}
	if got := hero.Value("align", "es"); got != "center" {
		t.Errorf("default: %q", got)
	}
	// Round trip: a normalised layout parses to the same JSON.
	again, err := Parse(l.JSON(), ctx)
	if err != nil || string(again.JSON()) != string(l.JSON()) {
		t.Errorf("round trip: %v\n%s\n%s", err, l.JSON(), again.JSON())
	}
	if l.Region("single") == nil || l.Region("archive") != nil || l.Region("header") == nil {
		t.Error("Region lookup")
	}
}

func TestParseRejects(t *testing.T) {
	sec := func(region, blocks string) string {
		wrap := `{"sections": [{"id": "s", "layout": "1", "columns": [[` + blocks + `]]}]}`
		if region == "header" || region == "footer" {
			return `{"` + region + `": ` + wrap + `}`
		}
		return `{"pages": {"` + region + `": ` + wrap + `}}`
	}
	cases := map[string]struct{ json, field string }{
		"unknown field":      {`{"headr": {}}`, ""},
		"unknown type":       {sec("home", `{"id": "b", "type": "marquee"}`), "pages.home.sections[0].columns[0][0].type"},
		"wrong region":       {sec("home", `{"id": "b", "type": "footer-meta"}`), "pages.home.sections[0].columns[0][0].type"},
		"content on home":    {sec("home", `{"id": "b", "type": "single-content"}`), "pages.home.sections[0].columns[0][0].type"},
		"twice":              {sec("home", `{"id": "a", "type": "post-list"}, {"id": "b", "type": "post-list"}`), "pages.home.sections[0].columns[0][1].type"},
		"unknown setting":    {sec("home", `{"id": "b", "type": "hero", "settings": {"color": "red"}}`), "pages.home.sections[0].columns[0][0].settings.color"},
		"javascript url":     {sec("home", `{"id": "b", "type": "button", "settings": {"url": "javascript:alert(1)"}}`), "pages.home.sections[0].columns[0][0].settings.url"},
		"protocol-relative":  {sec("home", `{"id": "b", "type": "button", "settings": {"url": "//evil.example"}}`), "pages.home.sections[0].columns[0][0].settings.url"},
		"bad select":         {sec("home", `{"id": "b", "type": "hero", "settings": {"align": "justify"}}`), "pages.home.sections[0].columns[0][0].settings.align"},
		"bad bool":           {sec("home", `{"id": "b", "type": "spacer", "settings": {"line": "yes"}}`), "pages.home.sections[0].columns[0][0].settings.line"},
		"missing image":      {sec("home", `{"id": "b", "type": "image", "settings": {"image": "8"}}`), "pages.home.sections[0].columns[0][0].settings.image"},
		"unknown menu":       {sec("home", `{"id": "b", "type": "menu", "settings": {"menu": "social"}}`), "pages.home.sections[0].columns[0][0].settings.menu"},
		"unknown area":       {sec("home", `{"id": "b", "type": "widget-area", "settings": {"area": "nowhere"}}`), "pages.home.sections[0].columns[0][0].settings.area"},
		"too long":           {sec("home", `{"id": "b", "type": "hero", "settings": {"title": "`+strings.Repeat("x", 161)+`"}}`), "pages.home.sections[0].columns[0][0].settings.title"},
		"bad email":          {sec("home", `{"id": "b", "type": "social-links", "settings": {"email": "nope"}}`), "pages.home.sections[0].columns[0][0].settings.email"},
		"unknown language":   {sec("home", `{"id": "b", "type": "hero", "i18n": {"fr": {"title": "Salut"}}}`), "pages.home.sections[0].columns[0][0].i18n.fr"},
		"untranslatable":     {sec("home", `{"id": "b", "type": "hero", "i18n": {"en": {"align": "left"}}}`), "pages.home.sections[0].columns[0][0].i18n.en.align"},
		"bad id":             {sec("home", `{"id": "B 1", "type": "spacer"}`), "pages.home.sections[0].columns[0][0].id"},
		"duplicate id":       {sec("home", `{"id": "s", "type": "spacer"}`), "pages.home.sections[0].columns[0][0].id"},
		"unknown page type":  {`{"pages": {"landing": {"sections": []}}}`, "pages.landing"},
		"single w/o content": {sec("single", `{"id": "b", "type": "comments"}`), "pages.single"},
		"bad preset":         {`{"pages": {"home": {"sections": [{"id": "s", "layout": "3-1", "columns": [[]]}]}}}`, "pages.home.sections[0].layout"},
		"column count":       {`{"pages": {"home": {"sections": [{"id": "s", "layout": "1-1", "columns": [[]]}]}}}`, "pages.home.sections[0].columns"},
		"bad style":          {`{"pages": {"home": {"sections": [{"id": "s", "layout": "1", "style": {"background": "url(x)"}, "columns": [[]]}]}}}`, "pages.home.sections[0].style.background"},
		"unknown style":      {`{"pages": {"home": {"sections": [{"id": "s", "layout": "1", "style": {"class": "x"}, "columns": [[]]}]}}}`, "pages.home.sections[0].style.class"},
		"top bar elsewhere":  {`{"footer": {"top_bar": "off", "sections": []}}`, "footer.top_bar"},
		"version":            {`{"version": 2}`, "version"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.json), ctx)
		errs, ok := err.(Errors)
		if !ok {
			t.Errorf("%s: accepted (%v)", name, err)
			continue
		}
		if c.field == "" {
			continue
		}
		found := false
		for _, e := range errs {
			found = found || e.Field == c.field
		}
		if !found {
			t.Errorf("%s: no error on %s: %v", name, c.field, errs)
		}
	}
	if _, err := Parse([]byte(`{"pages": {"home": {"sections": []}}}`+strings.Repeat(" ", MaxJSON)), ctx); err == nil {
		t.Error("oversized layout accepted")
	}
}

// The footer always carries the licence credit, whatever the layout says.
func TestFooterMetaEnforced(t *testing.T) {
	for _, src := range []string{
		`{"footer": {"sections": []}}`,
		`{"footer": {"sections": [{"id": "f", "layout": "1", "columns": [[{"id": "t", "type": "rich-text", "settings": {"text": "Bye"}}]]}]}}`,
	} {
		l, err := Parse([]byte(src), ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !l.Footer.HasBlock(MetaType) {
			t.Errorf("Parse(%s): no footer-meta: %s", src, l.JSON())
		}
		if !Load([]byte(src), ctx).Footer.HasBlock(MetaType) {
			t.Errorf("Load(%s): no footer-meta", src)
		}
	}
	// Moving it is fine; it is not added twice.
	l, _ := Parse([]byte(valid), ctx)
	n := 0
	for _, s := range l.Footer.Sections {
		for _, c := range s.Columns {
			for _, b := range c {
				if b.Type == MetaType {
					n++
				}
			}
		}
	}
	if n != 1 {
		t.Errorf("%d footer-meta blocks", n)
	}
}

func TestLoadLenient(t *testing.T) {
	src := `{"header": {"sections": [{"id": "h", "layout": "1-1", "columns": [[{"id": "x", "type": "marquee"}], [{"id": "img", "type": "image", "settings": {"image": "99", "size": "small"}}], [{"id": "s", "type": "spacer"}]]}]},
	  "pages": {"single": {"sections": [{"id": "x", "layout": "1", "columns": [[{"id": "cm", "type": "comments"}]]}]}, "landing": {"sections": []}}}`
	l := Load([]byte(src), ctx)
	if l.Pages != nil {
		t.Errorf("a single layout without content should fall back to the theme: %s", l.JSON())
	}
	cols := l.Header.Sections[0].Columns
	if len(cols) != 2 || len(cols[0]) != 0 || len(cols[1]) != 2 {
		t.Fatalf("columns not fitted: %s", l.JSON())
	}
	if img := cols[1][0]; img.Settings["image"] != "" || img.Settings["size"] != "small" {
		t.Errorf("a missing image should be dropped, the rest kept: %v", img.Settings)
	}
	if l.Header.Sections[0].ID == cols[1][1].ID {
		t.Error("duplicate ids kept")
	}
	if !Load(nil, ctx).IsZero() {
		t.Error("empty layouts change something")
	}
}

func TestSafeURL(t *testing.T) {
	for _, u := range []string{"https://example.com", "http://example.com/a?b=c#d", "/about", "/", "#top", "mailto:a@b.c", "tel:+34600000000"} {
		if !SafeURL(u) {
			t.Errorf("refused %q", u)
		}
	}
	for _, u := range []string{"", "javascript:alert(1)", "JavaScript:alert(1)", "data:text/html,x", "//evil.example", "/\\evil", "https:///x", "https://", "vbscript:x", "/a b", "/a\"onload", "ftp://x"} {
		if SafeURL(u) {
			t.Errorf("accepted %q", u)
		}
	}
}
