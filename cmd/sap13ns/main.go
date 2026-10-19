// Command sap13ns is a lightweight, security-focused content management system.
//
//	sap13ns serve                 run the web server (default)
//	sap13ns migrate               apply database migrations and exit
//	sap13ns keygen                print a new SAP13NS_SECRET_KEY
//	sap13ns user create ...       create a user from the command line
//	sap13ns user reset-password   set a new password for a user
//	sap13ns plugin new|pack|check create, package and scan plugins (plugin.go)
//	sap13ns plugin list|install   list and install the plugins of a site
//	sap13ns assistant reindex     rebuild the site assistant's search index
//	sap13ns export -o FILE        export the content as WXR or a .zip (export.go)
//	sap13ns network init          set up a network (SAP13NS_MULTISITE=true)
//	sap13ns site ...              create, list and manage the sites of a network
//	sap13ns version               print the version
//
// Configuration comes from SAP13NS_* environment variables, optionally loaded
// from a file with -env /etc/sap13ns/sap13ns.env. On a network, the commands
// that work on one site take -site SLUG (network.go).
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/siteapp"
	"sap13ns/internal/store"
	"sap13ns/internal/update"
	"sap13ns/internal/web"
	"sap13ns/ui"
)

func main() {
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve(args)
	case "migrate":
		err = migrate(args)
	case "keygen":
		b := make([]byte, 32)
		if _, err = rand.Read(b); err == nil {
			fmt.Println(hex.EncodeToString(b))
		}
	case "user":
		err = userCmd(args)
	case "plugin":
		err = pluginCmd(args)
	case "backup":
		err = backupCmd(args)
	case "export":
		err = exportCmd(args)
	case "assistant":
		err = assistantCmd(args)
	case "update":
		err = updateCmd(args)
	case "network":
		err = networkCmd(args)
	case "site":
		err = siteCmd(args)
	case "version", "-v", "--version":
		fmt.Printf("sap13ns %s (%s %s/%s)\n", web.Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: sap13ns [command] [-env file] [flags]

Commands:
  serve                          Run the web server (default)
  migrate                        Apply database migrations
  keygen                         Generate a secret key for SAP13NS_SECRET_KEY
  user create -username U -email E [-role admin] [-name N]
                                 Create a user. The password is read from stdin
                                 when piped, otherwise a strong one is generated.
  user reset-password -username U
                                 Set a new password (same rules as above)
  plugin new <id> [-dir D] [-hooks shortcode,widget,...] [-sdk PATH]
                                 Start a plugin project from the skeleton
  plugin pack <dir>              Build, scan and write <id>-<version>.zip
  plugin check <dir|file.zip>    Scan a plugin as an upload is scanned
                                 (exit status 1 on critical findings)
  plugin list | install <file.zip>
                                 List or install this site's plugins (installed
                                 disabled; enable them in Admin → Plugins)
  plugin hash <dir>              Print the SHA-256 of a plugin's wasm module
  backup create|list|verify|restore|prune
                                 Encrypted backups of the database and uploads
                                 (see docs/DEPLOY.md)
  export -o FILE.xml|FILE.zip [-drafts] [-pii] [-no-media] [-pending-comments]
                                 Export the content as a WordPress file (WXR),
                                 or a .zip with the uploads and Markdown copies
  assistant reindex [-full] [-changed] | status
                                 Rebuild the site assistant's search index, or
                                 show its state (embeddings, pgvector, failures)
  update [-check] [-yes] [-force]
                                 Install the latest signed release over this
                                 binary (see docs/RELEASES.md)
  version                        Print the version

Networks (SAP13NS_MULTISITE=true):
  network init [-username U -email E [-name N]]
                                 Create the network schema and the portal site
                                 on the SAP13NS_BASE_URL host, and a network
                                 administrator (password as for user create)
  site create -slug S -owner EMAIL [-plan P] [-title T] [-lang L]
  site list [-status S] [-json]
  site suspend -slug S -reason R | resume -slug S | delete -slug S -yes
  site purge -slug S -yes        Remove a deleted site for good
  site domain add -slug S HOST | verify [-force] HOST | primary HOST | remove HOST
                                 Manage the sites; running servers follow
  migrate [-site S]              Migrate the network, then every site (or one)
  user, plugin list|install, assistant, export -site S
                                 Work on one site; -site network is the portal
`)
}

// setup parses the common flags, loads configuration and builds a logger.
func setup(name string, args []string, requireSecret bool, extra func(*flag.FlagSet)) (*config.Config, *slog.Logger, *flag.FlagSet, error) {
	fsFlags := flag.NewFlagSet(name, flag.ContinueOnError)
	envFile := fsFlags.String("env", "", "load SAP13NS_* variables from this file")
	if extra != nil {
		extra(fsFlags)
	}
	if err := fsFlags.Parse(args); err != nil {
		return nil, nil, nil, err
	}
	if *envFile != "" {
		if err := config.LoadEnvFile(*envFile); err != nil {
			return nil, nil, nil, err
		}
	}
	cfg, err := config.Load(requireSecret)
	if err != nil {
		return nil, nil, nil, err
	}
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	var h slog.Handler = slog.NewJSONHandler(os.Stderr, opts)
	if cfg.LogFormat == "text" || cfg.Dev {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	log := slog.New(h)
	slog.SetDefault(log)
	return cfg, log, fsFlags, nil
}

func migrate(args []string) error {
	var site string
	cfg, log, _, err := setup("migrate", args, false, func(f *flag.FlagSet) {
		f.StringVar(&site, "site", "", "network: migrate the network schema and this site only")
	})
	if err != nil {
		return err
	}
	ctx := context.Background()
	if cfg.Multisite {
		pool, n, err := openNetwork(ctx, cfg, log, 4)
		if err != nil {
			return err
		}
		defer pool.Close()
		return migrateSites(ctx, pool, n, log, site)
	}
	if site != "" {
		return errNotNetwork
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool, log)
}

func serve(args []string) error {
	cfg, log, _, err := setup("serve", args, true, nil)
	if err != nil {
		return err
	}
	if cfg.Dev {
		log.Warn("development mode: templates reload from disk, e-mails are logged, never use this in production")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.Multisite {
		return serveNetwork(ctx, cfg, log)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return err
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	checker := update.NewChecker(web.Version, nil, log)
	app, err := siteapp.Open(ctx, siteapp.Params{Config: cfg, Store: store.New(pool), Log: log, UI: uiFS(cfg),
		Background: web.BackgroundOptions{Cron: true, Maintenance: true}, Updates: checker})
	if err != nil {
		return err
	}
	go checker.Run(ctx)
	return runHTTP(ctx, cfg, log, app, func(sctx context.Context) {
		if err := app.Close(sctx); err != nil {
			log.Error("analytics final flush", "err", err)
		}
	}, nil)
}

// uiFS is the embedded UI, or ui/ from disk in development mode when run
// from a checkout (live templates and assets).
func uiFS(cfg *config.Config) fs.FS {
	if cfg.Dev {
		if _, err := os.Stat("ui/admin/templates"); err == nil {
			return os.DirFS("ui")
		}
	}
	return ui.FS
}

// tlsServe is a process that terminates TLS itself (SAP13NS_TLS=autocert):
// the TLS configuration of SAP13NS_LISTEN, and the plain HTTP handler of
// SAP13NS_HTTP_LISTEN ("" for none).
type tlsServe struct {
	config     *tls.Config
	http       http.Handler
	httpListen string
}

func newHTTPServer(ctx context.Context, log *slog.Logger, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // large uploads on slow links
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
}

// runHTTP serves h on SAP13NS_LISTEN (over TLS with ts, plus its plain
// HTTP listener) until ctx ends or a server fails. Then it shuts the HTTP
// servers down and calls closeApp, in every case.
func runHTTP(ctx context.Context, cfg *config.Config, log *slog.Logger, h http.Handler, closeApp func(context.Context), ts *tlsServe) error {
	type served struct {
		hs   *http.Server
		ln   net.Listener
		addr string
		tls  bool
	}
	var all []served
	ln, err := listen(cfg.Listen, cfg.SocketGroup)
	if err == nil {
		hs := newHTTPServer(ctx, log, h)
		all = append(all, served{hs: hs, ln: ln, addr: cfg.Listen})
		if ts != nil {
			hs.TLSConfig = ts.config
			all[0].tls = true
			if ts.httpListen != "" {
				var pln net.Listener
				if pln, err = net.Listen("tcp", ts.httpListen); err == nil {
					ps := newHTTPServer(ctx, log, ts.http)
					ps.ReadTimeout, ps.WriteTimeout = 30*time.Second, 30*time.Second
					all = append(all, served{hs: ps, ln: pln, addr: ts.httpListen})
				} else {
					ln.Close()
				}
			}
		}
	}
	if err == nil {
		errc := make(chan error, len(all))
		for _, s := range all {
			go func() {
				log.Info("sap13ns listening", "addr", s.addr, "tls", s.tls, "url", cfg.BaseURL.String(), "version", web.Version)
				if s.tls {
					errc <- s.hs.ServeTLS(s.ln, "", "")
				} else {
					errc <- s.hs.Serve(s.ln)
				}
			}()
		}
		select {
		case err = <-errc:
			if errors.Is(err, http.ErrServerClosed) {
				err = nil
			}
		case <-ctx.Done():
			log.Info("shutting down")
		}
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		for _, s := range all {
			if err := s.hs.Shutdown(sctx); err != nil {
				log.Error("shutdown", "addr", s.addr, "err", err)
			}
		}
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	closeApp(sctx)
	return err
}

// listen supports "host:port" and "unix:/path/to.sock" (recommended behind nginx).
//
// group, when set, owns the socket: nginx is a member of that group only,
// not of the service's own group, so it cannot read the env file or data.
func listen(addr, group string) (net.Listener, error) {
	path, ok := strings.CutPrefix(addr, "unix:")
	if !ok {
		return net.Listen("tcp", addr)
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
		os.Remove(path) // stale socket from a previous run
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// nginx must be in the socket's group; nobody else can connect.
	if err := os.Chmod(path, 0o660); err != nil {
		ln.Close()
		return nil, err
	}
	if group != "" {
		g, err := user.LookupGroup(group)
		if err == nil {
			var gid int
			if gid, err = strconv.Atoi(g.Gid); err == nil {
				err = os.Chown(path, -1, gid) // the process must be a member (SupplementaryGroups)
			}
		}
		if err != nil {
			ln.Close()
			return nil, fmt.Errorf("socket group %q: %w", group, err)
		}
	}
	return ln, nil
}

// ------------------------------------------------------------------ users --

func userCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sap13ns user create|reset-password [flags]")
	}
	sub, args := args[0], args[1:]
	var username, email, role, name, site string
	cfg, log, _, err := setup("user "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&username, "username", "", "username")
		f.StringVar(&site, "site", "", "network: the site (network for the portal's accounts)")
		if sub == "create" {
			f.StringVar(&email, "email", "", "e-mail address")
			f.StringVar(&role, "role", auth.RoleAdmin, "role: admin, editor, author, contributor or subscriber")
			f.StringVar(&name, "name", "", "public name")
		}
	})
	if err != nil {
		return err
	}
	ctx := context.Background()
	t, err := openTarget(ctx, cfg, log, site, 2)
	if err != nil {
		return err
	}
	defer t.close()
	st := t.store
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return errors.New("-username is required")
	}

	switch sub {
	case "create":
		id, pw, generated, err := createUser(ctx, st, username, email, name, role)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "created user %s (#%d, %s)\n", username, id, role)
		if generated {
			fmt.Fprintf(out, "password: %s\n", pw)
		}
	case "reset-password":
		u, err := st.UserByUsername(ctx, username)
		if err != nil {
			return fmt.Errorf("user %q: %w", username, err)
		}
		pw, generated, err := readPassword()
		if err != nil {
			return err
		}
		if err := auth.CheckPasswordPolicy(pw, u.Username, u.Email); err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := st.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		st.DeleteUserSessions(ctx, u.ID, nil)
		st.Audit(ctx, u.ID, "password.reset_cli", u.Username, netipZero, nil)
		fmt.Printf("password changed for %s; all sessions were closed\n", u.Username)
		if generated {
			fmt.Printf("password: %s\n", pw)
		}
	default:
		return fmt.Errorf("unknown user command %q", sub)
	}
	return nil
}

// readPassword reads a password piped on stdin, or generates a strong one
// when stdin is a terminal (it is then printed once).
func readPassword() (string, bool, error) {
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice == 0 {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 1024)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", false, err
		}
		if pw := strings.TrimRight(line, "\r\n"); pw != "" {
			return pw, false, nil
		}
	}
	return auth.NewToken(18), true, nil
}

var netipZero netip.Addr
