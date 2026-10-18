package web

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
)

// TestPublicMarkup covers the shared theme markup: unique search field ids,
// the announced search count, current terms in widgets, the post
// navigation, scrolling tables and the theme preview kept in pagination.
func TestPublicMarkup(t *testing.T) {
	e := newCustomEnv(t)
	ctx := context.Background()
	st := e.s.settings()
	st.PostsPerPage = 2
	if err := e.st.SaveSettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	e.s.ReloadSettings(ctx)
	cat, err := e.st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "News", Slug: "news"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.st.UserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	for i, title := range []string{"First note", "Second note", "Third note"} {
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		body := "Body of the note."
		if i == 0 {
			body = "| Plan | Price |\n|--|--:|\n| Pro | 5 |"
		}
		if _, err := e.st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: title,
			Slug: strings.ToLower(strings.ReplaceAll(title, " ", "-")), Lang: e.s.mainLang(), Content: body, AuthorID: u.ID,
			CommentStatus: "open", PublishedAt: &at, TermIDs: []int64{cat}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"search", "categories", "archives"} {
		if err := e.st.AddWidget(ctx, "footer", "", kind, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	e.s.contentChanged()
	tr := func(key string, args ...any) string { return e.s.i18n.T(e.s.mainLang(), key, args...) }

	// Search: the page's form and the widget's get their own ids, and the
	// count is in the window title and in a status message.
	body := e.get("/search?q=note", nil).Body.String()
	ids := regexp.MustCompile(`<input id="([^"]+)" type="search"`).FindAllStringSubmatch(body, -1)
	if len(ids) != 2 || ids[0][1] == ids[1][1] {
		t.Fatalf("search fields %v", ids)
	}
	for _, id := range ids {
		if !strings.Contains(body, `<label class="sr-only" for="`+id[1]+`">`) {
			t.Errorf("no label for %s", id[1])
		}
	}
	count := tr("%d results for “%s”", 3, "note")
	if !strings.Contains(body, "<title>"+count+" · ") || !strings.Contains(body, `<p class="sr-only" role="status">`+count+`</p>`) {
		t.Errorf("search count missing:\n%s", body)
	}
	if !strings.Contains(body, `href="/search?page=2&amp;q=note"`) || strings.Contains(body, "preview_theme") {
		t.Error("search pagination")
	}

	// The category archive marks its own link in the categories widget.
	body = e.get("/category/news", nil).Body.String()
	if !strings.Contains(body, `<a href="/category/news" aria-current="page">News</a>`) {
		t.Errorf("current category not marked:\n%s", body)
	}
	if strings.Contains(e.get("/", nil).Body.String(), `aria-current="page">News`) {
		t.Error("category marked current on the home page")
	}

	// The oldest post has a next link only, and no placeholder; its table
	// scrolls in a wrapper.
	body = e.get("/blog/first-note", nil).Body.String()
	nav := regexp.MustCompile(`(?s)<nav class="post-nav".*?</nav>`).FindString(body)
	if !strings.Contains(nav, `rel="next"`) || strings.Contains(nav, `rel="prev"`) || strings.Contains(nav, "<span></span>") {
		t.Errorf("post nav: %s", nav)
	}
	if !strings.Contains(body, `<div class="table-scroll"><table>`) {
		t.Error("table not wrapped")
	}

	// Theme preview: kept in pagination for an administrator, never for
	// visitors, and the visitors' cached pages stay clean.
	e.s.cfg.Dev = false // in development anyone may preview
	const other = "bento"
	if e.slug == other {
		t.Fatalf("active theme is %s", other)
	}
	for i := 0; i < 2; i++ {
		rec := e.get("/?preview_theme="+other, nil)
		if b := rec.Body.String(); strings.Contains(b, "preview_theme") || strings.Contains(b, "/themes/"+other+"/") {
			t.Errorf("visitor %d previews: X-Cache %s", i, rec.Header().Get("X-Cache"))
		}
	}
	body = e.get("/?preview_theme="+other, e.admin).Body.String()
	if !strings.Contains(body, "/themes/"+other+"/") || !strings.Contains(body, `?page=2&amp;preview_theme=`+other) ||
		!strings.Contains(body, `<meta name="sap-preview-theme" content="`+other+`">`) {
		t.Errorf("admin preview lost the theme in pagination:\n%s", body)
	}
	if body := e.get("/?preview_theme=nope", e.admin).Body.String(); strings.Contains(body, "preview_theme") || strings.Contains(body, "sap-preview-theme") {
		t.Error("unknown theme kept in links")
	}
	for i := 0; i < 2; i++ {
		if b := e.get("/", nil).Body.String(); strings.Contains(b, "preview_theme") || strings.Contains(b, "sap-preview-theme") {
			t.Errorf("visitor home %d has the preview", i)
		}
	}

	// The admin shows theme manifest strings translated.
	e.s.cfg.Dev = true // 2FA is mandatory for administrators otherwise
	th := e.s.themes.Active(e.slug)
	for path, key := range map[string]string{
		"/admin/appearance/themes":                        th.Description,
		"/admin/appearance/themes/" + e.slug + "/options": th.Options[0].Label,
		"/admin/appearance/widgets":                       th.WidgetAreas[0].Label,
		"/admin/appearance/menus":                         th.Menus[0].Label,
	} {
		want := tr(key)
		if want == key {
			t.Fatalf("%q has no translation", key)
		}
		if body := e.get(path, e.admin).Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: %q not translated", path, key)
		}
	}
}
