package wpimport

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A sap13ns export: WXR plus the sap13ns namespace and postmeta.
const sap13nsWXR = `<?xml version="1.0" encoding="UTF-8" ?>
<rss version="2.0" xmlns:excerpt="http://wordpress.org/export/1.2/excerpt/" xmlns:content="http://purl.org/rss/1.0/modules/content/"
	xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:sap13ns="https://sap13ns.site/export/1.0/">
<channel>
	<title>Old</title><link>https://old.example</link><language>es</language><wp:wxr_version>1.2</wp:wxr_version>
	<wp:category><wp:term_id>1</wp:term_id><wp:category_nicename><![CDATA[noticias]]></wp:category_nicename><wp:category_parent><![CDATA[]]></wp:category_parent>
		<wp:cat_name><![CDATA[Noticias]]></wp:cat_name>
		<wp:termmeta><wp:meta_key><![CDATA[_sap13ns_i18n]]></wp:meta_key><wp:meta_value><![CDATA[{"en":{"name":"News","description":"All of it"},"xx!":{"name":"bad"},"fr":{"name":""}}]]></wp:meta_value></wp:termmeta></wp:category>
	<sap13ns:redirect><sap13ns:source><![CDATA[/viejo]]></sap13ns:source><sap13ns:target><![CDATA[/blog/hola]]></sap13ns:target><sap13ns:status>302</sap13ns:status></sap13ns:redirect>
	<sap13ns:redirect><sap13ns:source><![CDATA[/gone]]></sap13ns:source><sap13ns:target><![CDATA[/ignored]]></sap13ns:target><sap13ns:status>410</sap13ns:status></sap13ns:redirect>
	<sap13ns:redirect><sap13ns:source><![CDATA[/odd]]></sap13ns:source><sap13ns:target><![CDATA[/x]]></sap13ns:target><sap13ns:status>299</sap13ns:status></sap13ns:redirect>
	<sap13ns:redirect><sap13ns:source><![CDATA[https://evil.example/]]></sap13ns:source><sap13ns:target><![CDATA[/x]]></sap13ns:target><sap13ns:status>301</sap13ns:status></sap13ns:redirect>
	<sap13ns:redirect><sap13ns:source><![CDATA[/viejo]]></sap13ns:source><sap13ns:target><![CDATA[/again]]></sap13ns:target><sap13ns:status>301</sap13ns:status></sap13ns:redirect>
	<item><title>Hola</title><link>https://old.example/blog/hola</link><wp:post_id>10</wp:post_id><wp:post_name><![CDATA[hola]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[publish]]></wp:status><wp:post_date_gmt><![CDATA[2026-03-04 10:00:00]]></wp:post_date_gmt>
		<content:encoded><![CDATA[<p>HTML that is not used</p>]]></content:encoded>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_markdown]]></wp:meta_key><wp:meta_value><![CDATA[  Indented first line.

![pic](/uploads/2026/10/pic.png) [hello](/en/blog/hello) [contact](/contacto) ![cdn](https://cdn.example/x.png)
]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_lang]]></wp:meta_key><wp:meta_value><![CDATA[es]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_translation_group]]></wp:meta_key><wp:meta_value><![CDATA[10]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_yoast_wpseo_metadesc]]></wp:meta_key><wp:meta_value><![CDATA[  Para
 buscadores ]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_assistant_exclude]]></wp:meta_key><wp:meta_value><![CDATA[1]]></wp:meta_value></wp:postmeta>
	</item>
	<item><title>Hello</title><link>https://old.example/en/blog/hello</link><wp:post_id>11</wp:post_id><wp:post_name><![CDATA[hello]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[publish]]></wp:status><wp:post_date_gmt><![CDATA[2026-03-01 10:00:00]]></wp:post_date_gmt>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_markdown]]></wp:meta_key><wp:meta_value><![CDATA[Back to [hola](/blog/hola).]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_lang]]></wp:meta_key><wp:meta_value><![CDATA[en]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_translation_group]]></wp:meta_key><wp:meta_value><![CDATA[10]]></wp:meta_value></wp:postmeta>
	</item>
	<item><title>Hello again</title><wp:post_id>12</wp:post_id><wp:post_name><![CDATA[hello-again]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[publish]]></wp:status><wp:post_date_gmt><![CDATA[2026-03-06 10:00:00]]></wp:post_date_gmt>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_lang]]></wp:meta_key><wp:meta_value><![CDATA[en]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_translation_group]]></wp:meta_key><wp:meta_value><![CDATA[10]]></wp:meta_value></wp:postmeta>
	</item>
	<item><title>Bonjour</title><wp:post_id>13</wp:post_id><wp:post_name><![CDATA[bonjour]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[draft]]></wp:status>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_lang]]></wp:meta_key><wp:meta_value><![CDATA[fr]]></wp:meta_value></wp:postmeta>
	</item>
	<item><title>Weird</title><wp:post_id>14</wp:post_id><wp:post_name><![CDATA[weird]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[draft]]></wp:status>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_lang]]></wp:meta_key><wp:meta_value><![CDATA[EN; drop table]]></wp:meta_value></wp:postmeta>
	</item>
</channel>
</rss>`

func TestImportSap13nsExport(t *testing.T) {
	exp, err := Parse(strings.NewReader(sap13nsWXR))
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Redirects) != 5 || exp.Redirects[0] != (Redirect{Source: "/viejo", Target: "/blog/hola", Status: 302}) {
		t.Fatalf("redirects = %+v", exp.Redirects)
	}
	if it := exp.Items[0]; !strings.HasPrefix(it.Markdown, "  Indented") || it.Lang != "es" || it.TranslationGroup != 10 || !it.AssistantExclude {
		t.Fatalf("item = %+v", it)
	}

	sink := newFakeSink()
	rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, Language: "es", Languages: []string{"es", "en"},
		PostURL: func(typ, lang, slug string) string {
			if lang != "" {
				return "/" + lang + defaultPostURL(typ, "", slug)
			}
			return defaultPostURL(typ, "", slug)
		}})
	if err != nil {
		t.Fatal(err)
	}

	holaID, hola := sink.postBySlug(t, TypePost, "hola")
	_, hello := sink.postBySlug(t, TypePost, "hello")
	_, again := sink.postBySlug(t, TypePost, "hello-again")
	_, bonjour := sink.postBySlug(t, TypePost, "bonjour")
	_, weird := sink.postBySlug(t, TypePost, "weird")

	// The original comes first although its translation is older. The
	// Markdown source is kept as is, apart from its addresses: media
	// that was not downloaded loads from the old site, links to imported
	// posts follow them, other links stay.
	want := "  Indented first line.\n\n![pic](https://old.example/uploads/2026/10/pic.png) [hello](/en/blog/hello) [contact](/contacto) ![cdn](https://cdn.example/x.png)\n"
	if hola.Content != want {
		t.Errorf("content = %q, want %q", hola.Content, want)
	}
	if hola.Lang != "" || hola.TranslationOf != 0 || hola.MetaDescription != "Para buscadores" || !hola.AssistantExclude {
		t.Errorf("hola = %+v", hola)
	}
	if hello.Lang != "en" || hello.TranslationOf != holaID || hello.Content != "Back to [hola](/blog/hola)." {
		t.Errorf("hello = %+v", hello)
	}
	if again.Lang != "en" || again.TranslationOf != 0 || !hasWarning(rep, `"Hello again"`, "already has a post in that language") {
		t.Errorf("a second translation into en: %+v, warnings %q", again, rep.Warnings)
	}
	if bonjour.Lang != "fr" || !hasWarning(rep, `"fr"`, "does not offer") {
		t.Errorf("bonjour = %+v, warnings %q", bonjour, rep.Warnings)
	}
	if weird.Lang != "" || !hasWarning(rep, "invalid language") {
		t.Errorf("weird = %+v, warnings %q", weird, rep.Warnings)
	}

	// Term translations, cleaned.
	if tc := sink.term(t, TaxonomyCategory, "noticias"); len(tc.Term.I18n) != 1 || tc.Term.I18n["en"] != (TermText{Name: "News", Description: "All of it"}) {
		t.Errorf("term i18n = %+v", tc.Term.I18n)
	}

	// The redirect rules of the export are off by default: none is made,
	// and the report lists each one.
	if len(sink.redirects) != 0 || rep.Counts.Redirects.Created != 0 || len(rep.Redirects) != 5 {
		t.Errorf("redirects = %v, report %+v", sink.redirects, rep.Redirects)
	}
	for _, r := range rep.Redirects {
		if r.Origin != RedirectRule || r.Reason != SkipDisabled {
			t.Errorf("rule %+v", r)
		}
	}

	// With the option: statuses kept, a 410 has no target, the first rule
	// for a path wins, unknown statuses and non-paths are skipped.
	sink = newFakeSink()
	rep, err = Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, Language: "es", RedirectRules: true,
		PostURL: func(typ, lang, slug string) string {
			if lang != "" {
				return "/" + lang + defaultPostURL(typ, "", slug)
			}
			return defaultPostURL(typ, "", slug)
		}})
	if err != nil {
		t.Fatal(err)
	}
	for from, to := range map[string]string{"/viejo": "302 /blog/hola", "/gone": "410 "} {
		if sink.redirects[from] != to {
			t.Errorf("redirect %s = %q, want %q", from, sink.redirects[from], to)
		}
	}
	if len(sink.redirects) != 2 {
		t.Errorf("redirects = %v", sink.redirects)
	}
	reasons := map[string]string{}
	for _, r := range rep.Redirects {
		reasons[r.Source+" "+r.Target] = r.Reason
	}
	for rule, want := range map[string]string{"/viejo /blog/hola": "", "/gone ": "", "/odd /x": SkipStatus,
		"https://evil.example/ /x": SkipSource, "/viejo /again": SkipDuplicate} {
		if got, ok := reasons[rule]; !ok || got != want {
			t.Errorf("rule %q: reason %q (listed %v), want %q", rule, got, ok, want)
		}
	}
	if c := rep.Counts.Redirects; c.Created != 2 || c.Failed != 0 || c.Skipped != 3 {
		t.Errorf("redirect counts = %+v", c)
	}
}

// TestImportRedirectRules covers what a crafted export may try with its
// redirect rules.
func TestImportRedirectRules(t *testing.T) {
	rule := func(source, target string, status int) Redirect {
		return Redirect{Source: source, Target: target, Status: status}
	}
	exp := &Export{
		Site: Site{Link: "https://old.example", BaseSiteURL: "https://old.example"},
		Items: []Item{{ID: 1, Type: TypePost, Status: "publish", Title: "Hola", Slug: "hola", Link: "https://old.example/2020/hola/",
			Date: time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)}},
		Redirects: []Redirect{
			rule("/a", "https://evil.example/phish", 302),         // off-site
			rule("/b", "//evil.example/phish", 301),               // protocol-relative
			rule("/c", `/\evil.example`, 301),                     // a backslash, which browsers read as a slash
			rule("/d", "/%2F%2Fevil.example", 301),                // "//" once decoded
			rule("/e", "javascript:alert(1)", 301),                // another scheme
			rule("/f", "https://old.example/blog/hola?x=1", 308),  // the exported site: rewritten to its path
			rule("/g", "https://OLD.example:8443/contacto", 307),  // the same host, another port
			rule("/h", "/ok\r\nSet-Cookie: x=1", 301),             // a control character
			rule("/blog/hola", "/elsewhere", 301),                 // the address of an imported post
			rule("/blog/hola/", "/elsewhere", 301),                // a variant of it
			rule("/existing", "/new", 301),                        // a redirect the site has
			rule("/reserved", "/new", 301),                        // refused by the sink
			rule("/i", "", 301),                                   // no target
			rule("/j", "/j", 301),                                 // itself
			rule("/k#frag", "/x", 301),                            // a fragment never reaches the server
			rule("/2020/hola/", "/blog/otra", 301),                // the post's own redirect came first
			rule(`\evil`, "/x", 301),                              // not a path
			rule("/l", "/fine?ref=import#top", 301),               // a local path, kept as is
			rule("/m", "https://old.example.evil.example/x", 301), // a look-alike host
		},
	}
	sink := newFakeSink()
	sink.redirects["/existing"] = "/kept"
	sink.refuse = map[string]error{"/reserved": ErrRedirectReserved}
	rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, RedirectRules: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"/a": SkipOffSite, "/b": SkipOffSite, "/c": SkipOffSite, "/d": SkipOffSite, "/e": SkipOffSite, "/f": "", "/g": "",
		"/h": SkipTarget, "/blog/hola": SkipContent, "/blog/hola/": SkipContent, "/existing": SkipExists, "/reserved": SkipReserved,
		"/i": SkipTarget, "/j": SkipTarget, "/k#frag": SkipSource, "/2020/hola/": SkipDuplicate, `\evil`: SkipSource, "/l": "",
		"/m": SkipOffSite,
	}
	got := map[string]RedirectResult{}
	for _, r := range rep.Redirects {
		if r.Origin == RedirectRule {
			got[r.Source] = r
		}
	}
	for src, reason := range want {
		if r, ok := got[src]; !ok || r.Reason != reason {
			t.Errorf("rule %q: %+v (listed %v), want reason %q", src, r, ok, reason)
		}
	}
	for from, to := range map[string]string{
		"/f": "308 /blog/hola?x=1", "/g": "307 /contacto", "/l": "/fine?ref=import#top", "/existing": "/kept", "/2020/hola/": "/blog/hola",
	} {
		if sink.redirects[from] != to {
			t.Errorf("redirect %s = %q, want %q", from, sink.redirects[from], to)
		}
	}
	if len(sink.redirects) != 5 {
		t.Errorf("redirects = %v", sink.redirects)
	}
	if c := rep.Counts.Redirects; c.Created != 4 || c.Skipped != len(want)-3 || c.Failed != 0 {
		t.Errorf("counts = %+v", c)
	}

	// A dry run asks the sink too, and writes nothing.
	sink = newFakeSink()
	sink.refuse = map[string]error{"/reserved": ErrRedirectReserved}
	rep, err = Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, RedirectRules: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.redirects) != 0 {
		t.Errorf("dry run wrote %v", sink.redirects)
	}
	for _, r := range rep.Redirects {
		if r.Source == "/reserved" && r.Reason != SkipReserved {
			t.Errorf("dry run: %+v", r)
		}
	}

	// The report is bounded.
	exp.Redirects = nil
	for i := range maxRedirectResults + 10 {
		exp.Redirects = append(exp.Redirects, rule(fmt.Sprintf("/r%d", i), "/x", 301))
	}
	rep, _ = Import(context.Background(), exp, newFakeSink(), Options{DefaultAuthorID: 1})
	if len(rep.Redirects) != maxRedirectResults || rep.RedirectsDropped != 10 {
		t.Errorf("report: %d listed, %d dropped", len(rep.Redirects), rep.RedirectsDropped)
	}
}

// attSink is a sink that has the files of the attachments, as the restore
// of a backup has.
type attSink struct {
	*fakeSink
	srcs []string
}

func (s *attSink) StoreAttachment(_ context.Context, it *Item, src string) (int64, string, error) {
	s.srcs = append(s.srcs, src)
	return it.ID, "/uploads/2026/10/pic.png", nil
}

// TestImportKeepAddresses covers the options of a restore: the Markdown
// is stored as it is, no redirects from old addresses are made, private
// posts stay private and the attachments come from the sink.
func TestImportKeepAddresses(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8" ?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel><title>Old</title><link>https://old.example</link><language>en</language><wp:wxr_version>1.2</wp:wxr_version>
	<item><title>pic</title><wp:post_id>5</wp:post_id><wp:post_type><![CDATA[attachment]]></wp:post_type><wp:status><![CDATA[inherit]]></wp:status>
		<wp:attachment_url><![CDATA[https://old.example/uploads/2026/10/pic.png]]></wp:attachment_url></item>
	<item><title>Hello</title><link>https://old.example/blog/hello</link><wp:post_id>10</wp:post_id><wp:post_name><![CDATA[hello]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[publish]]></wp:status><wp:post_date_gmt><![CDATA[2026-03-04 10:00:00]]></wp:post_date_gmt>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_markdown]]></wp:meta_key><wp:meta_value><![CDATA[![pic](/uploads/2026/10/pic.png) [secret](/blog/secret)]]></wp:meta_value></wp:postmeta>
		<wp:postmeta><wp:meta_key><![CDATA[_thumbnail_id]]></wp:meta_key><wp:meta_value><![CDATA[5]]></wp:meta_value></wp:postmeta></item>
	<item><title>Secret</title><link>https://old.example/blog/secret</link><wp:post_id>11</wp:post_id><wp:post_name><![CDATA[secret]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[private]]></wp:status><wp:post_date_gmt><![CDATA[2026-03-05 10:00:00]]></wp:post_date_gmt>
		<wp:postmeta><wp:meta_key><![CDATA[_sap13ns_markdown]]></wp:meta_key><wp:meta_value><![CDATA[Only for us.]]></wp:meta_value></wp:postmeta></item>
	<item><title>Undated</title><wp:post_id>12</wp:post_id><wp:post_name><![CDATA[undated]]></wp:post_name>
		<wp:post_type><![CDATA[post]]></wp:post_type><wp:status><![CDATA[private]]></wp:status></item>
</channel></rss>`
	run := func(keep bool) (*attSink, *Report) {
		t.Helper()
		exp, err := Parse(strings.NewReader(doc))
		if err != nil {
			t.Fatal(err)
		}
		sink := &attSink{fakeSink: newFakeSink()}
		rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, Language: "en", DownloadMedia: true,
			KeepAddresses: keep, KeepPrivate: keep, PostURL: func(typ, lang, slug string) string { return "/blog/" + slug }})
		if err != nil {
			t.Fatal(err)
		}
		return sink, rep
	}

	sink, rep := run(true)
	if len(sink.srcs) != 1 || sink.srcs[0] != "https://old.example/uploads/2026/10/pic.png" || len(sink.media) != 0 {
		t.Fatalf("attachments = %v, downloaded %v", sink.srcs, sink.media)
	}
	if rep.Counts.Media.Created != 1 || rep.Mapping.Media[5] != 5 {
		t.Errorf("media = %+v, mapping %v", rep.Counts.Media, rep.Mapping.Media)
	}
	_, hello := sink.postBySlug(t, TypePost, "hello")
	if hello.Content != "![pic](/uploads/2026/10/pic.png) [secret](/blog/secret)" {
		t.Errorf("content = %q", hello.Content)
	}
	if hello.FeaturedMediaID == nil || *hello.FeaturedMediaID != 5 {
		t.Errorf("featured = %v", hello.FeaturedMediaID)
	}
	_, secret := sink.postBySlug(t, TypePost, "secret")
	if secret.Status != StatusPrivate || secret.PublishedAt == nil || !secret.PublishedAt.Equal(time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("secret = %s %v", secret.Status, secret.PublishedAt)
	}
	if _, undated := sink.postBySlug(t, TypePost, "undated"); undated.Status != StatusPrivate || undated.PublishedAt != nil {
		t.Errorf("undated = %s %v", undated.Status, undated.PublishedAt)
	}
	if len(sink.redirects) != 0 {
		t.Errorf("redirects = %v", sink.redirects)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(w, "private") {
			t.Errorf("warning %q", w)
		}
	}

	// Without the options the same file imports as before: the private
	// posts become drafts.
	sink, _ = run(false)
	if _, secret := sink.postBySlug(t, TypePost, "secret"); secret.Status != StatusDraft {
		t.Errorf("secret = %s", secret.Status)
	}
}
