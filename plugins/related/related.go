package main

// Related finds the posts that share tags and categories with a post and
// renders them like the host's "Recent posts" widget (.widget-posts).

import (
	"fmt"
	"html"
	"net/url"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // the sandbox has no zoneinfo files
	"unicode"

	"github.com/vicktor/sap13ns/pluginsdk"
)

const (
	// maxQueries is the host's limit of database calls per hook call.
	maxQueries = 10
	// queryLimit is the most posts one QueryPosts call returns.
	queryLimit = 20
	// maxCount is the most related posts shown.
	maxCount = 10
	// searchWords is how many words of the page title the widget searches
	// for to find the post it is shown with.
	searchWords = 8
)

// Querier runs one QueryPosts call: pluginsdk.QueryPosts in the plugin, a
// fake in tests.
type Querier func(pluginsdk.PostsQuery) ([]pluginsdk.Post, error)

// Options are the settings for one call.
type Options struct {
	Count        int  // posts to show, 1-10
	Images       bool // show the featured image
	AfterContent bool // append the block to posts (content.filter)
	Spanish      bool
	Title        string         // block title, plain text
	Location     *time.Location // site time zone for the dates
}

type termRef struct{ taxonomy, slug string }

// plan picks the terms of p to query: tags first, then categories, at most
// budget in total. When p has categories at least one of them is kept, so a
// post with many tags still finds posts of its category.
func plan(p *pluginsdk.Post, budget int) []termRef {
	var tags, cats []termRef
	seen := map[termRef]bool{}
	for _, t := range p.Terms {
		r := termRef{t.Taxonomy, t.Slug}
		if t.Slug == "" || seen[r] {
			continue
		}
		seen[r] = true
		switch t.Taxonomy {
		case "tag":
			tags = append(tags, r)
		case "category":
			cats = append(cats, r)
		}
	}
	if budget <= 0 {
		return nil
	}
	nc := min(len(cats), max(budget-len(tags), 1))
	nt := min(len(tags), budget-nc)
	return append(tags[:nt:nt], cats[:nc]...)
}

type candidate struct {
	post       pluginsdk.Post
	terms      map[termRef]bool
	tags, cats int // shared with the current post
	published  time.Time
}

// Related returns up to count published posts in lang that share terms with
// p: most shared tags first, then most shared categories, then newest. It
// makes at most budget queries; posts that share nothing are never listed.
func Related(p *pluginsdk.Post, lang string, count, budget int, query Querier) ([]pluginsdk.Post, error) {
	if p == nil || count <= 0 {
		return nil, nil
	}
	terms := plan(p, budget)
	if len(terms) == 0 {
		return nil, nil
	}
	mine := map[termRef]bool{}
	for _, t := range p.Terms {
		mine[termRef{t.Taxonomy, t.Slug}] = true
	}
	found := map[int64]*candidate{}
	for _, t := range terms {
		q := pluginsdk.PostsQuery{Type: "post", Lang: lang, Exclude: p.ID, Limit: queryLimit}
		if t.taxonomy == "tag" {
			q.Tag = t.slug
		} else {
			q.Category = t.slug
		}
		posts, err := query(q)
		if err != nil {
			return nil, err
		}
		for _, post := range posts {
			if post.ID <= 0 || post.ID == p.ID || (post.Type != "" && post.Type != "post") ||
				(lang != "" && !strings.EqualFold(post.Lang, lang)) {
				continue
			}
			c := found[post.ID]
			if c == nil {
				c = &candidate{post: post, terms: map[termRef]bool{}}
				c.published, _ = time.Parse(time.RFC3339, post.PublishedAt)
				found[post.ID] = c
			}
			// The term it was found by counts even if the host left its
			// terms out.
			c.terms[t] = true
			for _, pt := range post.Terms {
				c.terms[termRef{pt.Taxonomy, pt.Slug}] = true
			}
		}
	}
	list := make([]*candidate, 0, len(found))
	for _, c := range found {
		for r := range c.terms {
			if !mine[r] {
				continue
			}
			if r.taxonomy == "tag" {
				c.tags++
			} else {
				c.cats++
			}
		}
		if c.tags+c.cats > 0 {
			list = append(list, c)
		}
	}
	slices.SortFunc(list, func(a, b *candidate) int {
		switch {
		case a.tags != b.tags:
			return b.tags - a.tags
		case a.cats != b.cats:
			return b.cats - a.cats
		case !a.published.Equal(b.published):
			return b.published.Compare(a.published)
		}
		return compareIDs(b.post.ID, a.post.ID)
	})
	out := make([]pluginsdk.Post, 0, min(count, len(list)))
	for _, c := range list[:min(count, len(list))] {
		out = append(out, c.post)
	}
	return out, nil
}

func compareIDs(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Current finds the post a single page shows. The widget payload has no
// post (only kind, URL, language and title), so it searches for the words
// of the page title and keeps the result whose URL is the page URL. It
// returns the number of queries it made.
func Current(page *pluginsdk.Page, lang string, query Querier) (*pluginsdk.Post, int, error) {
	if page == nil || page.Kind != "single" {
		return nil, 0, nil
	}
	if p := page.Post; p != nil && p.ID > 0 && p.Terms != nil {
		// A host that sends the post saves the search.
		if p.Type != "" && p.Type != "post" {
			return nil, 0, nil
		}
		return p, 0, nil
	}
	want := strings.TrimSuffix(page.URL, "/")
	if want == "" {
		return nil, 0, nil
	}
	posts, err := query(pluginsdk.PostsQuery{Type: "post", Lang: lang, Search: titleWords(page.Title), Limit: queryLimit})
	if err != nil {
		return nil, 1, err
	}
	for i := range posts {
		if strings.TrimSuffix(posts[i].URL, "/") == want {
			return &posts[i], 1, nil
		}
	}
	return nil, 1, nil
}

// titleWords keeps the letters and digits of a title, as at most
// searchWords words, so the host's web-search syntax sees no quotes or
// minus signs.
func titleWords(title string) string {
	words := strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return strings.Join(words[:min(len(words), searchWords)], " ")
}

// Items renders posts as the <li> elements of a .widget-posts list.
func Items(posts []pluginsdk.Post, o Options) string {
	loc := o.Location
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder
	for _, p := range posts {
		if !safeURL(p.URL) {
			continue
		}
		b.WriteString(`<li><a href="` + html.EscapeString(p.URL) + `">`)
		if o.Images && p.Image != "" && safeURL(p.Image) {
			b.WriteString(`<img src="` + html.EscapeString(p.Image) + `" alt="" width="64" height="64" loading="lazy">`)
		}
		b.WriteString(`<span>` + html.EscapeString(p.Title))
		if t, err := time.Parse(time.RFC3339, p.PublishedAt); err == nil {
			b.WriteString(`<time datetime="` + t.UTC().Format(time.RFC3339) + `">` + html.EscapeString(shortDate(t.In(loc), o.Spanish)) + `</time>`)
		}
		b.WriteString(`</span></a></li>`)
	}
	return b.String()
}

// Block renders the card appended after the post content; "" when there is
// nothing to list.
func Block(posts []pluginsdk.Post, o Options) string {
	items := Items(posts, o)
	if items == "" {
		return ""
	}
	return `<aside class="card related-posts" aria-label="` + ariaLabel(o.Title) + `"><header><h2 class="related-posts-title">` +
		html.EscapeString(o.Title) + `</h2></header><section><ul class="widget-posts related-posts-list">` + items + `</ul></section></aside>`
}

// Widget renders the widget body; "" when there is nothing to list.
func Widget(posts []pluginsdk.Post, o Options) string {
	items := Items(posts, o)
	if items == "" {
		return ""
	}
	return `<ul class="widget-posts related-widget">` + items + `</ul>`
}

var spanishMonths = [...]string{"enero", "febrero", "marzo", "abril", "mayo", "junio", "julio",
	"agosto", "septiembre", "octubre", "noviembre", "diciembre"}

// shortDate formats a date like the host's widgets: "Jan 2, 2026" or
// "2 ene 2026".
func shortDate(t time.Time, spanish bool) string {
	if spanish {
		return fmt.Sprintf("%d %s %d", t.Day(), string([]rune(spanishMonths[t.Month()-1])[:3]), t.Year())
	}
	return fmt.Sprintf("%s %d, %d", t.Month().String()[:3], t.Day(), t.Year())
}

// ariaLabel keeps an attribute value the host sanitizer accepts.
func ariaLabel(s string) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r == '"' {
			return -1
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return html.EscapeString(s)
}

// safeURL accepts absolute http(s) URLs and site paths.
func safeURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00\"<>\\") {
		return false
	}
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}
