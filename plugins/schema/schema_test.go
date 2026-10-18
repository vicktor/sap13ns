package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vicktor/sap13ns/pluginsdk"
)

const site = "https://example.test"

var testSite = pluginsdk.Site{URL: site, Title: "Example", Language: "en", Timezone: "UTC"}

func cfgWith(settings map[string]string) Config {
	c, _ := ConfigFrom(settings, testSite)
	return c
}

func samplePost() *pluginsdk.Post {
	return &pluginsdk.Post{ID: 7, Type: "post", Title: "Hello world", Slug: "hello-world", Lang: "en",
		URL: site + "/blog/hello-world", Excerpt: "A first post.", Author: "Ada Lovelace",
		Categories: []string{"News"}, Tags: []string{"go", "cms", "Go"},
		Terms: []pluginsdk.Term{{Taxonomy: "category", Slug: "news", Name: "News", URL: site + "/category/news"},
			{Taxonomy: "tag", Slug: "go", Name: "go", URL: site + "/tag/go"}},
		Image: site + "/uploads/hello.jpg", PublishedAt: "2026-05-01T10:00:00Z", UpdatedAt: "2026-05-03T08:30:00Z", WordCount: 420}
}

// graphOf marshals ld and returns its nodes by @type (several nodes of a
// type are joined in order) after checking the JSON and the references.
func graphOf(t *testing.T, ld *Node) map[string][]map[string]any {
	t.Helper()
	if ld == nil {
		t.Fatal("no JSON-LD")
	}
	b, err := json.Marshal(ld)
	if err != nil || !json.Valid(b) {
		t.Fatalf("invalid JSON: %v\n%s", err, b)
	}
	if len(b) > 16<<10 {
		t.Fatalf("object of %d bytes is over the host limit", len(b))
	}
	var doc struct {
		Context string           `json:"@context"`
		Graph   []map[string]any `json:"@graph"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Context != "https://schema.org" {
		t.Fatalf("@context = %q", doc.Context)
	}
	ids := map[string]bool{}
	out := map[string][]map[string]any{}
	for _, n := range doc.Graph {
		typ, _ := n["@type"].(string)
		id, _ := n["@id"].(string)
		if typ == "" || id == "" {
			t.Fatalf("graph node without @type or @id: %v", n)
		}
		if ids[id] {
			t.Fatalf("duplicate @id %s", id)
		}
		ids[id] = true
		out[typ] = append(out[typ], n)
	}
	var check func(v any)
	check = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if id, ok := x["@id"].(string); ok && len(x) == 1 && !ids[id] {
				t.Errorf("reference to %s does not resolve", id)
			}
			for _, c := range x {
				check(c)
			}
		case []any:
			for _, c := range x {
				check(c)
			}
		}
	}
	for _, n := range doc.Graph {
		check(n)
	}
	return out
}

func refID(v any) string {
	m, _ := v.(map[string]any)
	s, _ := m["@id"].(string)
	return s
}

func crumbs(t *testing.T, g map[string][]map[string]any) []string {
	t.Helper()
	if len(g["BreadcrumbList"]) != 1 {
		return nil
	}
	var out []string
	for _, it := range g["BreadcrumbList"][0]["itemListElement"].([]any) {
		m := it.(map[string]any)
		out = append(out, m["name"].(string)+" "+m["item"].(string))
	}
	return out
}

func TestBuildKinds(t *testing.T) {
	post := samplePost()
	page := &pluginsdk.Post{ID: 3, Type: "page", Title: "About", Lang: "en", URL: site + "/about", Author: "Ada",
		PublishedAt: "2026-01-02T00:00:00Z"}
	for _, tc := range []struct {
		name       string
		page       pluginsdk.Page
		types      []string // node types that must be present
		absent     []string
		crumbs     []string
		searchable bool
	}{
		{"home", pluginsdk.Page{Kind: "home", URL: site + "/", Lang: "en", Title: "", Description: "Tagline"},
			[]string{"WebSite", "Organization", "CollectionPage"}, []string{"BlogPosting", "BreadcrumbList"}, nil, true},
		{"home page 2 has the same canonical URL", pluginsdk.Page{Kind: "home", URL: site + "/", Lang: "en", Title: "Page 2"},
			[]string{"WebSite", "CollectionPage"}, []string{"BreadcrumbList"}, nil, true},
		{"blog listing", pluginsdk.Page{Kind: "home", URL: site + "/blog", Lang: "en", Title: "Blog"},
			[]string{"WebSite", "CollectionPage", "BreadcrumbList"}, []string{"BlogPosting"},
			[]string{"Home " + site + "/", "Blog " + site + "/blog"}, false},
		{"post", pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Description: "Meta description", Post: post},
			[]string{"WebSite", "Organization", "WebPage", "BlogPosting", "ImageObject", "BreadcrumbList"}, nil,
			[]string{"Home " + site + "/", "News " + site + "/category/news", "Hello world " + post.URL}, false},
		{"page", pluginsdk.Page{Kind: "page", URL: page.URL, Lang: "en", Title: "About", Post: page},
			[]string{"WebSite", "WebPage", "BreadcrumbList"}, []string{"BlogPosting", "Article"},
			[]string{"Home " + site + "/", "About " + page.URL}, false},
		{"static front page", pluginsdk.Page{Kind: "page", URL: site + "/", Lang: "en", Title: "Welcome", Post: &pluginsdk.Post{ID: 9, Type: "page", Title: "Welcome", URL: site + "/welcome"}},
			[]string{"WebSite", "WebPage"}, []string{"BreadcrumbList", "CollectionPage"}, nil, true},
		{"archive", pluginsdk.Page{Kind: "archive", URL: site + "/category/news", Lang: "en", Title: "News"},
			[]string{"CollectionPage", "BreadcrumbList"}, []string{"BlogPosting"},
			[]string{"Home " + site + "/", "News " + site + "/category/news"}, false},
		{"search", pluginsdk.Page{Kind: "search", URL: site + "/search", Lang: "en", Title: "Results"},
			[]string{"WebSite", "SearchResultsPage"}, []string{"BreadcrumbList"}, nil, false},
		{"unknown kind", pluginsdk.Page{Kind: "ask", URL: site + "/ask", Lang: "en", Title: "Ask"},
			[]string{"WebPage", "BreadcrumbList"}, nil, []string{"Home " + site + "/", "Ask " + site + "/ask"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := graphOf(t, Build(tc.page, cfgWith(nil)))
			for _, typ := range tc.types {
				if len(g[typ]) == 0 {
					t.Errorf("no %s node", typ)
				}
			}
			for _, typ := range tc.absent {
				if len(g[typ]) > 0 {
					t.Errorf("unexpected %s node", typ)
				}
			}
			if got := crumbs(t, g); strings.Join(got, "|") != strings.Join(tc.crumbs, "|") {
				t.Errorf("breadcrumbs = %q, want %q", got, tc.crumbs)
			}
			_, hasSearch := g["WebSite"][0]["potentialAction"]
			if hasSearch != tc.searchable {
				t.Errorf("SearchAction present = %v, want %v", hasSearch, tc.searchable)
			}
		})
	}
}

func TestBuild404(t *testing.T) {
	if ld := Build(pluginsdk.Page{Kind: "404", URL: site + "/nope", Lang: "en"}, cfgWith(nil)); ld != nil {
		t.Fatal("404 pages get no structured data")
	}
	if ld := Build(pluginsdk.Page{Kind: "single", Lang: "en"}, cfgWith(nil)); ld != nil {
		t.Fatal("a page without URL gets no structured data")
	}
}

func TestBuildPost(t *testing.T) {
	post := samplePost()
	g := graphOf(t, Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Description: "Meta description", Post: post}, cfgWith(nil)))
	a := g["BlogPosting"][0]
	want := map[string]any{
		"@id": post.URL + "#article", "headline": "Hello world", "description": "Meta description", "url": post.URL,
		"inLanguage": "en", "datePublished": "2026-05-01T10:00:00Z", "dateModified": "2026-05-03T08:30:00Z",
		"articleSection": "News", "wordCount": float64(420),
	}
	for k, v := range want {
		if a[k] != v {
			t.Errorf("%s = %v, want %v", k, a[k], v)
		}
	}
	if refID(a["publisher"]) != site+"/#publisher" || refID(a["mainEntityOfPage"]) != post.URL+"#webpage" || refID(a["image"]) != post.URL+"#primaryimage" {
		t.Errorf("references: %v", a)
	}
	if au := a["author"].(map[string]any); au["@type"] != "Person" || au["name"] != "Ada Lovelace" {
		t.Errorf("author = %v", au)
	}
	if kw := a["keywords"].([]any); len(kw) != 2 || kw[0] != "go" || kw[1] != "cms" {
		t.Errorf("keywords = %v (duplicates must go)", kw)
	}
	wp := g["WebPage"][0]
	if wp["datePublished"] != post.PublishedAt || wp["dateModified"] != post.UpdatedAt {
		t.Errorf("webpage dates: %v", wp)
	}
	if refID(wp["breadcrumb"]) != post.URL+"#breadcrumb" || refID(wp["isPartOf"]) != site+"/#website" || refID(wp["primaryImageOfPage"]) != post.URL+"#primaryimage" {
		t.Errorf("webpage references: %v", wp)
	}
	if img := g["ImageObject"][0]; img["url"] != post.Image {
		t.Errorf("image = %v", img)
	}
}

func TestBuildWithoutDates(t *testing.T) {
	// A post without dates, image, terms or word count: nothing is invented.
	post := &pluginsdk.Post{ID: 7, Type: "post", Title: "Hello", Lang: "en", URL: site + "/blog/hello", Author: "Ada"}
	g := graphOf(t, Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: "Hello", Post: post}, cfgWith(nil)))
	a := g["BlogPosting"][0]
	for _, k := range []string{"datePublished", "dateModified", "image", "articleSection", "keywords", "wordCount"} {
		if _, ok := a[k]; ok {
			t.Errorf("%s present without data: %v", k, a[k])
		}
	}
	for _, k := range []string{"datePublished", "dateModified", "primaryImageOfPage"} {
		if _, ok := g["WebPage"][0][k]; ok {
			t.Errorf("webpage %s present without data", k)
		}
	}
	if len(g["ImageObject"]) != 0 {
		t.Error("image node without an image")
	}
}

func TestBuildDates(t *testing.T) {
	// A page has no article node: its dates go on the WebPage.
	page := &pluginsdk.Post{ID: 3, Type: "page", Title: "About", Lang: "en", URL: site + "/about",
		PublishedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-03-04T12:00:00+01:00"}
	g := graphOf(t, Build(pluginsdk.Page{Kind: "page", URL: page.URL, Lang: "en", Title: "About", Post: page}, cfgWith(nil)))
	if wp := g["WebPage"][0]; wp["datePublished"] != page.PublishedAt || wp["dateModified"] != page.UpdatedAt {
		t.Errorf("page dates: %v", wp)
	}
	// Only the publication date known: no dateModified.
	post := samplePost()
	post.UpdatedAt = ""
	g = graphOf(t, Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Post: post}, cfgWith(nil)))
	for _, n := range []map[string]any{g["BlogPosting"][0], g["WebPage"][0]} {
		if _, ok := n["dateModified"]; ok || n["datePublished"] != post.PublishedAt {
			t.Errorf("%s dates: %v", n["@type"], n)
		}
	}
	// Listings describe no single post: no dates.
	g = graphOf(t, Build(pluginsdk.Page{Kind: "archive", URL: site + "/category/news", Lang: "en", Title: "News"}, cfgWith(nil)))
	if _, ok := g["CollectionPage"][0]["dateModified"]; ok {
		t.Error("an archive has a dateModified")
	}
}

func TestBuildSpanish(t *testing.T) {
	post := samplePost()
	post.Lang, post.URL, post.Title = "es", site+"/es/blog/hola", "Hola mundo"
	post.Terms = []pluginsdk.Term{{Taxonomy: "category", Slug: "noticias", Name: "Noticias", URL: site + "/es/category/noticias"}}
	g := graphOf(t, Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "es", Title: post.Title, Post: post}, cfgWith(nil)))
	if got := strings.Join(crumbs(t, g), "|"); got != "Inicio "+site+"/es/|Noticias "+site+"/es/category/noticias|Hola mundo "+post.URL {
		t.Errorf("breadcrumbs = %s", got)
	}
	ws := g["WebSite"][0]
	if ws["@id"] != site+"/es/#website" || ws["url"] != site+"/es/" || ws["inLanguage"] != "es" {
		t.Errorf("website = %v", ws)
	}
	if g["BlogPosting"][0]["inLanguage"] != "es" || g["WebPage"][0]["inLanguage"] != "es" {
		t.Error("inLanguage is not es")
	}
	// Spanish home: the search box searches /es/search.
	g = graphOf(t, Build(pluginsdk.Page{Kind: "home", URL: site + "/es/", Lang: "es"}, cfgWith(nil)))
	target := g["WebSite"][0]["potentialAction"].(map[string]any)["target"].(map[string]any)["urlTemplate"]
	if target != site+"/es/search?q={search_term_string}" {
		t.Errorf("search target = %v", target)
	}
}

func TestHomeLabel(t *testing.T) {
	for _, tc := range []struct {
		settings map[string]string
		lang     string
		want     string
	}{
		{nil, "en", "Home"},
		{nil, "es", "Inicio"},
		{nil, "es-MX", "Inicio"},
		{nil, "fr", "Home"},
		{map[string]string{"home_label": "Start"}, "es", "Start"},
		{map[string]string{"home_label": "Start", "home_label_es": "Portada"}, "es", "Portada"},
		{map[string]string{"home_label_en": "Front"}, "en-GB", "Front"},
		{map[string]string{"home_label_en": "Front"}, "es", "Inicio"},
	} {
		if got := cfgWith(tc.settings).homeLabel(tc.lang); got != tc.want {
			t.Errorf("homeLabel(%v, %s) = %q, want %q", tc.settings, tc.lang, got, tc.want)
		}
	}
}

func TestConfigFrom(t *testing.T) {
	c, warnings := ConfigFrom(map[string]string{
		"article_type": "NewsArticle", "publisher_type": "Person", "publisher_name": " Ada ",
		"publisher_url": "ftp://bad", "publisher_logo": "https://example.test/ada.png",
		"same_as":     "https://social.example/@ada\nhttp://insecure.example/ada, https://social.example/@ada https://code.example/ada",
		"breadcrumbs": "false", "search_action": "false",
	}, testSite)
	if c.ArticleType != "NewsArticle" || c.PublisherType != "Person" || c.PublisherName != "Ada" || c.PublisherURL != site+"/" ||
		c.PublisherLogo != "https://example.test/ada.png" || c.Breadcrumbs || c.SearchAction {
		t.Errorf("config = %+v", c)
	}
	if strings.Join(c.SameAs, " ") != "https://social.example/@ada https://code.example/ada" {
		t.Errorf("sameAs = %q", c.SameAs)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %q", warnings)
	}
	d, _ := ConfigFrom(map[string]string{"article_type": "Bogus", "publisher_type": "Robot"}, testSite)
	if d.ArticleType != "BlogPosting" || d.PublisherType != "Organization" || d.PublisherName != "Example" || !d.Breadcrumbs || !d.SearchAction {
		t.Errorf("defaults = %+v", d)
	}
	var many []string
	for i := 0; i < 15; i++ {
		many = append(many, "https://p"+string(rune('a'+i))+".example/")
	}
	e, _ := ConfigFrom(map[string]string{"same_as": strings.Join(many, "\n")}, testSite)
	if len(e.SameAs) != 10 {
		t.Errorf("sameAs keeps %d profiles, want 10", len(e.SameAs))
	}
}

func TestPublisher(t *testing.T) {
	logo := "https://example.test/logo.png"
	g := graphOf(t, Build(pluginsdk.Page{Kind: "home", URL: site + "/", Lang: "en"},
		cfgWith(map[string]string{"publisher_logo": logo, "same_as": "https://social.example/@site"})))
	org := g["Organization"][0]
	if org["name"] != "Example" || org["url"] != site+"/" || org["logo"].(map[string]any)["url"] != logo || org["sameAs"].([]any)[0] != "https://social.example/@site" {
		t.Errorf("organization = %v", org)
	}
	g = graphOf(t, Build(pluginsdk.Page{Kind: "home", URL: site + "/", Lang: "en"},
		cfgWith(map[string]string{"publisher_type": "Person", "publisher_name": "Ada", "publisher_logo": logo, "search_action": "false"})))
	p := g["Person"][0]
	if p["name"] != "Ada" || p["image"].(map[string]any)["url"] != logo {
		t.Errorf("person = %v", p)
	}
	if _, ok := p["logo"]; ok {
		t.Error("a Person has no logo")
	}
	if _, ok := g["WebSite"][0]["potentialAction"]; ok {
		t.Error("search_action=false still adds a SearchAction")
	}
}

func TestNoBreadcrumbs(t *testing.T) {
	post := samplePost()
	g := graphOf(t, Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Post: post}, cfgWith(map[string]string{"breadcrumbs": "false"})))
	if len(g["BreadcrumbList"]) != 0 {
		t.Error("breadcrumbs=false still adds a BreadcrumbList")
	}
	if _, ok := g["WebPage"][0]["breadcrumb"]; ok {
		t.Error("dangling breadcrumb reference")
	}
}

func TestEscapingAndLimits(t *testing.T) {
	post := samplePost()
	post.Title = `He said "hi" </script><script>alert(1)</script> ` + strings.Repeat("long ", 60)
	post.Tags = nil
	for i := 0; i < 30; i++ {
		post.Tags = append(post.Tags, strings.Repeat("t", 3)+string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	page := pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Description: strings.Repeat("Descripción larga. ", 100), Post: post}
	ld := Build(page, cfgWith(nil))
	b, _ := json.Marshal(ld)
	if strings.Contains(string(b), "</script>") {
		t.Fatalf("</script> is not escaped: %s", b)
	}
	g := graphOf(t, ld)
	a := g["BlogPosting"][0]
	if n := len([]rune(a["headline"].(string))); n > maxHeadline {
		t.Errorf("headline has %d runes", n)
	}
	if n := len([]rune(a["description"].(string))); n > maxDescRunes {
		t.Errorf("description has %d runes", n)
	}
	if n := len(a["keywords"].([]any)); n != maxKeywords {
		t.Errorf("%d keywords, want %d", n, maxKeywords)
	}
	if !strings.HasPrefix(a["headline"].(string), `He said "hi" </script>`) {
		t.Errorf("headline = %q", a["headline"])
	}
}

func TestFitDropsOptionalData(t *testing.T) {
	post := samplePost()
	long := "https://example.test/" + strings.Repeat("a", 3000)
	settings := map[string]string{"same_as": long + "1\n" + long + "2\n" + long + "3", "publisher_logo": long + ".png"}
	post.Tags = nil
	for i := 0; i < 10; i++ {
		post.Tags = append(post.Tags, strings.Repeat("k", 99)+string(rune('a'+i)))
	}
	ld := Build(pluginsdk.Page{Kind: "single", URL: post.URL, Lang: "en", Title: post.Title, Post: post}, cfgWith(settings))
	g := graphOf(t, ld) // fails if over 16 KiB
	if len(g["BlogPosting"]) != 1 {
		t.Fatal("the article was dropped instead of optional fields")
	}
	if _, ok := g["Organization"][0]["sameAs"]; ok {
		t.Error("sameAs kept although the object was too large")
	}
	// Absurd URLs: nothing fits, nothing is returned.
	huge := pluginsdk.Page{Kind: "single", URL: site + "/" + strings.Repeat("x", 17000), Lang: "en", Title: "x"}
	if Build(huge, cfgWith(nil)) != nil {
		t.Error("an object over the limit was returned")
	}
}

func TestNodeOrderAndEmptyValues(t *testing.T) {
	n := node("Thing", "#a").set("name", "").set("x", nil).set("b", "2").set("a", "1")
	n.set("b", "3")
	n.del("missing")
	b, _ := json.Marshal(n)
	if string(b) != `{"@type":"Thing","@id":"#a","b":"3","a":"1"}` {
		t.Errorf("got %s", b)
	}
}

func TestTruncateRunes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"  padded  ", 10, "padded"},
		{"ñandú ñandú", 6, "ñandú…"},
		{"日本語のテキスト", 4, "日本語…"},
	} {
		if got := truncateRunes(tc.in, tc.n); got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}
