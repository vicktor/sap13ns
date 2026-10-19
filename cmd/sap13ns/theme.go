package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"sap13ns/internal/theme"
	"sap13ns/internal/web"
)

const themeUsage = `usage:
  sap13ns theme list [-env FILE] [-site S]
  sap13ns theme install [-env FILE] [-site S] <file.zip>
  sap13ns theme remove [-env FILE] [-site S] <slug>`

// themeCmd implements "sap13ns theme list|install|remove": the themes of a
// site's own directory, validated as an upload in Appearance → Themes is.
// The command line is the operator's: it skips the plan's theme_uploads
// gate and the storage count, which the network's daily measurement
// corrects.
func themeCmd(args []string) error {
	if len(args) == 0 {
		return errors.New(themeUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list", "install", "remove":
	default:
		return errors.New(themeUsage)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(args[1:], args[0]) // the argument may come before the flags
	}
	var siteSlug string
	cfg, log, fs, err := setup("theme "+sub, args, false, func(f *flag.FlagSet) {
		f.StringVar(&siteSlug, "site", "", "network: the site")
	})
	if err != nil {
		return err
	}
	pos := fs.Args()
	switch {
	case sub == "list" && len(pos) != 0, sub != "list" && len(pos) != 1:
		return errors.New(themeUsage)
	}
	t, err := openTarget(ctx, cfg, log, siteSlug, 2)
	if err != nil {
		return err
	}
	defer t.close()
	settings, err := t.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	m, err := web.ThemeManager(t.cfg, uiFS(cfg), log)
	if err != nil {
		return err
	}
	active := m.Active(settings.ActiveTheme).Slug

	switch sub {
	case "list":
		tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "SLUG\tNAME\tVERSION\tSOURCE\tACTIVE")
		for _, th := range m.List() {
			source := "network"
			switch {
			case th.Builtin:
				source = "built-in"
			case th.Uploaded:
				source = "uploaded"
			case th.Own:
				source = "site"
			}
			on := ""
			if th.Slug == active {
				on = "yes"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", th.Slug, termSafe(th.Name), termSafe(th.Version), source, on)
		}
		for _, f := range m.Failures() {
			fmt.Fprintf(tw, "%s\t%s\t\tnot loaded\t\n", f.Slug, termSafe(f.Err))
		}
		return tw.Flush()

	case "install":
		f, err := os.Open(pos[0])
		if err != nil {
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.Size() > theme.MaxArchiveBytes {
			return fmt.Errorf("the archive is larger than %d MB", theme.MaxArchiveBytes>>20)
		}
		res, err := m.Install(ctx, f, fi.Size(), theme.InstallOptions{Source: "cli"})
		if ref, ok := theme.IsRefusal(err); ok {
			var codes []string
			for _, p := range ref.Problems {
				fmt.Fprintln(out, "  "+termSafe(p.Error()))
				if !slices.Contains(codes, p.Code) {
					codes = append(codes, p.Code)
				}
			}
			details := map[string]string{"source": "cli", "file": filepath.Base(pos[0]), "problems": strconv.Itoa(len(ref.Problems)),
				"rules": strings.Join(codes, " ")}
			if res != nil {
				details["sha256"] = res.SHA256
			}
			t.store.Audit(ctx, 0, "theme.refused", ref.Slug, netipZero, details)
			return fmt.Errorf("refused: %d problems; nothing was installed", len(ref.Problems))
		}
		if err != nil {
			return err
		}
		auditAction := "theme.uploaded"
		if res.Replaced {
			auditAction = "theme.updated"
		}
		t.store.Audit(ctx, 0, auditAction, res.Slug, netipZero, map[string]string{"source": "cli", "version": res.Version,
			"previous": res.Previous, "sha256": res.SHA256, "bytes": strconv.FormatInt(res.Bytes, 10), "file": filepath.Base(pos[0])})
		action := "installed"
		if res.Replaced {
			action = "replaced"
			if res.Previous != "" {
				action = "replaced version " + termSafe(res.Previous)
			}
		}
		fmt.Fprintf(out, "%s %s %s (%d bytes). It is not active: preview it with ?preview_theme=%s, then activate it in Admin → Appearance → Themes.\n",
			res.Slug, termSafe(res.Version), action, res.Bytes, res.Slug)
		if res.Slug == active {
			fmt.Fprintln(out, "It is the active theme: a running server shows the new version after Admin → Appearance → Themes is reloaded.")
		}
		return nil

	default: // remove
		slug := strings.TrimSpace(pos[0])
		if slug == settings.ActiveTheme {
			active = slug
		}
		n, err := m.Remove(ctx, slug, active, nil)
		switch {
		case errors.Is(err, theme.ErrActive):
			return fmt.Errorf("%s is the active theme: activate another one first", slug)
		case errors.Is(err, theme.ErrNotOwn):
			return fmt.Errorf("%s is not a theme of the site's own directory (built-in and network themes stay)", termSafe(slug))
		case err != nil:
			return err
		}
		t.store.Audit(ctx, 0, "theme.deleted", slug, netipZero, map[string]string{"source": "cli", "bytes": strconv.FormatInt(n, 10)})
		fmt.Fprintf(out, "removed %s (%d bytes)\n", slug, n)
		return nil
	}
}
