package web

import (
	"bytes"
	"context"
	"html"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/wpimport"
)

func TestReservedRedirectSource(t *testing.T) {
	for _, src := range []string{
		// The home page and the language roots.
		"/", "/en", "/en/", "/es/", "/pt-br/",
		// Sign-in and the other account pages.
		"/login", "/login/2fa", "/LOGIN", "/logout", "/register", "/setup", "/forgot-password", "/reset-password?token=x",
		// The admin, also spelled otherwise.
		"/admin", "/admin/", "/admin/tools/redirects", "/%61dmin/", "/x/../admin", "/en/admin",
		// Single sign-on, the APIs, billing.
		"/sso/start", "/sso/callback", "/sso/authorize", "/api/assistant", "/mcp", "/billing/webhook",
		// Feeds, sitemaps, robots.txt, llms.txt.
		"/feed", "/feed/", "/feed.xml", "/feed.atom", "/feed.json", "/en/feed.xml", "/category/news/feed.xml", "/author/ana/feed.json",
		"/sitemap.xml", "/sitemap_index.xml", "/robots.txt", "/llms.txt", "/llms-full.txt", "/manifest.webmanifest",
		// /.well-known/, assets and uploads.
		"/.well-known/webauthn", "/.well-known/acme-challenge/x", "/static/admin.css", "/themes/default/static/theme.css",
		"/plugin-assets/x/1/a.js", "/plugin-forms/x/y", "/og/post-1.png", "/uploads/2026/10/x.png",
		// The health check, search, the assistant, the blog index.
		"/healthz", "/search", "/search?q=x", "/ask", "/blog", "/blog/", "/en/blog", "/en/search",
	} {
		if !reservedRedirectSource(src) {
			t.Errorf("%q is not reserved", src)
		}
	}
	for _, src := range []string{
		"/?p=12", "/en/?p=12", "/2019/05/old-post/", "/blog/old-slug", "/en/blog/old-slug", "/category/old", "/old-page",
		"/feedback", "/searching", "/administrators", "/blog/feed-reader", "/wp-login.php", "/llms", "/uploads-old/x",
	} {
		if reservedRedirectSource(src) {
			t.Errorf("%q is reserved", src)
		}
	}
}

func TestCheckRedirectTargets(t *testing.T) {
	for _, c := range []struct {
		target string
		local  bool
	}{
		{"/blog/x", true}, {"/x?y=1#z", true}, {"//evil.example", false}, {`/\evil.example`, false}, {"/%2F%2Fevil.example", false},
		{"/%5Cevil.example", false}, {"https://evil.example", false}, {"javascript:alert(1)", false}, {"/ok%0d%0aSet-Cookie:x", false},
	} {
		if got := localRedirectTarget(c.target); got != c.local {
			t.Errorf("localRedirectTarget(%q) = %v", c.target, got)
		}
	}
	// The redirects page accepts http(s) targets, never one a browser reads
	// as another host.
	for target, ok := range map[string]bool{"https://example.org/x": true, "/x": true, `/\evil.example`: false,
		"//evil.example": false, "javascript:alert(1)": false, "/x\r\nSet-Cookie: a=b": false} {
		if _, _, err := checkRedirect("/old", target, 301); (err == nil) != ok {
			t.Errorf("checkRedirect(/old, %q): %v", target, err)
		}
	}
}

// postRedirect saves a rule through the redirects page.
func (e *customEnv) postRedirect(source, target string, status int) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{"csrf": {e.csrf}, "source": {source}, "target": {target}, "status": {strconv.Itoa(status)}}
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/redirects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, e.admin)
}

func TestAdminRedirectsRefuseReservedSources(t *testing.T) {
	e := newCustomEnv(t)
	for _, src := range []string{"/", "/login", "/admin/x", "/feed.xml", "/sitemap.xml", "/.well-known/x", "/uploads/a.png", "/en/"} {
		if rec := e.postRedirect(src, "/elsewhere", 301); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("redirect from %s: %d", src, rec.Code)
		}
	}
	if rec := e.postRedirect("/old", "/new", 301); rec.Code != http.StatusSeeOther {
		t.Fatalf("redirect: %d %s", rec.Code, rec.Body)
	}
	// A second rule for the same source does not replace the first.
	if rec := e.postRedirect("/old", "/other", 302); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("second rule: %d", rec.Code)
	}
	if rules, _ := e.st.Redirects(context.Background()); len(rules) != 1 || rules[0].Target != "/new" || rules[0].Status != 301 {
		t.Errorf("rules = %+v", rules)
	}
}

// TestImportRedirectSafety imports crafted redirect rules into a site that
// has posts, pages and redirects of its own.
func TestImportRedirectSafety(t *testing.T) {
	ctx := context.Background()
	e := newCustomEnv(t)
	e.setLangs("es", "en")
	admin := e.userID("admin")
	pub := time.Now().Add(-time.Hour)
	for _, p := range []store.PostInput{
		{Type: store.TypePost, Status: store.StatusPublished, Title: "Hola", Slug: "hola", Lang: "es", AuthorID: admin, CommentStatus: "open", PublishedAt: &pub},
		{Type: store.TypePage, Status: store.StatusPublished, Title: "Contacto", Slug: "contacto", Lang: "es", AuthorID: admin, CommentStatus: "closed", PublishedAt: &pub},
	} {
		if _, err := e.st.CreatePost(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range []string{"/viejo", "/antiguo/"} {
		if err := e.st.SaveRedirect(ctx, src, "/blog/hola", 301, false); err != nil {
			t.Fatal(err)
		}
	}
	rule := func(source, target string, status int) wpimport.Redirect {
		return wpimport.Redirect{Source: source, Target: target, Status: status}
	}
	want := map[string]string{
		"/viejo": wpimport.SkipExists, "/antiguo": wpimport.SkipExists, // the site's own redirects, also without its slash
		"/blog/hola": wpimport.SkipContent, "/contacto/": wpimport.SkipContent, "/en/blog/hello": wpimport.SkipContent,
		"/a": wpimport.SkipOffSite, "/b": wpimport.SkipOffSite, "/c": wpimport.SkipOffSite,
		"/d": "", "/e": "", "/f": "",
	}
	exp := &wpimport.Export{
		Site: wpimport.Site{Link: "https://old.example", Language: "es"},
		Items: []wpimport.Item{{ID: 7, Type: wpimport.TypePost, Status: "publish", Title: "Hello", Slug: "hello", Lang: "en",
			Link: "https://old.example/en/blog/hello", Date: pub}},
		Redirects: []wpimport.Redirect{
			rule("/viejo", "/blog/otra", 302), rule("/antiguo", "/blog/otra", 302),
			rule("/blog/hola", "/x", 301), rule("/contacto/", "/x", 301), rule("/en/blog/hello", "/x", 301),
			rule("/a", "https://evil.example/phish", 302), rule("/b", "//evil.example", 301), rule("/c", `/\evil.example`, 301),
			rule("/d", "https://old.example/contacto", 301), rule("/e", "", 410), rule("/f", "/blog/hola", 308),
		},
	}
	for _, src := range []string{"/", "/en/", "/login", "/logout", "/admin", "/admin/x", "/sso/start", "/api/assistant", "/mcp",
		"/feed", "/feed.xml", "/en/feed.json", "/sitemap.xml", "/robots.txt", "/llms.txt", "/.well-known/webauthn", "/static/a.css",
		"/themes/x/static/a.css", "/uploads/a.png", "/healthz", "/search", "/ask"} {
		exp.Redirects = append(exp.Redirects, rule(src, "/x", 301))
		want[src] = wpimport.SkipReserved
	}
	opts := func(edit func(*wpimport.Options)) wpimport.Options {
		o := wpimport.Options{DefaultAuthorID: admin, Language: "es", Languages: e.s.siteLangs(),
			PostURL: func(typ, l, slug string) string { return e.s.langPath(l, postPath(typ, slug)) }}
		if edit != nil {
			edit(&o)
		}
		return o
	}
	count := func() int {
		rules, err := e.st.Redirects(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(rules)
	}
	reasons := func(rep *wpimport.Report) map[string]string {
		out := map[string]string{}
		for _, r := range rep.Redirects {
			if r.Origin == wpimport.RedirectRule {
				out[r.Source] = r.Reason
			}
		}
		return out
	}

	// A dry run asks the site, and writes nothing.
	rep, err := wpimport.Import(ctx, exp, &wpSink{s: e.s, lang: "es", uploader: admin},
		opts(func(o *wpimport.Options) { o.DryRun, o.RedirectRules = true, true }))
	if err != nil {
		t.Fatal(err)
	}
	if got := reasons(rep); got["/login"] != wpimport.SkipReserved || got["/viejo"] != wpimport.SkipExists || got["/d"] != "" || count() != 2 {
		t.Errorf("dry run: %v, %d redirects", got, count())
	}

	rep, err = wpimport.Import(ctx, exp, &wpSink{s: e.s, lang: "es", uploader: admin}, opts(func(o *wpimport.Options) { o.RedirectRules = true }))
	if err != nil {
		t.Fatal(err)
	}
	got := reasons(rep)
	for src, reason := range want {
		if r, ok := got[src]; !ok || r != reason {
			t.Errorf("rule %s: reason %q (listed %v), want %q", src, r, ok, reason)
		}
	}
	rules, err := e.st.Redirects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	made := map[string]string{}
	for _, r := range rules {
		made[r.Source] = strconv.Itoa(r.Status) + " " + r.Target
	}
	for src, w := range map[string]string{"/viejo": "301 /blog/hola", "/antiguo/": "301 /blog/hola", "/d": "301 /contacto", "/e": "410 ", "/f": "308 /blog/hola"} {
		if made[src] != w {
			t.Errorf("redirect %s = %q, want %q (all %v)", src, made[src], w, made)
		}
	}
	if len(rules) != 5 {
		t.Errorf("redirects: %v", made)
	}
	if c := rep.Counts.Redirects; c.Created != 3 || c.Failed != 0 || c.Skipped != len(want)-3 {
		t.Errorf("counts %+v", c)
	}
}

// TestImportRedirectRulesOffByDefault posts the import form as a browser
// does, without touching the new switch.
func TestImportRedirectRulesOffByDefault(t *testing.T) {
	e := newCustomEnv(t)
	page := e.get("/admin/tools/import", e.admin).Body.String()
	if !strings.Contains(page, `name="redirect_rules" value="1">`) {
		t.Fatalf("the import form has no redirect rules switch, or it is on")
	}
	const wxr = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:wp="http://wordpress.org/export/1.2/" xmlns:sap13ns="https://sap13ns.site/export/1.0/"><channel>
<title>Old</title><link>https://old.example</link><wp:wxr_version>1.2</wp:wxr_version>
<sap13ns:redirect><sap13ns:source>/old</sap13ns:source><sap13ns:target>/new</sap13ns:target><sap13ns:status>301</sap13ns:status></sap13ns:redirect>
</channel></rss>`
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range map[string]string{"csrf": e.csrf, "author_id": strconv.FormatInt(e.userID("admin"), 10)} {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("wxr", "site.xml")
	fw.Write([]byte(wxr))
	mw.Close()
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if rec := e.do(req, e.admin); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(30 * time.Second)
	for e.s.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the import never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rules, _ := e.st.Redirects(context.Background()); len(rules) != 0 {
		t.Errorf("rules imported without the option: %+v", rules)
	}
	page = e.get("/admin/tools/import", e.admin).Body.String()
	if !strings.Contains(page, html.EscapeString(e.s.redirectResult(wpimport.SkipDisabled, false))) || !strings.Contains(page, "/old") {
		t.Errorf("the report does not list the rule as not imported")
	}
	entries, _, _ := e.st.ListAudit(context.Background(), "import.started", 1, 0)
	if len(entries) != 1 || entries[0].Details["redirect_rules"] != "false" {
		t.Errorf("audit: %+v", entries)
	}
}
