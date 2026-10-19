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
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"sap13ns/internal/backup"
	"sap13ns/internal/config"
	"sap13ns/internal/network"
	"sap13ns/internal/sitebackup"
	"sap13ns/internal/web"
)

// The two formats of `sap13ns backup`:
//
//   - full: the whole database (pg_dump) and the uploads, in one tar.gz.
//     Everything comes back, users' passwords and sessions included, so it
//     stays the default of a single site for disaster recovery. A network
//     cannot use it: one archive would hold every site.
//   - site: the per-site archive of internal/sitebackup, a .zip that
//     WordPress imports and that sap13ns restores into a running site.
//     It is chosen with -site (a network), with -format site (a single
//     site), and found by itself when an archive is verified or restored.
const (
	formatFull = "full"
	formatSite = "site"
)

const backupUsage = `usage: sap13ns backup create|list|verify|restore|prune|decrypt [flags]
  create [-format full|site] [-site S] [-wp-base URL]
  list [-format full|site] [-site S]
  verify [-site S] ARCHIVE
  restore [-site S] [-replace] [-yes] ARCHIVE
  prune [-format full|site] [-site S] [-keep-daily N] [-keep-weekly N]
  decrypt [-o OUT] ARCHIVE.enc`

// backupCmd implements "sap13ns backup create|list|verify|restore|prune|decrypt".
func backupCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(backupUsage)
	}
	sub, args := args[0], args[1:]
	var dir, passFile, pgDump, pgRestore, site, format, wpBase, output string
	var skipDB, skipUploads, force, yes, replace bool
	var daily, weekly, monthly int
	cfg, log, fs, err := setup("backup "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&dir, "dir", "", "backup directory (default $SAP13NS_BACKUP_DIR or <data dir>/backups; for -format site, the site's own)")
		f.StringVar(&passFile, "passphrase-file", "", "file holding the archive passphrase")
		f.StringVar(&format, "format", "", "full (database and uploads) or site (one site, WordPress-compatible); default site with -site, full otherwise")
		f.StringVar(&site, "site", "", "network: the site (implies -format site)")
		f.StringVar(&wpBase, "wp-base", "", "create -format site: the address of the WordPress site the content moves to (media listed at URL/wp-content/uploads/)")
		f.BoolVar(&replace, "replace", false, "restore -format site: replace the content the site has (a backup of it is taken first)")
		f.StringVar(&output, "o", "", "decrypt: the output file (default: the archive's name without .enc)")
		f.StringVar(&pgDump, "pg-dump", os.Getenv("SAP13NS_PG_DUMP"), "pg_dump binary")
		f.StringVar(&pgRestore, "pg-restore", os.Getenv("SAP13NS_PG_RESTORE"), "pg_restore binary")
		f.BoolVar(&skipDB, "skip-db", false, "restore -format full: leave the database alone")
		f.BoolVar(&skipUploads, "skip-uploads", false, "restore -format full: leave the uploads alone")
		f.BoolVar(&force, "force", false, "restore -format full over a database that already has tables; decrypt over an existing file")
		f.BoolVar(&yes, "yes", false, "restore without asking for confirmation")
		f.IntVar(&daily, "keep-daily", -1, "prune: daily archives to keep (default $SAP13NS_BACKUP_KEEP_DAILY, or $SAP13NS_SITE_BACKUP_KEEP_DAILY for -format site; 7)")
		f.IntVar(&weekly, "keep-weekly", -1, "prune: weekly archives to keep (default $SAP13NS_BACKUP_KEEP_WEEKLY, or $SAP13NS_SITE_BACKUP_KEEP_WEEKLY; 4)")
		f.IntVar(&monthly, "keep-monthly", envInt("SAP13NS_BACKUP_KEEP_MONTHLY", 12), "prune -format full: monthly archives to keep")
	})
	if err != nil {
		return err
	}
	if format == "" {
		format = formatFull
		if site != "" || (sub == "verify" || sub == "restore") && fs.NArg() == 1 && isSiteArchive(fs.Arg(0)) {
			format = formatSite
		}
	}
	if err := backupScope(cfg, site, sub, format); err != nil {
		return err
	}
	pass, err := backupPassphrase(passFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if sub == "decrypt" {
		return decryptArchive(fs, pass, output, force)
	}
	if format == formatSite {
		return siteBackupCmd(ctx, cfg, log, sub, fs, siteBackupArgs{dir: dir, site: site, pass: pass, wpBase: wpBase,
			replace: replace, yes: yes, daily: daily, weekly: weekly})
	}

	dir = cmp.Or(dir, cfg.SiteBackups.Root) // $SAP13NS_BACKUP_DIR or <data dir>/backups
	switch sub {
	case "create":
		path, m, err := backup.Create(ctx, backup.Options{DatabaseURL: cfg.DatabaseURL, UploadsDir: cfg.UploadsDir,
			OutDir: dir, Passphrase: pass, AppVersion: web.Version, PgDump: pgDump, Log: log})
		if err != nil {
			return err
		}
		fmt.Printf("%s\n  database %s (PostgreSQL %s), %d files, %s\n", path, m.Database, m.PGVersion, m.FileCount, humanBytes(m.TotalBytes))
		if pass == "" {
			fmt.Fprintln(os.Stderr, "warning: the archive is not encrypted; set SAP13NS_BACKUP_PASSPHRASE_FILE to protect it")
		}
	case "list":
		list, err := backup.List(dir)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tCREATED (UTC)\tSIZE\tENCRYPTED\tAPP\tPOSTGRESQL\tFILES")
		for _, e := range list {
			app, pg, files := "?", "?", "?"
			if e.Manifest != nil {
				app, pg, files = e.Manifest.AppVersion, e.Manifest.PGVersion, strconv.Itoa(e.Manifest.FileCount)
			} else if e.Err != nil {
				app = "error: " + e.Err.Error()
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\t%s\t%s\n", e.Name, e.Created.Format("2006-01-02 15:04:05"), humanBytes(e.Size), e.Encrypted, app, pg, files)
		}
		tw.Flush()
	case "verify":
		if fs.NArg() != 1 {
			return errors.New("usage: sap13ns backup verify [-passphrase-file F] ARCHIVE")
		}
		m, err := backup.Verify(fs.Arg(0), pass)
		if err != nil {
			return err
		}
		fmt.Printf("ok: created %s by sap13ns %s; database %s (PostgreSQL %s, %s); %d files\n",
			m.Created.Format("2006-01-02 15:04:05 MST"), m.AppVersion, m.Database, m.PGVersion, humanBytes(m.DBSize), m.FileCount)
	case "restore":
		if fs.NArg() != 1 {
			return errors.New("usage: sap13ns backup restore [-passphrase-file F] [-skip-db] [-skip-uploads] [-force] [-yes] ARCHIVE")
		}
		if !yes {
			fmt.Fprintf(os.Stderr, "Restoring %s replaces the current %s. Stop sap13ns first (systemctl stop sap13ns).\nType \"restore\" to continue: ",
				fs.Arg(0), map[bool]string{true: "uploads", false: "database and uploads"}[skipDB])
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != "restore" {
				return errors.New("cancelled")
			}
		}
		if err := backup.Restore(ctx, fs.Arg(0), backup.RestoreOptions{DatabaseURL: cfg.DatabaseURL, UploadsDir: cfg.UploadsDir,
			Passphrase: pass, SkipDB: skipDB, SkipUploads: skipUploads, Force: force, PgRestore: pgRestore, Log: log}); err != nil {
			return err
		}
		fmt.Println("restored; migrations run on the next start (systemctl start sap13ns)")
	case "prune":
		if daily < 0 {
			daily = envInt("SAP13NS_BACKUP_KEEP_DAILY", 7)
		}
		if weekly < 0 {
			weekly = envInt("SAP13NS_BACKUP_KEEP_WEEKLY", 4)
		}
		removed, err := backup.Prune(dir, daily, weekly, monthly)
		if err != nil {
			return err
		}
		for _, p := range removed {
			fmt.Println("removed", p)
		}
	default:
		return fmt.Errorf("unknown backup command %q\n%s", sub, backupUsage)
	}
	return nil
}

// backupScope refuses what a format cannot do where it runs. A full
// archive holds one database and one uploads directory: on a network that
// would be every schema with the network's directory only, and a restore
// would replace every site, so a network backs up one site at a time.
// Full archives that exist can still be listed, verified and pruned.
func backupScope(cfg *config.Config, site, sub, format string) error {
	switch {
	case !cfg.Multisite && site != "":
		return errNotNetwork
	case format != formatFull && format != formatSite:
		return fmt.Errorf("-format must be %s or %s, got %q", formatFull, formatSite, format)
	case format == formatFull && site != "":
		return errors.New("-site makes a per-site archive: leave -format out, or use -format site")
	case cfg.Multisite && format == formatFull && (sub == "create" || sub == "restore"):
		return errors.New("this is a network: back up and restore one site at a time with -site SLUG (docs/MULTISITE.md, Backups); " +
			"to save the whole network, back up the database with pg_dump and the data directory")
	case cfg.Multisite && format == formatSite && site == "" && sub != "verify" && sub != "decrypt":
		return errors.New("this is a network: name the site with -site SLUG")
	}
	return nil
}

// isSiteArchive tells a per-site archive from a full one: by its name, or
// for a renamed file by its first bytes (a .zip; full archives are gzip).
// An encrypted archive with another name is taken as a full one; -format
// site says otherwise.
func isSiteArchive(file string) bool {
	if _, ok := sitebackup.ParseName(filepath.Base(file)); ok {
		return true
	}
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 4)
	_, err = io.ReadFull(f, head)
	return err == nil && string(head) == "PK\x03\x04"
}

type siteBackupArgs struct {
	dir, site, pass, wpBase string
	replace, yes            bool
	daily, weekly           int
}

// siteBackupCmd runs a backup command on the per-site archives of a site:
// the single site, or the one named by -site on a network.
func siteBackupCmd(ctx context.Context, cfg *config.Config, log *slog.Logger, sub string, fs *flag.FlagSet, a siteBackupArgs) error {
	if sub == "verify" {
		if fs.NArg() != 1 {
			return errors.New("usage: sap13ns backup verify [-passphrase-file F] ARCHIVE")
		}
		// An encrypted archive is decrypted next to it to be read.
		m, err := sitebackup.Verify(fs.Arg(0), a.pass, cmp.Or(a.dir, filepath.Dir(fs.Arg(0))))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "ok: backup of %s (%s) made %s by sap13ns %s; %d posts, %d pages, %d files, %s\n", m.Site.Label, m.Site.URL,
			m.Created.Format("2006-01-02 15:04:05 MST"), m.AppVersion, m.Counts.Posts, m.Counts.Pages, m.Counts.Files, humanBytes(m.TotalBytes))
		return nil
	}
	wpBase, err := sitebackup.CheckWPBase(a.wpBase)
	if err != nil {
		return err
	}
	if wpBase != "" && sub != "create" {
		return errors.New("-wp-base is for backup create")
	}
	if a.replace && sub != "restore" {
		return errors.New("-replace is for backup restore")
	}
	if cfg.Multisite && len(cfg.SecretKey) == 0 {
		return errors.New("SAP13NS_SECRET_KEY is required to open a site of the network")
	}
	t, err := openTarget(ctx, cfg, log, a.site, 4)
	if err != nil {
		return err
	}
	defer t.close()
	o, err := siteBackupOptions(ctx, cfg, t, a.dir, a.pass, wpBase)
	if err != nil {
		return err
	}
	keepDaily, keepWeekly := a.daily, a.weekly
	if keepDaily < 0 {
		keepDaily = cfg.SiteBackups.KeepDaily
	}
	if keepWeekly < 0 {
		keepWeekly = cfg.SiteBackups.KeepWeekly
	}
	prune := func() error {
		removed, err := sitebackup.Prune(o.Dir, keepDaily, keepWeekly, time.Now())
		for _, p := range removed {
			fmt.Fprintln(out, "removed", p)
		}
		return err
	}

	switch sub {
	case "create":
		arch, m, err := sitebackup.Create(ctx, t.store, o)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\n  %d posts, %d pages, %d media items, %d comments, %d users, %d files, %s\n", arch.Path, m.Counts.Posts,
			m.Counts.Pages, m.Counts.Attachments, m.Counts.Comments, m.Counts.Users, m.Counts.Files, humanBytes(arch.Size))
		if wpBase != "" {
			fmt.Fprintf(out, "  media listed at %s/wp-content/uploads/ (README.txt inside has the steps)\n", wpBase)
		}
		if a.pass == "" {
			fmt.Fprintln(os.Stderr, "warning: the archive is not encrypted; set SAP13NS_BACKUP_PASSPHRASE_FILE to protect it")
		}
		// As with the database backups, prune is a command of its own
		// (sap13ns-backup.service runs it after create), so that an
		// archive made by hand is not pruned away by the next one.
		return nil
	case "list":
		list, err := sitebackup.List(o.Dir)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tCREATED (UTC)\tSIZE\tENCRYPTED\tKIND")
		for _, b := range list {
			kind := "backup"
			if b.PreRestore {
				kind = "before a restore"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\n", b.Name, b.Created.Format("2006-01-02 15:04:05"), humanBytes(b.Size), b.Encrypted, kind)
		}
		tw.Flush()
		if s, err := sitebackup.ReadStatus(o.Dir); err == nil && !s.Tried.IsZero() {
			fmt.Fprintf(out, "last attempt %s: %s", s.Tried.Format("2006-01-02 15:04:05 MST"), s.Result)
			if s.Error != "" {
				fmt.Fprintf(out, " (%s)", s.Error)
			}
			fmt.Fprintln(out)
		}
		fmt.Fprintln(out, "directory", o.Dir)
		return nil
	case "prune":
		return prune()
	case "restore":
		if fs.NArg() != 1 {
			return errors.New("usage: sap13ns backup restore [-site S] [-replace] [-yes] [-passphrase-file F] ARCHIVE")
		}
		if t.site != nil && t.site.Kind == network.KindNetwork {
			return errors.New("the portal holds the network's accounts: restore it from a backup of the database, not from a site archive")
		}
		file := fs.Arg(0)
		if !a.yes {
			fmt.Fprintf(os.Stderr, "Restoring %s replaces the content of %s: posts, pages, media, comments, menus, widgets, designs, "+
				"redirects and settings. A backup of what it replaces is taken first.\nType the site's name (%s) to continue: ",
				file, o.Site.URL, o.Site.Label)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != o.Site.Label {
				return errors.New("cancelled")
			}
		}
		rep, err := sitebackup.Restore(ctx, t.store, file, sitebackup.RestoreOptions{Options: o, Replace: a.replace,
			CheckRedirect: web.CheckRestoredRedirect})
		if errors.Is(err, sitebackup.ErrNotEmpty) {
			return errors.New("the site has content: add -replace to replace it (a backup of it is taken first)")
		}
		if err != nil {
			return err
		}
		details := map[string]string{"archive": filepath.Base(file), "made": rep.Manifest.Created.Format(time.RFC3339)}
		if rep.Safety != nil {
			details["safety_backup"] = rep.Safety.Name
		}
		if err := t.store.Audit(ctx, 0, "backup.restored_cli", o.Site.Label, netipZero, details); err != nil {
			log.Error("audit the restore", "err", err)
		}
		printRestore(rep)
		if t.net != nil {
			if err := t.net.SiteRestored(ctx, t.site.ID); err != nil {
				fmt.Fprintln(os.Stderr, "warning: the running servers were not told; they pick the content up when the site reloads:", err)
			}
		} else {
			fmt.Fprintln(out, "restart sap13ns (systemctl restart sap13ns) so that a running server reloads the settings and caches")
		}
		return nil
	}
	return fmt.Errorf("unknown backup command %q\n%s", sub, backupUsage)
}

// siteBackupOptions are the options of a site's backups: its directory,
// uploads, address and name.
func siteBackupOptions(ctx context.Context, cfg *config.Config, t *target, dir, pass, wpBase string) (sitebackup.Options, error) {
	set, err := t.store.LoadSettings(ctx)
	if err != nil {
		return sitebackup.Options{}, err
	}
	o := sitebackup.Options{Dir: cmp.Or(dir, t.cfg.SiteBackups.Dir), UploadsDir: t.cfg.UploadsDir, Passphrase: pass, WPBase: wpBase,
		AppVersion: web.Version, Site: sitebackup.Site{URL: t.cfg.BaseURL.String(), Title: set.SiteTitle, Label: t.cfg.BaseURL.Hostname()}}
	if t.site != nil {
		o.Site.ID, o.Site.Label, o.Site.Network = t.site.ID, t.site.Slug, cfg.BaseURL.Hostname()
	}
	return o, nil
}

func printRestore(rep *sitebackup.RestoreReport) {
	m := rep.Manifest
	fmt.Fprintf(out, "restored the backup of %s made %s: %d posts, %d pages, %d media items, %d comments, %d categories and tags\n",
		m.Site.URL, m.Created.Format("2006-01-02 15:04:05 MST"), rep.Posts, rep.Pages, rep.Media, rep.Comments, rep.Terms)
	fmt.Fprintf(out, "  users: %d matched, %d created (they set a password with \"forgot password\"); %d menu items, %d widgets, %d designs, %d redirects",
		rep.UsersMatched, rep.UsersCreated, rep.MenuItems, rep.Widgets, rep.Designs, rep.Redirects)
	if rep.RedirectsRefused > 0 {
		fmt.Fprintf(out, " (%d refused)", rep.RedirectsRefused)
	}
	fmt.Fprintln(out)
	if rep.Safety != nil {
		fmt.Fprintln(out, "  what it replaced is in", rep.Safety.Path)
	}
	for _, w := range rep.Warnings {
		fmt.Fprintln(out, "  warning:", w)
	}
}

// decryptArchive writes the plaintext of an encrypted archive, of either
// format. The output appears only once the whole archive is authenticated.
func decryptArchive(fs *flag.FlagSet, pass, output string, force bool) error {
	if fs.NArg() != 1 {
		return errors.New("usage: sap13ns backup decrypt [-passphrase-file F] [-o OUT] ARCHIVE.enc")
	}
	src := fs.Arg(0)
	if output == "" {
		if !strings.HasSuffix(src, backup.EncryptedSuffix) {
			return errors.New("name the output with -o")
		}
		output = strings.TrimSuffix(src, backup.EncryptedSuffix)
	}
	if pass == "" {
		return errors.New("decrypting needs the passphrase: set SAP13NS_BACKUP_PASSPHRASE_FILE or -passphrase-file")
	}
	if _, err := os.Lstat(output); err == nil && !force {
		return fmt.Errorf("%s exists; add -force to replace it", output)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	br := bufio.NewReader(in)
	if !backup.IsEncrypted(br) {
		return fmt.Errorf("%s is not encrypted", src)
	}
	plain, err := backup.Decrypt(br, pass)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(output), ".sap13ns-decrypt-*")
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	n, err := io.Copy(tmp, plain)
	if err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), output); err != nil {
		return err
	}
	ok = true
	fmt.Fprintf(out, "%s (%s)\n", output, humanBytes(n))
	return nil
}

// backupPassphrase reads the passphrase from a flag-named file, then
// SAP13NS_BACKUP_PASSPHRASE_FILE, then SAP13NS_BACKUP_PASSPHRASE. It is
// never taken from a flag value, which other users could read with ps.
func backupPassphrase(file string) (string, error) {
	file = cmp.Or(file, os.Getenv("SAP13NS_BACKUP_PASSPHRASE_FILE"))
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("passphrase file: %w", err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return os.Getenv("SAP13NS_BACKUP_PASSPHRASE"), nil
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
