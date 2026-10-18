package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
)

// ------------------------------------------------------------- dashboard --

type dashboardData struct {
	Posts    map[string]int
	Pages    map[string]int
	Comments map[string]int
	Drafts   []store.Post
	Recent   []store.Post
	Pending  []store.Comment
	Users    int
	Plugins  int
	Theme    string
	Stats    *dashStats // last 7 days; nil when analytics are off or not allowed
	Now      time.Time
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if !auth.Can(u.Role, auth.CapEditOwnPosts) {
		http.Redirect(w, r, "/admin/profile", http.StatusSeeOther)
		return
	}
	ctx := r.Context()
	var authorID int64
	if !auth.Can(u.Role, auth.CapEditOthersPosts) {
		authorID = u.ID
	}
	d := dashboardData{Now: time.Now(), Theme: s.themes.Active(s.settings().ActiveTheme).Name}
	if auth.Can(u.Role, auth.CapEditOthersPosts) {
		d.Stats = s.dashboardStats(ctx)
	}
	var err error
	if d.Posts, err = s.store.CountPostsByStatus(ctx, store.TypePost, authorID); err != nil {
		s.serverError(w, r, err)
		return
	}
	if auth.Can(u.Role, auth.CapManagePages) {
		if d.Pages, err = s.store.CountPostsByStatus(ctx, store.TypePage, 0); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if d.Drafts, _, err = s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePost, AuthorID: u.ID,
		Statuses: []string{store.StatusDraft, store.StatusPending}, Order: "updated", Limit: 5}); err != nil {
		s.serverError(w, r, err)
		return
	}
	if d.Recent, _, err = s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePost, PublicOnly: true, Limit: 5}); err != nil {
		s.serverError(w, r, err)
		return
	}
	if auth.Can(u.Role, auth.CapModerate) {
		if d.Comments, err = s.store.CountCommentsByStatus(ctx); err != nil {
			s.serverError(w, r, err)
			return
		}
		if d.Pending, _, err = s.store.ListComments(ctx, store.CommentQuery{Status: "pending", Limit: 5}); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if auth.Can(u.Role, auth.CapManageUsers) {
		d.Users, _ = s.store.CountUsers(ctx)
	}
	for _, p := range s.plugins.List() {
		if p.Status == "enabled" {
			d.Plugins++
		}
	}
	s.renderAdmin(w, r, "dashboard", s.t("Dashboard"), "dashboard", d)
}

// ----------------------------------------------------------------- terms --

type termsData struct {
	Taxonomy string
	Terms    []store.Term
	Term     *store.Term
	Error    string
	Form     store.TermInput
	Langs    []LangLink // extra languages whose names can be translated
}

func validTaxonomy(t string) bool { return t == "category" || t == "tag" }

func taxonomyTitle(s *Server, t string) string {
	if t == "tag" {
		return s.t("Tags")
	}
	return s.t("Categories")
}

func (s *Server) adminTerms(w http.ResponseWriter, r *http.Request) {
	tax := r.PathValue("taxonomy")
	if !validTaxonomy(tax) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	s.renderTerms(w, r, http.StatusOK, tax, "", store.TermInput{})
}

func (s *Server) renderTerms(w http.ResponseWriter, r *http.Request, status int, tax, errMsg string, form store.TermInput) {
	terms, err := s.store.ListTerms(r.Context(), tax, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "terms", taxonomyTitle(s, tax), tax, termsData{Taxonomy: tax, Terms: terms, Error: errMsg, Form: form, Langs: s.extraLangs()})
}

func (s *Server) termFromForm(r *http.Request, tax string, exceptID int64) (store.TermInput, string) {
	in := store.TermInput{Taxonomy: tax, Name: trimmed(r, "name"), Description: trimmed(r, "description")}
	in.Slug = content.Slugify(trimmed(r, "slug"))
	if in.Slug == "" {
		in.Slug = content.Slugify(in.Name)
	}
	if tax == "category" {
		in.ParentID = formInt64Ptr(r, "parent_id")
		if in.ParentID != nil && *in.ParentID == exceptID {
			in.ParentID = nil
		}
	}
	// Names in the site's other languages: i18n_<lang>_name / _description.
	for _, l := range s.siteLangs()[1:] {
		tr := store.TermText{Name: trimmed(r, "i18n_"+l+"_name"), Description: trimmed(r, "i18n_"+l+"_description")}
		if tooLong(tr.Name, 100) || tooLong(tr.Description, 1000) {
			return in, s.t("The name is too long (100 characters max).")
		}
		if tr.Name != "" || tr.Description != "" {
			if in.I18n == nil {
				in.I18n = map[string]store.TermText{}
			}
			in.I18n[l] = tr
		}
	}
	switch {
	case in.Name == "" || in.Slug == "":
		return in, s.t("The name is required.")
	case tooLong(in.Name, 100):
		return in, s.t("The name is too long (100 characters max).")
	case tooLong(in.Description, 1000):
		return in, s.t("The description is too long (1000 characters max).")
	}
	return in, ""
}

func (s *Server) adminTermCreate(w http.ResponseWriter, r *http.Request) {
	tax := r.PathValue("taxonomy")
	if !validTaxonomy(tax) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	in, msg := s.termFromForm(r, tax, 0)
	if msg != "" {
		s.renderTerms(w, r, http.StatusUnprocessableEntity, tax, msg, in)
		return
	}
	id, err := s.store.CreateTerm(r.Context(), in)
	if errors.Is(err, store.ErrConflict) {
		s.renderTerms(w, r, http.StatusUnprocessableEntity, tax, s.t("Another item already uses that slug."), in)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), tax+".created", fmt.Sprintf("#%d %s", id, in.Name), nil)
	s.contentChanged()
	s.redirect(w, r, "/admin/terms/"+tax, "success", s.t("“%s” created.", in.Name))
}

func (s *Server) loadTerm(w http.ResponseWriter, r *http.Request) (*store.Term, bool) {
	id, ok := pathID(r)
	tax := r.PathValue("taxonomy")
	if !ok || !validTaxonomy(tax) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	t, err := s.store.TermByID(r.Context(), id)
	if err != nil || t.Taxonomy != tax {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	return t, true
}

func (s *Server) adminTermEdit(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTerm(w, r)
	if !ok {
		return
	}
	s.renderTermEdit(w, r, http.StatusOK, t, "")
}

func (s *Server) renderTermEdit(w http.ResponseWriter, r *http.Request, status int, t *store.Term, msg string) {
	terms, err := s.store.ListTerms(r.Context(), t.Taxonomy, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "term_edit", t.Name, t.Taxonomy, termsData{Taxonomy: t.Taxonomy, Terms: terms, Term: t, Error: msg, Langs: s.extraLangs()})
}

func (s *Server) adminTermUpdate(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTerm(w, r)
	if !ok {
		return
	}
	in, msg := s.termFromForm(r, t.Taxonomy, t.ID)
	edited := *t
	edited.Name, edited.Slug, edited.Description, edited.ParentID = in.Name, in.Slug, in.Description, in.ParentID
	if msg != "" {
		s.renderTermEdit(w, r, http.StatusUnprocessableEntity, &edited, msg)
		return
	}
	err := s.store.UpdateTerm(r.Context(), t.ID, in)
	if errors.Is(err, store.ErrConflict) {
		s.renderTermEdit(w, r, http.StatusUnprocessableEntity, &edited, s.t("Another item already uses that slug."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), t.Taxonomy+".updated", fmt.Sprintf("#%d %s", t.ID, in.Name), nil)
	s.contentChanged()
	s.redirect(w, r, "/admin/terms/"+t.Taxonomy, "success", s.t("Saved."))
}

func (s *Server) adminTermDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := s.loadTerm(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteTerm(r.Context(), t.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), t.Taxonomy+".deleted", fmt.Sprintf("#%d %s", t.ID, t.Name), nil)
	s.contentChanged()
	s.redirect(w, r, "/admin/terms/"+t.Taxonomy, "success", s.t("“%s” deleted.", t.Name))
}

// ----------------------------------------------------------------- media --

type mediaData struct {
	Items  []store.Media
	Total  int
	Page   *Pagination
	Query  string
	Images bool
	Item   *store.Media
	Max    int64
	Accept string
}

const mediaAccept = "image/jpeg,image/png,image/gif,image/webp,image/avif,application/pdf,audio/mpeg,audio/ogg,audio/wav,video/mp4,video/webm,text/plain,font/woff2,.woff2"

// mediaScope limits authors and contributors to their own uploads.
func mediaScope(u *store.User) int64 {
	if auth.Can(u.Role, auth.CapManageMedia) {
		return 0
	}
	return u.ID
}

func canManageMedia(u *store.User, m *store.Media) bool {
	return auth.Can(u.Role, auth.CapManageMedia) || (m.UploaderID != nil && *m.UploaderID == u.ID)
}

func (s *Server) adminMedia(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	images := r.URL.Query().Get("type") == "image"
	page := listPage(r)
	const per = 36
	items, total, err := s.store.ListMedia(r.Context(), store.MediaQuery{UploaderID: mediaScope(u), Search: q, ImagesOnly: images, Limit: per, Offset: (page - 1) * per})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	vals := r.URL.Query()
	vals.Del("page")
	s.renderAdmin(w, r, "media", s.t("Media"), "media", mediaData{Items: items, Total: total, Query: q, Images: images,
		Page: newPagination("/admin/media", vals, page, per, total), Max: s.cfg.MaxUploadBytes, Accept: mediaAccept})
}

// adminMediaUpload accepts one or more files. Requests from the editor's
// fetch() get JSON back; plain form posts are redirected.
func (s *Server) adminMediaUpload(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	wantJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	fail := func(status int, msg string) {
		if wantJSON {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		s.redirect(w, r, "/admin/media", "error", msg)
	}
	if !s.uploadUser.Allow(fmt.Sprint(u.ID)) {
		fail(http.StatusTooManyRequests, s.t("Too many uploads. Try again later."))
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		fail(http.StatusBadRequest, s.t("The file is too large (%s max).", filesize(s.cfg.MaxUploadBytes)))
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 || len(files) > 20 {
		fail(http.StatusBadRequest, s.t("Choose between 1 and 20 files."))
		return
	}
	var out []map[string]any
	var errs []string
	for _, fh := range files {
		m, err := s.storeUpload(r, u, fh.Filename, func() (multipartFile, error) { return fh.Open() })
		if err != nil {
			errs = append(errs, fh.Filename+": "+err.Error())
			continue
		}
		out = append(out, mediaJSON(m))
	}
	if len(out) > 0 {
		s.audit(r, u.ID, "media.uploaded", fmt.Sprintf("%d files", len(out)), nil)
	}
	if wantJSON {
		status := http.StatusOK
		if len(out) == 0 {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(w, status, map[string]any{"items": out, "errors": errs})
		return
	}
	if len(errs) > 0 {
		s.redirect(w, r, "/admin/media", "error", strings.Join(errs, " · "))
		return
	}
	s.redirect(w, r, "/admin/media", "success", s.t("%d files uploaded.", len(out)))
}

type multipartFile interface {
	Read([]byte) (int, error)
	Close() error
}

func (s *Server) storeUpload(r *http.Request, u *store.User, name string, open func() (multipartFile, error)) (*store.Media, error) {
	f, err := open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	res, err := s.media.Save(f)
	switch {
	case errors.Is(err, media.ErrTooLarge):
		return nil, errors.New(s.t("The file is too large (%s max).", filesize(s.cfg.MaxUploadBytes)))
	case errors.Is(err, media.ErrUnsupported):
		return nil, errors.New(s.t("This file type is not allowed."))
	case errors.Is(err, media.ErrBadImage):
		return nil, errors.New(s.t("The image could not be processed."))
	case err != nil:
		s.log.Error("save upload", "err", err)
		return nil, errors.New(s.t("The file could not be saved."))
	}
	m := &store.Media{UploaderID: &u.ID, Path: res.Path, OriginalName: cleanFilename(name), MIME: res.MIME, Size: res.Size,
		Width: res.Width, Height: res.Height, Variants: res.Variants, SHA256: res.SHA256}
	if !media.MatchesName(res.MIME, m.OriginalName) {
		s.media.Remove(m)
		return nil, errors.New(s.t("This file type is not allowed."))
	}
	if existing, err := s.store.MediaBySHA(r.Context(), res.SHA256); err == nil && canManageMedia(u, existing) {
		s.media.Remove(m) // duplicate upload: reuse the stored file
		return existing, nil
	}
	if m.Variants == nil {
		m.Variants = map[string]store.MediaVariant{}
	}
	if m.ID, err = s.store.CreateMedia(r.Context(), m); err != nil {
		s.media.Remove(m)
		return nil, err
	}
	return m, nil
}

func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	if len(name) > 200 {
		name = name[:200]
	}
	return name
}

func mediaJSON(m *store.Media) map[string]any {
	out := map[string]any{"id": m.ID, "url": m.URL(), "name": m.OriginalName, "mime": m.MIME, "alt": m.Alt,
		"width": m.Width, "height": m.Height, "image": m.IsImage(), "size": m.Size}
	if m.IsImage() {
		out["thumb"] = m.VariantURL("thumb")
		out["medium"] = m.VariantURL("medium")
	}
	return out
}

func (s *Server) loadMedia(w http.ResponseWriter, r *http.Request) (*store.Media, bool) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	m, err := s.store.MediaByID(r.Context(), id)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	if !canManageMedia(currentUser(r), m) {
		s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
		return nil, false
	}
	return m, true
}

func (s *Server) adminMediaEdit(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	s.renderAdmin(w, r, "media_edit", m.OriginalName, "media", mediaData{Item: m})
}

func (s *Server) adminMediaUpdate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	alt, caption := trimmed(r, "alt"), trimmed(r, "caption")
	if tooLong(alt, 500) || tooLong(caption, 1000) {
		s.redirect(w, r, fmt.Sprintf("/admin/media/%d", m.ID), "error", s.t("The text is too long."))
		return
	}
	if err := s.store.UpdateMediaText(r.Context(), m.ID, alt, caption); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.content.Invalidate()
	s.redirect(w, r, fmt.Sprintf("/admin/media/%d", m.ID), "success", s.t("Saved."))
}

func (s *Server) adminMediaDelete(w http.ResponseWriter, r *http.Request) {
	m, ok := s.loadMedia(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMedia(r.Context(), m.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.media.Remove(m)
	s.audit(r, uid(r), "media.deleted", fmt.Sprintf("#%d %s", m.ID, m.OriginalName), nil)
	s.contentChanged()
	s.redirect(w, r, "/admin/media", "success", s.t("“%s” deleted.", m.OriginalName))
}

// apiMediaList feeds the media picker in the editor.
func (s *Server) apiMediaList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	page := listPage(r)
	const per = 30
	items, total, err := s.store.ListMedia(r.Context(), store.MediaQuery{UploaderID: mediaScope(u), ImagesOnly: r.URL.Query().Get("type") == "image",
		Search: strings.TrimSpace(r.URL.Query().Get("q")), Limit: per, Offset: (page - 1) * per})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "media"})
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, mediaJSON(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": total, "more": page*per < total})
}

// -------------------------------------------------------------- comments --

type commentsData struct {
	Tabs     []Tab
	Comments []store.Comment
	Status   string
	Counts   map[string]int
	Page     *Pagination
	Total    int
}

var commentStatuses = map[string]bool{"pending": true, "approved": true, "spam": true, "trash": true}

func (s *Server) adminComments(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if !commentStatuses[status] {
		status = ""
	}
	page := listPage(r)
	const per = 30
	q := store.CommentQuery{Status: status, Limit: per, Offset: (page - 1) * per}
	list, total, err := s.store.ListComments(r.Context(), q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	counts, err := s.store.CountCommentsByStatus(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	counts["all"] = counts["pending"] + counts["approved"]
	vals := r.URL.Query()
	vals.Del("page")
	tab := func(label, st string) Tab {
		n := counts[st]
		if st == "" {
			n = counts["all"]
		}
		return Tab{Label: label, URL: withQuery("/admin/comments", "status", st), Current: status == st, Count: n}
	}
	tabs := []Tab{tab(s.t("All"), ""), tab(s.t("Pending"), "pending"), tab(s.t("Approved"), "approved"), tab(s.t("Spam"), "spam"), tab(s.t("Trash"), "trash")}
	s.renderAdmin(w, r, "comments", s.t("Comments"), "comments", commentsData{Tabs: tabs, Comments: list, Status: status, Counts: counts, Total: total,
		Page: newPagination("/admin/comments", vals, page, per, total)})
}

// applyCommentAction performs a moderation action and reports whether a
// public page changed.
func (s *Server) applyCommentAction(r *http.Request, c *store.Comment, action string) error {
	ctx := r.Context()
	switch action {
	case "approve":
		if err := s.store.SetCommentStatus(ctx, c.ID, "approved"); err != nil {
			return err
		}
		if c.Status != "approved" {
			s.plugins.Emit("comment.approved", map[string]any{"id": c.ID, "post_id": c.PostID, "author": c.AuthorName})
		}
		return nil
	case "unapprove":
		return s.store.SetCommentStatus(ctx, c.ID, "pending")
	case "spam":
		return s.store.SetCommentStatus(ctx, c.ID, "spam")
	case "trash":
		return s.store.SetCommentStatus(ctx, c.ID, "trash")
	case "restore":
		return s.store.SetCommentStatus(ctx, c.ID, "pending")
	case "delete":
		return s.store.DeleteComment(ctx, c.ID)
	}
	return errors.New("unknown action")
}

func (s *Server) adminCommentAction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	c, err := s.store.CommentByID(r.Context(), id)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	action := r.PathValue("action")
	if err := s.applyCommentAction(r, c, action); err != nil {
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	s.audit(r, uid(r), "comment."+action, fmt.Sprintf("#%d %s", c.ID, c.AuthorName), nil)
	s.contentChanged()
	back := withQuery("/admin/comments", "status", r.FormValue("status"))
	s.redirect(w, r, back, "success", s.t("Comment updated."))
}

func (s *Server) adminCommentBulk(w http.ResponseWriter, r *http.Request) {
	action := r.FormValue("action")
	n := 0
	for _, id := range formIDs(r, "ids") {
		c, err := s.store.CommentByID(r.Context(), id)
		if err != nil {
			continue
		}
		if s.applyCommentAction(r, c, action) == nil {
			n++
		}
	}
	if n > 0 {
		s.audit(r, uid(r), "comment.bulk_"+action, fmt.Sprintf("%d items", n), nil)
		s.contentChanged()
	}
	s.redirect(w, r, withQuery("/admin/comments", "status", r.FormValue("status")), "success", s.t("%d items updated.", n))
}

func (s *Server) adminCommentEmpty(w http.ResponseWriter, r *http.Request) {
	status := r.PathValue("status")
	if status != "spam" && status != "trash" {
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	n, err := s.store.EmptyCommentStatus(r.Context(), status)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, uid(r), "comment.emptied_"+status, fmt.Sprintf("%d items", n), nil)
	s.redirect(w, r, "/admin/comments?status="+status, "success", s.t("%d items deleted permanently.", n))
}
