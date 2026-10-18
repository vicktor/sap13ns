// Package wpimport imports a WordPress export file (WXR, Tools → Export) into
// sap13ns. Parse reads the XML as a stream, ToMarkdown turns post HTML
// (classic or Gutenberg) into Markdown, and Import feeds users, terms, media,
// pages, posts, comments and redirects to a Sink implemented by the web layer.
//
// Nothing in this package trusts the export: the XML parser refuses DOCTYPE
// and entity declarations, scripts, styles, iframes and forms are dropped from
// the content, and media is downloaded through an SSRF-guarded HTTP client.
package wpimport

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/htmlindex"
)

var (
	ErrDoctype  = errors.New("wpimport: DOCTYPE and entity declarations are not allowed")
	ErrNotWXR   = errors.New("wpimport: not a WordPress export file")
	ErrTooDeep  = errors.New("wpimport: XML nesting too deep")
	ErrTooLarge = errors.New("wpimport: export file too large")
)

// maxDepth bounds element nesting. A WXR file needs 6 levels
// (rss/channel/item/wp:comment/wp:commentmeta/wp:meta_key).
const maxDepth = 32

// Export is the parsed content of a WXR file.
type Export struct {
	Site       Site
	Authors    []Author
	Categories []Term
	Tags       []Term
	// Items holds posts, pages and attachments in file order.
	Items []Item
	// Ignored counts the items of other post types (nav_menu_item, revision,
	// wp_block, custom_css, products of plugins…) by type.
	Ignored map[string]int
}

type Site struct {
	Title       string
	Link        string
	Description string
	Language    string
	BaseSiteURL string // WordPress address, where wp-content lives
	BaseBlogURL string // site address (home)
	WXRVersion  string
}

type Author struct {
	ID          int64 // WordPress user ID
	Login       string
	Email       string
	DisplayName string
	FirstName   string
	LastName    string
}

// Term is a category or a tag.
type Term struct {
	ID          int64  // WordPress term ID
	Slug        string // nicename, possibly percent-encoded
	Name        string
	Parent      string // nicename of the parent category, "" at the top level
	Description string
}

// ItemTerm is a category or tag assigned to an item.
type ItemTerm struct {
	Domain string // "category", "post_tag" or another taxonomy
	Slug   string // nicename
	Name   string
}

// Item is a post, a page or an attachment.
type Item struct {
	ID            int64 // wp:post_id
	Type          string
	Title         string
	Link          string // public permalink
	GUID          string
	Creator       string    // author login (dc:creator)
	Content       string    // content:encoded, HTML
	Excerpt       string    // excerpt:encoded
	Date          time.Time // post_date_gmt, or post_date read as UTC when the GMT date is missing
	Modified      time.Time // post_modified_gmt, or post_modified
	Slug          string    // post_name, possibly percent-encoded
	Status        string    // publish, draft, pending, private, future, trash, inherit, auto-draft
	ParentID      int64
	MenuOrder     int
	Sticky        bool
	CommentStatus string
	Protected     bool // has a post password
	Terms         []ItemTerm

	ThumbnailID   int64  // _thumbnail_id
	AttachmentAlt string // _wp_attachment_image_alt
	PageTemplate  string // _wp_page_template
	AttachmentURL string

	Comments []Comment
}

// Comment is a comment, pingback or trackback of an item. The fields after
// UserID are filled by Import before it calls Sink.CreateComment.
type Comment struct {
	ID          int64
	Author      string
	AuthorEmail string
	AuthorURL   string
	AuthorIP    string
	Date        time.Time // comment_date_gmt, or comment_date
	Content     string    // as exported: text with a little HTML
	Approved    string    // "1", "0", "spam", "trash" or "post-trashed"
	Type        string    // "", "comment", "pingback" or "trackback"
	ParentID    int64     // WordPress ID of the parent comment, 0 at the top level
	UserID      int64     // WordPress user ID, 0 for guests

	Status    string // CommentApproved, CommentPending, CommentSpam or CommentTrash
	Text      string // Content as plain text, paragraphs separated by blank lines
	NewUserID *int64 // CMS user of an imported author, nil for guests
}

// Parse reads a WXR export. It does not limit the size of the input: the
// caller MUST wrap r, typically with http.MaxBytesReader or LimitReader, so
// that an upload cannot exhaust memory. Read errors from r (including the
// limit) are returned wrapped, so errors.Is/As still work.
//
// DOCTYPE and entity declarations are refused (no XML bombs, no external
// entities); only the predefined XML entities and the fixed HTML entity set
// are recognised.
func Parse(r io.Reader) (*Export, error) {
	d := xml.NewDecoder(&controlFilter{r: r})
	d.Strict = true
	// A fixed, non-recursive table: unknown entities are errors and
	// declarations are rejected below, so nothing can expand.
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	p := &parser{d: d}
	exp := &Export{Ignored: map[string]int{}}
	sawChannel := false
	for {
		tok, err := p.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, wrapErr(err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch {
		case p.depth == 1 && se.Name.Local == "rss":
			// Descend into the root.
		case p.depth == 2 && se.Name.Local == "channel" && !sawChannel:
			sawChannel = true
			if err := p.channel(exp); err != nil {
				return nil, wrapErr(err)
			}
		case p.depth == 1:
			return nil, ErrNotWXR
		default:
			if err := p.skip(); err != nil {
				return nil, wrapErr(err)
			}
		}
	}
	if !sawChannel {
		return nil, ErrNotWXR
	}
	return exp, nil
}

// LimitReader returns a reader that fails with ErrTooLarge once r yields more
// than n bytes. Unlike io.LimitReader it does not truncate silently, which
// would surface as a confusing XML syntax error.
func LimitReader(r io.Reader, n int64) io.Reader {
	return &limitReader{r: r, remaining: n, err: ErrTooLarge}
}

type limitReader struct {
	r         io.Reader
	remaining int64
	err       error
	exceeded  bool
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.exceeded {
		return 0, l.err
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		l.exceeded = true
		return n + int(l.remaining), l.err
	}
	return n, err
}

func wrapErr(err error) error {
	if errors.Is(err, ErrDoctype) || errors.Is(err, ErrTooDeep) || errors.Is(err, ErrNotWXR) {
		return err
	}
	return fmt.Errorf("wpimport: invalid export: %w", err)
}

// charsetReader accepts the encodings WordPress may declare besides UTF-8.
func charsetReader(label string, input io.Reader) (io.Reader, error) {
	enc, err := htmlindex.Get(label)
	if err != nil {
		return nil, fmt.Errorf("unsupported encoding %q", label)
	}
	return enc.NewDecoder().Reader(input), nil
}

// controlFilter drops the C0 control characters XML 1.0 forbids (WordPress
// exports them verbatim when they were pasted into a post), which would
// otherwise abort the whole parse. These bytes never occur inside multi-byte
// UTF-8 sequences, so filtering byte by byte is safe.
type controlFilter struct{ r io.Reader }

func (f *controlFilter) Read(p []byte) (int, error) {
	for {
		n, err := f.r.Read(p)
		j := 0
		for i := 0; i < n; i++ {
			c := p[i]
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				continue
			}
			p[j] = c
			j++
		}
		if j > 0 || err != nil || n == 0 {
			return j, err
		}
	}
}

type nsKind int

const (
	nsNone nsKind = iota
	nsWP
	nsContent
	nsExcerpt
	nsDC
	nsOther
)

// nsOf classifies a namespace. encoding/xml resolves prefixes to URIs, and
// WordPress has used several export versions (1.0 to 1.2), so URIs are
// matched by prefix; undeclared prefixes are kept as-is by the decoder.
func nsOf(space string) nsKind {
	s := strings.TrimPrefix(strings.TrimPrefix(space, "http://"), "https://")
	switch {
	case space == "":
		return nsNone
	case space == "wp":
		return nsWP
	case space == "content" || strings.HasPrefix(s, "purl.org/rss/1.0/modules/content"):
		return nsContent
	case space == "excerpt":
		return nsExcerpt
	case space == "dc" || strings.HasPrefix(s, "purl.org/dc/elements/"):
		return nsDC
	case strings.HasPrefix(s, "wordpress.org/export/"):
		if strings.HasSuffix(strings.TrimSuffix(s, "/"), "/excerpt") {
			return nsExcerpt
		}
		return nsWP
	}
	return nsOther
}

type parser struct {
	d     *xml.Decoder
	depth int
}

func (p *parser) next() (xml.Token, error) {
	tok, err := p.d.Token()
	if err != nil {
		return nil, err
	}
	switch tok.(type) {
	case xml.Directive:
		// <!DOCTYPE …>, <!ENTITY …> and friends.
		return nil, ErrDoctype
	case xml.StartElement:
		p.depth++
		if p.depth > maxDepth {
			return nil, ErrTooDeep
		}
	case xml.EndElement:
		p.depth--
	}
	return tok, nil
}

// skip consumes the current element up to its end tag.
func (p *parser) skip() error {
	return p.children(func(xml.StartElement) error { return p.skip() })
}

// children calls fn for each child element of the current element; fn must
// consume the child completely.
func (p *parser) children(fn func(se xml.StartElement) error) error {
	for {
		tok, err := p.next()
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(t); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

// text returns the character data of the current element (CDATA sections
// included), trimmed.
func (p *parser) text() (string, error) {
	var b strings.Builder
	level := 0
	for {
		tok, err := p.next()
		if err != nil {
			if err == io.EOF {
				return "", io.ErrUnexpectedEOF
			}
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			b.Write(t)
		case xml.StartElement:
			level++
		case xml.EndElement:
			if level == 0 {
				return strings.TrimSpace(b.String()), nil
			}
			level--
		}
	}
}

func (p *parser) textInto(dst *string) error {
	s, err := p.text()
	*dst = s
	return err
}

func (p *parser) int64Into(dst *int64) error {
	s, err := p.text()
	if err != nil {
		return err
	}
	*dst, _ = strconv.ParseInt(s, 10, 64)
	return nil
}

func (p *parser) channel(exp *Export) error {
	return p.children(func(se xml.StartElement) error {
		ns, local := nsOf(se.Name.Space), se.Name.Local
		switch {
		case ns == nsNone && local == "title":
			return p.textInto(&exp.Site.Title)
		case ns == nsNone && local == "link":
			return p.textInto(&exp.Site.Link)
		case ns == nsNone && local == "description":
			return p.textInto(&exp.Site.Description)
		case ns == nsNone && local == "language":
			return p.textInto(&exp.Site.Language)
		case ns == nsNone && local == "item":
			return p.item(exp)
		case ns != nsWP:
			return p.skip()
		}
		switch local {
		case "base_site_url":
			return p.textInto(&exp.Site.BaseSiteURL)
		case "base_blog_url":
			return p.textInto(&exp.Site.BaseBlogURL)
		case "wxr_version":
			return p.textInto(&exp.Site.WXRVersion)
		case "author":
			a, err := p.author()
			if err == nil && a.Login != "" {
				exp.Authors = append(exp.Authors, a)
			}
			return err
		case "category":
			t, err := p.term("category_nicename", "cat_name", "category_parent", "category_description")
			if err == nil && t.Slug != "" {
				exp.Categories = append(exp.Categories, t)
			}
			return err
		case "tag":
			t, err := p.term("tag_slug", "tag_name", "", "tag_description")
			if err == nil && t.Slug != "" {
				exp.Tags = append(exp.Tags, t)
			}
			return err
		}
		return p.skip()
	})
}

func (p *parser) author() (Author, error) {
	var a Author
	err := p.children(func(se xml.StartElement) error {
		if nsOf(se.Name.Space) != nsWP {
			return p.skip()
		}
		switch se.Name.Local {
		case "author_id":
			return p.int64Into(&a.ID)
		case "author_login":
			return p.textInto(&a.Login)
		case "author_email":
			return p.textInto(&a.Email)
		case "author_display_name":
			return p.textInto(&a.DisplayName)
		case "author_first_name":
			return p.textInto(&a.FirstName)
		case "author_last_name":
			return p.textInto(&a.LastName)
		}
		return p.skip()
	})
	return a, err
}

func (p *parser) term(slugEl, nameEl, parentEl, descEl string) (Term, error) {
	var t Term
	err := p.children(func(se xml.StartElement) error {
		if nsOf(se.Name.Space) != nsWP {
			return p.skip()
		}
		switch se.Name.Local {
		case "term_id":
			return p.int64Into(&t.ID)
		case slugEl:
			return p.textInto(&t.Slug)
		case nameEl:
			return p.textInto(&t.Name)
		case descEl:
			return p.textInto(&t.Description)
		case parentEl:
			if parentEl != "" {
				return p.textInto(&t.Parent)
			}
		}
		return p.skip()
	})
	return t, err
}

func (p *parser) item(exp *Export) error {
	var it Item
	var date, dateGMT, mod, modGMT, password string
	err := p.children(func(se xml.StartElement) error {
		local := se.Name.Local
		switch nsOf(se.Name.Space) {
		case nsNone:
			switch local {
			case "title":
				return p.textInto(&it.Title)
			case "link":
				return p.textInto(&it.Link)
			case "guid":
				return p.textInto(&it.GUID)
			case "category":
				t := ItemTerm{Domain: "category"}
				for _, a := range se.Attr {
					switch a.Name.Local {
					case "domain":
						t.Domain = a.Value
					case "nicename":
						t.Slug = a.Value
					}
				}
				if t.Domain == "tag" { // WXR 1.0
					t.Domain = "post_tag"
				}
				if err := p.textInto(&t.Name); err != nil {
					return err
				}
				if t.Slug == "" {
					t.Slug = t.Name // WXR 1.0 had no nicename attribute
				}
				if t.Slug != "" {
					it.Terms = append(it.Terms, t)
				}
				return nil
			}
		case nsDC:
			if local == "creator" {
				return p.textInto(&it.Creator)
			}
		case nsContent:
			if local == "encoded" {
				return p.textInto(&it.Content)
			}
		case nsExcerpt:
			if local == "encoded" {
				return p.textInto(&it.Excerpt)
			}
		case nsWP:
			switch local {
			case "post_id":
				return p.int64Into(&it.ID)
			case "post_date":
				return p.textInto(&date)
			case "post_date_gmt":
				return p.textInto(&dateGMT)
			case "post_modified":
				return p.textInto(&mod)
			case "post_modified_gmt":
				return p.textInto(&modGMT)
			case "comment_status":
				return p.textInto(&it.CommentStatus)
			case "post_name":
				return p.textInto(&it.Slug)
			case "status":
				return p.textInto(&it.Status)
			case "post_parent":
				return p.int64Into(&it.ParentID)
			case "menu_order":
				var n int64
				err := p.int64Into(&n)
				it.MenuOrder = int(n)
				return err
			case "post_type":
				return p.textInto(&it.Type)
			case "post_password":
				return p.textInto(&password)
			case "is_sticky":
				var s string
				err := p.textInto(&s)
				it.Sticky = s == "1"
				return err
			case "attachment_url":
				return p.textInto(&it.AttachmentURL)
			case "postmeta":
				key, value, err := p.meta()
				switch key {
				case "_thumbnail_id":
					it.ThumbnailID, _ = strconv.ParseInt(value, 10, 64)
				case "_wp_attachment_image_alt":
					it.AttachmentAlt = value
				case "_wp_page_template":
					it.PageTemplate = value
				}
				return err
			case "comment":
				c, err := p.comment()
				if err == nil {
					it.Comments = append(it.Comments, c)
				}
				return err
			}
		}
		return p.skip()
	})
	if err != nil {
		return err
	}
	it.Date = pickDate(dateGMT, date)
	it.Modified = pickDate(modGMT, mod)
	it.Protected = password != ""
	if it.Type == "" {
		it.Type = "post"
	}
	switch it.Type {
	case "post", "page", "attachment":
		if it.Type == "attachment" {
			it.Comments = nil
		}
		exp.Items = append(exp.Items, it)
	default:
		exp.Ignored[it.Type]++
	}
	return nil
}

func (p *parser) meta() (key, value string, err error) {
	err = p.children(func(se xml.StartElement) error {
		if nsOf(se.Name.Space) == nsWP {
			switch se.Name.Local {
			case "meta_key":
				return p.textInto(&key)
			case "meta_value":
				return p.textInto(&value)
			}
		}
		return p.skip()
	})
	return key, value, err
}

func (p *parser) comment() (Comment, error) {
	var c Comment
	var date, dateGMT string
	err := p.children(func(se xml.StartElement) error {
		if nsOf(se.Name.Space) != nsWP {
			return p.skip()
		}
		switch se.Name.Local {
		case "comment_id":
			return p.int64Into(&c.ID)
		case "comment_author":
			return p.textInto(&c.Author)
		case "comment_author_email":
			return p.textInto(&c.AuthorEmail)
		case "comment_author_url":
			return p.textInto(&c.AuthorURL)
		case "comment_author_IP":
			return p.textInto(&c.AuthorIP)
		case "comment_date":
			return p.textInto(&date)
		case "comment_date_gmt":
			return p.textInto(&dateGMT)
		case "comment_content":
			return p.textInto(&c.Content)
		case "comment_approved":
			return p.textInto(&c.Approved)
		case "comment_type":
			return p.textInto(&c.Type)
		case "comment_parent":
			return p.int64Into(&c.ParentID)
		case "comment_user_id":
			return p.int64Into(&c.UserID)
		}
		return p.skip()
	})
	c.Date = pickDate(dateGMT, date)
	return c, err
}

const wpDateLayout = "2006-01-02 15:04:05"

// pickDate parses the GMT date, falling back to the local one (read as UTC:
// the export does not carry the site's timezone). WordPress writes
// "0000-00-00 00:00:00" for unset dates, e.g. the GMT date of drafts.
func pickDate(gmt, local string) time.Time {
	for _, s := range []string{gmt, local} {
		if s == "" || strings.HasPrefix(s, "0000-") {
			continue
		}
		if t, err := time.ParseInLocation(wpDateLayout, s, time.UTC); err == nil {
			return t
		}
	}
	return time.Time{}
}
