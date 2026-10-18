package wpimport

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

func render(t *testing.T, md string) string {
	t.Helper()
	var buf bytes.Buffer
	if err := goldmark.New(goldmark.WithExtensions(extension.GFM)).Convert([]byte(md), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("missing %q in:\n%s", p, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if strings.Contains(s, p) {
			t.Errorf("unexpected %q in:\n%s", p, s)
		}
	}
}

func TestToMarkdownClassic(t *testing.T) {
	in := "First paragraph with <strong>bold</strong> and <em>italics</em>.\nSame paragraph, new line.\n\n<h2>Title</h2>\nText after the heading &amp; an entity.\n\n<ul>\n<li>one</li>\n<li>two</li>\n</ul>\n\n<blockquote>A quote</blockquote>"
	md, excerpt := ToMarkdown(in, ConvertOptions{})
	want := "First paragraph with **bold** and *italics*.  \nSame paragraph, new line.\n\n## Title\n\nText after the heading & an entity.\n\n- one\n- two\n\n> A quote"
	if md != want {
		t.Errorf("got:\n%s\nwant:\n%s", md, want)
	}
	if excerpt != "" {
		t.Errorf("excerpt without <!--more--> = %q", excerpt)
	}
}

func TestToMarkdownGutenberg(t *testing.T) {
	in := `<!-- wp:heading {"level":3} -->
<h3 class="wp-block-heading">Heading</h3>
<!-- /wp:heading -->

<!-- wp:paragraph -->
<p>Text with a <a href="https://example.org/x">link</a>.</p>
<!-- /wp:paragraph -->

<!-- wp:list -->
<ul class="wp-block-list"><!-- wp:list-item -->
<li>One</li>
<!-- /wp:list-item -->
<!-- wp:list-item -->
<li>Two<!-- wp:list -->
<ul class="wp-block-list"><!-- wp:list-item -->
<li>Nested</li>
<!-- /wp:list-item --></ul>
<!-- /wp:list --></li>
<!-- /wp:list-item --></ul>
<!-- /wp:list -->

<!-- wp:table -->
<figure class="wp-block-table"><table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table></figure>
<!-- /wp:table -->

<!-- wp:separator -->
<hr class="wp-block-separator"/>
<!-- /wp:separator -->

<!-- wp:image {"id":5} /-->`
	md, _ := ToMarkdown(in, ConvertOptions{})
	want := "### Heading\n\nText with a [link](https://example.org/x).\n\n- One\n- Two\n  - Nested\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n* * *"
	if md != want {
		t.Errorf("got:\n%s\nwant:\n%s", md, want)
	}
	mustNotContain(t, md, "wp:", "<!--")
	// Gutenberg content is not run through wpautop: single newlines inside
	// a block must not become <br>.
	md, _ = ToMarkdown("<!-- wp:paragraph -->\n<p>one\ntwo</p>\n<!-- /wp:paragraph -->", ConvertOptions{})
	if md != "one two" {
		t.Errorf("gutenberg paragraph = %q", md)
	}
}

func TestToMarkdownMoreSplit(t *testing.T) {
	md, excerpt := ToMarkdown("Teaser with <b>bold</b>.\n\nSecond teaser paragraph.\n<!--more Read on-->\nThe rest.", ConvertOptions{})
	if md != "Teaser with **bold**.\n\nSecond teaser paragraph.\n\nThe rest." {
		t.Errorf("md = %q", md)
	}
	if excerpt != "Teaser with bold.\n\nSecond teaser paragraph." {
		t.Errorf("excerpt = %q", excerpt)
	}
	mustNotContain(t, md, "WPIMPORT", "more")

	md, excerpt = ToMarkdown("Teaser [contact-form-7 id=\"1\"] end.\n\n[tabs x=1]\n\n<!--more-->\n\nBody", ConvertOptions{})
	if excerpt != "Teaser end." || !strings.Contains(md, `[contact-form-7 id="1"]`) {
		t.Errorf("shortcodes in teaser: md %q excerpt %q", md, excerpt)
	}

	md, excerpt = ToMarkdown("<!-- wp:paragraph --><p>Intro</p><!-- /wp:paragraph --><!-- wp:more --><!--more--><!-- /wp:more --><!-- wp:paragraph --><p>Body</p><!-- /wp:paragraph -->", ConvertOptions{})
	if md != "Intro\n\nBody" || excerpt != "Intro" {
		t.Errorf("gutenberg more: md %q excerpt %q", md, excerpt)
	}
}

func TestToMarkdownCaption(t *testing.T) {
	for _, in := range []string{
		`[caption id="attachment_6" align="aligncenter" width="300"]<a href="https://example.com/big.jpg"><img class="size-medium" src="https://example.com/small.jpg" alt="Alt text" width="300" height="200" /></a> The <em>caption</em>[/caption]`,
		`[caption id="" align="alignnone" width="300" caption="The caption"]<img src="https://example.com/small.jpg" alt="Alt text" />[/caption]`,
	} {
		md, _ := ToMarkdown("Before.\n\n"+in+"\n\nAfter.", ConvertOptions{})
		mustContain(t, md, "![Alt text](https://example.com/small.jpg)", "*The caption*", "Before.\n\n", "\n\nAfter.")
		mustNotContain(t, md, "[caption", "[/caption]")
		html := render(t, md)
		mustContain(t, html, `<img src="https://example.com/small.jpg" alt="Alt text"`, "<em>The caption</em>")
	}
	// Gutenberg figcaption.
	md, _ := ToMarkdown(`<!-- wp:image --><figure class="wp-block-image"><img src="https://example.com/a.png" alt=""/><figcaption class="wp-element-caption">Under the image</figcaption></figure><!-- /wp:image -->`, ConvertOptions{})
	if md != "![](https://example.com/a.png)\n\n*Under the image*" {
		t.Errorf("figcaption = %q", md)
	}
}

func TestToMarkdownEmbedsAndBareURLs(t *testing.T) {
	in := "Intro.\n\nhttps://www.youtube.com/watch?v=abc_123\n\n[embed width=\"640\"]https://vimeo.com/76979871[/embed]\n\nA sentence with https://example.org/inline in it.\n\n<pre>https://example.org/in-code</pre>"
	md, _ := ToMarkdown(in, ConvertOptions{})
	mustContain(t, md, "<https://www.youtube.com/watch?v=abc_123>", "<https://vimeo.com/76979871>",
		"A sentence with https://example.org/inline in it.", "```\nhttps://example.org/in-code\n```")
	mustNotContain(t, md, "[embed", "abc\\_123")

	md, _ = ToMarkdown(`<!-- wp:embed {"url":"https://youtu.be/xyz"} --><figure class="wp-block-embed"><div class="wp-block-embed__wrapper">
https://youtu.be/xyz
</div></figure><!-- /wp:embed -->`, ConvertOptions{})
	if md != "<https://youtu.be/xyz>" {
		t.Errorf("gutenberg embed = %q", md)
	}
}

func TestToMarkdownCodeBlocks(t *testing.T) {
	tests := map[string]string{
		`<pre class="wp-block-code"><code class="language-go">fmt.Println("&lt;hi&gt;")</code></pre>`:  "```go\nfmt.Println(\"<hi>\")\n```",
		`<pre class="wp-block-code"><code lang="python" class="language-python">print(1)</code></pre>`: "```python\nprint(1)\n```",
		"<pre class=\"brush: php; gutter: true\">&lt;?php\necho 1;</pre>":                              "```php\n<?php\necho 1;\n```",
		`<pre class="lang:js decode:true">let a = 1;</pre>`:                                            "```js\nlet a = 1;\n```",
		`<pre class="wp-block-code"><code>plain</code></pre>`:                                          "```\nplain\n```",
		"[code lang=\"js\"]if (a < b && c) { [embed]x[/embed] }[/code]":                                "```js\nif (a < b && c) { [embed]x[/embed] }\n```",
		"[php]echo \"&lt;b&gt;\";[/php]":                                                               "```php\necho \"<b>\";\n```",
		"<pre class=\"language-c#\">var x = 1;</pre>":                                                  "```csharp\nvar x = 1;\n```",
		"<pre>line 1\n\n    indented line</pre>":                                                       "```\nline 1\n\n    indented line\n```",
	}
	for in, want := range tests {
		if md, _ := ToMarkdown(in, ConvertOptions{}); md != want {
			t.Errorf("ToMarkdown(%q):\n got %q\nwant %q", in, md, want)
		}
	}
}

func TestToMarkdownRewritesURLs(t *testing.T) {
	var seen []string
	opts := ConvertOptions{
		BaseURL: "https://old.example/blog/",
		RewriteURL: func(u string) string {
			seen = append(seen, u)
			switch {
			case strings.Contains(u, "/wp-content/uploads/photo"):
				return "/uploads/2026/10/abc.jpg"
			case u == "https://old.example/hello/":
				return "/blog/hello"
			}
			return u
		},
	}
	in := `<p><a href="https://old.example/wp-content/uploads/photo.jpg"><img src="/wp-content/uploads/photo-300x200.jpg" alt="P" srcset="x 1x" onerror="evil()"></a></p>
<p>See <a href="https://old.example/hello/">hello</a>, <a href="#top">top</a> and <a href="mailto:a@example.org">mail</a>.</p>
<p><video controls src="https://old.example/wp-content/uploads/clip.mp4"></video></p>
<p><img data-lazy-src="https://old.example/wp-content/uploads/photo-lazy.jpg" src="data:image/gif;base64,R0lGOD" alt="L"></p>`
	md, _ := ToMarkdown(in, opts)
	want := "![P](/uploads/2026/10/abc.jpg)\n\nSee [hello](/blog/hello), [top](#top) and [mail](mailto:a@example.org).\n\n[clip.mp4](https://old.example/wp-content/uploads/clip.mp4)\n\n![L](/uploads/2026/10/abc.jpg)"
	if md != want {
		t.Errorf("got:\n%s\nwant:\n%s", md, want)
	}
	mustContain(t, strings.Join(seen, " "), "https://old.example/wp-content/uploads/photo-300x200.jpg", "https://old.example/hello/")
	for _, u := range seen {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("RewriteURL got non-absolute %q", u)
		}
	}
}

func TestToMarkdownDropsDangerousContent(t *testing.T) {
	in := `<p>Safe <a href="javascript:alert(1)">click</a> <img src="javascript:alert(2)" alt="x"> <img src="data:image/svg+xml;base64,PHN2Zz4=" alt="y"></p>
<script>alert(3)</script><style>p{display:none}</style>
<form action="https://evil.example"><input name="pw"><button>Go</button></form>
<iframe src="javascript:alert(4)"></iframe>
<iframe src="https://www.youtube.com/embed/abcdefghijk?rel=0" title="YouTube video player"></iframe>
<object data="x.swf"><embed src="x.swf"></object>
<svg onload="alert(5)"><text>svg</text></svg>
<div onclick="alert(6)" style="color:red">Text</div>`
	var warnings []string
	md, _ := ToMarkdown(in, ConvertOptions{Warn: func(m string) { warnings = append(warnings, m) }})
	want := "Safe click\n\n<https://www.youtube.com/watch?v=abcdefghijk>\n\nText"
	if md != want {
		t.Errorf("got:\n%s\nwant:\n%s", md, want)
	}
	html := render(t, md)
	mustNotContain(t, strings.ToLower(html), "javascript", "script", "alert", "onclick", "onload", "data:", "<form", "<iframe", "style=")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "form") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestToMarkdownIndentedHTMLIsNotCode(t *testing.T) {
	for _, in := range []string{
		"<div class=\"box\">\n    <p>\n        Indented paragraph.\n    </p>\n        <p>More indented text.</p>\n</div>",
		"Intro\n\n        Indented classic text line.\n\n    <div>\n        <span>nested</span>\n    </div>",
		"<!-- wp:group -->\n<div class=\"wp-block-group\">\n\t\t<!-- wp:paragraph -->\n\t\t<p>Tabbed</p>\n\t\t<!-- /wp:paragraph -->\n</div>\n<!-- /wp:group -->",
		"<table>\n    <tr>\n        <td>cell</td>\n    </tr>\n</table>\n\n    trailing",
	} {
		md, _ := ToMarkdown(in, ConvertOptions{})
		html := render(t, md)
		if strings.Contains(html, "<pre") || strings.Contains(html, "<code") {
			t.Errorf("indented HTML became code:\n%s\n->\n%s", md, html)
		}
	}
	// Indentation that belongs to a list survives.
	md, _ := ToMarkdown("<ol><li><p>one</p><p>continued</p></li><li>two<ul><li><ul><li>deep</li></ul></li></ul></li></ol>", ConvertOptions{})
	html := render(t, md)
	mustContain(t, html, "<p>continued</p>", "<li>deep</li>")
	mustNotContain(t, html, "<pre")
}

func TestToMarkdownShortcodes(t *testing.T) {
	var warnings []string
	opts := ConvertOptions{
		Warn: func(m string) { warnings = append(warnings, m) },
		Gallery: func(ids []int64) []Image {
			if len(ids) != 2 || ids[0] != 4 || ids[1] != 5 {
				t.Errorf("gallery ids = %v", ids)
			}
			return []Image{{URL: "https://example.com/a.jpg", Alt: "A", Caption: "First"}, {URL: "https://example.com/b.jpg", Alt: "B"}}
		},
	}
	md, _ := ToMarkdown("Form:\n\n[contact-form-7 id=\"9\" title=\"Contact_us\"]\n\n[gallery ids=\"4,5\" columns=\"2\"]\n\n[tabs][tab]x[/tab][/tabs]\n\n[audio mp3=\"https://example.com/song.mp3\"][/audio]\n\nA [sic] remark.", opts)
	mustContain(t, md, "[contact-form-7 id=\"9\" title=\"Contact_us\"]", "![A](https://example.com/a.jpg)\n\n*First*\n\n![B](https://example.com/b.jpg)",
		"[tabs][tab]x[/tab][/tabs]", "[song.mp3](https://example.com/song.mp3)", "A [sic] remark.")
	mustNotContain(t, md, `\[`, "WPIMPORT", "[gallery", "[audio")
	if len(warnings) != 1 || !strings.Contains(warnings[0], "[contact-form-7]") || !strings.Contains(warnings[0], "[tabs]") || strings.Contains(warnings[0], "sic") {
		t.Errorf("warnings = %q", warnings)
	}
	// Without a gallery resolver the shortcode disappears.
	if md, _ := ToMarkdown("[gallery]", ConvertOptions{}); md != "" {
		t.Errorf("gallery without resolver = %q", md)
	}
}

func TestToMarkdownEmptyAndWhitespace(t *testing.T) {
	for _, in := range []string{"", "  \n ", "<p>&nbsp;</p>", "<!-- wp:paragraph --><p></p><!-- /wp:paragraph -->"} {
		if md, ex := ToMarkdown(in, ConvertOptions{}); md != "" || ex != "" {
			t.Errorf("ToMarkdown(%q) = %q, %q", in, md, ex)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	for in, want := range map[string]string{
		"Hi <b>there</b> &amp; you\nsecond line\n\nNew para with <a href=\"https://x.example\">a link</a>.": "Hi there & you\nsecond line\n\nNew para with a link.",
		"<p>One</p><p>Two</p>":                    "One\n\nTwo",
		"<script>alert(1)</script>Only text":      "Only text",
		"[&#8230;] enlazado desde otro [&#8230;]": "[…] enlazado desde otro […]",
		"": "",
	} {
		if got := HTMLToText(in); got != want {
			t.Errorf("HTMLToText(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzToMarkdown(f *testing.F) {
	for _, s := range []string{
		"a\n\nb", "<!-- wp:paragraph --><p>x</p><!-- /wp:paragraph -->", "[caption]<img src=x>c[/caption]",
		"<pre class=\"brush: js\">x</pre>", "<!--more-->", "[gallery ids=\"1\"]", "<a href=\"javascript:x\"><img src=y></a>",
		"<ul><li>a<ul><li>b</li></ul></li></ul>", "[code]<x>[/code]", "<iframe src=\"https://x\"></iframe>", "<p>https://x.example</p>",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		opts := ConvertOptions{
			BaseURL:    "https://example.com",
			RewriteURL: func(u string) string { return u },
			Gallery:    func([]int64) []Image { return []Image{{URL: "https://example.com/a.jpg"}} },
		}
		md, _ := ToMarkdown(s, opts)
		if strings.Contains(md, "WPIMPORT") && !strings.Contains(s, "WPIMPORT") {
			t.Errorf("placeholder leaked: %q -> %q", s, md)
		}
		HTMLToText(s)
	})
}

func TestToMarkdownAngleBrackets(t *testing.T) {
	for in, want := range map[string]string{
		"<p>a < b and c -> d, 5<6 and x>=y</p>":                        "a < b and c -> d, 5<6 and x>=y",
		"<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>":                 "&lt;script>alert(1)&lt;/script>",
		"<p>&gt; not a quote</p>":                                      "&gt; not a quote",
		"<ul><li>&gt; not a quote either</li></ul>":                    "- &gt; not a quote either",
		"<p>Use <code>a &lt; b</code> or <code>``x``</code> &lt;3</p>": "Use `a < b` or ``` ``x`` ``` <3",
		"<p>&lt;https://not-an-autolink.example&gt;</p>":               "&lt;https://not-an-autolink.example>",
	} {
		md, _ := ToMarkdown(in, ConvertOptions{})
		if md != want {
			t.Errorf("ToMarkdown(%q) = %q, want %q", in, md, want)
		}
		html := render(t, md)
		mustNotContain(t, html, "<script", "<blockquote", "<a href")
	}
}
