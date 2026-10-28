package network

// The registry keeps the loaded sites of a network: one siteapp.App per
// site, opened on its first request and closed when idle.
//
//   - A site loads once however many requests arrive together: the first
//     one creates the entry and starts the load, the others wait for it.
//     At most Loads sites load at once. A request that waits longer than
//     LoadWait gets a 503 with Retry-After; the load goes on.
//   - An entry that is closing stays in the map until its App is closed,
//     and a request for it waits, so a site never runs twice.
//   - Sweep unloads sites idle for SAP13NS_SITE_IDLE, the least recently
//     used beyond SAP13NS_MAX_LOADED_SITES, and more when the heap passes
//     85% of SAP13NS_MEMORY_LIMIT_MB, until an estimated 70%. It never
//     unloads the portal, a site with requests in flight or a busy one (an
//     import, an AI job).
//   - The host table is an atomic snapshot, reloaded on every notification
//     on sap13ns_network (LISTEN on a connection of its own) and every
//     minute, so changes made by the CLI or another process reach a
//     running server. A site that is no longer served (suspended, deleted)
//     is unloaded at once, after its requests in flight; a site whose hosts
//     changed is reloaded when idle.
//   - A suspended site is served only during an emergency access, and then
//     inert (siteapp.Params.Inert): none of its plugins loads and none of
//     its background loops runs. An instance of the wrong kind never stays
//     loaded: a site suspended while loaded is unloaded, one resumed while
//     inert is unloaded too, and the next request loads the right one. So
//     is an inert one when the emergency ends, or within a minute (the
//     next reload of the host table) of its expiry.
//   - The work that must reach unloaded sites (scheduled posts, the plugin
//     cron, daily maintenance and storage accounting) is in sweeps.go.

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/tetratelabs/wazero"

	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/mail"
	"sap13ns/internal/plugin"
	"sap13ns/internal/siteapp"
	"sap13ns/internal/store"
	"sap13ns/internal/update"
	"sap13ns/internal/web"
)

// Limits of a site in a network.
const (
	defaultLoadWait    = 10 * time.Second
	defaultMaxInflight = 32
	defaultLoads       = 4
	loadTimeout        = 2 * time.Minute
	closeTimeout       = 30 * time.Second
	sweepEvery         = 30 * time.Second
	reloadEvery        = time.Minute
	announceEvery      = time.Minute
	cronEvery          = time.Hour
	maintainFirst      = 10 * time.Minute // after a start, not during it
	maintainEvery      = 24 * time.Hour
	announceRetry      = 10 * time.Minute // after a failed wake-up load
	cronLoads          = 2                // unloaded sites the cron loads at once

	pluginPages     = 512 // 32 MiB per plugin instance
	pluginInstances = 2   // per plugin and site
)

// The caches of a site, smaller than a single-site install's.
var siteCaches = web.CacheSizes{Pages: 500, PagesBytes: 8 << 20, Content: 300, ContentBytes: 4 << 20}

var (
	errClosed      = errors.New("network: the registry is closed")
	errBusy        = errors.New("network: too many requests in flight for this site")
	errUnavailable = errors.New("network: the site did not load in time")
	errNotServed   = errors.New("network: the site is not served")
)

type RegistryOptions struct {
	Config  *config.Config // the network's (multisite)
	Network *Network       // on the shared pool, which every site uses
	Log     *slog.Logger
	UI      fs.FS
	Mailer  *mail.Mailer    // shared by every site; nil: one from Config.SMTP
	Updates *update.Checker // release checks shown on the portal; nil = none
	Billing *Billing        // NewBilling; nil when billing is off
	// Official are the official plugins in the binary: every site can
	// install them, except those the operator hides; nil = none.
	Official *plugin.Official

	// Zero values give the defaults; tests shorten them.
	Now         func() time.Time
	LoadWait    time.Duration // how long a request waits for its site to load
	MaxInflight int           // requests of one site at once
	Loads       int           // sites loading at once
	Heap        func() uint64 // live heap bytes
}

type Registry struct {
	o      RegistryOptions
	cfg    *config.Config
	net    *Network
	log    *slog.Logger
	pages  *web.StatusPages
	mailer *mail.Mailer
	cache  wazero.CompilationCache
	limit  *plugin.InstanceLimit
	loads  chan struct{}

	table     atomic.Pointer[hostTable]
	refreshes atomic.Int64 // host table reloads, for tests
	opened    atomic.Int64 // sites opened, for tests
	unloaded  atomic.Int64 // and closed

	access      accessCache                     // operator sessions' checks (portal_support.go)
	langs       siteLangs                       // of the sites whose status pages are served (router.go)
	lastBackups atomic.Pointer[web.BackupRun]   // the latest nightly backups
	backupFree  func(dir string) (int64, error) // tests: the free disk space the backups see

	mu      sync.Mutex
	entries map[int64]*entry
	closed  bool

	ctx    context.Context // ends with Close
	cancel context.CancelFunc
	wg     sync.WaitGroup // loads, unloads and loops
}

type entry struct {
	id       int64
	kind     string
	key      string       // siteRoute.key() of the configuration it was loaded with
	app      *siteapp.App // set once ready, nil if the load failed
	err      error
	ready    chan struct{} // closed when the load ended
	closed   chan struct{} // closed when the entry is gone
	lastUsed time.Time
	inflight int
	closing  bool
	stale    bool // its hosts changed: reload when idle
	inert    bool // a suspended site opened for an emergency access (siteRoute.inert)
}

// NewRegistry prepares the registry of a network. Start loads the host
// table and the portal and starts the loops.
func NewRegistry(o RegistryOptions) (*Registry, error) {
	if o.Config == nil || !o.Config.Multisite || o.Network == nil || o.Log == nil || o.UI == nil {
		return nil, errors.New("network: the registry needs a multisite config, the network, a logger and the UI")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	o.LoadWait = cmp.Or(o.LoadWait, defaultLoadWait)
	o.MaxInflight = cmp.Or(o.MaxInflight, defaultMaxInflight)
	o.Loads = cmp.Or(o.Loads, defaultLoads)
	if o.Heap == nil {
		o.Heap = liveHeap
	}
	if o.Billing != nil {
		o.Billing.now = o.Now
	}
	o.Network.now = o.Now // the domain checks
	pages, err := web.NewStatusPages(o.UI)
	if err != nil {
		return nil, err
	}
	mailer := o.Mailer
	if mailer == nil {
		mailer = mail.New(o.Config.SMTP, o.Log, o.Config.Dev)
	}
	// One compilation cache for every site: a plugin used by many sites is
	// compiled once, and restarts reuse the machine code on disk.
	cache, err := wazero.NewCompilationCacheWithDir(filepath.Join(o.Config.DataDir, "cache", "wasm"))
	if err != nil {
		return nil, fmt.Errorf("network: plugin compilation cache: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Registry{
		o: o, cfg: o.Config, net: o.Network, log: o.Log, pages: pages, mailer: mailer, cache: cache,
		limit: plugin.NewInstanceLimit(instanceLimit(o.Config.MemoryLimitMB)),
		loads: make(chan struct{}, o.Loads), entries: map[int64]*entry{}, ctx: ctx, cancel: cancel,
	}, nil
}

// instanceLimit is the number of plugin instances the process keeps alive
// at once: 256 (8 GiB of plugin memory at most), or a quarter of the memory
// limit in 32 MiB instances, at least 8.
func instanceLimit(memoryMB int) int {
	if memoryMB <= 0 {
		return 256
	}
	return min(max(memoryMB/4/32, 8), 256)
}

func liveHeap() uint64 {
	s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	metrics.Read(s)
	if s[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return s[0].Value.Uint64()
}

// Start loads the host table and the portal, then starts listening for
// changes and sweeping idle sites until Close.
func (r *Registry) Start(ctx context.Context) error {
	if err := r.Refresh(ctx); err != nil {
		return err
	}
	portal := r.table.Load().network
	if portal == nil {
		return errors.New("network: there is no network site yet; run `sap13ns network init`")
	}
	e, err := r.acquire(ctx, portal, loadTimeout)
	if err != nil {
		return fmt.Errorf("network: load the portal: %w", err)
	}
	r.release(e)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errClosed
	}
	r.wg.Add(7)
	if b := r.o.Billing; b != nil {
		b.notify = r.billingNotice
		r.wg.Go(func() { b.Run(r.ctx) })
	}
	go r.listen()
	go r.every(reloadEvery, reloadEvery, func(ctx context.Context) {
		if err := r.Refresh(ctx); err != nil && ctx.Err() == nil {
			r.log.Error("network host table", "err", err)
		}
	})
	go r.every(sweepEvery, sweepEvery, func(ctx context.Context) { r.Sweep(ctx) })
	go r.every(announceEvery, announceEvery, func(ctx context.Context) { r.Announce(ctx) })
	go r.every(cronEvery, cronEvery, func(ctx context.Context) { r.Cron(ctx) })
	go r.every(maintainFirst, cronEvery, r.SweepExports)
	go r.every(maintainFirst, maintainEvery, r.Maintain)
	if b := r.cfg.SiteBackups; b.Nightly {
		r.wg.Go(func() { r.nightly(b.At, func(ctx context.Context) { r.SweepBackups(ctx) }) })
	}
	return nil
}

// every runs fn after first, then every d until Close.
func (r *Registry) every(first, d time.Duration, fn func(context.Context)) {
	defer r.wg.Done()
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-t.C:
			r.safely(fn)
			t.Reset(d)
		}
	}
}

// safely runs fn, logging a panic instead of letting it end the process
// and with it every site.
func (r *Registry) safely(fn func(context.Context)) {
	defer func() {
		if v := recover(); v != nil {
			r.log.Error("network loop panicked", "panic", v, "stack", string(debug.Stack()))
		}
	}()
	fn(r.ctx)
}

// Close stops the loops and closes every loaded site.
func (r *Registry) Close(ctx context.Context) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()
	r.cancel() // stops the loops; loads in progress fail
	r.wg.Wait()

	r.mu.Lock()
	var open []*entry
	for _, e := range r.entries {
		if e.app != nil && !e.closing {
			e.closing = true
			open = append(open, e)
		}
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, e := range open {
		wg.Go(func() { r.unload(ctx, e) })
	}
	wg.Wait()
	return r.cache.Close(context.WithoutCancel(ctx))
}

// Loaded lists the ids of the loaded sites.
func (r *Registry) Loaded() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []int64
	for id, e := range r.entries {
		if e.app != nil && !e.closing {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

// acquire returns the loaded entry of a site, loading it if needed, and
// counts a request in flight on it until release. It waits at most wait
// for a load.
func (r *Registry) acquire(ctx context.Context, rt *siteRoute, wait time.Duration) (*entry, error) {
	return r.acquireFor(ctx, rt, wait, true)
}

func (r *Registry) release(e *entry) { r.releaseFor(e, true) }

// acquireFor is acquire; touch is false for the registry's own work (a
// wake-up, the cron), which does not count as a use of the site: a site
// loaded only for it is unloaded by the next sweep.
func (r *Registry) acquireFor(ctx context.Context, rt *siteRoute, wait time.Duration, touch bool) (*entry, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			return nil, errClosed
		}
		e := r.entries[rt.ID]
		if e != nil && e.closing {
			gone := e.closed
			r.mu.Unlock()
			select {
			case <-gone:
				continue
			case <-timer.C:
				return nil, errUnavailable
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if e != nil && e.app != nil && e.inert != rt.inert() {
			// Loaded as the other kind: the site was suspended or resumed
			// and reconcile has not caught up yet. Close it and load the
			// right one.
			e.closing = true
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.unload(r.ctx, e)
			}()
			r.mu.Unlock()
			continue
		}
		if e == nil {
			e = &entry{id: rt.ID, kind: rt.Kind, key: rt.key(), inert: rt.inert(), ready: make(chan struct{}), closed: make(chan struct{})}
			r.entries[rt.ID] = e
			r.wg.Add(1)
			go r.load(e, rt)
		}
		if e.inflight >= r.o.MaxInflight {
			r.mu.Unlock()
			return nil, errBusy
		}
		e.inflight++
		if touch {
			e.lastUsed = r.o.Now()
		}
		r.mu.Unlock()

		select {
		case <-e.ready:
		case <-timer.C:
			r.releaseFor(e, touch)
			return nil, errUnavailable
		case <-ctx.Done():
			r.releaseFor(e, touch)
			return nil, ctx.Err()
		}
		if e.err != nil {
			r.releaseFor(e, touch)
			return nil, e.err
		}
		if e.inert != rt.inert() {
			r.releaseFor(e, touch)
			continue // loaded as the other kind: closed above
		}
		return e, nil
	}
}

func (r *Registry) releaseFor(e *entry, touch bool) {
	r.mu.Lock()
	e.inflight--
	if touch {
		e.lastUsed = r.o.Now()
	}
	r.mu.Unlock()
}

// load opens the site of e. A failed load removes the entry, so the next
// request tries again.
func (r *Registry) load(e *entry, rt *siteRoute) {
	defer r.wg.Done()
	ctx, cancel := context.WithTimeout(r.ctx, loadTimeout)
	defer cancel()
	app, err := r.open(ctx, rt)
	r.mu.Lock()
	e.app, e.err = app, err
	if err != nil {
		delete(r.entries, e.id)
		close(e.closed)
	}
	close(e.ready)
	r.mu.Unlock()
	if err != nil {
		r.log.Error("load site", "site", rt.ID, "slug", rt.Slug, "err", err)
		return
	}
	r.reconcile(r.table.Load()) // the table may have changed during the load
	r.Sweep(r.ctx)              // keep to the loaded-sites limit
}

// open builds a site's configuration and store and opens its App.
func (r *Registry) open(ctx context.Context, rt *siteRoute) (*siteapp.App, error) {
	select {
	case r.loads <- struct{}{}:
		defer func() { <-r.loads }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s, err := r.net.Site(ctx, rt.ID)
	if err != nil {
		return nil, err
	}
	inert := rt.inert()
	switch {
	case s.Status == StatusActive || s.Status == StatusPending:
		// Served. Just resumed while the table still says suspended, it
		// opens inert all the same and reconcile replaces it.
	case s.Status == StatusSuspended && inert && rt.Emergency:
		// An emergency access: inert, so none of the tenant's code runs.
	default:
		return nil, errNotServed // suspended since the table was read
	}
	secret, err := r.net.SiteSecret(s)
	if err != nil {
		return nil, err
	}
	var cfg *config.Config
	if s.Kind == KindNetwork {
		cfg, err = config.ForNetwork(r.cfg, s.ID, secret)
	} else {
		cfg, err = config.ForSite(r.cfg, config.SiteParams{ID: s.ID, Host: rt.Primary, Custom: rt.Custom, Secret: secret, RPID: s.RPID})
	}
	if err != nil {
		return nil, err
	}
	h, err := db.Scope(r.net.pool, s.Schema)
	if err != nil {
		return nil, err
	}
	p := siteapp.Params{
		Config: cfg, Store: store.New(h), Log: r.log.With("site", s.ID), UI: r.o.UI, Mailer: r.mailer,
		Official:   r.o.Official,
		Blocklist:  filepath.Join(r.cfg.DataDir, "plugin-blocklist.txt"),
		Network:    siteHooks{hooks{r: r, id: s.ID, slug: s.Slug}},
		Quota:      siteHooks{hooks{r: r, id: s.ID, slug: s.Slug}},
		Hosts:      rt.aliases(),
		CacheSizes: siteCaches,
		Plugins:    siteapp.PluginLimits{Cache: r.cache, MemoryLimitPages: pluginPages, MaxInstances: pluginInstances, Limit: r.limit},
		// The registry runs the cron and maintenance of every site.
		Background: web.BackgroundOptions{},
		Inert:      inert,
	}
	if s.Kind == KindNetwork {
		p.Updates = r.o.Updates
		p.Network = portalHooks{hooks{r: r, id: s.ID, slug: s.Slug}}
		p.Quota = nil // the operator's
	}
	app, err := siteapp.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	// An inert site loads no plugin, so it cannot tell whether its
	// plugins want the cron: what was recorded stays for its resumption.
	if wants := app.Plugins().WantsCron(); !inert && wants != s.WantsCron {
		if err := r.net.SetWantsCron(ctx, s.ID, wants); err != nil {
			r.log.Error("record the cron of a site", "site", s.ID, "err", err)
		}
	}
	r.opened.Add(1)
	r.log.Info("site loaded", "site", s.ID, "slug", s.Slug, "inert", inert)
	return app, nil
}

// unload closes the App of e, which the caller marked closing, and
// removes the entry. Then, with nothing of the site running, it records
// what wakes the site up while unloaded: whether its plugins want the
// cron (an admin may have changed them) and when its next scheduled post
// is due (a wake-up recorded while it ran may have been overwritten).
func (r *Registry) unload(ctx context.Context, e *entry) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
	defer cancel()
	wants := e.app.Plugins().WantsCron()
	if err := e.app.Close(cctx); err != nil {
		r.log.Error("close site", "site", e.id, "err", err)
	}
	if !e.app.Inert() { // see open
		if err := r.net.SetWantsCron(cctx, e.id, wants); err != nil {
			r.log.Error("record the cron of a site", "site", e.id, "err", err)
		}
	}
	if err := r.recordNextAnnounce(cctx, e); err != nil {
		r.log.Error("record the next scheduled post of a site", "site", e.id, "err", err)
	}
	r.unloaded.Add(1) // before the site can load again
	r.mu.Lock()
	if r.entries[e.id] == e {
		delete(r.entries, e.id)
	}
	close(e.closed)
	r.mu.Unlock()
	r.log.Info("site unloaded", "site", e.id)
}

// Sweep unloads the sites that are idle, stale or beyond the limits, and
// returns their ids once they are closed.
func (r *Registry) Sweep(ctx context.Context) []int64 {
	now := r.o.Now()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	loaded := 0
	var idle, keep []*entry // unloadable now
	for _, e := range r.entries {
		if e.app == nil || e.closing {
			continue
		}
		loaded++
		if e.kind == KindNetwork || e.inflight > 0 || e.app.Busy() {
			continue
		}
		if e.stale || now.Sub(e.lastUsed) >= r.cfg.SiteIdle {
			idle = append(idle, e)
		} else {
			keep = append(keep, e)
		}
	}
	slices.SortFunc(keep, func(a, b *entry) int { return a.lastUsed.Compare(b.lastUsed) })
	over := loaded - len(idle) - r.cfg.MaxLoadedSites
	if limit := uint64(r.cfg.MemoryLimitMB) << 20; limit > 0 && loaded > 0 {
		if heap := r.o.Heap(); heap > limit/100*85 {
			// Freed memory shows only after a GC, so estimate how many
			// sites to unload to get back to 70%.
			per := max(heap/uint64(loaded), 1)
			need := int((heap - limit/100*70 + per - 1) / per)
			over = max(over, need-len(idle))
		}
	}
	out := idle
	for i := 0; i < over && i < len(keep); i++ {
		out = append(out, keep[i])
	}
	for _, e := range out {
		e.closing = true
	}
	r.wg.Add(len(out))
	r.mu.Unlock()

	var wg sync.WaitGroup
	ids := make([]int64, 0, len(out))
	for _, e := range out {
		ids = append(ids, e.id)
		wg.Go(func() {
			defer r.wg.Done()
			r.unload(ctx, e)
		})
	}
	wg.Wait()
	slices.Sort(ids)
	return ids
}

// Refresh reloads the host table, then unloads the sites it no longer
// serves and marks for reload those whose hosts changed.
func (r *Registry) Refresh(ctx context.Context) error {
	t, err := r.net.hostTable(ctx, r.o.Now())
	if err != nil {
		return err
	}
	r.table.Store(t)
	r.refreshes.Add(1)
	r.reconcile(t)
	return nil
}

// reconcile applies a host table to the loaded sites. A site no longer
// routed, suspended without a live emergency access, or loaded as the
// other kind (normal although suspended, inert although resumed) is
// unloaded at once, after its requests in flight.
func (r *Registry) reconcile(t *hostTable) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	var gone []*entry
	for id, e := range r.entries {
		if e.app == nil || e.closing {
			continue
		}
		rt := t.sites[id]
		switch {
		case rt == nil || rt.Status == StatusSuspended && !rt.Emergency || rt.inert() != e.inert:
			e.closing = true
			gone = append(gone, e)
		case rt.key() != e.key:
			e.stale = true
		}
	}
	r.wg.Add(len(gone))
	r.mu.Unlock()
	for _, e := range gone {
		go func() {
			defer r.wg.Done()
			r.unload(r.ctx, e)
		}()
	}
}

// listen reloads the host table on every notification of a network
// change, on a connection of its own, reconnecting after failures.
func (r *Registry) listen() {
	defer r.wg.Done()
	wait := time.Second
	for r.ctx.Err() == nil {
		err := r.listenOnce()
		if r.ctx.Err() != nil {
			return
		}
		r.log.Warn("network notifications lost; reconnecting", "err", err, "in", wait)
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (r *Registry) listenOnce() error {
	conn, err := pgx.Connect(r.ctx, r.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(r.ctx, `LISTEN `+Channel); err != nil {
		return err
	}
	// What changed while there was no listener.
	if err := r.Refresh(r.ctx); err != nil {
		return err
	}
	for {
		n, err := conn.WaitForNotification(r.ctx)
		if err != nil {
			return err
		}
		restored := map[int64]bool{}
		note := func(n *pgconn.Notification) {
			if id, err := strconv.ParseInt(strings.TrimPrefix(n.Payload, restoredPrefix), 10, 64); err == nil &&
				strings.HasPrefix(n.Payload, restoredPrefix) {
				restored[id] = true
			}
		}
		note(n)
		// A burst of changes (a CLI run, a provisioning) reloads once.
		for {
			wctx, cancel := context.WithTimeout(r.ctx, 50*time.Millisecond)
			n, err := conn.WaitForNotification(wctx)
			cancel()
			if err != nil {
				if r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					return err
				}
				break
			}
			note(n)
		}
		if err := r.Refresh(r.ctx); err != nil {
			r.log.Error("network host table", "err", err)
		}
		for id := range restored {
			r.Restored(r.ctx, id)
		}
	}
}

// Restored makes a loaded site pick up content that a restore replaced
// from outside its server: its caches are dropped at once, and it is
// loaded again once idle (plugins, designs, everything it keeps in
// memory). A site that is not loaded has nothing to drop.
func (r *Registry) Restored(ctx context.Context, id int64) {
	err := r.visit(ctx, id, false, func(e *entry) {
		if err := e.app.Server().Restored(ctx); err != nil {
			r.log.Error("reload a restored site", "site", id, "err", err)
		}
		r.mu.Lock()
		e.stale = true
		r.mu.Unlock()
	})
	if err != nil && !errors.Is(err, errNotServed) && ctx.Err() == nil {
		r.log.Error("reload a restored site", "site", id, "err", err)
	}
}
