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
//	sap13ns version               print the version
//
// Configuration comes from SAP13NS_* environment variables, optionally loaded
// from a file with -env /etc/sap13ns/sap13ns.env.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/auth"
	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/mail"
	"sap13ns/internal/plugin"
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
	case "assistant":
		err = assistantCmd(args)
	case "update":
		err = updateCmd(args)
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
  assistant reindex [-full] [-changed] | status
                                 Rebuild the site assistant's search index, or
                                 show its state (embeddings, pgvector, failures)
  update [-check] [-yes] [-force]
                                 Install the latest signed release over this
                                 binary (see docs/RELEASES.md)
  version                        Print the version
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
	cfg, log, _, err := setup("migrate", args, false, nil)
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	return db.Migrate(ctx, pool, log)
}

// links forwards plugin URL lookups to the web server, which is created
// after the plugin host.
type links struct{ s *web.Server }

func (l *links) PostInfo(p *store.Post) plugin.PostInfo { return l.s.PostInfo(p) }

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

	for _, dir := range []string{cfg.DataDir, cfg.UploadsDir, cfg.PluginsDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	st := store.New(pool)

	var srv *web.Server
	lk := &links{}
	host, err := plugin.NewHost(ctx, cfg.PluginsDir, filepath.Join(cfg.DataDir, "cache", "wasm"), st, log,
		func() plugin.SiteInfo { return srv.SiteInfo() }, lk)
	if err != nil {
		return err
	}
	defer host.Close(context.Background())

	uiFS := fs.FS(ui.FS)
	if cfg.Dev {
		if _, err := os.Stat("ui/admin/templates"); err == nil {
			uiFS = os.DirFS("ui") // live templates and assets while developing
		}
	}
	mailer := mail.New(cfg.SMTP, log, cfg.Dev)
	host.SetSecrets(auth.NewKeys(cfg.SecretKey)) // secret plugin settings, encrypted at rest
	host.SetMailer(mailer)                       // the mail_send host function
	host.SetBlocklistFile(filepath.Join(cfg.DataDir, "plugin-blocklist.txt"))
	srv, err = web.New(ctx, web.Deps{Config: cfg, Log: log, Store: st, UI: uiFS, Plugins: host, Mailer: mailer})
	if err != nil {
		return err
	}
	lk.s = srv
	if err := host.Load(ctx); err != nil {
		log.Error("load plugins", "err", err)
	}
	go srv.Background(ctx)
	checker := update.NewChecker(web.Version, nil, log)
	srv.SetUpdateChecker(checker)
	go checker.Run(ctx)

	ln, err := listen(cfg.Listen, cfg.SocketGroup)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // large uploads on slow links
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("sap13ns listening", "addr", cfg.Listen, "url", cfg.BaseURL.String(), "version", web.Version)
		errc <- hs.Serve(ln)
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := hs.Shutdown(sctx); err != nil {
			log.Error("shutdown", "err", err)
		}
		if err := srv.Close(sctx); err != nil {
			log.Error("analytics final flush", "err", err)
		}
	}
	return nil
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
	var username, email, role, name string
	cfg, log, _, err := setup("user "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&username, "username", "", "username")
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
	pool, err := db.Open(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	st := store.New(pool)
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return errors.New("-username is required")
	}

	switch sub {
	case "create":
		if !auth.ValidRole(role) {
			return fmt.Errorf("invalid role %q", role)
		}
		if email == "" {
			return errors.New("-email is required")
		}
		pw, generated, err := readPassword()
		if err != nil {
			return err
		}
		if err := auth.CheckPasswordPolicy(pw, username, email); err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		id, err := st.CreateUser(ctx, store.NewUser{Username: username, Email: strings.ToLower(email), DisplayName: name, Role: role, PasswordHash: hash})
		if err != nil {
			return err
		}
		st.Audit(ctx, id, "user.created_cli", username, netipZero, map[string]string{"role": role})
		fmt.Printf("created user %s (#%d, %s)\n", username, id, role)
		if generated {
			fmt.Printf("password: %s\n", pw)
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
