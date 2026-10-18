package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/icons"
	"sap13ns/internal/store"
)

// typeFromPath maps /admin/posts and /admin/pages to a post type.
func typeFromPath(path string) string {
	if strings.HasPrefix(path, "/admin/pages") {
		return store.TypePage
	}
	return store.TypePost
}

func listPath(typ string) string {
	if typ == store.TypePage {
		return "/admin/pages"
	}
	return "/admin/posts"
}

func canPublish(u *store.User, typ string) bool {
	if typ == store.TypePage {
		return auth.Can(u.Role, auth.CapManagePages)
	}
	return auth.Can(u.Role, auth.CapPublishOwnPosts)
}

// canModify applies the WordPress rule that contributors cannot edit a
// post once it has been published.
func canModify(u *store.User, p *store.Post) bool {
	if !canEditPost(u, p) {
		return false
	}
	if p.Type == store.TypePost && !auth.Can(u.Role, auth.CapPublishOwnPosts) {
		return p.Status == store.StatusDraft || p.Status == store.StatusPending
	}
	return true
}

type postListData struct {
	Tabs     []Tab
	Type     string
	Posts    []store.Post
	Status   string
	Query    string
	Counts   map[string]int
	Page     *Pagination
	Total    int
	ListPath string
	Now      time.Time
	Lang     string     // language filter
	Langs    []LangLink // site languages, when multilingual
}

func (s *Server) adminPostList(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	typ := typeFromPath(r.URL.Path)
	q := r.URL.Query()
	status := q.Get("status")
	search := strings.TrimSpace(q.Get("q"))
	page := listPage(r)
	const per = 20
	pq := store.PostQuery{Type: typ, Search: search, Limit: per, Offset: (page - 1) * per, WithTerms: true, Order: "updated"}
	lang := q.Get("lang")
	if slices.Contains(s.siteLangs(), lang) {
		pq.Lang = lang
	} else {
		lang = ""
	}
	switch status {
	case store.StatusDraft, store.StatusPending, store.StatusPublished, store.StatusPrivate, store.StatusTrash:
		pq.Statuses = []string{status}
	default:
		status = ""
	}
	var authorFilter int64
	if typ == store.TypePost && !auth.Can(u.Role, auth.CapEditOthersPosts) {
		authorFilter = u.ID
	}
	pq.AuthorID = authorFilter
	posts, total, err := s.store.ListPosts(r.Context(), pq)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	counts, err := s.store.CountPostsByStatus(r.Context(), typ, authorFilter)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	all := 0
	for st, n := range counts {
		if st != store.StatusTrash {
			all += n
		}
	}
	counts["all"] = all
	title, section := s.t("Posts"), "posts"
	if typ == store.TypePage {
		title, section = s.t("Pages"), "pages"
	}
	vals := r.URL.Query()
	vals.Del("page")
	lp := listPath(typ)
	tab := func(label, st string) Tab {
		n := counts[st]
		if st == "" {
			n = counts["all"]
		}
		return Tab{Label: label, URL: withQuery(lp, "status", st, "lang", lang), Current: status == st, Count: n}
	}
	tabs := []Tab{tab(s.t("All"), ""), tab(s.t("Published"), store.StatusPublished), tab(s.t("Drafts"), store.StatusDraft),
		tab(s.t("Pending review"), store.StatusPending), tab(s.t("Private"), store.StatusPrivate), tab(s.t("Trash"), store.StatusTrash)}
	s.renderAdmin(w, r, "posts", title, section, postListData{
		Tabs: tabs, Type: typ, Posts: posts, Status: status, Query: search, Counts: counts, Total: total, ListPath: listPath(typ),
		Page: newPagination(listPath(typ), vals, page, per, total), Now: time.Now(),
		Lang: lang, Langs: s.adminLangs(),
	})
}

type postEditData struct {
	Type       string
	Post       *store.Post
	New        bool
	Categories []store.Term
	Selected   []int64
	TagsText   string
	Pages      []store.Post
	Authors    []store.User
	Errors     []string
	CanPublish bool
	CanAuthor  bool
	Revisions  int
	ListPath   string
	PublicURL  string
	Scheduled  bool
	// Languages: the site languages (empty on single-language sites), this
	// post's translations, and the languages it can still be translated to.
	Langs        []LangLink
	Translations []store.Translation
	Missing      []LangLink
	Edit         editState
	Conflict     string // who changed the post while it was being edited
	AI           bool   // the AI assistant is available to this user
}

// adminLangs lists the site languages for admin forms; nil when the site has
// only one.
func (s *Server) adminLangs() []LangLink {
	langs := s.siteLangs()
	if len(langs) < 2 {
		return nil
	}
	out := make([]LangLink, len(langs))
	for i, l := range langs {
		out[i] = LangLink{Code: l, Name: langName(l)}
	}
	return out
}

func (s *Server) editData(r *http.Request, typ string, p *store.Post, isNew bool, errs []string) (*postEditData, error) {
	u := currentUser(r)
	ctx := r.Context()
	d := &postEditData{Type: typ, Post: p, New: isNew, Errors: errs, CanPublish: canPublish(u, typ),
		CanAuthor: auth.Can(u.Role, auth.CapEditOthersPosts), ListPath: listPath(typ), AI: s.aiAvailable(r)}
	var err error
	if typ == store.TypePost {
		if d.Categories, err = s.store.ListTerms(ctx, "category", ""); err != nil {
			return nil, err
		}
		var tags []string
		for _, t := range p.Terms {
			if t.Taxonomy == "category" {
				d.Selected = append(d.Selected, t.ID)
			} else {
				tags = append(tags, t.Name)
			}
		}
		d.TagsText = strings.Join(tags, ", ")
	} else {
		pages, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePage, Lang: p.Lang, Order: "title", Limit: 500,
			ExcludeIDs: []int64{p.ID}, Statuses: []string{store.StatusPublished, store.StatusDraft, store.StatusPrivate, store.StatusPending}})
		if err != nil {
			return nil, err
		}
		d.Pages = pages
	}
	if d.CanAuthor {
		users, _, err := s.store.ListUsers(ctx, store.UserListQuery{Limit: 500})
		if err != nil {
			return nil, err
		}
		for _, au := range users {
			if auth.Can(au.Role, auth.CapEditOwnPosts) {
				d.Authors = append(d.Authors, au)
			}
		}
	}
	if !isNew {
		revs, err := s.store.ListRevisions(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		d.Revisions = len(revs)
		d.PublicURL = s.postURL(p)
		d.Scheduled = p.IsScheduled(time.Now())
	}
	if d.Langs = s.adminLangs(); d.Langs != nil && !isNew {
		trs, err := s.store.Translations(ctx, p)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{p.Lang: true}
		for _, t := range trs {
			if t.ID != p.ID {
				d.Translations = append(d.Translations, t)
				have[t.Lang] = true
			}
		}
		for _, l := range d.Langs {
			if !have[l.Code] {
				d.Missing = append(d.Missing, l)
			}
		}
	}
	return d, nil
}

func (s *Server) adminPostNew(w http.ResponseWriter, r *http.Request) {
	typ := typeFromPath(r.URL.Path)
	u := currentUser(r)
	p := &store.Post{Type: typ, Status: store.StatusDraft, CommentStatus: "open", AuthorID: &u.ID, Lang: s.mainLang()}
	if l := r.URL.Query().Get("lang"); slices.Contains(s.siteLangs(), l) {
		p.Lang = l
	}
	if typ == store.TypePage {
		p.CommentStatus = "closed"
	}
	d, err := s.editData(r, typ, p, true, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	title := s.t("New post")
	if typ == store.TypePage {
		title = s.t("New page")
	}
	s.renderAdmin(w, r, "post_edit", title, sectionFor(typ), d)
}

func sectionFor(typ string) string {
	if typ == store.TypePage {
		return "pages"
	}
	return "posts"
}

// postFromForm validates the editor form. It returns the input and a list of
// translated validation errors.
func (s *Server) postFromForm(r *http.Request, typ string, existing *store.Post) (store.PostInput, []string, error) {
	u := currentUser(r)
	ctx := r.Context()
	var errs []string
	in := store.PostInput{
		Type:            typ,
		Lang:            s.mainLang(),
		Title:           trimmed(r, "title"),
		Excerpt:         trimmed(r, "excerpt"),
		Content:         strings.ReplaceAll(r.FormValue("content"), "\r\n", "\n"),
		MetaDescription: trimmed(r, "meta_description"),
		FeaturedMediaID: formInt64Ptr(r, "featured_media_id"),
		CommentStatus:   "closed",
		AuthorID:        u.ID,
		// Hidden from the site assistant and MCP (assistant.go).
		AssistantExclude: r.FormValue("assistant_exclude") == "1",
	}
	if r.FormValue("comments_open") == "1" {
		in.CommentStatus = "open"
	}
	if existing != nil {
		in.Lang = existing.Lang
	}
	if l := r.FormValue("lang"); l != "" && l != in.Lang {
		switch {
		case !slices.Contains(s.siteLangs(), l):
			errs = append(errs, s.t("That language is not enabled on this site."))
		case existing != nil && s.translationTaken(ctx, existing, l):
			errs = append(errs, s.t("This content already has a translation in that language."))
		default:
			in.Lang = l
		}
	}
	if existing != nil && existing.AuthorID != nil {
		in.AuthorID = *existing.AuthorID
	}
	if auth.Can(u.Role, auth.CapEditOthersPosts) {
		if a := formInt64Ptr(r, "author_id"); a != nil {
			if au, err := s.store.UserByID(ctx, *a); err == nil && auth.Can(au.Role, auth.CapEditOwnPosts) {
				in.AuthorID = au.ID
			}
		}
	}
	if in.Title == "" {
		errs = append(errs, s.t("The title is required."))
	}
	if tooLong(in.Title, 300) {
		errs = append(errs, s.t("The title is too long (300 characters max)."))
	}
	if tooLong(in.Excerpt, 2000) {
		errs = append(errs, s.t("The excerpt is too long (2000 characters max)."))
	}
	if tooLong(in.MetaDescription, 300) {
		errs = append(errs, s.t("The meta description is too long (300 characters max)."))
	}
	if len(in.Content) > 2_000_000 {
		errs = append(errs, s.t("The content is too long."))
	}

	// Status from the button that was pressed.
	intent := r.FormValue("intent")
	status := r.FormValue("status")
	switch intent {
	case "publish":
		status = store.StatusPublished
	case "draft":
		status = store.StatusDraft
	case "pending":
		status = store.StatusPending
	}
	switch status {
	case store.StatusDraft, store.StatusPending, store.StatusPublished, store.StatusPrivate:
	default:
		status = store.StatusDraft
	}
	if (status == store.StatusPublished || status == store.StatusPrivate) && !canPublish(u, typ) {
		status = store.StatusPending
	}
	in.Status = status

	pub, err := s.parseLocal(r.FormValue("published_at"))
	if err != nil {
		errs = append(errs, s.t("The publication date is not valid."))
	}
	in.PublishedAt = pub
	if in.PublishedAt == nil && existing != nil {
		in.PublishedAt = existing.PublishedAt
	}
	if in.PublishedAt == nil && (status == store.StatusPublished || status == store.StatusPrivate) {
		now := time.Now()
		in.PublishedAt = &now
	}

	exceptID := int64(0)
	if existing != nil {
		exceptID = existing.ID
	}
	slugIn := trimmed(r, "slug")
	if typ == store.TypePage && reservedSlugs[content.Slugify(slugIn)] {
		errs = append(errs, s.t("That address is reserved by the system. Choose another one."))
	}
	in.Slug, err = s.uniqueSlug(ctx, typ, in.Lang, slugIn, in.Title, exceptID)
	if err != nil {
		return in, errs, err
	}

	if in.FeaturedMediaID != nil {
		if m, err := s.store.MediaByID(ctx, *in.FeaturedMediaID); err != nil || !m.IsImage() {
			in.FeaturedMediaID = nil
		}
	}
	if typ == store.TypePost {
		in.TermIDs = formIDs(r, "categories")
		var names, slugs []string
		seen := map[string]bool{}
		for _, raw := range strings.Split(r.FormValue("tags"), ",") {
			name := strings.TrimSpace(raw)
			slug := content.Slugify(name)
			if name == "" || slug == "" || seen[slug] || tooLong(name, 100) {
				continue
			}
			seen[slug] = true
			names, slugs = append(names, name), append(slugs, slug)
			if len(names) >= 30 {
				break
			}
		}
		if len(errs) == 0 && len(names) > 0 {
			ids, err := s.store.EnsureTags(ctx, names, slugs)
			if err != nil {
				return in, errs, err
			}
			in.TermIDs = append(in.TermIDs, ids...)
		}
	} else {
		in.ParentID = formInt64Ptr(r, "parent_id")
		if in.ParentID != nil && existing != nil && *in.ParentID == existing.ID {
			in.ParentID = nil
		}
		in.MenuOrder = formInt(r, "menu_order", 0)
	}
	return in, errs, nil
}

// repopulate rebuilds a post from rejected form input so the editor keeps it.
func repopulate(base *store.Post, in store.PostInput) *store.Post {
	p := *base
	p.Title, p.Slug, p.Excerpt, p.Content, p.Status = in.Title, in.Slug, in.Excerpt, in.Content, in.Status
	p.MetaDescription, p.CommentStatus, p.PublishedAt, p.FeaturedMediaID = in.MetaDescription, in.CommentStatus, in.PublishedAt, in.FeaturedMediaID
	p.ParentID, p.MenuOrder, p.Lang = in.ParentID, in.MenuOrder, in.Lang
	p.AssistantExclude = in.AssistantExclude
	return &p
}

// translationTaken reports whether p's translation group already has lang.
func (s *Server) translationTaken(ctx context.Context, p *store.Post, lang string) bool {
	trs, err := s.store.Translations(ctx, p)
	if err != nil {
		return true
	}
	for _, t := range trs {
		if t.Lang == lang && t.ID != p.ID {
			return true
		}
	}
	return false
}

// adminPostTranslate creates a draft copy of a post in another language,
// linked to it as a translation, and opens it in the editor.
func (s *Server) adminPostTranslate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	ctx := r.Context()
	lang := r.FormValue("lang")
	if lang == p.Lang || !slices.Contains(s.siteLangs(), lang) {
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	id, err := s.createTranslation(ctx, u, p, lang, p.Slug, p.Title, p.Excerpt, p.Content)
	if errors.Is(err, store.ErrTranslationExists) {
		s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", p.ID), "error", s.t("This content already has a translation in that language."))
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, p.Type+".translated", fmt.Sprintf("#%d → #%d (%s)", p.ID, id, lang), nil)
	s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", id), "success",
		s.t("Draft created in %s. Translate the text and publish it when ready.", langName(lang)))
}

// adminPostUnlink removes a post from its translation group.
func (s *Server) adminPostUnlink(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	if err := s.store.UnlinkTranslation(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.contentChanged()
	s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", p.ID), "success", s.t("No longer linked to its translations."))
}

func (s *Server) adminPostCreate(w http.ResponseWriter, r *http.Request) {
	typ := typeFromPath(r.URL.Path)
	u := currentUser(r)
	in, errs, err := s.postFromForm(r, typ, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(errs) > 0 {
		d, err := s.editData(r, typ, repopulate(&store.Post{Type: typ, AuthorID: &u.ID}, in), true, errs)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "post_edit", s.t("New post"), sectionFor(typ), d)
		return
	}
	id, err := s.store.CreatePost(r.Context(), in)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, typ+".created", fmt.Sprintf("#%d %s", id, in.Title), map[string]string{"status": in.Status})
	s.afterPostSave(r, id, "", in.Status)
	s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", id), "success", s.savedMessage(in.Status, in.PublishedAt))
}

func (s *Server) savedMessage(status string, at *time.Time) string {
	switch {
	case status == store.StatusPublished && at != nil && at.After(time.Now()):
		return s.t("Scheduled for %s.", s.dateTime(*at))
	case status == store.StatusPublished:
		return s.t("Published.")
	case status == store.StatusPending:
		return s.t("Submitted for review.")
	default:
		return s.t("Saved.")
	}
}

// afterPostSave invalidates caches and notifies plugins and WebSub. A post
// is announced (post.published) when it goes live: on this save, or for a
// scheduled post when its date arrives (announceDue). Plugins hear nothing
// about a scheduled post before that.
func (s *Server) afterPostSave(r *http.Request, id int64, oldStatus, newStatus string) {
	ctx := r.Context()
	s.contentChanged()
	s.assistantPostChanged(ctx, id)
	p, err := s.store.PostByID(ctx, id)
	if err != nil {
		return
	}
	pending := func(on bool) {
		if err := s.store.SetAnnouncePending(ctx, id, on); err != nil {
			s.log.Error("post announcement", "post", id, "err", err)
		}
	}
	switch {
	case newStatus != store.StatusPublished:
		pending(false)
		if newStatus == store.StatusTrash {
			s.plugins.Emit("post.trashed", s.PostInfo(p))
		}
	case p.IsScheduled(time.Now()):
		pending(true)
	default:
		// Live: announced if it was not published before, or was scheduled.
		claimed, err := s.store.ClaimAnnouncement(ctx, id)
		if err != nil {
			s.log.Error("post announcement", "post", id, "err", err)
		}
		if oldStatus != store.StatusPublished || claimed {
			s.announce(p)
		} else {
			s.plugins.Emit("post.updated", s.PostInfo(p))
		}
	}
}

// announce tells plugins and WebSub that a post went live.
func (s *Server) announce(p *store.Post) {
	s.plugins.Emit("post.published", s.PostInfo(p))
	if p.Type == store.TypePost {
		s.pingWebSub()
	}
}

// announceDue announces the scheduled posts whose date has arrived. It runs
// every minute; the claim is atomic, so each post is announced once.
func (s *Server) announceDue(ctx context.Context) {
	ids, err := s.store.ClaimDueAnnouncements(ctx)
	if err != nil {
		s.log.Error("scheduled posts", "err", err)
		return
	}
	if len(ids) == 0 {
		return
	}
	s.contentChanged()
	ping := false
	for _, id := range ids {
		if p, err := s.store.PostByID(ctx, id); err == nil {
			s.plugins.Emit("post.published", s.PostInfo(p))
			ping = ping || p.Type == store.TypePost
		}
	}
	if ping {
		s.pingWebSub()
	}
}

func (s *Server) loadEditable(w http.ResponseWriter, r *http.Request) (*store.Post, bool) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	p, err := s.store.PostByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return nil, false
	}
	if err != nil {
		s.serverError(w, r, err)
		return nil, false
	}
	if !canEditPost(currentUser(r), p) {
		s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
		return nil, false
	}
	return p, true
}

func (s *Server) adminPostEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	d, err := s.editData(r, p.Type, p, false, nil)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Edit = s.loadEditState(r, p)
	s.renderAdmin(w, r, "post_edit", p.Title, sectionFor(p.Type), d)
}

func (s *Server) adminPostUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if !canModify(u, p) {
		s.adminError(w, r, http.StatusForbidden, s.t("Published posts can only be edited by authors and editors."))
		return
	}
	in, errs, err := s.postFromForm(r, p.Type, p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	conflictBy, conflict := s.editConflict(r, p)
	if conflict && r.FormValue("overwrite") != "1" {
		errs = append(errs, s.t("%s changed this post while you were editing it. Your version is below: save again to replace theirs, or open the revisions to compare.", conflictBy))
	}
	if len(errs) > 0 {
		d, err := s.editData(r, p.Type, repopulate(p, in), false, errs)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		d.Edit.Version = r.FormValue("version")
		if conflict {
			d.Edit.Version, d.Conflict = strconv.FormatInt(p.UpdatedAt.UnixMicro(), 10), conflictBy
		}
		s.renderAdminStatus(w, r, http.StatusUnprocessableEntity, "post_edit", p.Title, sectionFor(p.Type), d)
		return
	}
	if err := s.store.UpdatePost(r.Context(), p.ID, in, u.ID, 25); err != nil {
		s.serverError(w, r, err)
		return
	}
	moved := *p
	moved.Slug, moved.Lang = in.Slug, in.Lang
	s.postMoved(r.Context(), p, &moved)
	s.store.DeleteAutosave(r.Context(), p.ID, u.ID)
	s.audit(r, u.ID, p.Type+".updated", fmt.Sprintf("#%d %s", p.ID, in.Title), map[string]string{"status": in.Status})
	s.afterPostSave(r, p.ID, p.Status, in.Status)
	s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", p.ID), "success", s.savedMessage(in.Status, in.PublishedAt))
}

// adminPostStatus moves a post to the trash or restores it as a draft.
func (s *Server) adminPostStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	to := r.FormValue("to")
	switch to {
	case store.StatusTrash:
		if !canModify(u, p) && !auth.Can(u.Role, auth.CapEditOthersPosts) {
			s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
			return
		}
	case store.StatusDraft:
	default:
		s.adminError(w, r, http.StatusBadRequest, s.t("Invalid request."))
		return
	}
	if err := s.store.SetPostStatus(r.Context(), p.ID, to); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, p.Type+"."+map[string]string{store.StatusTrash: "trashed", store.StatusDraft: "restored"}[to], fmt.Sprintf("#%d %s", p.ID, p.Title), nil)
	s.afterPostSave(r, p.ID, p.Status, to)
	msg := s.t("Moved to the trash.")
	if to == store.StatusDraft {
		msg = s.t("Restored as a draft.")
	}
	s.redirect(w, r, listPath(p.Type)+"?status="+map[string]string{store.StatusTrash: "", store.StatusDraft: "draft"}[to], "success", msg)
}

func (s *Server) adminPostDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if p.Status != store.StatusTrash || !(auth.Can(u.Role, auth.CapEditOthersPosts) || auth.Can(u.Role, auth.CapPublishOwnPosts)) {
		s.adminError(w, r, http.StatusForbidden, s.t("Only posts in the trash can be deleted permanently."))
		return
	}
	if err := s.store.DeletePost(r.Context(), p.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, p.Type+".deleted", fmt.Sprintf("#%d %s", p.ID, p.Title), nil)
	s.contentChanged()
	s.redirect(w, r, listPath(p.Type)+"?status=trash", "success", s.t("Deleted permanently."))
}

func (s *Server) adminPostBulk(w http.ResponseWriter, r *http.Request) {
	typ := typeFromPath(r.URL.Path)
	u := currentUser(r)
	action := r.FormValue("action")
	ids := formIDs(r, "ids")
	n := 0
	for _, id := range ids {
		p, err := s.store.PostByID(r.Context(), id)
		if err != nil || p.Type != typ || !canEditPost(u, p) {
			continue
		}
		var err2 error
		switch action {
		case "trash":
			if !canModify(u, p) && !auth.Can(u.Role, auth.CapEditOthersPosts) {
				continue
			}
			err2 = s.store.SetPostStatus(r.Context(), id, store.StatusTrash)
		case "restore":
			err2 = s.store.SetPostStatus(r.Context(), id, store.StatusDraft)
		case "publish":
			if !canPublish(u, typ) || p.Status == store.StatusTrash {
				continue
			}
			in := postInputOf(p)
			in.Status = store.StatusPublished
			if in.PublishedAt == nil {
				now := time.Now()
				in.PublishedAt = &now
			}
			err2 = s.store.UpdatePost(r.Context(), id, in, u.ID, 25)
			if err2 == nil {
				s.afterPostSave(r, id, p.Status, store.StatusPublished)
			}
		case "delete":
			if p.Status != store.StatusTrash {
				continue
			}
			err2 = s.store.DeletePost(r.Context(), id)
		default:
			continue
		}
		if err2 == nil {
			n++
		}
	}
	if n > 0 {
		s.audit(r, u.ID, typ+".bulk_"+action, fmt.Sprintf("%d items", n), nil)
		s.contentChanged()
	}
	s.redirect(w, r, withQuery(listPath(typ), "status", r.FormValue("status")), "success", s.t("%d items updated.", n))
}

func postInputOf(p *store.Post) store.PostInput {
	in := store.PostInput{Type: p.Type, Lang: p.Lang, Status: p.Status, Title: p.Title, Slug: p.Slug, Excerpt: p.Excerpt, Content: p.Content,
		ParentID: p.ParentID, MenuOrder: p.MenuOrder, FeaturedMediaID: p.FeaturedMediaID, CommentStatus: p.CommentStatus,
		MetaDescription: p.MetaDescription, PublishedAt: p.PublishedAt, AssistantExclude: p.AssistantExclude}
	if p.AuthorID != nil {
		in.AuthorID = *p.AuthorID
	}
	for _, t := range p.Terms {
		in.TermIDs = append(in.TermIDs, t.ID)
	}
	return in
}

func (s *Server) adminEmptyTrash(w http.ResponseWriter, r *http.Request) {
	typ := typeFromPath(r.URL.Path)
	u := currentUser(r)
	if typ == store.TypePost && !auth.Can(u.Role, auth.CapEditOthersPosts) {
		s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
		return
	}
	n, err := s.store.EmptyTrash(r.Context(), typ)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, typ+".trash_emptied", fmt.Sprintf("%d items", n), nil)
	s.contentChanged()
	s.redirect(w, r, listPath(typ)+"?status=trash", "success", s.t("%d items deleted permanently.", n))
}

func (s *Server) adminPostPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	s.renderSingle(w, r, p, false, true)
}

type revisionsData struct {
	Post      *store.Post
	Revisions []store.Revision
}

func (s *Server) adminRevisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.loadEditable(w, r)
	if !ok {
		return
	}
	revs, err := s.store.ListRevisions(r.Context(), p.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdmin(w, r, "revisions", s.t("Revisions")+" · "+p.Title, sectionFor(p.Type), revisionsData{Post: p, Revisions: revs})
}

func (s *Server) adminRevisionRestore(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	rev, err := s.store.RevisionByID(r.Context(), id)
	if err != nil {
		s.adminError(w, r, http.StatusNotFound, s.t("Not found."))
		return
	}
	p, err := s.store.PostByID(r.Context(), rev.PostID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	u := currentUser(r)
	if !canModify(u, p) {
		s.adminError(w, r, http.StatusForbidden, s.t("You do not have permission to access this page."))
		return
	}
	in := postInputOf(p)
	in.Title, in.Excerpt, in.Content = rev.Title, rev.Excerpt, rev.Content
	if err := s.store.UpdatePost(r.Context(), p.ID, in, u.ID, 25); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.audit(r, u.ID, p.Type+".revision_restored", fmt.Sprintf("#%d %s", p.ID, p.Title), map[string]string{"revision": fmt.Sprint(rev.ID)})
	s.afterPostSave(r, p.ID, p.Status, p.Status)
	s.redirect(w, r, fmt.Sprintf("/admin/posts/%d", p.ID), "success", s.t("Revision restored."))
}

// apiPreview renders Markdown exactly as the public site would.
func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Content) > 2_000_000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	iconFn := func(name string) string { return icons.SVG(name, "") }
	html := content.Render(req.Content, s.plugins.HasShortcode, func(sc content.Shortcode) (string, error) {
		return s.plugins.Shortcode(r.Context(), sc.Name, sc.Attrs, sc.Inner, nil)
	}, iconFn)
	writeJSON(w, http.StatusOK, map[string]any{"html": html, "words": content.WordCount(content.PlainText(html)),
		"minutes": content.ReadingMinutes(req.Content)})
}

func (s *Server) apiSlug(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title string `json:"title"`
		Type  string `json:"type"`
		Lang  string `json:"lang"`
		ID    int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Title) > 400 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Type != store.TypePage {
		req.Type = store.TypePost
	}
	if !slices.Contains(s.siteLangs(), req.Lang) {
		req.Lang = s.mainLang()
	}
	slug, err := s.uniqueSlug(r.Context(), req.Type, req.Lang, "", req.Title, req.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "slug"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug})
}
