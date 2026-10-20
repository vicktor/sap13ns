package web

// The theme's own sections (theme.json "sections"): parts of a page with
// the theme's design, which the page builder offers as blocks of type
// "theme-<id>", so that a page made of them can be rearranged, lose or
// repeat sections and get new ones, each with its own texts. A section's
// template ({{define "section/<id>"}}) gets a ThemeSectionView. The theme's own
// front page (templates/front.html) renders the same templates in
// FrontSections' order, so a theme looks the same with or without a
// layout.

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"sap13ns/internal/builder"
	"sap13ns/internal/icons"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// sectionTypePrefix starts the block type of a theme's section.
const sectionTypePrefix = "theme-"

// ThemeSectionView is what a section's template receives.
type ThemeSectionView struct {
	ID     string // the section's id in theme.json
	Anchor string // its id on the page, for menu links (#about)
	Page   *PageData
	Site   *SiteView
	// For shared/theme-section, which wraps the section: AnchorClass
	// marks the wrapper as a scroll target (sap-anchor), Animate is its
	// animation.
	AnchorClass bool
	Animate     string

	def   *theme.Section
	block *builder.Block // nil on the theme's own front page
	media func(int64) *store.Media
}

// themeSectionTypes are th's sections as page-builder block types. Their
// fields have no defaults: what was not edited follows the theme
// (ThemeSectionView.Text), not a copy of it.
func themeSectionTypes(th *theme.Theme) []builder.Type {
	if th == nil || len(th.Sections) == 0 {
		return nil
	}
	out := make([]builder.Type, 0, len(th.Sections))
	for _, sec := range th.Sections {
		where := sec.Where
		if len(where) == 0 {
			where = []string{"front"}
		}
		icon := sec.Icon
		if icon == "" || !slices.Contains(icons.Names(), icon) {
			icon = "layout-template"
		}
		t := builder.Type{Type: sectionTypePrefix + sec.ID, Label: sec.Label, Icon: icon, Help: sec.Help, Where: where}
		for _, f := range sec.Fields {
			bf := builder.Field{Key: f.Key, Label: f.Label, Help: f.Help, I18n: true}
			switch f.Type {
			case theme.ContentText:
				bf.Type, bf.Max = "text", 500
			case theme.ContentTextarea, theme.ContentLines:
				bf.Type, bf.Max = "textarea", 5000
			case theme.ContentImage:
				bf.Type, bf.I18n = "image", false
			case theme.SectionURL:
				bf.Type = "url"
			}
			t.Fields = append(t.Fields, bf)
		}
		t.Fields = append(t.Fields, builder.AnimationField())
		out = append(out, t)
	}
	return out
}

// sectionOf returns the theme section a block type names, or nil.
func sectionOf(th *theme.Theme, typ string) *theme.Section {
	if th == nil || !strings.HasPrefix(typ, sectionTypePrefix) {
		return nil
	}
	return th.Section(strings.TrimPrefix(typ, sectionTypePrefix))
}

// value is the field key as edited in this section for the page's
// language ("" when it was not): its translation, else its main text, as
// the page builder's blocks have them.
func (v *ThemeSectionView) value(key string) string {
	if v.block == nil {
		return ""
	}
	return v.block.Value(key, v.Site.Lang)
}

// Text returns the section's text key in the page's language: as edited in
// this section, else the theme's text it shows (Textos e imágenes), else
// its default in the theme's translation.
func (v *ThemeSectionView) Text(key string) string {
	f := v.def.Field(key)
	if f == nil || f.Type == theme.ContentImage {
		return ""
	}
	if s := v.value(key); s != "" {
		return s
	}
	if f.Content != "" {
		return v.Site.Text(f.Content)
	}
	if f.Default == "" {
		return ""
	}
	if f.Type == theme.ContentLines {
		return strings.Join(v.Lines(key), "\n")
	}
	if f.Type == theme.SectionURL {
		return f.Default
	}
	return v.Site.server.themeText(v.Site.Lang, v.Site.theme, f.Default)
}

// Lines returns the lines of the text key, each default line translated
// on its own.
func (v *ThemeSectionView) Lines(key string) []string {
	f := v.def.Field(key)
	if f == nil || f.Type == theme.ContentImage {
		return nil
	}
	src, translate := v.value(key), false
	switch {
	case src != "":
	case f.Content != "":
		return v.Site.Lines(f.Content)
	default:
		src, translate = f.Default, true
	}
	var out []string
	for l := range strings.SplitSeq(src, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if translate {
				l = v.Site.server.themeText(v.Site.Lang, v.Site.theme, l)
			}
			out = append(out, l)
		}
	}
	return out
}

// Image returns the image chosen for key in this section, else the
// theme's image it shows when that was chosen in the customiser, else nil:
// the template then shows its own.
func (v *ThemeSectionView) Image(key string) *ImageView {
	f := v.def.Field(key)
	if f == nil || f.Type != theme.ContentImage {
		return nil
	}
	if id, err := strconv.ParseInt(v.value(key), 10, 64); err == nil && id > 0 && v.media != nil {
		if m := v.media(id); m != nil && m.IsImage() {
			return imageView(m)
		}
	}
	if f.Content != "" {
		return v.Site.Image(f.Content)
	}
	return nil
}

// URL returns the link key, with the page's language prefix for a path of
// the site (as menus have them).
func (v *ThemeSectionView) URL(key string) string {
	f := v.def.Field(key)
	if f == nil || f.Type != theme.SectionURL {
		return ""
	}
	u := v.value(key)
	if u == "" {
		u = f.Default
	}
	if u == "" {
		return ""
	}
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") && v.Site.server != nil {
		u = v.Site.server.langPath(v.Site.Lang, u)
	}
	return v.Site.Link(u)
}

// FrontSections returns the theme's own front page (theme.json
// front_sections) as section views, for templates/front.html:
//
//	{{range .Site.FrontSections $}}{{template "shared/theme-section" .}}{{end}}
func (s *SiteView) FrontSections(pd *PageData) []*ThemeSectionView {
	if s == nil || s.theme == nil {
		return nil
	}
	var out []*ThemeSectionView
	for _, id := range s.theme.FrontSections {
		if def := s.theme.Section(id); def != nil {
			out = append(out, &ThemeSectionView{ID: id, Anchor: def.Anchor, AnchorClass: def.Anchor != "", Page: pd, Site: s, def: def})
		}
	}
	return out
}

// sectionView is the view of a block that is one of the theme's sections.
func themeSectionView(def *theme.Section, b *builder.Block, anchor string, pd *PageData, media func(int64) *store.Media) *ThemeSectionView {
	return &ThemeSectionView{ID: def.ID, Anchor: anchor, Page: pd, Site: pd.Site, def: def, block: b, media: media}
}

// sectionPreset is the theme's own front page as a layout for the page
// builder to start from: one section per front section, with its anchor,
// and nothing edited.
func sectionPreset(th *theme.Theme) *builder.Region {
	if th == nil || len(th.FrontSections) == 0 {
		return nil
	}
	r := &builder.Region{}
	for i, id := range th.FrontSections {
		def := th.Section(id)
		if def == nil {
			continue
		}
		n := strconv.Itoa(i + 1)
		r.Sections = append(r.Sections, builder.Section{ID: "ts" + n, Anchor: def.Anchor, Layout: "1",
			Columns: [][]builder.Block{{{ID: "tb" + n, Type: sectionTypePrefix + id}}}})
	}
	return r
}

// sectionHints are the texts a theme section shows until it is edited, per
// language, for the page builder's form: the main language's as the
// field's default (left out of the layout when typed again), the others'
// as placeholders.
func (s *Server) sectionHints(ctx context.Context, th *theme.Theme, t *builder.Type) {
	def := sectionOf(th, t.Type)
	if def == nil {
		return
	}
	main := s.mainLang()
	e := s.publishedDesign(ctx, th)
	published := &siteContent{design: e.design, media: e.mediaByID, main: main}
	langs := []string{main}
	for _, l := range s.extraLangs() {
		langs = append(langs, l.Code)
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		if f.Key == "animation" || f.Type == "image" {
			continue
		}
		hints := map[string]string{}
		for _, lang := range langs {
			sv := &SiteView{Lang: lang, theme: th, server: s, content: published}
			v := (&ThemeSectionView{def: def, Site: sv}).Text(f.Key)
			if f.Type == "url" {
				v = def.Field(f.Key).Default
			}
			if lang == main {
				f.Default = v
			} else if v != "" {
				hints[lang] = v
			}
		}
		if len(hints) > 0 {
			f.Hints = hints
		}
	}
}
