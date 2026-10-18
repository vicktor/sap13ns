package wpimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type termCall struct {
	Taxonomy string
	Term     Term
	ParentID *int64
	ID       int64
}

type commentCall struct {
	PostID   int64
	Comment  Comment
	ParentID *int64
	ID       int64
}

type mediaCall struct {
	Name, Alt, Data string
	ID              int64
}

// fakeSink records every call and hands out sequential IDs.
type fakeSink struct {
	next      int64
	users     []Author
	terms     []termCall
	posts     map[int64]Post
	postOrder []int64
	comments  []commentCall
	media     []mediaCall
	redirects map[string]string
	exists    map[string]bool // "type/slug"
	checks    int

	failPost  func(Post) error
	failMedia func(name string) error
	onPost    func(n int)
}

func newFakeSink() *fakeSink {
	return &fakeSink{posts: map[int64]Post{}, redirects: map[string]string{}, exists: map[string]bool{}}
}

func (s *fakeSink) id() int64 { s.next++; return s.next }

func (s *fakeSink) writes() int {
	return len(s.users) + len(s.terms) + len(s.posts) + len(s.comments) + len(s.media) + len(s.redirects)
}

func (s *fakeSink) EnsureUser(_ context.Context, a Author) (int64, error) {
	s.users = append(s.users, a)
	return s.id(), nil
}

func (s *fakeSink) EnsureTerm(_ context.Context, taxonomy string, t Term, parentID *int64) (int64, error) {
	id := s.id()
	s.terms = append(s.terms, termCall{taxonomy, t, parentID, id})
	return id, nil
}

func (s *fakeSink) PostExists(_ context.Context, typ, slug string) (bool, error) {
	s.checks++
	return s.exists[typ+"/"+slug], nil
}

func (s *fakeSink) CreatePost(ctx context.Context, p Post) (int64, error) {
	if s.onPost != nil {
		s.onPost(len(s.postOrder))
	}
	if err := ctx.Err(); err != nil {
		return 0, err // like a database call would
	}
	if s.failPost != nil {
		if err := s.failPost(p); err != nil {
			return 0, err
		}
	}
	id := s.id()
	s.posts[id] = p
	s.postOrder = append(s.postOrder, id)
	return id, nil
}

func (s *fakeSink) CreateComment(_ context.Context, postID int64, c Comment, parentID *int64) (int64, error) {
	id := s.id()
	s.comments = append(s.comments, commentCall{postID, c, parentID, id})
	return id, nil
}

func (s *fakeSink) StoreMedia(_ context.Context, name string, r io.Reader, alt string) (int64, string, error) {
	if s.failMedia != nil {
		if err := s.failMedia(name); err != nil {
			return 0, "", err
		}
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, "", err
	}
	id := s.id()
	s.media = append(s.media, mediaCall{name, alt, string(b), id})
	return id, fmt.Sprintf("/uploads/%d/%s", id, name), nil
}

func (s *fakeSink) AddRedirect(_ context.Context, from, to string) error {
	s.redirects[from] = to
	return nil
}

func (s *fakeSink) postBySlug(t *testing.T, typ, slug string) (int64, Post) {
	t.Helper()
	for _, id := range s.postOrder {
		if p := s.posts[id]; p.Type == typ && p.Slug == slug {
			return id, p
		}
	}
	t.Fatalf("%s %q not created", typ, slug)
	return 0, Post{}
}

func (s *fakeSink) term(t *testing.T, taxonomy, slug string) termCall {
	t.Helper()
	for _, c := range s.terms {
		if c.Taxonomy == taxonomy && c.Term.Slug == slug {
			return c
		}
	}
	t.Fatalf("%s %q not ensured", taxonomy, slug)
	return termCall{}
}

func (s *fakeSink) mediaID(t *testing.T, name string) int64 {
	t.Helper()
	for _, m := range s.media {
		if m.Name == name {
			return m.ID
		}
	}
	t.Fatalf("media %q not stored", name)
	return 0
}

// mediaFixture serves the attachments of testdata/export.xml.
var mediaFixture = map[string]string{
	"/wp-content/uploads/2020/01/photo-scaled.jpg": "JPEG-photo",
	"/wp-content/uploads/2021/03/diagram.png":      "PNG-diagram",
	"/wp-content/uploads/2020/01/report.pdf":       "PDF-report",
}

// hostRewriter sends the export's media URLs to the test server through a
// real HTTPFetcher.
type hostRewriter struct {
	base  string
	inner *HTTPFetcher
	hits  *atomic.Int32
}

func (f hostRewriter) Fetch(ctx context.Context, raw string) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	f.hits.Add(1)
	return f.inner.Fetch(ctx, f.base+u.Path)
}

func newFixtureFetcher(t *testing.T) hostRewriter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := mediaFixture[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return hostRewriter{
		base:  srv.URL,
		inner: &HTTPFetcher{AllowedHosts: []string{"127.0.0.1"}, AllowAddr: allowAll},
		hits:  &atomic.Int32{},
	}
}

var fixtureNow = time.Date(2024, 10, 1, 12, 0, 0, 0, time.UTC)

func fixtureOptions(t *testing.T) (Options, hostRewriter) {
	f := newFixtureFetcher(t)
	return Options{
		DownloadMedia:   true,
		DefaultAuthorID: 1000,
		Fetcher:         f,
		Now:             func() time.Time { return fixtureNow },
	}, f
}

func hasWarning(r *Report, parts ...string) bool {
	for _, w := range r.Warnings {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(w, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestImportFixture(t *testing.T) {
	exp := loadFixture(t)
	sink := newFakeSink()
	opts, _ := fixtureOptions(t)
	rep, err := Import(context.Background(), exp, sink, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Authors.
	if len(sink.users) != 2 || sink.users[0].Login != "admin" || sink.users[1].Login != "maria" {
		t.Errorf("users = %+v", sink.users)
	}
	adminID, mariaID := rep.Mapping.Users["admin"], rep.Mapping.Users["maria"]

	// Terms: parents before children, slugs decoded, names unescaped.
	var catOrder []string
	for _, c := range sink.terms {
		if c.Taxonomy == TaxonomyCategory {
			catOrder = append(catOrder, c.Term.Slug)
		}
	}
	if slices.Index(catOrder, "viajes") > slices.Index(catOrder, "europa") || slices.Index(catOrder, "europa") > slices.Index(catOrder, "espana") {
		t.Errorf("categories not parents-first: %v", catOrder)
	}
	espana, europa, viajes := sink.term(t, TaxonomyCategory, "espana"), sink.term(t, TaxonomyCategory, "europa"), sink.term(t, TaxonomyCategory, "viajes")
	if espana.ParentID == nil || *espana.ParentID != europa.ID || europa.ParentID == nil || *europa.ParentID != viajes.ID || viajes.ParentID != nil {
		t.Errorf("category parents: espana %v, europa %v", espana.ParentID, europa.ParentID)
	}
	if n := sink.term(t, TaxonomyCategory, "noticias"); n.Term.Name != "Noticias & avisos" {
		t.Errorf("category name = %q", n.Term.Name)
	}
	if c := sink.term(t, TaxonomyTag, "cafe"); c.Term.Name != "Café" || c.Term.Description != "Todo sobre el café" {
		t.Errorf("tag = %+v", c.Term)
	}
	sink.term(t, TaxonomyTag, "tutorial") // only referenced by an item
	for _, c := range sink.terms {
		if strings.Contains(c.Term.Slug, "format") {
			t.Errorf("post_format imported as a term: %+v", c)
		}
	}

	// Media.
	if len(sink.media) != 3 {
		t.Fatalf("media = %+v", sink.media)
	}
	if m := sink.media[0]; m.Name != "photo-scaled.jpg" || m.Alt != "Una foto" || m.Data != "JPEG-photo" {
		t.Errorf("media = %+v", m)
	}
	photoID, diagramID := sink.mediaID(t, "photo-scaled.jpg"), sink.mediaID(t, "diagram.png")
	photoURL := fmt.Sprintf("/uploads/%d/photo-scaled.jpg", photoID)
	diagramURL := fmt.Sprintf("/uploads/%d/diagram.png", diagramID)
	reportURL := fmt.Sprintf("/uploads/%d/report.pdf", sink.mediaID(t, "report.pdf"))

	// Pages: parents first, parents mapped.
	var pageOrder []string
	for _, id := range sink.postOrder {
		if p := sink.posts[id]; p.Type == TypePage {
			pageOrder = append(pageOrder, p.Slug)
		}
	}
	if strings.Join(pageOrder, ",") != "acerca-de,equipo,historia,pagina-privada" {
		t.Errorf("page order = %v", pageOrder)
	}
	aboutID, about := sink.postBySlug(t, TypePage, "acerca-de")
	teamID, team := sink.postBySlug(t, TypePage, "equipo")
	_, history := sink.postBySlug(t, TypePage, "historia")
	if about.ParentID != nil || team.ParentID == nil || *team.ParentID != aboutID || history.ParentID == nil || *history.ParentID != teamID {
		t.Errorf("page parents: about %v team %v history %v", about.ParentID, team.ParentID, history.ParentID)
	}
	if team.Template != "templates/full-width.php" || about.Template != "" || team.MenuOrder != 2 || about.CommentStatus != "closed" {
		t.Errorf("page meta: %+v / %+v", team, about)
	}
	if about.Content != "Somos un pequeño blog.\n\nEscríbenos a [hola@example.com](mailto:hola@example.com)." {
		t.Errorf("about content = %q", about.Content)
	}
	if !strings.Contains(team.Content, "[historia](/historia)") {
		t.Errorf("page link not rewritten: %q", team.Content)
	}
	if _, p := sink.postBySlug(t, TypePage, "pagina-privada"); p.Status != StatusDraft || !hasWarning(rep, "Página privada", "private") {
		t.Errorf("private page = %+v", p)
	}

	// Posts in date order.
	var postOrder []string
	for _, id := range sink.postOrder {
		if p := sink.posts[id]; p.Type == TypePost {
			postOrder = append(postOrder, p.Slug)
		}
	}
	if strings.Join(postOrder, ",") != "hola-mundo,guia-de-gutenberg,notas-privadas,secreto,cafe-con-lena,borrador-sin-slug,post-programado" {
		t.Errorf("post order = %v", postOrder)
	}

	holaID, hola := sink.postBySlug(t, TypePost, "hola-mundo")
	if hola.Status != StatusPublished || !hola.Sticky || hola.AuthorID != adminID || hola.CommentStatus != "open" ||
		hola.Title != "¡Hola mundo!" || hola.OldURL != "https://blog.example.com/2020/01/15/hola-mundo/" || hola.OldID != 20 {
		t.Errorf("hola = %+v", hola)
	}
	if hola.PublishedAt == nil || !hola.PublishedAt.Equal(time.Date(2020, 1, 15, 10, 30, 0, 0, time.UTC)) ||
		hola.ModifiedAt == nil || !hola.ModifiedAt.Equal(time.Date(2020, 1, 20, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("hola dates = %v / %v", hola.PublishedAt, hola.ModifiedAt)
	}
	if hola.FeaturedMediaID == nil || *hola.FeaturedMediaID != photoID {
		t.Errorf("featured = %v, want %d", hola.FeaturedMediaID, photoID)
	}
	if len(hola.TermIDs) != 2 || !slices.Contains(hola.TermIDs, sink.term(t, TaxonomyCategory, "noticias").ID) || !slices.Contains(hola.TermIDs, sink.term(t, TaxonomyTag, "go").ID) {
		t.Errorf("hola terms = %v", hola.TermIDs)
	}
	mustContain(t, hola.Content,
		"Bienvenido a **Café Ñandú**. Este es el primer párrafo.  \nUna segunda línea del mismo párrafo.",
		"!"+"[Una foto]("+photoURL+")\n\n*Atardecer en la playa*",
		"<https://www.youtube.com/watch?v=dQw4w9WgXcQ>",
		"<https://vimeo.com/76979871>",
		"[acerca de](/acerca-de)", "[informe]("+reportURL+")",
		"<https://www.youtube.com/watch?v=abc123def45>",
		"```php\n<?php\necho \"hola\";\n\n?>\n```",
		"\n\nTexto con sangría que no debe ser código.\n\n",
		`[contact-form-7 id="99" title="Contacto"]`)
	mustNotContain(t, hola.Content, "<script", "alert", "Suscribirse", "wp-content", "    Texto")
	if hola.Excerpt != "Bienvenido a Café Ñandú. Este es el primer párrafo.\nUna segunda línea del mismo párrafo.\n\nAtardecer en la playa" {
		t.Errorf("hola excerpt = %q", hola.Excerpt)
	}

	_, guide := sink.postBySlug(t, TypePost, "guia-de-gutenberg")
	if guide.AuthorID != mariaID || guide.FeaturedMediaID == nil || *guide.FeaturedMediaID != diagramID || len(guide.TermIDs) != 3 {
		t.Errorf("guide = %+v", guide)
	}
	mustContain(t, guide.Content,
		"Introducción a *Gutenberg*. Ver [el primer post](/blog/hola-mundo#comments) o [su enlace corto](/blog/hola-mundo).",
		"## Una imagen", "![Diagrama]("+diagramURL+")\n\n*Un diagrama*",
		"```go\npackage main\n\nfunc main() {\n\tprintln(\"<hola>\")\n}\n```",
		"- Uno\n- Dos\n  - Dos punto uno", "> Una cita.\n>\n> Alguien", "| Lenguaje | Año  |\n|----------|------|\n| Go       | 2009 |",
		"<https://www.youtube.com/watch?v=xyz>", "* * *", "HTML personalizado")
	mustNotContain(t, guide.Content, "steal", "evil.example", "display", "<!--", "wp:")
	if guide.Excerpt != "Introducción a Gutenberg. Ver el primer post o su enlace corto." {
		t.Errorf("guide excerpt = %q", guide.Excerpt)
	}

	_, future := sink.postBySlug(t, TypePost, "post-programado")
	if future.Status != StatusPublished || future.PublishedAt == nil || !future.PublishedAt.Equal(time.Date(2030, 6, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("future = %+v", future)
	}
	if future.Excerpt != "Un adelanto explícito." {
		t.Errorf("explicit excerpt = %q", future.Excerpt)
	}
	if _, draft := sink.postBySlug(t, TypePost, "borrador-sin-slug"); draft.Status != StatusDraft || draft.PublishedAt != nil {
		t.Errorf("draft = %+v", draft)
	}
	if _, p := sink.postBySlug(t, TypePost, "notas-privadas"); p.Status != StatusDraft || p.PublishedAt != nil {
		t.Errorf("private post = %+v", p)
	}
	if _, p := sink.postBySlug(t, TypePost, "secreto"); p.Status != StatusDraft || !hasWarning(rep, "Secreto", "password-protected") {
		t.Errorf("protected post = %+v", p)
	}
	_, cafe := sink.postBySlug(t, TypePost, "cafe-con-lena")
	if cafe.AuthorID != 1000 || cafe.FeaturedMediaID != nil || cafe.Title != "Café con leña" {
		t.Errorf("cafe = %+v", cafe)
	}
	mustContain(t, cafe.Content, "![Una foto]("+photoURL+")\n\n*Atardecer en la playa*\n\n![Diagrama]("+diagramURL+")",
		"![Perdida](https://blog.example.com/wp-content/uploads/2019/01/gone.jpg)")
	for _, p := range sink.posts {
		if p.Title == "Borrado" {
			t.Error("trashed post imported")
		}
	}

	// Comments: threaded, parents first, statuses mapped.
	var order []int64
	byWP := map[int64]commentCall{}
	for _, c := range sink.comments {
		if c.PostID != holaID {
			t.Errorf("comment on post %d", c.PostID)
		}
		order = append(order, c.Comment.ID)
		byWP[c.Comment.ID] = c
	}
	if fmt.Sprint(order) != "[1 2 3 4 7]" {
		t.Errorf("comment order = %v", order)
	}
	if c := byWP[1]; c.ParentID != nil || c.Comment.Status != CommentApproved || c.Comment.Text != "¡Gran post!\n\nSegundo párrafo con enlace." ||
		c.Comment.Author != "Ana" || c.Comment.NewUserID != nil {
		t.Errorf("comment 1 = %+v", c)
	}
	if c := byWP[2]; c.ParentID == nil || *c.ParentID != byWP[1].ID || c.Comment.NewUserID == nil || *c.Comment.NewUserID != adminID {
		t.Errorf("comment 2 = %+v", c)
	}
	if c := byWP[3]; c.ParentID == nil || *c.ParentID != byWP[2].ID || c.Comment.Status != CommentPending || c.Comment.Text != "Una pregunta & una duda" {
		t.Errorf("comment 3 = %+v", c)
	}
	if c := byWP[4]; c.Comment.Status != CommentSpam {
		t.Errorf("comment 4 = %+v", c)
	}
	if c := byWP[7]; c.ParentID != nil || c.Comment.Status != CommentApproved {
		t.Errorf("reply to a trashed comment should become top-level: %+v", c)
	}

	// Redirects: public content and media only.
	wantRedirects := map[string]string{
		"/2020/01/15/hola-mundo/":                      "/blog/hola-mundo",
		"/2021/03/01/guia-de-gutenberg/":               "/blog/guia-de-gutenberg",
		"/2023/01/01/caf%c3%a9-con-le%c3%b1a/":         "/blog/cafe-con-lena",
		"/acerca-de/":                                  "/acerca-de",
		"/acerca-de/equipo/":                           "/equipo",
		"/acerca-de/equipo/historia/":                  "/historia",
		"/wp-content/uploads/2020/01/photo-scaled.jpg": photoURL,
		"/wp-content/uploads/2021/03/diagram.png":      diagramURL,
		"/wp-content/uploads/2020/01/report.pdf":       reportURL,
	}
	if len(sink.redirects) != len(wantRedirects) {
		t.Errorf("redirects = %v", sink.redirects)
	}
	for from, to := range wantRedirects {
		if sink.redirects[from] != to {
			t.Errorf("redirect %s = %q, want %q", from, sink.redirects[from], to)
		}
	}

	// Report.
	c := rep.Counts
	for name, got := range map[string]Count{
		"users": c.Users, "categories": c.Categories, "tags": c.Tags, "media": c.Media, "pages": c.Pages,
		"posts": c.Posts, "comments": c.Comments, "redirects": c.Redirects,
	} {
		want := map[string]Count{
			"users": {2, 0, 0}, "categories": {4, 0, 0}, "tags": {3, 0, 0}, "media": {3, 0, 0}, "pages": {4, 0, 0},
			"posts": {7, 1, 0}, "comments": {5, 1, 0}, "redirects": {9, 0, 0},
		}[name]
		if got != want {
			t.Errorf("%s count = %+v, want %+v", name, got, want)
		}
	}
	for typ, n := range map[string]int{"nav_menu_item": 1, "revision": 1, "wp_block": 1, "custom_css": 1, "product": 1, "pingback": 1} {
		if rep.Ignored[typ] != n {
			t.Errorf("ignored %s = %d", typ, rep.Ignored[typ])
		}
	}
	for _, w := range [][]string{
		{"Notas privadas", "private"}, {"ghost", "default author"}, {"featured image 99"},
		{"¡Hola mundo!", "[contact-form-7]"}, {"¡Hola mundo!", "form was removed"},
	} {
		if !hasWarning(rep, w...) {
			t.Errorf("missing warning %q in %q", w, rep.Warnings)
		}
	}
	if rep.Mapping.Posts[20] != holaID || rep.Mapping.URLs["https://blog.example.com/2020/01/15/hola-mundo/"] != "/blog/hola-mundo" ||
		rep.Mapping.Media[10] != photoID || rep.Mapping.Terms["category/espana"] != espana.ID || len(rep.Mapping.Comments) != 5 {
		t.Errorf("mapping = %+v", rep.Mapping)
	}
	if rep.DryRun || !rep.StartedAt.Equal(fixtureNow) || !strings.Contains(rep.Summary(), "posts: 7 created, 1 skipped, 0 failed") {
		t.Errorf("summary:\n%s", rep.Summary())
	}
}

func TestImportDryRun(t *testing.T) {
	exp := loadFixture(t)
	sink := newFakeSink()
	opts, fetcher := fixtureOptions(t)
	opts.DryRun = true
	rep, err := Import(context.Background(), exp, sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sink.writes() != 0 || fetcher.hits.Load() != 0 {
		t.Errorf("dry run wrote %d objects and fetched %d files", sink.writes(), fetcher.hits.Load())
	}
	if sink.checks == 0 {
		t.Error("dry run should still check slugs")
	}
	if !rep.DryRun || rep.Counts.Posts.Created != 7 || rep.Counts.Pages.Created != 4 || rep.Counts.Comments.Created != 5 ||
		rep.Counts.Media.Created != 3 || rep.Counts.Redirects.Created != 6 || rep.Counts.Users.Created != 2 {
		t.Errorf("dry run counts = %+v", rep.Counts)
	}
	for _, id := range rep.Mapping.Posts {
		if id >= 0 {
			t.Errorf("dry run ID %d should be a negative placeholder", id)
		}
	}
	if !strings.HasPrefix(rep.Summary(), "Dry run") {
		t.Errorf("summary = %q", rep.Summary())
	}
}

func TestImportExistingSlugs(t *testing.T) {
	// SkipExisting: the post is skipped, links still point at it.
	sink := newFakeSink()
	sink.exists["post/hola-mundo"] = true
	opts, _ := fixtureOptions(t)
	opts.SkipExisting = true
	rep, err := Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Posts.Created != 6 || rep.Counts.Posts.Skipped != 2 || rep.Counts.Comments.Created != 0 || rep.Counts.Comments.Skipped != 7 {
		t.Errorf("counts = %+v", rep.Counts)
	}
	_, guide := sink.postBySlug(t, TypePost, "guia-de-gutenberg")
	mustContain(t, guide.Content, "(/blog/hola-mundo#comments)")
	if _, ok := sink.redirects["/2020/01/15/hola-mundo/"]; ok {
		t.Error("redirect added for a skipped post")
	}

	// Otherwise a free slug is chosen and links follow it.
	sink = newFakeSink()
	sink.exists["post/hola-mundo"] = true
	sink.exists["post/hola-mundo-2"] = true
	opts.SkipExisting = false
	rep, err = Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	sink.postBySlug(t, TypePost, "hola-mundo-3")
	_, guide = sink.postBySlug(t, TypePost, "guia-de-gutenberg")
	mustContain(t, guide.Content, "(/blog/hola-mundo-3#comments)")
	if sink.redirects["/2020/01/15/hola-mundo/"] != "/blog/hola-mundo-3" || !hasWarning(rep, `slug "hola-mundo" is taken, using "hola-mundo-3"`) {
		t.Errorf("redirects = %v, warnings = %q", sink.redirects, rep.Warnings)
	}
}

func TestImportContinuesOnItemErrors(t *testing.T) {
	sink := newFakeSink()
	sink.failPost = func(p Post) error {
		if p.Slug == "secreto" {
			return errors.New("constraint violated")
		}
		return nil
	}
	sink.failMedia = func(name string) error {
		if name == "diagram.png" {
			return errors.New("file type not allowed")
		}
		return nil
	}
	opts, _ := fixtureOptions(t)
	rep, err := Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Posts.Created != 6 || rep.Counts.Posts.Failed != 1 || rep.Counts.Media.Created != 2 || rep.Counts.Media.Failed != 1 {
		t.Errorf("counts = %+v", rep.Counts)
	}
	if !hasWarning(rep, "Secreto", "constraint violated") || !hasWarning(rep, "diagram.png", "file type not allowed") ||
		!hasWarning(rep, "Guía de Gutenberg", "featured image 11") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
	_, guide := sink.postBySlug(t, TypePost, "guia-de-gutenberg")
	mustContain(t, guide.Content, "![Diagrama](https://blog.example.com/wp-content/uploads/2021/03/diagram-1024x768.png)")
}

func TestImportMediaLimits(t *testing.T) {
	sink := newFakeSink()
	opts, _ := fixtureOptions(t)
	opts.MaxMediaFiles = 1
	rep, err := Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Media != (Count{Created: 1, Skipped: 2}) || !hasWarning(rep, "media limit of 1") {
		t.Errorf("media = %+v, warnings %q", rep.Counts.Media, rep.Warnings)
	}

	sink = newFakeSink()
	opts, _ = fixtureOptions(t)
	opts.MaxMediaBytes = 5
	rep, err = Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Media != (Count{Failed: 3}) || len(sink.media) != 0 || !hasWarning(rep, "too large") {
		t.Errorf("media = %+v, stored %d, warnings %q", rep.Counts.Media, len(sink.media), rep.Warnings)
	}

	// Without DownloadMedia nothing is fetched and URLs stay.
	sink = newFakeSink()
	opts, fetcher := fixtureOptions(t)
	opts.DownloadMedia = false
	rep, err = Import(context.Background(), loadFixture(t), sink, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fetcher.hits.Load() != 0 || rep.Counts.Media.Skipped != 3 {
		t.Errorf("media = %+v, hits %d", rep.Counts.Media, fetcher.hits.Load())
	}
	_, hola := sink.postBySlug(t, TypePost, "hola-mundo")
	mustContain(t, hola.Content, "https://blog.example.com/wp-content/uploads/2020/01/photo-300x200.jpg")
}

func TestImportCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newFakeSink()
	sink.onPost = func(n int) {
		if n == 2 {
			cancel()
		}
	}
	opts, _ := fixtureOptions(t)
	rep, err := Import(ctx, loadFixture(t), sink, opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if rep == nil || len(sink.posts) != 2 || len(sink.comments) != 0 || len(sink.redirects) != 0 {
		t.Errorf("work continued after cancellation: %d posts, %d comments", len(sink.posts), len(sink.comments))
	}
	if rep.Counts.Pages.Failed+rep.Counts.Posts.Failed != 0 {
		t.Errorf("cancellation counted as an item failure: %+v", rep.Counts)
	}
}

func TestImportWarningsAreBounded(t *testing.T) {
	exp := &Export{Site: Site{Link: "https://x.example"}}
	for i := range 600 {
		exp.Items = append(exp.Items, Item{ID: int64(i + 1), Type: TypePost, Status: "private", Title: fmt.Sprintf("P%d", i), Slug: fmt.Sprintf("p-%d", i)})
	}
	rep, err := Import(context.Background(), exp, newFakeSink(), Options{DefaultAuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != maxWarnings || rep.WarningsDropped != 100 || rep.Counts.Posts.Created != 600 {
		t.Errorf("warnings %d, dropped %d, created %d", len(rep.Warnings), rep.WarningsDropped, rep.Counts.Posts.Created)
	}
}

func TestImportValidatesOptions(t *testing.T) {
	if _, err := Import(context.Background(), &Export{}, newFakeSink(), Options{}); err == nil {
		t.Error("missing DefaultAuthorID accepted")
	}
	if _, err := Import(context.Background(), nil, newFakeSink(), Options{DefaultAuthorID: 1}); err == nil {
		t.Error("nil export accepted")
	}
	if _, err := Import(context.Background(), &Export{}, nil, Options{DefaultAuthorID: 1}); err == nil {
		t.Error("nil sink accepted")
	}
}

func TestImportPageCycleAndTrash(t *testing.T) {
	exp := &Export{Items: []Item{
		{ID: 1, Type: TypePage, Status: "publish", Title: "A", Slug: "a", ParentID: 2},
		{ID: 2, Type: TypePage, Status: "publish", Title: "B", Slug: "b", ParentID: 1},
		{ID: 3, Type: TypePost, Status: "trash", Title: "T", Slug: "t__trashed", Comments: []Comment{{ID: 1, Content: "x", Approved: "1"}}},
	}}
	sink := newFakeSink()
	rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1, IncludeTrash: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Counts.Pages.Created != 2 {
		t.Errorf("pages in a cycle = %+v", rep.Counts.Pages)
	}
	_, trashed := sink.postBySlug(t, TypePost, "t")
	if trashed.Status != StatusTrash || rep.Counts.Comments.Created != 1 {
		t.Errorf("IncludeTrash: %+v, comments %+v", trashed, rep.Counts.Comments)
	}
}

func TestImportSanitisesComments(t *testing.T) {
	exp := &Export{Items: []Item{{ID: 1, Type: TypePost, Status: "publish", Title: "P", Slug: "p", Comments: []Comment{
		{ID: 1, Author: "<b>Eve</b>", AuthorURL: "javascript:alert(1)", AuthorEmail: "not-an-email", Content: "<script>x</script>Hi", Approved: "1"},
		{ID: 2, Author: "", AuthorURL: "https://ok.example/me", AuthorEmail: "a@ok.example", Content: "Yo", Approved: "weird"},
		{ID: 3, Author: "Empty", Content: "  <p></p> ", Approved: "1"},
	}}}}
	sink := newFakeSink()
	rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.comments) != 2 || rep.Counts.Comments != (Count{Created: 2, Skipped: 1}) {
		t.Fatalf("comments = %+v, counts %+v", sink.comments, rep.Counts.Comments)
	}
	if c := sink.comments[0].Comment; c.Author != "Eve" || c.AuthorURL != "" || c.AuthorEmail != "" || c.Text != "Hi" {
		t.Errorf("comment 1 = %+v", c)
	}
	if c := sink.comments[1].Comment; c.Author != "Anonymous" || c.AuthorURL != "https://ok.example/me" || c.AuthorEmail != "a@ok.example" || c.Status != CommentPending {
		t.Errorf("comment 2 = %+v", c)
	}
}

func TestImportNonLatinTerms(t *testing.T) {
	exp := &Export{
		Categories: []Term{
			{ID: 3, Slug: "%e6%97%a5%e6%9c%ac", Name: "日本", Parent: "%e3%82%a2%e3%82%b8%e3%82%a2"},
			{ID: 2, Slug: "%e3%82%a2%e3%82%b8%e3%82%a2", Name: "アジア"},
		},
		Items: []Item{{ID: 1, Type: TypePost, Status: "publish", Title: "東京", Slug: "%e6%9d%b1%e4%ba%ac",
			Terms: []ItemTerm{{Domain: "category", Slug: "%E6%97%A5%E6%9C%AC", Name: "日本"}}}},
	}
	sink := newFakeSink()
	rep, err := Import(context.Background(), exp, sink, Options{DefaultAuthorID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.terms) != 2 || rep.Counts.Categories.Created != 2 {
		t.Fatalf("terms = %+v", sink.terms)
	}
	asia, japan := sink.terms[0], sink.terms[1]
	if !strings.HasPrefix(japan.Term.Slug, "term-") || japan.ParentID == nil || *japan.ParentID != asia.ID {
		t.Errorf("japan = %+v, asia = %+v", japan, asia)
	}
	_, p := sink.postBySlug(t, TypePost, "post-1")
	if len(p.TermIDs) != 1 || p.TermIDs[0] != japan.ID {
		t.Errorf("post terms = %v, want [%d]", p.TermIDs, japan.ID)
	}
}
