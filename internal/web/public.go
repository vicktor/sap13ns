package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/cache"
	"sap13ns/internal/content"
	"sap13ns/internal/i18n"
	"sap13ns/internal/icons"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
)

// --------------------------------------------------------------- caching --

// bufferWriter captures a response so it can be cached.
type bufferWriter struct {
	header http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufferWriter) Header() http.Header         { return b.header }
func (b *bufferWriter) WriteHeader(code int)        { b.status = code }
func (b *bufferWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }

var cacheableParams = map[string]bool{"page": true, "q": true}

// commentTokenSlot marks where an anonymous page needs a fresh comment-form
// token. Cached pages keep the slot, and every response fills it in, so the
// "submitted too fast" check measures this visitor's time on the page.
const commentTokenSlot = "sap-ctoken-"

var commentTokenRE = regexp.MustCompile(commentTokenSlot + `(\d+)`)

func (s *Server) commentToken(r *http.Request, postID int64) string {
	if rctx(r).Session == nil {
		return commentTokenSlot + strconv.FormatInt(postID, 10)
	}
	return s.keys.Sign("comment", strconv.FormatInt(postID, 10), time.Now())
}

// fillTokens returns e with fresh comment tokens, or e itself when the page
// has none. Filled pages get no ETag: a 304 would revive an old token.
func (s *Server) fillTokens(e *cache.Entry) *cache.Entry {
	if !bytes.Contains(e.Body, []byte(commentTokenSlot)) && !bytes.Contains(e.Body, []byte(formTokenSlot)) {
		return e
	}
	now := time.Now()
	body := commentTokenRE.ReplaceAllFunc(e.Body, func(m []byte) []byte {
		return []byte(s.keys.Sign("comment", string(m[len(commentTokenSlot):]), now))
	})
	body = s.fillFormTokens(body) // plugin forms (plugin_web.go)
	return &cache.Entry{Body: body, Status: e.Status, ContentType: e.ContentType}
}

// cached serves anonymous GET requests from the in-memory page cache.
func (s *Server) cached(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || rctx(r).Session != nil {
			h(w, r)
			return
		}
		cacheable := true
		for k := range r.URL.Query() {
			cacheable = cacheable && cacheableParams[k]
		}
		key := s.langPath(rctx(r).Lang, r.URL.Path)
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		if cacheable {
			if e, ok := s.pages.Get(key); ok {
				s.recordView(r, e.Status, e.ContentType)
				writeCached(w, r, s.fillTokens(e), "HIT")
				return
			}
		}
		rec := &bufferWriter{header: http.Header{}, status: http.StatusOK}
		h(rec, r)
		sum := sha256.Sum256(rec.buf.Bytes())
		e := &cache.Entry{Body: rec.buf.Bytes(), Status: rec.status, ContentType: rec.header.Get("Content-Type"),
			ETag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		for k, v := range rec.header {
			w.Header()[k] = v
		}
		if cacheable && (rec.status == http.StatusOK || rec.status == http.StatusNotFound) && rec.header.Get("Cache-Control") != "no-store" {
			s.pages.Set(key, e)
		}
		s.recordView(r, e.Status, e.ContentType)
		writeCached(w, r, s.fillTokens(e), "MISS")
	}
}

func writeCached(w http.ResponseWriter, r *http.Request, e *cache.Entry, state string) {
	h := w.Header()
	if e.ContentType != "" {
		h.Set("Content-Type", e.ContentType)
	}
	if h.Get("Cache-Control") == "" {
		h.Set("Cache-Control", "public, no-cache")
	}
	h.Set("X-Cache", state)
	if e.ETag != "" {
		h.Set("ETag", e.ETag)
	}
	if e.ETag != "" && e.Status == http.StatusOK && r.Header.Get("If-None-Match") == e.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		w.Write(e.Body)
	}
}

// --------------------------------------------------------------- helpers --

func (s *Server) activeTheme(r *http.Request) *theme.Theme {
	slug := s.settings().ActiveTheme
	// Administrators can preview another theme; in development anyone can.
	if q := r.URL.Query().Get("preview_theme"); q != "" {
		if u := currentUser(r); s.cfg.Dev || (u != nil && auth.Can(u.Role, auth.CapAppearance)) {
			slug = q
		}
	}
	return s.themes.Active(slug)
}

func (s *Server) basePage(r *http.Request, kind string) (*theme.Theme, *PageData, error) {
	th := s.activeTheme(r)
	if th == nil {
		return nil, nil, errors.New("no theme available")
	}
	lang := s.reqLang(r)
	d, err := s.loadSiteData(r.Context(), lang)
	if err != nil {
		return nil, nil, err
	}
	sv := s.siteView(r, th, d)
	pd := &PageData{
		Site: sv, Kind: kind, OGType: "website",
		Canonical:   s.cfg.AbsURL(s.here(r)),
		Description: sv.Tagline,
		NoIndex:     s.settings().DiscourageSearch,
		Alternates: []FeedLink{
			{Title: sv.Title + " (RSS)", Type: "application/rss+xml", URL: sv.FeedRSS},
			{Title: sv.Title + " (Atom)", Type: "application/atom+xml", URL: sv.FeedAtom},
			{Title: sv.Title + " (JSON Feed)", Type: "application/feed+json", URL: sv.FeedJSON},
		},
		Translations: s.translationLinks(r, s.sameAlternates(r)),
	}
	return th, pd, nil
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, th *theme.Theme, page string, pd *PageData, status int) {
	ctx := r.Context()
	if d, err := s.loadSiteData(ctx, pd.Site.Lang); err == nil {
		pd.Widgets = s.buildWidgets(ctx, th, d, pd)
	}
	s.pluginHead(ctx, pd, pageInfo(pd)) // meta tags, JSON-LD, plugin stylesheets and scripts

	// Block layouts replace theme regions (builder.go).
	page = s.applyLayout(r, th, page, pd)

	var buf bytes.Buffer
	if err := th.Render(&buf, page, pd.Site.Lang, pd); err != nil {
		s.log.Error("render theme", "theme", th.Slug, "page", page, "err", err)
		http.Error(w, "theme error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if pd.NoIndex {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	if pd.Preview {
		w.Header().Set("Cache-Control", "no-store")
	}
	s.customPreviewHeaders(w, pd) // draft previews (customize.go)

	body := s.expandForms(w, r, pd.Site.Lang, buf.Bytes()) // plugin forms (plugin_web.go)
	w.WriteHeader(status)
	w.Write(body)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	th, pd, err := s.basePage(r, "404")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	pd.Title = s.tr(r, "Page not found")
	pd.NoIndex = true
	s.renderPage(w, r, th, "404", pd, http.StatusNotFound)
}

func pageParam(r *http.Request) (int, bool) {
	v := r.URL.Query().Get("page")
	if v == "" {
		return 1, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 100000 {
		return 0, false
	}
	return n, true
}

func (s *Server) postViews(posts []store.Post, viewer *store.User) []*PostView {
	out := make([]*PostView, 0, len(posts))
	for i := range posts {
		out = append(out, s.postView(&posts[i], viewer))
	}
	return out
}

// listing renders any paginated list of posts (home, archives, search).
func (s *Server) listing(w http.ResponseWriter, r *http.Request, kind string, q store.PostQuery, pd *PageData, th *theme.Theme) {
	page, ok := pageParam(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	per := s.settings().PostsPerPage
	if n := pd.Site.Custom.PerPage; kind == "home" && n > 0 {
		per = n // customiser home setting
	}
	q.Type, q.Lang, q.PublicOnly, q.Limit, q.Offset, q.WithTerms, q.WithMedia = store.TypePost, s.reqLang(r), true, per, (page-1)*per, true, true
	posts, total, err := s.store.ListPosts(r.Context(), q)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if page > 1 && len(posts) == 0 {
		s.notFound(w, r)
		return
	}
	pd.Posts = s.postViews(posts, currentUser(r))
	query := url.Values{}
	if pd.Query != "" {
		query.Set("q", pd.Query)
	}
	pd.Pagination = newPagination(s.here(r), query, page, per, total)
	if pd.Archive != nil {
		pd.Archive.Count = total
	}
	if page > 1 {
		pd.Canonical = s.cfg.AbsURL(s.here(r) + "?page=" + strconv.Itoa(page))
		if pd.Title == "" {
			pd.Title = s.tr(r, "Page %d", page)
		} else {
			pd.Title += " · " + s.tr(r, "Page %d", page)
		}
	}
	s.renderPage(w, r, th, kind, pd, http.StatusOK)
}

// ----------------------------------------------------------------- pages --

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	lang := s.reqLang(r)
	if st.FrontPage == "page" && st.FrontPageID > 0 && r.URL.Query().Get("page") == "" {
		if p := s.frontPage(r.Context(), lang); p != nil {
			s.single(w, r, p, true)
			return
		}
	}
	th, pd, err := s.basePage(r, "home")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pd.IsFront = true
	pd.JSONLD = map[string]any{"@context": "https://schema.org", "@type": "WebSite", "name": pd.Site.Title,
		"url": pd.Site.URL, "description": pd.Site.Tagline, "inLanguage": lang,
		"potentialAction": map[string]any{"@type": "SearchAction", "target": s.cfg.AbsURL(pd.Site.Search + "?q={q}"), "query-input": "required name=q"}}
	s.listing(w, r, "home", store.PostQuery{}, pd, th)
}

// frontPage returns the public static front page in lang (the configured page
// or its translation), or nil to show the latest posts instead.
func (s *Server) frontPage(ctx context.Context, lang string) *store.Post {
	p, err := s.store.PostByID(ctx, s.settings().FrontPageID)
	if err != nil {
		return nil
	}
	if p.Lang != lang {
		trs, err := s.store.Translations(ctx, p)
		if err != nil {
			return nil
		}
		p = nil
		for _, t := range trs {
			if t.Lang == lang {
				p, _ = s.store.PostByID(ctx, t.ID)
				break
			}
		}
	}
	if p == nil || !p.IsPublic(time.Now()) {
		return nil
	}
	return p
}

func (s *Server) handleBlog(w http.ResponseWriter, r *http.Request) {
	th, pd, err := s.basePage(r, "home")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pd.Title = s.tr(r, "Blog")
	pd.Heading = pd.Title
	s.listing(w, r, "home", store.PostQuery{}, pd, th)
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	if slug, ok := strings.CutSuffix(r.PathValue("slug"), ".md"); ok {
		s.serveMarkdown(w, r, store.TypePost, slug)
		return
	}
	p, err := s.store.PostBySlug(r.Context(), store.TypePost, s.reqLang(r), r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.single(w, r, p, false)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if slug, ok := strings.CutSuffix(slug, ".md"); ok {
		s.serveMarkdown(w, r, store.TypePage, slug)
		return
	}
	p, err := s.store.PostBySlug(r.Context(), store.TypePage, s.reqLang(r), slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		s.serverError(w, r, err)
		return
	}
	if s.frontPageIDs(r.Context())[p.ID] {
		http.Redirect(w, r, s.langPath(p.Lang, "/"), http.StatusMovedPermanently)
		return
	}
	s.single(w, r, p, false)
}

// single renders a post or a page. Unpublished content is only visible to
// users who may edit it, never cached.
func (s *Server) single(w http.ResponseWriter, r *http.Request, p *store.Post, front bool) {
	viewer := currentUser(r)
	preview := false
	if !p.IsPublic(time.Now()) {
		if viewer == nil || !canEditPost(viewer, p) {
			s.notFound(w, r)
			return
		}
		preview = true
	}
	s.renderSingle(w, r, p, front, preview)
}

func (s *Server) renderSingle(w http.ResponseWriter, r *http.Request, p *store.Post, front, preview bool) {
	kind := "single"
	if p.Type == store.TypePage {
		kind = "page"
	}
	th, pd, err := s.basePage(r, kind)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	viewer := currentUser(r)
	ctx := r.Context()
	pv := s.postView(p, viewer)
	pv.Content = s.renderBody(ctx, p, plugin.FilterPage)
	pd.Post, pd.Title, pd.Heading, pd.IsFront, pd.Preview = pv, p.Title, p.Title, front, preview
	pi := s.PostInfo(p)
	pd.postInfo = &pi
	if preview {
		pd.NoIndex = true
	}
	pd.Description = p.MetaDescription
	if pd.Description == "" {
		pd.Description = content.Summary(p.Excerpt, p.Content, 160)
	}
	pd.Canonical = pv.AbsURL
	if front {
		pd.Canonical = s.cfg.AbsURL(s.langPath(p.Lang, "/"))
	}
	if s.multilingual() {
		alts := map[string]string{}
		if trs, err := s.store.Translations(ctx, p); err == nil {
			now := time.Now()
			for _, t := range trs {
				if t.IsPublic(now) || t.ID == p.ID {
					alts[t.Lang] = s.langPath(t.Lang, postPath(t.Type, t.Slug))
					if front {
						alts[t.Lang] = s.langPath(t.Lang, "/")
					}
				}
			}
		}
		pd.Translations = s.translationLinks(r, alts)
	}
	if s.aiOpen() && !preview {
		pd.Alternates = append(pd.Alternates, FeedLink{Title: p.Title, Type: "text/markdown", URL: s.cfg.AbsURL(markdownURL(s.postURL(p)))})
	}
	if pv.Image != nil {
		pd.OGImage = s.cfg.AbsURL(pv.Image.Large)
	} else if s.ogEnabled() && p.IsPublic(time.Now()) {
		pd.OGImage = s.ogImageURL(p)
	}
	if p.Type == store.TypePost {
		pd.OGType = "article"
		ld := map[string]any{"@context": "https://schema.org", "@type": "BlogPosting", "headline": p.Title,
			"inLanguage": p.Lang, "url": pv.AbsURL, "mainEntityOfPage": pv.AbsURL, "dateModified": p.UpdatedAt.UTC().Format(time.RFC3339),
			"author": map[string]any{"@type": "Person", "name": p.AuthorName}, "description": pd.Description}
		if p.PublishedAt != nil {
			ld["datePublished"] = p.PublishedAt.UTC().Format(time.RFC3339)
		}
		if pd.OGImage != "" {
			ld["image"] = pd.OGImage
		}
		pd.JSONLD = ld
		if prev, next, err := s.store.AdjacentPosts(ctx, p); err == nil {
			if prev != nil {
				pd.Prev = s.postView(prev, viewer)
			}
			if next != nil {
				pd.Next = s.postView(next, viewer)
			}
		}
		if err := s.loadComments(r, p, pd); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.renderPage(w, r, th, kind, pd, http.StatusOK)
}

func (s *Server) loadComments(r *http.Request, p *store.Post, pd *PageData) error {
	tree, err := s.store.ApprovedComments(r.Context(), p.ID)
	if err != nil {
		return err
	}
	conv := func(c *store.Comment) *CommentView {
		return &CommentView{ID: c.ID, Author: c.AuthorName, Initials: initials(c.AuthorName), URL: c.AuthorURL,
			Content: c.Content, Date: c.CreatedAt.In(s.loc()), IsAuthor: c.UserID != nil && p.AuthorID != nil && *c.UserID == *p.AuthorID}
	}
	for _, c := range tree {
		cv := conv(c)
		for _, rep := range c.Replies {
			cv.Replies = append(cv.Replies, conv(rep))
		}
		pd.Comments = append(pd.Comments, cv)
	}
	st := s.settings()
	if !pd.Post.CommentsOpen {
		return nil
	}
	form := &CommentForm{
		Action:       s.postURL(p) + "/comments",
		Token:        s.commentToken(r, p.ID),
		RequireLogin: st.CommentsRequireLogin,
		Notice:       r.URL.Query().Get("comment"),
	}
	if u := currentUser(r); u != nil {
		form.LoggedIn, form.ViewerName = true, u.Name()
		if sess := rctx(r).Session; sess != nil {
			form.CSRF = sess.CSRFToken
		}
	}
	pd.CommentForm = form
	return nil
}

func (s *Server) handleTermArchive(taxonomy string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		term, err := s.store.TermBySlug(r.Context(), taxonomy, r.PathValue("slug"))
		if err != nil {
			s.notFound(w, r)
			return
		}
		th, pd, err := s.basePage(r, "archive")
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		t := term.In(s.reqLang(r))
		feed := s.langPath(s.reqLang(r), termURL(&t)+"/feed.xml")
		pd.Archive = &ArchiveView{Kind: taxonomy, Title: t.Name, Description: t.Description, FeedURL: feed}
		pd.Title, pd.Heading = t.Name, t.Name
		if t.Description != "" {
			pd.Description = t.Description
		}
		pd.Alternates = append([]FeedLink{{Title: t.Name + " (RSS)", Type: "application/rss+xml", URL: feed}}, pd.Alternates...)
		s.listing(w, r, "archive", store.PostQuery{TermID: t.ID}, pd, th)
	}
}

func (s *Server) handleAuthorArchive(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByUsername(r.Context(), r.PathValue("username"))
	if err != nil || u.Role == auth.RoleSubscriber {
		s.notFound(w, r)
		return
	}
	th, pd, err := s.basePage(r, "archive")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	feed := s.langPath(s.reqLang(r), authorURL(u.Username)+"/feed.xml")
	pd.Archive = &ArchiveView{Kind: "author", Title: u.Name(), Description: u.Bio, FeedURL: feed}
	pd.Title, pd.Heading = u.Name(), u.Name()
	if u.Bio != "" {
		pd.Description = u.Bio
	}
	pd.Alternates = append([]FeedLink{{Title: u.Name() + " (RSS)", Type: "application/rss+xml", URL: feed}}, pd.Alternates...)
	s.listing(w, r, "archive", store.PostQuery{AuthorID: u.ID}, pd, th)
}

func (s *Server) handleDateArchive(w http.ResponseWriter, r *http.Request) {
	year, err1 := strconv.Atoi(r.PathValue("year"))
	month, err2 := strconv.Atoi(r.PathValue("month"))
	if err1 != nil || err2 != nil || year < 1970 || year > 9999 || month < 1 || month > 12 {
		s.notFound(w, r)
		return
	}
	page, ok := pageParam(r)
	if !ok {
		s.notFound(w, r)
		return
	}
	th, pd, err := s.basePage(r, "archive")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	per := s.settings().PostsPerPage
	lang := s.reqLang(r)
	posts, total, err := s.store.PostsInMonth(r.Context(), s.loc().String(), lang, year, month, per, (page-1)*per)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if total == 0 {
		s.notFound(w, r)
		return
	}
	title := upperFirst(i18n.MonthName(lang, time.Month(month))) + " " + strconv.Itoa(year)
	pd.Archive = &ArchiveView{Kind: "date", Title: title, Count: total}
	pd.Title, pd.Heading = title, title
	pd.Posts = s.postViews(posts, currentUser(r))
	pd.Pagination = newPagination(s.here(r), nil, page, per, total)
	s.renderPage(w, r, th, "archive", pd, http.StatusOK)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 200 {
		q = q[:200]
	}
	th, pd, err := s.basePage(r, "search")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pd.Query = q
	pd.NoIndex = true
	pd.Title = s.tr(r, "Search")
	if q != "" {
		pd.Title = s.tr(r, "Results for “%s”", q)
	}
	pd.Heading = pd.Title
	if q == "" {
		s.renderPage(w, r, th, "search", pd, http.StatusOK)
		return
	}
	s.listing(w, r, "search", store.PostQuery{Search: q, Order: "rank"}, pd, th)
}

// ------------------------------------------------------------- comments --

func (s *Server) handleComment(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	ctx := r.Context()
	p, err := s.store.PostBySlug(ctx, store.TypePost, s.reqLang(r), r.PathValue("slug"))
	if err != nil || !p.IsPublic(time.Now()) {
		s.notFound(w, r)
		return
	}
	back := s.postURL(p)
	done := func(notice string) {
		http.Redirect(w, r, back+"?comment="+notice+"#comments", http.StatusSeeOther)
	}
	if !s.commentsOpen(p) {
		done("closed")
		return
	}
	if err := r.ParseForm(); err != nil {
		done("error")
		return
	}
	ip := clientIP(r)
	if !s.commentIP.Allow(ipKey(ip)) {
		done("ratelimit")
		return
	}
	// Bots: honeypot field and a signed render timestamp (too fast = bot).
	age, valid := s.keys.Verify("comment", strconv.FormatInt(p.ID, 10), r.PostFormValue("token"), 48*time.Hour)
	if r.PostFormValue("website") != "" || !valid || age < 3*time.Second {
		s.log.Info("comment rejected as spam", "post", p.ID, "ip", ip.String(), "valid_token", valid)
		done("pending") // do not tell bots they were detected
		return
	}
	st := s.settings()
	u := currentUser(r)
	if u != nil && !validCSRF(r) {
		done("error")
		return
	}
	if u == nil && st.CommentsRequireLogin {
		done("login")
		return
	}
	in := store.NewComment{PostID: p.ID, IP: ip, UserAgent: truncateStr(r.UserAgent(), 300),
		Content: strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("content"), "\r\n", "\n"))}
	if u != nil {
		in.UserID, in.AuthorName, in.AuthorEmail, in.AuthorURL = &u.ID, u.Name(), u.Email, u.Website
	} else {
		in.AuthorName = strings.TrimSpace(r.PostFormValue("name"))
		in.AuthorEmail = strings.TrimSpace(r.PostFormValue("email"))
		in.AuthorURL = strings.TrimSpace(r.PostFormValue("url"))
	}
	if in.AuthorName == "" || len([]rune(in.AuthorName)) > 100 || in.Content == "" || len([]rune(in.Content)) > 10000 {
		done("error")
		return
	}
	if _, err := mail.ParseAddress(in.AuthorEmail); err != nil || len(in.AuthorEmail) > 254 {
		done("error")
		return
	}
	if in.AuthorURL != "" && !validHTTPURL(in.AuthorURL) {
		in.AuthorURL = ""
	}
	if pid, err := strconv.ParseInt(r.PostFormValue("parent_id"), 10, 64); err == nil && pid > 0 {
		parent, err := s.store.CommentByID(ctx, pid)
		if err != nil || parent.PostID != p.ID || parent.Status != "approved" {
			done("error")
			return
		}
		in.ParentID = &pid
	}
	in.Status = "pending"
	moderator := u != nil && auth.Can(u.Role, auth.CapModerate)
	// returning: this e-mail address had a comment approved before.
	returning := false
	if !moderator && in.AuthorEmail != "" {
		returning, _ = s.store.HasApprovedComment(ctx, in.AuthorEmail)
	}
	switch {
	case moderator:
		in.Status = "approved"
	case st.CommentModeration == "none":
		in.Status = "approved"
	case st.CommentModeration == "returning" && returning:
		in.Status = "approved"
	}
	// comment.check plugins (spam filters) may hold or reject the comment;
	// moderators' own comments are not checked.
	if !moderator {
		var parent int64
		if in.ParentID != nil {
			parent = *in.ParentID
		}
		pi := s.PostInfo(p)
		v := s.plugins.CheckComment(ctx, plugin.CommentInput{Author: in.AuthorName, URL: in.AuthorURL, Content: in.Content, Post: &pi,
			ParentID: parent, Registered: u != nil, Returning: returning, Email: in.AuthorEmail, IP: ip.String(), UserAgent: in.UserAgent})
		switch v.Verdict {
		case plugin.VerdictSpam:
			in.Status = "spam"
		case plugin.VerdictPending:
			in.Status = "pending"
		}
		if v.Verdict != plugin.VerdictOK {
			s.log.Info("comment held by plugin", "plugin", v.Plugin, "verdict", v.Verdict, "reason", v.Reason, "post", p.ID)
			in.HeldBy, in.HeldReason = v.Plugin, v.Reason
		}
	}
	id, err := s.store.CreateComment(ctx, in)
	if err != nil {
		s.log.Error("create comment", "err", err)
		done("error")
		return
	}
	s.plugins.Emit("comment.created", map[string]any{"id": id, "post_id": p.ID, "post": s.PostInfo(p), "author": in.AuthorName,
		"status": in.Status, "content": in.Content})
	if in.Status == "approved" {
		s.contentChanged()
		done("approved")
		return
	}
	done("pending")
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && len(raw) <= 300
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --------------------------------------------------------------- widgets --

var builtinWidgets = []string{"recent_posts", "categories", "tags", "archives", "search", "feeds", "text"}

func (s *Server) buildWidgets(ctx context.Context, th *theme.Theme, d *siteData, pd *PageData) map[string][]WidgetView {
	out := map[string][]WidgetView{}
	var page *plugin.PageInfo
	for _, area := range th.WidgetAreas {
		for _, wd := range d.widgets[area.ID] {
			v := WidgetView{ID: wd.ID, Kind: wd.Kind, Title: wd.Title, Class: "widget-" + wd.Kind, Label: wd.Title}
			switch wd.Kind {
			case "recent_posts":
				n, _ := strconv.Atoi(wd.Config["count"])
				if n < 1 || n > 20 {
					n = 5
				}
				posts, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Lang: d.lang, PublicOnly: true, Limit: n, WithMedia: true})
				if err != nil {
					continue
				}
				v.Posts = s.postViews(posts, nil)
			case "categories":
				v.Terms = d.categories
			case "tags":
				v.Terms = d.tags
			case "archives":
				v.Archives = d.archives
			case "feeds":
				v.Links = []FeedLink{{Title: "RSS", URL: pd.Site.FeedRSS}, {Title: "Atom", URL: pd.Site.FeedAtom}, {Title: "JSON Feed", URL: pd.Site.FeedJSON}}
			case "search":
			case "text":
				v.HTML = content.RenderPlain(wd.Config["text"])
			default:
				parts := strings.SplitN(wd.Kind, ":", 3)
				if len(parts) != 3 || parts[0] != "plugin" {
					continue
				}
				if page == nil {
					page = pageInfo(pd)
				}
				html, err := s.plugins.Widget(ctx, parts[1], parts[2], wd.Config, page)
				if err != nil || html == "" {
					continue
				}
				v.HTML = content.SanitizePluginHTML(html, func(n string) string { return icons.SVG(n, "") })
				v.Class = "widget-plugin widget-" + parts[1] + "-" + parts[2] // ids are validated by the manifest
				if v.Label == "" {
					v.Label = s.pluginWidgetTitle(parts[1], parts[2])
				}
			}
			out[area.ID] = append(out[area.ID], v)
		}
	}
	return out
}

// ----------------------------------------------------------- assets etc. --

func (s *Server) handleThemeStatic(w http.ResponseWriter, r *http.Request) {
	th, ok := s.themes.Get(r.PathValue("theme"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, hash, err := th.Static(r.PathValue("path"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	serveAsset(w, r, f, path.Base(r.PathValue("path")), hash)
}

// handleUploads serves media in development or without nginx. In production
// nginx serves /uploads directly with the same headers (deploy/nginx).
func (s *Server) handleUploads(w http.ResponseWriter, r *http.Request) {
	rel := path.Clean("/" + r.PathValue("path"))[1:]
	if rel == "" || strings.HasPrefix(rel, ".") || strings.Contains(rel, "/.") {
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(s.cfg.UploadsDir, filepath.FromSlash(rel))
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "public, max-age=2592000")
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".png", ".gif", ".webp", ".avif", ".mp3", ".ogg", ".wav", ".mp4", ".webm", ".pdf":
	default:
		h.Set("Content-Disposition", "attachment")
	}
	http.ServeContent(w, r, rel, fi.ModTime(), f)
}

// handleSitemap lists public URLs in every language. Translated posts carry
// xhtml:link alternates so search engines can pair the versions.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entries, err := s.store.SitemapEntries(ctx, 45000)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	langs := s.siteLangs()
	multi := len(langs) > 1
	groups := map[int64][]store.SitemapEntry{}
	for _, e := range entries {
		if e.Group != 0 {
			groups[e.Group] = append(groups[e.Group], e)
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`)
	if multi {
		b.WriteString(` xmlns:xhtml="http://www.w3.org/1999/xhtml"`)
	}
	b.WriteString(">\n")
	write := func(loc string, mod time.Time, alts map[string]string) {
		b.WriteString("  <url><loc>" + template.HTMLEscapeString(loc) + "</loc>")
		if !mod.IsZero() {
			b.WriteString("<lastmod>" + mod.UTC().Format("2006-01-02") + "</lastmod>")
		}
		if len(alts) > 1 {
			for _, l := range langs {
				if u, ok := alts[l]; ok {
					b.WriteString(`<xhtml:link rel="alternate" hreflang="` + l + `" href="` + template.HTMLEscapeString(u) + `"/>`)
				}
			}
		}
		b.WriteString("</url>\n")
	}
	last, _ := s.store.LastPublicUpdate(ctx)
	homes := map[string]string{}
	for _, l := range langs {
		homes[l] = s.cfg.AbsURL(s.langPath(l, "/"))
	}
	for _, l := range langs {
		write(homes[l], last, homes)
	}
	front := s.frontPageIDs(ctx)
	for _, e := range entries {
		if front[e.ID] {
			continue
		}
		var alts map[string]string
		if multi && e.Group != 0 {
			alts = map[string]string{}
			for _, g := range groups[e.Group] {
				alts[g.Lang] = s.cfg.AbsURL(s.langPath(g.Lang, postPath(g.Type, g.Slug)))
			}
		}
		write(s.cfg.AbsURL(s.langPath(e.Lang, postPath(e.Type, e.Slug))), e.UpdatedAt, alts)
	}
	for _, l := range langs {
		cats, err := s.store.ListTerms(ctx, "category", l)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		for i := range cats {
			if cats[i].Count > 0 {
				write(s.cfg.AbsURL(s.langPath(l, termURL(&cats[i]))), time.Time{}, nil)
			}
		}
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(b.String()))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.DB.Ping(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "ok\n")
}
