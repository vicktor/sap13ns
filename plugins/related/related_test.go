package main

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vicktor/sap13ns/pluginsdk"
)

func tag(slug string) pluginsdk.Term { return pluginsdk.Term{Taxonomy: "tag", Slug: slug, Name: slug} }
func cat(slug string) pluginsdk.Term {
	return pluginsdk.Term{Taxonomy: "category", Slug: slug, Name: slug}
}

func post(id int64, lang, published string, terms ...pluginsdk.Term) pluginsdk.Post {
	return pluginsdk.Post{ID: id, Type: "post", Title: fmt.Sprintf("Post %d", id), Lang: lang,
		URL: fmt.Sprintf("https://example.test/blog/p%d/", id), PublishedAt: published, Terms: terms}
}

// fakeHost answers queries like the host: published posts filtered by type,
// language, term, exclusion and search, newest first, at most Limit.
type fakeHost struct {
	posts      []pluginsdk.Post
	queries    []pluginsdk.PostsQuery
	ignoreLang bool // a host that does not filter by language
	err        error
}

func (f *fakeHost) query(q pluginsdk.PostsQuery) ([]pluginsdk.Post, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	has := func(p pluginsdk.Post, tax, slug string) bool {
		return slices.ContainsFunc(p.Terms, func(t pluginsdk.Term) bool { return t.Taxonomy == tax && t.Slug == slug })
	}
	var out []pluginsdk.Post
	for _, p := range f.posts {
		switch {
		case p.Type != cmp.Or(q.Type, "post"), !f.ignoreLang && q.Lang != "" && p.Lang != q.Lang,
			q.Tag != "" && !has(p, "tag", q.Tag), q.Category != "" && !has(p, "category", q.Category),
			p.ID == q.Exclude, q.Search != "" && !strings.Contains(strings.ToLower(p.Title), strings.ToLower(q.Search)):
			continue
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b pluginsdk.Post) int {
		return cmp.Or(strings.Compare(b.PublishedAt, a.PublishedAt), cmp.Compare(b.ID, a.ID))
	})
	return out[:min(len(out), cmp.Or(q.Limit, 5))], nil
}

func ids(posts []pluginsdk.Post) []int64 {
	out := []int64{}
	for _, p := range posts {
		out = append(out, p.ID)
	}
	return out
}

func TestRelatedOrder(t *testing.T) {
	current := post(1, "en", "2026-05-01T10:00:00Z", tag("go"), tag("wasm"), cat("dev"))
	page := post(20, "en", "2026-04-30T10:00:00Z", tag("go"))
	page.Type = "page"
	host := &fakeHost{posts: []pluginsdk.Post{
		current,
		post(2, "en", "2026-01-01T10:00:00Z", tag("go"), tag("wasm")),             // two tags, oldest
		post(3, "en", "2026-03-01T10:00:00Z", tag("go"), cat("dev")),              // one tag and the category
		post(4, "en", "2026-04-01T10:00:00Z", tag("wasm"), tag("rust")),           // one tag, newest
		post(5, "en", "2026-03-15T10:00:00Z", tag("go")),                          // one tag
		post(6, "en", "2026-03-15T10:00:00Z", tag("wasm")),                        // same date as 5, higher id
		post(7, "en", "2026-04-20T10:00:00Z", cat("dev")),                         // only the category
		post(8, "en", "2026-04-25T10:00:00Z", tag("rust"), cat("life")),           // nothing shared
		post(9, "es", "2026-04-28T10:00:00Z", tag("go"), tag("wasm"), cat("dev")), // other language
		page,
	}}
	got, err := Related(&current, "en", 10, maxQueries, host.query)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{2, 3, 4, 6, 5, 7}; !slices.Equal(ids(got), want) {
		t.Fatalf("order %v, want %v", ids(got), want)
	}
	if len(host.queries) != 3 {
		t.Fatalf("%d queries, want one per term: %+v", len(host.queries), host.queries)
	}
	for _, q := range host.queries {
		if q.Type != "post" || q.Lang != "en" || q.Exclude != 1 || q.Limit != 20 || (q.Tag == "") == (q.Category == "") {
			t.Errorf("query %+v", q)
		}
	}
	if q := host.queries; q[0].Tag != "go" || q[1].Tag != "wasm" || q[2].Category != "dev" {
		t.Errorf("tags are not queried first: %+v", q)
	}

	// The count keeps the best ones.
	if got, _ := Related(&current, "en", 2, maxQueries, host.query); !slices.Equal(ids(got), []int64{2, 3}) {
		t.Errorf("count 2: %v", ids(got))
	}
}

func TestRelatedLanguage(t *testing.T) {
	current := post(1, "es", "2026-05-01T10:00:00Z", tag("go"))
	posts := []pluginsdk.Post{current, post(2, "en", "2026-04-01T10:00:00Z", tag("go")), post(3, "es", "2026-03-01T10:00:00Z", tag("go"))}
	for _, ignore := range []bool{false, true} {
		host := &fakeHost{posts: posts, ignoreLang: ignore}
		got, err := Related(&current, "es", 4, maxQueries, host.query)
		if err != nil || !slices.Equal(ids(got), []int64{3}) {
			t.Errorf("host filters language=%v: %v, %v", !ignore, ids(got), err)
		}
	}
}

func TestRelatedExcludesCurrentPost(t *testing.T) {
	current := post(1, "en", "2026-05-01T10:00:00Z", tag("go"))
	// A host that ignores Exclude still never lists the post itself.
	host := &fakeHost{posts: []pluginsdk.Post{current, post(2, "en", "2026-04-01T10:00:00Z", tag("go"))}}
	got, _ := Related(&current, "en", 4, maxQueries, func(q pluginsdk.PostsQuery) ([]pluginsdk.Post, error) {
		q.Exclude = 0
		return host.query(q)
	})
	if !slices.Equal(ids(got), []int64{2}) {
		t.Fatalf("got %v", ids(got))
	}
}

func TestRelatedNothingShared(t *testing.T) {
	host := &fakeHost{posts: []pluginsdk.Post{post(2, "en", "2026-04-01T10:00:00Z", tag("go"))}}
	for name, p := range map[string]pluginsdk.Post{
		"no terms":      post(1, "en", "2026-05-01T10:00:00Z"),
		"unshared term": post(1, "en", "2026-05-01T10:00:00Z", tag("rust")),
		"empty slug":    post(1, "en", "2026-05-01T10:00:00Z", tag("")),
	} {
		host.queries = nil
		got, err := Related(&p, "en", 4, maxQueries, host.query)
		if err != nil || len(got) != 0 {
			t.Errorf("%s: %v, %v", name, ids(got), err)
		}
		if name != "unshared term" && len(host.queries) != 0 {
			t.Errorf("%s: %d queries", name, len(host.queries))
		}
	}
	if got, err := Related(nil, "en", 4, maxQueries, host.query); got != nil || err != nil {
		t.Errorf("nil post: %v, %v", got, err)
	}
}

func TestRelatedQueryError(t *testing.T) {
	current := post(1, "en", "2026-05-01T10:00:00Z", tag("go"), tag("wasm"))
	host := &fakeHost{err: errors.New("denied")}
	if got, err := Related(&current, "en", 4, maxQueries, host.query); err == nil || got != nil || len(host.queries) != 1 {
		t.Fatalf("got %v, %v after %d queries", got, err, len(host.queries))
	}
}

func TestPlanBudget(t *testing.T) {
	terms := func(tax string, n int) []pluginsdk.Term {
		var out []pluginsdk.Term
		for i := range n {
			out = append(out, pluginsdk.Term{Taxonomy: tax, Slug: fmt.Sprintf("%s%d", tax, i)})
		}
		return out
	}
	for _, tc := range []struct {
		name             string
		tags, cats       int
		budget           int
		wantTags, wantCs int
	}{
		{"few terms", 3, 2, 10, 3, 2},
		{"many tags keep one category", 12, 3, 10, 9, 1},
		{"many tags, no category", 12, 0, 10, 10, 0},
		{"few tags, many categories", 2, 15, 10, 2, 8},
		{"widget budget", 12, 3, 9, 8, 1},
		{"only categories", 0, 12, 10, 0, 10},
		{"no budget", 3, 3, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &pluginsdk.Post{ID: 1, Terms: append(terms("category", tc.cats), terms("tag", tc.tags)...)}
			got := plan(p, tc.budget)
			var nt, nc int
			for i, r := range got {
				if r.taxonomy == "tag" {
					nt++
					if nc > 0 {
						t.Fatalf("a tag after a category at %d: %v", i, got)
					}
				} else {
					nc++
				}
			}
			if nt != tc.wantTags || nc != tc.wantCs || len(got) > tc.budget {
				t.Fatalf("%d tags and %d categories, want %d and %d: %v", nt, nc, tc.wantTags, tc.wantCs, got)
			}
			host := &fakeHost{}
			if _, err := Related(p, "en", 4, tc.budget, host.query); err != nil || len(host.queries) != len(got) {
				t.Fatalf("%d queries for %d planned terms, %v", len(host.queries), len(got), err)
			}
		})
	}
	// Terms listed twice are queried once.
	p := &pluginsdk.Post{Terms: []pluginsdk.Term{tag("go"), tag("go"), cat("dev"), cat("dev"), {Taxonomy: "format", Slug: "x"}}}
	if got := plan(p, 10); len(got) != 2 {
		t.Errorf("duplicates: %v", got)
	}
}

func TestCurrent(t *testing.T) {
	target := post(5, "en", "2026-04-01T10:00:00Z", tag("go"))
	target.Title = "Go: WASM & you"
	other := post(6, "en", "2026-04-02T10:00:00Z")
	other.Title = "Go WASM you, again"
	var seen []pluginsdk.PostsQuery
	query := func(q pluginsdk.PostsQuery) ([]pluginsdk.Post, error) {
		seen = append(seen, q)
		return []pluginsdk.Post{other, target}, nil
	}
	single := func(url string) *pluginsdk.Page {
		return &pluginsdk.Page{Kind: "single", URL: url, Lang: "en", Title: target.Title}
	}
	for _, tc := range []struct {
		name    string
		page    *pluginsdk.Page
		want    int64
		queries int
	}{
		{"same URL", single(target.URL), 5, 1},
		{"URL without the trailing slash", single(strings.TrimSuffix(target.URL, "/")), 5, 1},
		{"not found", single("https://example.test/blog/elsewhere/"), 0, 1},
		{"no URL", single(""), 0, 0},
		{"home page", &pluginsdk.Page{Kind: "home", URL: target.URL}, 0, 0},
		{"static page", &pluginsdk.Page{Kind: "page", URL: target.URL}, 0, 0},
		{"no page", nil, 0, 0},
		{"post sent by the host", &pluginsdk.Page{Kind: "single", Post: &pluginsdk.Post{ID: 9, Type: "post", Terms: []pluginsdk.Term{}}}, 9, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			p, n, err := Current(tc.page, "en", query)
			if err != nil || n != tc.queries || len(seen) != n || (p == nil) != (tc.want == 0) || (p != nil && p.ID != tc.want) {
				t.Fatalf("got %+v, %d queries, %v", p, n, err)
			}
			if n == 1 && !reflect.DeepEqual(seen[0], pluginsdk.PostsQuery{Type: "post", Lang: "en", Search: "Go WASM you", Limit: 20}) {
				t.Fatalf("query %+v", seen[0])
			}
		})
	}
	if _, n, err := Current(single(target.URL), "en", (&fakeHost{err: errors.New("x")}).query); err == nil || n != 1 {
		t.Fatalf("error not returned: %d, %v", n, err)
	}
}

func TestTitleWords(t *testing.T) {
	for in, want := range map[string]string{
		"Hello, world!":                                    "Hello world",
		`"Quoted" -minus or (parens)`:                      "Quoted minus or parens",
		"Año 2026: ¿qué viene?":                            "Año 2026 qué viene",
		"one two three four five six seven eight nine ten": "one two three four five six seven eight",
		"🙂 — …":                                            "",
	} {
		if got := titleWords(in); got != want {
			t.Errorf("titleWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestItems(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	p := post(2, "es", "2026-01-01T23:30:00Z")
	p.Title = `<b>"Tips" & tricks</b>`
	p.Image = "https://example.test/media/a.jpg?x=1&y=2"
	undated := post(3, "es", "")
	unsafe := post(4, "es", "2026-01-01T10:00:00Z")
	unsafe.URL = "javascript:alert(1)"
	badImage := post(5, "es", "2026-01-01T10:00:00Z")
	badImage.Image = `https://example.test/a.jpg" onerror="x`

	o := Options{Spanish: true, Location: madrid, Images: true}
	got := Items([]pluginsdk.Post{p, undated, unsafe, badImage}, o)
	want := `<li><a href="https://example.test/blog/p2/"><img src="https://example.test/media/a.jpg?x=1&amp;y=2" alt="" width="64" height="64" loading="lazy">` +
		`<span>&lt;b&gt;&#34;Tips&#34; &amp; tricks&lt;/b&gt;<time datetime="2026-01-01T23:30:00Z">2 ene 2026</time></span></a></li>` +
		`<li><a href="https://example.test/blog/p3/"><span>Post 3</span></a></li>` +
		`<li><a href="https://example.test/blog/p5/"><span>Post 5<time datetime="2026-01-01T10:00:00Z">1 ene 2026</time></span></a></li>`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	o.Images = false
	if got := Items([]pluginsdk.Post{p}, o); strings.Contains(got, "<img") {
		t.Errorf("image shown with show_images off: %s", got)
	}
	// English, without a time zone: UTC.
	if got := Items([]pluginsdk.Post{p}, Options{}); !strings.Contains(got, `>Jan 1, 2026</time>`) {
		t.Errorf("english date: %s", got)
	}
}

func TestShortDate(t *testing.T) {
	en := []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	es := []string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}
	for m := time.January; m <= time.December; m++ {
		d := time.Date(2026, m, 2, 12, 0, 0, 0, time.UTC)
		if got, want := shortDate(d, false), en[m-1]+" 2, 2026"; got != want {
			t.Errorf("en %v: %q, want %q", m, got, want)
		}
		if got, want := shortDate(d, true), "2 "+es[m-1]+" 2026"; got != want {
			t.Errorf("es %v: %q, want %q", m, got, want)
		}
	}
}

func TestBlock(t *testing.T) {
	posts := []pluginsdk.Post{post(2, "en", "2026-01-02T10:00:00Z")}
	got := Block(posts, Options{Title: `Read "next" <now>`, Location: time.UTC})
	want := `<aside class="card related-posts" aria-label="Read next now"><header><h2 class="related-posts-title">Read &#34;next&#34; &lt;now&gt;</h2></header>` +
		`<section><ul class="widget-posts related-posts-list"><li><a href="https://example.test/blog/p2/"><span>Post 2<time datetime="2026-01-02T10:00:00Z">Jan 2, 2026</time></span></a></li></ul></section></aside>`
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if Block(nil, Options{Title: "x"}) != "" || Widget(nil, Options{}) != "" {
		t.Error("empty lists render markup")
	}
	if got := Widget(posts, Options{}); !strings.HasPrefix(got, `<ul class="widget-posts related-widget"><li>`) || !strings.HasSuffix(got, `</li></ul>`) {
		t.Errorf("widget: %s", got)
	}
}

func TestCount(t *testing.T) {
	for in, want := range map[string]int{"": 4, "abc": 4, "NaN": 4, "6": 6, " 3 ": 3, "0": 1, "-2": 1, "15": 10, "3.7": 3, "Inf": 10, "-Inf": 1} {
		if got := count(in, 4); got != want {
			t.Errorf("count(%q) = %d, want %d", in, got, want)
		}
	}
}
