package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"sap13ns/internal/update"
	"sap13ns/internal/web"
)

// updateCmd implements "sap13ns update [-check] [-yes] [-force]". It needs
// no configuration: only the running binary and outbound https to GitHub.
func updateCmd(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only report whether a newer release exists")
	yes := fs.Bool("yes", false, "install without asking for confirmation")
	force := fs.Bool("force", false, "reinstall the latest release even if it is not newer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := update.NewHTTPClient()
	rel, newer, err := update.Check(ctx, c, update.DefaultRepo, web.Version)
	if errors.Is(err, update.ErrNoRelease) {
		fmt.Println("no release has been published yet")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("running %s, latest release %s\n", web.Version, strings.TrimPrefix(rel.Version, "v"))
	if rel.URL != "" {
		fmt.Println("release notes:", rel.URL)
	}
	if !newer && !*force {
		fmt.Println("up to date")
		return nil
	}
	if *check {
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
	if !*yes {
		fmt.Fprintf(os.Stderr, "Install sap13ns %s over %s? Take a backup first (sap13ns backup create). [y/N] ",
			strings.TrimPrefix(rel.Version, "v"), exe)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	if err := update.Apply(ctx, c, rel, exe, update.PublicKey, log); err != nil {
		return err
	}
	fmt.Printf("installed %s (previous binary kept as %s.old)\nrestart the service to run it: sudo systemctl restart sap13ns\n",
		strings.TrimPrefix(rel.Version, "v"), exe)
	return nil
}
