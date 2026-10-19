package export

import (
	"context"
	"html"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"sap13ns/internal/store"
)

// Menus in WXR. WordPress keeps a menu as a term of the nav_menu taxonomy
// (<wp:term> in the channel) and each of its links as a post of type
// nav_menu_item in that term, described by postmeta: _menu_item_type
// ("custom", "post_type" or "taxonomy"), _menu_item_object ("custom",
// "post", "page", "category", "post_tag"), _menu_item_object_id (the WXR
// id of the post or term, or the item's own id for a custom link),
// _menu_item_url (custom links only) and _menu_item_menu_item_parent.
//
// A sap13ns menu is one location in one language: the location's menu
// ("primary") and, per language that has its own, "primary-en". A link to
// an exported post, page, category or tag becomes a link to that object,
// which the WordPress Importer points at the imported one; any other link
// is a custom link with an absolute address. sap13ns menus are flat, so
// every item is at the top level. WordPress does not assign the menus to
// the theme's locations on import: that is one click in Appearance → Menus.

type menu struct {
	termID int64
	slug   string
	name   string
	items  []store.MenuItem
}

// loadMenus reads the menus once, before the channel's terms are written.
func (e *exporter) loadMenus(ctx context.Context) error {
	if e.o.NoMenus {
		return nil
	}
	all, err := e.st.AllMenus(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	maxMedia, maxTerm, err := e.st.ExportIDBounds(ctx)
	if err != nil {
		return err
	}
	for i, k := range keys {
		items := all[k]
		if len(items) == 0 {
			continue
		}
		loc, lang := items[0].Location, items[0].Lang
		m := menu{termID: maxTerm + int64(i) + 1, slug: menuSlug(loc, lang), name: menuName(loc, lang), items: items}
		e.menus = append(e.menus, m)
	}
	// Posts keep their ids and attachments come after them (writeMedia);
	// menu items come after both.
	base, err := e.st.MaxPostID(ctx)
	if err != nil {
		return err
	}
	e.menuBase = base + maxMedia
	return nil
}

func menuSlug(location, lang string) string {
	if lang == "" {
		return location
	}
	return location + "-" + lang
}

// menuName is a readable name for a location: "footer-links" becomes
// "Footer links", with the language of a language's own menu.
func menuName(location, lang string) string {
	words := strings.FieldsFunc(location, func(r rune) bool { return r == '-' || r == '_' })
	name := strings.Join(words, " ")
	if name == "" {
		name = location
	}
	r := []rune(name)
	r[0] = unicode.ToUpper(r[0])
	name = string(r)
	if lang != "" {
		name += " (" + lang + ")"
	}
	return name
}

func (e *exporter) writeMenuTerms(ctx context.Context, x *xmlWriter) error {
	if err := e.loadMenus(ctx); err != nil {
		return err
	}
	for _, m := range e.menus {
		x.raw("\t<wp:term>")
		x.intEl("wp:term_id", m.termID)
		x.cdataEl("wp:term_taxonomy", "nav_menu")
		x.cdataEl("wp:term_slug", m.slug)
		x.cdataEl("wp:term_parent", "")
		x.cdataEl("wp:term_name", html.EscapeString(m.name))
		x.raw("</wp:term>\n")
		e.rep.Menus++
	}
	return x.err
}

// menuTarget is what a menu link points at in WordPress terms.
type menuTarget struct {
	typ, object string
	objectID    int64 // 0: the item itself (a custom link)
	url         string
}

// resolve maps the address of a menu item to an exported post, page,
// category or tag, or to a custom link with an absolute address.
func (e *exporter) resolve(raw string) menuTarget {
	custom := menuTarget{typ: "custom", object: "custom", url: raw}
	u, err := url.Parse(raw)
	if err != nil {
		return custom
	}
	switch {
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
		custom.url = e.ownURL(raw)
	case (u.Scheme == "http" || u.Scheme == "https") && e.sameSite(u):
		if e.o.WPBase != "" {
			rel := *u
			rel.Scheme, rel.Host, rel.User = "", "", nil
			custom.url = e.ownURL(rel.String())
		}
	default:
		return custom // another site, mailto:, #anchors
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return custom
	}
	path := strings.TrimSuffix(u.Path, "/")
	if path == "" {
		return custom // the home page
	}
	if id, ok := e.posts[path]; ok {
		return menuTarget{typ: "post_type", object: e.postType[id], objectID: id}
	}
	// A term archive, possibly in another language ("/en/category/news").
	candidates := []string{path}
	if code, after, ok := strings.Cut(strings.TrimPrefix(path, "/"), "/"); ok && langCode(code) {
		candidates = append(candidates, "/"+after)
	}
	for _, rest := range candidates {
		for _, t := range []struct{ prefix, taxonomy, object string }{
			{"/category/", "category", "category"}, {"/tag/", "tag", "post_tag"},
		} {
			if slug, ok := strings.CutPrefix(rest, t.prefix); ok && !strings.Contains(slug, "/") {
				if id, ok := e.termIDs[t.taxonomy+"/"+slug]; ok {
					return menuTarget{typ: "taxonomy", object: t.object, objectID: id}
				}
			}
		}
	}
	return custom
}

// ownURL is the address of a path of the site: on the WordPress site it
// moves to, when there is one (WPBase).
func (e *exporter) ownURL(path string) string {
	if e.o.WPBase != "" {
		return e.o.WPBase + path
	}
	return e.o.Site.URL + path
}

func (e *exporter) sameSite(u *url.URL) bool {
	site, err := url.Parse(e.o.Site.URL)
	return err == nil && strings.EqualFold(site.Host, u.Host)
}

// langCode reports whether s looks like the language prefix of a path.
func langCode(s string) bool {
	if len(s) < 2 || len(s) > 12 {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '-' && i >= 2 || r >= '0' && r <= '9' && i >= 3) {
			return false
		}
	}
	return true
}

func (e *exporter) writeMenuItems(x *xmlWriter) error {
	n := int64(0)
	for _, m := range e.menus {
		for pos, it := range m.items {
			n++
			id := e.menuBase + n
			t := e.resolve(it.URL)
			objectID := t.objectID
			if objectID == 0 {
				objectID = id
			}
			link := t.url
			if link == "" {
				link = e.o.Site.URL + "/?p=" + strconv.FormatInt(objectID, 10)
			}
			x.raw("\t<item>\n")
			x.cdataEl("title", html.EscapeString(it.Label))
			x.textEl("link", link)
			x.textEl("pubDate", it.CreatedAt.UTC().Format(rssDate))
			x.cdataEl("dc:creator", "")
			x.raw(`<guid isPermaLink="false">`)
			x.text(e.o.Site.URL + "/?p=" + strconv.FormatInt(id, 10))
			x.raw("</guid>")
			x.raw("<description></description>")
			x.cdataEl("content:encoded", "")
			x.cdataEl("excerpt:encoded", "")
			x.intEl("wp:post_id", id)
			e.dates(x, "wp:post_date", &it.CreatedAt)
			e.dates(x, "wp:post_modified", &it.CreatedAt)
			x.cdataEl("wp:comment_status", "closed")
			x.cdataEl("wp:ping_status", "closed")
			x.cdataEl("wp:post_name", strconv.FormatInt(id, 10))
			x.cdataEl("wp:status", "publish")
			x.intEl("wp:post_parent", 0)
			x.intEl("wp:menu_order", int64(pos+1))
			x.cdataEl("wp:post_type", "nav_menu_item")
			x.cdataEl("wp:post_password", "")
			x.intEl("wp:is_sticky", 0)
			x.raw(`<category domain="nav_menu" nicename="`)
			x.text(m.slug)
			x.raw(`">`)
			x.cdata(html.EscapeString(m.name))
			x.raw("</category>")
			x.meta("_menu_item_type", t.typ)
			x.meta("_menu_item_menu_item_parent", "0")
			x.meta("_menu_item_object_id", strconv.FormatInt(objectID, 10))
			x.meta("_menu_item_object", t.object)
			x.meta("_menu_item_target", "")
			x.meta("_menu_item_classes", `a:1:{i:0;s:0:"";}`)
			x.meta("_menu_item_xfn", "")
			x.meta("_menu_item_url", t.url)
			x.raw("\n\t</item>\n")
			e.rep.MenuItems++
		}
	}
	return x.err
}
