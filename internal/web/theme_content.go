package web

// The theme's editable texts and images (theme.json "content"), which the
// customiser edits with the rest of the design: templates read them with
// .Site.Text, .Site.Lines and .Site.Image, and mark the element that
// shows one with data-sap-edit="key", so that a click on it in the
// customiser's preview opens its field.
// What was not edited is the theme's default, translated by its catalog.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"sap13ns/internal/customize"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// siteContent is the design's content for one render.
type siteContent struct {
	design *customize.Design
	media  func(int64) *store.Media
	main   string
}

// contentSpecs are th's content fields for validating a design.
func contentSpecs(th *theme.Theme) []customize.ContentSpec {
	var out []customize.ContentSpec
	for _, g := range th.Content {
		for _, f := range g.Fields {
			out = append(out, customize.ContentSpec{Key: f.Key, Type: f.Type})
		}
	}
	return out
}

// prefetchContent looks up the images of a stored design in one query,
// before validating it checks them one by one.
func prefetchContent(ms *mediaSet, th *theme.Theme, raw []byte) {
	var d struct {
		Content map[string]string `json:"content"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &d) != nil {
		return
	}
	var ids []int64
	for k, v := range d.Content {
		if f := th.ContentField(k); f != nil && f.Type == theme.ContentImage {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
	}
	ms.prefetch(ids)
}

// edited returns the text key as edited for the page's language.
func (s *SiteView) edited(key string) string {
	if s.content == nil || s.content.design == nil {
		return ""
	}
	return s.content.design.ContentIn(s.Lang, s.content.main, key)
}

// Text returns the theme's text key in the page's language: the edited
// one, or the theme's default in its translation.
func (s *SiteView) Text(key string) string {
	if v := s.edited(key); v != "" {
		return v
	}
	if s.theme == nil {
		return ""
	}
	f := s.theme.ContentField(key)
	if f == nil || f.Type == theme.ContentImage || f.Default == "" {
		return ""
	}
	if f.Type == theme.ContentLines {
		return strings.Join(s.Lines(key), "\n")
	}
	return s.server.themeText(s.Lang, s.theme, f.Default)
}

// Lines returns the lines of the text key (a list of the theme's: values,
// steps…), each default line translated on its own.
func (s *SiteView) Lines(key string) []string {
	v := s.edited(key)
	if v == "" && s.theme != nil {
		f := s.theme.ContentField(key)
		if f == nil || f.Type == theme.ContentImage {
			return nil
		}
		var lines []string
		for l := range strings.SplitSeq(f.Default, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, s.server.themeText(s.Lang, s.theme, l))
			}
		}
		return lines
	}
	var lines []string
	for l := range strings.SplitSeq(v, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// Image returns the image chosen for key, or nil: the theme then shows
// its own.
func (s *SiteView) Image(key string) *ImageView {
	if s.content == nil || s.content.design == nil || s.content.media == nil {
		return nil
	}
	id, err := strconv.ParseInt(s.content.design.Content[key], 10, 64)
	if err != nil || id <= 0 {
		return nil
	}
	return imageView(s.content.media(id))
}

// contentGroupView is a content group in the customiser form.
type contentGroupView struct {
	ID, Label string
	Fields    []contentFieldView
}

// contentFieldView is one field: a text in each language, or an image.
type contentFieldView struct {
	Key, Label, Help, Type string
	Values                 []contentValue // texts: the main language first
	Image                  *ImageView     // images: the chosen one
	ImageID                int64
	ThemeImage             string // the theme's own image, as a thumbnail
}

// contentValue is a text in one language: what the page shows (the edited
// text or the default) and the default, which the customiser leaves out of
// the design when the text is back to it.
type contentValue struct {
	Lang, LangName, Name string
	Value, Default       string
	Main                 bool
}

// contentForm builds the customiser's "Texts and images" for d.
func (s *Server) contentForm(r *http.Request, th *theme.Theme, d *customize.Design, ms *mediaSet) []contentGroupView {
	main := s.mainLang()
	langs := append([]LangLink{{Code: main, Name: langName(main)}}, s.extraLangs()...)
	ui := s.uiLang(r)
	var out []contentGroupView
	for _, g := range th.Content {
		gv := contentGroupView{ID: g.ID, Label: s.themeText(ui, th, g.Label)}
		for _, f := range g.Fields {
			fv := contentFieldView{Key: f.Key, Label: s.themeText(ui, th, f.Label), Type: f.Type}
			if f.Help != "" {
				fv.Help = s.themeText(ui, th, f.Help)
			}
			if f.Type == theme.ContentImage {
				if id, err := strconv.ParseInt(d.Content[f.Key], 10, 64); err == nil {
					fv.ImageID, fv.Image = id, imageView(ms.lookup(id))
				}
				if f.Default != "" {
					fv.ThemeImage = th.StaticURL(f.Default)
				}
				gv.Fields = append(gv.Fields, fv)
				continue
			}
			for _, l := range langs {
				sv := &SiteView{Lang: l.Code, theme: th, server: s}
				def := sv.Text(f.Key)
				sv.content = &siteContent{design: d, main: main}
				name := "content." + f.Key
				if l.Code != main {
					name = "content_i18n." + l.Code + "." + f.Key
				}
				fv.Values = append(fv.Values, contentValue{Lang: l.Code, LangName: l.Name, Name: name, Value: sv.Text(f.Key), Default: def, Main: l.Code == main})
			}
			gv.Fields = append(gv.Fields, fv)
		}
		out = append(out, gv)
	}
	return out
}
