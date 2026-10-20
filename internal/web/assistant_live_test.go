package web

import (
	"context"
	"strings"
	"testing"

	"sap13ns/internal/config"
	"sap13ns/internal/testutil"
)

const frontHTML = `<!doctype html><html><head><title>Home · Acme</title><script>var x = "hidden";</script></head>
<body><header><nav><a href="/">Menu link</a></nav></header>
<main>
<section id="intro"><h1>Fast sites for everyone</h1><p>Acme builds <b>websites</b> in a week.</p>
<div aria-hidden="true">decoration</div><button>Click</button></section>
<section id="pricing"><h2 id="pricing-title">Precios claros</h2><p>Pay monthly or yearly.</p>
<div class="plans-box" data-plans><article><h3>Pro</h3><p>9 € / month</p></article></div></section>
<section id="faq"><h2>Questions</h2><p>Can I cancel? Yes, at any time.</p><svg><text>no</text></svg></section>
</main><footer>Powered by sap13ns</footer></body></html>`

func TestParseFront(t *testing.T) {
	fp := parseFront([]byte(frontHTML))
	if fp == nil {
		t.Fatal("no front page")
	}
	if fp.Title != "Acme" {
		t.Errorf("title %q", fp.Title)
	}
	if fp.Plans != "pricing-title" {
		t.Errorf("plans anchor %q", fp.Plans)
	}
	var heads []string
	for _, s := range fp.Sections {
		heads = append(heads, s.Heading+"#"+s.Anchor)
	}
	if got := strings.Join(heads, " | "); got != "Fast sites for everyone#intro | Precios claros#pricing-title | Questions#faq" {
		t.Errorf("sections: %s", got)
	}
	all := ""
	for _, s := range fp.Sections {
		all += s.Text + "\n"
	}
	for _, bad := range []string{"hidden", "Menu link", "decoration", "Click", "Powered", "no\n"} {
		if strings.Contains(all, bad) {
			t.Errorf("text has %q:\n%s", bad, all)
		}
	}
	if !strings.Contains(all, "Acme builds websites in a week.") || !strings.Contains(all, "9 € / month") {
		t.Errorf("text:\n%s", all)
	}
	if parseFront([]byte("<html><body></body></html>")) != nil {
		t.Error("an empty page has sections")
	}
}

func TestRankSections(t *testing.T) {
	fp := parseFront([]byte(frontHTML))
	top := func(q string) string {
		var hs []string
		for _, s := range rankSections(fp.Sections, q, 2) {
			hs = append(hs, s.Heading)
		}
		return strings.Join(hs, ", ")
	}
	for q, want := range map[string]string{
		"¿Cuáles son los precios?":       "Precios claros",
		"can I cancel my plan":           "Questions",
		"what is this about":             "",                          // only stop words
		"websites, questions and precio": "Precios claros, Questions", // headings count double
	} {
		if got := top(q); got != want {
			t.Errorf("%q: %q, want %q", q, got, want)
		}
	}
}

// On a portal the assistant gets the plans in words, translated by the
// theme like the pricing table, and the front page of the site.
func TestLiveSources(t *testing.T) {
	s := networkServer(t, testutil.Store(t), func(c *config.Config, _ *Deps) { c.SitesDomain = "sap13ns.site" })
	if got := s.liveSources(context.Background(), "en", "plans", 1); len(got) != 0 && got[0].Title == "Hosting plans" {
		t.Fatal("a site that sells nothing describes plans")
	}
	s.portal = &fakePlansPortal{plans: []NetPlan{{Slug: "personal", Name: "Personal", HandApproved: true, SitesPerGrant: 1, StorageBytes: 1 << 30}}}
	src := s.liveSources(context.Background(), "en", "how much does it cost", 3)
	if len(src) == 0 || src[0].Title != "Hosting plans" || src[0].N != 3 {
		t.Fatalf("sources: %+v", src)
	}
	if c := src[0].Content; !strings.Contains(c, "- Personal: free") || !strings.Contains(c, "1 site") {
		t.Errorf("plans: %q", c)
	}
	for i, x := range src {
		if x.N != 3+i || !strings.HasPrefix(x.URL, "/") {
			t.Errorf("source %d: %+v", i, x)
		}
	}
	if s.front.pages == nil {
		t.Error("the front page was not rendered and kept")
	}
	s.contentChanged()
	if s.front.pages != nil {
		t.Error("a content change keeps the old front page")
	}
}
