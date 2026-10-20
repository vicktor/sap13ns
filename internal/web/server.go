// Package web contains the HTTP layer: public site, feeds, authentication and
// the administration panel.
package web

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	_ "time/tzdata" // self-contained timezone database

	"sap13ns/internal/analytics"
	"sap13ns/internal/auth"
	"sap13ns/internal/cache"
	"sap13ns/internal/config"
	"sap13ns/internal/geoip"
	"sap13ns/internal/i18n"
	"sap13ns/internal/mail"
	"sap13ns/internal/media"
	"sap13ns/internal/plugin"
	"sap13ns/internal/store"
	"sap13ns/internal/theme"
	"sap13ns/internal/update"
	"sap13ns/internal/videothumb"
)

// siteState is an immutable snapshot of the settings, swapped atomically.
type siteState struct {
	S     store.Settings
	Loc   *time.Location
	Stamp time.Time // when S was saved (store.SettingsStamp)
}

type Server struct {
	redirects redirectTable
	imports   importJob
	exports   exportJob
	backups   backupJob
	converted convertedThemes // themes converted from another CMS, awaiting confirmation (admin_theme_import.go)
	ai        aiState
	cfg       *config.Config
	log       *slog.Logger
	store     *store.Store
	keys      *auth.Keys
	i18n      *i18n.Catalogs
	themes    *theme.Manager
	plugins   *plugin.Host
	official  *plugin.Official // the official plugins in the binary (plugins_official.go); nil = none
	media     *media.Processor
	mailer    *mail.Mailer

	ui     fs.FS
	static map[string]string // static file -> content hash
	admin  *adminTemplates

	pages   *cache.Cache // rendered public responses
	content *cache.Cache // rendered post bodies

	state atomic.Pointer[siteState]
	site  siteCache
	front frontCache // the rendered front page, for the assistant (assistant_live.go)

	loginIP    *auth.Limiter
	loginUser  *auth.Limiter
	mfaUser    *auth.Limiter
	commentIP  *auth.Limiter
	registerIP *auth.Limiter
	resetIP    *auth.Limiter
	uploadUser *auth.Limiter
	tokenIP    *auth.Limiter // failed personal access tokens, per IP (design_mcp.go)
	agentUser  *auth.Limiter // design MCP calls, per user

	cop        *http.CrossOriginProtection
	setupMu    sync.Mutex
	setupToken string

	pk passkeyState // WebAuthn ceremonies (passkeys.go)

	signIn atomic.Pointer[signInRouter] // the sign-in pages at the sign-in address (signin.go)

	updates *update.Checker // release checks (updates.go); nil in tests
	// The update service's request file and status directory
	// (internal/update/request.go); tests point them elsewhere.
	updateRequest, updateStatus, updateUnit string
	stats                                   *analytics.Collector // cookie-less page views (stats.go)
	geo                                     *geoip.DB            // visitor countries for the stats; nil when off

	bot botState // site assistant and MCP (assistant.go, mcp.go)

	plug pluginWebState // plugin assets, forms and CSP sources (plugin_web.go)

	custom customCache // published theme designs (customize.go)

	plans planCache // the portal's pricing table (plans.go)

	grav gravatarState // Gravatar lookups and their copies (avatars.go)

	network     NetworkHooks   // nil on a single-site install
	sso         SSOClient      // a tenant of a network: sign-in through the portal
	portal      Portal         // the network portal
	billing     PortalBilling  // the portal of a network with a Stripe key (billing.go)
	reviews     PortalPlugins  // the portal: the operator's review of uploaded plugins (limits.go)
	limits      SiteLimits     // a site of a network: its plan (limits.go)
	switcher    SiteSwitcher   // a site of a network, the portal too: the admin's site switcher (switcher.go)
	switchCache switcherCache  // the switcher's list, per session
	backupHooks BackupHooks    // a site of a network: its name, and the reload after a restore (backups.go)
	netBackups  NetworkBackups // the portal: the network's nightly backups
	backupNow   *auth.Limiter  // backups started from Tools → Backups
	webSub      *http.Client   // tests: the client of WebSub pings (webSubClient)
	ssoFailures *auth.Limiter  // failed single sign-on callbacks, per IP
	checkout    *auth.Limiter  // Checkout and Customer Portal sessions, per account
	// DNS checks and additions of custom domains, per account
	domainChecks *auth.Limiter
	hosts        []string          // other host names of this site (multisite aliases)
	jobs         atomic.Int32      // background jobs started by requests (Busy)
	videos       *videothumb.Cache // posters and titles of embedded videos (video.go)
}

type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Store   *store.Store
	UI      fs.FS
	Plugins *plugin.Host
	Mailer  *mail.Mailer
	// Official is the bundle of official plugins the site can install
	// (plugin.LoadOfficial of internal/plugin/bundled); nil = none.
	Official *plugin.Official

	// Set by a network for each of its sites; nil and zero on a
	// single-site install, which keeps today's behaviour.
	Network    NetworkHooks
	Hosts      []string // other routed host names of the site, for analytics referrers
	CacheSizes CacheSizes
	// Videos is the poster cache of embedded videos; nil keeps one in
	// DataDir/cache/video-thumbs that fetches from YouTube and Vimeo.
	Videos *videothumb.Cache
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
		updateRequest: update.RequestFile,
		updateStatus:  update.StatusDir,
		updateUnit:    update.PathUnit,
		cfg:           d.Config,
		log:           d.Log,
		store:         d.Store,
		keys:          auth.NewKeys(d.Config.SecretKey),
		ui:            d.UI,
		plugins:       d.Plugins,
		official:      d.Official,
		mailer:        d.Mailer,
		network:       d.Network,
		hosts:         d.Hosts,
		media:         &media.Processor{Dir: d.Config.UploadsDir, MaxBytes: d.Config.MaxUploadBytes},
		pages:         cache.New(cmp.Or(cs.Pages, 2000), cmp.Or(cs.PagesBytes, 64<<20), 2*time.Minute),
		content:       cache.New(cmp.Or(cs.Content, 1000), cmp.Or(cs.ContentBytes, 32<<20), 24*time.Hour),
		loginIP:       auth.NewLimiter(20, 15*time.Minute),
		loginUser:     auth.NewLimiter(8, 15*time.Minute),
		mfaUser:       auth.NewLimiter(6, 15*time.Minute),
		commentIP:     auth.NewLimiter(6, 10*time.Minute),
		registerIP:    auth.NewLimiter(5, time.Hour),
		resetIP:       auth.NewLimiter(5, time.Hour),
		uploadUser:    auth.NewLimiter(120, time.Hour),
		tokenIP:       auth.NewLimiter(20, 10*time.Minute),
		agentUser:     auth.NewLimiter(120, time.Minute),
		cop:           http.NewCrossOriginProtection(),
		grav:          newGravatarState(),

		ssoFailures: auth.NewLimiter(10, 15*time.Minute),
		checkout:    auth.NewLimiter(30, time.Hour),
		// DNS checks and additions of custom domains, per account.
		domainChecks: auth.NewLimiter(30, time.Hour),
		backupNow:    auth.NewLimiter(6, time.Hour),
	}
	s.videos = d.Videos
	if s.videos == nil && d.Config.DataDir != "" {
		s.videos = videothumb.New(videothumb.Options{Dir: filepath.Join(d.Config.DataDir, "cache", "video-thumbs"), Log: d.Log})
	}
	if s.videos != nil {
		// A new poster or title changes the pages that show the video.
		s.videos.OnChange(func(string) { s.pages.Invalidate(); s.content.Invalidate() })
	}
	s.sso, _ = d.Network.(SSOClient)
	s.portal, _ = d.Network.(Portal)
	s.backupHooks, _ = d.Network.(BackupHooks)
	s.switcher, _ = d.Network.(SiteSwitcher)
	if b, ok := d.Network.(PortalBilling); ok && s.portal != nil && b.BillingEnabled() {
		s.billing = b
	}
	if s.portal != nil {
		s.reviews, _ = d.Network.(PortalPlugins)
		s.netBackups, _ = d.Network.(NetworkBackups)
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
	if s.themes, err = s.newThemeManager(funcs); err != nil {
		return nil, err
	}
	if err := s.themes.Load(); err != nil {
		return nil, err
	}
	if s.admin, err = newAdminTemplates(d.UI, s.adminFuncs, d.Config.Dev); err != nil {
		return nil, err
	}
	if s.network == nil {
		if err := s.prepareSetup(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// newThemeManager builds the theme manager. The site's own themes
// directory, where uploads are installed, overrides the network's (on a
// site of a network), which overrides the built-in themes; an uploaded
// theme never shadows a theme of the other two.
func (s *Server) newThemeManager(funcs template.FuncMap) (*theme.Manager, error) {
	shared, err := fs.Sub(s.ui, "shared/templates")
	if err != nil {
		return nil, err
	}
	builtin, err := fs.Sub(s.ui, "themes")
	if err != nil {
		return nil, err
	}
	sources := []theme.Source{{FS: builtin, Builtin: true}}
	if dir := s.cfg.NetworkThemesDir; dir != "" {
		sources = append(sources, theme.Source{FS: dirFS(dir)})
	}
	if dir := s.cfg.ThemesDir; dir != "" {
		sources = append(sources, theme.Source{Dir: dir})
	}
	m := theme.NewManager(shared, funcs, s.langFuncs, s.log, s.cfg.Dev, sources...)
	m.SetPageType(reflect.TypeFor[*PageData]())
	return m, nil
}

// ThemeManager returns the theme manager of a site without a database or
// a server, for the command line (sap13ns theme): the templates get the
// server's functions, so an install validates as it does in the admin.
func ThemeManager(cfg *config.Config, ui fs.FS, log *slog.Logger) (*theme.Manager, error) {
	s := &Server{cfg: cfg, log: log, ui: ui}
	var err error
	if s.i18n, err = i18n.Load(ui, "i18n"); err != nil {
		return nil, err
	}
	s.state.Store(&siteState{S: store.Settings{Language: "en", Timezone: "UTC"}, Loc: time.UTC})
	s.static = hashFiles(ui, "static")
	m, err := s.newThemeManager(s.funcMap())
	if err != nil {
		return nil, err
	}
	return m, m.Load()
}

// ReloadSettings refreshes the cached settings snapshot.
func (s *Server) ReloadSettings(ctx context.Context) error {
	stamp, err := s.store.SettingsStamp(ctx)
	if err != nil {
		return err
	}
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
	s.state.Store(&siteState{S: st, Loc: loc, Stamp: stamp})
	s.stats.SetEnabled(!st.AnalyticsOff) // nil-safe: not created yet on the first load
	s.stats.SetLocation(loc)
	return nil
}

func (s *Server) settings() store.Settings { return s.state.Load().S }
func (s *Server) loc() *time.Location      { return s.state.Load().Loc }
func (s *Server) lang() string             { return s.state.Load().S.Language }

func (s *Server) t(key string, args ...any) string { return s.i18n.T(s.lang(), key, args...) }

// followSettings reloads the settings when they were saved outside this
// server, by the command line (sap13ns settings) or another process, and
// drops the pages that showed the old ones.
func (s *Server) followSettings(ctx context.Context) {
	stamp, err := s.store.SettingsStamp(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("settings stamp", "err", err)
		}
		return
	}
	if stamp.Equal(s.state.Load().Stamp) {
		return
	}
	if err := s.ReloadSettings(ctx); err != nil {
		s.log.Error("reload settings", "err", err)
		return
	}
	s.contentChanged()
}

// contentChanged invalidates caches after any write that affects public pages.
func (s *Server) contentChanged() {
	s.pages.Invalidate()
	s.content.Invalidate() // bodies can show other posts (shortcodes, filters)
	s.site.invalidate()
	s.custom.invalidate()
	s.front.invalidate()
	s.assistant().Nudge()
}

// Restored makes the server pick up a site whose content and settings a
// backup restore replaced: it reloads the settings and the plugins, and
// drops every cache (pages, bodies, menus, designs, AI and assistant
// settings; the assistant's index is built again).
func (s *Server) Restored(ctx context.Context) error {
	err := s.ReloadSettings(ctx)
	if s.plugins != nil {
		err = errors.Join(err, s.plugins.Load(ctx))
	}
	s.resetAI()
	s.resetAssistant() // and contentChanged
	return err
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
		s.log.Warn("no users yet: finish the installation at " + s.cfg.AbsURL(s.auth().Setup+"?token="+s.setupToken))
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
	wg.Go(func() { s.runGravatars(ctx) }) // avatars.go
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
				s.followSettings(ctx)
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

// Maintain purges expired sessions, prunes the audit log and old
// analytics, and removes expired exports.
func (s *Server) Maintain(ctx context.Context) {
	Maintain(ctx, s.store, s.settings(), s.log)
	s.SweepExports(time.Now())
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
// an export, an AI job, an index rebuild, a WebSub ping). A network does
// not unload a busy site.
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
// themeCookie keeps the light or dark choice on every site of a network
// (theme-init.js writes it).
const themeCookie = "sap13ns-theme"

// themeDomain returns the domain whose sites share the light or dark choice
// (theme-init.js): the sites domain of a network, "" for a single site.
func (s *Server) themeDomain() string {
	if !s.cfg.Multisite {
		return ""
	}
	return s.cfg.SitesDomain
}

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
