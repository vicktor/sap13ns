package themeimport

import (
	"slices"
	"strconv"
	"strings"
)

// The template writer. Each block of a WordPress template becomes
// sap13ns template text: a static block keeps its saved HTML (html.go),
// a dynamic one is written from its attributes with the page data of
// sap13ns (docs/THEMES.md): the site's title and menus, the posts of the
// page, the post being read. Foreign text never becomes template code:
// every action in the output is written here, from a fixed list.

// emitter writes the templates of one page.
type emitter struct {
	c    *converter
	file string // the source file, for the report
	kind string // the sap13ns page: layout, front, home, single, page, archive, search, 404
	// post says how the blocks of a post reach it: "." inside a post
	// loop, "page" on a single post or page (through {{with $.Post}}),
	// "" where there is no post.
	post   string
	region string // header, footer or "" for the page's content
	query  *queryInfo
	stack  []string // patterns and parts being written, against cycles
	navs   *int     // navigation blocks written in the region
	tools  *bool    // the language and dark-mode switches are written
}

type queryInfo struct {
	inherit         bool
	perPage, offset int
}

func (e *emitter) note(code string, args ...any) {
	e.c.report.add(Note{Code: code, File: e.file, Args: args})
}

func (e *emitter) sub() *emitter {
	s := *e
	s.stack = slices.Clone(e.stack)
	return &s
}

func (e *emitter) blocks(bs []*Block) string {
	var b strings.Builder
	for _, x := range bs {
		b.WriteString(e.block(x))
	}
	return b.String()
}

// staticBlocks save the HTML they show.
var staticBlocks = map[string]bool{
	"core/paragraph": true, "core/heading": true, "core/list": true, "core/list-item": true, "core/quote": true,
	"core/pullquote": true, "core/image": true, "core/gallery": true, "core/group": true, "core/columns": true,
	"core/column": true, "core/buttons": true, "core/button": true, "core/cover": true, "core/separator": true,
	"core/spacer": true, "core/media-text": true, "core/table": true, "core/code": true, "core/preformatted": true,
	"core/verse": true, "core/details": true, "core/html": true, "core/freeform": true, "core/text-columns": true,
	"core/social-links": true, "core/missing": true,
}

// droppedBlocks show what sap13ns has no counterpart for: media players,
// embeds, widgets of WordPress.
var droppedBlocks = map[string]bool{
	"core/embed": true, "core/video": true, "core/audio": true, "core/file": true, "core/shortcode": true,
	"core/more": true, "core/nextpage": true, "core/legacy-widget": true, "core/widget-group": true,
	"core/archives": true, "core/calendar": true, "core/latest-comments": true, "core/rss": true,
	"core/post-comments-count": true, "core/post-comments-link": true, "core/post-author-biography": true,
	"core/block": true, "core/footnotes": true, "core/query-total": true, "core/comments-title": true,
	"core/comment-template": true, "core/comments-pagination": true, "core/accordion": true,
}

func (e *emitter) block(b *Block) string {
	if !e.c.spend(1, 0) {
		return "" // past the conversion's budget: writeTemplates fails
	}
	if b.BadJSON {
		e.note("block.bad-json", b.Name)
	}
	if b.Name != "" {
		e.c.report.Blocks[b.Name]++
	}
	switch b.Name {
	case "":
		out, _ := e.static(b, nil, nil)
		return out
	case "core/pattern":
		return e.pattern(b)
	case "core/template-part":
		return e.part(b, "")
	case "core/group", "core/columns", "core/column", "core/buttons", "core/details", "core/quote", "core/pullquote", "core/social-links":
		first := slices.Concat(e.layoutClasses(b), []string{e.elementClass(b)})
		if b.Name == "core/column" && attrString(b.Attrs, "width") != "" {
			first = append(first, "sap-wp-col-basis") // its flex-basis holds (blocks.css)
		}
		out, _ := e.static(b, first, nil)
		return out
	case "core/cover":
		if attrBool(b.Attrs, "useFeaturedImage") {
			e.note("block.dynamic-content", b.Name+" (featured image)")
		}
		out, f := e.static(b, []string{e.elementClass(b)}, map[string][]string{"wp-block-cover__inner-container": e.layoutClasses(b)})
		e.c.report.Images += f.images
		return out
	case "core/image":
		out, f := e.static(b, []string{e.elementClass(b)}, nil)
		if f.images == 0 && f.lostImages > 0 {
			return "" // the picture is not the theme's: nothing left to show
		}
		e.c.report.Images += f.images
		return out
	case "core/site-title":
		return e.siteTitle(b)
	case "core/site-tagline":
		return `{{with $.Site.Tagline}}<p` + classAttr([]string{"wp-block-site-tagline"}, e.supportClasses(b, true)) + `>{{.}}</p>{{end}}`
	case "core/site-logo":
		return e.siteLogo(b)
	case "core/navigation":
		return e.navigation(b)
	case "core/page-list":
		return `<ul` + classAttr([]string{"wp-block-page-list"}, e.supportClasses(b, true)) + `>{{template "shared/menu" $.Site.Pages}}</ul>`
	case "core/home-link", "core/navigation-link", "core/navigation-submenu", "core/spacer-nav":
		return "" // only inside a navigation, which shows the site's menu
	case "core/search":
		cls := []string{"wp-block-search"}
		if p := attrString(b.Attrs, "buttonPosition"); p != "" {
			cls = append(cls, "wp-block-search__"+cssIdent(p))
		}
		return `{{if not $.Site.Custom.HideSearch}}<div` + classAttr(cls, e.supportClasses(b, true)) + `>{{template "shared/search-form" $}}</div>{{end}}`
	case "core/loginout":
		return `{{if not $.Site.Viewer}}{{with $.Site.LoginURL}}<div` + classAttr([]string{"wp-block-loginout"}, e.supportClasses(b, true)) +
			`><a href="{{.}}" rel="nofollow">{{T "Log in"}}</a></div>{{end}}{{end}}`
	case "core/social-link":
		return e.socialLink(b)
	case "core/categories":
		return `{{with $.Site.Categories}}<ul` + classAttr([]string{"wp-block-categories-list", "wp-block-categories"}, e.supportClasses(b, true)) +
			`>{{range .}}<li class="cat-item"><a href="{{.URL}}">{{.Name}}</a></li>{{end}}</ul>{{end}}`
	case "core/tag-cloud":
		return `{{with $.Site.Tags}}<p` + classAttr([]string{"wp-block-tag-cloud"}, e.supportClasses(b, true)) +
			`>{{range .}}<a href="{{.URL}}" class="tag-cloud-link">{{.Name}}</a> {{end}}</p>{{end}}`
	case "core/latest-posts":
		return `<ul` + classAttr([]string{"wp-block-latest-posts__list", "wp-block-latest-posts"}, e.supportClasses(b, true)) +
			`>{{range $i, $p := $.Posts}}{{if lt $i 5}}<li><a class="wp-block-latest-posts__post-title" href="{{.URL}}">{{.Title}}</a></li>{{end}}{{end}}</ul>`
	case "core/query":
		return e.queryBlock(b)
	case "core/post-template":
		return e.postTemplate(b)
	case "core/query-no-results":
		if e.kind == "single" || e.kind == "page" || e.query == nil {
			return ""
		}
		return `{{if not $.Posts}}<div` + classAttr([]string{"wp-block-query-no-results"}, e.supportClasses(b, true)) + `>` + e.blocks(b.Inner) + `</div>{{end}}`
	case "core/query-pagination":
		return e.pagination(b)
	case "core/query-pagination-previous", "core/query-pagination-next", "core/query-pagination-numbers":
		return "" // only inside query-pagination
	case "core/query-title":
		return e.queryTitle(b)
	case "core/term-description":
		return `{{with $.Archive}}{{with .Description}}<div` + classAttr([]string{"wp-block-term-description"}, e.supportClasses(b, true)) + `><p>{{.}}</p></div>{{end}}{{end}}`
	case "core/comments", "core/comments-query-loop", "core/post-comments", "core/post-comments-form":
		if e.kind != "single" && e.kind != "page" {
			return ""
		}
		if len(b.Inner) > 0 {
			e.note("block.comments-inner")
		}
		return `<div` + classAttr([]string{"wp-block-comments"}, e.supportClasses(b, true)) + `>{{template "shared/comments" $}}</div>`
	case "core/post-navigation-link":
		return e.postNavLink(b)
	}
	if strings.HasPrefix(b.Name, "core/post-") || b.Name == "core/read-more" || b.Name == "core/avatar" {
		if out, ok := e.postBlock(b); ok {
			return out
		}
	}
	if strings.HasPrefix(b.Name, "woocommerce/") {
		e.c.woo = true
		return ""
	}
	if droppedBlocks[b.Name] {
		e.note("block.dynamic-content", b.Name)
		return ""
	}
	if staticBlocks[b.Name] {
		out, _ := e.static(b, []string{e.elementClass(b)}, nil)
		return out
	}
	// A block sap13ns does not know: its saved HTML, if it has any.
	if hasHTML(b) {
		e.note("block.unknown", b.Name)
		out, _ := e.static(b, nil, nil)
		return out
	}
	e.note("block.dropped", b.Name)
	return ""
}

func hasHTML(b *Block) bool {
	for _, p := range b.Parts {
		if p.Block < 0 && strings.Contains(p.HTML, "<") {
			return true
		}
	}
	return false
}

// static rewrites a block's saved HTML, its inner blocks written in their
// places.
func (e *emitter) static(b *Block, first []string, extra map[string][]string) (string, *fragment) {
	var src strings.Builder
	holes := make([]string, len(b.Inner))
	own := 0
	for _, p := range b.Parts {
		if p.Block < 0 {
			own += len(p.HTML)
		}
	}
	if !e.c.spend(0, own) {
		return "", &fragment{}
	}
	for _, p := range b.Parts {
		if p.Block >= 0 {
			holes[p.Block] = e.block(b.Inner[p.Block])
			src.WriteString(e.c.marks.hole(p.Block))
		} else {
			src.WriteString(p.HTML)
		}
	}
	return e.rewriteHTML(src.String(), holes, first, extra)
}

// pattern writes a pattern of the theme in place.
func (e *emitter) pattern(b *Block) string {
	slug := attrString(b.Attrs, "slug")
	p := e.c.patterns[slug]
	switch {
	case p == nil:
		e.note("pattern.missing", slug)
		return ""
	case slices.Contains(e.stack, "pattern:"+slug) || len(e.stack) >= 12:
		e.note("pattern.cycle", slug)
		return ""
	}
	bs, err := e.c.parsed("pattern:"+slug, p.Markup)
	if err != nil {
		e.c.report.add(Note{Code: "block.too-many", File: p.File})
		return ""
	}
	s := e.sub()
	s.stack = append(s.stack, "pattern:"+slug)
	return s.blocks(bs)
}

// part writes a template part with its element: <header>, <footer> or
// the tag the block names.
func (e *emitter) part(b *Block, region string) string {
	slug := cssIdent(attrString(b.Attrs, "slug"))
	src, ok := e.c.parts[slug]
	if !ok {
		e.note("part.missing", slug)
		return ""
	}
	if slices.Contains(e.stack, "part:"+slug) || len(e.stack) >= 12 {
		e.note("pattern.cycle", slug)
		return ""
	}
	bs, err := e.c.parsed("part:"+slug, src)
	if err != nil {
		e.c.report.add(Note{Code: "block.too-many", File: "parts/" + slug + ".html"})
		return ""
	}
	tag := attrString(b.Attrs, "tagName")
	area := e.c.partArea(slug, tag)
	if !slices.Contains([]string{"header", "footer", "div", "section", "aside", "main", "article", "nav"}, tag) {
		tag = "div"
		if area == "header" || area == "footer" {
			tag = area
		}
	}
	if tag == "main" {
		tag = "div"
	}
	s := e.sub()
	s.stack = append(s.stack, "part:"+slug)
	s.file = "parts/" + slug + ".html"
	if region != "" {
		s.region = region
		if s.navs == nil {
			n := 0
			placed := false
			s.navs, s.tools = &n, &placed
		}
	}
	inner := s.blocks(bs)
	if region == "header" && !*s.tools {
		inner += toolsHTML
		*s.tools = true
	}
	return "<" + tag + classAttr([]string{"wp-block-template-part"}, e.supportClasses(b, true)) + ">" + inner + "</" + tag + ">"
}

// toolsHTML: the site's language and dark-mode switches, which sap13ns
// themes show in their header.
const toolsHTML = `<div class="sap-wp-tools">{{template "shared/lang-switch" $}}{{template "shared/theme-toggle"}}</div>`

func (e *emitter) siteTitle(b *Block) string {
	level := attrInt(b.Attrs, "level", 1)
	tag := "p"
	if level >= 1 && level <= 6 {
		tag = "h" + strconv.Itoa(level)
	}
	link := true
	if v, ok := b.Attrs["isLink"].(bool); ok {
		link = v
	}
	inner := "{{$.Site.Title}}"
	if link {
		inner = `<a href="{{link "/"}}" rel="home"` + targetAttr(b) + `>{{$.Site.Title}}</a>`
	}
	return `{{if not $.Site.Custom.LogoOnly}}<` + tag + classAttr([]string{"wp-block-site-title"}, e.supportClasses(b, true)) + ">" + inner + "</" + tag + ">{{end}}"
}

func targetAttr(b *Block) string {
	if attrString(b.Attrs, "linkTarget") == "_blank" {
		return ` target="_blank" rel="noopener"`
	}
	return ""
}

func (e *emitter) siteLogo(b *Block) string {
	cls := []string{"wp-block-site-logo"}
	if w := attrInt(b.Attrs, "width", 0); w > 0 && w < 2000 {
		cls = append(cls, e.c.instance("wps", []rule{{" img", []decl{{"width", strconv.Itoa(w) + "px"}, {"height", "auto"}}}}, true))
	}
	inner := `{{template "shared/logo" $}}`
	if v, ok := b.Attrs["isLink"].(bool); !ok || v {
		inner = `<a href="{{link "/"}}" class="custom-logo-link" rel="home">` + inner + `</a>`
	}
	return `{{if $.Site.Custom.Logo}}<div` + classAttr(cls, e.supportClasses(b, true)) + ">" + inner + "</div>{{end}}"
}

// navigation writes a navigation block as a menu location of the theme:
// the site's menu, which the administrator edits, in the block's place.
func (e *emitter) navigation(b *Block) string {
	n := 0
	if e.navs != nil {
		*e.navs++
		n = *e.navs
	}
	loc, label := e.c.menuLocation(e.region, n)
	id := "wpnav-" + loc
	cls := []string{"wp-block-navigation", "is-responsive"}
	if l := attrMap(b.Attrs, "layout"); l != nil {
		if j := attrString(l, "justifyContent"); slices.Contains([]string{"left", "center", "right", "space-between"}, j) {
			cls = append(cls, "items-justified-"+j)
		}
		if attrString(l, "orientation") == "vertical" {
			cls = append(cls, "is-vertical")
		}
	}
	overlay := attrString(b.Attrs, "overlayMenu")
	switch overlay {
	case "never":
		cls = append(cls, "sap-wp-nav-never")
	case "always":
		cls = append(cls, "sap-wp-nav-always")
	default:
		cls = append(cls, "sap-wp-nav-mobile")
	}
	out := `<nav` + classAttr(cls, e.supportClasses(b, true), e.layoutClasses(b)) + ` aria-label="{{T "` + label + `"}}">` +
		`<ul id="` + id + `" class="wp-block-navigation__container wp-block-navigation">{{template "shared/menu" ($.Site.Menu "` + loc + `")}}</ul>`
	if overlay != "never" {
		out += `{{template "shared/nav-toggle" "` + id + `"}}`
	}
	out += `</nav>`
	if e.region == "header" && e.tools != nil && !*e.tools && n == 1 {
		*e.tools = true
		out += toolsHTML
	}
	return out
}

func (e *emitter) socialLink(b *Block) string {
	u := strings.TrimSpace(attrString(b.Attrs, "url"))
	service := cssIdent(attrString(b.Attrs, "service"))
	if service == "" {
		service = "link"
	}
	switch {
	case service == "mail" && strings.Contains(u, "@") && !strings.Contains(u, ":"):
		u = "mailto:" + u
	case service != "mail" && service != "feed" && u != "" && !strings.Contains(u, ":") && !strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "#"):
		u = "https://" + u
	}
	if u == "" || !safeURL(stripMarks(u)) {
		return ""
	}
	href := escapeText(u)
	label := strings.TrimSpace(attrString(b.Attrs, "label"))
	name := socialNames[service]
	if name == "" {
		name = strings.ToUpper(service[:1]) + service[1:]
	}
	if label == "" {
		label = name
	}
	var icon string
	switch service {
	case "mail":
		icon = `{{icon "mail"}}`
	case "feed":
		icon = `{{icon "rss"}}`
	default:
		// Lucide has no brand icons, and WordPress's are GPL drawings of
		// trademarks: the service's name stands in for its logo.
		icon = `<span class="sap-wp-social-name" aria-hidden="true">` + escapeText(name) + `</span>`
	}
	return `<li` + classAttr([]string{"wp-social-link", "wp-social-link-" + service, "wp-block-social-link"}, e.supportClasses(b, true)) +
		`><a href="` + href + `" class="wp-block-social-link-anchor" rel="noopener nofollow" target="_blank">` + icon +
		`<span class="wp-block-social-link-label screen-reader-text">` + escapeText(label) + `</span></a></li>`
}

var socialNames = map[string]string{
	"facebook": "Facebook", "x": "X", "twitter": "Twitter", "instagram": "Instagram", "linkedin": "LinkedIn",
	"youtube": "YouTube", "tiktok": "TikTok", "github": "GitHub", "mastodon": "Mastodon", "bluesky": "Bluesky",
	"threads": "Threads", "pinterest": "Pinterest", "wordpress": "WordPress", "whatsapp": "WhatsApp",
	"telegram": "Telegram", "dribbble": "Dribbble", "behance": "Behance", "vimeo": "Vimeo", "twitch": "Twitch",
	"spotify": "Spotify", "soundcloud": "SoundCloud", "reddit": "Reddit", "tumblr": "Tumblr", "medium": "Medium",
	"link": "Link", "chain": "Link",
}

// queryBlock writes a query: its saved wrapper and inner blocks, which
// list the posts of the page.
func (e *emitter) queryBlock(b *Block) string {
	q := attrMap(b.Attrs, "query")
	s := e.sub()
	s.query = &queryInfo{inherit: attrBool(q, "inherit"), perPage: attrInt(q, "perPage", 0), offset: attrInt(q, "offset", 0)}
	if !s.query.inherit && e.kind != "single" && e.kind != "page" {
		var picks []string
		for _, k := range []string{"postType", "taxQuery", "author", "search", "sticky", "parents"} {
			switch v := q[k].(type) {
			case string:
				if v != "" && v != "post" {
					picks = append(picks, k)
				}
			case map[string]any, []any:
				if !emptyValue(v) {
					picks = append(picks, k)
				}
			}
		}
		if len(picks) > 0 {
			e.note("block.query-custom", strings.Join(picks, ", "))
		}
	}
	migrateDisplayLayout(b)
	out, _ := s.static(b, slices.Concat(e.layoutClasses(b), []string{e.elementClass(b)}), nil)
	return out
}

// migrateDisplayLayout does what WordPress's deprecation of the query
// block does: an old displayLayout of columns ({"type":"flex","columns":3},
// Twenty Twenty-Three) becomes a grid layout of its post template.
func migrateDisplayLayout(b *Block) {
	dl := attrMap(b.Attrs, "displayLayout")
	if attrString(dl, "type") != "flex" {
		return
	}
	n := attrInt(dl, "columns", 3)
	var walk func([]*Block, int)
	walk = func(bs []*Block, depth int) {
		for _, c := range bs {
			if c.Name == "core/post-template" {
				if attrMap(c.Attrs, "layout") == nil {
					if c.Attrs == nil {
						c.Attrs = map[string]any{}
					}
					c.Attrs["layout"] = map[string]any{"type": "grid", "columnCount": float64(n)}
				}
				continue
			}
			if depth < 8 {
				walk(c.Inner, depth+1)
			}
		}
	}
	walk(b.Inner, 0)
}

func emptyValue(v any) bool {
	switch v := v.(type) {
	case map[string]any:
		for _, x := range v {
			if !emptyValue(x) {
				return false
			}
		}
		return true
	case []any:
		return len(v) == 0
	case string:
		return v == ""
	}
	return v == nil
}

// postTemplate writes the posts of a query, each with the template's
// blocks: the page's posts on listings, the previous and next posts on a
// single post (sap13ns has no other list there).
func (e *emitter) postTemplate(b *Block) string {
	s := e.sub()
	s.post = "."
	body := s.blocks(b.Inner)
	item := `<li class="wp-block-post post">` + body + `</li>`
	ul := `<ul` + classAttr([]string{"wp-block-post-template"}, e.supportClasses(b, true), e.layoutClasses(b)) + `>`
	switch {
	case e.kind == "single" || e.kind == "page":
		return ul + `{{with $.Prev}}` + item + `{{end}}{{with $.Next}}` + item + `{{end}}</ul>`
	case e.kind == "404":
		return ""
	case e.query != nil && !e.query.inherit && (e.query.perPage > 0 || e.query.offset > 0):
		off, per := max(e.query.offset, 0), min(max(e.query.perPage, 0), 100)
		cond := ""
		switch {
		case off > 0 && per > 0:
			cond = "and (ge $i " + strconv.Itoa(off) + ") (lt $i " + strconv.Itoa(off+per) + ")"
		case off > 0:
			cond = "ge $i " + strconv.Itoa(off)
		default:
			cond = "lt $i " + strconv.Itoa(per)
		}
		return ul + `{{range $i, $p := $.Posts}}{{if ` + cond + `}}` + item + `{{end}}{{end}}</ul>`
	}
	return ul + `{{range $.Posts}}` + item + `{{end}}</ul>`
}

// pagination writes the pages of a listing.
func (e *emitter) pagination(b *Block) string {
	if e.kind == "single" || e.kind == "page" || e.kind == "404" || e.kind == "front" {
		return ""
	}
	arrow := attrString(b.Attrs, "paginationArrow")
	arrows := map[string][2]string{"arrow": {"←", "→"}, "chevron": {"«", "»"}}[arrow]
	var parts strings.Builder
	for _, in := range b.Inner {
		switch in.Name {
		case "core/query-pagination-previous":
			label := `{{T "Previous"}}`
			if l := strings.TrimSpace(attrString(in.Attrs, "label")); l != "" {
				label = escapeText(l)
			}
			if arrows[0] != "" {
				label = `<span class="wp-block-query-pagination-previous-arrow is-arrow-` + arrow + `" aria-hidden="true">` + arrows[0] + `</span>` + label
			}
			parts.WriteString(`{{with .PrevURL}}<a` + classAttr([]string{"wp-block-query-pagination-previous"}, e.supportClasses(in, true)) + ` href="{{.}}" rel="prev">` + label + `</a>{{end}}`)
		case "core/query-pagination-next":
			label := `{{T "Next"}}`
			if l := strings.TrimSpace(attrString(in.Attrs, "label")); l != "" {
				label = escapeText(l)
			}
			if arrows[1] != "" {
				label += `<span class="wp-block-query-pagination-next-arrow is-arrow-` + arrow + `" aria-hidden="true">` + arrows[1] + `</span>`
			}
			parts.WriteString(`{{with .NextURL}}<a` + classAttr([]string{"wp-block-query-pagination-next"}, e.supportClasses(in, true)) + ` href="{{.}}" rel="next">` + label + `</a>{{end}}`)
		case "core/query-pagination-numbers":
			parts.WriteString(`<div` + classAttr([]string{"wp-block-query-pagination-numbers"}, e.supportClasses(in, true)) + `>{{range .Links}}{{if .Gap}}<span class="page-numbers dots">…</span>{{else if .Current}}<span aria-current="page" class="page-numbers current">{{.Num}}</span>{{else}}<a class="page-numbers" href="{{.URL}}">{{.Num}}</a>{{end}}{{end}}</div>`)
		}
		e.c.report.Blocks[in.Name]++
	}
	return `{{with $.Pagination}}<nav` + classAttr([]string{"wp-block-query-pagination"}, e.supportClasses(b, true), e.layoutClasses(b)) +
		` aria-label="{{T "Pagination"}}">` + parts.String() + `</nav>{{end}}`
}

func (e *emitter) queryTitle(b *Block) string {
	level := attrInt(b.Attrs, "level", 1)
	if level < 1 || level > 6 {
		level = 1
	}
	tag := "h" + strconv.Itoa(level)
	open := "<" + tag + classAttr([]string{"wp-block-query-title"}, e.supportClasses(b, true)) + ">"
	switch e.kind {
	case "archive":
		return open + `{{with $.Archive}}{{.Title}}{{else}}{{$.Heading}}{{end}}</` + tag + `>`
	case "search":
		return open + `{{or $.Heading $.Title}}</` + tag + `>{{template "shared/search-count" $}}`
	case "home":
		return `{{with $.Heading}}` + open + `{{.}}</` + tag + `>{{end}}`
	}
	return ""
}

func (e *emitter) postNavLink(b *Block) string {
	if e.kind != "single" && e.kind != "page" {
		return ""
	}
	// type is "next" unless it says "previous" (its default in block.json).
	field, rel, def := "$.Next", "next", `{{T "Next"}}`
	cls := "post-navigation-link-next"
	if attrString(b.Attrs, "type") == "previous" {
		field, rel, def, cls = "$.Prev", "prev", `{{T "Previous"}}`, "post-navigation-link-previous"
	}
	label := def
	if l := strings.TrimSpace(attrString(b.Attrs, "label")); l != "" {
		label = escapeText(l)
	}
	if attrBool(b.Attrs, "showTitle") {
		label = `{{.Title}}`
		if l := strings.TrimSpace(attrString(b.Attrs, "label")); l != "" {
			label = `<span class="post-navigation-link__label">` + escapeText(l) + `</span> <span class="post-navigation-link__title">{{.Title}}</span>`
		}
	}
	arrow := map[string][2]string{"arrow": {"←", "→"}, "chevron": {"«", "»"}}[attrString(b.Attrs, "arrow")]
	if rel == "prev" && arrow[0] != "" {
		label = `<span class="wp-block-post-navigation-link__arrow-previous is-arrow-` + attrString(b.Attrs, "arrow") + `" aria-hidden="true">` + arrow[0] + `</span>` + label
	}
	if rel == "next" && arrow[1] != "" {
		label += `<span class="wp-block-post-navigation-link__arrow-next is-arrow-` + attrString(b.Attrs, "arrow") + `" aria-hidden="true">` + arrow[1] + `</span>`
	}
	return `<div` + classAttr([]string{cls, "wp-block-post-navigation-link"}, e.supportClasses(b, true)) + `>{{with ` + field + `}}<a href="{{.URL}}" rel="` + rel + `">` + label + `</a>{{end}}</div>`
}

// postWrap gives a post block its post: "." in a loop, the page's post
// on a single post or page. "" where there is no post.
func (e *emitter) postWrap(body string) string {
	switch e.post {
	case ".":
		return body
	case "page":
		return "{{with $.Post}}" + body + "{{end}}"
	}
	return ""
}

func (e *emitter) postBlock(b *Block) (string, bool) {
	a := b.Attrs
	sup := func(base ...string) string { return classAttr(base, e.supportClasses(b, true)) }
	switch b.Name {
	case "core/post-title":
		level := attrInt(a, "level", 2)
		tag := "p"
		if level >= 1 && level <= 6 {
			tag = "h" + strconv.Itoa(level)
		}
		inner := "{{.Title}}"
		if attrBool(a, "isLink") {
			inner = `<a href="{{.URL}}"` + targetAttr(b) + `>{{.Title}}</a>`
		}
		return e.postWrap("<" + tag + sup("wp-block-post-title") + ">" + inner + "</" + tag + ">"), true
	case "core/post-content":
		cls := classAttr([]string{"entry-content", "wp-block-post-content", "prose"}, e.supportClasses(b, true), e.layoutClasses(b))
		return e.postWrap("<div" + cls + ">{{.Content}}</div>"), true
	case "core/post-excerpt":
		more := ""
		if t := strings.TrimSpace(attrString(a, "moreText")); t != "" {
			link := `<a class="wp-block-post-excerpt__more-link" href="{{.URL}}">` + escapeText(t) + `</a>`
			if attrBool(a, "showMoreOnNewLine") || a["showMoreOnNewLine"] == nil {
				more = `<p class="wp-block-post-excerpt__more-text">` + link + `</p>`
			} else {
				more = " " + link
			}
		}
		return e.postWrap(`{{if .Summary}}<div` + sup("wp-block-post-excerpt") + `><p class="wp-block-post-excerpt__excerpt">{{.Summary}}</p>` + more + `</div>{{end}}`), true
	case "core/post-date":
		field := ".Published"
		if attrString(a, "displayType") == "modified" {
			field = ".Updated"
		}
		inner := `<time datetime="{{isoDate ` + field + `}}">{{date ` + field + `}}</time>`
		if attrBool(a, "isLink") {
			inner = `<a href="{{.URL}}">` + inner + `</a>`
		}
		return e.postWrap(`{{if .HasDate}}<div` + sup("wp-block-post-date") + `>` + inner + `</div>{{end}}`), true
	case "core/post-author", "core/avatar":
		size := attrInt(a, "avatarSize", attrInt(a, "size", 48))
		if size < 16 || size > 256 {
			size = 48
		}
		avatar := `<div class="wp-block-post-author__avatar">{{template "shared/avatar" (dict "A" .Author.Avatar "Size" ` + strconv.Itoa(size) + `)}}</div>`
		if b.Name == "core/avatar" {
			return e.postWrap(`<div` + sup("wp-block-avatar") + `>{{template "shared/avatar" (dict "A" .Author.Avatar "Size" ` + strconv.Itoa(size) + ` "Class" "avatar wp-block-avatar__image")}}</div>`), true
		}
		if v, ok := a["showAvatar"].(bool); ok && !v {
			avatar = ""
		}
		name := `{{.Author.Name}}`
		if attrBool(a, "isLink") {
			name = `{{if .Author.URL}}<a href="{{.Author.URL}}">{{.Author.Name}}</a>{{else}}{{.Author.Name}}{{end}}`
		}
		return e.postWrap(`<div` + sup("wp-block-post-author") + `>` + avatar + `<div class="wp-block-post-author__content"><p class="wp-block-post-author__name">` + name + `</p></div></div>`), true
	case "core/post-author-name":
		name := `{{.Author.Name}}`
		if attrBool(a, "isLink") {
			name = `{{if .Author.URL}}<a href="{{.Author.URL}}" class="wp-block-post-author-name__link">{{.Author.Name}}</a>{{else}}{{.Author.Name}}{{end}}`
		}
		return e.postWrap(`<div` + sup("wp-block-post-author-name") + `>` + name + `</div>`), true
	case "core/post-featured-image":
		return e.featuredImage(b), true
	case "core/post-terms":
		field, tax := ".Categories", "category"
		if t := attrString(a, "term"); t == "post_tag" {
			field, tax = ".Tags", "post_tag"
		} else if t != "" && t != "category" {
			return "", true
		}
		sep := ", "
		if s, ok := a["separator"].(string); ok {
			sep = s
		}
		prefix, suffix := "", ""
		if p := attrString(a, "prefix"); p != "" {
			prefix = `<span class="wp-block-post-terms__prefix">` + escapeText(p) + `</span>`
		}
		if s := attrString(a, "suffix"); s != "" {
			suffix = `<span class="wp-block-post-terms__suffix">` + escapeText(s) + `</span>`
		}
		return e.postWrap(`{{with ` + field + `}}<div` + sup("taxonomy-"+tax, "wp-block-post-terms") + `>` + prefix +
			`{{range $i, $t := .}}{{if $i}}<span class="wp-block-post-terms__separator">` + escapeText(sep) + `</span>{{end}}<a href="{{$t.URL}}" rel="tag">{{$t.Name}}</a>{{end}}` +
			suffix + `</div>{{end}}`), true
	case "core/read-more":
		text := strings.TrimSpace(attrString(a, "content"))
		label := `{{T "Read more"}}`
		if text != "" {
			label = escapeText(text)
		}
		return e.postWrap(`<a` + sup("wp-block-read-more") + ` href="{{.URL}}"` + targetAttr(b) + `>` + label + `</a>`), true
	case "core/post-time-to-read":
		return e.postWrap(`{{if .ReadingMinutes}}<div` + sup("wp-block-post-time-to-read") + `>{{plural .ReadingMinutes "%d min read" "%d min read"}}</div>{{end}}`), true
	}
	return "", false
}

// featuredImage writes a post's picture, sized as the block says.
func (e *emitter) featuredImage(b *Block) string {
	a := b.Attrs
	var img []decl
	if v := cssValue(attrString(a, "aspectRatio")); v != "" && v != "auto" {
		img = append(img, decl{"aspect-ratio", v}, decl{"width", "100%"}, decl{"height", "100%"})
	}
	if v := cssValue(attrString(a, "height")); v != "" {
		img = append(img, decl{"height", v})
	}
	if v := cssValue(attrString(a, "width")); v != "" {
		img = append(img, decl{"width", v})
	}
	if v := attrString(a, "scale"); slices.Contains([]string{"cover", "contain", "fill"}, v) {
		img = append(img, decl{"object-fit", v})
	} else if len(img) > 0 {
		img = append(img, decl{"object-fit", "cover"})
	}
	imgClass := "wp-post-image"
	if c := e.c.classFor("wpi", img); c != "" {
		imgClass += " " + c
	}
	inner := `{{template "shared/image" (dict "Image" .Image "Sizes" "(min-width: 1200px) 1200px, 100vw" "Class" "` + imgClass + `")}}`
	if attrBool(a, "isLink") {
		inner = `<a href="{{.URL}}"` + targetAttr(b) + `>` + inner + `</a>`
	}
	return e.postWrap(`{{if .Image}}<figure` + classAttr([]string{"wp-block-post-featured-image"}, e.supportClasses(b, true)) + `>` + inner + `</figure>{{end}}`)
}
