package network

// The nightly backups of the sites of a network (internal/sitebackup).
// The sweep runs at SAP13NS_SITE_BACKUP_TIME, headless: it reads each
// site's schema and files without loading the site, so suspended sites
// (and those held for billing) are backed up like active ones. It runs a
// few sites at once (SAP13NS_SITE_BACKUP_CONCURRENCY) and starts none after
// its time budget (SAP13NS_SITE_BACKUP_BUDGET): the sites least recently
// tried go first, so the sites a night did not reach are the first of the
// next. Before each site, sitebackup checks the free disk space; a site it
// skips is logged and shown in the network administration.

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"sap13ns/internal/db"
	"sap13ns/internal/sitebackup"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// backupSite bounds the backup of one site in the nightly sweep.
const backupSite = 3 * time.Hour

// BackupSites lists the sites the nightly backups go through: the portal
// and the tenants whose sites exist (active, held for billing or
// suspended), the least recently tried first.
func (r *Registry) BackupSites(ctx context.Context) ([]Site, error) {
	served, err := r.net.ServedSites(ctx)
	if err != nil {
		return nil, err
	}
	var sites []Site
	if portal, err := r.net.NetworkSite(ctx); err == nil {
		sites = append(sites, *portal)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	for _, s := range served {
		if s.Status == StatusActive || s.Status == StatusSuspended {
			sites = append(sites, s)
		}
	}
	tried := make(map[int64]time.Time, len(sites))
	for _, s := range sites {
		st, err := sitebackup.ReadStatus(r.cfg.SiteBackups.SiteBackupDir(s.ID))
		if err != nil {
			r.log.Warn("site backup status", "site", s.ID, "err", err)
		}
		tried[s.ID] = st.Tried
	}
	slices.SortStableFunc(sites, func(a, b Site) int { return tried[a.ID].Compare(tried[b.ID]) })
	return sites, nil
}

// SweepBackups backs up the sites of the network once, within the time
// budget, and prunes their old backups.
func (r *Registry) SweepBackups(ctx context.Context) web.BackupRun {
	b := r.cfg.SiteBackups
	run := web.BackupRun{Started: r.o.Now()}
	defer func() {
		run.Finished = r.o.Now()
		r.lastBackups.Store(&run)
		r.log.Info("nightly site backups", "made", run.Done, "failed", run.Failed, "skipped", run.Skipped,
			"left", run.Left, "took", run.Finished.Sub(run.Started).Round(time.Second))
	}()
	if b.Root == "" {
		r.log.Error("nightly site backups: no backup directory")
		run.Error = "no backup directory"
		return run
	}
	sites, err := r.BackupSites(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("nightly site backups", "err", err)
		}
		return run
	}
	pass, err := b.ReadPassphrase()
	if err != nil {
		// Unencrypted backups would be a surprise: make none.
		r.log.Error("nightly site backups: the passphrase cannot be read", "err", err)
		run.Error, run.Left = "the backup passphrase cannot be read", len(sites)
		return run
	}
	deadline := run.Started.Add(b.Budget)
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, max(b.Concurrency, 1))
	)
	for i, s := range sites {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil || !r.o.Now().Before(deadline) {
			// Stopped, or out of time: the next night starts with these.
			mu.Lock()
			run.Left = len(sites) - i
			mu.Unlock()
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			result := r.backupSite(ctx, &s, pass)
			mu.Lock()
			switch result {
			case sitebackup.ResultOK:
				run.Done++
			case sitebackup.ResultSkipped:
				run.Skipped++
			default:
				run.Failed++
			}
			mu.Unlock()
		})
	}
	wg.Wait()
	return run
}

// backupSite makes the nightly backup of one site and prunes the old
// ones. It returns a sitebackup result.
func (r *Registry) backupSite(ctx context.Context, s *Site, pass string) string {
	log := r.log.With("site", s.ID, "slug", s.Slug)
	ctx, cancel := context.WithTimeout(ctx, backupSite)
	defer cancel()
	cfg, err := r.net.SiteConfig(ctx, r.cfg, s)
	if err != nil {
		log.Error("site backup", "err", err)
		return sitebackup.ResultFailed
	}
	h, err := db.Scope(r.net.pool, s.Schema)
	if err != nil {
		log.Error("site backup", "err", err)
		return sitebackup.ResultFailed
	}
	st := store.New(h)
	title := s.Slug
	if set, err := st.LoadSettings(ctx); err == nil {
		title = cmp.Or(set.SiteTitle, title)
	}
	o := sitebackup.Options{Dir: cfg.SiteBackups.Dir, UploadsDir: cfg.UploadsDir, Passphrase: pass, AppVersion: web.Version,
		FreeSpace: r.backupFree, Site: sitebackup.Site{ID: s.ID, Label: s.Slug, URL: cfg.BaseURL.String(), Title: title, Network: r.cfg.BaseURL.Hostname()}}
	a, _, err := sitebackup.Create(ctx, st, o)
	switch {
	case errors.Is(err, sitebackup.ErrLowDisk):
		log.Warn("site backup skipped: not enough free disk space", "dir", o.Dir, "err", err)
		return sitebackup.ResultSkipped
	case errors.Is(err, sitebackup.ErrBusy):
		// Someone backs it up or restores it now: tonight's backup is theirs.
		log.Info("site backup skipped: another backup or restore of the site is running")
		return sitebackup.ResultSkipped
	case err != nil:
		log.Error("site backup", "err", err)
		return sitebackup.ResultFailed
	}
	removed, err := sitebackup.Prune(o.Dir, cfg.SiteBackups.KeepDaily, cfg.SiteBackups.KeepWeekly, r.o.Now())
	if err != nil {
		log.Error("prune site backups", "err", err)
	}
	log.Info("site backed up", "archive", a.Name, "bytes", a.Size, "pruned", len(removed))
	return sitebackup.ResultOK
}

// nextAt is the first time of day at (from midnight, local time) after now.
func nextAt(now time.Time, at time.Duration) time.Time {
	y, m, d := now.Date()
	h, mins := int(at/time.Hour), int(at%time.Hour/time.Minute)
	t := time.Date(y, m, d, h, mins, 0, 0, now.Location())
	if !t.After(now) {
		t = time.Date(y, m, d+1, h, mins, 0, 0, now.Location())
	}
	return t
}

// nightly runs fn every day at the time of day at, until Close.
func (r *Registry) nightly(at time.Duration, fn func(context.Context)) {
	for {
		now := r.o.Now()
		t := time.NewTimer(nextAt(now, at).Sub(now))
		select {
		case <-r.ctx.Done():
			t.Stop()
			return
		case <-t.C:
			fn(r.ctx)
		}
	}
}

// LastBackups is the latest nightly run since the server started.
func (r *Registry) LastBackups() *web.BackupRun { return r.lastBackups.Load() }

// --------------------------------------------- the portal's view of them --

var _ web.NetworkBackups = portalHooks{}

func (h portalHooks) NetworkBackups(ctx context.Context) (*web.NetBackups, error) {
	b := h.r.cfg.SiteBackups
	out := &web.NetBackups{Nightly: b.Nightly, At: time.Time{}.Add(b.At).Format("15:04"), Dir: b.Root,
		KeepDaily: b.KeepDaily, KeepWeekly: b.KeepWeekly, Last: h.r.LastBackups()}
	if b.Root == "" {
		return out, nil
	}
	sites, err := h.r.BackupSites(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range sites {
		st, err := sitebackup.ReadStatus(b.SiteBackupDir(s.ID))
		if err != nil {
			st = sitebackup.Status{Result: sitebackup.ResultFailed, Error: err.Error()}
		}
		if st.Result == sitebackup.ResultFailed || st.Result == sitebackup.ResultSkipped {
			out.Failing = append(out.Failing, web.NetSiteBackup{ID: s.ID, Slug: s.Slug, Portal: s.Kind == KindNetwork, Status: st})
		}
	}
	return out, nil
}
