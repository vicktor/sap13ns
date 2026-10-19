package web

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/backup"
	"sap13ns/internal/config"
	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
)

var backupLink = regexp.MustCompile(`href="(/admin/tools/backups/download/[^"]+)"`)

const backupPassword = "correct horse battery staple"

// backupEnv is a site whose backups go to a temporary directory, and whose
// administrator has a password.
func backupEnv(t *testing.T) *customEnv {
	t.Helper()
	e := newCustomEnv(t)
	e.s.cfg.SiteBackups = config.SiteBackups{Root: t.TempDir(), KeepDaily: 7, KeepWeekly: 4}
	e.s.cfg.SiteBackups.Dir = filepath.Join(e.s.cfg.SiteBackups.Root, "site")
	hash, err := auth.HashPassword(backupPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetPassword(context.Background(), e.userID("admin"), hash); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *customEnv) postBackups(action string, cookie *http.Cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/backups"+action, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, cookie)
}

// backUp makes a backup from the page and returns its download link.
func (e *customEnv) backUp() string {
	e.t.Helper()
	if rec := e.postBackups("", e.admin, e.csrf, nil); rec.Code != http.StatusSeeOther {
		e.t.Fatalf("back up now: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	page := e.get("/admin/tools/backups", e.admin).Body.String()
	if !strings.Contains(page, `class="alert" data-variant="success"`) {
		i := strings.Index(page, `class="alert"`)
		e.t.Fatalf("the page does not show the backup made: %.600s", page[max(i, 0):])
	}
	m := backupLink.FindStringSubmatch(page) // the newest first
	if m == nil {
		e.t.Fatal("no download link")
	}
	return html.UnescapeString(m[1])
}

func (e *customEnv) audited(action string) []store.AuditEntry {
	e.t.Helper()
	entries, _, err := e.st.ListAudit(context.Background(), action, 50, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return entries
}

func TestBackupsPage(t *testing.T) {
	e := backupEnv(t)
	e.publishPost("Kept post", "It was there when the backup was made.")

	if rec := e.get("/admin/tools/backups", e.admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), `action="/admin/tools/backups"`) {
		t.Fatalf("backups page: %d", rec.Code)
	}
	if rec := e.get("/admin/tools/backups", e.author); rec.Code != http.StatusForbidden {
		t.Errorf("an author sees the backups page: %d", rec.Code)
	}
	if rec := e.postBackups("", e.author, e.acsrf, nil); rec.Code != http.StatusForbidden {
		t.Errorf("an author backs up: %d", rec.Code)
	}
	if rec := e.postBackups("", e.admin, "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("a backup without the CSRF token: %d", rec.Code)
	}
	if rec := e.postBackups("", e.admin, e.csrf, url.Values{"wp_base": {"javascript:alert(1)"}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a bad WordPress address: %d", rec.Code)
	}
	if list, _ := sitebackup.List(e.s.cfg.SiteBackups.Dir); len(list) != 0 {
		t.Fatalf("refused requests made backups: %v", list)
	}

	link := e.backUp()
	if n := len(e.audited("backup.created")); n != 1 {
		t.Errorf("%d backup.created entries", n)
	}
	rec := e.get(link, e.admin)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var wxr bool
	for _, f := range zr.File {
		wxr = wxr || f.Name == sitebackup.NameWXR
	}
	if !wxr {
		t.Error("the download has no export.xml")
	}
	if n := len(e.audited("backup.downloaded")); n != 1 {
		t.Errorf("%d backup.downloaded entries", n)
	}

	// The link is for this session only.
	sameUser := e.newSession("admin")
	other, _ := e.login("admin2", auth.RoleAdmin)
	for name, c := range map[string]*http.Cookie{"another session of the admin": sameUser, "another admin": other} {
		if rec := e.get(link, c); rec.Code != http.StatusForbidden {
			t.Errorf("%s downloads: %d", name, rec.Code)
		}
	}
	if rec := e.get(link, e.author); rec.Code != http.StatusForbidden {
		t.Errorf("an author downloads: %d", rec.Code)
	}
	if rec := e.get(link, nil); rec.Code == 200 {
		t.Errorf("anonymous download: %d", rec.Code)
	}
	if rec := e.get(link[:len(link)-2]+"xx", e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("tampered token: %d", rec.Code)
	}
	if rec := e.get(strings.Replace(link, "/download/", "/download/x", 1), e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("another name with the token: %d", rec.Code)
	}

	// Encrypted on the server, plain in the download.
	e.s.cfg.SiteBackups.Passphrase = "a passphrase long enough for the test"
	link = e.backUp()
	name := strings.TrimPrefix(link[:strings.Index(link, "?")], "/admin/tools/backups/download/")
	if !strings.HasSuffix(name, ".enc") {
		t.Fatalf("not an encrypted backup: %s", name)
	}
	raw, err := os.ReadFile(filepath.Join(e.s.cfg.SiteBackups.Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if !backup.IsEncrypted(bufio.NewReader(bytes.NewReader(raw))) || bytes.Contains(raw, []byte(sitebackup.NameWXR)) {
		t.Fatal("the file on the server is not encrypted")
	}
	rec = e.get(link, e.admin)
	if _, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len())); rec.Code != 200 || err != nil {
		t.Fatalf("decrypted download: %d %v", rec.Code, err)
	}
	if cd := rec.Header().Get("Content-Disposition"); strings.Contains(cd, ".enc") {
		t.Errorf("download name: %s", cd)
	}
}

// newSession signs a user in again, in a second session.
func (e *customEnv) newSession(username string) *http.Cookie {
	e.t.Helper()
	token, csrf := auth.NewToken(32), auth.NewToken(24)
	if err := e.st.CreateSession(context.Background(), store.Session{TokenHash: auth.HashToken(token), UserID: e.userID(username),
		CSRFToken: csrf, ExpiresAt: time.Now().Add(time.Hour), UserAgent: "test"}); err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: e.s.cookieName(), Value: token}
}

func TestBackupsRestore(t *testing.T) {
	ctx := context.Background()
	e := backupEnv(t)
	e.publishPost("Kept post", "It was there when the backup was made.")
	link := e.backUp()
	name := strings.TrimPrefix(link[:strings.Index(link, "?")], "/admin/tools/backups/download/")

	// What the restore replaces.
	e.publishPost("Later post", "Written after the backup.")
	list, _, err := e.st.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Limit: 10})
	if err != nil || len(list) != 2 {
		t.Fatalf("posts before the restore: %d %v", len(list), err)
	}

	form := func(confirm, password string) url.Values {
		return url.Values{"name": {name}, "confirm": {confirm}, "current_password": {password}}
	}
	label := e.s.cfg.BaseURL.Hostname()
	if rec := e.postBackups("/restore", e.author, e.acsrf, form(label, backupPassword)); rec.Code != http.StatusForbidden {
		t.Errorf("an author restores: %d", rec.Code)
	}
	if rec := e.postBackups("/restore", e.admin, "", form(label, backupPassword)); rec.Code != http.StatusForbidden {
		t.Errorf("a restore without the CSRF token: %d", rec.Code)
	}
	if rec := e.postBackups("/restore", e.admin, e.csrf, form("another-site", backupPassword)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a restore with the wrong name: %d", rec.Code)
	}
	if rec := e.postBackups("/restore", e.admin, e.csrf, form(label, "")); rec.Code != http.StatusUnauthorized {
		t.Errorf("a restore without the password: %d", rec.Code)
	}
	if rec := e.postBackups("/restore", e.admin, e.csrf, form(label, "not the password")); rec.Code != http.StatusUnauthorized {
		t.Errorf("a restore with a wrong password: %d", rec.Code)
	}
	if rec := e.postBackups("/restore", e.admin, e.csrf, url.Values{"name": {"../../etc/passwd"}, "confirm": {label},
		"current_password": {backupPassword}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a restore of a file not in the list: %d", rec.Code)
	}
	if n := len(e.audited("backup.restore_refused")); n != 2 {
		t.Errorf("%d backup.restore_refused entries", n)
	}
	if list, _, _ := e.st.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Limit: 10}); len(list) != 2 {
		t.Fatalf("a refused restore changed the site: %d posts", len(list))
	}

	// The portal of a network is never restored from a site backup.
	e.s.backupHooks = fakeBackupHooks{portal: true}
	if rec := e.postBackups("/restore", e.admin, e.csrf, form(label, backupPassword)); rec.Code != http.StatusForbidden {
		t.Errorf("a restore of the portal: %d", rec.Code)
	}
	e.s.backupHooks = nil

	if rec := e.postBackups("/restore", e.admin, e.csrf, form(label, backupPassword)); rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	list, _, err = e.st.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Limit: 10})
	if err != nil || len(list) != 1 || list[0].Title != "Kept post" {
		t.Fatalf("posts after the restore: %+v %v", list, err)
	}
	page := e.get("/admin/tools/backups", e.admin)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `class="alert" data-variant="success"`) {
		t.Fatalf("the page after the restore: %d", page.Code)
	}
	if n := len(e.audited("backup.restored")); n != 1 {
		t.Errorf("%d backup.restored entries", n)
	}
	archives, _ := sitebackup.List(e.s.cfg.SiteBackups.Dir)
	var safety bool
	for _, a := range archives {
		safety = safety || a.PreRestore
	}
	if !safety {
		t.Errorf("no safety backup: %+v", archives)
	}
	// The public pages show the restored content at once.
	if rec := e.get("/", nil); !strings.Contains(rec.Body.String(), "Kept post") || strings.Contains(rec.Body.String(), "Later post") {
		t.Errorf("home page after the restore: %d", rec.Code)
	}

	// Each restore makes a safety backup: a second within the cooldown is
	// refused, and makes none.
	restore := func() *httptest.ResponseRecorder {
		t.Helper()
		rec := e.postBackups("/restore", e.admin, e.csrf, form(label, backupPassword))
		e.waitJobs()
		return rec
	}
	if rec := restore(); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a second restore within the cooldown: %d", rec.Code)
	}
	if n := safetyBackups(t, e); n != 1 {
		t.Errorf("%d safety backups after a refused restore", n)
	}
	// The pace is on disk, with the backups: a site reloaded after its
	// restore (or when idle) keeps it.
	dir := e.s.cfg.SiteBackups.Dir
	if wait, err := sitebackup.RestoreWait(dir, restorePace, time.Now()); err != nil || wait <= restorePace.Cooldown-time.Minute {
		t.Errorf("wait after a restore: %v %v", wait, err)
	}
	cooledDown := func() {
		t.Helper()
		st, err := sitebackup.ReadStatus(dir)
		if err != nil || len(st.Restores) == 0 {
			t.Fatalf("status: %+v %v", st, err)
		}
		for i := range st.Restores {
			st.Restores[i] = st.Restores[i].Add(-restorePace.Cooldown)
		}
		b, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(dir, "status.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Past it, at most 3 an hour.
	for i := range 2 {
		cooledDown()
		if rec := restore(); rec.Code != http.StatusSeeOther {
			t.Fatalf("restore %d after the cooldown: %d", i+2, rec.Code)
		}
	}
	cooledDown()
	if rec := restore(); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a fourth restore in an hour: %d", rec.Code)
	}
	if n := safetyBackups(t, e); n != sitebackup.SafetyKeep {
		t.Errorf("%d safety backups after 3 restores", n)
	}
	if n := len(e.audited("backup.restored")); n != 3 {
		t.Errorf("%d backup.restored entries", n)
	}
	if st, _ := sitebackup.ReadStatus(dir); len(st.Restores) != 3 {
		t.Errorf("restores recorded: %v", st.Restores)
	}
}

func safetyBackups(t *testing.T, e *customEnv) int {
	t.Helper()
	list, err := sitebackup.List(e.s.cfg.SiteBackups.Dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, a := range list {
		if a.PreRestore {
			n++
		}
	}
	return n
}

// TestBackupsQuota: on a network, a backup larger than the storage of the
// site's plan is refused, and so is a restore whose safety backup would
// be; what the backups keep stays within twice the storage.
func TestBackupsQuota(t *testing.T) {
	e := backupEnv(t)
	e.publishPost("Kept post", "It was there when the backup was made.")
	link := e.backUp()
	name := strings.TrimPrefix(link[:strings.Index(link, "?")], "/admin/tools/backups/download/")
	first, err := sitebackup.Find(e.s.cfg.SiteBackups.Dir, name)
	if err != nil {
		t.Fatal(err)
	}

	e.s.limits = quotaLimits{quota: 1 << 10}
	if page := e.get("/admin/tools/backups", e.admin).Body.String(); !strings.Contains(page, filesize(1<<10)) {
		t.Errorf("the page does not show the plan's storage")
	}
	if rec := e.postBackups("", e.admin, e.csrf, nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("back up now: %d", rec.Code)
	}
	e.waitJobs()
	if res := e.s.backups.last; res == nil || res.Error != e.s.t("The backup would be larger than the storage of the site's plan. Remove media you no longer need, or move to a larger plan.") {
		t.Errorf("an oversize backup: %+v", res)
	}
	if list, _ := sitebackup.List(e.s.cfg.SiteBackups.Dir); len(list) != 1 {
		t.Errorf("an oversize backup was written: %v", list)
	}
	if st, _ := sitebackup.ReadStatus(e.s.cfg.SiteBackups.Dir); st.Result != sitebackup.ResultSkipped || st.Reason != sitebackup.ReasonQuota {
		t.Errorf("status: %+v", st)
	}
	e.s.backups.mu.Lock()
	e.s.backups.last = nil // the page shows the status then
	e.s.backups.mu.Unlock()
	why := e.s.t("The latest backup, %s, was skipped: the site is larger than the storage of its plan.", "\x00")
	why = html.EscapeString(why[strings.Index(why, "\x00")+1:])
	if page := e.get("/admin/tools/backups", e.admin).Body.String(); !strings.Contains(page, why) {
		t.Errorf("the page does not say why the backup was skipped (%q)", why)
	}

	// A restore of a backup larger than the plan: nothing changes.
	rec := e.postBackups("/restore", e.admin, e.csrf, url.Values{"name": {name}, "confirm": {e.s.cfg.BaseURL.Hostname()},
		"current_password": {backupPassword}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", rec.Code)
	}
	e.waitJobs()
	if res := e.s.backups.last; res == nil || !res.Restore || res.Error != e.s.t("The backup would be larger than the storage of the site's plan. Remove media you no longer need, or move to a larger plan.") {
		t.Errorf("a restore larger than the plan: %+v", res)
	}
	if n := safetyBackups(t, e); n != 0 {
		t.Errorf("%d safety backups", n)
	}

	// What the backups keep stays within twice the storage (Prune is
	// tested in sitebackup); a single site has no bound.
	e.s.limits = quotaLimits{quota: first.Size}
	_, maxBackup, keep, err := e.s.backupLimits(context.Background())
	if err != nil || maxBackup != first.Size || keep.MaxBytes != 2*first.Size || keep.Daily != 7 || keep.Weekly != 4 {
		t.Errorf("limits: %d %+v %v", maxBackup, keep, err)
	}
	e.s.limits = nil
	if _, maxBackup, keep, err := e.s.backupLimits(context.Background()); err != nil || maxBackup != 0 || keep.MaxBytes != 0 {
		t.Errorf("limits of a single site: %d %+v %v", maxBackup, keep, err)
	}
}

type fakeBackupHooks struct{ portal bool }

func (h fakeBackupHooks) BackupIdentity() BackupIdentity {
	return BackupIdentity{ID: 1, Label: "example.test", Network: "example.test", Portal: h.portal}
}
func (fakeBackupHooks) SiteRestored(context.Context) {}
