// The schema plugin describes every public page with Schema.org JSON-LD.
//
// Build returns one object with an "@graph" of nodes linked by "@id": the
// WebSite (with a SearchAction on the home page), the publisher
// (Organization or Person), the WebPage (CollectionPage for archives and
// listings, SearchResultsPage for search), a BlogPosting/Article/NewsArticle
// for posts, the primary image and a BreadcrumbList.
//
// Everything comes from the page object the host sends to head.meta; the
// plugin makes no host calls besides logging. This file is plain Go so it
// can be tested natively; handler.go adapts the plugin SDK and main.go
// registers it (wasip1 only).
package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// Size limits. The host refuses objects over 16 KiB; maxBytes leaves room
// for the escaping and compaction it applies.
const (
	maxBytes       = 16<<10 - 512
	maxNameRunes   = 200
	maxDescRunes   = 300
	maxHeadline    = 110
	maxKeywords    = 10
	maxSameAs      = 10
	defaultArticle = "BlogPosting"
)

// Config holds the plugin settings and the site the request came from.
type Config struct {
	SiteURL       string // base URL without a trailing slash
	SiteTitle     string
	SiteLang      string // the site's main language
	ArticleType   string // BlogPosting | Article | NewsArticle
	PublisherType string // Organization | Person
	PublisherName string
	PublisherURL  string
	PublisherLogo string
	SameAs        []string
	Breadcrumbs   bool
	SearchAction  bool
	HomeLabel     map[string]string // "": any language, "es", "en"
}

// ConfigFrom reads the plugin settings. Invalid values fall back to their
// defaults; the returned warnings explain what was ignored.
func ConfigFrom(settings map[string]string, site pluginsdk.Site) (Config, []string) {
	get := func(k string) string { return strings.TrimSpace(settings[k]) }
	var warnings []string
	c := Config{
		SiteURL:       strings.TrimRight(strings.TrimSpace(site.URL), "/"),
		SiteTitle:     strings.TrimSpace(site.Title),
		SiteLang:      strings.ToLower(strings.TrimSpace(site.Language)),
		ArticleType:   defaultArticle,
		PublisherType: "Organization",
		PublisherName: get("publisher_name"),
		Breadcrumbs:   get("breadcrumbs") != "false",
		SearchAction:  get("search_action") != "false",
		HomeLabel:     map[string]string{"": get("home_label"), "es": get("home_label_es"), "en": get("home_label_en")},
	}
	switch t := get("article_type"); t {
	case "BlogPosting", "Article", "NewsArticle":
		c.ArticleType = t
	}
	if get("publisher_type") == "Person" {
		c.PublisherType = "Person"
	}
	if c.PublisherName == "" {
		c.PublisherName = c.SiteTitle
	}
	c.PublisherURL = c.SiteURL + "/"
	if u := get("publisher_url"); u != "" {
		if httpURL(u) {
			c.PublisherURL = u
		} else {
			warnings = append(warnings, "publisher_url is not an http(s) address; using the site address")
		}
	}
	if u := get("publisher_logo"); u != "" && httpsURL(u) {
		c.PublisherLogo = u
	}
	c.SameAs, warnings = parseSameAs(get("same_as"), warnings)
	return c, warnings
}

// parseSameAs reads profile URLs written one per line (or separated by
// spaces or commas). Only https:// URLs are kept, without duplicates.
func parseSameAs(s string, warnings []string) ([]string, []string) {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ',' }) {
		switch {
		case !httpsURL(f):
			warnings = append(warnings, "same_as: ignored "+truncateRunes(f, 80)+" (not an https:// address)")
		case seen[f]:
		case len(out) == maxSameAs:
			warnings = append(warnings, "same_as: only the first 10 profiles are used")
			return out, warnings
		default:
			seen[f] = true
			out = append(out, f)
		}
	}
	return out, warnings
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil && !strings.ContainsAny(s, " \t\r\n")
}

func httpsURL(s string) bool { return httpURL(s) && strings.HasPrefix(strings.ToLower(s), "https://") }

// spanish reports whether a language code is Spanish ("es", "es-MX", "es_AR").
func spanish(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	return l == "es" || strings.HasPrefix(l, "es-") || strings.HasPrefix(l, "es_")
}

// homeLabel is the breadcrumb name of the home page in lang.
func (c Config) homeLabel(lang string) string {
	key := "en"
	if spanish(lang) {
		key = "es"
	}
	if v := c.HomeLabel[key]; v != "" {
		return truncateRunes(v, maxNameRunes)
	}
	if v := c.HomeLabel[""]; v != "" {
		return truncateRunes(v, maxNameRunes)
	}
	if key == "es" {
		return "Inicio"
	}
	return "Home"
}

// homeURL is the home page of lang: the site root for the main language,
// /<lang>/ for the others (the host's langPath).
func (c Config) homeURL(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" || lang == c.SiteLang || !langCode(lang) {
		return c.SiteURL + "/"
	}
	return c.SiteURL + "/" + lang + "/"
}

func langCode(s string) bool {
	if len(s) < 2 || len(s) > 12 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// isHome reports whether the page is the home page of its language: the
// latest-posts home, or a static front page served at the home URL.
func (c Config) isHome(page pluginsdk.Page) bool {
	home := strings.TrimRight(c.homeURL(page.Lang), "/")
	return strings.TrimRight(page.URL, "/") == home
}

// Build returns the JSON-LD object for a page, or nil when the page gets
// none (404 pages, pages without a URL).
func Build(page pluginsdk.Page, c Config) *Node {
	if page.Kind == "404" || page.URL == "" || c.SiteURL == "" {
		return nil
	}
	lang := page.Lang
	if lang == "" {
		lang = c.SiteLang
	}
	home := c.homeURL(lang)
	isHome := c.isHome(page)
	ids := struct{ website, publisher, webpage, article, image, breadcrumb string }{
		website:    home + "#website",
		publisher:  c.SiteURL + "/#publisher",
		webpage:    page.URL + "#webpage",
		article:    page.URL + "#article",
		image:      page.URL + "#primaryimage",
		breadcrumb: page.URL + "#breadcrumb",
	}
	post := page.Post
	if post != nil && post.URL == "" {
		post = nil
	}

	// WebSite and publisher.
	website := node("WebSite", ids.website).
		set("url", home).
		set("name", truncateRunes(c.SiteTitle, maxNameRunes)).
		set("inLanguage", lang).
		set("publisher", ref(ids.publisher))
	if isHome && c.SearchAction {
		website.set("potentialAction", node("SearchAction", "").
			set("target", node("EntryPoint", "").set("urlTemplate", home+"search?q={search_term_string}")).
			set("query-input", "required name=search_term_string"))
	}
	publisher := node(c.PublisherType, ids.publisher).
		set("name", truncateRunes(c.PublisherName, maxNameRunes)).
		set("url", c.PublisherURL)
	if c.PublisherLogo != "" {
		logo := node("ImageObject", c.SiteURL+"/#logo").set("url", c.PublisherLogo).set("contentUrl", c.PublisherLogo)
		if c.PublisherType == "Organization" {
			publisher.set("logo", logo)
		} else {
			publisher.set("image", logo)
		}
	}
	if len(c.SameAs) > 0 {
		publisher.set("sameAs", c.SameAs)
	}

	// The page itself.
	pageType := "WebPage"
	switch {
	case page.Kind == "archive" || page.Kind == "home" && post == nil:
		pageType = "CollectionPage"
	case page.Kind == "search":
		pageType = "SearchResultsPage"
	}
	name := page.Title
	if name == "" && post != nil {
		name = post.Title
	}
	if name == "" {
		name = c.SiteTitle
	}
	desc := page.Description
	if desc == "" && post != nil {
		desc = post.Excerpt
	}
	webpage := node(pageType, ids.webpage).
		set("url", page.URL).
		set("name", truncateRunes(name, maxNameRunes)).
		set("description", truncateRunes(collapse(desc), maxDescRunes)).
		set("inLanguage", lang).
		set("isPartOf", ref(ids.website))

	graph := []*Node{website, publisher, webpage}

	var image *Node
	if post != nil && post.Image != "" && httpURL(post.Image) {
		image = node("ImageObject", ids.image).set("url", post.Image).set("contentUrl", post.Image)
		webpage.set("primaryImageOfPage", ref(ids.image))
		graph = append(graph, image)
	}
	if post != nil {
		webpage.set("datePublished", post.PublishedAt).set("dateModified", post.UpdatedAt)
	}

	// The post.
	if post != nil && post.Type != "page" && (page.Kind == "single" || page.Kind == "page") {
		postLang := post.Lang
		if postLang == "" {
			postLang = lang
		}
		article := node(c.ArticleType, ids.article).
			set("headline", truncateRunes(collapse(post.Title), maxHeadline)).
			set("description", truncateRunes(collapse(desc), maxDescRunes)).
			set("url", post.URL).
			set("mainEntityOfPage", ref(ids.webpage)).
			set("isPartOf", ref(ids.webpage)).
			set("inLanguage", postLang).
			set("datePublished", post.PublishedAt).
			set("dateModified", post.UpdatedAt).
			set("publisher", ref(ids.publisher))
		if a := strings.TrimSpace(post.Author); a != "" {
			article.set("author", node("Person", "").set("name", truncateRunes(a, maxNameRunes)))
		}
		if image != nil {
			article.set("image", ref(ids.image))
		}
		if cats := post.Categories; len(cats) > 0 && strings.TrimSpace(cats[0]) != "" {
			article.set("articleSection", truncateRunes(cats[0], maxNameRunes))
		}
		if kw := keywords(post.Tags); len(kw) > 0 {
			article.set("keywords", kw)
		}
		if post.WordCount > 0 {
			article.set("wordCount", post.WordCount)
		}
		graph = append(graph, article)
	}

	// Breadcrumbs: not on the home page itself, nor on search results.
	if c.Breadcrumbs && !isHome && page.Kind != "search" {
		items := []crumb{{c.homeLabel(lang), home}}
		if post != nil && post.Type != "page" {
			if t, ok := firstCategory(post); ok {
				items = append(items, crumb{t.Name, t.URL})
			}
		}
		items = append(items, crumb{name, page.URL})
		list := node("BreadcrumbList", ids.breadcrumb)
		var elems []*Node
		for i, it := range items {
			elems = append(elems, node("ListItem", "").set("position", i+1).set("name", truncateRunes(collapse(it.name), maxNameRunes)).set("item", it.url))
		}
		list.set("itemListElement", elems)
		webpage.set("breadcrumb", ref(ids.breadcrumb))
		graph = append(graph, list)
	}

	ld := &Node{}
	ld.set("@context", "https://schema.org").set("@graph", graph)
	return fit(ld, graph)
}

type crumb struct{ name, url string }

func firstCategory(p *pluginsdk.Post) (pluginsdk.Term, bool) {
	for _, t := range p.Terms {
		if t.Taxonomy == "category" && strings.TrimSpace(t.Name) != "" && httpURL(t.URL) {
			return t, true
		}
	}
	return pluginsdk.Term{}, false
}

func keywords(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = truncateRunes(collapse(t), 100)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if len(out) == maxKeywords {
			break
		}
	}
	return out
}

// fit drops optional properties, least useful first, until the object is
// small enough for the host. It returns nil if even the smallest form is too
// large (only possible with absurdly long URLs).
func fit(ld *Node, graph []*Node) *Node {
	steps := []func(){
		func() { forEach(graph, func(n *Node) { n.del("keywords") }) },
		func() { forEach(graph, func(n *Node) { n.del("sameAs") }) },
		func() { forEach(graph, func(n *Node) { shorten(n, "description", 120) }) },
		func() { forEach(graph, func(n *Node) { n.del("description") }) },
		func() { forEach(graph, func(n *Node) { n.del("articleSection") }) },
		func() { forEach(graph, func(n *Node) { n.del("logo"); n.del("image"); n.del("primaryImageOfPage") }) },
	}
	for i := 0; ; i++ {
		b, err := json.Marshal(ld)
		if err == nil && len(b) <= maxBytes {
			return ld
		}
		if i == len(steps) {
			return nil
		}
		steps[i]()
	}
}

func forEach(graph []*Node, f func(*Node)) {
	for _, n := range graph {
		f(n)
	}
}

func shorten(n *Node, key string, runes int) {
	if v, ok := n.get(key).(string); ok {
		n.set(key, truncateRunes(v, runes))
	}
}

// collapse joins whitespace runs into single spaces.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncateRunes shortens s to at most n runes, adding "…" when it cuts.
func truncateRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// ------------------------------------------------------------- nodes --

// Node is a JSON object that keeps its keys in insertion order, so the
// JSON-LD in the page source reads "@type", "@id", then the properties.
// Empty strings and nil values are never written.
type Node struct {
	keys []string
	vals map[string]any
}

func node(typ, id string) *Node {
	n := &Node{}
	n.set("@type", typ)
	if id != "" {
		n.set("@id", id)
	}
	return n
}

// ref is a reference to another node of the graph.
func ref(id string) *Node { return (&Node{}).set("@id", id) }

func (n *Node) set(key string, v any) *Node {
	switch x := v.(type) {
	case nil:
		return n
	case string:
		if x == "" {
			return n
		}
	case []*Node:
		if len(x) == 0 {
			return n
		}
	}
	if n.vals == nil {
		n.vals = map[string]any{}
	}
	if _, ok := n.vals[key]; !ok {
		n.keys = append(n.keys, key)
	}
	n.vals[key] = v
	return n
}

func (n *Node) get(key string) any { return n.vals[key] }

func (n *Node) del(key string) {
	if _, ok := n.vals[key]; !ok {
		return
	}
	delete(n.vals, key)
	for i, k := range n.keys {
		if k == key {
			n.keys = append(n.keys[:i], n.keys[i+1:]...)
			break
		}
	}
}

func (n *Node) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range n.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(n.vals[k])
		if err != nil {
			return nil, err
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
