package main

import (
	"strings"
	"testing"
)

var defaults = Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}

const tocOpen = `<nav class="card toc" aria-label="Contents"><header><strong>Contents</strong></header>`

func TestFilter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		opt     Options
		want    string // exact output; "" with changed=false means unchanged
		changed bool
	}{
		{
			name: "after the first paragraph, existing ids kept",
			in:   `<p>Intro</p><h2 id="one">One</h2><p>a</p><h3 id="sub">Sub</h3><h2 id="two">Two</h2>`,
			opt:  defaults,
			want: `<p>Intro</p>` + tocOpen + `<ol><li><a href="#one">One</a><ol><li><a href="#sub">Sub</a></li></ol></li><li><a href="#two">Two</a></li></ol></nav>` +
				`<h2 id="one">One</h2><p>a</p><h3 id="sub">Sub</h3><h2 id="two">Two</h2>`,
			changed: true,
		},
		{
			name: "missing ids are added, duplicates numbered, other ids avoided",
			in:   `<p id="setup">x</p><h2>Setup</h2><h2>Setup</h2><h2>Instalación</h2><h2 id="">Setup</h2>`,
			opt:  defaults,
			want: `<p id="setup">x</p>` + tocOpen + `<ol><li><a href="#setup-2">Setup</a></li><li><a href="#setup-3">Setup</a></li><li><a href="#instalacion">Instalación</a></li><li><a href="#setup-4">Setup</a></li></ol></nav>` +
				`<h2 id="setup-2">Setup</h2><h2 id="setup-3">Setup</h2><h2 id="instalacion">Instalación</h2><h2 id="setup-4">Setup</h2>`,
			changed: true,
		},
		{
			name: "below the minimum nothing changes",
			in:   `<p>Intro</p><h2>One</h2><h2>Two</h2>`,
			opt:  defaults,
		},
		{
			name:    "markers are removed even without a table",
			in:      `<p>Intro</p><p>[toc]</p><h2>One</h2>`,
			opt:     defaults,
			want:    `<p>Intro</p><h2>One</h2>`,
			changed: true,
		},
		{
			name:    "disabled for this post type: markers only",
			in:      `<p>[TOC]</p><h2>A</h2><h2>B</h2><h2>C</h2>`,
			opt:     Options{Enabled: false, MinHeadings: 1, MaxLevel: 4, Title: "Contents"},
			want:    `<h2>A</h2><h2>B</h2><h2>C</h2>`,
			changed: true,
		},
		{
			name: "marker paragraph wins over the first paragraph; extra markers dropped",
			in:   `<p>Intro</p><h2 id="a">A</h2><p> [Toc] </p><h2 id="b">B</h2><p>[toc]</p><h2 id="c">C</h2>`,
			opt:  defaults,
			want: `<p>Intro</p><h2 id="a">A</h2>` + tocOpen + `<ol><li><a href="#a">A</a></li><li><a href="#b">B</a></li><li><a href="#c">C</a></li></ol></nav>` +
				`<h2 id="b">B</h2><h2 id="c">C</h2>`,
			changed: true,
		},
		{
			name:    "bare text marker",
			in:      "<p>Intro</p>\n[toc]\n<h2 id=\"a\">A</h2>",
			opt:     Options{Enabled: true, MinHeadings: 1, MaxLevel: 4, Title: "Contents"},
			want:    "<p>Intro</p>" + tocOpen + `<ol><li><a href="#a">A</a></li></ol></nav>` + "<h2 id=\"a\">A</h2>",
			changed: true,
		},
		{
			name: "[toc] written as code is not a marker",
			in:   `<p>Use <code>[toc]</code></p><p><code>[toc]</code></p><h2 id="a">A</h2>`,
			opt:  Options{Enabled: true, MinHeadings: 1, MaxLevel: 4, Title: "Contents"},
			want: `<p>Use <code>[toc]</code></p>` + tocOpen + `<ol><li><a href="#a">A</a></li></ol></nav>` +
				`<p><code>[toc]</code></p><h2 id="a">A</h2>`,
			changed: true,
		},
		{
			name: "headings in quotes, code, callouts and boxes are ignored",
			in: `<p>i</p><blockquote><h2>Quoted</h2></blockquote><pre><code>## not a heading</code></pre>` +
				`<div class="alert"><h2>Callout</h2></div><aside><h2>Aside</h2></aside><details><summary>s</summary><h3>Hidden</h3></details>` +
				`<h2 id="real">Real</h2>`,
			opt: Options{Enabled: true, MinHeadings: 1, MaxLevel: 4, Title: "Contents"},
			want: `<p>i</p>` + tocOpen + `<ol><li><a href="#real">Real</a></li></ol></nav>` +
				`<blockquote><h2>Quoted</h2></blockquote><pre><code>## not a heading</code></pre>` +
				`<div class="alert"><h2>Callout</h2></div><aside><h2>Aside</h2></aside><details><summary>s</summary><h3>Hidden</h3></details>` +
				`<h2 id="real">Real</h2>`,
			changed: true,
		},
		{
			name: "max depth h3 drops h4 (and leaves it without id)",
			in:   `<h2>A</h2><h3>B</h3><h4>C</h4><h2>D</h2>`,
			opt:  Options{Enabled: true, MinHeadings: 3, MaxLevel: 3, Title: "Contents"},
			want: tocOpen + `<ol><li><a href="#a">A</a><ol><li><a href="#b">B</a></li></ol></li><li><a href="#d">D</a></li></ol></nav>` +
				`<h2 id="a">A</h2><h3 id="b">B</h3><h4>C</h4><h2 id="d">D</h2>`,
			changed: true,
		},
		{
			name: "skipped levels indent once; a leading h3 stays at the top",
			in:   `<h3 id="pre">Pre</h3><h2 id="a">A</h2><h4 id="deep">Deep</h4><h3 id="b">B</h3><h2 id="c">C</h2>`,
			opt:  defaults,
			want: tocOpen + `<ol><li><a href="#pre">Pre</a></li><li><a href="#a">A</a><ol><li><a href="#deep">Deep</a></li><li><a href="#b">B</a></li></ol></li><li><a href="#c">C</a></li></ol></nav>` +
				`<h3 id="pre">Pre</h3><h2 id="a">A</h2><h4 id="deep">Deep</h4><h3 id="b">B</h3><h2 id="c">C</h2>`,
			changed: true,
		},
		{
			name: "heading text flattens markup, drops footnotes and screen-reader text",
			in: `<p>x</p><h2>Use <code>go&nbsp;vet</code>  <em>now</em><sup id="fnref:1"><a href="#fn:1" class="footnote-ref">1</a></sup></h2>` +
				`<h2><a href="https://e.test">Link<span class="sr-only"> (opens in a new tab)</span></a></h2><h2>  </h2><h2>&lt;b&gt;</h2>`,
			opt: Options{Enabled: true, MinHeadings: 2, MaxLevel: 4, Title: "Contents"},
			want: `<p>x</p>` + tocOpen + `<ol><li><a href="#use-go-vet-now">Use go vet now</a></li><li><a href="#link">Link</a></li><li><a href="#b">&lt;b&gt;</a></li></ol></nav>` +
				`<h2 id="use-go-vet-now">Use <code>go` + "\u00a0" + `vet</code>  <em>now</em><sup id="fnref:1"><a href="#fn:1" class="footnote-ref">1</a></sup></h2>` +
				`<h2 id="link"><a href="https://e.test">Link<span class="sr-only"> (opens in a new tab)</span></a></h2><h2>  </h2><h2 id="b">&lt;b&gt;</h2>`,
			changed: true,
		},
		{
			name: "non-Latin headings",
			in:   `<p>Вступление</p><h2>Почему?</h2><h2>Ελληνικά</h2><h2>日本語</h2>`,
			opt:  defaults,
			want: `<p>Вступление</p>` + tocOpen + `<ol><li><a href="#pochemu">Почему?</a></li><li><a href="#ellinika">Ελληνικά</a></li><li><a href="#` + slugify("日本語") + `">日本語</a></li></ol></nav>` +
				`<h2 id="pochemu">Почему?</h2><h2 id="ellinika">Ελληνικά</h2><h2 id="` + slugify("日本語") + `">日本語</h2>`,
			changed: true,
		},
		{
			name:    "a table already present is not duplicated",
			in:      `<p>[toc]</p><nav class="card toc"><ol></ol></nav><h2>A</h2><h2>B</h2><h2>C</h2>`,
			opt:     defaults,
			want:    `<nav class="card toc"><ol></ol></nav><h2>A</h2><h2>B</h2><h2>C</h2>`,
			changed: true,
		},
		{name: "empty input", in: "", opt: defaults},
		{name: "whitespace only", in: " \n\t", opt: defaults},
		{name: "no headings", in: `<p>Just text</p>`, opt: defaults},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := Filter(tc.in, tc.opt)
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v\n%s", changed, tc.changed, got)
			}
			want := tc.want
			if !tc.changed {
				want = tc.in
			}
			if got != want {
				t.Errorf("got\n%s\nwant\n%s", got, want)
			}
		})
	}
}

func TestFilterNoParagraphBeforeFirstHeading(t *testing.T) {
	got, _ := Filter(`<h2 id="a">A</h2><p>Body</p><h2 id="b">B</h2><h2 id="c">C</h2>`, defaults)
	if !strings.HasPrefix(got, tocOpen) {
		t.Errorf("table not inserted before the first heading:\n%s", got)
	}
}

func TestFilterMalformedHTML(t *testing.T) {
	for _, in := range []string{
		`<p>Intro<h2>Open <b>bold<h2>Second</p><h3>Third`,
		`<h2 id="x" id="y">Dup attr</h2><h2>B</h2><h2>C`,
		`</div></p><h2>Stray closers</h2><h2>B</h2><h2>C</h2>`,
		`<p>[toc]<h2>A</h2><h2>B</h2><h2>C</h2>`,
		"<h2>\x00nul</h2><h2>B</h2><h2>C</h2>",
	} {
		got, changed := Filter(in, defaults)
		if !changed || !strings.Contains(got, `class="card toc"`) {
			t.Errorf("Filter(%q) = %q, %v; want a table", in, got, changed)
		}
		if strings.Count(got, `class="card toc"`) != 1 {
			t.Errorf("Filter(%q) inserted several tables: %s", in, got)
		}
	}
}

func TestFilterTitleIsEscaped(t *testing.T) {
	o := defaults
	o.Title = `Índice <b>"x"</b>`
	got, _ := Filter(`<h2>A</h2><h2>B</h2><h2>C</h2>`, o)
	if !strings.Contains(got, `aria-label="Índice bx/b"`) {
		t.Errorf("aria-label not cleaned for the sanitizer: %s", got)
	}
	if !strings.Contains(got, `<strong>Índice &lt;b&gt;&#34;x&#34;&lt;/b&gt;</strong>`) {
		t.Errorf("title not escaped: %s", got)
	}
}

func TestOptionsFrom(t *testing.T) {
	for _, tc := range []struct {
		name               string
		settings           map[string]string
		typ, lang, siteLng string
		want               Options
	}{
		{"defaults for a post", map[string]string{}, "post", "", "en",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}},
		{"pages are off by default", map[string]string{}, "page", "en", "en",
			Options{Enabled: false, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}},
		{"Spanish post on an English site", map[string]string{"post_types": "both"}, "page", "es", "en",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Índice"}},
		{"site language when the post has none", map[string]string{}, "post", "", "es",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Índice"}},
		{"regional Spanish", map[string]string{}, "post", "es-mx", "en",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Índice"}},
		{"other languages fall back to English", map[string]string{}, "post", "fr", "es",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}},
		{"custom values", map[string]string{"min_headings": " 5 ", "max_depth": "h2", "title": " On this page ", "post_types": "pages"}, "page", "es", "es",
			Options{Enabled: true, MinHeadings: 5, MaxLevel: 2, Title: "On this page"}},
		{"pages only skips posts", map[string]string{"post_types": "pages"}, "post", "", "en",
			Options{Enabled: false, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}},
		{"bad numbers keep the default, zero means one", map[string]string{"min_headings": "x", "max_depth": "h9"}, "", "", "",
			Options{Enabled: true, MinHeadings: 3, MaxLevel: 4, Title: "Contents"}},
		{"zero minimum", map[string]string{"min_headings": "0", "max_depth": "h3"}, "post", "", "",
			Options{Enabled: true, MinHeadings: 1, MaxLevel: 3, Title: "Contents"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OptionsFrom(tc.settings, tc.typ, tc.lang, tc.siteLng); got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
