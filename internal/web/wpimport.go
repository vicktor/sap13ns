package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/content"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
	"sap13ns/internal/wpimport"
)

// WordPress import. The WXR file is parsed while it uploads (it is never
// written to disk); the import itself runs in the background, one at a
// time, and the tools page shows its progress and final report.

const wxrMaxBytes = 256 << 20

type importJob struct {
	mu       sync.Mutex
	running  bool
	started  time.Time
	finished time.Time
	user     string
	file     string
	report   *wpimport.Report
	err      string
}

type importStatus struct {
	Running          bool
	Started          time.Time
	Finished         time.Time
	User             string
	File             string
	Summary          string
	Warnings         []string
	Dropped          int
	DryRun           bool
	Error            string
	Redirects        []importRedirect
	RedirectsDropped int
}

// importRedirect is a line of the import report about one redirect.
type importRedirect struct {
	wpimport.RedirectResult
	OriginLabel string
	Result      string
}

func (s *Server) importStatus() importStatus {
	j := &s.imports
	j.mu.Lock()
	defer j.mu.Unlock()
	st := importStatus{Running: j.running, Started: j.started, Finished: j.finished, User: j.user, File: j.file, Error: j.err}
	if rep := j.report; rep != nil {
		st.Summary, st.Warnings, st.Dropped, st.DryRun = rep.Summary(), rep.Warnings, rep.WarningsDropped, rep.DryRun
		st.RedirectsDropped = rep.RedirectsDropped
		for _, r := range rep.Redirects {
			st.Redirects = append(st.Redirects, importRedirect{RedirectResult: r, OriginLabel: s.redirectOrigin(r.Origin),
				Result: s.redirectResult(r.Reason, rep.DryRun)})
		}
	}
	return st
}

func (s *Server) redirectOrigin(origin string) string {
	switch origin {
	case wpimport.RedirectRule:
		return s.t("Rule of the export")
	case wpimport.RedirectFile:
		return s.t("Old address of a file")
	}
	return s.t("Old address of a post or page")
}

func (s *Server) redirectResult(reason string, dryRun bool) string {
	switch reason {
	case "":
		if dryRun {
			return s.t("Would be imported")
		}
		return s.t("Imported")
	case wpimport.SkipDisabled:
		return s.t("Not imported: the option was off")
	case wpimport.SkipSource:
		return s.t("Skipped: the source is not a path of this site")
	case wpimport.SkipStatus:
		return s.t("Skipped: unknown redirect type")
	case wpimport.SkipTarget:
		return s.t("Skipped: no usable target")
	case wpimport.SkipOffSite:
		return s.t("Skipped: the target is on another site")
	case wpimport.SkipDuplicate:
		return s.t("Skipped: an earlier redirect of the import has this source")
	case wpimport.SkipReserved:
		return s.t("Skipped: the site serves this address itself")
	case wpimport.SkipContent:
		return s.t("Skipped: a post or page has this address")
	case wpimport.SkipExists:
		return s.t("Skipped: the site already has a redirect for this address")
	case wpimport.SkipInvalid:
		return s.t("Skipped: the rule is not valid")
	}
	return s.t("Failed: see the warnings")
}

type importData struct {
	Status  importStatus
	Authors []store.User
	Langs   []LangLink
	Error   string
}

func (s *Server) adminImport(w http.ResponseWriter, r *http.Request) {
	s.renderImport(w, r, http.StatusOK, "")
}

func (s *Server) renderImport(w http.ResponseWriter, r *http.Request, status int, msg string) {
	authors, err := s.authorChoices(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderAdminStatus(w, r, status, "import", s.t("Import from WordPress"), "import",
		importData{Status: s.importStatus(), Authors: authors, Langs: s.adminLangs(), Error: msg})
}

func (s *Server) authorChoices(r *http.Request) ([]store.User, error) {
	list, _, err := s.store.ListUsers(r.Context(), store.UserListQuery{Limit: 500})
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, u := range list {
		if auth.Can(u.Role, auth.CapEditOwnPosts) {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Server) adminImportStart(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, wxrMaxBytes+1<<20)
	file, hdr, err := r.FormFile("wxr")
	if err != nil {
		s.renderImport(w, r, http.StatusBadRequest, s.t("Choose the export file (.xml) from Tools → Export in WordPress."))
		return
	}
	defer file.Close()
	exp, err := wpimport.Parse(wpimport.LimitReader(file, wxrMaxBytes))
	if err != nil {
		msg := s.t("This is not a valid WordPress export file.")
		if errors.Is(err, wpimport.ErrTooLarge) {
			msg = s.t("The file is too large (%s max).", filesize(wxrMaxBytes))
		}
		s.log.Warn("wxr parse", "err", err)
		s.renderImport(w, r, http.StatusUnprocessableEntity, msg)
		return
	}
	authorID := uid(r)
	if id := formInt64Ptr(r, "author_id"); id != nil {
		authorID = *id
	}
	if au, err := s.store.UserByID(r.Context(), authorID); err != nil || !auth.Can(au.Role, auth.CapEditOwnPosts) {
		s.renderImport(w, r, http.StatusUnprocessableEntity, s.t("Choose an author for the imported content."))
		return
	}
	lang := r.FormValue("lang")
	if !slices.Contains(s.siteLangs(), lang) {
		lang = s.mainLang()
	}
	opts := wpimport.Options{DryRun: r.FormValue("dry_run") == "1", DownloadMedia: r.FormValue("media") == "1",
		SkipExisting: r.FormValue("skip_existing") == "1", DefaultAuthorID: authorID, RedirectRules: r.FormValue("redirect_rules") == "1",
		MaxMediaBytes: s.cfg.MaxUploadBytes, Language: lang, Languages: s.siteLangs(),
		PostURL: func(typ, l, slug string) string { return s.langPath(cmp.Or(l, lang), postPath(typ, slug)) }}
	for _, h := range strings.Fields(strings.ReplaceAll(r.FormValue("media_hosts"), ",", " ")) {
		opts.MediaHosts = append(opts.MediaHosts, strings.ToLower(h))
	}

	j := &s.imports
	j.mu.Lock()
	if j.running {
		j.mu.Unlock()
		s.renderImport(w, r, http.StatusConflict, s.t("An import is already running."))
		return
	}
	u := currentUser(r)
	j.running, j.started, j.finished, j.user, j.file, j.report, j.err = true, time.Now(), time.Time{}, u.Name(), hdr.Filename, nil, ""
	j.mu.Unlock()
	s.audit(r, u.ID, "import.started", hdr.Filename, map[string]string{"dry_run": fmt.Sprint(opts.DryRun),
		"redirect_rules": fmt.Sprint(opts.RedirectRules)})

	sink := &wpSink{s: s, lang: lang, uploader: u.ID}
	s.goJob(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Hour)
		defer cancel()
		rep, err := wpimport.Import(ctx, exp, sink, opts)
		j.mu.Lock()
		j.running, j.finished, j.report = false, time.Now(), rep
		if err != nil {
			j.err = err.Error()
		}
		j.mu.Unlock()
		if !opts.DryRun {
			s.contentChanged()
			s.redirects.reset()
		}
		s.log.Info("wordpress import finished", "dry_run", opts.DryRun, "err", err)
	})
	s.redirect(w, r, "/admin/tools/import", "success", s.t("Import started."))
}

// wpSink stores imported content through the store.
type wpSink struct {
	s        *Server
	lang     string
	uploader int64
	existing map[string]bool // the sources of the site's redirects, by sourceKey; loaded once
}

var nonUsername = regexp.MustCompile(`[^a-z0-9_.-]+`)

func (k *wpSink) EnsureUser(ctx context.Context, a wpimport.Author) (int64, error) {
	st := k.s.store
	if a.Email != "" {
		if u, err := st.UserByEmail(ctx, strings.ToLower(a.Email)); err == nil {
			return u.ID, nil
		}
	}
	name := strings.Trim(nonUsername.ReplaceAllString(strings.ToLower(content.Slugify(a.Login)), ""), "._-")
	for len(name) < 3 {
		name += "x"
	}
	if len(name) > 28 {
		name = name[:28]
	}
	if u, err := st.UserByUsername(ctx, name); err == nil {
		if a.Email == "" || strings.EqualFold(u.Email, a.Email) {
			return u.ID, nil
		}
		name = fmt.Sprintf("%s-wp%d", name, a.ID%1000)
	}
	email := strings.ToLower(a.Email)
	if email == "" || !strings.Contains(email, "@") {
		email = name + "@import.invalid"
	}
	display := strings.TrimSpace(a.DisplayName)
	if display == "" {
		display = strings.TrimSpace(a.FirstName + " " + a.LastName)
	}
	// No password: the account can only be used after a password reset.
	return st.CreateUser(ctx, store.NewUser{Username: name, Email: email, DisplayName: display, Role: auth.RoleContributor})
}

func (k *wpSink) EnsureTerm(ctx context.Context, taxonomy string, t wpimport.Term, parentID *int64) (int64, error) {
	if term, err := k.s.store.TermBySlug(ctx, taxonomy, t.Slug); err == nil {
		return term.ID, nil
	}
	if taxonomy != wpimport.TaxonomyCategory {
		parentID = nil
	}
	var i18n map[string]store.TermText
	for lang, tt := range t.I18n {
		if i18n == nil {
			i18n = map[string]store.TermText{}
		}
		i18n[lang] = store.TermText{Name: tt.Name, Description: tt.Description}
	}
	return k.s.store.CreateTerm(ctx, store.TermInput{Taxonomy: taxonomy, Name: t.Name, Slug: t.Slug, Description: t.Description,
		ParentID: parentID, I18n: i18n})
}

func (k *wpSink) PostExists(ctx context.Context, typ, lang, slug string) (bool, error) {
	if typ == store.TypePage && (reservedSlugs[slug] || validLang(slug)) {
		return true, nil
	}
	return k.s.store.SlugExists(ctx, typ, cmp.Or(lang, k.lang), slug, 0)
}

func (k *wpSink) CreatePost(ctx context.Context, p wpimport.Post) (int64, error) {
	in := store.PostInput{Type: p.Type, Status: p.Status, Title: p.Title, Slug: p.Slug, Lang: cmp.Or(p.Lang, k.lang),
		TranslationOf: p.TranslationOf, Excerpt: p.Excerpt, Content: p.Content, AuthorID: p.AuthorID, ParentID: p.ParentID,
		MenuOrder: p.MenuOrder, FeaturedMediaID: p.FeaturedMediaID, CommentStatus: p.CommentStatus,
		MetaDescription: p.MetaDescription, PublishedAt: p.PublishedAt, TermIDs: p.TermIDs, AssistantExclude: p.AssistantExclude}
	id, err := k.s.store.CreatePost(ctx, in)
	if errors.Is(err, store.ErrTranslationExists) {
		in.TranslationOf = 0 // the importer prevents this; never lose the post over it
		id, err = k.s.store.CreatePost(ctx, in)
	}
	return id, err
}

func (k *wpSink) CreateComment(ctx context.Context, postID int64, c wpimport.Comment, parentID *int64) (int64, error) {
	ip, _ := netip.ParseAddr(c.AuthorIP)
	date := c.Date
	return k.s.store.CreateComment(ctx, store.NewComment{PostID: postID, ParentID: parentID, UserID: c.NewUserID,
		AuthorName: c.Author, AuthorEmail: c.AuthorEmail, AuthorURL: c.AuthorURL, Content: c.Text, Status: c.Status,
		IP: ip, CreatedAt: &date})
}

func (k *wpSink) StoreMedia(ctx context.Context, name string, r io.Reader, alt string) (int64, string, error) {
	// Over the site's storage quota, each file fails with media.ErrQuota
	// and the import report lists it.
	res, err := k.s.media.Save(ctx, r)
	if err != nil {
		if errors.Is(err, media.ErrTooLarge) {
			return 0, "", wpimport.ErrMediaTooLarge
		}
		return 0, "", err
	}
	m := &store.Media{UploaderID: &k.uploader, Path: res.Path, OriginalName: cleanFilename(name), MIME: res.MIME, Size: res.Size,
		Width: res.Width, Height: res.Height, Variants: res.Variants, SHA256: res.SHA256, Alt: alt}
	if !media.MatchesName(res.MIME, m.OriginalName) {
		k.s.media.Remove(ctx, m)
		return 0, "", errors.New("file type does not match its name")
	}
	if existing, err := k.s.store.MediaBySHA(ctx, res.SHA256); err == nil {
		k.s.media.Remove(ctx, m) // already imported
		return existing.ID, existing.URL(), nil
	}
	if m.Variants == nil {
		m.Variants = map[string]store.MediaVariant{}
	}
	if m.ID, err = k.s.store.CreateMedia(ctx, m); err != nil {
		k.s.media.Remove(ctx, m)
		return 0, "", err
	}
	return m.ID, m.URL(), nil
}

// AddRedirect adds an imported redirect, never over an existing one.
func (k *wpSink) AddRedirect(ctx context.Context, from, to string, status int) error {
	src, dst, err := k.checkRedirect(ctx, from, to, status)
	if err != nil {
		return err
	}
	added, err := k.s.store.AddRedirect(ctx, src, dst, status, true)
	switch {
	case err != nil:
		return err
	case !added:
		return wpimport.ErrRedirectExists
	}
	k.existing[sourceKey(src)] = true
	return nil
}

func (k *wpSink) CheckRedirect(ctx context.Context, from, to string, status int) error {
	_, _, err := k.checkRedirect(ctx, from, to, status)
	return err
}

// checkRedirect applies the rules of the redirects page, and more: an
// imported redirect stays on the site, and takes neither the address of a
// post or page nor that of a redirect the site has (with or without its
// trailing slash).
func (k *wpSink) checkRedirect(ctx context.Context, from, to string, status int) (string, string, error) {
	src, dst, err := checkRedirect(from, to, status)
	switch {
	case errors.Is(err, errRedirectReserved):
		return "", "", wpimport.ErrRedirectReserved
	case err != nil:
		return "", "", wpimport.ErrRedirectInvalid
	case dst != "" && !localRedirectTarget(dst):
		return "", "", wpimport.ErrRedirectOffSite
	}
	key := sourceKey(src)
	if taken, err := k.s.contentAt(ctx, src); err != nil {
		return "", "", err
	} else if taken && sourceKey(dst) != key {
		return "", "", wpimport.ErrRedirectContent
	}
	if k.existing == nil {
		list, err := k.s.store.Redirects(ctx)
		if err != nil {
			return "", "", err
		}
		k.existing = make(map[string]bool, len(list))
		for _, r := range list {
			k.existing[sourceKey(r.Source)] = true
		}
	}
	if k.existing[key] {
		return "", "", wpimport.ErrRedirectExists
	}
	return src, dst, nil
}
