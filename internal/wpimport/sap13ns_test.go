package wpimport

import (
	"context"
	"strings"
	"testing"
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

	// Redirects: statuses kept, a 410 has no target, the first rule for a
	// path wins, unknown statuses and non-paths are refused.
	for from, to := range map[string]string{"/viejo": "302 /blog/hola", "/gone": "410 "} {
		if sink.redirects[from] != to {
			t.Errorf("redirect %s = %q, want %q", from, sink.redirects[from], to)
		}
	}
	if _, ok := sink.redirects["/odd"]; ok || len(sink.redirects) != 2 || !hasWarning(rep, "/odd", "unknown status 299") {
		t.Errorf("redirects = %v", sink.redirects)
	}
	if c := rep.Counts.Redirects; c.Created != 2 || c.Failed != 1 || c.Skipped != 2 {
		t.Errorf("redirect counts = %+v", c)
	}
}
