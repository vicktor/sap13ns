package export

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
	"sap13ns/internal/wpimport"
)

func TestClean(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain", "plain"},
		{"tab\tnl\ncr\r", "tab\tnl\ncr\r"},
		{"bell\x07nul\x00esc\x1b", "bellnulesc"},
		{"bad \xff\xfe utf-8", "bad \uFFFD\uFFFD utf-8"},
		{"non-chars \uFFFE\uFFFF end", "non-chars  end"},
		{"emoji 🎉 and ñ", "emoji 🎉 and ñ"},
	} {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCDATA writes nasty strings the way the exporter does and reads them
// back with encoding/xml and the importer's parser.
func TestCDATA(t *testing.T) {
	nasty := []string{
		"ends with ]]>",
		"]]>]]>]]>",
		"a ]]]]> b ]]> c ]",
		"<script>alert(1)</script> & < > \" '",
		"control\x01\x02\x1f chars",
		"invalid \xc3\x28 utf-8",
		"",
	}
	for _, s := range nasty {
		var buf bytes.Buffer
		x := &xmlWriter{w: bufio.NewWriter(&buf)}
		x.raw("<r>")
		x.cdataEl("c", s)
		x.raw("<t>")
		x.text(s)
		x.raw("</t></r>")
		if err := x.flush(); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(buf.String(), "<r><c><![CDATA[") || !utf8Valid(buf.Bytes()) {
			t.Fatalf("%q: bad output %q", s, buf.String())
		}
		var got struct {
			C string `xml:"c"`
			T string `xml:"t"`
		}
		if err := xml.Unmarshal(buf.Bytes(), &got); err != nil {
			t.Fatalf("%q: %v in %q", s, err, buf.String())
		}
		if want := Clean(s); got.C != want || got.T != want {
			t.Errorf("%q: read back %q and %q, want %q", s, got.C, got.T, want)
		}
	}
}

func utf8Valid(b []byte) bool { return strings.ToValidUTF8(string(b), "\x00") == string(b) }

func TestNamesAndSweep(t *testing.T) {
	now := time.Now()
	name := NewName(42, now)
	uid, created, ok := ParseName(name)
	if !ok || uid != 42 || created.Unix() != now.Unix() {
		t.Fatalf("ParseName(%q) = %d, %v, %v", name, uid, created, ok)
	}
	for _, bad := range []string{"", "u0-1-" + strings.Repeat("a", 32) + ".zip", "u1-1-abc.zip", "../u1-1-" + strings.Repeat("a", 32) + ".zip",
		"u1-1-" + strings.Repeat("a", 32) + ".zip.part", "u1-1-" + strings.Repeat("A", 32) + ".zip"} {
		if _, _, ok := ParseName(bad); ok {
			t.Errorf("ParseName(%q) accepted", bad)
		}
	}

	dir := t.TempDir()
	touch := func(name string, mod time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
		return p
	}
	fresh := touch(NewName(1, now.Add(-time.Hour)), now)
	expired := touch(NewName(1, now.Add(-TTL-time.Minute)), now)
	freshPart := touch(NewName(2, now)+PartSuffix, now)
	stalePart := touch(NewName(2, now.Add(-5*time.Hour))+PartSuffix, now.Add(-4*time.Hour))
	staleClaim := touch(NewName(3, now)+ClaimSuffix, now.Add(-time.Hour))
	other := touch("notes.txt", now.Add(-48*time.Hour))

	ready, err := List(dir, now)
	if err != nil || len(ready) != 1 || filepath.Join(dir, ready[0].Name) != fresh || ready[0].UserID != 1 {
		t.Fatalf("List = %+v, %v", ready, err)
	}
	n, err := Sweep(dir, now)
	if err != nil || n != 3 {
		t.Fatalf("Sweep = %d, %v; want 3 removed", n, err)
	}
	for p, want := range map[string]bool{fresh: true, expired: false, freshPart: true, stalePart: false, staleClaim: false, other: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
	if n, err := Sweep(filepath.Join(dir, "missing"), now); n != 0 || err != nil {
		t.Errorf("Sweep of a missing directory = %d, %v", n, err)
	}
}

// - - - with a database - - -

type seeded struct {
	admin, author int64
	posts         []int64
	uploads       string
}

// seed creates a small multilingual site whose personal data is easy to
// look for: every e-mail is at example.com and every IP in 192.0.2.0/24.
func seed(t testing.TB, st *store.Store, marker string) seeded {
	t.Helper()
	ctx := context.Background()
	var s seeded
	var err error
	if s.admin, err = st.CreateUser(ctx, store.NewUser{Username: marker + "-admin", Email: marker + "-admin@example.com",
		DisplayName: "Admin " + marker, Role: "admin", PasswordHash: "$argon2id$secret-hash-" + marker}); err != nil {
		t.Fatal(err)
	}
	if s.author, err = st.CreateUser(ctx, store.NewUser{Username: marker + "-author", Email: marker + "-author@example.com",
		DisplayName: "Author " + marker, Role: "author", PasswordHash: "$argon2id$secret-hash-" + marker}); err != nil {
		t.Fatal(err)
	}
	cat, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "News " + marker, Slug: marker + "-news",
		I18n: map[string]store.TermText{"en": {Name: "News EN " + marker}}})
	if err != nil {
		t.Fatal(err)
	}
	s.uploads = t.TempDir()
	if err := os.MkdirAll(filepath.Join(s.uploads, "2026", "10"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.uploads, "2026", "10", marker+".png"), []byte("png bytes "+marker), 0o644); err != nil {
		t.Fatal(err)
	}
	mid, err := st.CreateMedia(ctx, &store.Media{UploaderID: &s.admin, Path: "2026/10/" + marker + ".png", OriginalName: marker + ".png",
		MIME: "image/png", Size: 10, Alt: "Alt " + marker, Variants: map[string]store.MediaVariant{}, SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	post, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: "Hello " + marker, Slug: marker + "-hello",
		Lang: "es", Content: "Body of " + marker + " ![img](/uploads/2026/10/" + marker + ".png)", AuthorID: s.author,
		CommentStatus: "open", PublishedAt: &pub, TermIDs: []int64{cat}, FeaturedMediaID: &mid})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: "Hello EN " + marker, Slug: marker + "-hello",
		Lang: "en", TranslationOf: post, Content: "English body of " + marker, AuthorID: s.author, CommentStatus: "open", PublishedAt: &pub})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "draft", Title: "Draft " + marker, Slug: marker + "-draft",
		Lang: "es", Content: "Secret draft of " + marker, AuthorID: s.admin, CommentStatus: "open"})
	if err != nil {
		t.Fatal(err)
	}
	s.posts = []int64{post, tr, draft}
	ip := netip.MustParseAddr("192.0.2.7")
	for i, status := range []string{"approved", "pending", "spam"} {
		if _, err := st.CreateComment(ctx, store.NewComment{PostID: post, AuthorName: "Reader " + marker,
			AuthorEmail: marker + "-reader@example.com", Content: status + " comment of " + marker, Status: status, IP: ip,
			UserAgent: "secret-agent"}); err != nil {
			t.Fatal(err, i)
		}
	}
	if err := st.SaveRedirect(ctx, "/old-"+marker, "/blog/"+marker+"-hello", 301, false); err != nil {
		t.Fatal(err)
	}
	return s
}

func siteOpts(s seeded) Options {
	return Options{Media: true, Site: Site{URL: "https://example.com", Title: "Site", Language: "es", UploadsDir: s.uploads}}
}

func wxr(t *testing.T, st *store.Store, o Options) (string, *Report) {
	t.Helper()
	var buf bytes.Buffer
	rep, err := WriteWXR(context.Background(), st, &buf, o)
	if err != nil {
		t.Fatal(err)
	}
	if err := xml.Unmarshal(buf.Bytes(), new(struct{})); err != nil {
		t.Fatalf("not well-formed XML: %v", err)
	}
	return buf.String(), rep
}

func TestWXRContentAndPersonalData(t *testing.T) {
	st := testutil.Store(t)
	s := seed(t, st, "alpha")

	out, rep := wxr(t, st, siteOpts(s))
	if rep.Posts != 2 || rep.Authors != 1 || rep.Attachments != 1 || rep.Comments != 1 || rep.Categories != 1 || rep.Redirects != 1 {
		t.Errorf("report = %+v", rep)
	}
	for _, want := range []string{"Hello alpha", "Hello EN alpha", wpimport.MetaMarkdown, "_thumbnail_id", "News EN alpha",
		"<sap13ns:source><![CDATA[/old-alpha]]>", "approved comment of alpha", "https://example.com/en/blog/alpha-hello",
		`src="https://example.com/uploads/2026/10/alpha.png"`} {
		if !strings.Contains(out, want) {
			t.Errorf("export lacks %q", want)
		}
	}
	for _, never := range []string{"Secret draft", "pending comment", "spam comment", "secret-hash", "secret-agent", "argon2id"} {
		if strings.Contains(out, never) {
			t.Errorf("export contains %q", never)
		}
	}
	// Without personal data: no e-mail address and no IP address at all.
	if strings.Contains(out, "@") || strings.Contains(out, "192.0.2.") {
		t.Errorf("export without personal data has an e-mail or IP address")
	}

	o := siteOpts(s)
	o.PersonalData, o.PendingComments = true, true
	o.Statuses = []string{store.StatusPublished, store.StatusDraft}
	out, rep = wxr(t, st, o)
	for _, want := range []string{"alpha-reader@example.com", "192.0.2.7", "alpha-author@example.com", "alpha-admin@example.com",
		"Secret draft of alpha", "pending comment of alpha"} {
		if !strings.Contains(out, want) {
			t.Errorf("export with personal data, drafts and pending comments lacks %q", want)
		}
	}
	if strings.Contains(out, "spam comment") || strings.Contains(out, "secret-hash") || rep.Comments != 2 || rep.Posts != 3 {
		t.Errorf("report = %+v", rep)
	}

	// Filters: an author, a date range, pages only.
	o = siteOpts(s)
	o.AuthorID = s.admin
	if _, rep = wxr(t, st, o); rep.Posts != 0 {
		t.Errorf("admin has no published posts, got %d", rep.Posts)
	}
	o = siteOpts(s)
	o.From = time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if _, rep = wxr(t, st, o); rep.Posts != 0 {
		t.Errorf("date range: got %d posts", rep.Posts)
	}
	o.From, o.To = time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC)
	if _, rep = wxr(t, st, o); rep.Posts != 2 {
		t.Errorf("date range: got %d posts", rep.Posts)
	}
	o = siteOpts(s)
	o.Types, o.Media = []string{store.TypePage}, false
	if out, rep = wxr(t, st, o); rep.Posts != 0 || rep.Attachments != 0 || strings.Contains(out, "Hello alpha") {
		t.Errorf("pages only: %+v", rep)
	}

	// The importer's parser reads it back.
	out, _ = wxr(t, st, siteOpts(s))
	exp, err := wpimport.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	var langs []string
	for _, it := range exp.Items {
		if it.Type == "post" {
			langs = append(langs, it.Lang)
			if it.TranslationGroup != s.posts[0] || !strings.HasPrefix(it.Markdown, "Body of alpha") && !strings.HasPrefix(it.Markdown, "English body") {
				t.Errorf("item %q: group %d, markdown %q", it.Title, it.TranslationGroup, it.Markdown)
			}
		}
	}
	if strings.Join(langs, ",") != "es,en" || len(exp.Redirects) != 1 || exp.Categories[0].I18n["en"].Name != "News EN alpha" {
		t.Errorf("parsed back: langs %v, redirects %+v, categories %+v", langs, exp.Redirects, exp.Categories)
	}
}

// TestEscaping stores what XML cannot carry as is and checks that the
// file stays well-formed and the importer reads the text back.
func TestEscaping(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, store.NewUser{Username: "writer", Email: "w@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	pub := time.Now().Add(-time.Hour)
	title := "Tricky ]]> <b>&amp; \x01title"
	body := "Code: `a]]>b` and <span>x</span> ]]]]> \x07bell, & ünïcödé 🎉"
	if _, err := st.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: title, Slug: "tricky", Lang: "es",
		Content: body, Excerpt: "Excerpt with ]]> and <tags> & stuff", AuthorID: uid, CommentStatus: "open", PublishedAt: &pub}); err != nil {
		t.Fatal(err)
	}
	out, _ := wxr(t, st, Options{Site: Site{URL: "https://example.com", Language: "es"}})
	if strings.ContainsAny(out, "\x01\x07") {
		t.Error("control characters reached the file")
	}
	exp, err := wpimport.Parse(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	it := exp.Items[0]
	if it.Markdown != Clean(body) {
		t.Errorf("markdown = %q, want %q", it.Markdown, Clean(body))
	}
	if got := wpimport.HTMLToText(it.Title); got != Clean(title) {
		t.Errorf("title = %q (raw %q), want %q", got, it.Title, Clean(title))
	}
	if got := wpimport.HTMLToText(it.Excerpt); got != "Excerpt with ]]> and <tags> & stuff" {
		t.Errorf("excerpt = %q", got)
	}
}

// TestStreamingMemory exports a site whose content is several times what
// the heap may grow by while it is written.
func TestStreamingMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	st := testutil.Store(t)
	ctx := context.Background()
	uid, err := st.CreateUser(ctx, store.NewUser{Username: "writer", Email: "w@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	const posts, size = 4000, 12 << 10 // 48 MiB of Markdown
	if _, err := st.DB.Exec(ctx, `INSERT INTO posts (type, status, title, slug, lang, content, author_id, published_at)
		SELECT 'post', 'published', 'Post ' || i, 'post-' || i, 'es', repeat('Lorem ipsum dolor sit amet. ', $2 / 28), $3, now() - make_interval(mins => i)
		FROM generate_series(1, $1) AS i`, posts, size, uid); err != nil {
		t.Fatal(err)
	}
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	w := &peakWriter{every: 1 << 20}
	rep, err := WriteWXR(ctx, st, w, Options{Site: Site{URL: "https://example.com", Language: "es"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Posts != posts || w.n < posts*size*2 {
		t.Fatalf("exported %d posts, %d bytes", rep.Posts, w.n)
	}
	grew := int64(w.peak) - int64(base.HeapAlloc)
	t.Logf("wrote %d MiB; heap grew by at most %d MiB", w.n>>20, grew>>20)
	if limit := int64(posts * size / 2); grew > limit {
		t.Errorf("heap grew by %d MiB while writing %d MiB of posts: the export is not streaming", grew>>20, posts*size>>20)
	}
}

// peakWriter discards what it gets and samples the heap every so often.
type peakWriter struct {
	n, next, every int
	peak           uint64
}

func (p *peakWriter) Write(b []byte) (int, error) {
	p.n += len(b)
	if p.n >= p.next {
		p.next = p.n + p.every
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		p.peak = max(p.peak, m.HeapAlloc)
	}
	return len(b), nil
}

func TestZip(t *testing.T) {
	st := testutil.Store(t)
	s := seed(t, st, "beta")
	// Left out: the importer's temporary files and links.
	if err := os.MkdirAll(filepath.Join(s.uploads, ".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.uploads, ".tmp", "partial"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(s.uploads, "upload.tmp"), []byte("x"), 0o644)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	os.WriteFile(secret, []byte("outside the uploads"), 0o600)
	if err := os.Symlink(secret, filepath.Join(s.uploads, "link.txt")); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rep, err := WriteZip(context.Background(), st, &buf, siteOpts(s))
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	if len(files) != 4 || rep.Files != 1 {
		t.Errorf("members: %v (report %+v)", keys(files), rep)
	}
	if _, err := wpimport.Parse(strings.NewReader(files[ZipWXR])); err != nil {
		t.Errorf("export.xml: %v", err)
	}
	if files["uploads/2026/10/beta.png"] != "png bytes beta" {
		t.Errorf("upload = %q", files["uploads/2026/10/beta.png"])
	}
	md := files["posts/es/beta-hello.md"]
	for _, want := range []string{"---\nid: ", `title: "Hello beta"`, `lang: "es"`, `categories: ["beta-news"]`,
		`featured_image: "/uploads/2026/10/beta.png"`, `date: "2026-03-04T10:00:00Z"`, "---\n\nBody of beta"} {
		if !strings.Contains(md, want) {
			t.Errorf("posts/es/beta-hello.md lacks %q:\n%s", want, md)
		}
	}
	if !strings.Contains(files["posts/en/beta-hello.md"], "translation_group: ") {
		t.Errorf("translation lacks its group:\n%s", files["posts/en/beta-hello.md"])
	}

	// Without the media library the .zip has no files.
	o := siteOpts(s)
	o.Media = false
	buf.Reset()
	if rep, err := WriteZip(context.Background(), st, &buf, o); err != nil || rep.Files != 0 || rep.Attachments != 1 {
		t.Errorf("without media: %+v, %v", rep, err)
	}

	// Limits.
	for name, o := range map[string]Options{
		"bytes": func() Options { o := siteOpts(s); o.MaxBytes = 100; return o }(),
		"files": func() Options { o := siteOpts(s); o.MaxFiles = -1; return o }(),
		"xml":   func() Options { o := siteOpts(s); o.MaxXMLBytes = 100; return o }(),
	} {
		if _, err := WriteZip(context.Background(), st, io.Discard, o); !errors.Is(err, ErrTooLarge) {
			t.Errorf("%s limit: err = %v", name, err)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestNetworkIsolation exports one site of a network and looks for
// anything of another site in the file.
func TestNetworkIsolation(t *testing.T) {
	pool := testutil.Schemas(t)
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	sa := seed(t, a, "alpha")
	seed(t, b, "bravo")

	o := siteOpts(sa)
	o.PersonalData, o.PendingComments = true, true
	o.Statuses = []string{store.StatusPublished, store.StatusDraft, store.StatusPending, store.StatusPrivate}
	var buf bytes.Buffer
	if _, err := WriteZip(context.Background(), a, &buf, o); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range zr.File {
		all.WriteString(f.Name + "\n")
		rc, _ := f.Open()
		io.Copy(&all, rc)
		rc.Close()
	}
	if !strings.Contains(all.String(), "alpha") {
		t.Fatal("site A's export lacks its own content")
	}
	if strings.Contains(all.String(), "bravo") {
		t.Error("site A's export contains site B's data")
	}
}
