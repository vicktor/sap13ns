package plugin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

type sentMail struct{ to, replyTo, subject, body string }

type fakeMailer struct {
	mu   sync.Mutex
	sent []sentMail
}

func (f *fakeMailer) SendMessage(to, replyTo, subject, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMail{to, replyTo, subject, body})
	return nil
}

// wait returns the mails once n have arrived (delivery is asynchronous).
func (f *fakeMailer) wait(t *testing.T, n int) []sentMail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		got := slices.Clone(f.sent)
		f.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			if len(got) != n {
				t.Fatalf("got %d e-mails, want %d", len(got), n)
			}
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func hasLog(p *Plugin, text string) bool {
	return slices.ContainsFunc(p.Logs(), func(l LogLine) bool { return strings.Contains(l.Text, text) })
}

// hooksHost installs testdata/hooks twice: as "hooks" and, with only the
// comment.check hook and the comments:pii permission, as "pii".
func hooksHost(t *testing.T) (*Host, *store.Store, *fakeMailer, int64) {
	t.Helper()
	st := testutil.Store(t)
	dir := t.TempDir()
	src := installPlugin(t, dir, "./internal/plugin/testdata/hooks", "hooks")
	var m map[string]any
	raw, _ := os.ReadFile(filepath.Join(src, "plugin.json"))
	json.Unmarshal(raw, &m)
	m["id"], m["hooks"], m["permissions"] = "pii", []string{"comment.check"}, []string{"comments:pii"}
	delete(m, "shortcodes")
	delete(m, "widgets")
	delete(m, "forms")
	delete(m, "events")
	os.MkdirAll(filepath.Join(dir, "pii"), 0o755)
	raw, _ = json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "pii", "plugin.json"), raw, 0o644)
	wasm, _ := os.ReadFile(filepath.Join(src, "plugin.wasm"))
	os.WriteFile(filepath.Join(dir, "pii", "plugin.wasm"), wasm, 0o644)

	ctx := context.Background()
	h, err := NewHost(ctx, dir, "", st, slog.New(slog.NewTextHandler(io.Discard, nil)),
		func() SiteInfo {
			return SiteInfo{URL: "https://example.test", Title: "Test", Language: "es", Timezone: "Europe/Madrid"}
		}, testLinks{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close(context.Background()) })
	mailer := &fakeMailer{}
	h.SetMailer(mailer)
	h.SetSecrets(auth.NewKeys([]byte("test-secret-key-test-secret-key!")))
	if err := h.Load(ctx); err != nil {
		t.Fatal(err)
	}
	uid := adminUser(t, st)
	for _, id := range []string{"hooks", "pii"} {
		if err := h.Approve(ctx, id, "", uid); err != nil {
			t.Fatal(id, err)
		}
	}
	return h, st, mailer, uid
}

func TestHooks(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a wasm module")
	}
	h, st, mailer, uid := hooksHost(t)
	ctx := context.Background()

	t.Run("comment.check", func(t *testing.T) {
		in := CommentInput{Author: "Visitor", Content: "hold this", Email: "v@example.test", IP: "203.0.113.5", UserAgent: "UA/1"}
		v := h.CheckComment(ctx, in)
		// "hooks" answers first without personal data (pending); "pii" sees
		// the e-mail address and says spam. The strictest verdict wins.
		if v.Verdict != VerdictSpam || v.Plugin != "pii" || v.Reason != "saw v@example.test 203.0.113.5 UA/1" {
			t.Fatalf("verdict %+v", v)
		}
		if !hasLog(h.Get("hooks"), "comment.check: pending by \"Visitor\": held by hooks") || !hasLog(h.Get("pii"), "spam by") {
			t.Fatal("verdicts not logged")
		}
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "hold"}); v.Verdict != VerdictPending || v.Plugin != "hooks" {
			t.Fatalf("pending: %+v", v)
		}
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "timing", Elapsed: 42 * time.Second}); v.Reason != "elapsed 42000" {
			t.Fatalf("elapsed: %+v", v)
		}
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "hello"}); v.Verdict != VerdictOK || v.Plugin != "" {
			t.Fatalf("ok: %+v", v)
		}
		// Unknown verdicts and errors hold the comment for moderation.
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "weird"}); v.Verdict != VerdictPending || v.Plugin != "hooks" ||
			!hasLog(h.Get("hooks"), "unknown verdict maybe") {
			t.Fatalf("unknown verdict: %+v", v)
		}
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "fail"}); v.Verdict != VerdictPending || v.Plugin != "hooks" {
			t.Fatalf("error: %+v", v)
		}
		// A plugin that never answers costs at most the shared budget.
		start := time.Now()
		if v := h.CheckComment(ctx, CommentInput{Author: "V", Content: "loop"}); v.Verdict != VerdictPending {
			t.Fatalf("timeout: %+v", v)
		}
		if d := time.Since(start); d > commentTimeout+time.Second {
			t.Fatalf("comment check took %s", d)
		}
		if !hasLog(h.Get("pii"), "time budget used up") {
			t.Fatal("the second plugin was not skipped")
		}
	})

	t.Run("head.meta", func(t *testing.T) {
		out := h.Head(ctx, &PageInfo{Kind: "home", URL: "https://example.test/", Lang: "es"})
		if len(out.Meta) != 1 || out.Meta[0] != (MetaTag{Name: "author", Content: "Ada"}) {
			t.Fatalf("meta %+v", out.Meta)
		}
		if len(out.JSONLD) != 1 || strings.Contains(out.JSONLD[0], "</script") || strings.Contains(out.JSONLD[0], "<") ||
			!strings.Contains(out.JSONLD[0], esc("</script>")) {
			t.Fatalf("json-ld %q", out.JSONLD)
		}
		var back map[string]string
		if err := json.Unmarshal([]byte(out.JSONLD[0]), &back); err != nil || back["name"] != "</script><script>alert(1)</script>" {
			t.Fatalf("escaped JSON-LD does not decode to the original: %v %v", err, back)
		}
		if !hasLog(h.Get("hooks"), "JSON-LD object dropped") {
			t.Fatal("dropped objects not logged")
		}
	})

	t.Run("forms", func(t *testing.T) {
		html, err := h.Shortcode(ctx, "forms", map[string]string{}, "", nil)
		if err != nil || html != `<div data-form="hooks.contact"></div><p data-blocked="x">p</p>` {
			t.Fatalf("placeholders: %v %q", err, html)
		}
		if _, ok := h.Form("hooks", "contact"); !ok {
			t.Fatal("form not found")
		}
		if _, ok := h.Form("hooks", "nope"); ok {
			t.Fatal("undeclared form found")
		}
		if _, err := h.SubmitForm(ctx, "pii", "contact", nil, "es", ""); err == nil {
			t.Fatal("submitted to a plugin without the form")
		}

		submit := func(fields map[string]string) *FormResult {
			t.Helper()
			res, err := h.SubmitForm(ctx, "hooks", "contact", fields, "es", "https://example.test/contacto")
			if err != nil {
				t.Fatal(err)
			}
			return res
		}
		if res := submit(map[string]string{"email": "v@example.test"}); !res.OK || res.Message != "sent 1 in es" {
			t.Fatalf("submit: %+v", res)
		}
		mails := mailer.wait(t, 1)
		if m := mails[0]; m.to != "a@example.test" || m.replyTo != "v@example.test" || m.subject != "Contact" ||
			!strings.Contains(m.body, `Sent by the plugin "Hooks" (hooks) of https://example.test`) {
			t.Fatalf("mail %+v", m)
		}

		// No relay: only the administrator and addresses saved in email
		// settings. Errors for undeclared fields are dropped.
		res := submit(map[string]string{"email": "v@example.test", "to": "victim@example.org"})
		if res.OK || res.Message != "sent 0: permission denied" || len(res.Errors) != 1 || res.Errors["to"] != "refused" {
			t.Fatalf("relay: %+v", res)
		}
		if !hasLog(h.Get("hooks"), "refused recipient victim@example.org") {
			t.Fatal("refused recipient not logged")
		}
		// A value stored without the e-mail mark (saved while the key had
		// another type, before an update redeclared it) is no recipient.
		if err := st.SavePluginSettings(ctx, "hooks", map[string]string{"copy_to": "copy@example.test"}); err != nil {
			t.Fatal(err)
		}
		if err := h.Load(ctx); err != nil {
			t.Fatal(err)
		}
		if res := submit(map[string]string{"email": "v@example.test", "to": "copy@example.test"}); res.OK {
			t.Fatalf("unmarked recipient: %+v", res)
		}
		if err := h.SaveSettings(ctx, "hooks", map[string]string{"copy_to": "copy@example.test"}); err != nil {
			t.Fatal(err)
		}
		if res := submit(map[string]string{"email": "v@example.test", "to": "COPY@example.test"}); !res.OK {
			t.Fatalf("saved recipient: %+v", res)
		}

		// At most 5 per call, MailPerHour per plugin.
		res = submit(map[string]string{"email": "v@example.test", "n": "6"})
		if res.OK || !strings.HasPrefix(res.Message, "sent 5: ") {
			t.Fatalf("per call: %+v", res)
		}
		total := 7
		for range 10 {
			res := submit(map[string]string{"email": "v@example.test", "n": "5"})
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.Fields(res.Message)[1], ":"))
			total += n
			if !res.OK {
				if !strings.Contains(res.Message, "rate limit") {
					t.Fatalf("limit: %+v", res)
				}
				break
			}
		}
		if total != MailPerHour {
			t.Fatalf("sent %d e-mails, want %d", total, MailPerHour)
		}
		mails = mailer.wait(t, MailPerHour)
		if !hasLog(h.Get("hooks"), "hourly limit") {
			t.Fatal("limit not logged")
		}
		if mails[1].to != "copy@example.test" {
			t.Fatalf("second mail to %s", mails[1].to)
		}
	})

	t.Run("widget config and settings", func(t *testing.T) {
		kind := "plugin:hooks:info"
		if decls := h.WidgetConfig(kind); len(decls) != 3 || h.WidgetConfig("plugin:hooks:nope") != nil || h.WidgetConfig("search") != nil {
			t.Fatalf("widget config %v", decls)
		}
		if _, err := h.CleanWidgetConfig(kind, map[string]string{"count": "many"}); err == nil {
			t.Fatal("accepted a non-number")
		}
		cfg, err := h.CleanWidgetConfig(kind, map[string]string{"count": "7", "show": "yes", "until": "2026-07-01T10:00:00"})
		if err != nil || cfg["count"] != "7" || cfg["show"] != "false" || cfg["until"] != "2026-07-01T10:00" {
			t.Fatalf("clean config: %v %v", err, cfg)
		}
		widget := func(cfg map[string]string, lang string) string {
			t.Helper()
			out, err := h.Widget(ctx, "hooks", "info", cfg, &PageInfo{Kind: "home", Lang: lang})
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		want := "count=7 show=false until=2026-07-01T10:00:00+02:00 lang=en tz=Europe/Madrid starts=2026-03-29T01:30:00+01:00 key= mode=a limit=10"
		if got := widget(cfg, "en"); got != want {
			t.Fatalf("widget\n got %s\nwant %s", got, want)
		}
		// A widget placed before a field existed gets its default.
		if got := widget(nil, "es"); !strings.HasPrefix(got, "count=3 show=false until= lang=es") {
			t.Fatalf("defaults: %s", got)
		}

		// An empty number or select means the default; secrets are
		// encrypted and kept when not submitted.
		if err := h.SaveSettings(ctx, "hooks", map[string]string{"limit": "", "mode": "", "api_key": "s3cret", "starts": "2026-07-01T10:00"}); err != nil {
			t.Fatal(err)
		}
		stored := h.Get("hooks").Record.Settings
		if !strings.HasPrefix(stored["api_key"], secretPrefix) || strings.Contains(stored["api_key"], "s3cret") || stored["copy_to"] != "copy@example.test" {
			t.Fatalf("stored %v", stored)
		}
		if got := widget(cfg, "en"); !strings.HasSuffix(got, "starts=2026-07-01T10:00:00+02:00 key=s3cret mode=a limit=10") {
			t.Fatalf("after save: %s", got)
		}
		if err := h.SaveSettings(ctx, "hooks", map[string]string{"mode": "b"}); err != nil {
			t.Fatal(err)
		}
		if got := widget(cfg, "en"); !strings.HasSuffix(got, "key=s3cret mode=b limit=10") {
			t.Fatalf("secret not kept: %s", got)
		}
		for _, bad := range []map[string]string{{"copy_to": "a@example.test, b@example.test"}, {"starts": "tomorrow"},
			{"mode": "c"}, {"limit": "ten"}, {"site": "http://example.test/"}, {"site": "https://user@example.test/"}} {
			if err := h.SaveSettings(ctx, "hooks", bad); err == nil {
				t.Errorf("accepted %v", bad)
			}
		}
		if err := h.SaveSettings(ctx, "hooks", map[string]string{"site": "https://example.test/"}); err != nil {
			t.Fatal(err)
		}

		// A secret is bound to its plugin and key.
		if err := st.SavePluginSettings(ctx, "pii", map[string]string{"api_key": stored["api_key"]}); err != nil {
			t.Fatal(err)
		}
		h.Load(ctx)
		if v := h.pluginSettings(h.Get("pii"))["api_key"]; v != "" || !hasLog(h.Get("pii"), "cannot decrypt") {
			t.Fatalf("secret moved to another plugin decrypts to %q", v)
		}
	})

	t.Run("restored settings", func(t *testing.T) {
		current := map[string]string{"api_key": "enc1:mine", "copy_to": "copy@example.test", emailMark + "copy_to": "1",
			"mode": "a", "limit": "10", "site": "https://example.test/"}
		restored := map[string]string{"api_key": "enc1:theirs", "copy_to": "thief@example.org", emailMark + "copy_to": "1",
			"mode": "c", "limit": "20", "site": "http://127.0.0.1/", "starts": "2026-07-01T10:00", "undeclared": "x"}
		out, refused, ok := h.RestoreSettings("hooks", restored, current)
		if !ok {
			t.Fatal("hooks is installed")
		}
		want := map[string]string{"api_key": "enc1:mine", "copy_to": "copy@example.test", emailMark + "copy_to": "1",
			"mode": "a", "limit": "20", "site": "https://example.test/", "starts": "2026-07-01T10:00"}
		if !maps.Equal(out, want) || !slices.Equal(refused, []string{"Mode", "Site"}) {
			t.Fatalf("restored %v, refused %v", out, refused)
		}
		if _, _, ok := h.RestoreSettings("not-installed", restored, current); ok {
			t.Error("a plugin that is not installed")
		}
		if !h.WidgetKnown("plugin:hooks:info") || h.WidgetKnown("plugin:hooks:nope") || h.WidgetKnown("text") {
			t.Error("WidgetKnown")
		}
	})

	t.Run("posts_query", func(t *testing.T) {
		cat, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "News", Slug: "news"})
		if err != nil {
			t.Fatal(err)
		}
		tag, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "tag", Name: "Go", Slug: "go"})
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]int64{}
		for i, p := range []struct {
			title, lang, status string
			terms               []int64
		}{
			{"Uno", "es", store.StatusPublished, []int64{cat, tag}},
			{"Dos", "es", store.StatusPublished, []int64{cat}},
			{"One", "en", store.StatusPublished, []int64{cat, tag}},
			{"Borrador", "es", store.StatusDraft, []int64{cat, tag}},
			{"Otro", "es", store.StatusPublished, nil},
		} {
			at := time.Now().Add(-time.Duration(i+1) * time.Hour)
			id, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: p.status, Title: p.title, Slug: strings.ToLower(p.title),
				Lang: p.lang, Content: "x", AuthorID: uid, CommentStatus: "open", PublishedAt: &at, TermIDs: p.terms})
			if err != nil {
				t.Fatal(err)
			}
			ids[p.title] = id
		}
		query := func(attrs map[string]string) []string {
			t.Helper()
			out, err := h.Shortcode(ctx, "posts", attrs, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if out == "" {
				return nil
			}
			got := strings.Split(out, ",")
			slices.Sort(got)
			return got
		}
		for _, tc := range []struct {
			attrs map[string]string
			want  []string
		}{
			{map[string]string{"category": "news"}, []string{"Dos/es", "One/en", "Uno/es"}},
			{map[string]string{"category": "news", "tag": "go"}, []string{"One/en", "Uno/es"}},
			{map[string]string{"category": "news", "lang": "es"}, []string{"Dos/es", "Uno/es"}},
			{map[string]string{"category": "news", "exclude": strconv.FormatInt(ids["Uno"], 10) + " " + strconv.FormatInt(ids["One"], 10)}, []string{"Dos/es"}},
			{map[string]string{"category": "missing"}, nil},
			{map[string]string{"lang": "en"}, []string{"One/en"}},
		} {
			if got := query(tc.attrs); !slices.Equal(got, tc.want) {
				t.Errorf("%v: got %v, want %v", tc.attrs, got, tc.want)
			}
		}
		if _, err := h.Shortcode(ctx, "posts", map[string]string{"lang": "EN; drop"}, "", nil); err == nil {
			t.Error("accepted an invalid language")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		if err := h.Disable(ctx, "hooks"); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.Form("hooks", "contact"); ok {
			t.Fatal("form of a disabled plugin")
		}
		if _, err := h.Widget(ctx, "hooks", "info", nil, &PageInfo{}); err == nil {
			t.Fatal("widget of a disabled plugin")
		}
		if h.HasShortcode("posts") || len(h.Head(ctx, &PageInfo{}).Meta) != 0 {
			t.Fatal("disabled plugin still active")
		}
	})
}

// esc writes s the way json.HTMLEscape does.
func esc(s string) string {
	return strings.NewReplacer("<", "\\u003c", ">", "\\u003e", "&", "\\u0026").Replace(s)
}

func TestSafeJSONLD(t *testing.T) {
	for in, want := range map[string]string{
		`{"name": "</script>&<!--"}`: `{"name":"` + esc("</script>&<!--") + `"}`,
		` {"a": [1, 2]} `:            `{"a":[1,2]}`,
		`[1, 2]`:                     "",
		`"text"`:                     "",
		`{"a": `:                     "",
		`{"big": "` + strings.Repeat("x", maxJSONLDBytes) + `"}`: "",
	} {
		got, ok := safeJSONLD(json.RawMessage(in))
		if got != want || ok != (want != "") {
			t.Errorf("safeJSONLD(%.40q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestQualifyForms(t *testing.T) {
	p := &Plugin{Manifest: Manifest{ID: "contact", Forms: []FormDecl{{ID: "main"}}}}
	// A content filter may keep the placeholders it received, place its own
	// forms, and nothing else.
	input := `<p>x</p><div data-form="other.form"></div>`
	out := qualifyForms(p, `<div data-form="other.form"></div><div data-form="evil.steal"></div><div data-form="main"></div>`+
		`<div data-form="nope"></div><span DATA-FORM="x"></span>`, input)
	want := `<div data-form="other.form"></div><div data-form="contact.main"></div><span data-blocked="x"></span>`
	if out != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	if out := qualifyForms(p, "<p>no forms</p>", ""); out != "<p>no forms</p>" {
		t.Fatal(out)
	}
}

func TestAssetsAndCSP(t *testing.T) {
	st := testutil.Store(t)
	dir := t.TempDir()
	pdir := filepath.Join(dir, "embed")
	files := map[string]string{
		"plugin.json": `{"abi": 1, "id": "embed", "name": "Embed", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["embed"],
			"permissions": ["assets:js"], "assets": {"css": ["assets/embed.css"], "js": ["assets/embed.js"]},
			"csp": {"frame-src": ["https://www.youtube-nocookie.com"], "img-src": ["https://i.ytimg.com"]}}`,
		"plugin.wasm":      string(respond(okHTML).bytes()),
		"assets/embed.css": ".embed{aspect-ratio:16/9}",
		"assets/embed.js":  "document.querySelectorAll('.embed')",
		"assets/logo.svg":  "<svg></svg>",
	}
	for name, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(pdir, name)), 0o755)
		os.WriteFile(filepath.Join(pdir, name), []byte(body), 0o644)
	}
	h := newTestHost(t, st, dir)
	ctx := context.Background()
	p := h.Get("embed")
	if css, js := h.AssetLinks(); len(css)+len(js) != 0 || len(h.CSPSources()) != 0 {
		t.Fatal("assets or CSP before approval")
	}
	if _, _, ok := h.Asset("embed", p.AssetsVersion(), "assets/embed.css"); ok {
		t.Fatal("asset served before approval")
	}
	want := []string{"assets:js", "csp:frame-src:https://www.youtube-nocookie.com", "csp:img-src:https://i.ytimg.com"}
	if got := p.EffectivePermissions(); !slices.Equal(got, want) {
		t.Fatalf("effective permissions %v", got)
	}
	gen := h.Generation()
	if err := h.Approve(ctx, "embed", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	if h.Generation() == gen {
		t.Fatal("generation unchanged after approval")
	}
	p = h.Get("embed")
	v := p.AssetsVersion()
	css, js := h.AssetLinks()
	if !slices.Equal(css, []string{"/plugin-assets/embed/" + v + "/assets/embed.css"}) || !slices.Equal(js, []string{"/plugin-assets/embed/" + v + "/assets/embed.js"}) {
		t.Fatalf("links %v %v", css, js)
	}
	if b, ctype, ok := h.Asset("embed", v, "assets/embed.css"); !ok || string(b) != files["assets/embed.css"] || ctype != "text/css; charset=utf-8" {
		t.Fatalf("asset: %v %q", ok, ctype)
	}
	if _, ctype, ok := h.Asset("embed", v, "assets/logo.svg"); !ok || ctype != "image/svg+xml" {
		t.Fatal("svg asset")
	}
	for _, tc := range [][2]string{{"0123456789abcdef", "assets/embed.css"}, {v, "assets/missing.css"}, {v, "plugin.json"}, {v, "assets/../plugin.json"}} {
		if _, _, ok := h.Asset("embed", tc[0], tc[1]); ok {
			t.Errorf("served %v", tc)
		}
	}
	csp := h.CSPSources()
	if !slices.Equal(csp["frame-src"], []string{"https://www.youtube-nocookie.com"}) || !slices.Equal(csp["img-src"], []string{"https://i.ytimg.com"}) {
		t.Fatalf("csp %v", csp)
	}

	// Changing an asset stops the plugin until it is approved again.
	os.WriteFile(filepath.Join(pdir, "assets", "embed.js"), []byte("fetch('/steal?c='+document.cookie)"), 0o644)
	h.Load(ctx)
	if h.Get("embed").Status != StatusModified || len(h.CSPSources()) != 0 {
		t.Fatalf("status after changing an asset: %s", h.Get("embed").Status)
	}
	if css, js := h.AssetLinks(); len(css)+len(js) != 0 {
		t.Fatal("assets of a modified plugin still linked")
	}
}

func TestPermissionRisk(t *testing.T) {
	for perm, want := range map[string]string{
		"kv": "low", "posts:read": "low", "mail": "medium", "http:api.example.com": "medium", "http:*.example.com": "medium",
		"http:*": "high", "comments:pii": "high", "assets:js": "high", "csp:frame-src:https://www.youtube-nocookie.com": "high",
	} {
		if got := PermissionRisk(perm); got != want {
			t.Errorf("PermissionRisk(%q) = %q, want %q", perm, got, want)
		}
	}
	if !HighRisk([]string{"kv", "assets:js"}) || HighRisk([]string{"kv", "mail"}) {
		t.Error("HighRisk")
	}
}

// A script under another spelling of .js is still JavaScript and needs
// assets:js: the check follows the served content type, not the name.
func TestAssetScriptNeedsPermission(t *testing.T) {
	st := testutil.Store(t)
	dir := t.TempDir()
	pdir := filepath.Join(dir, "nojs")
	files := map[string]string{
		"plugin.json":  `{"abi": 1, "id": "nojs", "name": "No JS", "version": "1.0.0", "hooks": ["shortcode"], "shortcodes": ["nojs"]}`,
		"plugin.wasm":  string(respond(okHTML).bytes()),
		"assets/a.JS":  "alert(1)",
		"assets/b.css": "a{}",
	}
	for name, body := range files {
		os.MkdirAll(filepath.Dir(filepath.Join(pdir, name)), 0o755)
		os.WriteFile(filepath.Join(pdir, name), []byte(body), 0o644)
	}
	h := newTestHost(t, st, dir)
	if err := h.Approve(context.Background(), "nojs", "", adminUser(t, st)); err != nil {
		t.Fatal(err)
	}
	v := h.Get("nojs").AssetsVersion()
	if _, ctype, ok := h.Asset("nojs", v, "assets/a.JS"); ok {
		t.Fatalf("script served without assets:js (%s)", ctype)
	}
	if _, _, ok := h.Asset("nojs", v, "assets/b.css"); !ok {
		t.Fatal("stylesheet not served")
	}
}

func TestManifestRules(t *testing.T) {
	base := func(fields string) string {
		return `{"abi": 1, "id": "m1", "name": "M", "version": "1.0.0", ` + fields + `}`
	}
	sc := `"hooks": ["shortcode"], "shortcodes": ["m"]`
	for _, tc := range []struct {
		name, manifest string
		ok             bool
	}{
		{"minimal", base(sc), true},
		{"trailing whitespace", base(sc) + "\n\n", true},
		{"second object", base(sc) + `{"id": "other"}`, false},
		{"unknown field", base(sc + `, "permisions": ["kv"]`), false},
		{"select default", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "select", "choices": ["a"], "default": "b"}]`), false},
		{"select without choices", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "select"}]`), false},
		{"select choice with a newline", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "select", "choices": ["a\nb"]}]`), false},
		{"empty select choice", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "select", "choices": [""]}]`), false},
		{"no label", base(sc + `, "settings": [{"key": "s", "type": "text"}]`), false},
		{"duplicate key", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "text"}, {"key": "s", "label": "T", "type": "text"}]`), false},
		{"secret default", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "secret", "default": "x"}]`), false},
		{"datetime", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "datetime", "default": "2026-01-01T09:00"}]`), true},
		{"datetime rfc3339", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "datetime", "default": "2026-01-01T09:00:00Z"}]`), true},
		{"bad datetime", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "datetime", "default": "soon"}]`), false},
		{"email", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "email"}]`), true},
		{"email default", base(sc + `, "settings": [{"key": "s", "label": "S", "type": "email", "default": "x@example.test"}]`), false},
		{"widget config", base(`"hooks": ["widget"], "widgets": [{"id": "w", "title": "W", "config": [{"key": "n", "label": "N", "type": "number", "default": "3"}]}]`), true},
		{"widget secret", base(`"hooks": ["widget"], "widgets": [{"id": "w", "title": "W", "config": [{"key": "n", "label": "N", "type": "secret"}]}]`), false},
		{"js without permission", base(sc + `, "assets": {"js": ["assets/a.js"]}`), false},
		{"asset outside assets", base(sc + `, "assets": {"css": ["a.css"]}`), false},
		{"asset traversal", base(sc + `, "assets": {"css": ["assets/../a.css"]}`), false},
		{"csp", base(sc + `, "csp": {"frame-src": ["https://www.youtube-nocookie.com", "https://*.vimeo.com"]}`), true},
		{"csp script-src", base(sc + `, "csp": {"script-src": ["https://cdn.example.com"]}`), false},
		{"csp http", base(sc + `, "csp": {"img-src": ["http://images.example.com"]}`), false},
		{"csp keyword", base(sc + `, "csp": {"img-src": ["'unsafe-inline'"]}`), false},
		{"csp any host", base(sc + `, "csp": {"img-src": ["https:"]}`), false},
		{"form", base(`"hooks": ["form.submit"], "forms": [{"id": "f", "fields": [{"name": "e", "label": "E", "type": "email"}]}]`), true},
		{"form without fields", base(`"hooks": ["form.submit"], "forms": [{"id": "f", "fields": []}]`), false},
		{"form without hook", base(sc + `, "forms": [{"id": "f", "fields": [{"name": "e", "label": "E", "type": "email"}]}]`), false},
		{"form field type", base(`"hooks": ["form.submit"], "forms": [{"id": "f", "fields": [{"name": "e", "label": "E", "type": "file"}]}]`), false},
	} {
		_, err := ParseManifest([]byte(tc.manifest))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}
	var many []string
	for i := range 51 {
		many = append(many, `{"key": "k`+strconv.Itoa(i)+`", "label": "K", "type": "text"}`)
	}
	if _, err := ParseManifest([]byte(base(sc + `, "settings": [` + strings.Join(many, ",") + `]`))); err == nil {
		t.Error("accepted 51 settings")
	}
}

func TestParseSettingTime(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no time zone database")
	}
	for in, want := range map[string]string{
		"2026-01-15T09:30":          "2026-01-15T09:30:00+01:00",
		"2026-07-15T09:30:45":       "2026-07-15T09:30:45+02:00",
		"2026-07-15T09:30:00Z":      "2026-07-15T09:30:00Z",
		"2026-07-15T09:30:00-05:00": "2026-07-15T09:30:00-05:00",
	} {
		got, err := ParseSettingTime(in, madrid)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("ParseSettingTime(%q) = %v, %v; want %s", in, got.Format(time.RFC3339), err, want)
		}
	}
	if _, err := ParseSettingTime("15/07/2026", madrid); err == nil {
		t.Error("accepted a local date format")
	}
}

func TestAllowedMeta(t *testing.T) {
	for _, tc := range []struct {
		m  MetaTag
		ok bool
	}{
		{MetaTag{Name: "description", Content: "x"}, true},
		{MetaTag{Name: "Author", Content: "x"}, true},
		{MetaTag{Name: "twitter:card", Content: "summary"}, true},
		{MetaTag{Property: "og:image", Content: "https://e.test/i.png"}, true},
		{MetaTag{Property: "article:published_time", Content: "2026-01-01"}, true},
		{MetaTag{Name: "google-site-verification", Content: "token"}, false},
		{MetaTag{Name: "msvalidate.01", Content: "token"}, false},
		{MetaTag{Name: "referrer", Content: "unsafe-url"}, false},
		{MetaTag{Name: "robots", Content: "noindex"}, false},
		{MetaTag{Name: "viewport", Content: "width=1"}, false},
		{MetaTag{Property: "fb:admins", Content: "1"}, false},
		{MetaTag{Name: "twitter:", Content: "x"}, false},
		{MetaTag{Name: "og:title", Content: "x"}, false}, // og:* is a property
		{MetaTag{Name: "description", Property: "og:title", Content: "x"}, false},
		{MetaTag{Name: "description", Content: strings.Repeat("x", 1001)}, false},
	} {
		if got := allowedMeta(tc.m); got != tc.ok {
			t.Errorf("%+v: got %v", tc.m, got)
		}
	}
}

func TestOneLine(t *testing.T) {
	if got := oneLine("a\x00b\nc\u0085d", 300); got != "a b c d" {
		t.Errorf("oneLine = %q", got)
	}
	if got := oneLine("ñññ", 3); got != "ñ" {
		t.Errorf("oneLine cut = %q", got)
	}
}

func TestFetchLimit(t *testing.T) {
	if d, ok := fetchLimit(context.Background(), 0); !ok || d != 0 {
		t.Errorf("no deadline, no timeout: %v %v", d, ok)
	}
	if d, ok := fetchLimit(context.Background(), 800); !ok || d != 800*time.Millisecond {
		t.Errorf("timeout only: %v %v", d, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if d, ok := fetchLimit(ctx, 0); !ok || d > 2*time.Second-fetchMargin || d < time.Second {
		t.Errorf("deadline: %v %v", d, ok)
	}
	if d, ok := fetchLimit(ctx, 300); !ok || d != 300*time.Millisecond {
		t.Errorf("shorter timeout: %v %v", d, ok)
	}
	short, cancel2 := context.WithTimeout(context.Background(), fetchMargin/2)
	defer cancel2()
	if _, ok := fetchLimit(short, 0); ok {
		t.Error("a request was allowed with no time left")
	}
}
