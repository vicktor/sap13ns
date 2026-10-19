package network

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/sitebackup"
)

// TestRestoredNotification: a restore from the command line reaches the
// running server. The site's cached pages go at once, and the site is
// loaded again once idle.
func TestRestoredNotification(t *testing.T) {
	e := newNetEnv(t, nil)
	ctx := context.Background()
	a := e.tenant("alpha", "pro")
	sa := e.siteStore(a)
	publish(t, sa, "Before the restore", "news")
	const page = "https://alpha.sap13ns.site/blog/news"
	if rec := e.do("GET", page); !strings.Contains(rec.Body.String(), "Before the restore") {
		t.Fatalf("before: %d", rec.Code)
	}
	// The restore changes the database behind the server's back.
	if _, err := sa.DB.Exec(ctx, `UPDATE posts SET title = 'After the restore'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", page); !strings.Contains(rec.Body.String(), "Before the restore") {
		t.Fatal("the page was not cached; the test proves nothing")
	}
	if err := e.n.SiteRestored(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restored content", func() bool { return strings.Contains(e.do("GET", page).Body.String(), "After the restore") })
	waitFor(t, "a reload", func() bool {
		e.reg.mu.Lock()
		defer e.reg.mu.Unlock()
		en := e.reg.entries[a.ID]
		return en == nil || en.stale
	})
}

// backupNetEnv is a network whose backups go to a temporary directory,
// and whose clock moves on by *tick at every reading.
func backupNetEnv(t *testing.T) (*netEnv, *atomic.Int64) {
	t.Helper()
	var tick, offset atomic.Int64
	root := t.TempDir()
	e := newNetEnv(t, func(o *RegistryOptions) {
		o.Config.SiteBackups = config.SiteBackups{Root: root, KeepDaily: 7, KeepWeekly: 4, Concurrency: 1, Budget: time.Hour,
			At: 3*time.Hour + 30*time.Minute}
		now := o.Now
		o.Now = func() time.Time { return now().Add(time.Duration(offset.Add(tick.Load()))) }
	})
	return e, &tick
}

func (e *netEnv) backups(id int64) []sitebackup.Archive {
	e.t.Helper()
	list, err := sitebackup.List(e.cfg.SiteBackups.SiteBackupDir(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return list
}

// TestNightlyBackups: the nightly run backs up the portal and the active
// and suspended sites, without loading them; within its time budget, the
// sites it did not reach go first the next night; a site without the
// disk space for its backup is skipped and shown to the network admins.
func TestNightlyBackups(t *testing.T) {
	e, tick := backupNetEnv(t)
	a, b, c := e.tenant("alpha", "pro"), e.tenant("bravo", "pro"), e.tenant("charlie", "pro")
	publish(t, e.siteStore(a), "Alpha's post", "alpha-post")
	if err := e.n.SuspendSite(e.ctx, c.ID, "test"); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	portal := e.portalID()
	opened := e.reg.opened.Load()

	// The first night has time for one site.
	tick.Store(int64(40 * time.Minute))
	run := e.reg.SweepBackups(e.ctx)
	tick.Store(0)
	if run.Done != 1 || run.Left != 3 || run.Failed+run.Skipped != 0 {
		t.Fatalf("first night: %+v", run)
	}
	var done int64
	for _, id := range []int64{portal, a.ID, b.ID, c.ID} {
		if n := len(e.backups(id)); n == 1 {
			done = id
		} else if n != 0 {
			t.Fatalf("site %d: %d backups", id, n)
		}
	}
	sites, err := e.reg.BackupSites(e.ctx)
	if err != nil || len(sites) != 4 || sites[3].ID != done {
		t.Fatalf("the site backed up is not the last of the next night: %d, %v", done, err)
	}

	// The second night does the rest; the suspended site too.
	run = e.reg.SweepBackups(e.ctx)
	if run.Done != 4 || run.Left != 0 {
		t.Fatalf("second night: %+v", run)
	}
	for _, id := range []int64{portal, a.ID, b.ID, c.ID} {
		list := e.backups(id)
		if len(list) == 0 {
			t.Fatalf("site %d has no backup", id)
		}
		info, err := os.Stat(list[0].Path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("site %d: backup mode %v %v", id, info.Mode(), err)
		}
		dir, err := os.Stat(filepath.Dir(list[0].Path))
		if err != nil || dir.Mode().Perm() != 0o700 {
			t.Errorf("site %d: directory mode %v %v", id, dir.Mode(), err)
		}
	}
	if e.reg.opened.Load() != opened {
		t.Error("the nightly backups loaded sites")
	}
	st, _ := sitebackup.ReadStatus(e.cfg.SiteBackups.SiteBackupDir(a.ID))
	if st.Result != sitebackup.ResultOK || st.Archive == "" {
		t.Errorf("alpha's status: %+v", st)
	}
	// Backups are not the site's storage.
	if !strings.HasPrefix(e.cfg.SiteBackups.SiteBackupDir(a.ID), e.cfg.SiteBackups.Root) ||
		strings.HasPrefix(e.cfg.SiteBackups.SiteBackupDir(a.ID), e.n.Dir(a.ID)) {
		t.Errorf("backups under the site's directory: %s", e.cfg.SiteBackups.SiteBackupDir(a.ID))
	}

	// Out of disk space: skipped, logged, shown on the network's pages.
	e.reg.backupFree = func(string) (int64, error) { return 1 << 10, nil }
	run = e.reg.SweepBackups(e.ctx)
	e.reg.backupFree = nil
	if run.Skipped != 4 || run.Done != 0 {
		t.Fatalf("low disk: %+v", run)
	}
	if st, _ := sitebackup.ReadStatus(e.cfg.SiteBackups.SiteBackupDir(b.ID)); st.Result != sitebackup.ResultSkipped || st.OK.IsZero() {
		t.Errorf("bravo's status after a skipped night: %+v", st)
	}
	_, nb, _ := e.networkAdmin("boss")
	page := nb.page("/admin/network/sites")
	for _, want := range []string{`/admin/network/sites/` + itoa(b.ID) + `">bravo</a>`, `data-variant="warning"`, e.cfg.SiteBackups.Root} {
		if !strings.Contains(page, want) {
			t.Errorf("the sites page lacks %q", want)
		}
	}
	if page := nb.page("/admin/network/sites/" + itoa(b.ID)); !strings.Contains(page, `data-variant="warning"`) {
		t.Error("the site's page does not show the skipped backup")
	}

	// Sites larger than the storage of their plan are skipped too, and
	// shown as such; the portal has no plan.
	if _, err := e.n.db.Exec(e.ctx, `UPDATE plans SET storage_bytes = 1024 WHERE slug = 'pro'`); err != nil {
		t.Fatal(err)
	}
	if q, err := e.n.BackupQuota(e.ctx, b); err != nil || q != 1024 {
		t.Fatalf("bravo's backup quota: %d %v", q, err)
	}
	before := len(e.backups(b.ID))
	run = e.reg.SweepBackups(e.ctx)
	if run.Skipped != 3 || run.Done != 1 {
		t.Fatalf("over the plan: %+v", run)
	}
	if st, _ := sitebackup.ReadStatus(e.cfg.SiteBackups.SiteBackupDir(b.ID)); st.Result != sitebackup.ResultSkipped || st.Reason != sitebackup.ReasonQuota {
		t.Errorf("bravo's status over the plan: %+v", st)
	}
	if n := len(e.backups(b.ID)); n != before {
		t.Errorf("bravo's backups: %d, then %d", before, n)
	}
	if page := nb.page("/admin/network/sites"); !strings.Contains(page, "larger than the storage of its plan") &&
		!strings.Contains(page, "ocupa más que el almacenamiento de su plan") {
		t.Error("the sites page does not say why the backups were skipped")
	}
}

// TestSiteBackupsPage: a site of a network names its backups after its
// slug, and finds them where the nightly run puts them.
func TestSiteBackupsPage(t *testing.T) {
	e, _ := backupNetEnv(t)
	a := e.tenant("alpha", "pro")
	e.reg.SweepBackups(e.ctx)
	ab, _ := e.siteAdmin(a, "alice")
	rec := ab.get("https://alpha.sap13ns.site/admin/tools/backups")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "sap13ns-site-alpha-") || !strings.Contains(body, "/admin/tools/backups/download/sap13ns-site-alpha-") {
		t.Fatalf("alpha's backups page: %d", rec.Code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
