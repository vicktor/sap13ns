package wpimport

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Values used by sap13ns (they mirror the store package, which this package
// deliberately does not import).
const (
	TypePost = "post"
	TypePage = "page"

	StatusPublished = "published"
	StatusDraft     = "draft"
	StatusPending   = "pending"
	StatusTrash     = "trash"

	CommentApproved = "approved"
	CommentPending  = "pending"
	CommentSpam     = "spam"
	CommentTrash    = "trash"

	TaxonomyCategory = "category"
	TaxonomyTag      = "tag"
)

// Column limits of the sap13ns schema; longer values are truncated rather
// than failing the item.
const (
	maxTitleRunes   = 300
	maxExcerptRunes = 2000
	maxAltRunes     = 500
	maxCommentRunes = 10000
	maxNameRunes    = 100
	maxWarnings     = 500
	defaultMaxFiles = 2000
)

// Sink receives the imported content. The web layer implements it on top of
// the store; Import never calls the write methods in a dry run.
type Sink interface {
	// EnsureUser returns the CMS user for a WordPress author, creating one
	// (without a usable password) when no user has the same login or e-mail.
	EnsureUser(ctx context.Context, a Author) (int64, error)
	// EnsureTerm returns the term with t.Slug in taxonomy (TaxonomyCategory
	// or TaxonomyTag), creating it under parentID when missing. t.Slug is
	// already sanitised; t.Parent is the parent's WordPress nicename.
	EnsureTerm(ctx context.Context, taxonomy string, t Term, parentID *int64) (int64, error)
	// PostExists reports whether slug is taken for typ (TypePost, TypePage).
	// Slugs that would clash with routes may be reported as taken too.
	PostExists(ctx context.Context, typ, slug string) (bool, error)
	CreatePost(ctx context.Context, p Post) (int64, error)
	// CreateComment stores c under postID; c.Status, c.Text, c.Date and
	// c.NewUserID are the fields to persist.
	CreateComment(ctx context.Context, postID int64, c Comment, parentID *int64) (int64, error)
	// StoreMedia validates and stores a downloaded file and returns its ID
	// and public URL. r fails with ErrMediaTooLarge past the size limit.
	StoreMedia(ctx context.Context, name string, r io.Reader, alt string) (id int64, url string, err error)
	// AddRedirect records a permanent redirect from an old path (with its
	// query for sites on plain permalinks, "/?p=12") to a new URL or path.
	AddRedirect(ctx context.Context, from, to string) error
}

// Post is a post or a page ready to be stored.
type Post struct {
	Type            string // TypePost or TypePage
	Status          string // StatusPublished (PublishedAt may lie in the future), StatusDraft, StatusPending, StatusTrash
	Title           string // plain text
	Slug            string // [a-z0-9-], unique for Type within the import
	Excerpt         string // plain text
	Content         string // Markdown
	AuthorID        int64
	PublishedAt     *time.Time // nil for drafts and pending posts
	ModifiedAt      *time.Time
	ParentID        *int64 // pages only
	MenuOrder       int
	CommentStatus   string // "open" or "closed"
	Sticky          bool
	FeaturedMediaID *int64
	TermIDs         []int64
	OldURL          string // WordPress permalink
	OldID           int64  // WordPress post ID
	Template        string // _wp_page_template, "" for the default template
}

type Options struct {
	DryRun        bool // plan and count, but call no Sink write method and download nothing
	DownloadMedia bool // download attachments, rewrite their URLs and map featured images
	SkipExisting  bool // skip items whose slug is taken; otherwise pick a free "-2" slug
	IncludeTrash  bool // import trashed posts and comments as trash instead of skipping them
	// DefaultAuthorID owns content whose author could not be imported. Required.
	DefaultAuthorID int64
	MaxMediaBytes   int64 // per file; 0 means 32 MiB
	MaxMediaFiles   int   // 0 means 2000
	// MediaHosts are extra hosts the default fetcher may download from (a
	// CDN); the export's own site hosts are always allowed.
	MediaHosts []string
	// Fetcher downloads media; nil uses an HTTPFetcher limited to the
	// export's hosts and MediaHosts.
	Fetcher Fetcher
	Now     func() time.Time
	// PostURL returns the new public URL of a post or page; nil means
	// "/blog/<slug>" for posts and "/<slug>" for pages.
	PostURL func(typ, slug string) string
}

// Count tallies one kind of object. For users and terms, Created counts the
// ones ensured: created or matched to existing ones.
type Count struct {
	Created int
	Skipped int
	Failed  int
}

type Counts struct {
	Users      Count
	Categories Count
	Tags       Count
	Media      Count
	Pages      Count
	Posts      Count
	Comments   Count
	Redirects  Count
}

// Mapping relates WordPress objects to the new ones. In a dry run IDs are
// negative placeholders.
type Mapping struct {
	Users    map[string]int64  // login → user ID
	Terms    map[string]int64  // "category/slug" or "tag/slug" → term ID
	Media    map[int64]int64   // attachment ID → media ID
	Posts    map[int64]int64   // post or page ID → post ID
	Comments map[int64]int64   // comment ID → comment ID
	URLs     map[string]string // old URL → new URL (posts, pages and media)
}

type Report struct {
	DryRun     bool
	StartedAt  time.Time
	FinishedAt time.Time
	Counts     Counts
	// Ignored counts what was not imported by nature: other post types
	// (nav_menu_item, revision, wp_block…), pingbacks and trackbacks.
	Ignored         map[string]int
	Warnings        []string // at most 500
	WarningsDropped int
	Mapping         Mapping
}

// Summary renders the counts as text, one kind per line.
func (r *Report) Summary() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("Dry run: nothing was written.\n")
	}
	for _, k := range []struct {
		name string
		c    Count
	}{
		{"users", r.Counts.Users}, {"categories", r.Counts.Categories}, {"tags", r.Counts.Tags},
		{"media", r.Counts.Media}, {"pages", r.Counts.Pages}, {"posts", r.Counts.Posts},
		{"comments", r.Counts.Comments}, {"redirects", r.Counts.Redirects},
	} {
		fmt.Fprintf(&b, "%s: %d created, %d skipped, %d failed\n", k.name, k.c.Created, k.c.Skipped, k.c.Failed)
	}
	if len(r.Ignored) > 0 {
		keys := make([]string, 0, len(r.Ignored))
		for k := range r.Ignored {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = fmt.Sprintf("%s %d", k, r.Ignored[k])
		}
		fmt.Fprintf(&b, "ignored: %s\n", strings.Join(parts, ", "))
	}
	if n := len(r.Warnings) + r.WarningsDropped; n > 0 {
		fmt.Fprintf(&b, "warnings: %d\n", n)
	}
	return b.String()
}

type mediaRef struct {
	id  int64
	url string // "" in a dry run
}

type plan struct {
	it      *Item
	status  string
	slug    string
	url     string
	skip    bool
	newID   int64
	created bool
	public  bool // published with a date that has passed
}

type importer struct {
	exp  *Export
	sink Sink
	opts Options
	rep  *Report
	now  time.Time

	fakeID    int64
	baseURL   string
	siteHosts map[string]bool

	usersByLogin map[string]int64
	usersByWPID  map[int64]int64
	unknownUsers map[string]bool
	terms        map[string]int64 // "taxonomy/slug"
	failedTerms  map[string]bool
	attachments  map[int64]*Item
	media        map[int64]mediaRef
	mediaURLs    map[string]string // host+path → new URL
	mediaStems   map[string]string // host+stem+ext → new URL, for size variants
	links        map[string]string // host+path(+query) of old permalinks → new URL
	planned      []*plan           // pages and posts, in export order
	plans        map[int64]*plan   // planned by WordPress ID
	order        []*plan           // created pages and posts, in creation order
	redirected   map[string]bool
}

// Import writes exp to sink in dependency order: authors, terms (parents
// first), attachments, pages (parents first), posts, comments (parents
// first), then redirects. Per-item failures are reported and skipped; only
// invalid options and context cancellation stop the import, in which case
// the partial report is returned with the error.
func Import(ctx context.Context, exp *Export, sink Sink, opts Options) (*Report, error) {
	im := &importer{
		exp: exp, sink: sink, opts: opts,
		siteHosts:    map[string]bool{},
		usersByLogin: map[string]int64{}, usersByWPID: map[int64]int64{}, unknownUsers: map[string]bool{},
		terms: map[string]int64{}, failedTerms: map[string]bool{}, attachments: map[int64]*Item{}, media: map[int64]mediaRef{},
		mediaURLs: map[string]string{}, mediaStems: map[string]string{}, links: map[string]string{},
		plans: map[int64]*plan{}, redirected: map[string]bool{},
	}
	if im.opts.Now == nil {
		im.opts.Now = time.Now
	}
	if im.opts.PostURL == nil {
		im.opts.PostURL = defaultPostURL
	}
	if im.opts.MaxMediaBytes <= 0 {
		im.opts.MaxMediaBytes = defaultMaxMedia
	}
	if im.opts.MaxMediaFiles <= 0 {
		im.opts.MaxMediaFiles = defaultMaxFiles
	}
	im.now = im.opts.Now().UTC()
	im.rep = &Report{
		DryRun: opts.DryRun, StartedAt: im.now, Ignored: map[string]int{},
		Mapping: Mapping{
			Users: map[string]int64{}, Terms: map[string]int64{}, Media: map[int64]int64{},
			Posts: map[int64]int64{}, Comments: map[int64]int64{}, URLs: map[string]string{},
		},
	}
	switch {
	case exp == nil:
		return im.finish(), errors.New("wpimport: nil export")
	case sink == nil:
		return im.finish(), errors.New("wpimport: nil sink")
	case opts.DefaultAuthorID <= 0:
		return im.finish(), errors.New("wpimport: DefaultAuthorID is required")
	}
	for k, n := range exp.Ignored {
		im.rep.Ignored[k] += n
	}
	for _, s := range []string{exp.Site.Link, exp.Site.BaseBlogURL, exp.Site.BaseSiteURL} {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			im.siteHosts[normHost(u.Host)] = true
			if im.baseURL == "" {
				im.baseURL = s
			}
		}
	}
	for i := range exp.Items {
		if it := &exp.Items[i]; it.Type == "attachment" {
			im.attachments[it.ID] = it
		}
	}

	for _, step := range []func(context.Context) error{
		im.importUsers, im.importTerms, im.importMedia, im.plan, im.importPages, im.importPosts,
		im.importComments, im.importRedirects,
	} {
		if err := ctx.Err(); err != nil {
			return im.finish(), err
		}
		if err := step(ctx); err != nil {
			return im.finish(), err
		}
	}
	return im.finish(), nil
}

func defaultPostURL(typ, slug string) string {
	if typ == TypePage {
		return "/" + slug
	}
	return "/blog/" + slug
}

func (im *importer) finish() *Report {
	im.rep.FinishedAt = im.opts.Now().UTC()
	return im.rep
}

func (im *importer) warn(format string, args ...any) {
	if len(im.rep.Warnings) >= maxWarnings {
		im.rep.WarningsDropped++
		return
	}
	im.rep.Warnings = append(im.rep.Warnings, fmt.Sprintf(format, args...))
}

// failed records a per-item error, or returns the context error when the
// failure comes from cancellation and the import must stop.
func (im *importer) failed(ctx context.Context, c *Count, format string, args ...any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Failed++
	im.warn(format, args...)
	return nil
}

func (im *importer) fake() int64 {
	im.fakeID--
	return im.fakeID
}

// - - - users and terms - - -

func (im *importer) importUsers(ctx context.Context) error {
	c := &im.rep.Counts.Users
	for _, a := range im.exp.Authors {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, dup := im.usersByLogin[a.Login]; dup {
			continue
		}
		a.DisplayName = truncateRunes(a.DisplayName, maxNameRunes)
		id := im.fake()
		if !im.opts.DryRun {
			var err error
			if id, err = im.sink.EnsureUser(ctx, a); err != nil {
				if err := im.failed(ctx, c, "user %q: %v; their content goes to the default author", a.Login, err); err != nil {
					return err
				}
				im.unknownUsers[a.Login] = true // already reported
				continue
			}
		}
		c.Created++
		im.usersByLogin[a.Login] = id
		if a.ID > 0 {
			im.usersByWPID[a.ID] = id
		}
		im.rep.Mapping.Users[a.Login] = id
	}
	return nil
}

// termSlug sanitises a nicename. Slugs without an ASCII transliteration
// (日本, Ελλάδα) get a stable hash, so that the channel's term list, item
// references and parent links still agree.
func termSlug(t Term) string {
	raw := t.Slug
	if strings.TrimSpace(raw) == "" {
		raw = plainTextOf(t.Name)
	}
	if s := Slugify(raw); s != "" {
		return s
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	if dec, err := url.PathUnescape(raw); err == nil {
		raw = dec
	}
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return "term-" + hex.EncodeToString(sum[:4])
}

// ensureTerm returns the ID of a term, creating it once. ok is false when
// the term could not be created (already reported).
func (im *importer) ensureTerm(ctx context.Context, taxonomy string, t Term, parentID *int64) (id int64, ok bool, err error) {
	c := &im.rep.Counts.Categories
	if taxonomy == TaxonomyTag {
		c = &im.rep.Counts.Tags
	}
	t.Slug = termSlug(t)
	if t.Slug == "" {
		c.Skipped++
		im.warn("%s %q has no usable slug", taxonomy, t.Name)
		return 0, false, nil
	}
	key := taxonomy + "/" + t.Slug
	if id, ok := im.terms[key]; ok {
		return id, true, nil
	}
	if im.failedTerms[key] {
		return 0, false, nil
	}
	t.Name = truncateRunes(plainTextOf(t.Name), maxNameRunes)
	if t.Name == "" {
		t.Name = t.Slug
	}
	t.Description = plainTextOf(t.Description)
	id = im.fake()
	if !im.opts.DryRun {
		if id, err = im.sink.EnsureTerm(ctx, taxonomy, t, parentID); err != nil {
			im.failedTerms[key] = true
			return 0, false, im.failed(ctx, c, "%s %q: %v", taxonomy, t.Slug, err)
		}
	}
	c.Created++
	im.terms[key] = id
	im.rep.Mapping.Terms[key] = id
	return id, true, nil
}

func (im *importer) importTerms(ctx context.Context) error {
	// Categories, parents first.
	cats := map[string]*Term{}
	var keys []string
	for i := range im.exp.Categories {
		t := &im.exp.Categories[i]
		k := termSlug(*t)
		if _, dup := cats[k]; !dup {
			cats[k] = t
			keys = append(keys, k)
		}
	}
	done := map[string]bool{}
	var visit func(k string, depth int) error
	visit = func(k string, depth int) error {
		t := cats[k]
		if t == nil || done[k] {
			return nil
		}
		done[k] = true
		var parentID *int64
		if p := termSlug(Term{Slug: t.Parent}); t.Parent != "" && p != "" && p != k {
			if cats[p] == nil {
				im.warn("category %q: parent %q is not in the export", t.Slug, t.Parent)
			} else if depth > 100 {
				im.warn("category %q: parent chain too deep", t.Slug)
			} else {
				if err := visit(p, depth+1); err != nil {
					return err
				}
				if id, ok := im.terms[TaxonomyCategory+"/"+p]; ok {
					parentID = &id
				}
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _, err := im.ensureTerm(ctx, TaxonomyCategory, *t, parentID)
		return err
	}
	for _, k := range keys {
		if err := visit(k, 0); err != nil {
			return err
		}
	}
	for _, t := range im.exp.Tags {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, _, err := im.ensureTerm(ctx, TaxonomyTag, t, nil); err != nil {
			return err
		}
	}
	return nil
}

// - - - media - - -

var (
	reSizeVariant = regexp.MustCompile(`^(.+)-\d{1,5}x\d{1,5}(\.[A-Za-z0-9]{1,8})$`)
	reStemSuffix  = regexp.MustCompile(`-(?:scaled|rotated)$`)
)

func hostPath(u *url.URL) string { return normHost(u.Host) + u.Path }

func (im *importer) registerMedia(u *url.URL, newURL string) {
	if newURL == "" {
		return
	}
	im.mediaURLs[hostPath(u)] = newURL
	ext := ""
	if i := strings.LastIndexByte(u.Path, '.'); i > strings.LastIndexByte(u.Path, '/') {
		ext = u.Path[i:]
	}
	stem := strings.TrimSuffix(u.Path, ext)
	host := normHost(u.Host)
	for _, s := range []string{stem, reStemSuffix.ReplaceAllString(stem, "")} {
		if _, taken := im.mediaStems[host+s+ext]; !taken {
			im.mediaStems[host+s+ext] = newURL
		}
	}
}

// lookupMedia maps an old media URL, including WordPress size variants
// ("photo-300x200.jpg") and the original of a "-scaled" image, to its new URL.
func (im *importer) lookupMedia(u *url.URL) (string, bool) {
	key := hostPath(u)
	if v, ok := im.mediaURLs[key]; ok {
		return v, true
	}
	if v, ok := im.mediaStems[key]; ok {
		return v, true
	}
	if m := reSizeVariant.FindStringSubmatch(u.Path); m != nil {
		v, ok := im.mediaStems[normHost(u.Host)+m[1]+m[2]]
		return v, ok
	}
	return "", false
}

func (im *importer) importMedia(ctx context.Context) error {
	c := &im.rep.Counts.Media
	var atts []*Item
	for i := range im.exp.Items {
		if it := &im.exp.Items[i]; it.Type == "attachment" {
			atts = append(atts, it)
		}
	}
	if !im.opts.DownloadMedia {
		c.Skipped += len(atts)
		return nil
	}
	fetcher := im.opts.Fetcher
	if fetcher == nil && !im.opts.DryRun {
		hosts := slices.Clone(im.opts.MediaHosts)
		for h := range im.siteHosts {
			hosts = append(hosts, h)
		}
		fetcher = &HTTPFetcher{AllowedHosts: hosts, MaxBytes: im.opts.MaxMediaBytes}
	}
	seen := map[string]mediaRef{}
	files := 0
	for _, it := range atts {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := it.AttachmentURL
		if src == "" {
			src = it.GUID
		}
		u, err := url.Parse(src)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			c.Failed++
			im.warn("attachment %d: invalid URL %q", it.ID, src)
			continue
		}
		if ref, ok := seen[hostPath(u)]; ok {
			im.media[it.ID] = ref
			im.rep.Mapping.Media[it.ID] = ref.id
			continue
		}
		if files >= im.opts.MaxMediaFiles {
			if c.Skipped == 0 {
				im.warn("media limit of %d files reached; the remaining attachments keep their old URLs", im.opts.MaxMediaFiles)
			}
			c.Skipped++
			continue
		}
		files++
		ref := mediaRef{id: im.fake()}
		if !im.opts.DryRun {
			ref.id, ref.url, err = im.download(ctx, fetcher, u, truncateRunes(it.AttachmentAlt, maxAltRunes))
			if err != nil {
				if err := im.failed(ctx, c, "media %s: %v", u.Redacted(), err); err != nil {
					return err
				}
				continue
			}
			im.registerMedia(u, ref.url)
			im.rep.Mapping.URLs[src] = ref.url
			if it.Link != "" && it.Link != src {
				// Links to the attachment page now lead to the file.
				if lu, err := url.Parse(it.Link); err == nil {
					im.links[linkKey(lu)] = ref.url
				}
			}
		}
		c.Created++
		seen[hostPath(u)] = ref
		im.media[it.ID] = ref
		im.rep.Mapping.Media[it.ID] = ref.id
	}
	return nil
}

func (im *importer) download(ctx context.Context, f Fetcher, u *url.URL, alt string) (int64, string, error) {
	rc, err := f.Fetch(ctx, u.String())
	if err != nil {
		return 0, "", err
	}
	defer rc.Close()
	lr := &limitReader{r: rc, remaining: im.opts.MaxMediaBytes, err: ErrMediaTooLarge}
	id, newURL, err := im.sink.StoreMedia(ctx, mediaName(u), lr, alt)
	if lr.exceeded {
		return 0, "", ErrMediaTooLarge
	}
	return id, newURL, err
}

// - - - planning - - -

func linkKey(u *url.URL) string {
	p := strings.TrimSuffix(u.Path, "/")
	if p == "" {
		p = "/"
	}
	k := normHost(u.Host) + p
	if u.RawQuery != "" {
		k += "?" + u.RawQuery
	}
	return k
}

// rewriteURL is the ConvertOptions.RewriteURL of imported content: media
// files go to their new URLs, links to imported posts and pages to their
// new permalinks.
func (im *importer) rewriteURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return raw
	}
	if v, ok := im.lookupMedia(u); ok {
		return v
	}
	frag := ""
	if u.Fragment != "" {
		frag = "#" + u.Fragment
	}
	if v, ok := im.links[linkKey(u)]; ok {
		return v + frag
	}
	if im.siteHosts[normHost(u.Host)] && strings.Trim(u.Path, "/") == "" {
		q := u.Query()
		for _, k := range []string{"p", "page_id"} {
			if id, err := strconv.ParseInt(q.Get(k), 10, 64); err == nil {
				if p := im.plans[id]; p != nil && p.url != "" {
					return p.url + frag
				}
			}
		}
	}
	return raw
}

func (im *importer) mapStatus(it *Item) (status string, ok bool) {
	what := fmt.Sprintf("%s %q", it.Type, plainTextOf(it.Title))
	switch it.Status {
	case "publish":
		status = StatusPublished
	case "future":
		status = StatusPublished // scheduled by its future PublishedAt
	case "draft":
		status = StatusDraft
	case "pending":
		status = StatusPending
	case "private":
		status = StatusDraft
		im.warn("%s was private; imported as a draft", what)
	case "trash":
		if !im.opts.IncludeTrash {
			return "", false
		}
		status = StatusTrash
	case "auto-draft", "inherit":
		return "", false
	default:
		status = StatusDraft
		im.warn("%s has unknown status %q; imported as a draft", what, it.Status)
	}
	if it.Protected && status == StatusPublished {
		// sap13ns has no post passwords: never expose protected content.
		status = StatusDraft
		im.warn("%s was password-protected; imported as a draft", what)
	}
	return status, true
}

func (im *importer) count(typ string) *Count {
	if typ == TypePage {
		return &im.rep.Counts.Pages
	}
	return &im.rep.Counts.Posts
}

// plan decides status and slug of every post and page before anything is
// created, so that links between them can be rewritten in a single pass.
func (im *importer) plan(ctx context.Context) error {
	taken := map[string]bool{}
	for i := range im.exp.Items {
		it := &im.exp.Items[i]
		if it.Type != TypePost && it.Type != TypePage {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		c := im.count(it.Type)
		p := &plan{it: it, skip: true}
		im.planned = append(im.planned, p)
		if it.ID > 0 {
			im.plans[it.ID] = p
		}
		status, ok := im.mapStatus(it)
		if !ok {
			c.Skipped++
			im.rep.Counts.Comments.Skipped += len(it.Comments)
			continue
		}
		p.status = status
		base := Slugify(strings.TrimSuffix(it.Slug, "__trashed"))
		if base == "" {
			base = Slugify(it.Title)
		}
		if base == "" {
			base = it.Type + "-" + strconv.FormatInt(it.ID, 10)
		}
		slug := base
		for n := 2; ; n++ {
			exists := false
			if !taken[it.Type+"/"+slug] {
				var err error
				if exists, err = im.sink.PostExists(ctx, it.Type, slug); err != nil {
					if err := im.failed(ctx, c, "%s %q: %v", it.Type, it.Title, err); err != nil {
						return err
					}
					slug = ""
					break
				}
				if exists && im.opts.SkipExisting && slug == base {
					// Already imported: keep links pointing at it.
					c.Skipped++
					im.rep.Counts.Comments.Skipped += len(it.Comments)
					im.addLinks(it, im.opts.PostURL(it.Type, slug))
					slug = ""
					break
				}
				if !exists {
					break
				}
			}
			if n > 100 {
				if err := im.failed(ctx, c, "%s %q: no free slug for %q", it.Type, it.Title, base); err != nil {
					return err
				}
				slug = ""
				break
			}
			slug = base + "-" + strconv.Itoa(n)
		}
		if slug == "" {
			continue
		}
		if slug != base {
			im.warn("%s %q: slug %q is taken, using %q", it.Type, it.Title, base, slug)
		}
		taken[it.Type+"/"+slug] = true
		p.slug, p.skip = slug, false
		p.url = im.opts.PostURL(it.Type, slug)
		im.addLinks(it, p.url)
	}
	return nil
}

func (im *importer) addLinks(it *Item, newURL string) {
	for _, s := range []string{it.Link, it.GUID} {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			im.links[linkKey(u)] = newURL
		}
	}
	if it.Link != "" {
		im.rep.Mapping.URLs[it.Link] = newURL
	}
}

// - - - pages and posts - - -

func (im *importer) importPages(ctx context.Context) error {
	var pages []*plan
	children := map[int64][]*plan{}
	for _, p := range im.planned {
		it := p.it
		if it.Type != TypePage || p.skip {
			continue
		}
		pages = append(pages, p)
		if parent := im.plans[it.ParentID]; it.ParentID != 0 && it.ParentID != it.ID && parent != nil && !parent.skip && parent.it.Type == TypePage {
			children[it.ParentID] = append(children[it.ParentID], p)
		}
	}
	visited := map[*plan]bool{}
	var visit func(p *plan) error
	visit = func(p *plan) error {
		if visited[p] {
			return nil
		}
		visited[p] = true
		if err := im.create(ctx, p); err != nil {
			return err
		}
		for _, ch := range children[p.it.ID] {
			if err := visit(ch); err != nil {
				return err
			}
		}
		return nil
	}
	for _, p := range pages {
		if parent := im.plans[p.it.ParentID]; p.it.ParentID == 0 || p.it.ParentID == p.it.ID || parent == nil || parent.skip || parent.it.Type != TypePage {
			if err := visit(p); err != nil {
				return err
			}
		}
	}
	// Pages caught in a parent cycle.
	for _, p := range pages {
		if err := visit(p); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) importPosts(ctx context.Context) error {
	var posts []*plan
	for _, p := range im.planned {
		if p.it.Type == TypePost && !p.skip {
			posts = append(posts, p)
		}
	}
	slices.SortStableFunc(posts, func(a, b *plan) int {
		return cmp.Or(a.it.Date.Compare(b.it.Date), cmp.Compare(a.it.ID, b.it.ID))
	})
	for _, p := range posts {
		if err := im.create(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) gallery(parentID int64) func(ids []int64) []Image {
	return func(ids []int64) []Image {
		if len(ids) == 0 {
			var atts []*Item
			for _, a := range im.attachments {
				if a.ParentID == parentID && parentID != 0 {
					atts = append(atts, a)
				}
			}
			slices.SortFunc(atts, func(a, b *Item) int {
				return cmp.Or(cmp.Compare(a.MenuOrder, b.MenuOrder), cmp.Compare(a.ID, b.ID))
			})
			for _, a := range atts {
				ids = append(ids, a.ID)
			}
		}
		var out []Image
		for _, id := range ids {
			if a := im.attachments[id]; a != nil && a.AttachmentURL != "" {
				out = append(out, Image{URL: a.AttachmentURL, Alt: a.AttachmentAlt, Caption: plainTextOf(a.Excerpt)})
			}
		}
		return out
	}
}

func (im *importer) create(ctx context.Context, p *plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	it := p.it
	c := im.count(it.Type)
	title := truncateRunes(plainTextOf(it.Title), maxTitleRunes)
	what := fmt.Sprintf("%s %q", it.Type, title)
	md, teaser := ToMarkdown(it.Content, ConvertOptions{
		BaseURL:    im.baseURL,
		RewriteURL: im.rewriteURL,
		Gallery:    im.gallery(it.ID),
		Warn:       func(msg string) { im.warn("%s: %s", what, msg) },
	})
	excerpt := HTMLToText(it.Excerpt)
	if excerpt == "" {
		excerpt = teaser
	}
	post := Post{
		Type: it.Type, Status: p.status, Title: title, Slug: p.slug, Content: md,
		Excerpt:       truncateText(excerpt, maxExcerptRunes),
		AuthorID:      im.opts.DefaultAuthorID,
		MenuOrder:     it.MenuOrder,
		CommentStatus: "closed",
		Sticky:        it.Sticky && it.Type == TypePost,
		OldURL:        it.Link,
		OldID:         it.ID,
	}
	if it.CommentStatus == "open" {
		post.CommentStatus = "open"
	}
	if it.PageTemplate != "default" {
		post.Template = it.PageTemplate
	}
	if id, ok := im.usersByLogin[it.Creator]; ok {
		post.AuthorID = id
	} else if it.Creator != "" && !im.unknownUsers[it.Creator] {
		im.unknownUsers[it.Creator] = true
		im.warn("author %q was not imported; their content goes to the default author", it.Creator)
	}

	switch p.status {
	case StatusPublished, StatusTrash:
		date := it.Date
		if date.IsZero() {
			date = im.now
		}
		post.PublishedAt = &date
		p.public = p.status == StatusPublished && !date.After(im.now)
		if it.Status == "future" && !date.After(im.now) {
			im.warn("%s was scheduled for %s, which has passed; it is published now", what, date.Format(time.DateTime))
		}
	}
	if !it.Modified.IsZero() {
		mod := it.Modified
		post.ModifiedAt = &mod
	}
	if it.Type == TypePage && it.ParentID != 0 {
		if parent := im.plans[it.ParentID]; parent != nil && parent.created {
			id := parent.newID
			post.ParentID = &id
		} else {
			im.warn("%s: parent page %d was not imported; placed at the top level", what, it.ParentID)
		}
	}
	if it.ThumbnailID != 0 {
		if ref, ok := im.media[it.ThumbnailID]; ok {
			id := ref.id
			post.FeaturedMediaID = &id
		} else if im.opts.DownloadMedia {
			im.warn("%s: featured image %d was not imported", what, it.ThumbnailID)
		}
	}
	for _, t := range it.Terms {
		tax := ""
		switch t.Domain {
		case "category":
			tax = TaxonomyCategory
		case "post_tag":
			tax = TaxonomyTag
		default:
			continue
		}
		id, ok, err := im.ensureTerm(ctx, tax, Term{Slug: t.Slug, Name: t.Name}, nil)
		if err != nil {
			return err
		}
		if ok && !slices.Contains(post.TermIDs, id) {
			post.TermIDs = append(post.TermIDs, id)
		}
	}

	id := im.fake()
	if !im.opts.DryRun {
		var err error
		if id, err = im.sink.CreatePost(ctx, post); err != nil {
			im.rep.Counts.Comments.Skipped += len(it.Comments)
			return im.failed(ctx, c, "%s: %v", what, err)
		}
	}
	c.Created++
	p.newID, p.created = id, true
	im.order = append(im.order, p)
	im.rep.Mapping.Posts[it.ID] = id
	return nil
}

// - - - comments - - -

// commentStatus maps wp:comment_approved; unknown values are held for
// moderation.
func commentStatus(approved string) string {
	switch approved {
	case "1", "approve", "approved":
		return CommentApproved
	case "spam":
		return CommentSpam
	case "trash", "post-trashed":
		return CommentTrash
	}
	return CommentPending
}

// threadOrder sorts comments so that every parent precedes its replies,
// siblings by date.
func threadOrder(list []Comment) []*Comment {
	byID := map[int64]*Comment{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	children := map[int64][]*Comment{}
	var roots []*Comment
	for i := range list {
		c := &list[i]
		if c.ParentID != 0 && c.ParentID != c.ID && byID[c.ParentID] != nil {
			children[c.ParentID] = append(children[c.ParentID], c)
		} else {
			roots = append(roots, c)
		}
	}
	byDate := func(a, b *Comment) int { return cmp.Or(a.Date.Compare(b.Date), cmp.Compare(a.ID, b.ID)) }
	slices.SortStableFunc(roots, byDate)
	out := make([]*Comment, 0, len(list))
	seen := map[*Comment]bool{}
	var visit func(c *Comment)
	visit = func(c *Comment) {
		if seen[c] {
			return
		}
		seen[c] = true
		out = append(out, c)
		kids := children[c.ID]
		slices.SortStableFunc(kids, byDate)
		for _, k := range kids {
			visit(k)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	for i := range list { // cycles
		visit(&list[i])
	}
	return out
}

func (im *importer) importComments(ctx context.Context) error {
	cnt := &im.rep.Counts.Comments
	for _, p := range im.order {
		if len(p.it.Comments) == 0 {
			continue
		}
		parentOf := map[int64]int64{}
		for _, c := range p.it.Comments {
			parentOf[c.ID] = c.ParentID
		}
		newIDs := map[int64]int64{}
		for _, c := range threadOrder(p.it.Comments) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if c.Type == "pingback" || c.Type == "trackback" {
				im.rep.Ignored[c.Type]++
				continue
			}
			status := commentStatus(c.Approved)
			if status == CommentTrash && !im.opts.IncludeTrash {
				cnt.Skipped++
				continue
			}
			cm := *c
			cm.Status = status
			cm.Text = HTMLToText(c.Content)
			if cm.Text == "" {
				cnt.Skipped++
				continue
			}
			if utf8.RuneCountInString(cm.Text) > maxCommentRunes {
				cm.Text = truncateText(cm.Text, maxCommentRunes)
				im.warn("comment %d on %q was truncated", c.ID, p.it.Title)
			}
			cm.Author = truncateRunes(plainTextOf(c.Author), maxNameRunes)
			if cm.Author == "" {
				cm.Author = "Anonymous"
			}
			cm.AuthorURL = safeAuthorURL(c.AuthorURL)
			if len(cm.AuthorEmail) > 254 || !strings.Contains(cm.AuthorEmail, "@") {
				cm.AuthorEmail = ""
			}
			if c.UserID > 0 {
				if id, ok := im.usersByWPID[c.UserID]; ok {
					cm.NewUserID = &id
				}
			}
			// Attach to the nearest imported ancestor.
			var parentID *int64
			for anc, hops := c.ParentID, 0; anc != 0 && hops <= len(parentOf); anc, hops = parentOf[anc], hops+1 {
				if id, ok := newIDs[anc]; ok {
					parentID = &id
					break
				}
			}
			id := im.fake()
			if !im.opts.DryRun {
				var err error
				if id, err = im.sink.CreateComment(ctx, p.newID, cm, parentID); err != nil {
					if err := im.failed(ctx, cnt, "comment %d on %q: %v", c.ID, p.it.Title, err); err != nil {
						return err
					}
					continue
				}
			}
			cnt.Created++
			newIDs[c.ID] = id
			im.rep.Mapping.Comments[c.ID] = id
		}
	}
	return nil
}

// - - - redirects - - -

// oldPath returns the path (and query) a visitor of the old site requested.
func oldPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	return p
}

func (im *importer) redirect(ctx context.Context, from, to string) error {
	c := &im.rep.Counts.Redirects
	if from == "" || from == "/" || to == "" || from == to || im.redirected[from] {
		return nil
	}
	if u, err := url.Parse(to); err == nil && u.Host == "" && u.Path == from {
		return nil
	}
	im.redirected[from] = true
	if !im.opts.DryRun {
		if err := im.sink.AddRedirect(ctx, from, to); err != nil {
			return im.failed(ctx, c, "redirect %s → %s: %v", from, to, err)
		}
	}
	c.Created++
	return nil
}

// importRedirects maps old permalinks of public content (a redirect for a
// draft or a scheduled post would reveal its new slug) and old media files.
func (im *importer) importRedirects(ctx context.Context) error {
	for _, p := range im.order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !p.public {
			continue
		}
		if err := im.redirect(ctx, oldPath(p.it.Link), p.url); err != nil {
			return err
		}
	}
	for i := range im.exp.Items {
		it := &im.exp.Items[i]
		if ref, ok := im.media[it.ID]; ok && it.Type == "attachment" && ref.url != "" {
			if err := im.redirect(ctx, oldPath(it.AttachmentURL), ref.url); err != nil {
				return err
			}
		}
	}
	return nil
}

// - - - text helpers - - -

// safeAuthorURL keeps a commenter's website only when it is a plain http(s)
// URL that fits the column; "javascript:" and friends are dropped.
func safeAuthorURL(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 300 {
		return ""
	}
	return raw
}

func plainTextOf(s string) string {
	if !strings.ContainsAny(s, "<&") {
		return strings.TrimSpace(s)
	}
	t, _ := plainText(parseBody(s), "")
	return t
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max])
}

// truncateText shortens text at a word boundary, adding an ellipsis.
func truncateText(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)[:max-1]
	cut := len(runes)
	for i := len(runes) - 1; i > len(runes)*2/3; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(runes[:cut]), unicode.IsSpace) + "…"
}
