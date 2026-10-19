package export

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"encoding/xml"
	"html"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"sap13ns/internal/content"
	"sap13ns/internal/store"
	"sap13ns/internal/wpimport"
)

// Batch sizes: posts are also bounded by the bytes of their content, so a
// batch of very long posts stays small.
const (
	postBatch      = 100
	postBatchBytes = int64(8 << 20)
	commentBatch   = 500
	mediaBatch     = 500
)

const (
	wpDate   = "2006-01-02 15:04:05"
	noDate   = "0000-00-00 00:00:00" // what WordPress writes for an unset GMT date
	rssDate  = "Mon, 02 Jan 2006 15:04:05 -0700"
	wxrIntro = `<?xml version="1.0" encoding="UTF-8" ?>
<!-- This is a WordPress eXtended RSS file (WXR 1.2) written by sap13ns as an export of a site. -->
<!-- WordPress imports it with Tools → Import → WordPress, and sap13ns with Tools → Import. -->
<rss version="2.0"
	xmlns:excerpt="http://wordpress.org/export/1.2/excerpt/"
	xmlns:content="http://purl.org/rss/1.0/modules/content/"
	xmlns:wfw="http://wellformedweb.org/CommentAPI/"
	xmlns:dc="http://purl.org/dc/elements/1.1/"
	xmlns:wp="http://wordpress.org/export/1.2/"
	xmlns:sap13ns="` + wpimport.Namespace + `"
>
<channel>
`
)

// WriteWXR writes the WXR file of o to w.
func WriteWXR(ctx context.Context, st *store.Store, w io.Writer, o Options) (*Report, error) {
	e := newExporter(st, o)
	lw := &limitWriter{w: w, remaining: e.o.MaxXMLBytes}
	x := &xmlWriter{w: bufio.NewWriterSize(lw, 64<<10)}
	if err := e.wxr(ctx, x); err != nil {
		return e.rep, err
	}
	if err := x.flush(); err != nil {
		return e.rep, err
	}
	e.rep.Bytes = lw.n
	return e.rep, nil
}

type exporter struct {
	st  *store.Store
	o   Options
	f   store.ExportFilter
	rep *Report
	now time.Time

	authors   map[int64]bool  // exported users
	mediaBase int64           // attachment ids are mediaBase + media id
	parents   map[int64]int64 // media id → the first exported post featuring it
	// variants maps the path of a resized copy of an exported image
	// ("2026/10/x-medium.png") to its original ("2026/10/x.png").
	variants map[string]string

	// Menus (menus.go): WXR item ids of menu items follow the attachments
	// (menuBase + n), and nav_menu term ids follow the categories and tags.
	menus    []menu
	menuBase int64
	termIDs  map[string]int64 // "category/slug", "tag/slug" → term id
	posts    map[string]int64 // public path of an exported post or page → its id
	postType map[int64]string
}

func newExporter(st *store.Store, o Options) *exporter {
	o = o.withDefaults()
	return &exporter{st: st, o: o, f: o.filter(), rep: &Report{}, now: o.Now(), authors: map[int64]bool{},
		termIDs: map[string]int64{}, posts: map[string]int64{}, postType: map[int64]string{}, variants: map[string]string{}}
}

func (e *exporter) wxr(ctx context.Context, x *xmlWriter) error {
	site := e.o.Site
	x.raw(wxrIntro)
	x.textEl("title", site.Title)
	x.textEl("link", site.URL)
	x.textEl("description", site.Description)
	x.textEl("pubDate", e.now.UTC().Format(rssDate))
	x.textEl("language", site.Language)
	x.textEl("wp:wxr_version", "1.2")
	x.textEl("wp:base_site_url", site.URL)
	x.textEl("wp:base_blog_url", site.URL)
	x.nl()
	if err := e.writeAuthors(ctx, x); err != nil {
		return err
	}
	if err := e.writeTerms(ctx, x); err != nil {
		return err
	}
	if err := e.writeMenuTerms(ctx, x); err != nil {
		return err
	}
	if err := e.writeRedirects(ctx, x); err != nil {
		return err
	}
	x.textEl("generator", cmp.Or(site.Generator, "sap13ns"))
	x.nl()
	if err := e.writeMedia(ctx, x); err != nil {
		return err
	}
	if err := e.eachPost(ctx, true, func(p *store.Post, comments int) error { return e.writePost(ctx, x, p, comments) }); err != nil {
		return err
	}
	// Menu items come last: the WordPress Importer links an item to its
	// post or page once that one is imported.
	if err := e.writeMenuItems(x); err != nil {
		return err
	}
	x.raw("</channel>\n</rss>\n")
	return x.err
}

func (e *exporter) writeAuthors(ctx context.Context, x *xmlWriter) error {
	users, err := e.st.ExportAuthors(ctx, e.f)
	if err != nil {
		return err
	}
	for _, u := range users {
		e.authors[u.ID] = true
		email := ""
		if e.o.PersonalData {
			email = u.Email
		}
		x.raw("\t<wp:author>")
		x.intEl("wp:author_id", u.ID)
		x.cdataEl("wp:author_login", u.Username)
		x.cdataEl("wp:author_email", email)
		x.cdataEl("wp:author_display_name", u.DisplayName)
		x.cdataEl("wp:author_first_name", "")
		x.cdataEl("wp:author_last_name", "")
		x.raw("</wp:author>\n")
		e.rep.Authors++
	}
	return x.err
}

// sortTerms orders categories so that every parent comes before its
// children, as the WordPress importer needs; tags keep their order.
func sortTerms(terms []store.Term) []store.Term {
	byID := map[int64]*store.Term{}
	for i := range terms {
		byID[terms[i].ID] = &terms[i]
	}
	out := make([]store.Term, 0, len(terms))
	done := map[int64]bool{}
	var visit func(t *store.Term, depth int)
	visit = func(t *store.Term, depth int) {
		if done[t.ID] {
			return
		}
		done[t.ID] = true
		if t.ParentID != nil && depth < 100 {
			if p := byID[*t.ParentID]; p != nil {
				visit(p, depth+1)
			}
		}
		out = append(out, *t)
	}
	for i := range terms {
		visit(&terms[i], 0)
	}
	return out
}

func (e *exporter) writeTerms(ctx context.Context, x *xmlWriter) error {
	terms, err := e.st.ExportTerms(ctx)
	if err != nil {
		return err
	}
	slugs := make(map[int64]string, len(terms))
	for _, t := range terms {
		slugs[t.ID] = t.Slug
		e.termIDs[t.Taxonomy+"/"+t.Slug] = t.ID
	}
	for _, t := range sortTerms(terms) {
		switch t.Taxonomy {
		case "category":
			parent := ""
			if t.ParentID != nil {
				parent = slugs[*t.ParentID]
			}
			x.raw("\t<wp:category>")
			x.intEl("wp:term_id", t.ID)
			x.cdataEl("wp:category_nicename", t.Slug)
			x.cdataEl("wp:category_parent", parent)
			x.cdataEl("wp:cat_name", html.EscapeString(t.Name))
			x.cdataEl("wp:category_description", html.EscapeString(t.Description))
			e.termMeta(x, t)
			x.raw("</wp:category>\n")
			e.rep.Categories++
		case "tag":
			x.raw("\t<wp:tag>")
			x.intEl("wp:term_id", t.ID)
			x.cdataEl("wp:tag_slug", t.Slug)
			x.cdataEl("wp:tag_name", html.EscapeString(t.Name))
			x.cdataEl("wp:tag_description", html.EscapeString(t.Description))
			e.termMeta(x, t)
			x.raw("</wp:tag>\n")
			e.rep.Tags++
		}
	}
	return x.err
}

func (e *exporter) termMeta(x *xmlWriter, t store.Term) {
	if len(t.I18n) == 0 {
		return
	}
	b, err := json.Marshal(t.I18n)
	if err != nil {
		return
	}
	x.raw("<wp:termmeta>")
	x.cdataEl("wp:meta_key", wpimport.MetaTermI18n)
	x.cdataEl("wp:meta_value", string(b))
	x.raw("</wp:termmeta>")
}

func (e *exporter) writeRedirects(ctx context.Context, x *xmlWriter) error {
	list, err := e.st.Redirects(ctx)
	if err != nil {
		return err
	}
	slices.SortFunc(list, func(a, b store.Redirect) int { return cmp.Compare(a.ID, b.ID) })
	for _, rd := range list {
		x.raw("\t<sap13ns:redirect>")
		x.cdataEl("sap13ns:source", rd.Source)
		x.cdataEl("sap13ns:target", rd.Target)
		x.intEl("sap13ns:status", int64(rd.Status))
		x.raw("</sap13ns:redirect>\n")
		e.rep.Redirects++
	}
	return x.err
}

// eachMedia calls fn for each exported file: the library, or the featured
// images of the exported posts.
func (e *exporter) eachMedia(ctx context.Context, fn func(*store.Media) error) error {
	var featured *store.ExportFilter
	if !e.o.Media {
		featured = &e.f
	}
	var after int64
	for {
		list, err := e.st.ExportMedia(ctx, featured, after, mediaBatch)
		if err != nil || len(list) == 0 {
			return err
		}
		for i := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(&list[i]); err != nil {
				return err
			}
		}
		after = list[len(list)-1].ID
	}
}

// mediaURL is the address of an uploaded file in the export: on the site,
// or with WPBase where WordPress keeps its uploads.
func (e *exporter) mediaURL(path string) string {
	if e.o.WPBase != "" {
		return e.o.WPBase + "/wp-content/uploads/" + path
	}
	return e.o.Site.URL + "/uploads/" + path
}

func (e *exporter) writeMedia(ctx context.Context, x *xmlWriter) error {
	base, err := e.st.MaxPostID(ctx)
	if err != nil {
		return err
	}
	e.mediaBase = base
	if e.parents, err = e.st.ExportFeaturedParents(ctx, e.f); err != nil {
		return err
	}
	return e.eachMedia(ctx, func(m *store.Media) error {
		fileURL := e.mediaURL(m.Path)
		for _, v := range m.Variants {
			e.variants[v.Path] = m.Path
		}
		name := m.OriginalName
		if name == "" {
			name = m.Path[strings.LastIndexByte(m.Path, '/')+1:]
		}
		x.raw("\t<item>\n")
		x.cdataEl("title", html.EscapeString(name))
		x.textEl("link", fileURL)
		x.textEl("pubDate", m.CreatedAt.UTC().Format(rssDate))
		x.cdataEl("dc:creator", "")
		x.raw(`<guid isPermaLink="false">`)
		x.text(fileURL)
		x.raw("</guid>")
		x.raw("<description></description>")
		x.cdataEl("content:encoded", "")
		x.cdataEl("excerpt:encoded", html.EscapeString(m.Caption))
		x.intEl("wp:post_id", e.mediaBase+m.ID)
		e.dates(x, "wp:post_date", &m.CreatedAt)
		e.dates(x, "wp:post_modified", &m.CreatedAt)
		x.cdataEl("wp:comment_status", "closed")
		x.cdataEl("wp:ping_status", "closed")
		x.cdataEl("wp:post_name", content.Slugify(strings.TrimSuffix(name, fileExt(name))))
		x.cdataEl("wp:status", "inherit")
		// Attached to the post it is the featured image of, as WordPress
		// does with an image uploaded in the editor.
		x.intEl("wp:post_parent", e.parents[m.ID])
		x.intEl("wp:menu_order", 0)
		x.cdataEl("wp:post_type", "attachment")
		x.cdataEl("wp:post_password", "")
		x.intEl("wp:is_sticky", 0)
		x.cdataEl("wp:attachment_url", fileURL)
		x.meta("_wp_attached_file", m.Path)
		if m.Alt != "" {
			x.meta("_wp_attachment_image_alt", m.Alt)
		}
		x.raw("\n\t</item>\n")
		e.rep.Attachments++
		return x.err
	})
}

func fileExt(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

// eachPost calls fn for each exported post and page, by id, with the
// number of its exported comments when withComments is set.
func (e *exporter) eachPost(ctx context.Context, withComments bool, fn func(p *store.Post, comments int) error) error {
	var after int64
	for {
		list, err := e.st.ExportPosts(ctx, e.f, after, postBatch, postBatchBytes)
		if err != nil || len(list) == 0 {
			return err
		}
		ids := make([]int64, len(list))
		for i, p := range list {
			ids[i] = p.ID
		}
		var counts map[int64]int
		if withComments {
			if counts, err = e.st.ExportCommentCounts(ctx, ids, e.o.commentStatuses()); err != nil {
				return err
			}
		}
		for i := range list {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(&list[i], counts[list[i].ID]); err != nil {
				return err
			}
			list[i] = store.Post{} // let the content go before the next batch arrives
		}
		after = ids[len(ids)-1]
	}
}

// path is the public path of a post, with its language prefix.
func (e *exporter) path(p *store.Post) string {
	out := "/blog/" + p.Slug
	if p.Type == store.TypePage {
		out = "/" + p.Slug
	}
	if p.Lang != "" && p.Lang != e.o.Site.Language {
		out = "/" + p.Lang + out
	}
	return out
}

// wpStatus maps a status to WordPress's; a published post with a date to
// come is scheduled ("future").
func (e *exporter) wpStatus(p *store.Post) string {
	switch p.Status {
	case store.StatusPublished:
		if p.IsScheduled(e.now) {
			return "future"
		}
		return "publish"
	case store.StatusDraft, store.StatusPending, store.StatusPrivate, store.StatusTrash:
		return p.Status
	}
	return "draft"
}

// rootURL finds site-relative addresses in attributes of rendered HTML.
var rootURL = regexp.MustCompile(`(\s(?:src|href|poster)=")(/[^/"][^"]*|/)"`)

// renderHTML renders a post for content:encoded, with absolute addresses:
// the WordPress importer swaps the URLs of the files it downloads, and
// only finds them absolute.
// uploadPath matches the address of an uploaded file in rendered HTML.
var uploadPath = regexp.MustCompile(`/uploads/[0-9]{4}/[0-9]{2}/[^"'\s?#<>]+`)

func (e *exporter) renderHTML(md string) string {
	h := string(content.RenderPlain(md))
	h = rootURL.ReplaceAllString(h, `${1}`+strings.ReplaceAll(e.o.Site.URL, "$", "$$")+`${2}"`)
	if len(e.variants) > 0 {
		// The editor inserts the medium copy of an image. WordPress makes
		// copies of its own, with other names, and its importer only swaps
		// the addresses of the files it imports: point at the original.
		h = uploadPath.ReplaceAllStringFunc(h, func(s string) string {
			if orig, ok := e.variants[s[len("/uploads/"):]]; ok {
				return "/uploads/" + orig
			}
			return s
		})
	}
	if e.o.WPBase != "" {
		// The files move with the content: point at where WordPress
		// serves them, so that its importer finds and swaps them.
		h = strings.ReplaceAll(h, e.o.Site.URL+"/uploads/", e.o.WPBase+"/wp-content/uploads/")
	}
	return h
}

func (e *exporter) writePost(ctx context.Context, x *xmlWriter, p *store.Post, comments int) error {
	link := e.o.Site.URL + e.path(p)
	if !e.o.NoMenus {
		e.posts[e.path(p)] = p.ID
		e.postType[p.ID] = p.Type
	}
	date := p.PublishedAt
	if date == nil {
		date = &p.CreatedAt
	}
	x.raw("\t<item>\n")
	x.cdataEl("title", html.EscapeString(p.Title))
	x.textEl("link", link)
	x.textEl("pubDate", date.UTC().Format(rssDate))
	x.cdataEl("dc:creator", p.AuthorUsername)
	x.raw(`<guid isPermaLink="false">`)
	x.text(link)
	x.raw("</guid>")
	x.raw("<description></description>")
	x.cdataEl("content:encoded", e.renderHTML(p.Content))
	x.cdataEl("excerpt:encoded", html.EscapeString(p.Excerpt))
	x.intEl("wp:post_id", p.ID)
	x.cdataEl("wp:post_date", date.In(e.o.Site.Location).Format(wpDate))
	if p.PublishedAt != nil {
		x.cdataEl("wp:post_date_gmt", p.PublishedAt.UTC().Format(wpDate))
	} else {
		x.cdataEl("wp:post_date_gmt", noDate)
	}
	e.dates(x, "wp:post_modified", &p.UpdatedAt)
	x.cdataEl("wp:comment_status", p.CommentStatus)
	x.cdataEl("wp:ping_status", "closed")
	x.cdataEl("wp:post_name", p.Slug)
	x.cdataEl("wp:status", e.wpStatus(p))
	var parent int64
	if p.ParentID != nil {
		parent = *p.ParentID
	}
	x.intEl("wp:post_parent", parent)
	x.intEl("wp:menu_order", int64(p.MenuOrder))
	x.cdataEl("wp:post_type", p.Type)
	x.cdataEl("wp:post_password", "")
	x.intEl("wp:is_sticky", 0) // sap13ns has no sticky posts
	for _, t := range p.Terms {
		domain := "category"
		if t.Taxonomy == "tag" {
			domain = "post_tag"
		}
		x.raw(`<category domain="` + domain + `" nicename="`)
		x.text(t.Slug)
		x.raw(`">`)
		x.cdata(html.EscapeString(t.Name))
		x.raw("</category>")
	}
	if p.FeaturedMediaID != nil {
		x.meta("_thumbnail_id", strconv.FormatInt(e.mediaBase+*p.FeaturedMediaID, 10))
	}
	x.meta(wpimport.MetaMarkdown, p.Content)
	x.meta(wpimport.MetaLang, p.Lang)
	if p.TGroup != nil {
		x.meta(wpimport.MetaTranslationGroup, strconv.FormatInt(*p.TGroup, 10))
	}
	if p.MetaDescription != "" {
		x.meta(wpimport.MetaDescription, p.MetaDescription)
	}
	if p.AssistantExclude {
		x.meta(wpimport.MetaAssistantExclude, "1")
	}
	if p.Status == store.StatusTrash {
		// What WordPress keeps for "Restore" from the trash.
		x.meta("_wp_trash_meta_status", "draft")
		x.meta("_wp_trash_meta_time", strconv.FormatInt(p.UpdatedAt.Unix(), 10))
	}
	if comments > 0 {
		if err := e.writeComments(ctx, x, p.ID); err != nil {
			return err
		}
	}
	x.raw("\n\t</item>\n")
	if p.Type == store.TypePage {
		e.rep.Pages++
	} else {
		e.rep.Posts++
	}
	return x.err
}

func (e *exporter) writeComments(ctx context.Context, x *xmlWriter, postID int64) error {
	var after int64
	// Comments come by id, so a parent always precedes its replies, as the
	// WordPress Importer needs. A reply whose parent is left out (spam,
	// trash, or pending without PendingComments) is attached to its
	// nearest exported ancestor instead, never to an id the file lacks.
	written := map[int64]bool{}
	var parents map[int64]int64
	parentOf := func(id int64) (int64, error) {
		for hops := 0; id != 0 && hops < 1000; hops++ {
			if written[id] {
				return id, nil
			}
			if parents == nil {
				var err error
				if parents, err = e.st.CommentParents(ctx, postID); err != nil {
					return 0, err
				}
			}
			id = parents[id]
		}
		return 0, nil
	}
	for {
		list, err := e.st.ExportComments(ctx, postID, e.o.commentStatuses(), after, commentBatch)
		if err != nil || len(list) == 0 {
			return err
		}
		for _, c := range list {
			email, ip := "", ""
			if e.o.PersonalData {
				email = c.AuthorEmail
				if c.IP.IsValid() && !c.IP.IsUnspecified() {
					ip = c.IP.String()
				}
			}
			approved := "0"
			if c.Status == "approved" {
				approved = "1"
			}
			var parent, user int64
			if c.ParentID != nil {
				if parent, err = parentOf(*c.ParentID); err != nil {
					return err
				}
			}
			if c.UserID != nil && e.authors[*c.UserID] {
				user = *c.UserID
			}
			x.raw("\n\t\t<wp:comment>")
			x.intEl("wp:comment_id", c.ID)
			x.cdataEl("wp:comment_author", html.EscapeString(c.AuthorName))
			x.cdataEl("wp:comment_author_email", email)
			x.cdataEl("wp:comment_author_url", c.AuthorURL)
			x.cdataEl("wp:comment_author_IP", ip)
			e.dates(x, "wp:comment_date", &c.CreatedAt)
			x.cdataEl("wp:comment_content", html.EscapeString(c.Content))
			x.cdataEl("wp:comment_approved", approved)
			x.cdataEl("wp:comment_type", "comment")
			x.intEl("wp:comment_parent", parent)
			x.intEl("wp:comment_user_id", user)
			x.raw("</wp:comment>")
			written[c.ID] = true
			e.rep.Comments++
		}
		if x.err != nil {
			return x.err
		}
		after = list[len(list)-1].ID
	}
}

// dates writes name (local time) and name_gmt.
func (e *exporter) dates(x *xmlWriter, name string, t *time.Time) {
	x.cdataEl(name, t.In(e.o.Site.Location).Format(wpDate))
	x.cdataEl(name+"_gmt", t.UTC().Format(wpDate))
}

// - - - XML - - -

// xmlWriter writes XML, keeping the first error.
type xmlWriter struct {
	w   *bufio.Writer
	err error
}

func (x *xmlWriter) raw(s string) {
	if x.err == nil {
		_, x.err = x.w.WriteString(s)
	}
}

func (x *xmlWriter) nl() { x.raw("\n") }

// text writes escaped character data.
func (x *xmlWriter) text(s string) {
	if x.err == nil {
		x.err = xml.EscapeText(x.w, []byte(Clean(s)))
	}
}

// cdata writes s as a CDATA section, split where s contains "]]>".
func (x *xmlWriter) cdata(s string) {
	x.raw("<![CDATA[")
	x.raw(strings.ReplaceAll(Clean(s), "]]>", "]]]]><![CDATA[>"))
	x.raw("]]>")
}

func (x *xmlWriter) textEl(name, s string) {
	x.raw("\t<" + name + ">")
	x.text(s)
	x.raw("</" + name + ">\n")
}

func (x *xmlWriter) cdataEl(name, s string) {
	x.raw("<" + name + ">")
	x.cdata(s)
	x.raw("</" + name + ">")
}

func (x *xmlWriter) intEl(name string, n int64) {
	x.raw("<" + name + ">" + strconv.FormatInt(n, 10) + "</" + name + ">")
}

func (x *xmlWriter) meta(key, value string) {
	x.raw("<wp:postmeta>")
	x.cdataEl("wp:meta_key", key)
	x.cdataEl("wp:meta_value", value)
	x.raw("</wp:postmeta>")
}

func (x *xmlWriter) flush() error {
	if x.err != nil {
		return x.err
	}
	return x.w.Flush()
}

// Clean makes s safe for XML 1.0: invalid UTF-8 becomes U+FFFD, and the
// characters XML does not allow are dropped (C0 controls other than tab,
// newline and carriage return, and U+FFFE and U+FFFF).
func Clean(s string) string {
	ok := utf8.ValidString(s)
	if ok {
		for _, r := range s {
			if !xmlChar(r) {
				ok = false
				break
			}
		}
	}
	if ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s { // invalid bytes come as utf8.RuneError
		if xmlChar(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func xmlChar(r rune) bool {
	switch {
	case r == '\t' || r == '\n' || r == '\r':
		return true
	case r < 0x20, r == 0xFFFE, r == 0xFFFF:
		return false
	}
	return true
}

// limitWriter fails with ErrTooLarge once more than remaining bytes are
// written, and counts them.
type limitWriter struct {
	w         io.Writer
	remaining int64
	n         int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > l.remaining {
		return 0, ErrTooLarge
	}
	n, err := l.w.Write(p)
	l.remaining -= int64(n)
	l.n += int64(n)
	return n, err
}
