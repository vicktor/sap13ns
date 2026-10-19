package themeimport

import (
	"strings"
	"testing"
)

// Each block converts to its sap13ns counterpart. The markup is the
// whole index template of a theme; want are pieces of its home content,
// none of which may be missing, and not pieces none may have.
func TestBlockMappings(t *testing.T) {
	cases := []struct {
		name, markup string
		want, not    []string
	}{
		{"paragraph", `<!-- wp:paragraph {"align":"center"} --><p class="has-text-align-center">Hello <strong>world</strong></p><!-- /wp:paragraph -->`,
			[]string{`<p class="has-text-align-center">Hello <strong>world</strong></p>`}, nil},
		{"heading", `<!-- wp:heading {"level":3} --><h3 class="wp-block-heading">Title</h3><!-- /wp:heading -->`,
			[]string{`<h3 class="wp-block-heading">Title</h3>`}, nil},
		{"list", `<!-- wp:list --><ul class="wp-block-list"><!-- wp:list-item --><li>One</li><!-- /wp:list-item --></ul><!-- /wp:list -->`,
			[]string{`<ul class="wp-block-list">`, `<li>One</li>`}, nil},
		{"quote", `<!-- wp:quote --><blockquote class="wp-block-quote"><!-- wp:paragraph --><p>Q</p><!-- /wp:paragraph --><cite>C</cite></blockquote><!-- /wp:quote -->`,
			[]string{`<blockquote class="wp-block-quote is-layout-flow`, `<cite>C</cite>`}, nil},
		{"separator", `<!-- wp:separator --><hr class="wp-block-separator has-alpha-channel-opacity"/><!-- /wp:separator -->`,
			[]string{`<hr class="wp-block-separator has-alpha-channel-opacity`}, nil},
		{"spacer", `<!-- wp:spacer {"height":"40px"} --><div style="height:40px" aria-hidden="true" class="wp-block-spacer"></div><!-- /wp:spacer -->`,
			[]string{`wp-block-spacer`, `class="wpi-`, `aria-hidden="true"`}, []string{`style=`}},
		{"group constrained", `<!-- wp:group {"layout":{"type":"constrained","contentSize":"600px"}} --><div class="wp-block-group"></div><!-- /wp:group -->`,
			[]string{`is-layout-constrained`, `wp-block-group-is-layout-constrained`, `wpl-`}, nil},
		{"group flex", `<!-- wp:group {"layout":{"type":"flex","justifyContent":"space-between","flexWrap":"nowrap"}} --><div class="wp-block-group"></div><!-- /wp:group -->`,
			[]string{`is-layout-flex`, `is-content-justification-space-between`, `is-nowrap`}, nil},
		{"group grid", `<!-- wp:group {"layout":{"type":"grid","columnCount":3}} --><div class="wp-block-group"></div><!-- /wp:group -->`,
			[]string{`is-layout-grid`, `sap-wp-grid-columns`}, nil},
		{"columns", `<!-- wp:columns --><div class="wp-block-columns"><!-- wp:column {"width":"33%"} --><div class="wp-block-column" style="flex-basis:33%"></div><!-- /wp:column --></div><!-- /wp:columns -->`,
			[]string{`wp-block-columns is-layout-flex`, `sap-wp-col-basis`, `wp-block-column wpi-`}, []string{`style=`}},
		{"buttons", `<!-- wp:buttons --><div class="wp-block-buttons"><!-- wp:button --><div class="wp-block-button"><a class="wp-block-button__link wp-element-button" href="/about">Go</a></div><!-- /wp:button --></div><!-- /wp:buttons -->`,
			[]string{`wp-block-buttons is-layout-flex`, `<a class="wp-block-button__link wp-element-button" href="/about">Go</a>`}, nil},
		{"cover", `<!-- wp:cover {"dimRatio":50,"layout":{"type":"constrained"}} --><div class="wp-block-cover"><span aria-hidden="true" class="wp-block-cover__background has-background-dim"></span><div class="wp-block-cover__inner-container"></div></div><!-- /wp:cover -->`,
			[]string{`wp-block-cover__background has-background-dim`, `wp-block-cover__inner-container is-layout-constrained`}, nil},
		{"image of another site", `<!-- wp:image --><figure class="wp-block-image"><img src="https://example.com/a.jpg" alt=""/></figure><!-- /wp:image -->`,
			nil, []string{`example.com`, `<img`}},
		{"details", `<!-- wp:details --><details class="wp-block-details"><summary>S</summary><!-- wp:paragraph --><p>D</p><!-- /wp:paragraph --></details><!-- /wp:details -->`,
			[]string{`<details class="wp-block-details`, `<summary>S</summary>`}, nil},
		{"table", `<!-- wp:table --><figure class="wp-block-table"><table><tbody><tr><td>1</td></tr></tbody></table></figure><!-- /wp:table -->`,
			[]string{`<table><tbody><tr><td>1</td></tr></tbody></table>`}, nil},
		{"site title", `<!-- wp:site-title {"level":1} /-->`,
			[]string{`<h1 class="wp-block-site-title"><a href="{{link "/"}}" rel="home">{{$.Site.Title}}</a></h1>`}, nil},
		{"site tagline", `<!-- wp:site-tagline /-->`, []string{`{{with $.Site.Tagline}}<p class="wp-block-site-tagline">{{.}}</p>{{end}}`}, nil},
		{"site logo", `<!-- wp:site-logo /-->`, []string{`{{if $.Site.Custom.Logo}}`, `{{template "shared/logo" $}}`}, nil},
		{"navigation", `<!-- wp:navigation {"overlayMenu":"mobile"} /-->`,
			[]string{`<nav class="wp-block-navigation`, `{{template "shared/menu" ($.Site.Menu "content")}}`, `{{template "shared/nav-toggle" "wpnav-content"}}`}, nil},
		{"page list", `<!-- wp:page-list /-->`, []string{`{{template "shared/menu" $.Site.Pages}}`}, nil},
		{"search", `<!-- wp:search {"label":"Search"} /-->`, []string{`{{if not $.Site.Custom.HideSearch}}`, `{{template "shared/search-form" $}}`}, nil},
		{"loginout", `<!-- wp:loginout /-->`, []string{`{{with $.Site.LoginURL}}`, `<a href="{{.}}" rel="nofollow">{{T "Log in"}}</a>`}, []string{`"/login"`}},
		{"categories", `<!-- wp:categories /-->`, []string{`{{with $.Site.Categories}}`, `{{range .}}<li class="cat-item">`}, nil},
		{"tag cloud", `<!-- wp:tag-cloud /-->`, []string{`{{with $.Site.Tags}}`}, nil},
		{"social links", `<!-- wp:social-links --><ul class="wp-block-social-links"><!-- wp:social-link {"url":"https://github.com/x","service":"github"} /--><!-- wp:social-link {"url":"javascript:alert(1)","service":"x"} /--></ul><!-- /wp:social-links -->`,
			[]string{`href="https://github.com/x"`, `GitHub`}, []string{`javascript:`}},
		{"query", `<!-- wp:query {"query":{"inherit":true}} --><div class="wp-block-query"><!-- wp:post-template --><!-- wp:post-title {"isLink":true} /--><!-- wp:post-date /--><!-- wp:post-excerpt /--><!-- wp:post-featured-image /--><!-- wp:post-author-name /--><!-- wp:post-terms {"term":"category"} /--><!-- wp:read-more /--><!-- /wp:post-template --><!-- wp:query-no-results --><!-- wp:paragraph --><p>None</p><!-- /wp:paragraph --><!-- /wp:query-no-results --><!-- wp:query-pagination --><!-- wp:query-pagination-previous /--><!-- wp:query-pagination-numbers /--><!-- wp:query-pagination-next /--><!-- /wp:query-pagination --></div><!-- /wp:query -->`,
			[]string{`{{range $.Posts}}<li class="wp-block-post post">`, `<h2 class="wp-block-post-title"><a href="{{.URL}}">{{.Title}}</a></h2>`,
				`{{if .HasDate}}`, `{{.Summary}}`, `{{if .Image}}`, `{{.Author.Name}}`, `{{with .Categories}}`, `wp-block-read-more`,
				`{{if not $.Posts}}`, `{{with $.Pagination}}`, `{{with .PrevURL}}`, `{{range .Links}}`, `{{with .NextURL}}`}, nil},
		{"query with offset", `<!-- wp:query {"query":{"perPage":2,"offset":1,"inherit":false}} --><div class="wp-block-query"><!-- wp:post-template --><!-- wp:post-title /--><!-- /wp:post-template --></div><!-- /wp:query -->`,
			[]string{`{{range $i, $p := $.Posts}}`}, nil},
		{"query title", `<!-- wp:query-title {"type":"archive"} /-->`, []string{`{{with $.Heading}}<h1 class="wp-block-query-title">{{.}}</h1>{{end}}`}, nil},
		{"latest posts", `<!-- wp:latest-posts /-->`, []string{`{{range $i, $p := $.Posts}}{{if lt $i 5}}`}, nil},
		{"pattern missing", `<!-- wp:pattern {"slug":"x/none"} /-->`, nil, []string{`x/none`}},
		{"template part missing", `<!-- wp:template-part {"slug":"sidebar"} /-->`, nil, []string{`sidebar`}},
		{"unknown with HTML", `<!-- wp:acme/box --><div class="wp-block-acme-box"><p>Kept</p></div><!-- /wp:acme/box -->`,
			[]string{`<div class="wp-block-acme-box"><p>Kept</p></div>`}, nil},
		{"unknown without HTML", `<!-- wp:acme/dynamic {"x":1} /-->`, nil, []string{`acme`}},
		{"woocommerce", `<!-- wp:woocommerce/cart /-->`, nil, []string{`woocommerce`}},
		{"freeform", `<section><h2>Free</h2></section>`, []string{`<section><h2>Free</h2></section>`}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := mustConvert(t, minimal(tc.markup))
			got := content(t, res, "home")
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %s\nin %s", w, got)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(got, n) {
					t.Errorf("has %s\nin %s", n, got)
				}
			}
		})
	}
}

// The post blocks of single and page templates read .Post; comments
// are the site's own.
func TestPostBlocks(t *testing.T) {
	single := `<!-- wp:post-title {"level":1} /--><!-- wp:post-content /--><!-- wp:post-date /--><!-- wp:post-author {"showAvatar":true} /--><!-- wp:post-terms {"term":"post_tag"} /--><!-- wp:post-featured-image /-->` +
		`<!-- wp:post-navigation-link {"type":"previous"} /--><!-- wp:post-navigation-link /-->` +
		`<!-- wp:comments --><div class="wp-block-comments"><!-- wp:comments-title /--><!-- wp:comment-template --><!-- wp:comment-content /--><!-- /wp:comment-template --></div><!-- /wp:comments -->`
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>i</p><!-- /wp:paragraph -->`, "templates/single.html", single, "templates/page.html", single))
	for _, page := range []string{"single", "page"} {
		got := content(t, res, page)
		for _, w := range []string{
			`{{with $.Post}}<h1 class="wp-block-post-title">{{.Title}}</h1>{{end}}`, `{{.Content}}`, `prose`,
			`{{with .Tags}}`, `{{with $.Prev}}<a href="{{.URL}}" rel="prev">`, `{{with $.Next}}<a href="{{.URL}}" rel="next">`,
			`{{template "shared/comments" $}}`,
		} {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %s\nin %s", page, w, got)
			}
		}
		if strings.Contains(got, "comment-content") {
			t.Errorf("%s: the comment blocks were kept", page)
		}
	}
	if !hasNote(res.Report, "block.comments-inner") {
		t.Error("no note for the replaced comment blocks")
	}
	// Comments on a listing are left out.
	res = mustConvert(t, minimal(`<!-- wp:comments /-->`))
	if strings.Contains(content(t, res, "home"), "shared/comments") {
		t.Error("comments on the home page")
	}
}

// Template parts in the header and footer go to the layout, with the
// tools and the footer credit, and patterns are written in place.
func TestLayoutParts(t *testing.T) {
	files := minimal(`<!-- wp:template-part {"slug":"header","tagName":"header"} /--><!-- wp:paragraph --><p>Body</p><!-- /wp:paragraph --><!-- wp:template-part {"slug":"footer","tagName":"footer"} /-->`,
		"parts/header.html", `<!-- wp:site-title /--><!-- wp:navigation /-->`,
		"parts/footer.html", `<!-- wp:pattern {"slug":"mini/credit"} /-->`,
		"patterns/credit.php", "<?php\n/**\n * Title: Credit\n * Slug: mini/credit\n */\n?>\n<!-- wp:paragraph --><p><?php esc_html_e( 'Proudly made', 'mini' ); ?> <?php echo esc_html( gmdate( 'Y' ) ); ?></p><!-- /wp:paragraph -->")
	res := mustConvert(t, files)
	layout := string(res.Files["templates/layout.html"])
	for _, w := range []string{`{{$.Site.Title}}`, `($.Site.Menu "primary")`, `{{template "shared/theme-toggle"}}`, `{{template "shared/lang-switch" $}}`,
		`Proudly made {{$.Site.Year}}`, `{{template "shared/footer-meta" .}}`, `<main id="main"`, `{{template "content" .}}`} {
		if !strings.Contains(layout, w) {
			t.Errorf("layout: missing %s", w)
		}
	}
	home := content(t, res, "home")
	if !strings.Contains(home, "<p>Body</p>") || strings.Contains(home, "Site.Title") {
		t.Errorf("home content: %s", home)
	}
	for _, page := range []string{"home", "single", "page", "archive", "search", "404"} {
		if _, ok := res.Files["templates/"+page+".html"]; !ok && page != "page" && page != "single" && page != "archive" && page != "search" {
			t.Errorf("no %s template", page)
		}
	}
}

// The query block's old displayLayout of columns (Twenty Twenty-Three)
// becomes a grid of its post template, as WordPress migrates it.
func TestQueryDisplayLayout(t *testing.T) {
	res := mustConvert(t, minimal(`<!-- wp:query {"query":{"inherit":true},"displayLayout":{"type":"flex","columns":3}} --><div class="wp-block-query">`+
		`<!-- wp:post-template --><!-- wp:post-title /--><!-- /wp:post-template --></div><!-- /wp:query -->`))
	home := content(t, res, "home")
	if !strings.Contains(home, "is-layout-grid") || !strings.Contains(home, "sap-wp-grid-columns") {
		t.Errorf("no grid: %s", home)
	}
	if !strings.Contains(string(res.Files["static/theme.css"]), "repeat(3, minmax(0, 1fr))") {
		t.Error("no three columns")
	}
}

// A front page made only of patterns registered from PHP (Raft) converts
// to empty wrappers: it is left out, and the front page uses home.
func TestEmptyFrontPage(t *testing.T) {
	res := mustConvert(t, minimal(`<!-- wp:paragraph --><p>Home</p><!-- /wp:paragraph -->`,
		"templates/front-page.html", `<!-- wp:group {"tagName":"main"} --><main class="wp-block-group"><!-- wp:pattern {"slug":"x/registered-in-php"} /--></main><!-- /wp:group -->`))
	if _, ok := res.Files["templates/front.html"]; ok {
		t.Error("an empty front template was written")
	}
	if !hasNote(res.Report, "template.empty") {
		t.Errorf("no template.empty note: %v", res.Report.Notes)
	}
	res = mustConvert(t, minimal(`<!-- wp:paragraph --><p>Home</p><!-- /wp:paragraph -->`,
		"templates/front-page.html", `<!-- wp:group --><div class="wp-block-group"><!-- wp:paragraph --><p>Front</p><!-- /wp:paragraph --></div><!-- /wp:group -->`))
	if !strings.Contains(content(t, res, "front"), "Front") {
		t.Error("a front template with content was left out")
	}
}
