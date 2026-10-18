package main

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

func TestRestoreIcons(t *testing.T) {
	const svg = `<svg xmlns="http://www.w3.org/2000/svg" class="lucide lucide-%s" width="24" height="24" viewBox="0 0 24 24" aria-hidden="true"><path d="M1 1"></path></svg>`
	icon := func(name string) string { return strings.Replace(svg, "%s", name, 1) }
	for _, tc := range []struct{ in, want string }{
		{`<div class="alert">` + icon("info") + `<strong>x</strong></div>`, `<div class="alert"><span data-icon="info"></span><strong>x</strong></div>`},
		{`<p>a ` + icon("circle-check") + ` b ` + icon("triangle-alert") + `</p>`, `<p>a <span data-icon="circle-check"></span> b <span data-icon="triangle-alert"></span></p>`},
		{icon("x"), `<span data-icon="x"></span>`},
		{icon("Bad_Name"), icon("Bad_Name")},
		{`<svg class="other"><circle></circle></svg>`, `<svg class="other"><circle></circle></svg>`},
		{`<p>no icons — 日本語</p>`, `<p>no icons — 日本語</p>`},
		{``, ``},
	} {
		root := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
		nodes, err := html.ParseFragment(strings.NewReader(tc.in), root)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			root.AppendChild(n)
		}
		restoreIcons(root)
		var b strings.Builder
		for c := root.FirstChild; c != nil; c = c.NextSibling {
			html.Render(&b, c)
		}
		if got := b.String(); got != tc.want {
			t.Errorf("restoreIcons(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}
