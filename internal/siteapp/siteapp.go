// Package siteapp wires the server of one site: its directories, plugin
// host, web server and background loops. A single-site install opens one
// App in `sap13ns serve`; a network opens one for each site it loads
// (internal/network). Both go through Open, so they cannot drift apart.
package siteapp

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/mail"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/update"
	"sap13ns/internal/web"
)

// Params describe one site. Config, Store, Log and UI are required; the
// zero values of the rest give a single-site install.
type Params struct {
	Config *config.Config // config.Load, or config.ForSite for a site of a network
	Store  *store.Store
	Log    *slog.Logger
	UI     fs.FS
	Mailer *mail.Mailer // nil: a mailer from Config.SMTP

	// Blocklist is the operator's extra list of known-bad plugin hashes;
	// "" means <DataDir>/plugin-blocklist.txt.
	Blocklist string

	Network    web.NetworkHooks
	Quota      plugin.Quota // a site of a network: installed plugins count against its storage; nil = none
	Hosts      []string     // other routed host names of the site
	CacheSizes web.CacheSizes
	Plugins    PluginLimits
	Background web.BackgroundOptions
	Updates    *update.Checker // release checks shown to administrators; nil = none

	// Inert opens the site without running anything of the tenant's: its
	// plugin host is inert (plugin.HostOptions.Inert), so no plugin loads
	// and no hook, filter, shortcode, widget, form or plugin asset is
	// found, and the background loops (scheduled posts, the plugin cron,
	// analytics and redirect flushes, the assistant index, maintenance) do
	// not start. A network opens a suspended site this way, for an
	// emergency access only.
	Inert bool
}

// PluginLimits are the plugin.HostOptions a network shares between sites.
// Without a Cache, compiled modules are cached under <DataDir>/cache/wasm.
type PluginLimits struct {
	Cache            wazero.CompilationCache
	MemoryLimitPages uint32
	MaxInstances     int
	Limit            *plugin.InstanceLimit
}

// An App is an open site: an http.Handler that counts the requests in
// flight, so Close can wait for them.
type App struct {
	cfg     *config.Config
	store   *store.Store
	srv     *web.Server
	host    *plugin.Host
	handler http.Handler
	log     *slog.Logger

	inert  bool
	cancel context.CancelFunc
	bgDone chan struct{}

	mu      sync.Mutex
	active  int
	closing bool
	drained chan struct{} // closed once closing and no request is in flight

	closeOnce sync.Once
	closeErr  error

	step func(string) // Close reports each step to tests
}

// links forwards plugin URL lookups to the web server, which is created
// after the plugin host.
type links struct{ s *web.Server }

func (l *links) PostInfo(p *store.Post) plugin.PostInfo { return l.s.PostInfo(p) }

// Open creates the site's directories, plugin host and server, loads its
// plugins and starts its background loops, unless p.Inert. ctx bounds the
// opening only: the loops run until Close.
func Open(ctx context.Context, p Params) (*App, error) {
	cfg := p.Config
	if cfg == nil || p.Store == nil || p.Log == nil || p.UI == nil {
		return nil, errors.New("siteapp: Config, Store, Log and UI are required")
	}
	for _, dir := range []string{cfg.DataDir, cfg.UploadsDir, cfg.PluginsDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
	}
	a := &App{cfg: cfg, store: p.Store, log: p.Log, inert: p.Inert, bgDone: make(chan struct{}), drained: make(chan struct{})}
	lk := &links{}
	host, err := plugin.NewHostWithOptions(ctx, plugin.HostOptions{
		Dir: cfg.PluginsDir, CacheDir: filepath.Join(cfg.DataDir, "cache", "wasm"), Cache: p.Plugins.Cache,
		Store: p.Store, Log: p.Log, Site: func() plugin.SiteInfo { return a.srv.SiteInfo() }, Links: lk,
		MemoryLimitPages: p.Plugins.MemoryLimitPages, MaxInstances: p.Plugins.MaxInstances, Limit: p.Plugins.Limit,
		Quota: p.Quota, Inert: p.Inert,
	})
	if err != nil {
		return nil, err
	}
	mailer := p.Mailer
	if mailer == nil {
		mailer = mail.New(cfg.SMTP, p.Log, cfg.Dev)
	}
	blocklist := p.Blocklist
	if blocklist == "" {
		blocklist = filepath.Join(cfg.DataDir, "plugin-blocklist.txt")
	}
	host.SetSecrets(auth.NewKeys(cfg.SecretKey)) // secret plugin settings, encrypted at rest
	host.SetMailer(mailer)                       // the mail_send host function
	host.SetBlocklistFile(blocklist)
	srv, err := web.New(ctx, web.Deps{Config: cfg, Log: p.Log, Store: p.Store, UI: p.UI, Plugins: host, Mailer: mailer,
		Network: p.Network, Hosts: p.Hosts, CacheSizes: p.CacheSizes})
	if err != nil {
		host.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	a.srv, a.host, lk.s = srv, host, srv
	if !p.Inert {
		if err := host.Load(ctx); err != nil {
			p.Log.Error("load plugins", "err", err)
		}
	}
	if p.Updates != nil {
		srv.SetUpdateChecker(p.Updates)
	}
	a.handler = srv.Handler() // Handler builds a new mux on every call

	bg, cancel := context.WithCancel(context.WithoutCancel(ctx))
	a.cancel = cancel
	if p.Inert {
		close(a.bgDone) // nothing runs in the background
		return a, nil
	}
	go func() {
		defer close(a.bgDone)
		srv.Background(bg, p.Background)
	}()
	return a, nil
}

func (a *App) Config() *config.Config { return a.cfg }
func (a *App) Store() *store.Store    { return a.store }
func (a *App) Server() *web.Server    { return a.srv }
func (a *App) Plugins() *plugin.Host  { return a.host }

// Inert reports whether the App was opened with Params.Inert.
func (a *App) Inert() bool { return a.inert }

// ServeHTTP serves a request unless the App is closing, which answers 503.
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !a.enter() {
		w.Header().Set("Retry-After", "5")
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	defer a.leave()
	a.handler.ServeHTTP(w, r)
}

func (a *App) enter() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return false
	}
	a.active++
	return true
}

func (a *App) leave() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.active--
	if a.closing && a.active == 0 {
		close(a.drained)
	}
}

// Active is the number of requests in flight.
func (a *App) Active() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active
}

// Busy reports whether the site serves requests or runs a job a request
// started (an import, an AI job); a network does not unload it then.
func (a *App) Busy() bool { return a.Active() > 0 || a.srv.Busy() }

// Close stops the site, in this order: the background loops are
// cancelled and new requests refused, the requests in flight finish, the
// server flushes its analytics and redirect hits, and the plugin host
// delivers its queued events and closes. ctx bounds the waits; once it
// ends, the remaining steps run anyway. Close may be called more than
// once.
func (a *App) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.cancel()
		a.mu.Lock()
		a.closing = true
		if a.active == 0 {
			close(a.drained)
		}
		a.mu.Unlock()
		select {
		case <-a.bgDone:
		case <-ctx.Done():
			a.log.Warn("site closing before its background loops stopped")
		}
		a.report("background")
		select {
		case <-a.drained:
		case <-ctx.Done():
			a.log.Warn("site closing with requests in flight", "requests", a.Active())
		}
		a.report("requests")
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flushTimeout)
		defer cancel()
		a.closeErr = a.srv.Close(fctx)
		a.report("server")
		a.host.Close(context.WithoutCancel(ctx))
		a.report("plugins")
	})
	return a.closeErr
}

// flushTimeout bounds the final writes of Close, which run even when the
// caller's context has ended.
const flushTimeout = 15 * time.Second

func (a *App) report(step string) {
	if a.step != nil {
		a.step(step)
	}
}
