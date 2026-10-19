// Package web contains the HTTP layer: public site, feeds, authentication and
// the administration panel.
package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // self-contained timezone database

	"sap13ns/internal/analytics"
	"sap13ns/internal/auth"
	"sap13ns/internal/cache"
	"sap13ns/internal/config"
	"sap13ns/internal/i18n"
	"sap13ns/internal/mail"
	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
	"sap13ns/internal/update"
)

// siteState is an immutable snapshot of the settings, swapped atomically.
type siteState struct {
	S   store.Settings
	Loc *time.Location
}

type Server struct {
	redirects redirectTable
	imports   importJob
	ai        aiState
	cfg       *config.Config
	log       *slog.Logger
	store     *store.Store
	keys      *auth.Keys
	i18n      *i18n.Catalogs
	themes    *theme.Manager
	plugins   *plugin.Host
	media     *media.Processor
	mailer    *mail.Mailer

	ui     fs.FS
	static map[string]string // static file -> content hash
	admin  *adminTemplates

	pages   *cache.Cache // rendered public responses
	content *cache.Cache // rendered post bodies

	state atomic.Pointer[siteState]
	site  siteCache

	loginIP    *auth.Limiter
	loginUser  *auth.Limiter
	mfaUser    *auth.Limiter
	commentIP  *auth.Limiter
	registerIP *auth.Limiter
	resetIP    *auth.Limiter
	uploadUser *auth.Limiter

	cop        *http.CrossOriginProtection
	setupMu    sync.Mutex
	setupToken string

	pk passkeyState // WebAuthn ceremonies (passkeys.go)

	updates *update.Checker      // release checks (updates.go); nil in tests
	stats   *analytics.Collector // cookie-less page views (stats.go)

	bot botState // site assistant and MCP (assistant.go, mcp.go)

	plug pluginWebState // plugin assets, forms and CSP sources (plugin_web.go)

	custom customCache // published theme designs (customize.go)

	network     NetworkHooks  // nil on a single-site install
	sso         SSOClient     // a tenant of a network: sign-in through the portal
	portal      Portal        // the network portal
	billing     PortalBilling // the portal of a network with a Stripe key (billing.go)
	reviews     PortalPlugins // the portal: the operator's review of uploaded plugins (limits.go)
	limits      SiteLimits    // a site of a network: its plan (limits.go)
	ssoFailures *auth.Limiter // failed single sign-on callbacks, per IP
	checkout    *auth.Limiter // Checkout and Customer Portal sessions, per account
	// DNS checks and additions of custom domains, per account
	domainChecks *auth.Limiter
	hosts        []string     // other host names of this site (multisite aliases)
	jobs         atomic.Int32 // background jobs started by requests (Busy)
}

type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Store   *store.Store
	UI      fs.FS
	Plugins *plugin.Host
	Mailer  *mail.Mailer

	// Set by a network for each of its sites; nil and zero on a
	// single-site install, which keeps today's behaviour.
	Network    NetworkHooks
	Hosts      []string // other routed host names of the site, for analytics referrers
	CacheSizes CacheSizes
}

// NetworkHooks connect the server of one site to the network that runs it
// (internal/network). A site of a network never uses the web installer:
// its users come from the network. The hooks of a tenant also implement
// SSOClient, those of the portal Portal (sso.go).
type NetworkHooks interface {
	// Wakeup asks the network to load the site at at, when a scheduled
	// post is due, in case it is unloaded by then.
	Wakeup(ctx context.Context, at time.Time)
}

// CacheSizes bound the in-memory caches of a server. Zero fields keep the
// single-site defaults; a network gives each site smaller ones.
type CacheSizes struct {
	Pages, PagesBytes     int // rendered public responses
	Content, ContentBytes int // rendered post bodies
}

func New(ctx context.Context, d Deps) (*Server, error) {
	cs := d.CacheSizes
	s := &Server{
		cfg:        d.Config,
		log:        d.Log,
		store:      d.Store,
		keys:       auth.NewKeys(d.Config.SecretKey),
		ui:         d.UI,
		plugins:    d.Plugins,
		mailer:     d.Mailer,
		network:    d.Network,
		hosts:      d.Hosts,
		media:      &media.Processor{Dir: d.Config.UploadsDir, MaxBytes: d.Config.MaxUploadBytes},
		pages:      cache.New(cmp.Or(cs.Pages, 2000), cmp.Or(cs.PagesBytes, 64<<20), 2*time.Minute),
		content:    cache.New(cmp.Or(cs.Content, 1000), cmp.Or(cs.ContentBytes, 32<<20), 24*time.Hour),
		loginIP:    auth.NewLimiter(20, 15*time.Minute),
		loginUser:  auth.NewLimiter(8, 15*time.Minute),
		mfaUser:    auth.NewLimiter(6, 15*time.Minute),
		commentIP:  auth.NewLimiter(6, 10*time.Minute),
		registerIP: auth.NewLimiter(5, time.Hour),
		resetIP:    auth.NewLimiter(5, time.Hour),
		uploadUser: auth.NewLimiter(120, time.Hour),
		cop:        http.NewCrossOriginProtection(),

		ssoFailures: auth.NewLimiter(10, 15*time.Minute),
		checkout:    auth.NewLimiter(30, time.Hour),
		// DNS checks and additions of custom domains, per account.
		domainChecks: auth.NewLimiter(30, time.Hour),
	}
	s.sso, _ = d.Network.(SSOClient)
	s.portal, _ = d.Network.(Portal)
	if b, ok := d.Network.(PortalBilling); ok && s.portal != nil && b.BillingEnabled() {
		s.billing = b
	}
	if s.portal != nil {
		s.reviews, _ = d.Network.(PortalPlugins)
	} else {
		s.limits, _ = d.Network.(SiteLimits)
	}
	s.media.Quota = storageQuota(s.limits)
	if err := s.cop.AddTrustedOrigin(d.Config.BaseURL.String()); err != nil {
		return nil, err
	}
	s.cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.log.Warn("cross-origin request blocked", "path", r.URL.Path, "origin", r.Header.Get("Origin"), "ip", clientIP(r))
		http.Error(w, "cross-origin request blocked", http.StatusForbidden)
	}))

	var err error
	if s.i18n, err = i18n.Load(d.UI, "i18n"); err != nil {
		return nil, err
	}
	if err := s.ReloadSettings(ctx); err != nil {
		return nil, err
	}
	s.stats = s.newStats()
	s.static = hashFiles(d.UI, "static")

	funcs := s.funcMap()
	shared, err := fs.Sub(d.UI, "shared/templates")
	if err != nil {
		return nil, err
	}
	builtin, err := fs.Sub(d.UI, "themes")
	if err != nil {
		return nil, err
	}
	// The site's own themes override the network's (a site of a network),
	// which override the built-in ones.
	sources := []theme.Source{{FS: builtin, Builtin: true}}
	if dir := d.Config.NetworkThemesDir; dir != "" {
		sources = append(sources, theme.Source{FS: dirFS(dir)})
	}
	s.themes = theme.NewManager(shared, funcs, s.langFuncs, d.Log, d.Config.Dev, append(sources, theme.Source{FS: dirFS(d.Config.ThemesDir)})...)
	if err := s.themes.Load(); err != nil {
		return nil, err
	}
	if s.admin, err = newAdminTemplates(d.UI, funcs, d.Config.Dev); err != nil {
		return nil, err
	}
	if s.network == nil {
		if err := s.prepareSetup(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ReloadSettings refreshes the cached settings snapshot.
func (s *Server) ReloadSettings(ctx context.Context) error {
	st, err := s.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	if !s.i18n.Has(st.Language) {
		st.Language = "en"
	}
	loc, err := time.LoadLocation(st.Timezone)
	if err != nil {
		loc = time.UTC
	}
	s.state.Store(&siteState{S: st, Loc: loc})
	s.stats.SetEnabled(!st.AnalyticsOff) // nil-safe: not created yet on the first load
	s.stats.SetLocation(loc)
	return nil
}

func (s *Server) settings() store.Settings { return s.state.Load().S }
func (s *Server) loc() *time.Location      { return s.state.Load().Loc }
func (s *Server) lang() string             { return s.state.Load().S.Language }

func (s *Server) t(key string, args ...any) string { return s.i18n.T(s.lang(), key, args...) }

// contentChanged invalidates caches after any write that affects public pages.
func (s *Server) contentChanged() {
	s.pages.Invalidate()
	s.content.Invalidate() // bodies can show other posts (shortcodes, filters)
	s.site.invalidate()
	s.custom.invalidate()
	s.assistant().Nudge()
}

// prepareSetup creates a one-time token for the web installer when the
// database has no users yet. The token is only printed to the server log, so
// a stranger who finds a fresh install cannot claim it.
func (s *Server) prepareSetup(ctx context.Context) error {
	n, err := s.store.CountUsers(ctx)
	if err != nil {
		return err
	}
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if n == 0 {
		s.setupToken = auth.NewToken(24)
		s.log.Warn("no users yet: finish the installation at " + s.cfg.AbsURL("/setup?token="+s.setupToken))
	} else {
		s.setupToken = ""
	}
	return nil
}

// BackgroundOptions choose the loops Background runs besides analytics,
// the assistant index, redirect hits and scheduled posts. A single-site
// install runs both; a network drives them from its registry instead, for
// loaded and unloaded sites alike.
type BackgroundOptions struct {
	Cron        bool // the plugins' cron.hourly event
	Maintenance bool // Maintain every 30 minutes
}

// Background runs periodic work until ctx is cancelled, and returns once
// every loop it started has stopped.
func (s *Server) Background(ctx context.Context, opt BackgroundOptions) {
	var wg sync.WaitGroup
	defer wg.Wait()
	if opt.Cron {
		wg.Go(func() { s.plugins.RunCron(ctx) })
	}
	wg.Go(func() { s.stats.Run(ctx) })
	wg.Go(func() { s.assistant().Run(ctx) })
	wg.Go(func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flushRedirectHits(context.WithoutCancel(ctx))
				return
			case <-t.C:
				s.flushRedirectHits(ctx)
				s.AnnounceDue(ctx)
			}
		}
	})
	if !opt.Maintenance {
		<-ctx.Done()
		return
	}
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		s.Maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Maintain purges expired sessions and prunes the audit log and old
// analytics.
func (s *Server) Maintain(ctx context.Context) {
	Maintain(ctx, s.store, s.settings(), s.log)
}

// Maintain is the periodic cleanup of one site's store, with its settings.
// A network runs it over every site schema without loading their servers.
func Maintain(ctx context.Context, st *store.Store, set store.Settings, log *slog.Logger) {
	idle := time.Duration(set.IdleHours) * time.Hour
	if n, err := st.PurgeExpired(ctx, idle); err != nil {
		log.Error("purge sessions", "err", err)
	} else if n > 0 {
		log.Info("expired sessions purged", "count", n)
	}
	if err := st.PruneAudit(ctx, 365); err != nil {
		log.Error("prune audit log", "err", err)
	}
	pruneStats(ctx, st, log)
}

// Busy reports whether a job started by a request still runs (an import,
// an AI job, an index rebuild, a WebSub ping). A network does not unload
// a busy site.
func (s *Server) Busy() bool { return s.jobs.Load() > 0 }

// goJob runs fn in the background as a job Busy reports.
func (s *Server) goJob(fn func()) {
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Add(-1)
		fn()
	}()
}

func hashFiles(fsys fs.FS, dir string) map[string]string {
	out := map[string]string{}
	fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		out[strings.TrimPrefix(p, dir+"/")] = hex.EncodeToString(sum[:6])
		return nil
	})
	return out
}

// assetURL returns a cache-busted URL for a file under ui/static.
func (s *Server) assetURL(file string) string {
	if h, ok := s.staticHash(file); ok {
		return "/static/" + file + "?v=" + h
	}
	return "/static/" + file
}

// staticHash returns the content hash of a static file. In development the
// UI may be served from disk, so the hash is recomputed on every call.
func (s *Server) staticHash(file string) (string, bool) {
	if !s.cfg.Dev {
		h, ok := s.static[file]
		return h, ok
	}
	b, err := fs.ReadFile(s.ui, "static/"+file)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6]), true
}

// SiteInfo describes the site to plugins.
func (s *Server) SiteInfo() plugin.SiteInfo {
	st := s.settings()
	return plugin.SiteInfo{URL: s.cfg.BaseURL.String(), Title: st.SiteTitle, Language: st.Language, Timezone: s.state.Load().Loc.String()}
}
