package main

import (
	"strconv"
	"strings"

	"github.com/vicktor/sap13ns/pluginsdk"
)

// The "latest" widget lists the newest posts in the page's language.
// QueryPosts needs the "posts:read" permission and only ever returns
// published posts.
func init() {
	widgets["latest"] = func(req *pluginsdk.Request, w pluginsdk.Widget) (string, error) {
		lang := req.Site.Language
		if w.Page != nil && w.Page.Lang != "" {
			lang = w.Page.Lang
		}
		posts, err := pluginsdk.QueryPosts(pluginsdk.PostsQuery{Type: "post", Lang: lang, Limit: widgetCount(w.Config["count"])})
		if err != nil {
			return "", err
		}
		return LatestHTML(req.Setting("greeting"), posts), nil
	}
}

// widgetCount reads the per-widget "count" field (1 to 10).
func widgetCount(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 3
	}
	return min(n, 10)
}

// LatestHTML renders the list.
func LatestHTML(greeting string, posts []pluginsdk.Post) string {
	if len(posts) == 0 {
		return ""
	}
	if strings.TrimSpace(greeting) == "" {
		greeting = "Hello"
	}
	var b strings.Builder
	b.WriteString(`<p class="latest-greeting">` + pluginsdk.Escape(greeting) + ", reader!</p>")
	b.WriteString(`<ul class="latest-posts">`)
	for _, p := range posts {
		b.WriteString(`<li><a href="` + pluginsdk.Escape(p.URL) + `">` + pluginsdk.Escape(p.Title) + "</a></li>")
	}
	b.WriteString("</ul>")
	return b.String()
}
