package web

import (
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
	"strconv"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
	"sap13ns/internal/transfer"
)

var transferLink = regexp.MustCompile(`href="(/admin/tools/transfer/download/[^"]+)"`)

const transferPass = "a passphrase for the move"

func (e *customEnv) postTransfer(action string, cookie *http.Cookie, csrf string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	if csrf != "" {
		form.Set("csrf", csrf)
	}
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/transfer"+action, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.do(req, cookie)
}

// uploadTransfer sends data to the upload endpoint in chunks of n bytes.
func (e *customEnv) uploadTransfer(data []byte, n int) {
	e.t.Helper()
	id := ""
	for off := 0; off < len(data); {
		q := url.Values{"offset": {strconv.Itoa(off)}, "size": {strconv.Itoa(len(data))}}
		if id == "" {
			q.Set("name", "site.transfer.enc")
		} else {
			q.Set("id", id)
		}
		req := httptest.NewRequest("POST", "http://example.test/admin/tools/transfer/upload?"+q.Encode(), bytes.NewReader(data[off:min(off+n, len(data))]))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-CSRF-Token", e.csrf)
		rec := e.do(req, e.admin)
		var reply uploadReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); rec.Code != 200 || err != nil {
			e.t.Fatalf("upload at %d: %d %s", off, rec.Code, rec.Body.String())
		}
		id, off = reply.ID, int(reply.Received)
	}
}

func TestTransferMoveSite(t *testing.T) {
	ctx := context.Background()
	e := backupEnv(t)
	e.publishPost("Moved post", "It travels with the site.")
	if err := os.WriteFile(filepath.Join(e.s.cfg.UploadsDir, "picture.txt"), []byte("an upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	siteKey := append([]byte(nil), e.s.cfg.SecretKey...)

	// Only administrators, with the CSRF token, their password and a good
	// passphrase.
	if rec := e.get("/admin/tools/transfer", e.admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), `action="/admin/tools/transfer/export"`) {
		t.Fatalf("page: %d", rec.Code)
	}
	if strings.Contains(e.get("/admin/tools/transfer", e.admin).Body.String(), `data-transfer-upload`) {
		t.Error("a site without a server key offers to import")
	}
	if rec := e.get("/admin/tools/transfer", e.author); rec.Code != http.StatusForbidden {
		t.Errorf("an author sees the page: %d", rec.Code)
	}
	good := url.Values{"passphrase": {transferPass}, "passphrase2": {transferPass}, "current_password": {backupPassword}}
	if rec := e.postTransfer("/export", e.author, e.acsrf, good); rec.Code != http.StatusForbidden {
		t.Errorf("an author exports: %d", rec.Code)
	}
	if rec := e.postTransfer("/export", e.admin, "", good); rec.Code != http.StatusForbidden {
		t.Errorf("an export without the CSRF token: %d", rec.Code)
	}
	for name, f := range map[string]url.Values{
		"short":     {"passphrase": {"short"}, "passphrase2": {"short"}, "current_password": {backupPassword}},
		"different": {"passphrase": {transferPass}, "passphrase2": {transferPass + "x"}, "current_password": {backupPassword}},
		"spaces":    {"passphrase": {" " + transferPass}, "passphrase2": {" " + transferPass}, "current_password": {backupPassword}},
	} {
		if rec := e.postTransfer("/export", e.admin, e.csrf, f); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s passphrase: %d", name, rec.Code)
		}
	}
	bad := url.Values{"passphrase": {transferPass}, "passphrase2": {transferPass}, "current_password": {"wrong"}}
	if rec := e.postTransfer("/export", e.admin, e.csrf, bad); rec.Code != http.StatusUnauthorized {
		t.Errorf("an export with a wrong password: %d", rec.Code)
	}
	e.s.transfers.freeSpace = func(string) (int64, error) { return 10, nil }
	if rec := e.postTransfer("/export", e.admin, e.csrf, good); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an export without disk space: %d", rec.Code)
	}
	e.s.transfers.freeSpace = nil

	if rec := e.postTransfer("/export", e.admin, e.csrf, good); rec.Code != http.StatusSeeOther {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	page := e.get("/admin/tools/transfer", e.admin).Body.String()
	m := transferLink.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no download link: %.400s", page[max(strings.Index(page, `class="alert"`), 0):])
	}
	link := html.UnescapeString(m[1])
	if strings.Contains(e.get("/admin/tools/export", e.admin).Body.String(), "transfer.enc") {
		t.Error("Tools → Export lists the archive")
	}
	if rec := e.get(link, e.author); rec.Code != http.StatusForbidden {
		t.Errorf("an author downloads: %d", rec.Code)
	}
	other, ocsrf := e.login("admin2", "admin")
	if rec := e.get(link, other); rec.Code != http.StatusNotFound {
		t.Errorf("another admin downloads: %d", rec.Code)
	}
	if rec := e.get(link[:len(link)-2]+"xx", e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("tampered token: %d", rec.Code)
	}
	exportLink := strings.Replace(link, "/admin/tools/transfer/", "/admin/tools/export/", 1)
	if rec := e.get(exportLink, e.admin); rec.Code == 200 {
		t.Error("Tools → Export serves the archive")
	}
	rec := e.get(link, e.admin)
	if rec.Code != 200 || !strings.HasSuffix(strings.Trim(rec.Header().Get("Content-Disposition"), `"`), ".transfer.enc") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	archive := rec.Body.Bytes()
	if bytes.Contains(archive, []byte("Moved post")) || bytes.Contains(archive, []byte("manifest.json")) {
		t.Fatal("the archive is not encrypted")
	}
	// A second download works too, until the link expires.
	if rec := e.get(link, e.admin); rec.Code != 200 || rec.Body.Len() != len(archive) {
		t.Errorf("second download: %d", rec.Code)
	}
	if n := len(e.audited("transfer.exported")); n != 1 {
		t.Errorf("%d transfer.exported entries", n)
	}

	// Moving in needs a single site with its server key.
	if rec := e.postTransfer("/check", e.admin, e.csrf, url.Values{"passphrase": {transferPass}}); rec.Code != http.StatusNotFound {
		t.Errorf("a check without a server key: %d", rec.Code)
	}
	serverKey := []byte("fedcba9876543210fedcba9876543210")
	e.s.cfg.ServerKey = serverKey
	restarted := make(chan struct{})
	e.s.SetRestart(func() { close(restarted) })

	// What the import replaces.
	e.publishPost("Local post", "Written on this server before the import.")

	e.s.transfers.freeSpace = func(string) (int64, error) { return 10, nil }
	req := httptest.NewRequest("POST", "http://example.test/admin/tools/transfer/upload?offset=0&size=100&name=x", strings.NewReader("x"))
	req.Header.Set("X-CSRF-Token", e.csrf)
	if rec := e.do(req, e.admin); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("an upload without disk space: %d", rec.Code)
	}
	e.s.transfers.freeSpace = nil
	req = httptest.NewRequest("POST", "http://example.test/admin/tools/transfer/upload?offset=0&size=10&name=x", strings.NewReader("x"))
	if rec := e.do(req, e.admin); rec.Code != http.StatusForbidden {
		t.Errorf("an upload without the CSRF token: %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "http://example.test/admin/tools/transfer/upload?offset=5&size=10&id=nope", strings.NewReader("x"))
	req.Header.Set("X-CSRF-Token", e.csrf)
	if rec := e.do(req, e.admin); rec.Code != http.StatusConflict {
		t.Errorf("a chunk of no upload: %d", rec.Code)
	}

	e.uploadTransfer(archive, 1000)
	if !strings.Contains(e.get("/admin/tools/transfer", e.admin).Body.String(), `action="/admin/tools/transfer/check"`) {
		t.Fatal("the page does not ask for the passphrase after the upload")
	}
	if rec := e.postTransfer("/check", e.admin, e.csrf, url.Values{"passphrase": {"not the passphrase"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("check: %d", rec.Code)
	}
	e.waitJobs()
	if page := e.get("/admin/tools/transfer", e.admin).Body.String(); !strings.Contains(page, html.EscapeString(e.s.t("The passphrase is not the one of this archive."))) {
		t.Error("a wrong passphrase is not reported")
	}
	if rec := e.postTransfer("/check", e.admin, e.csrf, url.Values{"passphrase": {transferPass}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("check: %d", rec.Code)
	}
	e.waitJobs()
	page = e.get("/admin/tools/transfer", e.admin).Body.String()
	if !strings.Contains(page, `action="/admin/tools/transfer/import"`) {
		t.Fatalf("the page does not offer the import: %.600s", page[max(strings.Index(page, `class="alert"`), 0):])
	}
	if entries, _ := os.ReadDir(e.s.transferDir()); len(entries) != 0 {
		t.Errorf("the upload was kept after the check: %v", entries)
	}

	label := e.s.cfg.BaseURL.Hostname()
	form := func(confirm, password string) url.Values {
		return url.Values{"confirm": {confirm}, "current_password": {password}, "keep": {"1"}}
	}
	if rec := e.postTransfer("/import", e.author, e.acsrf, form(label, backupPassword)); rec.Code != http.StatusForbidden {
		t.Errorf("an author imports: %d", rec.Code)
	}
	if rec := e.postTransfer("/import", e.admin, e.csrf, form("another", backupPassword)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an import with the wrong name: %d", rec.Code)
	}
	if rec := e.postTransfer("/import", e.admin, e.csrf, form(label, "wrong")); rec.Code != http.StatusUnauthorized {
		t.Errorf("an import with a wrong password: %d", rec.Code)
	}
	if rec := e.postTransfer("/import", other, ocsrf, form(label, backupPassword)); rec.Code != http.StatusUnprocessableEntity {
		t.Error("another admin imports the archive checked by the first")
	}
	if rec := e.postTransfer("/import", e.admin, e.csrf, form(label, backupPassword)); rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	e.waitJobs()
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the server did not restart after the import")
	}

	posts, _, err := e.st.ListPosts(ctx, store.PostQuery{Type: store.TypePost, Limit: 10})
	if err != nil || len(posts) != 1 || posts[0].Title != "Moved post" {
		t.Fatalf("posts after the import: %+v %v", posts, err)
	}
	if b, err := os.ReadFile(filepath.Join(e.s.cfg.UploadsDir, "picture.txt")); err != nil || string(b) != "an upload" {
		t.Errorf("uploads after the import: %q %v", b, err)
	}
	if got, err := transfer.SiteKey(ctx, e.st.DB, serverKey); err != nil || !bytes.Equal(got, siteKey) {
		t.Errorf("site key after the import: %v", err)
	}
	var record transfer.Record
	if err := e.st.GetJSON(ctx, transfer.RecordKey, &record); err != nil || record.From != e.s.cfg.BaseURL.String() {
		t.Errorf("record: %+v %v", record, err)
	}
	if n := len(e.audited("transfer.imported")); n != 1 {
		t.Errorf("%d transfer.imported entries", n)
	}
	if list, _ := sitebackup.List(e.s.cfg.SiteBackups.Dir); len(list) == 0 {
		t.Error("no backup was made before the import")
	}
	// The import ends every session.
	if rec := e.get("/admin/tools/transfer", e.admin); rec.Code == 200 {
		t.Error("the session survived the import")
	}
	// Until the restart, nothing else is moved.
	if e.s.startTransferJob(httptest.NewRequest("GET", "/", nil), "export") {
		t.Error("a job started after the import, before the restart")
	}
}
