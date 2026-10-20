package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sap13ns/internal/update"
	"sap13ns/internal/web"
)

// updateCmd implements "sap13ns update [-check] [-yes] [-force]". It needs
// no configuration: only the running binary and outbound https to GitHub.
//
// sap13ns-update.service runs it for the admin's "Update now" with -yes,
// -status, -backup and -restart (internal/update/request.go): it backs up
// first, records its progress for the System page and restarts the server
// once installed.
func updateCmd(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	yes := fs.Bool("yes", false, "install without asking for confirmation")
	force := fs.Bool("force", false, "reinstall the latest release even if it is not newer")
	statusDir := fs.String("status", "", "write the progress to status.json in this directory")
	backup := fs.String("backup", "", "comma-separated systemd units to run first, those installed (backups)")
	restart := fs.String("restart", "", "systemd unit to restart after installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	from, to := strings.TrimPrefix(web.Version, "v"), ""
	report := func(st update.Status) {
		if st.To == "" {
			st.To = to // a failure keeps the version it was installing
		}
		to = st.To
		if *statusDir == "" {
			return
		}
		st.From, st.At = from, time.Now().UTC()
		if err := update.WriteStatus(*statusDir, st); err != nil {
			fmt.Fprintln(os.Stderr, "status:", err)
		}
	}
	err := runUpdate(*check, *yes, *force, *backup, *restart, report)
	if err != nil {
		report(update.Status{State: update.StateFailed, Error: err.Error()})
	}
	return err
}

func runUpdate(check, yes, force bool, backup, restart string, report func(update.Status)) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := update.NewHTTPClient()
	rel, newer, err := update.Check(ctx, c, update.DefaultRepo, web.Version)
	if errors.Is(err, update.ErrNoRelease) {
		fmt.Println("no release has been published yet")
		report(update.Status{State: update.StateCurrent})
		return nil
	}
	if err != nil {
		return err
	}
	to := strings.TrimPrefix(rel.Version, "v")
	fmt.Printf("running %s, latest release %s\n", web.Version, to)
	if rel.URL != "" {
		fmt.Println("release notes:", rel.URL)
	}
	if !newer && !force {
		fmt.Println("up to date")
		report(update.Status{State: update.StateCurrent, To: to})
		return nil
	}
	if check {
		return nil
	}
	if update.PublicKey == "" {
		return update.ErrNoPublicKey
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	if !yes {
		fmt.Fprintf(os.Stderr, "Install sap13ns %s over %s? Take a backup first (sap13ns backup create). [y/N] ", to, exe)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	report(update.Status{State: update.StateRunning, To: to})
	for _, unit := range backupUnits(backup, unitInstalled) {
		fmt.Println("backing up:", unit)
		if out, err := exec.CommandContext(ctx, "systemctl", "start", unit).CombinedOutput(); err != nil {
			return fmt.Errorf("the backup (%s) failed, nothing was installed: %v %s", unit, err, strings.TrimSpace(string(out)))
		}
	}
	if err := update.Apply(ctx, c, rel, exe, update.PublicKey, log); err != nil {
		return err
	}
	fmt.Printf("installed %s (previous binary kept as %s.old)\n", to, exe)
	if restart == "" {
		fmt.Println("restart the service to run it: sudo systemctl restart sap13ns")
		report(update.Status{State: update.StateInstalled, To: to})
		return nil
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "try-restart", restart).CombinedOutput(); err != nil {
		return fmt.Errorf("installed %s, but restarting %s failed: %v %s", to, restart, err, strings.TrimSpace(string(out)))
	}
	report(update.Status{State: update.StateInstalled, To: to})
	return nil
}

// unitInstalled reports whether a systemd unit file is installed.
// backupUnits are the units of the -backup list that are installed. A
// network backs up with sap13ns-network-backup.service; a single-site unit
// left from before the server became a network would only fail there
// ("sap13ns backup create" refuses a network), so it is skipped.
func backupUnits(list string, installed func(string) bool) []string {
	var out []string
	network := installed(networkBackupUnit)
	for _, unit := range strings.Split(list, ",") {
		unit = strings.TrimSpace(unit)
		if unit == "" || !installed(unit) || (network && unit == siteBackupUnit) {
			continue
		}
		out = append(out, unit)
	}
	return out
}

const (
	siteBackupUnit    = "sap13ns-backup.service"
	networkBackupUnit = "sap13ns-network-backup.service"
)

func unitInstalled(unit string) bool {
	for _, dir := range []string{"/etc/systemd/system", "/lib/systemd/system", "/usr/lib/systemd/system"} {
		if _, err := os.Stat(filepath.Join(dir, unit)); err == nil {
			return true
		}
	}
	return false
}
