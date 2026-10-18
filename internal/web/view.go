package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
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

// ------------------------------------------------------------------ URLs --

func termURL(t *store.Term) string {
	if t.Taxonomy == "tag" {
		return "/tag/" + t.Slug
	}
	return "/category/" + t.Slug
}

func authorURL(username string) string { return "/author/" + username }

// PostInfo implements plugin.Links.
func (s *Server) PostInfo(p *store.Post) plugin.PostInfo {
	info := plugin.PostInfo{
		ID: p.ID, Type: p.Type, Title: p.Title, Slug: p.Slug, Lang: p.Lang, URL: s.cfg.AbsURL(s.postURL(p)),
		Excerpt: content.Summary(p.Excerpt, p.Content, 300), Author: p.AuthorName,
		Categories: []string{}, Tags: []string{}, WordCount: content.WordCount(p.Content),
	}
	if p.PublishedAt != nil {
		info.PublishedAt = p.PublishedAt.UTC().Format(time.RFC3339)
	}
	for _, t := range p.Terms {
		if t.Taxonomy == "tag" {
			info.Tags = append(info.Tags, t.In(p.Lang).Name)
		} else {
			info.Categories = append(info.Categories, t.In(p.Lang).Name)
		}
		tr := t.In(p.Lang)
		info.Terms = append(info.Terms, plugin.TermInfo{Taxonomy: t.Taxonomy, Slug: t.Slug, Name: tr.Name,
			URL: s.cfg.AbsURL(s.langPath(p.Lang, termURL(&tr)))})
	}
	if info.Terms == nil {
		info.Terms = []plugin.TermInfo{}
	}
	if img := imageView(p.Featured); img != nil {
		info.Image = s.cfg.AbsURL(img.URL)
	}
	return info
}

// ----------------------------------------------------------- view models --

type TermView struct {
	Name        string
	Slug        string
	URL         string
	Count       int
	Description string
}

type ImageView struct {
	URL     string
	Srcset  string
	Width   int
	Height  int
	Alt     string
	Caption string
	Thumb   string
	Medium  string
	Large   string
}

type AuthorView struct {
	Name     string
	Username string
	URL      string
	Initials string
}

type PostView struct {
	ID             int64
	Type           string
	Title          string
	Slug           string
	Lang           string
	URL            string
	AbsURL         string
	Summary        string
	Content        template.HTML
	Published      time.Time
	HasDate        bool
	Updated        time.Time
	Author         AuthorView
	Categories     []TermView
	Tags           []TermView
	Image          *ImageView
	ReadingMinutes int
	CommentCount   int
	CommentsOpen   bool
	EditURL        string
	Status         string
	Scheduled      bool
}

func imageView(m *store.Media) *ImageView {
	if m == nil || !m.IsImage() {
		return nil
	}
	return &ImageView{URL: m.URL(), Srcset: m.Srcset(), Width: m.Width, Height: m.Height, Alt: m.Alt, Caption: m.Caption,
		Thumb: m.VariantURL("thumb"), Medium: m.VariantURL("medium"), Large: m.VariantURL("large")}
}

// termViews converts terms for display in lang (translated names, prefixed URLs).
func (s *Server) termViews(ts []store.Term, lang string) []TermView {
	out := make([]TermView, 0, len(ts))
	for i := range ts {
		t := ts[i].In(lang)
		out = append(out, TermView{Name: t.Name, Slug: t.Slug, URL: s.langPath(lang, termURL(&t)), Count: t.Count, Description: t.Description})
	}
	return out
}

func (s *Server) postView(p *store.Post, viewer *store.User) *PostView {
	now := time.Now()
	u := s.postURL(p)
	v := &PostView{
		ID: p.ID, Type: p.Type, Title: p.Title, Slug: p.Slug, Lang: p.Lang, URL: u, AbsURL: s.cfg.AbsURL(u),
		Summary: content.Summary(p.Excerpt, p.Content, 220),
		Updated: p.UpdatedAt.In(s.loc()),
		Author: AuthorView{Name: p.AuthorName, Username: p.AuthorUsername, URL: s.langPath(p.Lang, authorURL(p.AuthorUsername)),
			Initials: initials(p.AuthorName)},
		Categories:     s.termViews(p.Categories(), p.Lang),
		Tags:           s.termViews(p.Tags(), p.Lang),
		Image:          imageView(p.Featured),
		ReadingMinutes: content.ReadingMinutes(p.Content),
		CommentCount:   p.CommentCount,
		CommentsOpen:   s.commentsOpen(p),
		Status:         p.Status,
		Scheduled:      p.IsScheduled(now),
	}
	if p.PublishedAt != nil {
		v.Published, v.HasDate = p.PublishedAt.In(s.loc()), true
	}
	if p.AuthorUsername == "" {
		v.Author.URL = ""
	}
	if viewer != nil && canEditPost(viewer, p) {
		v.EditURL = fmt.Sprintf("/admin/posts/%d", p.ID)
	}
	return v
}

func (s *Server) commentsOpen(p *store.Post) bool {
	st := s.settings()
	if !st.CommentsEnabled || p.CommentStatus != "open" || p.Type != store.TypePost {
		return false
	}
	if st.CommentsCloseDays > 0 && p.PublishedAt != nil && time.Since(*p.PublishedAt) > time.Duration(st.CommentsCloseDays)*24*time.Hour {
		return false
	}
	return true
}

func canEditPost(u *store.User, p *store.Post) bool {
	if p.Type == store.TypePage {
		return auth.Can(u.Role, auth.CapManagePages)
	}
	if auth.Can(u.Role, auth.CapEditOthersPosts) {
		return true
	}
	return auth.Can(u.Role, auth.CapEditOwnPosts) && p.AuthorID != nil && *p.AuthorID == u.ID
}

// renderBody turns post Markdown into HTML through shortcodes, sanitizer and
// content.filter plugins. Results are cached per post version.
func (s *Server) renderBody(ctx context.Context, p *store.Post) template.HTML {
	key := fmt.Sprintf("%d:%d:%d", p.ID, p.UpdatedAt.UnixNano(), s.plugins.Generation())
	if e, ok := s.content.Get(key); ok {
		return template.HTML(e.Body)
	}
	info := s.PostInfo(p)
	// Icon placeholders stay as they are until every filter has run: each
	// filter's output is sanitised again, and <svg> would not survive that.
	html := content.Render(p.Content, s.plugins.HasShortcode, func(sc content.Shortcode) (string, error) {
		return s.plugins.Shortcode(ctx, sc.Name, sc.Attrs, sc.Inner, &info)
	}, nil)
	filtered := s.plugins.FilterContent(ctx, string(html), &info, func(h string) string {
		return string(content.SanitizePluginHTML(h, nil))
	})
	filtered = content.ExpandIcons(filtered, func(name string) string { return icons.SVG(name, "") })
	s.content.Set(key, &cache.Entry{Body: []byte(filtered)})
	return template.HTML(filtered)
}

// -------------------------------------------------------------- site data --

type MenuLink struct {
	Label    string
	URL      string
	External bool
	Active   bool
}

type ArchiveLink struct {
	Label string
	URL   string
	Count int
}

type Viewer struct {
	Name     string
	Username string
	Role     string
	Initials string
	Admin    bool
	CSRF     string
}

type SiteStats struct {
	Posts      int
	Pages      int
	Categories int
	Tags       int
	Comments   int
}

type SiteView struct {
	Title             string
	Tagline           string
	URL               string // absolute home URL in the current language
	Home              string // home path in the current language ("/" or "/en/")
	Search            string // search path in the current language
	Lang              string
	FooterText        string
	Year              int
	Theme             string
	Options           map[string]string
	Menus             map[string][]MenuLink
	Categories        []TermView
	Tags              []TermView
	Pages             []MenuLink
	Archives          []ArchiveLink
	Stats             SiteStats
	Viewer            *Viewer
	AllowRegistration bool
	FeedRSS           string
	FeedAtom          string
	FeedJSON          string
	theme             *theme.Theme
	server            *Server
}

// Asset returns the cache-busted URL of a file in the active theme's static/.
func (s *SiteView) Asset(file string) string { return s.theme.StaticURL(file) }

// Static returns the URL of a core static file (site.js, theme-init.js).
func (s *SiteView) Static(file string) string { return s.server.assetURL(file) }

// Menu returns menu links for a location, falling back to published pages
// for the primary location when no menu was configured.
func (s *SiteView) Menu(location string) []MenuLink {
	if m, ok := s.Menus[location]; ok && len(m) > 0 {
		return m
	}
	if location == "primary" {
		return s.Pages
	}
	return nil
}

// siteData is the cached, viewer-independent part of SiteView, per language.
type siteData struct {
	gen        uint64
	lang       string
	menus      map[string][]store.MenuItem
	categories []TermView
	tags       []TermView
	pages      []MenuLink
	archives   []ArchiveLink
	stats      SiteStats
	widgets    map[string][]store.Widget
}

type siteCache struct {
	mu   sync.Mutex
	data map[string]*siteData
	gen  uint64
}

func (c *siteCache) invalidate() {
	c.mu.Lock()
	c.gen++
	c.mu.Unlock()
}

func (s *Server) loadSiteData(ctx context.Context, lang string) (*siteData, error) {
	s.site.mu.Lock()
	defer s.site.mu.Unlock()
	if d := s.site.data[lang]; d != nil && d.gen == s.site.gen {
		return d, nil
	}
	d := &siteData{gen: s.site.gen, lang: lang}
	var err error
	if d.menus, err = s.store.AllMenus(ctx); err != nil {
		return nil, err
	}
	all, err := s.store.AllWidgets(ctx)
	if err != nil {
		return nil, err
	}
	d.widgets = map[string][]store.Widget{}
	for area, list := range all {
		for _, wd := range list {
			if wd.Lang == "" || wd.Lang == lang {
				d.widgets[area] = append(d.widgets[area], wd)
			}
		}
	}
	cats, err := s.store.ListTerms(ctx, "category", lang)
	if err != nil {
		return nil, err
	}
	for _, c := range s.termViews(cats, lang) {
		if c.Count > 0 {
			d.categories = append(d.categories, c)
		}
	}
	tags, err := s.store.PopularTerms(ctx, "tag", lang, 30)
	if err != nil {
		return nil, err
	}
	d.tags = s.termViews(tags, lang)
	pages, _, err := s.store.ListPosts(ctx, store.PostQuery{Type: store.TypePage, Lang: lang, PublicOnly: true, Order: "menu", Limit: 50})
	if err != nil {
		return nil, err
	}
	front := s.frontPageIDs(ctx)
	for i := range pages {
		if front[pages[i].ID] {
			continue
		}
		d.pages = append(d.pages, MenuLink{Label: pages[i].Title, URL: s.postURL(&pages[i])})
	}
	months, err := s.store.ArchiveMonths(ctx, s.loc().String(), lang, 24)
	if err != nil {
		return nil, err
	}
	for _, m := range months {
		name := i18n.MonthName(lang, time.Month(m.Month))
		d.archives = append(d.archives, ArchiveLink{
			Label: upperFirst(name) + " " + strconv.Itoa(m.Year),
			URL:   s.langPath(lang, fmt.Sprintf("/archive/%d/%02d", m.Year, m.Month)), Count: m.Count,
		})
	}
	err = s.store.DB.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM posts WHERE type = 'post' AND lang = $1 AND status = 'published' AND published_at <= now())::int,
		(SELECT count(*) FROM posts WHERE type = 'page' AND lang = $1 AND status = 'published' AND published_at <= now())::int,
		(SELECT count(*) FROM terms WHERE taxonomy = 'category')::int,
		(SELECT count(*) FROM terms WHERE taxonomy = 'tag')::int,
		(SELECT count(*) FROM comments c JOIN posts p ON p.id = c.post_id WHERE c.status = 'approved' AND p.lang = $1)::int`, lang).
		Scan(&d.stats.Posts, &d.stats.Pages, &d.stats.Categories, &d.stats.Tags, &d.stats.Comments)
	if err != nil {
		return nil, err
	}
	if s.site.data == nil {
		s.site.data = map[string]*siteData{}
	}
	s.site.data[lang] = d
	return d, nil
}

// frontPageIDs returns the static front page and its translations.
func (s *Server) frontPageIDs(ctx context.Context) map[int64]bool {
	st := s.settings()
	out := map[int64]bool{}
	if st.FrontPage != "page" || st.FrontPageID == 0 {
		return out
	}
	out[st.FrontPageID] = true
	if p, err := s.store.PostByID(ctx, st.FrontPageID); err == nil {
		if trs, err := s.store.Translations(ctx, p); err == nil {
			for _, t := range trs {
				out[t.ID] = true
			}
		}
	}
	return out
}

func (s *Server) siteView(r *http.Request, th *theme.Theme, d *siteData) *SiteView {
	st := s.settings()
	opts, err := s.store.ThemeOptions(r.Context(), th.Slug)
	if err != nil {
		opts = map[string]string{}
	}
	lang, here := d.lang, s.here(r)
	text := st.Text(lang)
	sv := &SiteView{
		Title: text.SiteTitle, Tagline: text.Tagline, URL: s.cfg.AbsURL(s.langPath(lang, "/")), Lang: lang,
		Home:       s.langPath(lang, "/"),
		FooterText: text.FooterText, Year: time.Now().In(s.loc()).Year(), Theme: th.Slug,
		Options: th.OptionValues(opts), Menus: map[string][]MenuLink{},
		Categories: d.categories, Tags: d.tags, Pages: markActive(d.pages, here), Archives: d.archives, Stats: d.stats,
		AllowRegistration: st.AllowRegistration,
		FeedRSS:           s.langPath(lang, "/feed.xml"), FeedAtom: s.langPath(lang, "/feed.atom"), FeedJSON: s.langPath(lang, "/feed.json"),
		Search: s.langPath(lang, "/search"),
		theme:  th, server: s,
	}
	// A language's own menu replaces the shared one for the same location.
	for key, items := range d.menus {
		loc, l, _ := strings.Cut(key, "@")
		if l != "" && l != lang {
			continue
		}
		if l == "" {
			if _, own := d.menus[store.MenuKey(loc, lang)]; own && lang != "" {
				continue
			}
		}
		links := make([]MenuLink, 0, len(items))
		for _, it := range items {
			links = append(links, MenuLink{Label: it.Label, URL: it.URL, External: isExternal(it.URL)})
		}
		sv.Menus[loc] = markActive(links, here)
	}
	if u := currentUser(r); u != nil {
		sv.Viewer = &Viewer{Name: u.Name(), Username: u.Username, Role: u.Role, Initials: initials(u.Name()),
			Admin: auth.Can(u.Role, auth.CapEditOwnPosts) || auth.Can(u.Role, auth.CapManageSettings)}
		if sess := rctx(r).Session; sess != nil {
			sv.Viewer.CSRF = sess.CSRFToken
		}
	}
	return sv
}

func markActive(links []MenuLink, path string) []MenuLink {
	out := make([]MenuLink, len(links))
	for i, l := range links {
		l.Active = l.URL == path || (l.URL != "/" && !l.External && strings.HasPrefix(path, strings.TrimSuffix(l.URL, "/")+"/"))
		out[i] = l
	}
	return out
}

func isExternal(u string) bool {
	pu, err := url.Parse(u)
	return err == nil && pu.Host != ""
}

// -------------------------------------------------------------- page data --

type Pagination struct {
	Page       int
	TotalPages int
	Total      int
	PrevURL    string
	NextURL    string
	Links      []PageLink
}

type PageLink struct {
	Num     int
	URL     string
	Current bool
	Gap     bool
}

func newPagination(base string, query url.Values, page, perPage, total int) *Pagination {
	pages := (total + perPage - 1) / perPage
	if pages <= 1 {
		return nil
	}
	link := func(n int) string {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		if n <= 1 {
			q.Del("page")
		} else {
			q.Set("page", strconv.Itoa(n))
		}
		if enc := q.Encode(); enc != "" {
			return base + "?" + enc
		}
		return base
	}
	p := &Pagination{Page: page, TotalPages: pages, Total: total}
	if page > 1 {
		p.PrevURL = link(page - 1)
	}
	if page < pages {
		p.NextURL = link(page + 1)
	}
	last := 0
	for n := 1; n <= pages; n++ {
		if n == 1 || n == pages || (n >= page-2 && n <= page+2) {
			if last != 0 && n-last > 1 {
				p.Links = append(p.Links, PageLink{Gap: true})
			}
			p.Links = append(p.Links, PageLink{Num: n, URL: link(n), Current: n == page})
			last = n
		}
	}
	return p
}

type FeedLink struct {
	Title string
	Type  string
	URL   string
}

type ArchiveView struct {
	Kind        string // category | tag | author | date
	Title       string
	Description string
	FeedURL     string
	Count       int
}

type CommentView struct {
	ID       int64
	Author   string
	Initials string
	URL      string
	Content  string
	Date     time.Time
	IsAuthor bool
	Replies  []*CommentView
}

type CommentForm struct {
	Action       string
	Token        string
	LoggedIn     bool
	ViewerName   string
	RequireLogin bool
	CSRF         string
	Notice       string // pending | approved | error | closed
}

type WidgetView struct {
	ID       int64
	Kind     string
	Title    string
	HTML     template.HTML
	Posts    []*PostView
	Terms    []TermView
	Archives []ArchiveLink
	Links    []FeedLink
}

type PageData struct {
	Site        *SiteView
	Kind        string // home | single | page | archive | search | 404
	Title       string
	Heading     string
	Description string
	Canonical   string
	OGType      string
	OGImage     string
	NoIndex     bool
	Alternates  []FeedLink
	Meta        []plugin.MetaTag
	JSONLD      any
	Posts       []*PostView
	Post        *PostView
	Archive     *ArchiveView
	Pagination  *Pagination
	Query       string
	Comments    []*CommentView
	CommentForm *CommentForm
	Prev        *PostView
	Next        *PostView
	Widgets     map[string][]WidgetView
	// Translations links this page in every site language (current included);
	// empty on single-language sites.
	Translations []LangLink
	IsFront      bool
	Preview      bool
	Ask          *AskView // the assistant page without JavaScript (assistant.go)
	// Plugins (plugin_web.go): JSON-LD objects from head.meta, already
	// escaped for a <script type="application/ld+json"> element, and the
	// stylesheets and scripts of enabled plugins.
	PluginLD  []template.JS
	PluginCSS []string
	PluginJS  []string
}

// LangLink points to a page in one of the site languages.
type LangLink struct {
	Code    string // BCP 47, e.g. "es", "en"
	Name    string // endonym, e.g. "Español"
	URL     string
	AbsURL  string
	Current bool
	Alt     bool // URL is this page's own version (an hreflang alternate)
}

// PlanView is a hosting plan offered by the network (sap13ns.site and other
// hosting providers). Themes list them with {{range plans}}.
type PlanView struct {
	Slug        string
	Name        string
	Description string
	Price       string // formatted, e.g. "5 €"; empty when free
	Interval    string // "month" | "year" | ""
	Free        bool
	Commercial  bool // allowed for commercial sites
	Highlight   bool
	Features    []string
	SignupURL   string
}

// Area returns the widgets placed in a theme area.
func (p *PageData) Area(id string) []WidgetView { return p.Widgets[id] }

// DocTitle builds the <title> content.
func (p *PageData) DocTitle() string {
	if p.Title == "" || p.IsFront {
		if p.Site.Tagline != "" {
			return p.Site.Title + " — " + p.Site.Tagline
		}
		return p.Site.Title
	}
	return p.Title + " · " + p.Site.Title
}
