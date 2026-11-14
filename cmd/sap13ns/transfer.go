package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/config"
	"sap13ns/internal/db"
	"sap13ns/internal/network"
	"sap13ns/internal/transfer"
	"sap13ns/internal/web"
)

const transferUsage = `usage: sap13ns transfer export|inspect|import [flags]
  export [-site S] [-o FILE] [-network-ai] [-no-smtp]
                   the whole site, its files and secrets, in one encrypted archive
  inspect ARCHIVE  check an archive and say what it holds
  import [-replace] [-yes] ARCHIVE
                   on a new single-site install, server stopped: load the
                   site and put its files in place
The passphrase comes from -passphrase-file, $SAP13NS_BACKUP_PASSPHRASE_FILE or
$SAP13NS_BACKUP_PASSPHRASE (at least 12 characters). docs/MULTISITE.md, "Moving
a site out of the network", has the steps.`

// transferCmd implements "sap13ns transfer export|inspect|import".
func transferCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(transferUsage)
	}
	sub, args := args[0], args[1:]
	var site, output, passFile string
	var networkAI, noSMTP, replace, yes bool
	cfg, log, fs, err := setup("transfer "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&passFile, "passphrase-file", "", "file holding the archive passphrase")
		switch sub {
		case "export":
			f.StringVar(&site, "site", "", "network: the site")
			f.StringVar(&output, "o", "", "the archive (default <site>-<date>.transfer.enc here)")
			f.BoolVar(&networkAI, "network-ai", false, "network: carry the network's AI provider and keys, for a site whose AI came from its plan")
			f.BoolVar(&noSMTP, "no-smtp", false, "leave out the server's SMTP settings (the site's own, Settings → E-mail, always travel)")
		case "import":
			f.BoolVar(&replace, "replace", false, "replace the site this install already has (its directories are kept, renamed)")
			f.BoolVar(&yes, "yes", false, "import without asking for confirmation")
		}
	})
	if err != nil {
		return err
	}
	pass, err := backupPassphrase(passFile)
	if err != nil {
		return err
	}
	if pass == "" {
		return errors.New("a transfer archive is always encrypted: set SAP13NS_BACKUP_PASSPHRASE_FILE or -passphrase-file")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch sub {
	case "export":
		return transferExport(ctx, cfg, log, site, output, pass, networkAI, noSMTP)
	case "inspect", "import":
		if fs.NArg() != 1 {
			return errors.New(transferUsage)
		}
		envFile := fs.Lookup("env").Value.String()
		return transferImport(ctx, cfg, log, fs.Arg(0), pass, envFile, sub == "inspect", replace, yes)
	}
	return fmt.Errorf("unknown transfer command %q\n%s", sub, transferUsage)
}

func transferExport(ctx context.Context, cfg *config.Config, log *slog.Logger, site, output, pass string, networkAI, noSMTP bool) error {
	if !cfg.Multisite && len(cfg.SecretKey) == 0 {
		return errors.New("SAP13NS_SECRET_KEY is not set: the site's encrypted settings could not be read where it goes")
	}
	if cfg.Multisite && len(cfg.SecretKey) == 0 {
		return errors.New("SAP13NS_SECRET_KEY is required to open a site of the network")
	}
	t, err := openTarget(ctx, cfg, log, site, 4)
	if err != nil {
		return err
	}
	defer t.close()
	if t.site != nil && t.site.Kind == network.KindNetwork {
		return errors.New("the portal is the network itself: move a site, not the portal")
	}
	src, ok := t.store.DB.(transfer.DB)
	if !ok {
		return errors.New("transfer: the site's database handle cannot open a snapshot")
	}
	set, err := t.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	o := transfer.ExportOptions{
		Source: transfer.Source{URL: t.cfg.BaseURL.String(), Title: set.SiteTitle, Label: t.cfg.BaseURL.Hostname(),
			RPID: cmp.Or(t.cfg.PasskeyRPID, t.cfg.BaseURL.Hostname())},
		SecretKey: t.cfg.SecretKey, UploadsDir: t.cfg.UploadsDir, PluginsDir: t.cfg.PluginsDir, ThemesDir: t.cfg.ThemesDir,
		NetworkThemesDir: t.cfg.NetworkThemesDir, Passphrase: pass, AppVersion: web.Version, Log: log,
	}
	if t.site != nil {
		o.Source.Label, o.Source.SiteID, o.Source.Network = t.site.Slug, t.site.ID, cfg.BaseURL.Hostname()
		ds, err := t.net.Domains(ctx, t.site.ID)
		if err != nil {
			return err
		}
		for _, d := range ds {
			o.Source.Domains = append(o.Source.Domains, d.Host)
		}
	}
	if s := t.cfg.SMTP; s.Enabled() && !noSMTP {
		o.SMTP = &transfer.SMTP{Host: s.Host, Port: s.Port, Username: s.Username, Password: s.Password, From: s.From}
	}
	if networkAI {
		n := cfg.NetworkAI
		if !n.Enabled() {
			return errors.New("-network-ai: the network has no AI provider (SAP13NS_NETWORK_AI_PROVIDER)")
		}
		o.NetworkAI = &transfer.NetworkAI{Provider: n.Provider, APIKey: n.APIKey, BaseURL: n.BaseURL, TextModel: n.TextModel,
			ImageProvider: n.ImageProvider, ImageAPIKey: n.ImageAPIKey, ImageBaseURL: n.ImageBaseURL, ImageModel: n.ImageModel}
	}
	if output == "" {
		output = o.Source.Label + "-" + time.Now().UTC().Format("20060102-150405") + ".transfer.enc"
	}
	if _, err := os.Lstat(output); err == nil {
		return fmt.Errorf("%s exists", output)
	}
	o.TmpDir = filepath.Dir(output)
	tmp, err := os.CreateTemp(o.TmpDir, ".sap13ns-transfer-*.tmp")
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	w := bufio.NewWriterSize(tmp, 1<<20)
	m, err := transfer.Export(ctx, src, w, o)
	if err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	size, _ := tmp.Seek(0, io.SeekCurrent)
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), output); err != nil {
		return err
	}
	done = true
	var rows int64
	for _, tb := range m.Tables {
		rows += tb.Rows
	}
	fmt.Fprintf(out, "%s (%s)\n  %s: %d tables, %d rows, %d files", output, humanBytes(size), m.Source.URL, len(m.Tables), rows, len(m.Files))
	if len(m.NetworkThemes) > 0 {
		fmt.Fprintf(out, ", the network's themes %s", strings.Join(m.NetworkThemes, ", "))
	}
	fmt.Fprintln(out)
	if m.SMTP != nil {
		fmt.Fprintf(out, "  SMTP: %s:%d\n", m.SMTP.Host, m.SMTP.Port)
	}
	if ai, err := t.store.AISettings(ctx); err == nil && ai.Source == "network" && m.NetworkAI == nil {
		fmt.Fprintln(out, "  the site's AI comes from the network: add -network-ai to carry it, or set up a provider after the move")
	}
	fmt.Fprintln(out, "The archive holds the site's secret key and its users' password hashes: keep it, and its passphrase, private.")
	fmt.Fprintln(out, "Next, on the new server: sap13ns transfer import -env /etc/sap13ns/sap13ns.env", filepath.Base(output))
	return nil
}

func transferImport(ctx context.Context, cfg *config.Config, log *slog.Logger, file, pass, envFile string, inspect, replace, yes bool) error {
	if !inspect && cfg.Multisite {
		return errors.New("a transfer archive is imported into a single-site install (SAP13NS_MULTISITE unset)")
	}
	if !inspect && len(cfg.ServerKey) == 0 {
		return errors.New("SAP13NS_SECRET_KEY is required: it seals the key of the site imported (generate one with sap13ns keygen)")
	}
	stage := cfg.DataDir
	if inspect {
		stage = os.TempDir()
	}
	staged, err := transfer.Open(ctx, file, pass, stage)
	if err != nil {
		return err
	}
	defer staged.Close()
	m := staged.Manifest
	var rows int64
	for _, t := range m.Tables {
		rows += t.Rows
	}
	from := "a single-site install"
	if m.Source.Network != "" {
		from = "the network " + m.Source.Network
	}
	fmt.Fprintf(out, "%s: %s (%s) from %s, made %s by sap13ns %s\n  %d tables, %d rows, %d files, %s\n", filepath.Base(file),
		cmp.Or(m.Source.Title, m.Source.Label), m.Source.URL, from, m.Created.Format("2006-01-02 15:04 MST"), m.AppVersion,
		len(m.Tables), rows, len(m.Files), humanBytes(m.TotalBytes))
	if len(m.Source.Domains) > 0 {
		fmt.Fprintf(out, "  domains: %s\n", strings.Join(m.Source.Domains, ", "))
	}
	if m.SMTP != nil {
		fmt.Fprintf(out, "  SMTP: %s:%d, from %s\n", m.SMTP.Host, m.SMTP.Port, m.SMTP.From)
	}
	if inspect {
		fmt.Fprintln(out, "ok: the archive is complete")
		return nil
	}

	if !yes {
		fmt.Fprintf(os.Stderr, "This loads the site into %s and its files into %s, replacing what is there.\n"+
			"Stop sap13ns first (systemctl stop sap13ns).\nType the site's name (%s) to continue: ", dbName(cfg.DatabaseURL), cfg.DataDir, m.Source.Label)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != m.Source.Label {
			return errors.New("cancelled")
		}
	}
	pool, err := db.Open(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		return err
	}
	defer pool.Close()
	rep, err := staged.Apply(ctx, pool, transfer.ApplyOptions{UploadsDir: cfg.UploadsDir, PluginsDir: cfg.PluginsDir,
		ThemesDir: cfg.ThemesDir, ServerKey: cfg.ServerKey, Replace: replace, Log: log})
	if errors.Is(err, transfer.ErrNotEmpty) {
		return fmt.Errorf("%w: add -replace to replace it (its directories are kept, renamed)", err)
	}
	if err != nil {
		return err
	}
	if err := db.Migrate(ctx, pool, log); err != nil {
		return fmt.Errorf("the site is loaded, but the migrations of this version failed: %w", err)
	}
	fmt.Fprintf(out, "imported %d rows and %d files\n", rep.Rows, rep.Files)
	for _, d := range rep.MovedAside {
		fmt.Fprintln(out, "  what was there is in", d)
	}

	fmt.Fprintln(out, "  the site keeps its own secret key, sealed in the database with SAP13NS_SECRET_KEY: keep that key as it is")
	if rep.SMTP {
		fmt.Fprintf(out, "  mail: the site sends through %s now (Settings → E-mail)\n", m.SMTP.Host)
	}

	if src, err := hostOf(m.Source.URL); err == nil && src != cfg.BaseURL.Hostname() {
		fmt.Fprintf(out, "note: the site was at %s and is now at %s (SAP13NS_BASE_URL): links written with the old address keep pointing there", src, cfg.BaseURL.Hostname())
		if m.Source.RPID != cfg.BaseURL.Hostname() {
			fmt.Fprintf(out, ", and passkeys made for %s do not work here (users sign in with their password and add one again)", m.Source.RPID)
		}
		fmt.Fprintln(out)
	}
	if len(rep.Operators) > 0 {
		fmt.Fprintf(out, "disabled the accounts of the network's administrators: %s\n", strings.Join(rep.Operators, ", "))
	}
	if len(rep.SSOUsers) > 0 {
		fmt.Fprintln(out, "these users signed in through the network and have no password here; give them one with")
		for _, u := range rep.SSOUsers {
			fmt.Fprintf(out, "  sap13ns user reset-password -env %s -username %s    # %s, %s\n", cmp.Or(envFile, "FILE"), u.Username, u.Role, u.Email)
		}
		fmt.Fprintln(out, "or let them use \"Forgot your password?\" once mail works.")
	}
	for _, w := range rep.Warnings {
		fmt.Fprintln(out, "warning:", w)
	}
	fmt.Fprintln(out, "start sap13ns (systemctl start sap13ns), and point the domain's DNS to this server")
	return nil
}

func hostOf(u string) (string, error) {
	p, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	return p.Hostname(), nil
}

// dbName is the database of a URL, without its credentials.
func dbName(dsn string) string {
	if i := strings.LastIndex(dsn, "/"); i >= 0 {
		name, _, _ := strings.Cut(dsn[i+1:], "?")
		if name != "" {
			return "the database " + name
		}
	}
	return "the database"
}
