package builder

import (
	"strings"
	"testing"
)

func errorOn(t *testing.T, err error, field string) {
	t.Helper()
	errs, ok := err.(Errors)
	if !ok {
		t.Fatalf("want an error on %s, got %v", field, err)
	}
	for _, e := range errs {
		if e.Field == field {
			return
		}
	}
	t.Errorf("no error on %s: %v", field, errs)
}

func front(blocks string) string {
	return `{"pages": {"front": {"sections": [{"id": "s", "layout": "1", "columns": [[` + blocks + `]]}]}}}`
}

func TestItemsParse(t *testing.T) {
	src := front(`{"id": "f", "type": "features", "settings": {"columns": "4"}, "items": [
	  {"settings": {"icon": "rocket", "title": "  Rápido  ", "text": "Sí"}, "i18n": {"en": {"title": "Fast"}}},
	  {"settings": {"icon": "sparkles", "title": "Bonito"}}
	]}, {"id": "p", "type": "pricing", "items": [
	  {"settings": {"name": "Pro", "highlight": "true", "button_url": "#contact"}},
	  {"settings": {"name": "Free", "highlight": "false"}}
	]}`)
	l, err := Parse([]byte(src), ctx)
	if err != nil {
		t.Fatal(err)
	}
	f := l.Pages["front"].Sections[0].Columns[0][0]
	if len(f.Items) != 2 || f.Items[0].Settings["title"] != "Rápido" {
		t.Fatalf("items: %+v", f.Items)
	}
	if _, ok := f.Items[1].Settings["icon"]; ok {
		t.Error("the default icon should not be stored")
	}
	ty, _ := TypeOf("features")
	title, _ := ty.Items.Field("title")
	icon, _ := ty.Items.Field("icon")
	if got := f.Items[0].Value(title, "en"); got != "Fast" {
		t.Errorf("translated item: %q", got)
	}
	if got := f.Items[0].Value(title, "fr"); got != "Rápido" {
		t.Errorf("main item value: %q", got)
	}
	if got := f.Items[1].Value(icon, "en"); got != "sparkles" {
		t.Errorf("item default: %q", got)
	}
	p := l.Pages["front"].Sections[0].Columns[0][1]
	if _, ok := p.Items[1].Settings["highlight"]; ok {
		t.Error("false is the default")
	}
	again, err := Parse(l.JSON(), ctx)
	if err != nil || string(again.JSON()) != string(l.JSON()) {
		t.Errorf("round trip: %v\n%s\n%s", err, l.JSON(), again.JSON())
	}
}

func TestItemsRejects(t *testing.T) {
	const at = "pages.front.sections[0].columns[0][0]"
	cases := map[string]struct{ blocks, field string }{
		"unknown icon":     {`{"id": "b", "type": "features", "items": [{"settings": {"icon": "skull"}}]}`, at + ".items[0].settings.icon"},
		"unknown setting":  {`{"id": "b", "type": "features", "items": [{"settings": {"colour": "red"}}]}`, at + ".items[0].settings.colour"},
		"long title":       {`{"id": "b", "type": "features", "items": [{}, {"settings": {"title": "` + strings.Repeat("x", 101) + `"}}]}`, at + ".items[1].settings.title"},
		"bad link":         {`{"id": "b", "type": "logos", "items": [{"settings": {"link": "javascript:x"}}]}`, at + ".items[0].settings.link"},
		"missing image":    {`{"id": "b", "type": "testimonials", "items": [{"settings": {"image": "8"}}]}`, at + ".items[0].settings.image"},
		"untranslatable":   {`{"id": "b", "type": "testimonials", "items": [{"i18n": {"en": {"name": "X"}}}]}`, at + ".items[0].i18n.en.name"},
		"unknown language": {`{"id": "b", "type": "faq", "items": [{"i18n": {"fr": {"question": "?"}}}]}`, at + ".items[0].i18n.fr"},
		"two highlighted":  {`{"id": "b", "type": "pricing", "items": [{"settings": {"highlight": "true"}}, {"settings": {"highlight": "true"}}]}`, at + ".items[1].settings.highlight"},
		"no items here":    {`{"id": "b", "type": "hero", "items": [{}]}`, at + ".items"},
		"too many":         {`{"id": "b", "type": "stats", "items": [` + strings.TrimSuffix(strings.Repeat(`{},`, 9), ",") + `]}`, at + ".items"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(front(c.blocks)), ctx)
			errorOn(t, err, c.field)
		})
	}
	// Load keeps what still validates: the first highlighted plan, eight
	// figures.
	l := load(t, front(`{"id": "p", "type": "pricing", "items": [{"settings": {"highlight": "true"}}, {"settings": {"highlight": "true", "name": "B"}}]},
	  {"id": "s", "type": "stats", "items": [`+strings.TrimSuffix(strings.Repeat(`{"settings": {"value": "1"}},`, 9), ",")+`]}`))
	col := l.Pages["front"].Sections[0].Columns[0]
	if col[0].Items[0].Settings["highlight"] != "true" || col[0].Items[1].Settings["highlight"] != "" || col[0].Items[1].Settings["name"] != "B" {
		t.Errorf("highlight: %+v", col[0].Items)
	}
	if len(col[1].Items) != 8 {
		t.Errorf("stats kept %d items", len(col[1].Items))
	}
}

func TestAnchors(t *testing.T) {
	sec := func(id, anchor string) string {
		return `{"id": "` + id + `", "anchor": "` + anchor + `", "layout": "1", "columns": [[]]}`
	}
	ok := `{"header": {"sections": [` + sec("h", "top") + `]},
	  "pages": {"front": {"sections": [` + sec("a", "features") + `, ` + sec("b", "pricing-2") + `]},
	            "home": {"sections": [` + sec("c", "features") + `]}}}`
	l, err := Parse([]byte(ok), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Pages["front"].Sections[1].Anchor; got != "pricing-2" {
		t.Errorf("anchor: %q", got)
	}
	cases := map[string]struct{ json, field string }{
		"upper case":   {`{"pages": {"front": {"sections": [` + sec("a", "Features") + `]}}}`, "pages.front.sections[0].anchor"},
		"digit first":  {`{"pages": {"front": {"sections": [` + sec("a", "1x") + `]}}}`, "pages.front.sections[0].anchor"},
		"reserved":     {`{"pages": {"front": {"sections": [` + sec("a", "main") + `]}}}`, "pages.front.sections[0].anchor"},
		"core prefix":  {`{"pages": {"front": {"sections": [` + sec("a", "sap-x") + `]}}}`, "pages.front.sections[0].anchor"},
		"too long":     {`{"pages": {"front": {"sections": [` + sec("a", "a"+strings.Repeat("b", 40)) + `]}}}`, "pages.front.sections[0].anchor"},
		"twice":        {`{"pages": {"front": {"sections": [` + sec("a", "x") + `, ` + sec("b", "x") + `]}}}`, "pages.front.sections[1].anchor"},
		"header twice": {`{"header": {"sections": [` + sec("h", "x") + `]}, "pages": {"front": {"sections": [` + sec("a", "x") + `]}}}`, "pages.front.sections[0].anchor"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.json), ctx)
			errorOn(t, err, c.field)
		})
	}
	l = load(t, `{"pages": {"front": {"sections": [`+sec("a", "x")+`, `+sec("b", "x")+`, `+sec("c", "Bad")+`]}}}`)
	if s := l.Pages["front"].Sections; s[0].Anchor != "x" || s[1].Anchor != "" || s[2].Anchor != "" {
		t.Errorf("lenient anchors: %+v", s)
	}
}

// Presets and starters are valid layouts in every language of the catalog
// (here, English and a fake translation).
func TestPresetsValidate(t *testing.T) {
	en := func(s string, _ ...any) string { return s }
	xx := func(s string, _ ...any) string { return "« " + s + " »" }
	for _, name := range PresetNames {
		r := Preset(name, en)
		if r == nil {
			t.Fatalf("%s: no preset", name)
		}
		Localize(r, Preset(name, xx), "en")
		l := &Layouts{Pages: map[string]*Region{"front": r}}
		got, err := Parse(l.JSON(), ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		front := got.Pages["front"]
		anchors := 0
		for _, s := range front.Sections {
			if s.Anchor != "" {
				anchors++
			}
		}
		if anchors == 0 {
			t.Errorf("%s: no anchors", name)
		}
		hero := front.Sections[0].Columns[0][0]
		if hero.Type != "hero" || hero.I18n["en"]["button_label"] == "" {
			t.Errorf("%s: hero not localised: %+v", name, hero)
		}
	}
	if Preset("blog", en) != nil {
		t.Error("unknown preset")
	}
	for typ, b := range Starters(en) {
		b.ID = "b"
		ty, _ := TypeOf(typ)
		if ty == nil || ty.Items == nil {
			t.Errorf("starter for %s, which has no items", typ)
			continue
		}
		l := &Layouts{Pages: map[string]*Region{"front": {Sections: []Section{{ID: "s", Layout: "1", Columns: [][]Block{{b}}}}}}}
		if _, err := Parse(l.JSON(), ctx); err != nil {
			t.Errorf("starter %s: %v", typ, err)
		}
	}
	for _, ty := range Types {
		if ty.Items != nil {
			if _, ok := Starters(en)[ty.Type]; !ok {
				t.Errorf("%s has no starter", ty.Type)
			}
		}
	}
}

func TestFrontRegion(t *testing.T) {
	// A post list may show on the front page; a post's content may not.
	if _, err := Parse([]byte(front(`{"id": "pl", "type": "post-list"}`)), ctx); err != nil {
		t.Error(err)
	}
	_, err := Parse([]byte(front(`{"id": "c", "type": "single-content"}`)), ctx)
	errorOn(t, err, "pages.front.sections[0].columns[0][0].type")
}
