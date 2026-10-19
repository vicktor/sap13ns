package web

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/export"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/internal/wpimport"
)

func (e *customEnv) setLangs(main string, extra ...string) {
	e.t.Helper()
	ctx := context.Background()
	st, err := e.st.LoadSettings(ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	st.Language, st.Languages = main, extra
	if err := e.st.SaveSettings(ctx, st); err != nil {
		e.t.Fatal(err)
	}
	if err := e.s.ReloadSettings(ctx); err != nil {
		e.t.Fatal(err)
	}
}

func (e *customEnv) userID(name string) int64 {
	e.t.Helper()
	u, err := e.st.UserByUsername(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return u.ID
}

// exportForm posts the export form; fields are added to a full export.
func (e *customEnv) postExport(cookie *http.Cookie, csrf string, fields url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	form := url.Values{"posts": {"1"}, "pages": {"1"}, "media": {"1"}, "published": {"1"}, "format": {"xml"}}
	for k, v := range fields {
		form[k] = v
	}
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/export", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, cookie)
}

func testPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for x := range 16 {
		for y := range 16 {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// seedExport fills a site in Spanish and English with everything the
// export carries, and personal data that is easy to look for.
func seedExport(e *customEnv) {
	t := e.t
	t.Helper()
	ctx := context.Background()
	st := e.st
	e.setLangs("es", "en")
	writer, err := st.CreateUser(ctx, store.NewUser{Username: "writer", Email: "writer@example.com", DisplayName: "Wanda Writer",
		Role: auth.RoleAuthor, PasswordHash: "$argon2id$HASHMARKER"})
	if err != nil {
		t.Fatal(err)
	}
	news, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Noticias", Slug: "noticias", Description: "Todo lo nuevo",
		I18n: map[string]store.TermText{"en": {Name: "News", Description: "Everything new"}}})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Lanzamientos", Slug: "lanzamientos", ParentID: &news})
	if err != nil {
		t.Fatal(err)
	}
	tag, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "tag", Name: "Go & <WASM>", Slug: "go-wasm"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.s.media.Save(ctx, bytes.NewReader(testPNG(t, color.RGBA{200, 30, 30, 255})))
	if err != nil {
		t.Fatal(err)
	}
	img := &store.Media{UploaderID: &writer, Path: res.Path, OriginalName: "red.png", MIME: res.MIME, Size: res.Size, Width: res.Width,
		Height: res.Height, Variants: res.Variants, SHA256: res.SHA256, Alt: "A red square"}
	if img.Variants == nil {
		img.Variants = map[string]store.MediaVariant{}
	}
	if img.ID, err = st.CreateMedia(ctx, img); err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 3, 4, 10, 30, 0, 0, time.UTC)
	es, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Hola & <adiós>", Slug: "hola",
		Lang: "es", Excerpt: "Un saludo ]]> raro", MetaDescription: "Resumen para buscadores", AssistantExclude: true,
		Content:  "## Hola\n\nUna imagen: ![A red square](" + img.URL() + ")\n\n```\nend ]]> here\n```\n\nVer [el equipo](/equipo).\n",
		AuthorID: writer, CommentStatus: "open", PublishedAt: &pub, TermIDs: []int64{sub, tag}, FeaturedMediaID: &img.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusPublished, Title: "Hello", Slug: "hello",
		Lang: "en", TranslationOf: es, Content: "## Hello\n\nIn English.\n", AuthorID: writer, CommentStatus: "closed",
		PublishedAt: &pub, TermIDs: []int64{news}, FeaturedMediaID: &img.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePost, Status: store.StatusDraft, Title: "Borrador", Slug: "borrador",
		Lang: "es", Content: "DRAFTMARKER", AuthorID: e.userID("admin"), CommentStatus: "open"}); err != nil {
		t.Fatal(err)
	}
	about, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePage, Status: store.StatusPublished, Title: "Nosotros", Slug: "nosotros",
		Lang: "es", Content: "Quiénes somos.", AuthorID: writer, CommentStatus: "closed", PublishedAt: &pub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePost(ctx, store.PostInput{Type: store.TypePage, Status: store.StatusPublished, Title: "Equipo", Slug: "equipo",
		Lang: "es", Content: "El equipo.", AuthorID: writer, CommentStatus: "closed", PublishedAt: &pub, ParentID: &about, MenuOrder: 3}); err != nil {
		t.Fatal(err)
	}
	ip := netip.MustParseAddr("192.0.2.44")
	at := pub.Add(time.Hour)
	first, err := st.CreateComment(ctx, store.NewComment{PostID: es, AuthorName: "Rita <Reader>", AuthorEmail: "rita@example.com",
		AuthorURL: "https://rita.example.com", Content: "¡Muy bueno! ]]> & <b>sí</b>", Status: "approved", IP: ip, UserAgent: "UAMARKER", CreatedAt: &at})
	if err != nil {
		t.Fatal(err)
	}
	at2 := at.Add(time.Hour)
	if _, err := st.CreateComment(ctx, store.NewComment{PostID: es, ParentID: &first, UserID: &writer, AuthorName: "Wanda Writer",
		AuthorEmail: "writer@example.com", Content: "Gracias", Status: "approved", IP: ip, CreatedAt: &at2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateComment(ctx, store.NewComment{PostID: es, AuthorName: "Paco", AuthorEmail: "paco@example.com",
		Content: "PENDINGMARKER", Status: "pending", IP: ip, CreatedAt: &at2}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateComment(ctx, store.NewComment{PostID: es, AuthorName: "Spammer", AuthorEmail: "spam@example.com",
		Content: "SPAMMARKER", Status: "spam", IP: ip, CreatedAt: &at2}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Redirect{{Source: "/viejo-hola", Target: "/blog/hola", Status: 301},
		{Source: "/temporal", Target: "https://example.org/x", Status: 302}, {Source: "/borrado", Status: 410}} {
		if err := st.SaveRedirect(ctx, r.Source, r.Target, r.Status, false); err != nil {
			t.Fatal(err)
		}
	}
}

// dirFetcher serves the media of a site from its uploads directory.
type dirFetcher struct{ dir string }

func (f dirFetcher) Fetch(_ context.Context, raw string) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	rel, ok := strings.CutPrefix(u.Path, "/uploads/")
	if !ok || !filepath.IsLocal(rel) {
		return nil, os.ErrNotExist
	}
	return os.Open(filepath.Join(f.dir, filepath.FromSlash(rel)))
}

// fileSHA hashes the stored file of m. Images are re-encoded when they are
// uploaded, so the file a site serves (and an import downloads) is not the
// upload that m.SHA256 identifies.
func fileSHA(e *customEnv, m *store.Media) string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.s.cfg.UploadsDir, filepath.FromSlash(m.Path)))
	if err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestExportRoundTrip exports a site through Tools → Export, imports the
// file into a fresh site with the importer, and compares the two.
func TestExportRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.Schemas(t)
	src := newEnvOn(t, testutil.SiteStore(t, pool, "site_1"))
	seedExport(src)

	rec := src.postExport(src.admin, src.csrf, url.Values{"drafts": {"1"}, "pending_comments": {"1"}, "personal_data": {"1"}})
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	wxr := rec.Body.Bytes()

	dst := newEnvOn(t, testutil.SiteStore(t, pool, "site_2"))
	dst.setLangs("es", "en")
	exp, err := wpimport.Parse(bytes.NewReader(wxr))
	if err != nil {
		t.Fatal(err)
	}
	admin := dst.userID("admin")
	rep, err := wpimport.Import(ctx, exp, &wpSink{s: dst.s, lang: "es", uploader: admin}, wpimport.Options{
		DownloadMedia: true, DefaultAuthorID: admin, MaxMediaBytes: 10 << 20, Language: "es", Languages: dst.s.siteLangs(),
		Fetcher: dirFetcher{src.s.cfg.UploadsDir},
		PostURL: func(typ, l, slug string) string { return dst.s.langPath(cmp.Or(l, "es"), postPath(typ, slug)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) > 0 {
		t.Errorf("import warnings: %q", rep.Warnings)
	}

	// Authors: username and display name; the e-mail with personal data.
	// Never a password: the imported account needs a reset.
	w, err := dst.st.UserByUsername(ctx, "writer")
	if err != nil {
		t.Fatal(err)
	}
	if w.DisplayName != "Wanda Writer" || w.Email != "writer@example.com" || w.PasswordHash != "" {
		t.Errorf("author = %q %q hash %q", w.DisplayName, w.Email, w.PasswordHash)
	}

	// Terms with their hierarchy, descriptions and translations.
	termOf := func(st *store.Store, tax, slug string) *store.Term {
		term, err := st.TermBySlug(ctx, tax, slug)
		if err != nil {
			t.Fatalf("%s %s: %v", tax, slug, err)
		}
		return term
	}
	parentSlug := func(st *store.Store, id *int64) string {
		if id == nil {
			return ""
		}
		term, err := st.TermByID(ctx, *id)
		if err != nil {
			t.Fatal(err)
		}
		return term.Slug
	}
	for _, k := range []struct{ tax, slug string }{{"category", "noticias"}, {"category", "lanzamientos"}, {"tag", "go-wasm"}} {
		a, b := termOf(src.st, k.tax, k.slug), termOf(dst.st, k.tax, k.slug)
		if a.Name != b.Name || a.Description != b.Description || parentSlug(src.st, a.ParentID) != parentSlug(dst.st, b.ParentID) ||
			len(a.I18n) != len(b.I18n) || a.I18n["en"] != b.I18n["en"] {
			t.Errorf("term %s: %+v, imported as %+v", k.slug, a, b)
		}
	}

	// Posts and pages, field by field.
	all := []string{store.StatusPublished, store.StatusDraft, store.StatusPending, store.StatusPrivate}
	var srcPosts []store.Post
	for _, typ := range []string{store.TypePost, store.TypePage} {
		posts, _, err := src.st.ListPosts(ctx, store.PostQuery{Type: typ, Statuses: all, Limit: 100, WithTerms: true, WithMedia: true})
		if err != nil {
			t.Fatal(err)
		}
		srcPosts = append(srcPosts, posts...)
	}
	if len(srcPosts) != 5 {
		t.Fatalf("seeded %d posts", len(srcPosts))
	}
	slugsOf := func(p *store.Post) string {
		var out []string
		for _, term := range p.Terms {
			out = append(out, term.Taxonomy+":"+term.Slug)
		}
		slices.Sort(out)
		return strings.Join(out, ",")
	}
	pageSlug := func(st *store.Store, id *int64) string {
		if id == nil {
			return ""
		}
		p, err := st.PostByID(ctx, *id)
		if err != nil {
			t.Fatal(err)
		}
		return p.Slug
	}
	for i := range srcPosts {
		a := &srcPosts[i]
		got, err := dst.st.PostBySlug(ctx, a.Type, a.Lang, a.Slug)
		if err != nil {
			t.Errorf("%s %s/%s was not imported: %v", a.Type, a.Lang, a.Slug, err)
			continue
		}
		bs, _, err := dst.st.ListPosts(ctx, store.PostQuery{Type: a.Type, Statuses: all, Limit: 100, WithTerms: true, WithMedia: true})
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(bs, func(p store.Post) bool { return p.ID == got.ID })
		b := &bs[i]
		content := a.Content
		if a.Featured != nil && b.Featured != nil {
			content = strings.ReplaceAll(content, a.Featured.URL(), b.Featured.URL())
		}
		for _, c := range []struct {
			field      string
			want, have any
		}{
			{"title", a.Title, b.Title}, {"status", a.Status, b.Status}, {"content", content, b.Content},
			{"excerpt", a.Excerpt, b.Excerpt}, {"meta description", a.MetaDescription, b.MetaDescription},
			{"assistant exclude", a.AssistantExclude, b.AssistantExclude}, {"comments", a.CommentStatus, b.CommentStatus},
			{"menu order", a.MenuOrder, b.MenuOrder}, {"author", a.AuthorUsername, b.AuthorUsername}, {"terms", slugsOf(a), slugsOf(b)},
			{"parent", pageSlug(src.st, a.ParentID), pageSlug(dst.st, b.ParentID)},
			{"published", a.PublishedAt != nil && b.PublishedAt != nil && a.PublishedAt.Equal(*b.PublishedAt) || a.PublishedAt == nil && b.PublishedAt == nil, true},
			{"featured", a.Featured != nil && b.Featured != nil && fileSHA(src, a.Featured) == b.Featured.SHA256 && a.Featured.Alt == b.Featured.Alt ||
				a.Featured == nil && b.Featured == nil, true},
		} {
			if c.want != c.have {
				t.Errorf("%s %s/%s: %s = %#v, want %#v", a.Type, a.Lang, a.Slug, c.field, c.have, c.want)
			}
		}
	}

	// Translations stay linked.
	hola, err := dst.st.PostBySlug(ctx, store.TypePost, "es", "hola")
	if err != nil {
		t.Fatal(err)
	}
	trs, err := dst.st.Translations(ctx, hola)
	if err != nil || len(trs) != 2 || trs[0].Slug != "hello" || trs[0].Lang != "en" {
		t.Errorf("translations of hola: %+v, %v", trs, err)
	}

	// Comments with their threads, statuses and (with personal data)
	// e-mail and IP addresses; never spam, never the user agent.
	comments, _, err := dst.st.ListComments(ctx, store.CommentQuery{PostID: hola.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byText := map[string]store.Comment{}
	for _, c := range comments {
		byText[c.Content] = c
	}
	rita, reply, pending := byText["¡Muy bueno! ]]> & <b>sí</b>"], byText["Gracias"], byText["PENDINGMARKER"]
	if len(comments) != 3 || rita.AuthorName != "Rita <Reader>" || rita.AuthorEmail != "rita@example.com" ||
		rita.AuthorURL != "https://rita.example.com" || rita.IP.String() != "192.0.2.44" || rita.Status != "approved" ||
		!rita.CreatedAt.Equal(time.Date(2026, 3, 4, 11, 30, 0, 0, time.UTC)) || rita.UserAgent != "" {
		t.Errorf("comments: %d; first %+v", len(comments), rita)
	}
	if reply.ParentID == nil || *reply.ParentID != rita.ID || reply.UserID == nil || *reply.UserID != w.ID || pending.Status != "pending" {
		t.Errorf("reply %+v, pending %+v", reply, pending)
	}

	// Redirects keep their status.
	rules, err := dst.st.Redirects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rules {
		got[r.Source] = r.Target + " " + strconv.Itoa(r.Status)
	}
	for src, want := range map[string]string{"/viejo-hola": "/blog/hola 301", "/temporal": "https://example.org/x 302", "/borrado": " 410"} {
		if got[src] != want {
			t.Errorf("redirect %s = %q, want %q (all: %v)", src, got[src], want, got)
		}
	}
}

// TestExportHTTP covers the form: who may export, CSRF, the download
// headers, the audit entry and the personal-data switch.
func TestExportHTTP(t *testing.T) {
	ctx := context.Background()
	e := newCustomEnv(t)
	seedExport(e)

	if rec := e.get("/admin/tools/export", e.admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="personal_data"`) {
		t.Fatalf("export page: %d", rec.Code)
	}
	if rec := e.get("/admin/tools/export", e.author); rec.Code != http.StatusForbidden {
		t.Errorf("author sees the export page: %d", rec.Code)
	}
	if rec := e.postExport(e.author, e.acsrf, nil); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<rss") {
		t.Errorf("author exports: %d", rec.Code)
	}
	if rec := e.postExport(e.admin, "", nil); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<rss") {
		t.Errorf("export without the CSRF token: %d", rec.Code)
	}
	if rec := e.postExport(e.admin, "wrong-token", nil); rec.Code != http.StatusForbidden {
		t.Errorf("export with a wrong CSRF token: %d", rec.Code)
	}
	if rec := e.postExport(e.admin, e.csrf, url.Values{"posts": {""}, "pages": {""}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("export of nothing: %d", rec.Code)
	}

	rec := e.postExport(e.admin, e.csrf, nil)
	body := rec.Body.String()
	h := rec.Header()
	if rec.Code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "application/xml") || h.Get("Cache-Control") != "no-store" ||
		!regexp.MustCompile(`^attachment; filename="sap13ns-example\.test-\d{8}-\d{6}\.xml"$`).MatchString(h.Get("Content-Disposition")) {
		t.Fatalf("download: %d %v", rec.Code, h)
	}
	for _, never := range []string{"@", "192.0.2.", "HASHMARKER", "argon2", "UAMARKER", "DRAFTMARKER", "PENDINGMARKER", "SPAMMARKER"} {
		if strings.Contains(body, never) {
			t.Errorf("default export contains %q", never)
		}
	}
	for _, want := range []string{"<wp:author_login><![CDATA[writer]]>", "Wanda Writer", "_sap13ns_markdown", "<sap13ns:redirect>"} {
		if !strings.Contains(body, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	entries, _, err := e.st.ListAudit(ctx, "export.created", 10, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit: %+v, %v", entries, err)
	}
	if d := entries[0].Details; entries[0].Target != "xml" || d["personal_data"] != "false" || d["types"] != "post,page" ||
		d["statuses"] != "published" || d["delivery"] != "download" || *entries[0].UserID != e.userID("admin") {
		t.Errorf("audit entry: %+v", entries[0])
	}

	// A filtered export: drafts by one author, in a date range.
	rec = e.postExport(e.admin, e.csrf, url.Values{"published": {""}, "drafts": {"1"}, "author_id": {strconv.FormatInt(e.userID("admin"), 10)},
		"from": {"2026-01-01"}, "to": {time.Now().Format("2006-01-02")}, "personal_data": {"1"}})
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, "DRAFTMARKER") || strings.Contains(body, "Hola") ||
		!strings.Contains(body, "admin@example.test") {
		t.Errorf("filtered export: %d", rec.Code)
	}
	if rec := e.postExport(e.admin, e.csrf, url.Values{"from": {"2026-05-01"}, "to": {"2026-04-01"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("reversed dates: %d", rec.Code)
	}

	// A small .zip downloads at once.
	rec = e.postExport(e.admin, e.csrf, url.Values{"format": {"zip"}})
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("zip download: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, export.ZipWXR) || !slices.Contains(names, "posts/es/hola.md") || !slices.Contains(names, "pages/es/equipo.md") ||
		!slices.ContainsFunc(names, func(n string) bool { return strings.HasPrefix(n, "uploads/") && strings.HasSuffix(n, ".png") }) {
		t.Errorf("zip members: %v", names)
	}
}

var downloadLink = regexp.MustCompile(`href="(/admin/tools/export/download/[^"]+)"`)

// TestExportBackground prepares a .zip in the background and checks its
// link: signed, for its user only, single-use, and expiring.
func TestExportBackground(t *testing.T) {
	e := newCustomEnv(t)
	seedExport(e)
	old := exportDirectBytes
	exportDirectBytes = 1
	t.Cleanup(func() { exportDirectBytes = old })

	rec := e.postExport(e.admin, e.csrf, url.Values{"format": {"zip"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/tools/export" {
		t.Fatalf("background export: %d %v", rec.Code, rec.Header())
	}
	deadline := time.Now().Add(30 * time.Second)
	for e.s.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("the export never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
	page := e.get("/admin/tools/export", e.admin).Body.String()
	m := downloadLink.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no download link on the page")
	}
	link := html.UnescapeString(m[1])
	name := strings.TrimPrefix(link[:strings.Index(link, "?")], "/admin/tools/export/download/")
	file := filepath.Join(e.s.exportDir(), name)
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export file: %v %v", info, err)
	}
	if pathInside(e.s.exportDir(), e.s.cfg.UploadsDir) {
		t.Fatal("the export directory is public")
	}
	entries, _, _ := e.st.ListAudit(context.Background(), "export.created", 10, 0)
	if len(entries) != 1 || entries[0].Details["delivery"] != "background" {
		t.Errorf("audit: %+v", entries)
	}

	// Another administrator, a tampered token, no token: refused.
	other, _ := e.login("admin2", auth.RoleAdmin)
	if rec := e.get(link, other); rec.Code != http.StatusNotFound {
		t.Errorf("another admin downloads: %d", rec.Code)
	}
	if rec := e.get(link, e.author); rec.Code != http.StatusForbidden {
		t.Errorf("author downloads: %d", rec.Code)
	}
	if rec := e.get(link, nil); rec.Code == 200 {
		t.Errorf("anonymous download: %d", rec.Code)
	}
	tampered := link[:len(link)-2] + "xx"
	if rec := e.get(tampered, e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("tampered token: %d", rec.Code)
	}
	if rec := e.get(link[:strings.Index(link, "?")], e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("no token: %d", rec.Code)
	}

	rec = e.get(link, e.admin)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); err != nil {
		t.Fatalf("download is not a zip: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("the file is still there after the download: %v", err)
	}
	if rec := e.get(link, e.admin); rec.Code != http.StatusGone {
		t.Errorf("second download: %d", rec.Code)
	}
	if downloadLink.MatchString(e.get("/admin/tools/export", e.admin).Body.String()) {
		t.Error("the page still lists the downloaded export")
	}

	// A link older than export.TTL no longer works, and the file goes.
	created := time.Now().Add(-export.TTL - time.Minute)
	stale := export.NewName(e.userID("admin"), created)
	if err := os.WriteFile(filepath.Join(e.s.exportDir(), stale), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := e.get(e.s.exportURL(stale, created), e.admin); rec.Code != http.StatusGone {
		t.Errorf("expired link: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(e.s.exportDir(), stale)); !os.IsNotExist(err) {
		t.Errorf("the expired file is still there: %v", err)
	}
	// And maintenance removes expired files nobody asked for.
	if err := os.WriteFile(filepath.Join(e.s.exportDir(), stale), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.s.SweepExports(time.Now())
	if _, err := os.Stat(filepath.Join(e.s.exportDir(), stale)); !os.IsNotExist(err) {
		t.Errorf("the sweep left the expired file: %v", err)
	}
}

func TestPathInside(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	if err := os.Mkdir(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "data") // a data directory that is the uploads directory
	if err := os.Symlink(uploads, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		dir, parent string
		want        bool
	}{
		{filepath.Join(root, "exports"), uploads, false},
		{filepath.Join(root, "exports"), root, true},
		{filepath.Join(uploads, "exports"), uploads, true},
		{filepath.Join(link, "exports"), uploads, true},
		{filepath.Join(root, "uploads-2", "exports"), uploads, false},
	} {
		if got := pathInside(c.dir, c.parent); got != c.want {
			t.Errorf("pathInside(%s, %s) = %v", c.dir, c.parent, got)
		}
	}
}
