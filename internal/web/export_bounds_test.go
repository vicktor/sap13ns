package web

import (
	"bytes"
	"context"
	"errors"
	"html"
	"image/color"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/export"
	"sap13ns/internal/store"
)

// Background exports are not counted in a site's storage, so they are
// bounded: one waits per site, and one starts only when it fits the plan
// and the disk.

// quotaLimits is a site of a network whose plan has quota bytes.
type quotaLimits struct {
	SiteLimits // nil: only StorageQuota is called
	quota      int64
}

func (q quotaLimits) StorageQuota(context.Context) (int64, error) { return q.quota, nil }

// bigExports makes every .zip with media a background export, and gives
// the disk free bytes.
func bigExports(e *customEnv, free int64) {
	old := exportDirectBytes
	exportDirectBytes = 1
	e.t.Cleanup(func() { exportDirectBytes = old })
	e.s.exports.freeSpace = func(string) (int64, error) { return free, nil }
	if _, err := e.s.media.Save(context.Background(), bytes.NewReader(testPNG(e.t, color.RGBA{10, 120, 200, 255}))); err != nil {
		e.t.Fatal(err)
	}
}

func (e *customEnv) waitJobs() {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for e.s.Busy() {
		if time.Now().After(deadline) {
			e.t.Fatal("the background job never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *customEnv) waitingExports() []export.Ready {
	e.t.Helper()
	ready, err := export.List(e.s.exportDir(), time.Now())
	if err != nil {
		e.t.Fatal(err)
	}
	return ready
}

// estimate is what the export form of postExport with a .zip comes to.
func (e *customEnv) estimate() int64 {
	e.t.Helper()
	n, err := export.Estimate(context.Background(), e.st, export.Options{Types: []string{store.TypePost, store.TypePage},
		Statuses: []string{store.StatusPublished}, Media: true, Site: export.Site{UploadsDir: e.s.cfg.UploadsDir}})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *customEnv) exportsCreated() int {
	e.t.Helper()
	entries, _, err := e.st.ListAudit(context.Background(), "export.created", 50, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return len(entries)
}

var zipExport = url.Values{"format": {"zip"}}

func TestExportOneWaitingPerSite(t *testing.T) {
	e := newCustomEnv(t)
	bigExports(e, 1<<40)
	if rec := e.postExport(e.admin, e.csrf, zipExport); rec.Code != http.StatusSeeOther {
		t.Fatalf("first export: %d %s", rec.Code, rec.Body)
	}
	e.waitJobs()
	first := e.waitingExports()
	if len(first) != 1 || first[0].UserID != e.userID("admin") {
		t.Fatalf("after the first export: %+v", first)
	}
	firstLink := e.s.exportURL(first[0].Name, first[0].Created)

	// Another administrator's export replaces it.
	other, csrf := e.login("admin2", auth.RoleAdmin)
	if rec := e.postExport(other, csrf, zipExport); rec.Code != http.StatusSeeOther {
		t.Fatalf("second export: %d %s", rec.Code, rec.Body)
	}
	e.waitJobs()
	if ready := e.waitingExports(); len(ready) != 1 || ready[0].UserID != e.userID("admin2") {
		t.Fatalf("after the second export: %+v", ready)
	}
	if _, err := os.Stat(filepath.Join(e.s.exportDir(), first[0].Name)); !os.IsNotExist(err) {
		t.Errorf("the first export is still there: %v", err)
	}
	if rec := e.get(firstLink, e.admin); rec.Code != http.StatusGone {
		t.Errorf("the replaced export's link: %d", rec.Code)
	}
	if downloadLink.MatchString(e.get("/admin/tools/export", e.admin).Body.String()) {
		t.Error("the first administrator still has a download link")
	}
	if !downloadLink.MatchString(e.get("/admin/tools/export", other).Body.String()) {
		t.Error("the second administrator has no download link")
	}
	entries, _, _ := e.st.ListAudit(context.Background(), "export.created", 10, 0)
	replaced := 0
	for _, a := range entries {
		if a.Details["replaced"] == "1" {
			replaced++
		}
	}
	if len(entries) != 2 || replaced != 1 {
		t.Errorf("audit: %+v", entries)
	}
}

func TestExportOverTheQuota(t *testing.T) {
	e := newCustomEnv(t)
	bigExports(e, 1<<40)
	est := e.estimate()
	e.s.limits = quotaLimits{quota: est - 1}
	rec := e.postExport(e.admin, e.csrf, zipExport)
	want := e.s.t("This export would take about %s, more than the storage of the site's plan (%s). Leave out the media library.",
		filesize(est), filesize(est-1))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Fatalf("over the quota: %d, want 413 with %q", rec.Code, want)
	}
	e.waitJobs()
	if ready := e.waitingExports(); len(ready) != 0 || e.exportsCreated() != 0 {
		t.Fatalf("an export over the quota started: %+v", ready)
	}
	// A WXR file, or a .zip without the media library, still downloads.
	if rec := e.postExport(e.admin, e.csrf, url.Values{"format": {"zip"}, "media": {"0"}}); rec.Code != http.StatusOK ||
		rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("a .zip without media: %d", rec.Code)
	}
	// Within the quota, it starts.
	e.s.limits = quotaLimits{quota: est}
	if rec := e.postExport(e.admin, e.csrf, zipExport); rec.Code != http.StatusSeeOther {
		t.Fatalf("within the quota: %d %s", rec.Code, rec.Body)
	}
	e.waitJobs()
	if ready := e.waitingExports(); len(ready) != 1 {
		t.Fatalf("within the quota: %+v", ready)
	}
}

// TestExportDiskSpace runs on a single site: no quota applies there, and
// the free disk space alone decides.
func TestExportDiskSpace(t *testing.T) {
	e := newCustomEnv(t)
	bigExports(e, 0)
	est := e.estimate()
	var measured string
	free := 2*est - 1
	e.s.exports.freeSpace = func(dir string) (int64, error) { measured = dir; return free, nil }
	rec := e.postExport(e.admin, e.csrf, zipExport)
	want := e.s.t("The server does not have enough free disk space for this export, which would take about %s. Try again later, or leave out the media library.",
		filesize(est))
	if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), html.EscapeString(want)) {
		t.Fatalf("low disk space: %d, want 413 with %q", rec.Code, want)
	}
	if measured != e.s.exportDir() {
		t.Errorf("measured %q, want the export directory %q", measured, e.s.exportDir())
	}
	e.waitJobs()
	if len(e.waitingExports()) != 0 || e.exportsCreated() != 0 {
		t.Fatal("an export started without the disk space")
	}

	// The export it would replace frees its space: one byte is enough here.
	other := e.userID("admin") + 1000
	waiting := filepath.Join(e.s.exportDir(), export.NewName(other, time.Now()))
	if err := os.WriteFile(waiting, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := e.postExport(e.admin, e.csrf, zipExport); rec.Code != http.StatusSeeOther {
		t.Fatalf("with the replaced export's space: %d %s", rec.Code, rec.Body)
	}
	e.waitJobs()
	if ready := e.waitingExports(); len(ready) != 1 || ready[0].UserID != e.userID("admin") {
		t.Fatalf("after the export: %+v", ready)
	}

	// A disk that cannot be measured refuses; a system that cannot tell
	// does not.
	e.s.exports.freeSpace = func(string) (int64, error) { return 0, errors.New("statfs failed") }
	if rec := e.postExport(e.admin, e.csrf, zipExport); rec.Code != http.StatusInternalServerError {
		t.Errorf("a failed measurement: %d", rec.Code)
	}
	e.s.exports.freeSpace = func(string) (int64, error) { return -1, nil }
	if rec := e.postExport(e.admin, e.csrf, zipExport); rec.Code != http.StatusSeeOther {
		t.Errorf("an unknown free space: %d", rec.Code)
	}
	e.waitJobs()
}
