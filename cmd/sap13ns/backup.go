package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"sap13ns/internal/backup"
	"sap13ns/internal/web"
)

// backupCmd implements "sap13ns backup create|list|verify|restore|prune".
func backupCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: sap13ns backup create|list|verify|restore|prune [flags]")
	}
	sub, args := args[0], args[1:]
	var dir, passFile, pgDump, pgRestore string
	var skipDB, skipUploads, force, yes bool
	var daily, weekly, monthly int
	cfg, log, fs, err := setup("backup "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&dir, "dir", "", "backup directory (default $SAP13NS_BACKUP_DIR or <data dir>/backups)")
		f.StringVar(&passFile, "passphrase-file", "", "file holding the archive passphrase")
		f.StringVar(&pgDump, "pg-dump", os.Getenv("SAP13NS_PG_DUMP"), "pg_dump binary")
		f.StringVar(&pgRestore, "pg-restore", os.Getenv("SAP13NS_PG_RESTORE"), "pg_restore binary")
		f.BoolVar(&skipDB, "skip-db", false, "restore: leave the database alone")
		f.BoolVar(&skipUploads, "skip-uploads", false, "restore: leave the uploads alone")
		f.BoolVar(&force, "force", false, "restore over a database that already has tables")
		f.BoolVar(&yes, "yes", false, "restore without asking for confirmation")
		f.IntVar(&daily, "keep-daily", envInt("SAP13NS_BACKUP_KEEP_DAILY", 7), "prune: daily archives to keep")
		f.IntVar(&weekly, "keep-weekly", envInt("SAP13NS_BACKUP_KEEP_WEEKLY", 4), "prune: weekly archives to keep")
		f.IntVar(&monthly, "keep-monthly", envInt("SAP13NS_BACKUP_KEEP_MONTHLY", 12), "prune: monthly archives to keep")
	})
	if err != nil {
		return err
	}
	dir = cmp.Or(dir, os.Getenv("SAP13NS_BACKUP_DIR"), filepath.Join(cfg.DataDir, "backups"))
	pass, err := backupPassphrase(passFile)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		removed, err := backup.Prune(dir, daily, weekly, monthly)
		if err != nil {
			return err
		}
		for _, p := range removed {
			fmt.Println("removed", p)
		}
	default:
		return fmt.Errorf("unknown backup command %q", sub)
	}
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
