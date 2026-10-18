// Package web contains the HTTP layer: public site, feeds, authentication and
// the administration panel.
package web

import (
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
}

type Deps struct {
	Config  *config.Config
	Log     *slog.Logger
	Store   *store.Store
	UI      fs.FS
	Plugins *plugin.Host
	Mailer  *mail.Mailer
}

func New(ctx context.Context, d Deps) (*Server, error) {
	s := &Server{
		cfg:        d.Config,
		log:        d.Log,
		store:      d.Store,
		keys:       auth.NewKeys(d.Config.SecretKey),
		ui:         d.UI,
		plugins:    d.Plugins,
		mailer:     d.Mailer,
		media:      &media.Processor{Dir: d.Config.UploadsDir, MaxBytes: d.Config.MaxUploadBytes},
		pages:      cache.New(2000, 64<<20, 2*time.Minute),
		content:    cache.New(1000, 32<<20, 24*time.Hour),
		loginIP:    auth.NewLimiter(20, 15*time.Minute),
		loginUser:  auth.NewLimiter(8, 15*time.Minute),
		mfaUser:    auth.NewLimiter(6, 15*time.Minute),
		commentIP:  auth.NewLimiter(6, 10*time.Minute),
		registerIP: auth.NewLimiter(5, time.Hour),
		resetIP:    auth.NewLimiter(5, time.Hour),
		uploadUser: auth.NewLimiter(120, time.Hour),
		cop:        http.NewCrossOriginProtection(),
	}
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
	s.themes = theme.NewManager(shared, funcs, s.langFuncs, d.Log, d.Config.Dev, theme.Source{FS: builtin, Builtin: true}, theme.Source{FS: dirFS(d.Config.ThemesDir)})
	if err := s.themes.Load(); err != nil {
		return nil, err
	}
	if s.admin, err = newAdminTemplates(d.UI, funcs, d.Config.Dev); err != nil {
		return nil, err
	}
	if err := s.prepareSetup(ctx); err != nil {
		return nil, err
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

// Background runs periodic maintenance until ctx is cancelled.
func (s *Server) Background(ctx context.Context) {
	go s.plugins.RunCron(ctx)
	go s.stats.Run(ctx)
	go s.assistant().Run(ctx)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				s.flushRedirectHits(context.WithoutCancel(ctx))
				return
			case <-t.C:
				s.flushRedirectHits(ctx)
				s.announceDue(ctx)
			}
		}
	}()
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		idle := time.Duration(s.settings().IdleHours) * time.Hour
		if n, err := s.store.PurgeExpired(ctx, idle); err != nil {
			s.log.Error("purge sessions", "err", err)
		} else if n > 0 {
			s.log.Info("expired sessions purged", "count", n)
		}
		if err := s.store.PruneAudit(ctx, 365); err != nil {
			s.log.Error("prune audit log", "err", err)
		}
		s.pruneStats(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
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
