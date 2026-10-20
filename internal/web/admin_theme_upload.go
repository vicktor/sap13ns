package web

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"sap13ns/internal/media"
	"sap13ns/internal/theme"
	"sap13ns/internal/themeimport"
)

// Theme uploads (docs/THEMES.md, "Uploading a theme"). An administrator
// uploads a .zip; internal/theme validates it and installs it into the
// site's own themes directory, not activated. On a site of a network the
// plan decides whether uploads are allowed, and the files count against
// its storage. No operator reviews themes: the validation refuses what a
// theme cannot hold, and uploaded themes render within output and time
// limits (theme.RenderContext).

// themeUploadLimit caps the request body of a theme upload.
const themeUploadLimit = theme.MaxArchiveBytes + 1<<20

// maxListedProblems is how many problems of a refused theme the page lists.
const maxListedProblems = 25

// themeRefusal is a refused upload, for the themes page.
type themeRefusal struct {
	Slug     string
	File     string
	Problems []themeProblem
	More     int // problems not listed
}

type themeProblem struct {
	Where, Message string
}

// themePolicy is whether the signed-in user can upload themes to the
// site, and its plan's name on a network. A network admin signed in
// through the portal can on any plan.
func (s *Server) themePolicy(r *http.Request) (ThemePolicy, error) {
	if s.limits == nil {
		return ThemePolicy{Uploads: true}, nil
	}
	pol, err := s.limits.ThemePolicy(r.Context())
	if err != nil || pol.Uploads {
		return pol, err
	}
	if account, ok := portalAccount(r); ok {
		admin, err := s.limits.NetworkAdmin(r.Context(), account)
		if err != nil {
			return pol, err
		}
		pol.Uploads, pol.NetworkAdmin = admin, admin
	}
	return pol, nil
}

// themesPage is the data of the themes page.
func (s *Server) themesPage(r *http.Request) (themesData, error) {
	pol, err := s.themePolicy(r)
	if err != nil {
		return themesData{}, err
	}
	return themesData{Themes: s.themes.List(), Active: s.themes.Active(s.settings().ActiveTheme).Slug,
		Uploads: pol.Uploads, NetAdmin: pol.NetworkAdmin, Plan: pol.Plan, Tenant: s.limits != nil, MaxMB: theme.MaxArchiveBytes >> 20,
		Failures: s.themes.Failures()}, nil
}

// refusal turns a refused upload into the list the page shows, translated.
func (s *Server) refusal(r *http.Request, ref *theme.Refusal, file string) *themeRefusal {
	out := &themeRefusal{Slug: ref.Slug, File: file}
	for i, p := range ref.Problems {
		if i == maxListedProblems {
			out.More = len(ref.Problems) - i
			break
		}
		out.Problems = append(out.Problems, themeProblem{Where: p.Where(), Message: s.tu(r, theme.Messages[p.Code], p.Args...)})
	}
	return out
}

func (s *Server) adminThemeUpload(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/appearance/themes"
	fail := func(msg string) { s.redirect(w, r, back, "error", msg) }
	pol, err := s.themePolicy(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !pol.Uploads {
		s.adminError(w, r, http.StatusForbidden, s.tu(r, "On the %s plan, sites use the built-in themes and the network's. Uploading your own needs a paid plan.", pol.Plan))
		return
	}
	file, hdr, err := r.FormFile("theme")
	if err != nil {
		fail(s.tu(r, "Choose a theme (.zip) to upload."))
		return
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(hdr.Filename), ".zip") || hdr.Size <= 0 {
		fail(s.tu(r, "A theme is uploaded as a .zip file."))
		return
	}
	if hdr.Size > theme.MaxArchiveBytes {
		fail(s.tu(r, "The theme is larger than %d MB.", theme.MaxArchiveBytes>>20))
		return
	}
	name := truncateStr(hdr.Filename, 120)
	// A theme of another CMS: converted when it can be (admin_theme_import.go).
	switch k := themeimport.Detect(file, hdr.Size); {
	case k.Convertible():
		s.convertUpload(w, r, file, hdr.Size, name)
		return
	case s.importRefusal(r, k) != "":
		fail(s.importRefusal(r, k))
		return
	}
	s.installTheme(w, r, file, hdr.Size, name, "")
}

// installTheme installs a theme archive, uploaded or converted from an
// upload (importedFrom), and renders the outcome.
func (s *Server) installTheme(w http.ResponseWriter, r *http.Request, file io.ReaderAt, size int64, name, importedFrom string) {
	const back = "/admin/appearance/themes"
	fail := func(msg string) { s.redirect(w, r, back, "error", msg) }
	var quota theme.Quota
	if s.limits != nil {
		quota = s.limits
	}
	res, err := s.themes.Install(r.Context(), file, size, theme.InstallOptions{UserID: uid(r), Source: "upload", Quota: quota, ImportedFrom: importedFrom})
	if ref, ok := theme.IsRefusal(err); ok {
		var codes []string
		for _, p := range ref.Problems {
			if !slices.Contains(codes, p.Code) {
				codes = append(codes, p.Code)
			}
		}
		details := map[string]string{"file": name, "problems": strconv.Itoa(len(ref.Problems)), "rules": strings.Join(codes, " ")}
		if importedFrom != "" {
			details["imported_from"] = importedFrom
		}
		if res != nil {
			details["sha256"] = res.SHA256
		}
		s.audit(r, uid(r), "theme.refused", ref.Slug, details)
		d, err := s.themesPage(r)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Refused = s.refusal(r, ref, name)
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "themes", s.tu(r, "Themes"), "themes", d)
		return
	}
	switch {
	case errors.Is(err, media.ErrQuota):
		s.adminError(w, r, http.StatusRequestEntityTooLarge, s.storageFull(r))
		return
	case errors.Is(err, theme.ErrNoUploads):
		fail(s.tu(r, "This site has no themes directory to install into."))
		return
	case err != nil:
		s.log.Error("install theme", "err", err)
		fail(s.tu(r, "The theme could not be installed: %s", err.Error()))
		return
	}
	action := "theme.uploaded"
	if res.Replaced {
		action = "theme.updated"
	}
	details := map[string]string{"version": res.Version, "previous": res.Previous,
		"sha256": res.SHA256, "bytes": strconv.FormatInt(res.Bytes, 10), "file": name}
	if importedFrom != "" {
		details["imported_from"] = importedFrom
	}
	s.audit(r, uid(r), action, res.Slug, details)
	s.contentChanged() // the active theme may be the one replaced
	msg := s.tu(r, "“%s” was installed. Preview it, then activate it when it is ready.", res.Name)
	if res.Replaced {
		msg = s.tu(r, "“%s” was replaced with the uploaded version.", res.Name)
	}
	s.redirect(w, r, back+"#theme-"+res.Slug, "success", msg)
}

func (s *Server) adminThemeDelete(w http.ResponseWriter, r *http.Request) {
	const back = "/admin/appearance/themes"
	slug := r.PathValue("slug")
	th, loaded := s.themes.Get(slug)
	name := slug
	switch {
	case loaded && th.Own:
		name = th.Name
	case slices.ContainsFunc(s.themes.Failures(), func(f theme.Failure) bool { return f.Slug == slug }):
		// A theme of the site's directory that does not load can go too,
		// also one a built-in or network theme of its slug shadows.
	default:
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	}
	active := s.themes.Active(s.settings().ActiveTheme).Slug
	if slug == s.settings().ActiveTheme {
		active = slug
	}
	var quota theme.Quota
	if s.limits != nil {
		quota = s.limits
	}
	n, err := s.themes.Remove(r.Context(), slug, active, quota)
	switch {
	case errors.Is(err, theme.ErrActive):
		s.redirect(w, r, back, "error", s.tu(r, "The active theme cannot be deleted. Activate another theme first."))
		return
	case errors.Is(err, theme.ErrNotOwn), errors.Is(err, theme.ErrNoUploads):
		s.adminError(w, r, http.StatusNotFound, s.tu(r, "Not found."))
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "theme.deleted", slug, map[string]string{"bytes": strconv.FormatInt(n, 10)})
	s.contentChanged()
	s.redirect(w, r, back, "success", s.tu(r, "“%s” was deleted.", name))
}
