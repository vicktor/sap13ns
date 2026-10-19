package sitebackup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/backup"
	"sap13ns/internal/media"
	"sap13ns/internal/store"
	"sap13ns/internal/testutil"
)

// Markers that must never be in a backup.
const (
	secretHash  = "$argon2id$v=19$m=65536,t=3,p=4$secret-hash-marker"
	totpMarker  = "totp-secret-marker"
	aiKeyMarker = "ai-key-marker"
	embedMarker = "embed-key-marker"
	mcpMarker   = "mcp-token-hash-marker"
	pluginSeal  = "enc1:sealed-plugin-secret"
	pluginToken = "plain-token-marker"
)

type seeded struct {
	admin, editor, author, reader int64
	posts                         map[string]int64
	media                         int64
	uploads                       string
}

// pngBytes is a real PNG of w×h pixels.
func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// seed fills a site with one of everything a backup carries, and with
// secrets it must not. E-mail addresses are at example.com, IPs in
// 192.0.2.0/24.
func seed(t testing.TB, st *store.Store, uploads string) seeded {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	s := seeded{posts: map[string]int64{}, uploads: uploads}
	var err error
	user := func(name, role string) int64 {
		id, err := st.CreateUser(ctx, store.NewUser{Username: name, Email: name + "@example.com", DisplayName: strings.ToUpper(name),
			Role: role, PasswordHash: secretHash})
		must(err)
		return id
	}
	s.admin, s.editor, s.author, s.reader = user("alice", "admin"), user("edith", "editor"), user("arthur", "author"), user("rita", "subscriber")
	must(st.SetTOTP(ctx, s.admin, []byte(totpMarker), true, []string{"recovery-marker"}))
	must(st.UpdateUserProfile(ctx, s.editor, store.UserProfile{Email: "edith@example.com", DisplayName: "Edith E.", Bio: "Editor bio", Website: "https://edith.example.com"}))

	parent, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Noticias", Slug: "noticias",
		Description: "Todo", I18n: map[string]store.TermText{"en": {Name: "News", Description: "All"}}})
	must(err)
	child, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "category", Name: "Local", Slug: "local", ParentID: &parent})
	must(err)
	tag, err := st.CreateTerm(ctx, store.TermInput{Taxonomy: "tag", Name: "Go & Co", Slug: "go"})
	must(err)

	proc := &media.Processor{Dir: uploads, MaxBytes: 32 << 20}
	res, err := proc.Save(ctx, bytes.NewReader(pngBytes(t, 640, 360)))
	must(err)
	if res.Variants["thumb"].Path == "" {
		t.Fatalf("no variants: %+v", res)
	}
	s.media, err = st.CreateMedia(ctx, &store.Media{UploaderID: &s.author, Path: res.Path, OriginalName: "Foto de prueba.png",
		MIME: res.MIME, Size: res.Size, Width: res.Width, Height: res.Height, Alt: "Una foto", Caption: "Pie <b>de</b> foto",
		Variants: res.Variants, SHA256: res.SHA256})
	must(err)
	txt, err := proc.Save(ctx, strings.NewReader("plain notes\n"))
	must(err)
	_, err = st.CreateMedia(ctx, &store.Media{Path: txt.Path, OriginalName: "notes.txt", MIME: txt.MIME, Size: txt.Size,
		Variants: map[string]store.MediaVariant{}, SHA256: txt.SHA256})
	must(err)

	pub := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	future := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	post := func(key string, in store.PostInput) int64 {
		t.Helper()
		if in.CommentStatus == "" {
			in.CommentStatus = "open"
		}
		if in.Lang == "" {
			in.Lang = "es"
		}
		if in.AuthorID == 0 {
			in.AuthorID = s.author
		}
		id, err := st.CreatePost(ctx, in)
		must(err)
		s.posts[key] = id
		return id
	}
	thumb := "/uploads/" + res.Variants["thumb"].Path // a 640-pixel image has no medium copy
	hello := post("hello", store.PostInput{Type: "post", Status: "published", Title: "Hola <mundo> & más", Slug: "hola",
		Excerpt: "Un resumen\ncon dos líneas", Content: "Texto con **negrita** y ![foto](" + thumb + ")\n\n[Sobre](/sobre)\n",
		PublishedAt: &pub, TermIDs: []int64{child, tag}, FeaturedMediaID: &s.media, MetaDescription: "Descripción",
		AssistantExclude: true})
	post("hello-en", store.PostInput{Type: "post", Status: "published", Title: "Hello world", Slug: "hello", Lang: "en",
		TranslationOf: hello, Content: "English text", PublishedAt: &pub, TermIDs: []int64{parent}})
	post("draft", store.PostInput{Type: "post", Status: "draft", Title: "Borrador", Slug: "borrador", Content: "Sin terminar",
		AuthorID: s.editor})
	post("pending", store.PostInput{Type: "post", Status: "pending", Title: "Pendiente", Slug: "pendiente", Content: "Por revisar"})
	post("private", store.PostInput{Type: "post", Status: "private", Title: "Privado", Slug: "privado", Content: "Solo yo",
		PublishedAt: &pub, AuthorID: s.admin})
	post("trash", store.PostInput{Type: "post", Status: "trash", Title: "Papelera", Slug: "papelera", Content: "Borrado",
		PublishedAt: &pub})
	post("future", store.PostInput{Type: "post", Status: "published", Title: "Futuro", Slug: "futuro", Content: "Pronto",
		PublishedAt: &future, CommentStatus: "closed"})
	about := post("about", store.PostInput{Type: "page", Status: "published", Title: "Sobre", Slug: "sobre", Content: "Somos",
		PublishedAt: &pub, AuthorID: s.admin, CommentStatus: "closed", MenuOrder: 2})
	post("team", store.PostInput{Type: "page", Status: "published", Title: "Equipo", Slug: "equipo", Content: "Gente",
		PublishedAt: &pub, AuthorID: s.admin, CommentStatus: "closed", ParentID: &about, MenuOrder: 1})

	ip := netip.MustParseAddr("192.0.2.7")
	c1, err := st.CreateComment(ctx, store.NewComment{PostID: hello, AuthorName: "Lectora", AuthorEmail: "lectora@example.com",
		AuthorURL: "https://lectora.example.com", Content: "¡Muy bueno!\n\nSegundo párrafo", Status: "approved", IP: ip})
	must(err)
	c2, err := st.CreateComment(ctx, store.NewComment{PostID: hello, ParentID: &c1, UserID: &s.editor, AuthorName: "Edith",
		AuthorEmail: "edith@example.com", Content: "Gracias", Status: "approved", IP: ip})
	must(err)
	_, err = st.CreateComment(ctx, store.NewComment{PostID: hello, ParentID: &c2, AuthorName: "Otro", Content: "En espera",
		Status: "pending", IP: ip})
	must(err)
	_, err = st.CreateComment(ctx, store.NewComment{PostID: hello, AuthorName: "Spammer", Content: "spam-marker", Status: "spam", IP: ip})
	must(err)

	for _, it := range []struct{ loc, lang, label, url string }{
		{"primary", "", "Inicio", "/"}, {"primary", "", "Sobre", "/sobre"}, {"primary", "", "Noticias", "/category/noticias"},
		{"primary", "en", "Home", "/en/"}, {"footer", "", "Fuera", "https://other.example.org/"},
	} {
		must(st.AddMenuItem(ctx, it.loc, it.lang, it.label, it.url))
	}
	must(st.AddWidget(ctx, "sidebar", "", "text", "Hola", map[string]string{"text": "Texto **libre**"}))
	must(st.AddWidget(ctx, "sidebar", "en", "recent_posts", "Recent", map[string]string{"count": "3"}))

	design := fmt.Sprintf(`{"header":{"logo":%d},"colors":{"light":{"accent":"#7c3aed"}}}`, s.media)
	must(st.PublishCustom(ctx, "default", store.CustomDesign, []byte(design), s.admin, nil))
	must(st.SaveCustomDraft(ctx, "default", store.CustomDesign, []byte(`{"colors":{"light":{"accent":"#ff0000"}}}`), s.admin))
	layout := fmt.Sprintf(`{"version":1,"pages":{"home":{"sections":[{"id":"s1","layout":"1","columns":[[{"id":"img","type":"image","settings":{"image":"%d"}}]]}]}}}`, s.media)
	must(st.PublishCustom(ctx, "default", store.CustomLayout, []byte(layout), s.editor, nil))

	set, err := st.LoadSettings(ctx)
	must(err)
	set.SiteTitle, set.Tagline, set.Language, set.Languages = "Mi sitio", "Un lema", "es", []string{"en"}
	set.SiteText = map[string]store.SiteText{"en": {SiteTitle: "My site"}}
	set.FrontPage, set.FrontPageID = "page", about
	must(st.SaveSettings(ctx, set))
	must(st.SaveThemeOptions(ctx, "default", map[string]string{"layout": "wide"}))
	must(st.SaveAISettings(ctx, store.AISettings{Enabled: true, Provider: "anthropic", APIKeyEnc: []byte(aiKeyMarker), TextModel: "m1",
		MinRole: "author", MonthlyRequests: 100}))
	must(st.SaveAssistantSettings(ctx, store.AssistantSettings{Enabled: true, Greeting: "Hola", EmbedKeyEnc: []byte(embedMarker),
		EmbedProvider: "same", PerIPHour: 20, MCPEnabled: true, MCPTokenHash: mcpMarker}))

	must(st.SaveRedirect(ctx, "/viejo", "/blog/hola", 301, false))
	must(st.SaveRedirect(ctx, "/tienda", "https://shop.example.com/", 302, false))
	must(st.SaveRedirect(ctx, "/blog/hola-antiguo", "/blog/hola", 301, true))

	must(st.RecordPluginInstall(ctx, store.PluginInstall{ID: "greeter", Version: "1.2.0", Source: "cli"}))
	must(st.SavePluginSettings(ctx, "greeter", map[string]string{"greeting": "hola", "api_token": pluginToken, "key": pluginSeal,
		"notify": "owner@example.com", "@email:notify": "1"}))
	return s
}

func siteOpts(dir, uploads string, id int64) Options {
	return Options{Dir: dir, UploadsDir: uploads, AppVersion: "test",
		Site: Site{ID: id, Label: fmt.Sprintf("site%d", id), URL: fmt.Sprintf("https://site%d.example.com", id), Network: "net.example.com"}}
}

// checkRedirect stands for web.CheckRestoredRedirect (tested in web, which
// imports this package): it refuses what the tests mark as refused.
func checkRedirect(_ context.Context, _ *store.Store, _, src, dst string, status int) (string, string, error) {
	if strings.Contains(src, "refused") {
		return "", "", errors.New("refused")
	}
	return src, dst, nil
}

func restoreOpts(o Options, actor int64, replace bool) RestoreOptions {
	return RestoreOptions{Options: o, Replace: replace, ActorID: actor, CheckRedirect: checkRedirect, Plugins: fakePlugins{}}
}

// fakePlugins stands for *plugin.Host (its RestoreSettings is tested in
// plugin): greeter declares greeting (text of at most 20 characters), key
// and api_token (secrets) and notify (an e-mail address), and has the
// widget hello.
type fakePlugins struct{}

func (fakePlugins) RestoreSettings(id string, restored, current map[string]string) (map[string]string, []string, bool) {
	if id != "greeter" {
		return nil, nil, false
	}
	out := map[string]string{}
	for _, k := range []string{"greeting", "key", "api_token", "notify", "@email:notify"} {
		if v, ok := current[k]; ok {
			out[k] = v
		}
	}
	var refused []string
	if v, ok := restored["greeting"]; ok && len(v) <= 20 {
		out["greeting"] = v
	} else if ok {
		refused = append(refused, "Greeting")
	}
	return out, refused, true
}

func (fakePlugins) WidgetKnown(kind string) bool { return kind == "plugin:greeter:hello" }

func (fakePlugins) CleanWidgetConfig(_ string, in map[string]string) (map[string]string, error) {
	if in["style"] != "" && in["style"] != "plain" {
		return nil, errors.New("Style: invalid choice")
	}
	return map[string]string{"style": "plain"}, nil
}

// members returns every member of a plain .zip.
func members(t testing.TB, file string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

// tree hashes the regular files below dir.
func tree(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			sum := sha256.Sum256(b)
			out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// snapshot is what a restore must reproduce, with users by name.
func snapshot(t testing.TB, st *store.Store) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	q := func(key, sql string) {
		t.Helper()
		rows, err := st.DB.Query(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(vals)
			lines = append(lines, string(b))
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out[key] = strings.Join(lines, "\n")
	}
	q("posts", `SELECT p.id, p.type, p.status, p.title, p.slug, p.lang, (SELECT min(q.id) FROM posts q WHERE q.tgroup = p.tgroup),
		p.excerpt, p.content, u.username, p.parent_id, p.menu_order, p.featured_media_id, p.comment_status, p.meta_description,
		p.published_at, p.assistant_exclude, (SELECT array_agg(t.slug ORDER BY t.slug) FROM post_terms pt JOIN terms t ON t.id = pt.term_id WHERE pt.post_id = p.id)
		FROM posts p LEFT JOIN users u ON u.id = p.author_id ORDER BY p.id`)
	// Spam is not backed up; WXR dates have seconds.
	q("comments", `SELECT c.id, c.post_id, c.parent_id, u.username, c.author_name, c.author_email, c.author_url, c.content, c.status,
		host(c.ip), date_trunc('second', c.created_at) FROM comments c LEFT JOIN users u ON u.id = c.user_id
		WHERE c.status <> 'spam' ORDER BY c.id`)
	q("terms", `SELECT id, taxonomy, name, slug, description, parent_id, i18n FROM terms ORDER BY id`)
	q("media", `SELECT m.id, u.username, m.path, m.original_name, m.mime, m.size, m.width, m.height, m.alt, m.caption, m.variants,
		m.sha256, m.created_at FROM media m LEFT JOIN users u ON u.id = m.uploader_id ORDER BY m.id`)
	q("menus", `SELECT id, location, lang, label, url, position, created_at FROM menu_items ORDER BY id`)
	q("widgets", `SELECT id, area, lang, kind, title, config, position, created_at FROM widgets ORDER BY id`)
	q("custom", `SELECT c.theme, c.kind, c.published, c.draft, c.published_at, c.draft_at, u.username FROM theme_custom c
		LEFT JOIN users u ON u.id = c.updated_by ORDER BY 1, 2`)
	q("revisions", `SELECT r.id, r.theme, r.kind, r.data, r.created_at, u.username FROM theme_custom_revisions r
		LEFT JOIN users u ON u.id = r.user_id ORDER BY r.id`)
	q("redirects", `SELECT source, target, status, auto, hits FROM redirects ORDER BY source`)
	q("settings", `SELECT key, value FROM settings WHERE key = 'site' OR key LIKE 'theme:%' ORDER BY key`)
	q("users", `SELECT username, email, display_name, bio, website, role, status FROM users ORDER BY username`)
	return out
}

func TestRoundTrip(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	base := t.TempDir()
	upA, upB := filepath.Join(base, "sites/1/uploads"), filepath.Join(base, "sites/2/uploads")
	dirA, dirB := filepath.Join(base, "backups/sites/1"), filepath.Join(base, "backups/sites/2")
	s := seed(t, a, upA)
	before := snapshot(t, a)
	filesA := tree(t, upA)

	oa := siteOpts(dirA, upA, 1)
	arch, m, err := Create(ctx, a, oa)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(arch.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(dirA); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("backups dir mode: %v %v", fi.Mode(), err)
	}
	if m.Counts.Posts != 7 || m.Counts.Pages != 2 || m.Counts.Attachments != 2 || m.Counts.Comments != 3 || m.Counts.MenuItems != 5 {
		t.Errorf("counts: %+v", m.Counts)
	}

	// What is inside: WordPress's layout, no secret, the manifest last.
	files := members(t, arch.Path)
	for _, want := range []string{NameWXR, NameReadme, NameManifest, nameSettings, nameUsers, nameMedia, nameMenus, nameWidgets,
		nameAppear, nameRedirect, namePlugins} {
		if files[want] == nil {
			t.Errorf("archive lacks %s", want)
		}
	}
	for rel, sum := range filesA {
		b := files[DirUploads+rel]
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum {
			t.Errorf("%s%s missing or different", DirUploads, rel)
		}
	}
	var all bytes.Buffer
	for name, b := range files {
		all.WriteString(name)
		all.Write(b)
	}
	for _, never := range []string{secretHash, "secret-hash-marker", totpMarker, "recovery-marker", aiKeyMarker, embedMarker,
		mcpMarker, pluginSeal, pluginToken, "spam-marker", "password_hash", "totp",
		// base64 of the byte keys, as JSON would carry them
		"YWkta2V5LW1hcmtlcg", "ZW1iZWQta2V5LW1hcmtlcg"} {
		if strings.Contains(all.String(), never) {
			t.Errorf("the backup contains %q", never)
		}
	}
	zr, _ := zip.OpenReader(arch.Path)
	if last := zr.File[len(zr.File)-1].Name; last != NameManifest {
		t.Errorf("last member %s", last)
	}
	zr.Close()
	if _, err := Verify(arch.Path, "", dirA); err != nil {
		t.Fatal(err)
	}

	// Restore into another site of the network, which has an admin and a
	// plugin of its own.
	bob, err := b.CreateUser(ctx, store.NewUser{Username: "bob", Email: "bob@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RecordPluginInstall(ctx, store.PluginInstall{ID: "greeter", Version: "1.2.0", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SavePluginSettings(ctx, "greeter", map[string]string{"key": "enc1:target-secret", "greeting": "hi"}); err != nil {
		t.Fatal(err)
	}
	ob := siteOpts(dirB, upB, 2)
	rep, err := Restore(ctx, b, arch.Path, restoreOpts(ob, bob, false))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Safety != nil {
		t.Error("a safety backup of an empty site")
	}
	if rep.Posts != 7 || rep.Pages != 2 || rep.Media != 2 || rep.Comments != 3 || rep.MenuItems != 5 || rep.Widgets != 2 ||
		rep.Redirects != 3 || rep.UsersCreated != 4 || rep.Plugins != 1 {
		t.Errorf("report: %+v", rep)
	}
	t.Logf("warnings: %q", rep.Warnings)
	for _, w := range rep.Warnings {
		if strings.Contains(w, "not valid") || strings.Contains(w, "left out") {
			t.Errorf("a valid backup restored with a change: %s", w)
		}
	}

	after := snapshot(t, b)
	for k, want := range before {
		got := after[k]
		if k == "users" {
			want = strings.Join(append(strings.Split(want, "\n"), `["bob","bob@example.com","","","","admin","active"]`), "\n")
			lines := strings.Split(want, "\n")
			slices.Sort(lines)
			want = strings.Join(lines, "\n")
		}
		if got != want {
			t.Errorf("%s differs after the round trip\nwant:\n%s\ngot:\n%s", k, want, got)
		}
	}
	if got := tree(t, upB); !mapsEqual(got, filesA) {
		t.Errorf("uploads differ: %v\nwant %v", got, filesA)
	}

	// Secrets: none came, the target's stayed; restored users have none.
	ctx2 := context.Background()
	alice, err := b.UserByUsername(ctx2, "alice")
	if err != nil || alice.PasswordHash != "" || alice.TOTPEnabled || alice.Role != "admin" {
		t.Errorf("restored admin: %+v %v", alice, err)
	}
	ai, _ := b.AISettings(ctx2)
	if !ai.Enabled || ai.TextModel != "m1" || len(ai.APIKeyEnc) != 0 {
		t.Errorf("AI settings: %+v", ai)
	}
	as, _ := b.AssistantSettings(ctx2)
	if as.Greeting != "Hola" || as.MCPEnabled || as.MCPTokenHash != "" {
		t.Errorf("assistant settings: %+v (MCP must be off without its token)", as)
	}
	recs, _ := b.ListPluginRecords(ctx2)
	if g := recs["greeter"].Settings; g["greeting"] != "hola" || g["key"] != "enc1:target-secret" || g["api_token"] != "" ||
		g["notify"] != "" || recs["greeter"].Enabled {
		t.Errorf("plugin settings: %v", g)
	}
	if dirs, _ := os.ReadDir(filepath.Dir(upB)); len(dirs) != 1 {
		t.Errorf("left over next to the uploads: %v", dirs)
	}

	// The source site is untouched (TestIsolation checks it harder).
	if got := snapshot(t, a); !mapsEqual(got, before) {
		t.Error("the source site changed")
	}
	_ = s
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestIsolation restores over a site with content and checks that the
// other site's schema and files do not change, and that the safety
// backup is of the restored site.
func TestIsolation(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	base := t.TempDir()
	upA, upB := filepath.Join(base, "sites/1/uploads"), filepath.Join(base, "sites/2/uploads")
	dirA, dirB := filepath.Join(base, "backups/sites/1"), filepath.Join(base, "backups/sites/2")
	seed(t, a, upA)
	sb := seed(t, b, upB)
	oa, ob := siteOpts(dirA, upA, 1), siteOpts(dirB, upB, 2)
	arch, _, err := Create(ctx, a, oa)
	if err != nil {
		t.Fatal(err)
	}
	// site_2 gets something site_1 does not have, which the restore drops.
	if _, err := b.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: "Only on two", Slug: "only-two",
		Lang: "es", AuthorID: sb.admin, CommentStatus: "open"}); err != nil {
		t.Fatal(err)
	}
	beforeA, filesA := snapshot(t, a), tree(t, upA)
	backupsA := tree(t, dirA)

	if _, err := Restore(ctx, b, arch.Path, restoreOpts(ob, sb.admin, false)); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("restore over content without replace: %v", err)
	}
	rep, err := Restore(ctx, b, arch.Path, restoreOpts(ob, sb.admin, true))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Safety == nil || filepath.Dir(rep.Safety.Path) != dirB || !rep.Safety.PreRestore {
		t.Fatalf("safety backup: %+v", rep.Safety)
	}
	if got := snapshot(t, a); !mapsEqual(got, beforeA) {
		t.Error("restoring site 2 changed the database of site 1")
	}
	if !mapsEqual(tree(t, upA), filesA) || !mapsEqual(tree(t, dirA), backupsA) {
		t.Error("restoring site 2 changed the files of site 1")
	}
	if ok, _ := b.SlugExists(ctx, "post", "es", "only-two", 0); ok {
		t.Error("the replaced content is still there")
	}
	// The safety backup restores what was replaced.
	if _, err := Restore(ctx, b, rep.Safety.Path, restoreOpts(ob, sb.admin, true)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.SlugExists(ctx, "post", "es", "only-two", 0); !ok {
		t.Error("the safety backup lacks the replaced post")
	}
}

// rewrite copies a backup, changing one member with fn and, if fix,
// updating the manifest to match.
func rewrite(t testing.TB, src, dst, name string, fix bool, fn func([]byte) []byte) {
	t.Helper()
	in := members(t, src)
	zr, err := zip.OpenReader(src)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	in[name] = fn(in[name])
	if fix {
		var m Manifest
		if err := json.Unmarshal(in[NameManifest], &m); err != nil {
			t.Fatal(err)
		}
		for i := range m.Files {
			if m.Files[i].Name == name {
				sum := sha256.Sum256(in[name])
				m.Files[i].Size, m.Files[i].SHA256 = int64(len(in[name])), hex.EncodeToString(sum[:])
			}
		}
		in[NameManifest], _ = json.Marshal(m)
	}
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, zf := range zr.File {
		w, err := zw.Create(zf.Name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(in[zf.Name])
	}
	zw.Close()
	f.Close()
}

// TestFailedRestore checks that a restore that fails, before or inside
// the transaction, leaves the site as it was.
func TestFailedRestore(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	base := t.TempDir()
	upA, upB := filepath.Join(base, "a/uploads"), filepath.Join(base, "b/uploads")
	seed(t, a, upA)
	sb := seed(t, b, upB)
	if _, err := b.CreatePost(ctx, store.PostInput{Type: "post", Status: "published", Title: "Mine", Slug: "mine", Lang: "es",
		AuthorID: sb.admin, CommentStatus: "open"}); err != nil {
		t.Fatal(err)
	}
	oa, ob := siteOpts(filepath.Join(base, "ba"), upA, 1), siteOpts(filepath.Join(base, "bb"), upB, 2)
	arch, _, err := Create(ctx, a, oa)
	if err != nil {
		t.Fatal(err)
	}
	before, files := snapshot(t, b), tree(t, upB)
	check := func(what string) {
		t.Helper()
		if !mapsEqual(snapshot(t, b), before) || !mapsEqual(tree(t, upB), files) {
			t.Errorf("%s: the site changed", what)
		}
		if dirs, _ := os.ReadDir(filepath.Dir(upB)); len(dirs) != 1 {
			t.Errorf("%s: left over next to the uploads: %v", what, dirs)
		}
	}
	var img string
	for name := range members(t, arch.Path) {
		if strings.HasPrefix(name, DirUploads) && strings.HasSuffix(name, "-thumb.png") {
			img = name
		}
	}

	// A file changed after the backup: its checksum fails.
	bad := filepath.Join(base, "bad.zip")
	rewrite(t, arch.Path, bad, img, false, func(b []byte) []byte { b[len(b)-20] ^= 1; return b })
	if _, err := Restore(ctx, b, bad, restoreOpts(ob, sb.admin, true)); !errors.Is(err, backup.ErrCorrupt) {
		t.Errorf("tampered file: %v", err)
	}
	check("tampered file")

	// A file that is not what its media item says, even with a matching
	// manifest: the media item is left out.
	rewrite(t, arch.Path, bad, img, true, func([]byte) []byte { return []byte("<svg onload=alert(1)>") })
	rep, err := Restore(ctx, b, bad, restoreOpts(ob, sb.admin, true))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Media != 1 || !slices.ContainsFunc(rep.Warnings, func(w string) bool { return strings.Contains(w, "not restored") }) {
		t.Errorf("disguised file: %d media, %q", rep.Media, rep.Warnings)
	}
	// Put site 2 back as it was for the next case.
	if _, err := Restore(ctx, b, rep.Safety.Path, restoreOpts(ob, sb.admin, true)); err != nil {
		t.Fatal(err)
	}
	before, files = snapshot(t, b), tree(t, upB)

	// A failure inside the transaction (settings that do not decode,
	// read after the content is in) rolls everything back.
	rewrite(t, arch.Path, bad, nameSettings, true, func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"posts_per_page": 10`), []byte(`"posts_per_page": "ten"`), 1)
	})
	if _, err := Restore(ctx, b, bad, restoreOpts(ob, sb.admin, true)); !errors.Is(err, backup.ErrCorrupt) {
		t.Errorf("settings that do not decode: %v", err)
	}
	check("failed transaction")

	// A menu item with a script address is left out.
	rewrite(t, arch.Path, bad, nameMenus, true, func(b []byte) []byte {
		return bytes.Replace(b, []byte(`"URL": "https://other.example.org/"`), []byte(`"URL": "javascript:alert(1)"`), 1)
	})
	rep, err = Restore(ctx, b, bad, restoreOpts(ob, sb.admin, true))
	if err != nil {
		t.Fatal(err)
	}
	if rep.MenuItems != 4 {
		t.Errorf("script menu item restored: %d items", rep.MenuItems)
	}
}

func TestUnsafeArchives(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, entries func(*zip.Writer)) string {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		zw := zip.NewWriter(f)
		entries(zw)
		zw.Close()
		f.Close()
		return p
	}
	manifest := func(zw *zip.Writer, files ...backup.Member) {
		w, _ := zw.Create(NameManifest)
		b, _ := json.Marshal(Manifest{Format: FormatVersion, Kind: manifestKind, Files: files})
		w.Write(b)
	}
	sum := func(b string) string { s := sha256.Sum256([]byte(b)); return hex.EncodeToString(s[:]) }
	cases := map[string]struct {
		file string
		want error
	}{
		"traversal": {mk("a.zip", func(zw *zip.Writer) {
			w, _ := zw.Create("../evil.txt")
			w.Write([]byte("x"))
			manifest(zw, backup.Member{Name: "../evil.txt", Size: 1, SHA256: sum("x")})
		}), backup.ErrUnsafeMember},
		"absolute": {mk("b.zip", func(zw *zip.Writer) {
			w, _ := zw.Create("/etc/passwd")
			w.Write([]byte("x"))
			manifest(zw)
		}), backup.ErrUnsafeMember},
		"symlink": {mk("c.zip", func(zw *zip.Writer) {
			h := &zip.FileHeader{Name: DirUploads + "link"}
			h.SetMode(fs.ModeSymlink | 0o777)
			w, _ := zw.CreateHeader(h)
			w.Write([]byte("/etc/passwd"))
			manifest(zw)
		}), backup.ErrUnsafeMember},
		"unlisted": {mk("d.zip", func(zw *zip.Writer) {
			w, _ := zw.Create(DirUploads + "2026/10/extra.png")
			w.Write([]byte("x"))
			manifest(zw)
		}), backup.ErrCorrupt},
		"size lie": {mk("e.zip", func(zw *zip.Writer) {
			w, _ := zw.Create(NameWXR)
			w.Write([]byte(strings.Repeat("x", 100)))
			manifest(zw, backup.Member{Name: NameWXR, Size: 10, SHA256: sum("x")})
		}), backup.ErrCorrupt},
		"not a backup": {mk("f.zip", func(zw *zip.Writer) {
			w, _ := zw.Create("hello.txt")
			w.Write([]byte("x"))
		}), ErrNotSiteBackup},
	}
	for name, c := range cases {
		if _, err := Verify(c.file, "", dir); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestEncryptedPruneAndStatus(t *testing.T) {
	st := testutil.Store(t)
	ctx := context.Background()
	base := t.TempDir()
	up, dir := filepath.Join(base, "uploads"), filepath.Join(base, "backups")
	s := seed(t, st, up)
	o := siteOpts(dir, up, 0)
	o.Site = Site{Label: "Blog.Example.com", URL: "https://blog.example.com"}
	o.Passphrase = "correct horse battery staple"
	arch, _, err := Create(ctx, st, o)
	if err != nil {
		t.Fatal(err)
	}
	if !arch.Encrypted || !strings.HasPrefix(arch.Name, "sap13ns-site-blog.example.com-") || !strings.HasSuffix(arch.Name, ".zip.enc") {
		t.Fatalf("archive %+v", arch)
	}
	if _, err := Verify(arch.Path, "wrong passphrase!!", dir); !errors.Is(err, backup.ErrWrongPassphrase) {
		t.Errorf("wrong passphrase: %v", err)
	}
	if _, err := Verify(arch.Path, o.Passphrase, dir); err != nil {
		t.Fatal(err)
	}
	// Plain gives the .zip itself.
	rc, err := Plain(arch.Path, o.Passphrase)
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(base, "plain.zip")
	f, _ := os.Create(plain)
	if _, err := io.Copy(f, rc); err != nil {
		t.Fatal(err)
	}
	f.Close()
	rc.Close()
	if files := members(t, plain); files[NameWXR] == nil {
		t.Error("the decrypted backup is not the .zip")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".decrypted-*")); len(left) != 0 {
		t.Errorf("decrypted copies left: %v", left)
	}
	stt, err := ReadStatus(dir)
	if err != nil || stt.Result != ResultOK || stt.Archive != arch.Name || stt.OK.IsZero() {
		t.Errorf("status %+v %v", stt, err)
	}

	// A restore of an encrypted backup into the same site.
	if _, err := Restore(ctx, st, arch.Path, restoreOpts(o, s.admin, true)); err != nil {
		t.Fatal(err)
	}

	// Busy while locked.
	unlock, err := lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Create(ctx, st, o); !errors.Is(err, ErrBusy) {
		t.Errorf("concurrent backup: %v", err)
	}
	unlock()

	// Low disk: skipped, and recorded.
	defer func(f func(string) (int64, error)) { freeSpace = f }(freeSpace)
	freeSpace = func(string) (int64, error) { return 1024, nil }
	if _, _, err := Create(ctx, st, o); !errors.Is(err, ErrLowDisk) {
		t.Errorf("low disk: %v", err)
	}
	if stt, _ := ReadStatus(dir); stt.Result != ResultSkipped || stt.Archive != arch.Name {
		t.Errorf("status after a skipped run: %+v", stt)
	}

	// Prune: 7 daily and 4 weekly, safety backups for 7 days.
	pd := t.TempDir()
	now := time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC)
	for d := 0; d < 40; d++ {
		at := now.Add(-time.Duration(d) * 24 * time.Hour)
		os.WriteFile(filepath.Join(pd, archiveName("x", at, false, false)), nil, 0o600)
		if d == 2 || d == 9 {
			os.WriteFile(filepath.Join(pd, archiveName("x", at.Add(time.Minute), true, false)), nil, 0o600)
		}
	}
	os.WriteFile(filepath.Join(pd, "unrelated.zip"), nil, 0o600)
	if _, err := Prune(pd, Retention{Daily: 7, Weekly: 4}, now); err != nil {
		t.Fatal(err)
	}
	left, _ := List(pd)
	var regular, safety int
	for _, a := range left {
		if a.PreRestore {
			safety++
		} else {
			regular++
		}
	}
	// 7 days (19 to 13 October), and the newest of 4 ISO weeks: 19, 18,
	// 11 and 4 October (19 October 2026 is a Monday), 2 more.
	if regular != 9 || safety != 1 {
		t.Errorf("after prune: %d regular, %d safety (%v)", regular, safety, left)
	}
	if _, err := os.Stat(filepath.Join(pd, "unrelated.zip")); err != nil {
		t.Error("prune removed a file that is not a backup")
	}
}

func TestCheckWPBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "",
		"https://new.example":           "https://new.example",
		" https://new.example/ ":        "https://new.example",
		"http://example.com:8080/blog/": "http://example.com:8080/blog",
		"https://example.com/my%20wp":   "https://example.com/my%20wp",
	} {
		if got, err := CheckWPBase(in); err != nil || got != want {
			t.Errorf("CheckWPBase(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"new.example", "ftp://new.example", "https://", "https://user:pw@new.example", "https://new.example/?x=1",
		"https://new.example/#top", "https://new.example/\"onload", "javascript:alert(1)", "https://new.example/a b"} {
		if got, err := CheckWPBase(in); err == nil {
			t.Errorf("CheckWPBase(%q) = %q, want an error", in, got)
		}
	}
}

// editJSON rewrites a JSON member of a backup through fn.
func editJSON(t *testing.T, src, dst, name string, v any, fn func()) {
	t.Helper()
	rewrite(t, src, dst, name, true, func(b []byte) []byte {
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
		fn()
		out, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	})
}

// TestRestoreChecks: what a backup brings back passes the checks of the
// pages that save it. A tampered archive restores with what fails reset
// to the site's values (or the defaults) or left out, and the report
// names it; secrets stay the site's.
func TestRestoreChecks(t *testing.T) {
	pool := testutil.Schemas(t)
	ctx := context.Background()
	a := testutil.SiteStore(t, pool, "site_1")
	b := testutil.SiteStore(t, pool, "site_2")
	base := t.TempDir()
	upA, upB := filepath.Join(base, "a/uploads"), filepath.Join(base, "b/uploads")
	seed(t, a, upA)
	if err := a.AddWidget(ctx, "sidebar", "", "plugin:greeter:hello", "Hello", map[string]string{"style": "loud"}); err != nil {
		t.Fatal(err)
	}
	if err := a.AddWidget(ctx, "sidebar", "", "plugin:gone:widget", "Gone", nil); err != nil {
		t.Fatal(err)
	}
	if err := a.AddWidget(ctx, "sidebar", "", "recent_posts", "Many", map[string]string{"count": "500", "extra": "x"}); err != nil {
		t.Fatal(err)
	}
	oa, ob := siteOpts(filepath.Join(base, "ba"), upA, 1), siteOpts(filepath.Join(base, "bb"), upB, 2)
	arch, _, err := Create(ctx, a, oa)
	if err != nil {
		t.Fatal(err)
	}

	// The target: an administrator, the greeter plugin, keys of its own
	// and a local model it does not allow.
	bob, err := b.CreateUser(ctx, store.NewUser{Username: "bob", Email: "bob@example.com", Role: "admin", PasswordHash: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.RecordPluginInstall(ctx, store.PluginInstall{ID: "greeter", Version: "1.2.0", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SavePluginSettings(ctx, "greeter", map[string]string{"greeting": "hi", "key": "enc1:target-secret"}); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAISettings(ctx, store.AISettings{Provider: "anthropic", MinRole: "editor", APIKeyEnc: []byte("target-ai-key"),
		MonthlyRequests: 50}); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveAssistantSettings(ctx, store.AssistantSettings{EmbedProvider: "gemini", EmbedKeyEnc: []byte("target-embed-key"),
		MCPTokenHash: "target-mcp"}); err != nil {
		t.Fatal(err)
	}

	// The tampered backup.
	tampered := filepath.Join(base, "tampered.zip")
	step := filepath.Join(base, "step.zip")
	var side map[string]any
	editJSON(t, arch.Path, step, nameSettings, &side, func() {
		site := side["site"].(map[string]any)
		site["websub_hub"] = "http://127.0.0.1:8137/hub"
		site["comment_moderation"] = "nobody"
		site["default_role"] = "admin"
		site["og_accent"] = "red; background:url(x)"
		site["session_days"] = 100000
		site["two_factor"] = "off"
		site["languages"] = []string{"en", "xx", "es"}
		site["active_theme"] = "../evil"
		ai := side["ai"].(map[string]any)
		ai["base_url"] = "http://127.0.0.1:8080/v1"
		ai["allow_local"] = true
		ai["provider"] = "evil"
		ai["min_role"] = "subscriber"
		as := side["assistant"].(map[string]any)
		as["embed_provider"] = "gemini"
		as["embed_base_url"] = "https://embed.example.org"
		as["per_ip_hour"] = -1
		as["suggestions"] = []string{"1", "2", "3", "4", "5", "6", "7"}
		side["theme_options"].(map[string]any)["default"] = map[string]any{"layout": "wide", "bad key": "x", "long": strings.Repeat("x", 3000)}
	})
	var menus []map[string]any
	editJSON(t, step, tampered, nameMenus, &menus, func() {
		for _, it := range menus {
			switch it["URL"] {
			case "https://other.example.org/":
				it["URL"] = "javascript:alert(document.cookie)"
			case "/sobre":
				it["URL"] = "//evil.example.org/"
			}
		}
	})
	var plugins []map[string]any
	editJSON(t, tampered, step, namePlugins, &plugins, func() {
		for _, p := range plugins {
			p["settings"].(map[string]any)["greeting"] = strings.Repeat("hola ", 10)
			p["settings"].(map[string]any)["key"] = "enc1:smuggled"
		}
	})
	var users []map[string]any
	editJSON(t, step, tampered, nameUsers, &users, func() {
		for _, u := range users {
			if u["username"] == "edith" {
				u["website"] = "javascript:alert(1)"
			}
		}
		users = append(users, map[string]any{"id": 999, "username": "../root", "email": "root@example.com", "role": "admin", "status": "active"})
	})

	rep, err := Restore(ctx, b, tampered, restoreOpts(ob, bob, false))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("warnings: %q", rep.Warnings)
	warned := func(parts ...string) bool {
		return slices.ContainsFunc(rep.Warnings, func(w string) bool {
			for _, p := range parts {
				if !strings.Contains(w, p) {
					return false
				}
			}
			return true
		})
	}
	for _, want := range [][]string{
		{"settings not valid", "websub_hub", "comment_moderation", "default_role", "og_accent", "session_days", "two_factor",
			"languages", "active_theme"},
		{"writing assistant settings not valid", "allow_local", "provider", "min_role", "base_url"},
		{"site assistant settings not valid", "per_ip_hour", "suggestions"},
		{"embedding API key was removed"},
		{"options of theme default left out", "bad key", "long"},
		{"menu item", "Fuera", "not allowed"},
		{"menu item", "Sobre", "not allowed"},
		{"settings of plugin greeter not valid", "Greeting"},
		{"widget", "Hello", "invalid choice"},
		{"widget", "Gone", "no plugin"},
		{"user", "edith", "website"},
		{"user", "../root", "left out"},
	} {
		if !warned(want...) {
			t.Errorf("no warning with %q", want)
		}
	}

	set, _ := b.LoadSettings(ctx)
	def := store.DefaultSettings()
	if set.WebSubHub != "" || set.CommentModeration != def.CommentModeration || set.DefaultRole != "subscriber" ||
		set.OGAccent != "" || set.SessionDays != def.SessionDays || set.TwoFactor != "staff" || set.ActiveTheme != def.ActiveTheme ||
		!slices.Equal(set.Languages, []string{"en"}) || set.SiteTitle != "Mi sitio" || set.FrontPage != "page" {
		t.Errorf("settings: %+v", set)
	}
	aiSet, _ := b.AISettings(ctx)
	if aiSet.Provider != "anthropic" || aiSet.MinRole != "editor" || aiSet.BaseURL != "" || aiSet.AllowLocal ||
		string(aiSet.APIKeyEnc) != "target-ai-key" || aiSet.TextModel != "m1" {
		t.Errorf("AI settings: %+v", aiSet)
	}
	as, _ := b.AssistantSettings(ctx)
	if as.EmbedBaseURL != "https://embed.example.org" || len(as.EmbedKeyEnc) != 0 || as.PerIPHour != 0 ||
		len(as.Suggestions) != 0 || as.MCPTokenHash != "target-mcp" || as.Greeting != "Hola" {
		t.Errorf("assistant settings: %+v", as)
	}
	opts, _ := b.ThemeOptions(ctx, "default")
	if !maps.Equal(opts, map[string]string{"layout": "wide"}) {
		t.Errorf("theme options: %v", opts)
	}
	recs, _ := b.ListPluginRecords(ctx)
	if g := recs["greeter"].Settings; g["greeting"] != "hi" || g["key"] != "enc1:target-secret" {
		t.Errorf("plugin settings: %v", g)
	}
	all, _ := b.AllMenus(ctx)
	for _, items := range all {
		for _, it := range items {
			if !strings.HasPrefix(it.URL, "/") || strings.HasPrefix(it.URL, "//") {
				t.Errorf("menu item %q restored with %q", it.Label, it.URL)
			}
		}
	}
	widgets, _ := b.AllWidgets(ctx)
	var kinds []string
	for _, list := range widgets {
		for _, w := range list {
			kinds = append(kinds, w.Kind)
			if w.Kind == "recent_posts" && w.Title == "Many" && !maps.Equal(w.Config, map[string]string{"count": "5"}) {
				t.Errorf("recent posts widget: %v", w.Config)
			}
		}
	}
	if slices.Contains(kinds, "plugin:gone:widget") || slices.Contains(kinds, "plugin:greeter:hello") {
		t.Errorf("widgets: %v", kinds)
	}
	edith, _ := b.UserByUsername(ctx, "edith")
	if edith == nil || edith.Website != "" {
		t.Errorf("edith: %+v", edith)
	}
	if _, err := b.UserByUsername(ctx, "../root"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a user with an invalid name was restored: %v", err)
	}
}
