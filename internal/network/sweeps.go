package network

// The background work of a network's sites. A loaded site runs its own
// minute loop (scheduled posts, redirect hits, analytics) like a single
// site; the registry runs what must also happen while a site is unloaded:
//
//   - every minute, the sites whose next scheduled post is due
//     (sites.next_announce_at, kept up to date by each site's Wakeup hook
//     and recomputed on unload) are loaded and announce it. A post is
//     announced once whoever claims it, since ClaimDueAnnouncements is
//     atomic;
//   - every hour, cron.hourly reaches every loaded site, and the unloaded
//     ones whose plugins handle it (sites.wants_cron), two loads at once,
//     and the expired exports of every site are removed;
//   - every day, each schema is cleaned up as a single site does every 30
//     minutes (sessions, audit log, analytics), without loading a server,
//     its storage is measured, expired SSO codes are purged, and the
//     verified custom domains are checked in DNS again (a domain that
//     keeps failing lapses, and its owner is e-mailed).
//
// Loads for this work do not count as uses: a site loaded only for it is
// unloaded by the next sweep.

import (
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"sap13ns/internal/db"
	"sap13ns/internal/export"
	"sap13ns/internal/store"
	"sap13ns/internal/web"
)

// visit runs fn on an active site, loading it if needed (or, if load is
// false, only if it is loaded).
func (r *Registry) visit(ctx context.Context, id int64, load bool, fn func(*entry)) error {
	t := r.table.Load()
	if t == nil || t.sites[id] == nil || t.sites[id].Status != StatusActive {
		return errNotServed
	}
	if !load {
		r.mu.Lock()
		e := r.entries[id]
		loaded := e != nil && e.app != nil && !e.closing
		r.mu.Unlock()
		if !loaded {
			return errNotServed
		}
	}
	e, err := r.acquireFor(ctx, t.sites[id], loadTimeout, false)
	if err != nil {
		return err
	}
	defer r.releaseFor(e, false)
	fn(e)
	return nil
}

// recordNextAnnounce stores when the next scheduled post of a site is due.
func (r *Registry) recordNextAnnounce(ctx context.Context, e *entry) error {
	next, err := e.app.Store().NextAnnouncement(ctx)
	if err != nil {
		return err
	}
	return r.net.SetNextAnnounce(ctx, e.id, next)
}

// Announce wakes the sites with a scheduled post due and returns their
// ids. A site that does not load is tried again after announceRetry.
func (r *Registry) Announce(ctx context.Context) []int64 {
	ids, err := r.net.DueAnnouncements(ctx, 100)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("scheduled posts of the network", "err", err)
		}
		return nil
	}
	var woken []int64
	for _, id := range ids {
		err := r.visit(ctx, id, true, func(e *entry) {
			e.app.Server().AnnounceDue(ctx)
			if err := r.recordNextAnnounce(ctx, e); err != nil {
				r.log.Error("record the next scheduled post of a site", "site", id, "err", err)
			}
		})
		switch {
		case err == nil:
			woken = append(woken, id)
		case ctx.Err() != nil:
			return woken
		default:
			if !errors.Is(err, errNotServed) {
				r.log.Error("wake a site for its scheduled posts", "site", id, "err", err)
			}
			if err := r.net.PostponeAnnounce(ctx, id, announceRetry); err != nil {
				r.log.Error("postpone a wake-up", "site", id, "err", err)
			}
		}
	}
	return woken
}

// Cron emits cron.hourly on the loaded sites and on the unloaded ones
// that want it, and returns the ids it reached.
func (r *Registry) Cron(ctx context.Context) []int64 {
	want, err := r.net.CronSites(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cron of the network", "err", err)
		}
		return nil
	}
	ids := append(r.Loaded(), want...)
	slices.Sort(ids)
	ids = slices.Compact(ids)
	now := r.o.Now()
	// A loaded site that does not want the cron is not loaded again for it
	// if it was unloaded meanwhile.
	load := func(id int64) bool { return slices.Contains(want, id) }

	var (
		mu      sync.Mutex
		reached []int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, cronLoads)
	)
	for _, id := range ids {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			err := r.visit(ctx, id, load(id), func(e *entry) { e.app.Plugins().Cron(now) })
			if err != nil {
				if !errors.Is(err, errNotServed) && ctx.Err() == nil {
					r.log.Error("cron of a site", "site", id, "err", err)
				}
				return
			}
			mu.Lock()
			reached = append(reached, id)
			mu.Unlock()
		})
	}
	wg.Wait()
	slices.Sort(reached)
	return reached
}

// SweepExports removes the expired exports of every site, loaded or not,
// from the sites' directories (Tools → Export, internal/export). It runs
// every hour, so a download link outlives its 24 hours by an hour at most
// on disk; the link itself stops working on time.
func (r *Registry) SweepExports(ctx context.Context) {
	dirs, err := filepath.Glob(filepath.Join(r.net.dataDir, "sites", "*", export.DirName))
	if err != nil {
		r.log.Error("sweep exports", "err", err)
		return
	}
	now := r.o.Now()
	for _, dir := range dirs {
		if ctx.Err() != nil {
			return
		}
		if n, err := export.Sweep(dir, now); err != nil {
			r.log.Error("sweep exports", "dir", dir, "err", err)
		} else if n > 0 {
			r.log.Info("expired exports removed", "dir", dir, "count", n)
		}
	}
}

// Maintain cleans up every site's schema and measures its storage, loaded
// or not, without loading it.
func (r *Registry) Maintain(ctx context.Context) {
	sites, err := r.net.ServedSites(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("network maintenance", "err", err)
		}
		return
	}
	if portal, err := r.net.NetworkSite(ctx); err == nil {
		sites = append([]Site{*portal}, sites...)
	} else if ctx.Err() == nil {
		r.log.Error("network maintenance", "err", err)
	}
	for _, s := range sites {
		if ctx.Err() != nil {
			return
		}
		log := r.log.With("site", s.ID)
		h, err := db.Scope(r.net.pool, s.Schema)
		if err != nil {
			log.Error("maintenance", "err", err)
			continue
		}
		st := store.New(h)
		if set, err := st.LoadSettings(ctx); err != nil {
			log.Error("maintenance", "err", err)
		} else {
			web.Maintain(ctx, st, set, log)
		}
		if err := r.measure(ctx, &s); err != nil && ctx.Err() == nil {
			log.Error("measure storage", "err", err)
		}
		if err := r.net.ReconcileAccess(ctx, &s); err != nil && ctx.Err() == nil {
			log.Error("reconcile the access of network accounts", "err", err)
		}
	}
	r.recheckDomains(ctx)
	if n, err := r.net.PurgeBillingEvents(ctx); err != nil {
		if ctx.Err() == nil {
			r.log.Error("purge billing events", "err", err)
		}
	} else if n > 0 {
		r.log.Info("old billing events purged", "count", n)
	}
	if n, err := r.net.PurgeSSOCodes(ctx); err != nil {
		if ctx.Err() == nil {
			r.log.Error("purge SSO codes", "err", err)
		}
	} else if n > 0 {
		r.log.Info("expired SSO codes purged", "count", n)
	}
}

// recheckDomains checks the verified custom domains again, and tells the
// owners of those that lapsed, through the portal.
func (r *Registry) recheckDomains(ctx context.Context) {
	lapses, err := r.net.RecheckDomains(ctx)
	if err != nil && ctx.Err() == nil {
		r.log.Error("re-check the custom domains", "err", err)
	}
	if len(lapses) > 0 {
		r.Refresh(ctx) // stop routing them now, not at the next notification
	}
	portal := store.New(r.net.DB())
	for _, l := range lapses {
		details := map[string]string{"site": strconv.FormatInt(l.SiteID, 10), "host": l.Host, "problem": l.Problem}
		if l.Fallback != "" {
			details["primary_now"] = l.Fallback
		}
		if err := portal.Audit(ctx, 0, "network.domain_lapsed", l.Host, netip.Addr{}, details); err != nil {
			r.log.Error("audit a lapsed domain", "host", l.Host, "err", err)
		}
		r.domainNotice(ctx, l)
	}
}

// domainNotice has the portal e-mail the owner of a site about a lapsed
// custom domain, in the portal's language.
func (r *Registry) domainNotice(ctx context.Context, l Lapse) {
	s, err := r.net.Site(ctx, l.SiteID)
	if err != nil || s.OwnerID == 0 {
		r.log.Error("lapsed domain notice", "site", l.SiteID, "err", err)
		return
	}
	t := r.table.Load()
	if t == nil || t.network == nil {
		return
	}
	e, err := r.acquireFor(ctx, t.network, loadTimeout, false)
	if err != nil {
		r.log.Error("lapsed domain notice", "site", l.SiteID, "err", err)
		return
	}
	defer r.releaseFor(e, false)
	if err := e.app.Server().NotifyDomain(ctx, web.DomainNotice{AccountID: s.OwnerID, SiteID: s.ID, Slug: s.Slug, Host: l.Host,
		Problem: l.Problem, Fallback: l.Fallback}); err != nil {
		r.log.Error("lapsed domain notice", "site", l.SiteID, "host", l.Host, "err", err)
	}
}

// measure records the space a site uses: its tables and its directory.
// It replaces the running count that uploads reserve and deletions
// release, keeping what changed while it measured.
func (r *Registry) measure(ctx context.Context, s *Site) error {
	before, err := r.net.StorageBytes(ctx, s.ID)
	if err != nil {
		return err
	}
	b, err := r.net.SchemaBytes(ctx, s.Schema)
	if err != nil {
		return err
	}
	files, err := dirBytes(r.net.Dir(s.ID))
	if err != nil {
		return err
	}
	return r.net.SetStorage(ctx, s.ID, b+files, before)
}

// dirBytes adds up the sizes of the regular files under dir. Symbolic
// links are not followed; a directory that does not exist is empty. The
// site's exports are left out: they are temporary, and an export of a
// site at its quota must not push it over. They are bounded instead: one
// waits per site, and none starts larger than the plan's storage or
// without twice its size free on the disk (web.Server.exportFits).
func dirBytes(dir string) (int64, error) {
	var n int64
	exports := export.Dir(dir)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // removed meanwhile
			}
			return err
		}
		if d.IsDir() && p == exports {
			return fs.SkipDir
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}
