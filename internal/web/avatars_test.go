package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sap13ns/internal/media"
	"sap13ns/internal/store"
)

const gpsMarker = "GPSLatitude 40.4168 N secret"

// photoJPEG is a w × h JPEG photo with an EXIF segment that carries
// gpsMarker.
func photoJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 160, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	app1 := append([]byte("Exif\x00\x00II*\x00\x08\x00\x00\x00\x00\x00\x00\x00"), gpsMarker...)
	out := append([]byte{0xFF, 0xD8, 0xFF, 0xE1}, binary.BigEndian.AppendUint16(nil, uint16(len(app1)+2))...)
	out = append(out, app1...)
	return append(out, buf.Bytes()[2:]...)
}

// uploadPhoto posts a photo form to target.
func (e *customEnv) uploadPhoto(target string, cookie *http.Cookie, csrf, name string, data []byte) *httptest.ResponseRecorder {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if csrf != "" {
		mw.WriteField("csrf", csrf)
	}
	if name != "" {
		fw, _ := mw.CreateFormFile("photo", name)
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "http://example.test"+target, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return e.do(req, cookie)
}

// postAs posts a plain form to target.
func (e *customEnv) postAs(target string, cookie *http.Cookie, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest("POST", "http://example.test"+target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, cookie)
}

func (e *customEnv) user(name string) *store.User {
	e.t.Helper()
	u, err := e.st.UserByUsername(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

// avatarFiles are the files of an avatar that exist.
func (e *customEnv) avatarFiles(key string) [][]byte {
	e.t.Helper()
	var out [][]byte
	for _, size := range media.AvatarSizes {
		if b, err := os.ReadFile(filepath.Join(e.s.cfg.UploadsDir, filepath.FromSlash(media.AvatarFile(key, size)))); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// avatarDirFiles counts the files in the avatars directory.
func (e *customEnv) avatarDirFiles() int {
	entries, _ := os.ReadDir(filepath.Join(e.s.cfg.UploadsDir, media.AvatarDir))
	return len(entries)
}

// fakeGravatar stands for gravatar.com: the hashes it has an image for
// answer it, any other 404.
type fakeGravatar struct {
	srv    *httptest.Server
	mu     sync.Mutex
	images map[string][]byte
	seen   []*url.URL
	agents []string
}

func newFakeGravatar(t *testing.T) *fakeGravatar {
	g := &fakeGravatar{images: map[string][]byte{}}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		u := *r.URL
		g.seen, g.agents = append(g.seen, &u), append(g.agents, r.UserAgent()+"|"+r.Header.Get("Cookie"))
		img, ok := g.images[strings.TrimPrefix(r.URL.Path, "/avatar/")]
		g.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(img)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGravatar) has(hash string, img []byte) {
	g.mu.Lock()
	g.images[hash] = img
	g.mu.Unlock()
}

func (g *fakeGravatar) requests() []*url.URL {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*url.URL(nil), g.seen...)
}

// useGravatar points the server's lookups at g, on this machine.
func (e *customEnv) useGravatar(g *fakeGravatar) {
	e.s.grav.fetcher = newGravatarFetcher(g.srv.URL+"/avatar/", func(a netip.Addr) bool { return a.Unmap().IsLoopback() })
}

// newAvatarEnv is a server whose Gravatar is a fake one.
func newAvatarEnv(t *testing.T) (*customEnv, *fakeGravatar) {
	e := newCustomEnv(t)
	g := newFakeGravatar(t)
	e.useGravatar(g)
	return e, g
}

func (e *customEnv) saveSettings(edit func(*store.Settings)) {
	e.t.Helper()
	ctx := context.Background()
	st := e.s.settings()
	edit(&st)
	if err := e.st.SaveSettings(ctx, st); err != nil {
		e.t.Fatal(err)
	}
	e.s.ReloadSettings(ctx)
}

// settingsForm is the general settings form as the page shows it.
func settingsForm(st store.Settings) url.Values {
	f := url.Values{"site_title": {st.SiteTitle}, "tagline": {st.Tagline}, "language": {st.Language}, "timezone": {st.Timezone},
		"posts_per_page": {fmt.Sprint(st.PostsPerPage)}, "front_page": {st.FrontPage}, "comment_moderation": {st.CommentModeration},
		"comments_close_days": {fmt.Sprint(st.CommentsCloseDays)}, "default_role": {st.DefaultRole},
		"feed_items": {fmt.Sprint(st.FeedItems)}, "two_factor": {st.TwoFactor}, "og_cards": {st.OGCards}, "og_accent": {st.OGAccent},
		"ai_access": {st.AIAccess}, "session_days": {fmt.Sprint(st.SessionDays)}, "idle_hours": {fmt.Sprint(st.IdleHours)},
		"login_path": {st.LoginPath}}
	for k, v := range map[string]bool{"comments_enabled": st.CommentsEnabled, "analytics": !st.AnalyticsOff,
		"gravatar_users": st.GravatarUsers, "gravatar_commenters": st.GravatarCommenters, "feed_full_text": st.FeedFullText,
		"hide_admin": st.HideAdmin} {
		if v {
			f.Set(k, "1")
		}
	}
	return f
}

// TestAvatarUpload: a photo is stored in three sizes without its
// metadata, shown in the admin and on the posts of its author, replaced
// and removed with its files.
func TestAvatarUpload(t *testing.T) {
	e, g := newAvatarEnv(t)
	path := e.publishPost("Hello photos", "A post by the admin.")
	if body := e.get(path, nil).Body.String(); strings.Contains(body, "/uploads/avatars/") { // cached now
		t.Fatal("a photo before any upload")
	}

	rec := e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "me.jpg", photoJPEG(t, 900, 600))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	admin := e.user("admin")
	key := admin.Avatar
	if !media.ValidAvatarKey(key) || !strings.HasPrefix(key, fmt.Sprintf("avatars/u%d-", admin.ID)) {
		t.Fatalf("key %q", key)
	}
	files := e.avatarFiles(key)
	if len(files) != len(media.AvatarSizes) {
		t.Fatalf("%d files", len(files))
	}
	for i, b := range files {
		if bytes.Contains(b, []byte("Exif")) || bytes.Contains(b, []byte(gpsMarker)) {
			t.Errorf("size %d keeps the EXIF data", media.AvatarSizes[i])
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil || format != "jpeg" || cfg.Width != media.AvatarSizes[i] || cfg.Height != cfg.Width {
			t.Errorf("size %d: %s %dx%d %v", media.AvatarSizes[i], format, cfg.Width, cfg.Height, err)
		}
	}
	small := "/uploads/" + media.AvatarFile(key, 64) // served by nginx, like every upload
	for _, page := range []string{"/admin/profile", "/admin/users", "/admin/", fmt.Sprintf("/admin/users/%d", admin.ID)} {
		body := e.get(page, e.admin).Body.String()
		if !strings.Contains(body, `<img class="avatar-initials avatar-photo`) || !strings.Contains(body, `src="`+small+`"`) {
			t.Errorf("%s does not show the photo", page)
		}
	}
	// The page cache was invalidated: the post shows its author's photo.
	body := e.get(path, nil).Body.String()
	if !strings.Contains(body, `src="`+small+`"`) || !strings.Contains(body, `alt="" loading="lazy" decoding="async"`) {
		t.Error("the post does not show its author's photo")
	}

	// Replacing deletes the old files.
	if rec := e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "new.png", testPNG(t, color.RGBA{200, 30, 30, 255})); rec.Code != http.StatusSeeOther {
		t.Fatalf("replace: %d", rec.Code)
	}
	again := e.user("admin").Avatar
	if again == key || len(e.avatarFiles(key)) != 0 || len(e.avatarFiles(again)) != len(media.AvatarSizes) {
		t.Fatalf("replace: %q → %q, %d old files left", key, again, len(e.avatarFiles(key)))
	}

	// Removing deletes them, and shows the initials.
	if rec := e.postAs("/admin/profile/avatar/delete", e.admin, url.Values{"csrf": {e.csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove: %d", rec.Code)
	}
	if e.user("admin").Avatar != "" || e.avatarDirFiles() != 0 {
		t.Fatalf("remove: %q, %d files", e.user("admin").Avatar, e.avatarDirFiles())
	}
	if body := e.get(path, nil).Body.String(); strings.Contains(body, "/uploads/avatars/") {
		t.Error("the post still shows the photo")
	}
	if len(e.audited("user.photo_uploaded")) != 2 || len(e.audited("user.photo_removed")) != 1 {
		t.Error("not audited")
	}
	if len(g.requests()) != 0 {
		t.Errorf("example.test addresses were looked up: %v", g.requests())
	}
}

// TestAvatarUploadRefused: what is not a photo, too large, without the
// CSRF token or for another user without users.manage changes nothing.
func TestAvatarUploadRefused(t *testing.T) {
	e, _ := newAvatarEnv(t)
	admin, author := e.user("admin"), e.user("author")
	big := make([]byte, media.AvatarMaxBytes+100)
	copy(big, photoJPEG(t, 64, 64))
	for _, c := range []struct {
		name   string
		target string
		cookie *http.Cookie
		csrf   string
		file   string
		data   []byte
		want   int
	}{
		{"text renamed .jpg", "/admin/profile/avatar", e.admin, e.csrf, "photo.jpg", []byte("just some text, not a photo\n"), http.StatusUnprocessableEntity},
		{"svg", "/admin/profile/avatar", e.admin, e.csrf, "photo.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), http.StatusUnprocessableEntity},
		{"avif", "/admin/profile/avatar", e.admin, e.csrf, "photo.avif", append([]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00avifmif1"), make([]byte, 64)...), http.StatusUnprocessableEntity},
		{"no file", "/admin/profile/avatar", e.admin, e.csrf, "", nil, http.StatusUnprocessableEntity},
		{"just over 5 MB", "/admin/profile/avatar", e.admin, e.csrf, "big.jpg", big, http.StatusRequestEntityTooLarge},
		{"far over 5 MB", "/admin/profile/avatar", e.admin, e.csrf, "big.jpg", make([]byte, 8<<20), http.StatusRequestEntityTooLarge},
		{"no CSRF token", "/admin/profile/avatar", e.admin, "", "me.jpg", photoJPEG(t, 64, 64), http.StatusForbidden},
		{"another's CSRF token", "/admin/profile/avatar", e.admin, e.acsrf, "me.jpg", photoJPEG(t, 64, 64), http.StatusForbidden},
		{"author on the admin", fmt.Sprintf("/admin/users/%d/avatar", admin.ID), e.author, e.acsrf, "me.jpg", photoJPEG(t, 64, 64), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := e.uploadPhoto(c.target, c.cookie, c.csrf, c.file, c.data)
			if rec.Code != c.want {
				t.Fatalf("%d, want %d", rec.Code, c.want)
			}
			if c.want == http.StatusUnprocessableEntity && !strings.Contains(rec.Body.String(), `role="alert"`) && !strings.Contains(rec.Body.String(), "alert") {
				t.Error("no error shown")
			}
			if e.user("admin").Avatar != "" || e.avatarDirFiles() != 0 {
				t.Fatal("something was stored")
			}
		})
	}
	if body := e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "photo.jpg", []byte("text")).Body.String(); !strings.Contains(body, html.EscapeString(e.s.t("Choose a JPEG, PNG, WebP or GIF image."))) {
		t.Error("the refusal does not say why")
	}
	if rec := e.postAs(fmt.Sprintf("/admin/users/%d/avatar/delete", admin.ID), e.author, url.Values{"csrf": {e.acsrf}}); rec.Code != http.StatusForbidden {
		t.Errorf("an author removes the admin's photo: %d", rec.Code)
	}

	// An author changes their own photo; an administrator anyone's.
	if rec := e.uploadPhoto("/admin/profile/avatar", e.author, e.acsrf, "me.jpg", photoJPEG(t, 80, 80)); rec.Code != http.StatusSeeOther || e.user("author").Avatar == "" {
		t.Fatalf("an author's own photo: %d", rec.Code)
	}
	if rec := e.uploadPhoto(fmt.Sprintf("/admin/users/%d/avatar", author.ID), e.admin, e.csrf, "them.jpg", photoJPEG(t, 80, 80)); rec.Code != http.StatusSeeOther {
		t.Fatalf("the admin on the author: %d", rec.Code)
	}
	key := e.user("author").Avatar
	if rec := e.postAs(fmt.Sprintf("/admin/users/%d/avatar/delete", author.ID), e.admin, url.Values{"csrf": {e.csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("the admin removes the author's: %d", rec.Code)
	}
	if e.user("author").Avatar != "" || len(e.avatarFiles(key)) != 0 {
		t.Error("the author's photo is still there")
	}
}

// avatarLimits is the plan of a site of a network: limit bytes of storage.
type avatarLimits struct {
	SiteLimits // nil: only the storage is used
	mu         sync.Mutex
	used       int64
	limit      int64
}

func (l *avatarLimits) Reserve(_ context.Context, n int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used+n > l.limit {
		return media.ErrQuota
	}
	l.used += n
	return nil
}

func (l *avatarLimits) Release(_ context.Context, n int64) {
	l.mu.Lock()
	l.used -= n
	l.mu.Unlock()
}

func (l *avatarLimits) StorageQuota(context.Context) (int64, error) { return l.limit, nil }

// TestAvatarStorage: on a network a photo is reserved against the site's
// storage before it is written, and removing it gives the bytes back.
func TestAvatarStorage(t *testing.T) {
	e, _ := newAvatarEnv(t)
	l := &avatarLimits{limit: 1000}
	e.s.limits, e.s.media.Quota = l, l

	rec := e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "me.jpg", photoJPEG(t, 300, 300))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), html.EscapeString(e.s.storageFull(nil))) {
		t.Fatalf("over the plan: %d", rec.Code)
	}
	if l.used != 0 || e.avatarDirFiles() != 0 || e.user("admin").Avatar != "" {
		t.Fatalf("over the plan: %d bytes, %d files", l.used, e.avatarDirFiles())
	}

	l.limit = 10 << 20
	if rec := e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "me.jpg", photoJPEG(t, 300, 300)); rec.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d", rec.Code)
	}
	var n int64
	for _, b := range e.avatarFiles(e.user("admin").Avatar) {
		n += int64(len(b))
	}
	if n == 0 || l.used != n {
		t.Fatalf("reserved %d for %d bytes", l.used, n)
	}
	e.postAs("/admin/profile/avatar/delete", e.admin, url.Values{"csrf": {e.csrf}})
	if l.used != 0 {
		t.Fatalf("%d bytes still reserved", l.used)
	}
}

// TestGravatarUsers: the server looks a user up with the hash of their
// address only, through its own client; 404 keeps the initials, an image
// is served from the site; an uploaded photo, a reserved domain or the
// setting off send nothing; switching it off deletes the copies.
func TestGravatarUsers(t *testing.T) {
	e, g := newAvatarEnv(t)
	ctx := context.Background()
	author := e.user("author")
	e.exec(`UPDATE users SET email = 'Ana.Lopez@Photos.Sap13ns.Site' WHERE username = 'author'`)
	sum := sha256.Sum256([]byte("ana.lopez@photos.sap13ns.site"))
	hash := hex.EncodeToString(sum[:])
	if gravatarHash("  Ana.Lopez@Photos.Sap13ns.Site ") != hash {
		t.Fatal("not the SHA-256 of the trimmed, lower-cased address")
	}

	// 404: no Gravatar, the initials stay.
	found, err := e.s.lookupUserGravatar(ctx, author.ID)
	if err != nil || found {
		t.Fatalf("404: %v %v", found, err)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].Path != "/avatar/"+hash || reqs[0].Query().Get("s") != "256" || reqs[0].Query().Get("d") != "404" {
		t.Fatalf("requests: %v", reqs)
	}
	for _, leak := range []string{"@", "ana", "lopez", "sap13ns.site"} {
		if strings.Contains(strings.ToLower(reqs[0].String()), leak) {
			t.Errorf("the request carries %q: %s", leak, reqs[0])
		}
	}
	if a := g.agents[0]; !strings.HasPrefix(a, "sap13ns/") || strings.Contains(a, "|"+e.author.Value) || !strings.HasSuffix(a, "|") {
		t.Errorf("user agent and cookies sent: %q", a)
	}
	if u := e.user("author"); u.Gravatar != "" || e.s.userAvatar(u).URL != "" {
		t.Fatal("a 404 gave a photo")
	}

	// 200: the image is re-encoded and served from the site.
	g.has(hash, photoJPEG(t, 300, 200))
	if body := e.postAs("/admin/profile/avatar/gravatar", e.author, url.Values{"csrf": {e.acsrf}}); body.Code != http.StatusSeeOther {
		t.Fatalf("refresh: %d", body.Code)
	}
	u := e.user("author")
	if !strings.HasPrefix(u.Gravatar, fmt.Sprintf("avatars/g%d-", u.ID)) {
		t.Fatalf("gravatar %q", u.Gravatar)
	}
	files0, files := u.Gravatar, e.avatarFiles(u.Gravatar)
	if len(files) != len(media.AvatarSizes) || bytes.Contains(files[2], []byte(gpsMarker)) {
		t.Fatalf("%d files, or the EXIF data kept", len(files))
	}
	if body := e.get("/admin/profile", e.author).Body.String(); !strings.Contains(body, `src="/uploads/`+media.AvatarFile(u.Gravatar, 64)+`"`) {
		t.Error("the profile does not show the Gravatar")
	}

	// An uploaded photo wins: the copy goes, and nothing is asked.
	n := len(g.requests())
	e.uploadPhoto("/admin/profile/avatar", e.author, e.acsrf, "me.jpg", photoJPEG(t, 90, 90))
	if u := e.user("author"); u.Gravatar != "" || len(e.avatarFiles(files0)) != 0 {
		t.Fatal("the Gravatar copy stayed")
	}
	if found, _ := e.s.lookupUserGravatar(ctx, author.ID); found || len(g.requests()) != n {
		t.Fatal("a user with a photo was looked up")
	}
	e.postAs("/admin/profile/avatar/delete", e.author, url.Values{"csrf": {e.acsrf}})
	e.waitJobs() // removing the photo looks the Gravatar up again
	if u := e.user("author"); u.Gravatar == "" || len(g.requests()) != n+1 {
		t.Fatalf("after removing the photo: %q, %d requests", u.Gravatar, len(g.requests())-n)
	}

	// Reserved domains are never looked up.
	if found, err := e.s.lookupUserGravatar(ctx, e.user("admin").ID); found || err != nil || len(g.requests()) != n+1 {
		t.Fatal("an example.test address was looked up")
	}

	// The background pass: due users only, a week apart.
	e.exec(`UPDATE users SET gravatar_checked_at = now() WHERE username <> 'author'`)
	e.exec(`UPDATE users SET gravatar_checked_at = NULL WHERE username = 'author'`)
	e.s.gravatarPass(ctx, time.Now())
	e.s.gravatarPass(ctx, time.Now())
	if len(g.requests()) != n+2 {
		t.Fatalf("passes: %d requests, want 1", len(g.requests())-n-1)
	}
	e.s.gravatarPass(ctx, time.Now().Add(store.GravatarRefresh+time.Hour))
	if len(g.requests()) != n+3 {
		t.Fatal("not rechecked after a week")
	}

	// Switched off from the settings page: the copies go, nothing is sent.
	copyKey := e.user("author").Gravatar
	form := settingsForm(e.s.settings())
	form.Del("gravatar_users")
	form.Set("csrf", e.csrf)
	if rec := e.postAs("/admin/settings", e.admin, form); rec.Code != http.StatusSeeOther {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	if e.s.settings().GravatarUsers {
		t.Fatal("still on")
	}
	if e.user("author").Gravatar != "" || len(e.avatarFiles(copyKey)) != 0 {
		t.Fatal("switched off, the copy stayed")
	}
	n = len(g.requests())
	e.s.lookupUserGravatar(ctx, author.ID)
	e.s.gravatarPass(ctx, time.Now().Add(30*24*time.Hour))
	e.postAs("/admin/profile/avatar/gravatar", e.author, url.Values{"csrf": {e.acsrf}})
	e.s.gravatarSoon(author.ID)
	e.waitJobs()
	if len(g.requests()) != n {
		t.Fatalf("switched off, %d requests", len(g.requests())-n)
	}
}

// TestGravatarFollowsEmail: a new address drops the old address's copy
// and looks the new one up; a deleted user's files go.
func TestGravatarFollowsEmail(t *testing.T) {
	e, g := newAvatarEnv(t)
	ctx := context.Background()
	old, next := "first@photos.sap13ns.site", "second@photos.sap13ns.site"
	g.has(gravatarHash(old), photoJPEG(t, 100, 100))
	rec := e.postAs("/admin/users/new", e.admin, url.Values{"csrf": {e.csrf}, "username": {"maria"}, "email": {old},
		"role": {"author"}, "password": {"a long enough password 1"}, "password_confirm": {"a long enough password 1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	u := e.user("maria")
	if u.Gravatar == "" {
		t.Fatal("a new user was not looked up")
	}
	first := u.Gravatar

	rec = e.postAs(fmt.Sprintf("/admin/users/%d", u.ID), e.admin, url.Values{"csrf": {e.csrf}, "email": {next}, "display_name": {"María"},
		"role": {"author"}, "status": {"active"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	u = e.user("maria")
	if u.Gravatar != "" || len(e.avatarFiles(first)) != 0 {
		t.Fatalf("the old address's copy stayed: %q", u.Gravatar)
	}
	last := g.requests()[len(g.requests())-1]
	if last.Path != "/avatar/"+gravatarHash(next) {
		t.Fatalf("the new address was not looked up: %v", last)
	}

	// Store: a changed address clears the copy in the same update, a
	// lookup of the old address arriving late is not recorded.
	g.has(gravatarHash(next), photoJPEG(t, 100, 100))
	if found, err := e.s.lookupUserGravatar(ctx, u.ID); !found || err != nil {
		t.Fatal(found, err)
	}
	if _, ok, err := e.st.SetUserGravatar(ctx, u.ID, old, "avatars/g1-0123456789abcdef0123.jpg"); ok || err != nil {
		t.Fatal("a lookup of the old address was recorded")
	}

	e.uploadPhoto(fmt.Sprintf("/admin/users/%d/avatar", u.ID), e.admin, e.csrf, "m.jpg", photoJPEG(t, 100, 100))
	u = e.user("maria")
	photo := u.Avatar
	e.waitJobs()
	if rec := e.postAs(fmt.Sprintf("/admin/users/%d/delete", u.ID), e.admin, url.Values{"csrf": {e.csrf}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("delete: %d", rec.Code)
	}
	if len(e.avatarFiles(photo)) != 0 || e.avatarDirFiles() != 0 {
		t.Fatalf("a deleted user's files stayed: %d", e.avatarDirFiles())
	}
}

// TestGravatarCommenters: off by default; on, commenters' images are
// looked up in the background, never for spam, into a bounded cache; a
// registered user who comments shows their own photo; off again, the
// copies go.
func TestGravatarCommenters(t *testing.T) {
	e, g := newAvatarEnv(t)
	ctx := context.Background()
	path := e.publishPost("Commented post", "Say something.")
	post, err := e.st.PostBySlug(ctx, store.TypePost, e.s.mainLang(), "commented-post")
	if err != nil {
		t.Fatal(err)
	}
	visitor, spammer := "visitor@photos.sap13ns.site", "spammer@photos.sap13ns.site"
	g.has(gravatarHash(visitor), photoJPEG(t, 120, 120))
	g.has(gravatarHash(spammer), photoJPEG(t, 120, 120))
	author := e.user("author")
	for _, c := range []store.NewComment{
		{PostID: post.ID, AuthorName: "Vera Visitor", AuthorEmail: visitor, Content: "Nice post.", Status: "approved"},
		{PostID: post.ID, AuthorName: "Sam Spam", AuthorEmail: spammer, Content: "Buy now.", Status: "spam"},
		{PostID: post.ID, UserID: &author.ID, AuthorName: "author", AuthorEmail: author.Email, Content: "Thanks!", Status: "approved"},
	} {
		if _, err := e.st.CreateComment(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	e.uploadPhoto("/admin/profile/avatar", e.author, e.acsrf, "me.jpg", photoJPEG(t, 100, 100))
	authorPhoto := "/uploads/" + media.AvatarFile(e.user("author").Avatar, 64)

	body := e.get(path, nil).Body.String()
	e.get("/admin/comments", e.admin)
	e.get("/admin/comments?status=spam", e.admin)
	e.waitJobs()
	if len(g.requests()) != 0 || strings.Contains(body, "/uploads/avatars/c-") {
		t.Fatalf("off by default, %d requests", len(g.requests()))
	}
	if !strings.Contains(body, `src="`+authorPhoto+`"`) || strings.Count(body, `class="sap-avatar comment-avatar"`) != 1 {
		t.Error("the registered commenter's photo is not shown")
	}
	if !strings.Contains(body, `<span class="comment-avatar" aria-hidden="true">VV</span>`) {
		t.Error("the visitor's initials are not shown")
	}

	e.saveSettings(func(st *store.Settings) { st.GravatarCommenters = true })
	e.s.pages.Invalidate()
	e.get(path, nil)
	e.get("/admin/comments?status=spam", e.admin)
	e.waitJobs()
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].Path != "/avatar/"+gravatarHash(visitor) {
		t.Fatalf("requests: %v", reqs)
	}
	cached, err := e.st.CommenterGravatars(ctx, []string{gravatarHash(visitor), gravatarHash(spammer)})
	if err != nil || len(cached) != 1 || cached[gravatarHash(visitor)].Path == "" {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	copyKey := cached[gravatarHash(visitor)].Path
	body = e.get(path, nil).Body.String()
	if !strings.Contains(body, `src="/uploads/`+media.AvatarFile(copyKey, 64)+`"`) {
		t.Error("the visitor's Gravatar is not shown")
	}
	if !strings.Contains(e.get("/admin/comments", e.admin).Body.String(), `src="/uploads/`+media.AvatarFile(copyKey, 64)+`"`) {
		t.Error("the moderation list does not show it")
	}
	e.get(path, nil)
	e.waitJobs()
	if len(g.requests()) != 1 {
		t.Error("a cached lookup was made again")
	}

	// The cache is bounded: the least recently shown go first.
	ck, err := e.s.media.SaveAvatar(ctx, mustAvatar(t), "c")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutCommenterGravatar(ctx, strings.Repeat("a", 64), ck, 10); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE gravatar_cache SET used_at = now() - interval '1 day' WHERE hash = '` + strings.Repeat("a", 64) + `'`)
	evicted, err := e.st.EvictCommenterGravatars(ctx, 1, 1<<30, time.Now().Add(-commenterUnused))
	if err != nil || len(evicted) != 1 || evicted[0] != ck {
		t.Fatalf("evicted %v %v", evicted, err)
	}
	e.s.media.RemoveAvatar(ctx, ck)

	// Off again: the copies go, the registered user's photo stays.
	e.saveSettings(func(st *store.Settings) { st.GravatarCommenters = false })
	e.s.gravatarSettingsChanged(ctx, store.Settings{GravatarUsers: true, GravatarCommenters: true}, e.s.settings())
	if cached, _ := e.st.CommenterGravatars(ctx, []string{gravatarHash(visitor)}); len(cached) != 0 || len(e.avatarFiles(copyKey)) != 0 {
		t.Fatal("the commenters' copies stayed")
	}
	body = e.get(path, nil).Body.String()
	if strings.Contains(body, "/uploads/avatars/c-") || !strings.Contains(body, `src="`+authorPhoto+`"`) {
		t.Error("off again: wrong photos")
	}
}

func mustAvatar(t *testing.T) *media.Avatar {
	t.Helper()
	a, err := media.MakeAvatar(context.Background(), bytes.NewReader(photoJPEG(t, 70, 70)), 0)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestGravatarGuarded: the real client goes through the SSRF guard, which
// refuses this machine; development points it at a stand-in only in
// development mode.
func TestGravatarGuarded(t *testing.T) {
	g := newFakeGravatar(t)
	f := newGravatarFetcher(g.srv.URL+"/avatar/", nil)
	if _, err := f.fetch(context.Background(), strings.Repeat("0", 64)); err == nil || len(g.requests()) != 0 {
		t.Fatalf("the guard let a loopback request through: %v", err)
	}
	if defaultGravatar().base != "https://gravatar.com/avatar/" {
		t.Error(defaultGravatar().base)
	}
	if tr, ok := defaultGravatar().client.Transport.(*http.Transport); !ok || tr.Proxy != nil || tr.DialContext == nil {
		t.Error("the default client is not the guarded one")
	}

	e := newCustomEnv(t)
	if e.s.gravatarFetcher() != defaultGravatar() {
		t.Error("the server does not use the guarded client")
	}
	// (one test database at a time: the same server, configured again)
	e.s.cfg.Dev, e.s.cfg.DevGravatarURL = false, g.srv.URL+"/avatar"
	if e.s.gravatarFetcher() != defaultGravatar() {
		t.Error("the stand-in is used outside development")
	}
	e.s.cfg.Dev = true
	if f := e.s.gravatarFetcher(); f.base != g.srv.URL+"/avatar/" {
		t.Errorf("development: %q", f.base)
	}
}

// TestAvatarInThemes: every built-in theme, and the corporate one, shows
// the author's photo on a post with shared/avatar.
func TestAvatarInThemes(t *testing.T) {
	e, _ := newAvatarEnv(t)
	if err := os.CopyFS(filepath.Join(e.s.cfg.ThemesDir, "sap13ns"), os.DirFS("../../site/themes/sap13ns")); err != nil {
		t.Fatal(err)
	}
	if err := e.s.themes.Load(); err != nil {
		t.Fatal(err)
	}
	path := e.publishPost("Themed photo", "Body.")
	e.uploadPhoto("/admin/profile/avatar", e.admin, e.csrf, "me.jpg", photoJPEG(t, 200, 200))
	small := "/uploads/" + media.AvatarFile(e.user("admin").Avatar, 64)
	seen := map[string]bool{}
	for _, th := range e.s.themes.List() {
		seen[th.Slug] = true
		e.saveSettings(func(st *store.Settings) { st.ActiveTheme = th.Slug })
		e.s.contentChanged()
		body := e.get(path, nil).Body.String()
		img := `<img class="sap-avatar `
		i := strings.Index(body, img)
		if i < 0 || !strings.Contains(body, `src="`+small+`"`) {
			t.Errorf("%s: no author photo", th.Slug)
			continue
		}
		tag := body[i : i+strings.Index(body[i:], ">")]
		for _, want := range []string{`alt=""`, `loading="lazy"`, `decoding="async"`, `width="`, `height="`, `srcset="`} {
			if !strings.Contains(tag, want) {
				t.Errorf("%s: the photo lacks %s: %s", th.Slug, want, tag)
			}
		}
	}
	for _, slug := range []string{"apple", "bento", "dashboard", "glassmorphism", "neumorphism", "onepage", "sap13ns"} {
		if !seen[slug] {
			t.Errorf("theme %s not checked", slug)
		}
	}
}
