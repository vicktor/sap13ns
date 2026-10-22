package web

import (
	"errors"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"sap13ns/internal/media"
	"sap13ns/internal/theme"
)

// The theme code editor (docs/THEMES.md, "Editing a theme's code"): an
// administrator opens a theme's templates, stylesheets, translations and
// theme.json in the code editor. A save goes through the checks of an
// upload (theme.SaveEdit), so it needs a plan that allows uploads. A
// built-in or network theme cannot change: its first save installs a copy
// in the site's own directory, which starts with the original's options
// and Design, and is not activated.

type themeCodeData struct {
	Theme   *theme.Theme
	Files   []themeCodeFile
	File    string
	Lang    string // the code editor's mode: html, css or json
	Content string
	// Copy: saving installs a copy (CopyName) instead of changing Theme.
	Copy     bool
	CopyName string
	Copies   []*theme.Theme // copies of Theme the site has already
	Active   bool
	Refused  *themeRefusal
}

type themeCodeFile struct {
	Name, Dir, Base string
	Current         bool
}

func (s *Server) themeCodeAllowed(w http.ResponseWriter, r *http.Request) bool {
	pol, err := s.themePolicy(r)
	if err != nil {
		s.serverError(w, r, err)
		return false
	}
	if !pol.Uploads {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "On the %s plan, sites use the built-in themes and the network's. Editing their code needs a paid plan.", pol.Plan))
		return false
	}
	return true
}

// themeCodePage is the editor's data for file of th (the layout when
// file is not one of its files).
func (s *Server) themeCodePage(r *http.Request, th *theme.Theme, file string) themeCodeData {
	names := th.EditableFiles()
	if !slices.Contains(names, file) {
		file = ""
		for _, f := range []string{"templates/layout.html", "theme.json"} {
			if slices.Contains(names, f) {
				file = f
				break
			}
		}
	}
	d := themeCodeData{Theme: th, File: file, Copy: !th.Own, Active: s.themes.Active(s.settings().ActiveTheme).Slug == th.Slug}
	for _, n := range names {
		dir, base := path.Split(n)
		d.Files = append(d.Files, themeCodeFile{Name: n, Dir: strings.TrimSuffix(dir, "/"), Base: base, Current: n == file})
	}
	d.Lang = codeLang(file)
	if file != "" {
		if b, err := th.ReadEditable(file); err == nil {
			d.Content = string(b)
			if d.Lang == "css" {
				d.Content = theme.FormatCSS(d.Content)
			}
		}
	}
	if d.Copy {
		d.CopyName = s.copyName(th)
		for _, t := range s.themes.List() {
			if t.Own && strings.HasPrefix(t.Slug, th.Slug+"-custom") {
				d.Copies = append(d.Copies, t)
			}
		}
	}
	return d
}

func (s *Server) copyName(th *theme.Theme) string {
	name := []rune(th.Name)
	if len(name) > 60 {
		name = name[:60]
	}
	return s.t("%s (custom)", string(name))
}

func codeLang(file string) string {
	switch path.Ext(file) {
	case ".css":
		return "css"
	case ".json":
		return "json"
	}
	return "html"
}

func (s *Server) adminThemeCode(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok || !s.themeCodeAllowed(w, r) {
		return
	}
	s.renderAdmin(w, r, "theme_code", th.Name, "themes", s.themeCodePage(r, th, r.URL.Query().Get("file")))
}

func (s *Server) adminThemeCodeSave(w http.ResponseWriter, r *http.Request) {
	th, ok := s.loadTheme(w, r)
	if !ok || !s.themeCodeAllowed(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, theme.MaxEditBytes*2+64<<10)
	file := r.FormValue("file")
	content := strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n")
	back := "/admin/appearance/themes/" + th.Slug + "/code?file=" + url.QueryEscape(file)
	if !slices.Contains(th.EditableFiles(), file) {
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	if len(content) > theme.MaxEditBytes {
		s.redirect(w, r, back, "error", s.tu(r, "The file is larger than %d KB.", theme.MaxEditBytes>>10))
		return
	}
	opts := theme.SaveOptions{Slug: th.Slug}
	if !th.Own {
		opts.Slug, opts.Name = s.themes.FreeSlug(th.Slug), s.copyName(th)
		if opts.Slug == "" {
			s.redirect(w, r, back, "error", s.tu(r, "There are too many copies of this theme. Delete one first."))
			return
		}
	}
	opts.UserID, opts.Source = uid(r), "editor"
	if s.limits != nil {
		opts.Quota = s.limits
	}
	res, err := s.themes.SaveEdit(r.Context(), th, file, []byte(content), opts)
	if ref, ok := theme.IsRefusal(err); ok {
		d := s.themeCodePage(r, th, file)
		d.Content = content
		d.Refused = s.refusal(r, ref, file)
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "theme_code", th.Name, "themes", d)
		return
	}
	switch {
	case errors.Is(err, media.ErrQuota):
		s.adminError(w, r, http.StatusRequestEntityTooLarge, s.storageFull(r))
		return
	case errors.Is(err, theme.ErrNoUploads):
		s.redirect(w, r, back, "error", s.tu(r, "This site has no themes directory to install into."))
		return
	case err != nil:
		s.log.Error("save theme code", "theme", th.Slug, "file", file, "err", err)
		s.redirect(w, r, back, "error", s.tu(r, "The theme could not be saved: %s", err.Error()))
		return
	}
	details := map[string]string{"file": file, "sha256": res.SHA256, "bytes": strconv.FormatInt(res.Bytes, 10)}
	if opts.Slug != th.Slug {
		// The copy starts where the original is: its options and Design.
		if err := s.store.CopyThemeData(r.Context(), th.Slug, opts.Slug); err != nil {
			s.log.Error("copy a theme's options and design", "from", th.Slug, "to", opts.Slug, "err", err)
		}
		details["from"] = th.Slug
		s.audit(r, uid(r), "theme.copied", opts.Slug, details)
		s.redirect(w, r, "/admin/appearance/themes/"+opts.Slug+"/code?file="+url.QueryEscape(file), "success",
			s.tu(r, "Saved as “%s”, a copy of “%s” with your change. Preview it, then activate it when it is ready.", res.Name, th.Name))
		return
	}
	s.audit(r, uid(r), "theme.edited", th.Slug, details)
	s.contentChanged() // the active theme may be the one edited
	s.redirect(w, r, back, "success", s.tu(r, "Saved."))
}
